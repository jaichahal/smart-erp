package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/config"
)

// ErrTokenUnregistered means the provider rejected the token as invalid or
// unregistered. The caller deletes the token (E7).
type ErrTokenUnregistered struct {
	Reason string
}

func (e *ErrTokenUnregistered) Error() string {
	if e.Reason == "" {
		return "token unregistered"
	}
	return "token unregistered: " + e.Reason
}

// PushSender delivers a data-only payload to one device token.
type PushSender interface {
	Send(ctx context.Context, platform, token string, data map[string]string) error
}

// Mailer is the email fallback. A nil mailer records a failed email row.
type Mailer interface {
	Send(ctx context.Context, to, subject, body string) error
}

// RequirePushCredentials fails fast when live push has no FCM or APNs
// credentials. kit/config owns PushMode; the credential variables are read
// here because the kit profile does not list them yet (Track A).
func RequirePushCredentials(cfg *config.Config) error {
	if cfg == nil || cfg.PushMode != "live" {
		return nil
	}
	var missing []string
	for _, k := range []string{
		"ERP_FCM_PROJECT_ID",
		"ERP_FCM_ACCESS_TOKEN",
		"ERP_APNS_TEAM_ID",
		"ERP_APNS_KEY_ID",
		"ERP_APNS_BUNDLE_ID",
		"ERP_APNS_AUTH_TOKEN",
	} {
		if strings.TrimSpace(os.Getenv(k)) == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("notifications: missing push credentials: %s", strings.Join(missing, ", "))
	}
	return nil
}

func classifyPush(status int, body []byte) error {
	text := string(body)
	unreg := status == http.StatusNotFound || status == http.StatusGone ||
		strings.Contains(text, "UNREGISTERED") ||
		strings.Contains(text, "InvalidRegistration") ||
		strings.Contains(text, "BadDeviceToken") ||
		strings.Contains(text, "Unregistered")
	if unreg {
		return &ErrTokenUnregistered{Reason: http.StatusText(status)}
	}
	if status >= 400 {
		return fmt.Errorf("notifications: push status %d", status)
	}
	return nil
}

type sinkSender struct {
	URL    string
	Client *http.Client
}

// Send posts the flattened data map to the dev push-sink.
func (s sinkSender) Send(ctx context.Context, platform, token string, data map[string]string) error {
	if s.URL == "" {
		return fmt.Errorf("notifications: push sink url is empty")
	}
	body, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if err := RejectNotificationBlock(body); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Push-Platform", platform)
	req.Header.Set("X-Push-Token", token)
	return doPush(s.client(), req)
}

func (s sinkSender) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// fcmSender posts a data-only message to FCM HTTP v1. There is no notification block.
type fcmSender struct {
	ProjectID   string
	AccessToken string
	Endpoint    string
	Client      *http.Client
}

// Send posts a data-only FCM HTTP v1 message.
func (s fcmSender) Send(ctx context.Context, _ string, token string, data map[string]string) error {
	endpoint := s.Endpoint
	if endpoint == "" {
		endpoint = "https://fcm.googleapis.com"
	}
	payload := map[string]any{
		"message": map[string]any{
			"token": token,
			"data":  data,
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if err := RejectNotificationBlock(raw); err != nil {
		return err
	}
	url := strings.TrimRight(endpoint, "/") + "/v1/projects/" + s.ProjectID + "/messages:send"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.AccessToken)
	return doPush(clientOr(s.Client), req)
}

// apnsSender posts the flattened data map to APNs. There is no aps.alert.
type apnsSender struct {
	BundleID  string
	AuthToken string
	Endpoint  string
	Client    *http.Client
}

// Send posts a data-only APNs request.
func (s apnsSender) Send(ctx context.Context, _ string, token string, data map[string]string) error {
	endpoint := s.Endpoint
	if endpoint == "" {
		endpoint = "https://api.push.apple.com"
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if err := RejectNotificationBlock(raw); err != nil {
		return err
	}
	url := strings.TrimRight(endpoint, "/") + "/3/device/" + token
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("authorization", "bearer "+s.AuthToken)
	req.Header.Set("apns-topic", s.BundleID)
	req.Header.Set("apns-push-type", "background")
	req.Header.Set("apns-priority", "5")
	return doPush(clientOr(s.Client), req)
}

func clientOr(c *http.Client) *http.Client {
	if c != nil {
		return c
	}
	return &http.Client{Timeout: 10 * time.Second}
}

func doPush(c *http.Client, req *http.Request) error {
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	return classifyPush(resp.StatusCode, body)
}

// routeSender picks the sink in dev and FCM or APNs when PushMode is live.
type routeSender struct {
	cfg    *config.Config
	client *http.Client
}

// Send chooses the sink in dev and FCM or APNs when push mode is live.
func (s routeSender) Send(ctx context.Context, platform, token string, data map[string]string) error {
	if s.cfg != nil && s.cfg.PushMode == "live" {
		if platform == "ios" {
			return (apnsSender{
				BundleID:  os.Getenv("ERP_APNS_BUNDLE_ID"),
				AuthToken: os.Getenv("ERP_APNS_AUTH_TOKEN"),
				Client:    s.client,
			}).Send(ctx, platform, token, data)
		}
		return (fcmSender{
			ProjectID:   os.Getenv("ERP_FCM_PROJECT_ID"),
			AccessToken: os.Getenv("ERP_FCM_ACCESS_TOKEN"),
			Client:      s.client,
		}).Send(ctx, platform, token, data)
	}
	url := ""
	if s.cfg != nil {
		url = s.cfg.PushSinkURL
	}
	return (sinkSender{URL: url, Client: s.client}).Send(ctx, platform, token, data)
}
