package identity

import (
	"crypto"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jws"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
)

type dpopClaims struct {
	HTM string `json:"htm"`
	HTU string `json:"htu"`
	IAT int64  `json:"iat"`
	JTI string `json:"jti"`
	ATH string `json:"ath"`
}

func jktOf(key jwk.Key) (string, error) {
	pub, err := jwk.PublicKeyOf(key)
	if err != nil {
		pub = key
	}
	sum, err := pub.Thumbprint(crypto.SHA256)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(sum), nil
}

func parseDeviceKey(raw map[string]any) (jwk.Key, string, error) {
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, "", err
	}
	key, err := jwk.ParseKey(b)
	if err != nil {
		return nil, "", err
	}
	if key.KeyType() != jwa.EC {
		return nil, "", fmt.Errorf("%w: kty %s raw %s", errDeviceKey, key.KeyType(), b)
	}
	got, ok := key.Get("crv")
	if !ok || fmt.Sprint(got) != "P-256" {
		return nil, "", fmt.Errorf("%w: crv %v (%T) raw %s", errDeviceKey, got, got, b)
	}
	jkt, err := jktOf(key)
	if err != nil {
		return nil, "", err
	}
	return key, jkt, nil
}

var errDeviceKey = errString("device public key must be EC P-256")

type errString string

func (e errString) Error() string { return string(e) }

func signDPoP(key jwk.Key, method, htu, access string, now time.Time) (string, error) {
	pub, err := jwk.PublicKeyOf(key)
	if err != nil {
		pub = key
	}
	claims := dpopClaims{HTM: method, HTU: htu, IAT: now.Unix(), JTI: uuid.NewString()}
	if access != "" {
		sum := sha256.Sum256([]byte(access))
		claims.ATH = base64.RawURLEncoding.EncodeToString(sum[:])
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	hdr := jws.NewHeaders()
	if err := hdr.Set(jws.AlgorithmKey, jwa.ES256); err != nil {
		return "", err
	}
	if err := hdr.Set(jws.TypeKey, "dpop+jwt"); err != nil {
		return "", err
	}
	if err := hdr.Set(jws.JWKKey, pub); err != nil {
		return "", err
	}
	signed, err := jws.Sign(payload, jws.WithKey(jwa.ES256, key, jws.WithProtectedHeaders(hdr)))
	if err != nil {
		return "", err
	}
	return string(signed), nil
}

func (s *Service) htu(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host + r.URL.Path
}

// verifyDPoP checks an RFC 9449 proof against the enrolled key thumbprint.
// accessToken is the bearer value when the caller has one, otherwise empty.
func (s *Service) verifyDPoP(r *http.Request, expectJKT, accessToken string) error {
	raw := r.Header.Get("DPoP")
	if raw == "" {
		return apierr.New(apierr.AuthRequired, s.text(r, msgAuthFailed))
	}
	msg, err := jws.Parse([]byte(raw))
	if err != nil || len(msg.Signatures()) != 1 {
		return apierr.New(apierr.AuthRequired, s.text(r, msgAuthFailed))
	}
	hdr := msg.Signatures()[0].ProtectedHeaders()
	if hdr.Type() != "dpop+jwt" || hdr.Algorithm() != jwa.ES256 || hdr.JWK() == nil {
		return apierr.New(apierr.AuthRequired, s.text(r, msgAuthFailed))
	}
	jkt, err := jktOf(hdr.JWK())
	if err != nil {
		return apierr.New(apierr.AuthRequired, s.text(r, msgAuthFailed))
	}
	if jkt != expectJKT {
		return apierr.New(apierr.DeviceMismatch, s.text(r, msgDeviceMismatch))
	}
	if _, err := jws.Verify([]byte(raw), jws.WithKey(jwa.ES256, hdr.JWK())); err != nil {
		return apierr.New(apierr.AuthRequired, s.text(r, msgAuthFailed))
	}
	var claims dpopClaims
	if err := json.Unmarshal(msg.Payload(), &claims); err != nil {
		return apierr.New(apierr.AuthRequired, s.text(r, msgAuthFailed))
	}
	now := s.now()
	iat := time.Unix(claims.IAT, 0)
	if claims.HTM != r.Method || claims.HTU != s.htu(r) || claims.JTI == "" || iat.Before(now.Add(-s.policy.DPoPSkew)) || iat.After(now.Add(s.policy.DPoPSkew)) {
		return apierr.New(apierr.AuthRequired, s.text(r, msgAuthFailed))
	}
	if accessToken != "" {
		sum := sha256.Sum256([]byte(accessToken))
		want := base64.RawURLEncoding.EncodeToString(sum[:])
		if claims.ATH != want {
			return apierr.New(apierr.AuthRequired, s.text(r, msgAuthFailed))
		}
	}
	seen, err := s.rememberJTI(r.Context(), claims.JTI, now.Add(s.policy.DPoPSkew))
	if err != nil {
		return apierr.Wrap(apierr.Internal, s.text(r, msgInternal), err)
	}
	if seen {
		return apierr.New(apierr.AuthRequired, s.text(r, msgAuthFailed))
	}
	return nil
}

func (s *Service) verifyBiometric(c caller, code string) error {
	msg, err := jws.Parse([]byte(code))
	if err != nil || len(msg.Signatures()) != 1 {
		return ErrFactor
	}
	hdr := msg.Signatures()[0].ProtectedHeaders()
	if hdr.Algorithm() != jwa.ES256 || hdr.JWK() == nil {
		return ErrFactor
	}
	jkt, err := jktOf(hdr.JWK())
	if err != nil || jkt != c.JKT {
		return ErrFactor
	}
	if _, err := jws.Verify([]byte(code), jws.WithKey(jwa.ES256, hdr.JWK())); err != nil {
		return ErrFactor
	}
	var claims dpopClaims
	if err := json.Unmarshal(msg.Payload(), &claims); err != nil {
		return ErrFactor
	}
	now := s.now()
	iat := time.Unix(claims.IAT, 0)
	if claims.HTM != "biometric" || claims.HTU != c.SessionID.String() || iat.Before(now.Add(-s.policy.DPoPSkew)) || iat.After(now.Add(s.policy.DPoPSkew)) {
		return ErrFactor
	}
	return nil
}
