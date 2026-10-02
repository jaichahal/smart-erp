package identity

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/oapi"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// OIDCConfig is the console login that proxies the authorization-code flow to
// Zitadel and then issues ERP tokens (R1.3). Zitadel's hosted login stays
// available unmodified; this path is the custom console page's server side.
type OIDCConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string
}

// CodeExchanger turns an authorization code into a Zitadel subject.
type CodeExchanger interface {
	Exchange(ctx context.Context, code string) (subject string, err error)
}

type httpExchanger struct {
	cfg    OIDCConfig
	client *http.Client
}

// Exchange trades an authorization code for the id_token subject.
func (h httpExchanger) Exchange(ctx context.Context, code string) (string, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", h.cfg.RedirectURL)
	form.Set("client_id", h.cfg.ClientID)
	form.Set("client_secret", h.cfg.ClientSecret)
	endpoint := strings.TrimRight(h.cfg.Issuer, "/") + "/oauth/v2/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := h.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", ErrFactor
	}
	var tok struct {
		IDToken string `json:"id_token"`
	}
	if err := json.Unmarshal(body, &tok); err != nil || tok.IDToken == "" {
		return "", ErrFactor
	}
	set, err := jwk.Fetch(ctx, strings.TrimRight(h.cfg.Issuer, "/")+"/oauth/v2/keys", jwk.WithHTTPClient(h.client))
	if err != nil {
		return "", err
	}
	parsed, err := jwt.Parse([]byte(tok.IDToken), jwt.WithKeySet(set), jwt.WithValidate(true), jwt.WithIssuer(strings.TrimRight(h.cfg.Issuer, "/")))
	if err != nil {
		return "", err
	}
	return parsed.Subject(), nil
}

func (s *Service) consoleLogin(w http.ResponseWriter, r *http.Request) {
	if s.oidc.ClientID == "" || s.oidc.Issuer == "" || s.oidc.RedirectURL == "" {
		apierr.Write(w, r, apierr.New(apierr.MissingConfig, s.text(r, msgMissingConfig)))
		return
	}
	state, err := randomToken()
	if err != nil {
		apierr.Write(w, r, apierr.Wrap(apierr.Internal, s.text(r, msgInternal), err))
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "erp_oidc_state", Value: state, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
	u, err := url.Parse(strings.TrimRight(s.oidc.Issuer, "/") + "/oauth/v2/authorize")
	if err != nil {
		apierr.Write(w, r, apierr.Wrap(apierr.Internal, s.text(r, msgInternal), err))
		return
	}
	q := u.Query()
	q.Set("client_id", s.oidc.ClientID)
	q.Set("redirect_uri", s.oidc.RedirectURL)
	q.Set("response_type", "code")
	q.Set("scope", "openid profile")
	q.Set("state", state)
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (s *Service) consoleCallback(w http.ResponseWriter, r *http.Request) {
	if s.exchanger == nil {
		apierr.Write(w, r, apierr.New(apierr.MissingConfig, s.text(r, msgMissingConfig)))
		return
	}
	state, err := r.Cookie("erp_oidc_state")
	if err != nil || state.Value == "" || state.Value != r.URL.Query().Get("state") {
		apierr.Write(w, r, apierr.New(apierr.AuthRequired, s.text(r, msgAuthFailed)))
		return
	}
	code := r.URL.Query().Get("code")
	deviceID, err := uuid.Parse(r.URL.Query().Get("device_id"))
	if code == "" || err != nil {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, s.text(r, msgValidation)))
		return
	}
	subject, err := s.exchanger.Exchange(r.Context(), code)
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.AuthRequired, s.text(r, msgAuthFailed)))
		return
	}
	acct, err := s.dir.ByID(r.Context(), subject)
	if err != nil {
		s.failLogin(w, r, subject, "unknown_user", nil)
		return
	}
	if acct.Disabled || (mfaRequired(acct.Roles) && r.URL.Query().Get("mfa") != "1") {
		cause := "disabled"
		if !acct.Disabled {
			cause = "mfa_required"
		}
		s.failLogin(w, r, acct.LoginName, cause, &acct)
		return
	}
	device, err := s.loadDevice(r.Context(), deviceID)
	if err != nil || device.Revoked || device.Platform != string(oapi.Console) {
		apierr.Write(w, r, apierr.New(apierr.DeviceMismatch, s.text(r, msgDeviceMismatch)))
		return
	}
	var access, refresh string
	var expIn int
	p := rls.Principal{UserID: acct.ID, CompanyID: acct.CompanyID, Roles: acct.Roles}
	err = rls.Tx(r.Context(), s.pool, sqlPrincipal(p), func(tx pgx.Tx) error {
		_, access, refresh, expIn, err = s.issuePair(r.Context(), tx, acct, device, "", time.Time{})
		return err
	})
	if errors.Is(err, errDeviceBound) {
		apierr.Write(w, r, apierr.New(apierr.DeviceMismatch, s.text(r, msgDeviceMismatch)))
		return
	}
	if err != nil {
		apierr.Write(w, r, apierr.Wrap(apierr.Internal, s.text(r, msgInternal), err))
		return
	}
	s.writeTokens(w, r, device.Platform, s.tokenResponse(access, refresh, expIn, acct))
}
