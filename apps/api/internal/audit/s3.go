package audit

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// S3Client talks to MinIO or any S3-compatible bucket with path-style URLs.
// It is the same code path for the on-prem bucket and the off-site bucket.
type S3Client struct {
	Endpoint  string
	Region    string
	AccessKey string
	SecretKey string
	Bucket    string
	HTTP      *http.Client
}

// NewS3Client builds a path-style client. region defaults to us-east-1, which MinIO accepts.
func NewS3Client(endpoint, region, accessKey, secretKey, bucket string) *S3Client {
	if region == "" {
		region = "us-east-1"
	}
	return &S3Client{
		Endpoint:  strings.TrimRight(endpoint, "/"),
		Region:    region,
		AccessKey: accessKey,
		SecretKey: secretKey,
		Bucket:    bucket,
		HTTP:      &http.Client{Timeout: 30 * time.Second},
	}
}

// PutCompliance writes the object with a compliance-mode retain-until and refuses to overwrite.
func (c *S3Client) PutCompliance(ctx context.Context, key string, body []byte, retainUntil time.Time) error {
	headers := map[string]string{
		"if-none-match":                       "*",
		"x-amz-object-lock-mode":              "COMPLIANCE",
		"x-amz-object-lock-retain-until-date": retainUntil.UTC().Format(time.RFC3339),
	}
	status, _, resp, err := c.do(ctx, http.MethodPut, key, nil, body, headers)
	if err != nil {
		return err
	}
	if status == http.StatusPreconditionFailed || status == http.StatusConflict || status == http.StatusForbidden {
		return fmt.Errorf("%w: put %s: %s", ErrImmutableObject, key, snippet(resp))
	}
	if status < 200 || status >= 300 {
		// Some MinIO builds reject an explicit retain-until that the caller policy cannot set.
		// Retry once with the bucket default retention so the object is still locked.
		if strings.Contains(string(resp), "ObjectLock") || status == http.StatusBadRequest {
			status, _, resp, err = c.do(ctx, http.MethodPut, key, nil, body, map[string]string{"if-none-match": "*"})
			if err != nil {
				return err
			}
			if status == http.StatusPreconditionFailed || status == http.StatusConflict {
				return fmt.Errorf("%w: put %s: %s", ErrImmutableObject, key, snippet(resp))
			}
		}
		if status < 200 || status >= 300 {
			return fmt.Errorf("s3 put %s: status %d: %s", key, status, snippet(resp))
		}
	}
	return nil
}

// Get reads an object.
func (c *S3Client) Get(ctx context.Context, key string) ([]byte, error) {
	status, _, resp, err := c.do(ctx, http.MethodGet, key, nil, nil, nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		return nil, osNotExist()
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("s3 get %s: status %d: %s", key, status, snippet(resp))
	}
	return resp, nil
}

// Delete removes an object. On a versioned compliance bucket the writer must
// delete the current version; a delete marker would hide the object without
// removing it. Compliance retention turns that version delete into ErrImmutableObject.
func (c *S3Client) Delete(ctx context.Context, key string) error {
	version, err := c.currentVersion(ctx, key)
	if err != nil {
		return err
	}
	if version != "" {
		return c.DeleteVersion(ctx, key, version)
	}
// Delete removes an object. Compliance retention turns this into ErrImmutableObject.
func (c *S3Client) Delete(ctx context.Context, key string) error {
	status, _, resp, err := c.do(ctx, http.MethodDelete, key, nil, nil, nil)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound {
		return osNotExist()
	}
	if status == http.StatusConflict || status == http.StatusForbidden || status == http.StatusMethodNotAllowed {
		return fmt.Errorf("%w: delete %s: %s", ErrImmutableObject, key, snippet(resp))
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("s3 delete %s: status %d: %s", key, status, snippet(resp))
	}
	return nil
}

func (c *S3Client) currentVersion(ctx context.Context, key string) (string, error) {
	status, hdr, resp, err := c.do(ctx, http.MethodHead, key, nil, nil, nil)
	if err != nil {
		return "", err
	}
	if status == http.StatusNotFound {
		return "", osNotExist()
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("s3 head %s: status %d: %s", key, status, snippet(resp))
	}
	return hdr.Get("X-Amz-Version-Id"), nil
}

// Exists reports whether the key is present.
func (c *S3Client) Exists(ctx context.Context, key string) (bool, error) {
	status, _, _, err := c.do(ctx, http.MethodHead, key, nil, nil, nil)
	if err != nil {
		return false, err
	}
	if status == http.StatusNotFound {
		return false, nil
	}
	if status < 200 || status >= 300 {
		return false, fmt.Errorf("s3 head %s: status %d", key, status)
	}
	return true, nil
}

// PutLockedVersion writes the object and returns its version id.
func (c *S3Client) PutLockedVersion(ctx context.Context, key string, body []byte, retainUntil time.Time) (string, error) {
	headers := map[string]string{
		"if-none-match":                       "*",
		"x-amz-object-lock-mode":              "COMPLIANCE",
		"x-amz-object-lock-retain-until-date": retainUntil.UTC().Format(time.RFC3339),
	}
	status, hdr, resp, err := c.do(ctx, http.MethodPut, key, nil, body, headers)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("s3 put %s: status %d: %s", key, status, snippet(resp))
	}
	return hdr.Get("X-Amz-Version-Id"), nil
}

// DeleteVersion tries to remove one version. Compliance mode must refuse.
func (c *S3Client) DeleteVersion(ctx context.Context, key, version string) error {
	q := url.Values{"versionId": []string{version}}
	status, _, resp, err := c.do(ctx, http.MethodDelete, key, q, nil, nil)
	if err != nil {
		return err
	}
	if status == http.StatusConflict || status == http.StatusForbidden || status == http.StatusMethodNotAllowed || wormRefusal(status, resp) {
		return fmt.Errorf("%w: delete version %s: %s", ErrImmutableObject, key, snippet(resp))
	}
	if status >= 200 && status < 300 {
		return nil
	}
	return fmt.Errorf("s3 delete version %s: status %d: %s", key, status, snippet(resp))
}

// GetVersion reads one object version.
func (c *S3Client) GetVersion(ctx context.Context, key, version string) ([]byte, error) {
	q := url.Values{"versionId": []string{version}}
	status, _, resp, err := c.do(ctx, http.MethodGet, key, q, nil, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("s3 get version %s: status %d: %s", key, status, snippet(resp))
	}
	return resp, nil
}

func (c *S3Client) do(ctx context.Context, method, key string, query url.Values, body []byte, extra map[string]string) (int, http.Header, []byte, error) {
	if body == nil {
		body = []byte{}
	}
	uri := "/" + url.PathEscape(c.Bucket) + "/" + escapeKey(key)
	endpoint, err := url.Parse(c.Endpoint)
	if err != nil {
		return 0, nil, nil, err
	}
	host := endpoint.Host
	payloadHash := sha256Hex(body)
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	headers := map[string]string{
		"host":                 host,
		"x-amz-content-sha256": payloadHash,
		"x-amz-date":           amzDate,
	}
	for k, v := range extra {
		headers[strings.ToLower(k)] = v
	}
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var canonHeaders strings.Builder
	for _, k := range keys {
		canonHeaders.WriteString(k)
		canonHeaders.WriteByte(':')
		canonHeaders.WriteString(strings.TrimSpace(headers[k]))
		canonHeaders.WriteByte('\n')
	}
	signed := strings.Join(keys, ";")
	canonQuery := canonicalQuery(query)
	canonical := method + "\n" + uri + "\n" + canonQuery + "\n" + canonHeaders.String() + "\n" + signed + "\n" + payloadHash
	scope := dateStamp + "/" + c.Region + "/s3/aws4_request"
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256Hex([]byte(canonical))
	sig := hex.EncodeToString(hmacSHA256(signingKey(c.SecretKey, dateStamp, c.Region, "s3"), []byte(stringToSign)))
	auth := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s", c.AccessKey, scope, signed, sig)

	rawURL := c.Endpoint + uri
	if canonQuery != "" {
		rawURL += "?" + canonQuery
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	for k, v := range headers {
		if k == "host" {
			continue
		}
		req.Header.Set(k, v)
	}
	req.Host = host
	req.Header.Set("Authorization", auth)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	buf, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, resp.Header, nil, err
	}
	return resp.StatusCode, resp.Header, buf, nil
}

func canonicalQuery(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vals := append([]string(nil), q[k]...)
		sort.Strings(vals)
		for _, v := range vals {
			parts = append(parts, awsEncode(k)+"="+awsEncode(v))
		}
	}
	return strings.Join(parts, "&")
}

func awsEncode(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

func wormRefusal(status int, body []byte) bool {
	if status != http.StatusBadRequest {
		return false
	}
	msg := string(body)
	return strings.Contains(msg, "WORM") || strings.Contains(msg, "ObjectLocked") || strings.Contains(msg, "AccessDenied")
}

func escapeKey(key string) string {
	parts := strings.Split(key, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

func signingKey(secret, date, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(date))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	return hmacSHA256(kService, []byte("aws4_request"))
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		return s[:300]
	}
	return s
}

func osNotExist() error {
	return errors.New("object not found")
}
