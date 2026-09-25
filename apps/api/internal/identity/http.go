package identity

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/riverqueue/river"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/httpx"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/idempotency"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/ifmatch"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/oapi"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// Service is the identity HTTP API.
type Service struct {
	pool      *pgxpool.Pool
	river     *river.Client[pgx.Tx]
	log       *slog.Logger
	broker    Broker
	dir       Directory
	push      PushUnregistrar
	policy    Policy
	now       func() time.Time
	kms       *localKMS
	issuer    string
	audience  string
	oidc      OIDCConfig
	exchanger CodeExchanger
}

// Option configures Service.
type Option func(*Service)

// WithBroker replaces the Zitadel gRPC client.
func WithBroker(b Broker) Option { return func(s *Service) { s.broker = b } }

// WithDirectory replaces the SQL user directory.
func WithDirectory(d Directory) Option { return func(s *Service) { s.dir = d } }

// WithClock replaces the wall clock. Tests use it to expire tokens.
func WithClock(now func() time.Time) Option { return func(s *Service) { s.now = now } }

// WithPolicy replaces rate, lockout, and session limits.
func WithPolicy(p Policy) Option { return func(s *Service) { s.policy = p } }

// WithPush registers a push-token hook invoked on logout.
func WithPush(p PushUnregistrar) Option { return func(s *Service) { s.push = p } }

// WithOIDC enables the console login proxy.
func WithOIDC(cfg OIDCConfig, ex CodeExchanger) Option {
	return func(s *Service) { s.oidc = cfg; s.exchanger = ex }
}

// Mount registers /auth/* and /me* on r. r is the /api/v1 router.
func Mount(r chi.Router, deps httpx.Deps, opts ...Option) *Service {
	s := newService(deps, opts...)
	if _, missing := s.broker.(missingBroker); missing {
		if b, err := BrokerFromEnv(context.Background()); err != nil {
			s.log.Warn("zitadel dial", "err", err)
		} else if b != nil {
			s.broker = b
		}
	}
	pub := r.With(s.withAnon, idempotency.Middleware(s.pool))
	pub.Post("/auth/session", s.createSession)
	pub.Post("/auth/session/{id}/check", s.checkSession)
	pub.Post("/auth/token", s.issueToken)
	pub.Post("/auth/refresh", s.refresh)
	pub.Post("/auth/device/enroll", s.enroll)
	authed := r.With(s.authenticate, idempotency.Middleware(s.pool))
	authed.Post("/auth/logout", s.logout)
	authed.Post("/auth/step-up", s.stepUp)
	authed.Delete("/me/sessions/{id}", s.endSession)
	r.With(s.authenticate).Get("/me", s.me)
	r.With(s.authenticate).Get("/me/sessions", s.listSessions)
	r.Get("/auth/jwks", s.jwks)
	r.Get("/auth/console/login", s.consoleLogin)
	r.Get("/auth/console/callback", s.consoleCallback)
	return s
}

func newService(deps httpx.Deps, opts ...Option) *Service {
	s := &Service{
		pool: deps.Pool, river: deps.River, log: deps.Log, policy: DefaultPolicy(),
		now: func() time.Time { return time.Now().UTC() }, issuer: "smart-erp", audience: "erp",
		broker: missingBroker{},
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	for _, opt := range opts {
		opt(s)
	}
	if s.policy.MaxSessions < 1 {
		s.policy.MaxSessions = 1
	}
	if s.policy.DPoPSkew <= 0 {
		s.policy.DPoPSkew = 5 * time.Minute
	}
	if s.policy.RateWindow <= 0 {
		s.policy.RateWindow = time.Minute
	}
	if s.policy.LoginTTL <= 0 {
		s.policy.LoginTTL = 10 * time.Minute
	}
	if s.policy.RefreshSliding <= 0 {
		s.policy.RefreshSliding = 30 * 24 * time.Hour
	}
	if s.policy.RefreshAbsolute <= 0 {
		s.policy.RefreshAbsolute = 90 * 24 * time.Hour
	}
	if s.policy.ConsoleIdle <= 0 {
		s.policy.ConsoleIdle = 8 * time.Hour
	}
	if s.policy.LockFor <= 0 {
		s.policy.LockFor = 15 * time.Minute
	}
	if s.policy.FailuresBeforeLock <= 0 {
		s.policy.FailuresBeforeLock = 5
	}
	if s.exchanger == nil && s.oidc.ClientID != "" {
		s.exchanger = httpExchanger{cfg: s.oidc, client: http.DefaultClient}
	}
	if s.dir == nil {
		s.dir = sqlDirectory{pool: s.pool}
	}
	s.kms = &localKMS{pool: s.pool, now: func() time.Time { return s.now().UTC() }}
	return s
}

type missingBroker struct{}

// Create reports that Zitadel is not configured.
func (missingBroker) Create(context.Context, string) (string, string, error) {
	return "", "", ErrUnavailable
}

// Password reports that Zitadel is not configured.
func (missingBroker) Password(context.Context, string, string) (Factors, error) {
	return Factors{}, ErrUnavailable
}

// TOTP reports that Zitadel is not configured.
func (missingBroker) TOTP(context.Context, string, string) (Factors, error) {
	return Factors{}, ErrUnavailable
}

// WebAuthN reports that Zitadel is not configured.
func (missingBroker) WebAuthN(context.Context, string, map[string]any) (Factors, error) {
	return Factors{}, ErrUnavailable
}

type caller struct {
	Account   Account
	SessionID uuid.UUID
	DeviceID  uuid.UUID
	JKT       string
	Platform  string
	BrokerID  string
	Version   int
}

type callerCtx struct{}

func (s *Service) text(r *http.Request, key string) string {
	return translate(langOf(r.Header.Get("Accept-Language")), key)
}

func (s *Service) withAnon(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := rls.Principal{UserID: "ip:" + clientIP(r), CompanyID: platformCompanyID}
		next.ServeHTTP(w, r.WithContext(rls.WithPrincipal(r.Context(), p)))
	})
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Service) limited(w http.ResponseWriter, r *http.Request, keys ...string) bool {
	var limits = []int{s.policy.RatePerLogin, s.policy.RatePerIP}
	for i, key := range keys {
		limit := s.policy.RatePerIP
		if i == 0 {
			limit = limits[0]
		}
		retry, err := s.allow(r.Context(), key, limit)
		if err != nil {
			apierr.Write(w, r, apierr.Wrap(apierr.Internal, s.text(r, msgInternal), err))
			return true
		}
		if retry > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			apierr.Write(w, r, apierr.New(apierr.RateLimited, s.text(r, msgRateLimited)))
			return true
		}
	}
	return false
}

type createBody struct {
	LoginName string `json:"login_name"`
}

func (s *Service) createSession(w http.ResponseWriter, r *http.Request) {
	var body createBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, s.text(r, msgValidation)).WithDetails(map[string]any{"field": "login_name"}))
		return
	}
	body.LoginName = strings.TrimSpace(body.LoginName)
	if body.LoginName == "" {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, s.text(r, msgValidation)).WithDetails(map[string]any{"field": "login_name"}))
		return
	}
	if s.limited(w, r, "login:"+body.LoginName, "ip:"+clientIP(r)) {
		return
	}
	row := loginRow{ID: uuid.New(), LoginName: body.LoginName, ExpiresAt: s.now().Add(s.policy.LoginTTL), Shadow: true}
	acct, err := s.dir.ByLogin(r.Context(), body.LoginName)
	switch {
	case err == nil && acct.Disabled:
		row.Shadow = false
		row.UserID = acct.ID
		row.CompanyID = acct.CompanyID
		row.MFARequired = mfaRequired(acct.Roles)
	case err == nil:
		sid, _, berr := s.broker.Create(r.Context(), body.LoginName)
		if berr != nil && !errors.Is(berr, ErrUnknown) && !errors.Is(berr, ErrLocked) && !errors.Is(berr, ErrFactor) {
			apierr.Write(w, r, apierr.Wrap(apierr.Internal, s.text(r, msgUnavailable), berr))
			return
		}
		row.Shadow = berr != nil
		row.UserID = acct.ID
		row.CompanyID = acct.CompanyID
		row.BrokerID = sid
		row.MFARequired = mfaRequired(acct.Roles)
	case errors.Is(err, ErrUnknown):
		row.Shadow = true
	default:
		apierr.Write(w, r, apierr.Wrap(apierr.Internal, s.text(r, msgInternal), err))
		return
	}
	if err := s.insertLogin(r.Context(), row); err != nil {
		apierr.Write(w, r, apierr.Wrap(apierr.Internal, s.text(r, msgInternal), err))
		return
	}
	httpx.JSON(w, r, http.StatusOK, authSession(row.ID.String(), false, false))
}

func authSession(id string, verified, totp bool) oapi.AuthSession {
	out := oapi.AuthSession{SessionId: id, Verified: &verified}
	if totp {
		v := true
		out.Challenges.TotpRequired = &v
	}
	return out
}

type checkBody struct {
	Password *string        `json:"password"`
	TOTP     *string        `json:"totp"`
	WebAuthn map[string]any `json:"webauthn_assertion"`
}

func (s *Service) checkSession(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.failLogin(w, r, "session", "unknown_user", nil)
		return
	}
	var body checkBody
	if err := httpx.DecodeJSON(r, &body); err != nil || (body.Password == nil && body.TOTP == nil && body.WebAuthn == nil) {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, s.text(r, msgValidation)))
		return
	}
	row, err := s.loadLogin(r.Context(), id)
	if err != nil || row.ExpiresAt.Before(s.now()) {
		s.failLogin(w, r, "session", "unknown_user", nil)
		return
	}
	if s.limited(w, r, "login:"+row.LoginName, "ip:"+clientIP(r)) {
		return
	}
	acct, aerr := s.dir.ByLogin(r.Context(), row.LoginName)
	var acctPtr *Account
	if aerr == nil {
		acctPtr = &acct
	}
	locked, err := s.locked(r.Context(), row.LoginName)
	if err != nil {
		apierr.Write(w, r, apierr.Wrap(apierr.Internal, s.text(r, msgInternal), err))
		return
	}
	if locked {
		s.failLogin(w, r, row.LoginName, "locked", acctPtr)
		return
	}
	if row.Shadow || aerr != nil || acct.Disabled || row.BrokerID == "" {
		cause := "unknown_user"
		if aerr == nil && acct.Disabled {
			cause = "disabled"
		}
		s.failLogin(w, r, row.LoginName, cause, acctPtr)
		return
	}
	if body.Password != nil {
		factors, err := s.broker.Password(r.Context(), row.BrokerID, *body.Password)
		if errors.Is(err, ErrLocked) {
			s.failLogin(w, r, row.LoginName, "locked", acctPtr)
			return
		}
		if err != nil || !factors.Password {
			s.failLogin(w, r, row.LoginName, "wrong_password", acctPtr)
			return
		}
		row.PasswordOK = true
	}
	if body.TOTP != nil {
		if !row.PasswordOK {
			s.failLogin(w, r, row.LoginName, "wrong_password", acctPtr)
			return
		}
		factors, err := s.broker.TOTP(r.Context(), row.BrokerID, *body.TOTP)
		if err != nil || !factors.TOTP {
			s.failLogin(w, r, row.LoginName, "wrong_password", acctPtr)
			return
		}
		row.SecondOK = true
	}
	if body.WebAuthn != nil {
		if !row.PasswordOK {
			s.failLogin(w, r, row.LoginName, "wrong_password", acctPtr)
			return
		}
		factors, err := s.broker.WebAuthN(r.Context(), row.BrokerID, body.WebAuthn)
		if err != nil || !factors.WebAuthN {
			s.failLogin(w, r, row.LoginName, "wrong_password", acctPtr)
			return
		}
		row.SecondOK = true
	}
	if err := s.markLogin(r.Context(), row.ID, row.PasswordOK, row.SecondOK); err != nil {
		apierr.Write(w, r, apierr.Wrap(apierr.Internal, s.text(r, msgInternal), err))
		return
	}
	verified := row.PasswordOK && (!row.MFARequired || row.SecondOK)
	httpx.JSON(w, r, http.StatusOK, authSession(row.ID.String(), verified, row.MFARequired && !row.SecondOK))
}

type tokenBody struct {
	SessionID string `json:"session_id"`
	DeviceID  string `json:"device_id"`
}

func (s *Service) issueToken(w http.ResponseWriter, r *http.Request) {
	var body tokenBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, s.text(r, msgValidation)))
		return
	}
	sid, err1 := uuid.Parse(body.SessionID)
	did, err2 := uuid.Parse(body.DeviceID)
	if err1 != nil || err2 != nil {
		s.failLogin(w, r, body.SessionID, "unknown_user", nil)
		return
	}
	row, err := s.loadLogin(r.Context(), sid)
	if err != nil || row.ExpiresAt.Before(s.now()) {
		s.failLogin(w, r, body.SessionID, "unknown_user", nil)
		return
	}
	if s.limited(w, r, "login:"+row.LoginName, "ip:"+clientIP(r)) {
		return
	}
	acct, aerr := s.dir.ByLogin(r.Context(), row.LoginName)
	var acctPtr *Account
	if aerr == nil {
		acctPtr = &acct
	}
	if locked, err := s.locked(r.Context(), row.LoginName); err != nil {
		apierr.Write(w, r, apierr.Wrap(apierr.Internal, s.text(r, msgInternal), err))
		return
	} else if locked {
		s.failLogin(w, r, row.LoginName, "locked", acctPtr)
		return
	}
	if row.Shadow || aerr != nil || acct.Disabled || !row.PasswordOK {
		cause := "unknown_user"
		if aerr == nil && acct.Disabled {
			cause = "disabled"
		} else if aerr == nil && !row.PasswordOK {
			cause = "wrong_password"
		}
		s.failLogin(w, r, row.LoginName, cause, acctPtr)
		return
	}
	if row.MFARequired && !row.SecondOK {
		s.failLogin(w, r, row.LoginName, "mfa_required", acctPtr)
		return
	}
	device, err := s.loadDevice(r.Context(), did)
	if err != nil || device.Revoked {
		apierr.Write(w, r, apierr.New(apierr.DeviceMismatch, s.text(r, msgDeviceMismatch)))
		return
	}
	if device.Platform != string(oapi.Console) {
		if err := s.verifyDPoP(r, device.JKT, ""); err != nil {
			apierr.Write(w, r, err)
			return
		}
	} else if r.Header.Get("DPoP") != "" {
		if err := s.verifyDPoP(r, device.JKT, ""); err != nil {
			apierr.Write(w, r, err)
			return
		}
	}
	var access, refresh string
	var expIn int
	p := rls.Principal{UserID: acct.ID, CompanyID: acct.CompanyID, Roles: acct.Roles}
	err = rls.Tx(r.Context(), s.pool, sqlPrincipal(p), func(tx pgx.Tx) error {
		_, a, rf, n, err := s.issuePair(r.Context(), tx, acct, device, row.BrokerID, time.Time{})
		access, refresh, expIn = a, rf, n
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
	s.clearFailures(r.Context(), row.LoginName)
	resp := s.tokenResponse(access, refresh, expIn, acct)
	s.writeTokens(w, r, device.Platform, resp)
}

func (s *Service) tokenResponse(access, refresh string, expIn int, acct Account) oapi.TokenResponse {
	methods := normalizeMethods(acct.StepUpMethods)
	out := make([]oapi.TokenResponseStepUpMethods, 0, len(methods))
	for _, m := range methods {
		out = append(out, oapi.TokenResponseStepUpMethods(m))
	}
	return oapi.TokenResponse{
		AccessToken: access, RefreshToken: refresh, ExpiresIn: expIn, StepUpMethods: out,
		User: oapi.User{Id: acct.ID, Name: acct.Name, Roles: rolesOrEmpty(acct.Roles), Personas: rolesOrEmpty(acct.Personas), CompanyId: acct.CompanyID.String()},
	}
}

func (s *Service) writeTokens(w http.ResponseWriter, r *http.Request, platform string, resp oapi.TokenResponse) {
	if platform == string(oapi.Console) {
		csrf, err := randomToken()
		if err == nil {
			http.SetCookie(w, &http.Cookie{Name: "erp_access", Value: resp.AccessToken, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
			http.SetCookie(w, &http.Cookie{Name: "erp_csrf", Value: csrf, Path: "/", SameSite: http.SameSiteLaxMode})
		}
	}
	httpx.JSON(w, r, http.StatusOK, resp)
}

type refreshBody struct {
	RefreshToken string `json:"refresh_token"`
}

func (s *Service) refresh(w http.ResponseWriter, r *http.Request) {
	var body refreshBody
	if err := httpx.DecodeJSON(r, &body); err != nil || body.RefreshToken == "" {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, s.text(r, msgValidation)))
		return
	}
	var jkt, platform string
	err := s.pool.QueryRow(r.Context(), `SELECT d.jkt, d.platform FROM erp.identity_refresh_tokens t JOIN erp.identity_devices d ON d.id = t.device_id WHERE t.token_hash=$1`, hashToken(body.RefreshToken)).Scan(&jkt, &platform)
	if errors.Is(err, pgx.ErrNoRows) {
		apierr.Write(w, r, apierr.New(apierr.AuthRequired, s.text(r, msgAuthFailed)))
		return
	}
	if err != nil {
		apierr.Write(w, r, apierr.Wrap(apierr.Internal, s.text(r, msgInternal), err))
		return
	}
	if platform != string(oapi.Console) || r.Header.Get("DPoP") != "" {
		if err := s.verifyDPoP(r, jkt, ""); err != nil {
			apierr.Write(w, r, err)
			return
		}
	}
	var access, next string
	var expIn int
	var plat, userID string
	var reused bool
	p := rls.Principal{UserID: "refresh", CompanyID: platformCompanyID}
	err = rls.Tx(r.Context(), s.pool, sqlPrincipal(p), func(tx pgx.Tx) error {
		row, err := s.loadRefresh(r.Context(), tx, body.RefreshToken)
		if err != nil {
			return err
		}
		device, err := s.loadDevice(r.Context(), row.DeviceID)
		if err != nil {
			return err
		}
		plat = device.Platform
		userID = row.UserID
		if row.Used {
			reused = true
			return s.revokeDeviceTx(r.Context(), tx, row.DeviceID)
		}
		now := s.now()
		if row.Revoked || device.Revoked {
			return errRevoked
		}
		if !row.ExpiresAt.After(now) || !row.Absolute.After(now) {
			return errExpired
		}
		tag, err := tx.Exec(r.Context(), `UPDATE erp.identity_refresh_tokens SET used_at=$2 WHERE id=$1 AND used_at IS NULL`, row.ID, now.UTC())
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			reused = true
			return s.revokeDeviceTx(r.Context(), tx, row.DeviceID)
		}
		acct, err := s.dir.ByID(r.Context(), row.UserID)
		if err != nil {
			return err
		}
		if acct.Disabled {
			return errRevoked
		}
		sess, err := s.loadSession(r.Context(), row.SessionID)
		if err != nil {
			return err
		}
		if sess.Ended {
			return errRevoked
		}
		access, next, expIn, err = s.mint(r.Context(), tx, acct, row.SessionID, device, row.Absolute, now)
		return err
	})
	if err == nil && reused {
		err = errReused
	}
	switch {
	case errors.Is(err, errReused), errors.Is(err, errRevoked):
		apierr.Write(w, r, apierr.New(apierr.TokenRevoked, s.text(r, msgTokenRevoked)))
	case errors.Is(err, errExpired):
		apierr.Write(w, r, apierr.New(apierr.TokenExpired, s.text(r, msgTokenExpired)))
	case err != nil:
		apierr.Write(w, r, apierr.Wrap(apierr.Internal, s.text(r, msgInternal), err))
	default:
		acct, aerr := s.dir.ByID(r.Context(), userID)
		if aerr != nil {
			apierr.Write(w, r, apierr.Wrap(apierr.Internal, s.text(r, msgInternal), aerr))
			return
		}
		s.writeTokens(w, r, plat, s.tokenResponse(access, next, expIn, acct))
	}
}

var (
	errReused  = errors.New("refresh reused")
	errRevoked = errors.New("token revoked")
	errExpired = errors.New("token expired")
)

func (s *Service) logout(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	p := rls.Principal{UserID: c.Account.ID, CompanyID: c.Account.CompanyID, Roles: c.Account.Roles}
	err := rls.Tx(r.Context(), s.pool, sqlPrincipal(p), func(tx pgx.Tx) error {
		return s.endSessionTx(r.Context(), tx, c.SessionID, c.Account.ID)
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		apierr.Write(w, r, apierr.Wrap(apierr.Internal, s.text(r, msgInternal), err))
		return
	}
	if s.push != nil {
		if err := s.push.Unregister(r.Context(), c.Account.ID, c.DeviceID.String()); err != nil {
			s.log.WarnContext(r.Context(), "push unregister", "err", err)
		}
	}
	http.SetCookie(w, &http.Cookie{Name: "erp_access", Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	http.SetCookie(w, &http.Cookie{Name: "erp_csrf", Value: "", Path: "/", MaxAge: -1})
	httpx.JSON(w, r, http.StatusOK, struct{}{})
}

type stepBody struct {
	Method string `json:"method"`
	Code   string `json:"code"`
}

func (s *Service) stepUp(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	var body stepBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, s.text(r, msgValidation)))
		return
	}
	if err := s.verifyStep(r, c, body.Method, body.Code); err != nil {
		if errors.Is(err, errStepMethod) {
			apierr.Write(w, r, apierr.New(apierr.ValidationError, s.text(r, msgStepUpMethod)))
			return
		}
		apierr.Write(w, r, apierr.New(apierr.AuthRequired, s.text(r, msgAuthFailed)))
		return
	}
	raw, err := randomToken()
	if err != nil {
		apierr.Write(w, r, apierr.Wrap(apierr.Internal, s.text(r, msgInternal), err))
		return
	}
	exp := s.now().Add(stepUpLifetime)
	_, err = s.pool.Exec(r.Context(), `INSERT INTO erp.identity_step_up(token_hash, user_id, session_id, expires_at) VALUES ($1,$2,$3,$4)`,
		hashToken(raw), c.Account.ID, c.SessionID, exp.UTC())
	if err != nil {
		apierr.Write(w, r, apierr.Wrap(apierr.Internal, s.text(r, msgInternal), err))
		return
	}
	httpx.JSON(w, r, http.StatusOK, struct {
		StepUpToken string `json:"step_up_token"`
		ExpiresIn   int    `json:"expires_in"`
	}{StepUpToken: raw, ExpiresIn: int(stepUpLifetime.Seconds())})
}

var errStepMethod = errors.New("unsupported step-up method")

func (s *Service) verifyStep(r *http.Request, c caller, method, code string) error {
	switch method {
	case "totp":
		factors, err := s.broker.TOTP(r.Context(), c.BrokerID, code)
		if err != nil || !factors.TOTP {
			return ErrFactor
		}
		return nil
	case "webauthn":
		var assertion map[string]any
		if json.Unmarshal([]byte(code), &assertion) != nil {
			return ErrFactor
		}
		factors, err := s.broker.WebAuthN(r.Context(), c.BrokerID, assertion)
		if err != nil || !factors.WebAuthN {
			return ErrFactor
		}
		return nil
	case "biometric":
		return s.verifyBiometric(c, code)
	default:
		return errStepMethod
	}
}

// RequireStepUp consumes a single-use step-up token for the caller's session.
// Other modules call it before a gated action (R1.6, A6). False means the
// error envelope was already written.
func (s *Service) RequireStepUp(w http.ResponseWriter, r *http.Request) bool {
	c, ok := callerFrom(r.Context())
	if !ok {
		apierr.Write(w, r, apierr.New(apierr.AuthRequired, s.text(r, msgAuthFailed)))
		return false
	}
	raw := r.Header.Get("Step-Up-Token")
	if raw == "" {
		apierr.Write(w, r, apierr.New(apierr.StepUpRequired, s.text(r, msgStepUpRequired)))
		return false
	}
	tag, err := s.pool.Exec(r.Context(), `UPDATE erp.identity_step_up SET used_at=$4 WHERE token_hash=$1 AND user_id=$2 AND session_id=$3 AND used_at IS NULL AND expires_at > $4`,
		hashToken(raw), c.Account.ID, c.SessionID, s.now().UTC())
	if err != nil {
		apierr.Write(w, r, apierr.Wrap(apierr.Internal, s.text(r, msgInternal), err))
		return false
	}
	if tag.RowsAffected() != 1 {
		apierr.Write(w, r, apierr.New(apierr.StepUpRequired, s.text(r, msgStepUpRequired)))
		return false
	}
	return true
}

type enrollBody struct {
	PublicKey  map[string]any `json:"public_key"`
	Platform   string         `json:"platform"`
	AppVersion string         `json:"app_version"`
	DeviceName string         `json:"device_name"`
}

func (s *Service) enroll(w http.ResponseWriter, r *http.Request) {
	var body enrollBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, s.text(r, msgValidation)))
		return
	}
	switch body.Platform {
	case string(oapi.Android), string(oapi.Ios), string(oapi.Console):
	default:
		apierr.Write(w, r, apierr.New(apierr.ValidationError, s.text(r, msgValidation)).WithDetails(map[string]any{"field": "platform"}))
		return
	}
	if body.AppVersion == "" || body.DeviceName == "" || len(body.DeviceName) > 120 {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, s.text(r, msgValidation)))
		return
	}
	key, jkt, err := parseDeviceKey(body.PublicKey)
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, s.text(r, msgValidation)).WithDetails(map[string]any{"field": "public_key"}))
		return
	}
	pub, err := jwk.PublicKeyOf(key)
	if err != nil {
		pub = key
	}
	raw, err := json.Marshal(pub)
	if err != nil {
		apierr.Write(w, r, apierr.Wrap(apierr.Internal, s.text(r, msgInternal), err))
		return
	}
	id := uuid.New()
	p := rls.Principal{UserID: "enrol", CompanyID: platformCompanyID}
	err = rls.Tx(r.Context(), s.pool, sqlPrincipal(p), func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `INSERT INTO erp.identity_devices(id, public_jwk, jkt, platform, app_version, device_name) VALUES ($1,$2,$3,$4,$5,$6)`,
			id, raw, jkt, body.Platform, body.AppVersion, body.DeviceName); err != nil {
			return err
		}
		return s.enqueue(r.Context(), tx, deviceRegisteredArgs{DeviceID: id.String(), JKT: jkt, Platform: body.Platform})
	})
	if err != nil {
		apierr.Write(w, r, apierr.Wrap(apierr.Internal, s.text(r, msgInternal), err))
		return
	}
	httpx.JSON(w, r, http.StatusCreated, struct {
		DeviceID string `json:"device_id"`
	}{DeviceID: id.String()})
}

func (s *Service) jwks(w http.ResponseWriter, r *http.Request) {
	raw, err := s.kms.jwks(r.Context())
	if err != nil {
		apierr.Write(w, r, apierr.Wrap(apierr.Internal, s.text(r, msgInternal), err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

func (s *Service) me(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	httpx.JSON(w, r, http.StatusOK, oapi.User{
		Id: c.Account.ID, Name: c.Account.Name, Roles: rolesOrEmpty(c.Account.Roles),
		Personas: rolesOrEmpty(c.Account.Personas), CompanyId: c.Account.CompanyID.String(),
	})
}

func (s *Service) listSessions(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			apierr.Write(w, r, apierr.New(apierr.ValidationError, s.text(r, msgValidation)).WithDetails(map[string]any{"field": "limit"}))
			return
		}
		limit = n
	}
	var cursorTime time.Time
	var cursorID uuid.UUID
	var haveCursor bool
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		t, id, err := decodeCursor(raw)
		if err != nil {
			apierr.Write(w, r, apierr.New(apierr.ValidationError, s.text(r, msgValidation)).WithDetails(map[string]any{"field": "cursor"}))
			return
		}
		cursorTime, cursorID, haveCursor = t, id, true
	}
	var total int
	if err := s.pool.QueryRow(r.Context(), `SELECT count(*) FROM erp.identity_sessions WHERE user_id=$1 AND ended_at IS NULL`, c.Account.ID).Scan(&total); err != nil {
		apierr.Write(w, r, apierr.Wrap(apierr.Internal, s.text(r, msgInternal), err))
		return
	}
	q := `SELECT s.id, s.device_id, d.device_name, d.platform, s.created_at, s.last_seen_at, s.state_version
		FROM erp.identity_sessions s JOIN erp.identity_devices d ON d.id = s.device_id
		WHERE s.user_id=$1 AND s.ended_at IS NULL`
	args := []any{c.Account.ID}
	if haveCursor {
		q += ` AND (s.created_at, s.id) < ($2, $3)`
		args = append(args, cursorTime, cursorID)
	}
	q += ` ORDER BY s.created_at DESC, s.id DESC LIMIT ` + strconv.Itoa(limit+1)
	rows, err := s.pool.Query(r.Context(), q, args...)
	if err != nil {
		apierr.Write(w, r, apierr.Wrap(apierr.Internal, s.text(r, msgInternal), err))
		return
	}
	defer rows.Close()
	var list []oapi.UserSession
	for rows.Next() {
		var id, did uuid.UUID
		var name, platform string
		var created, seen time.Time
		var version int
		if err := rows.Scan(&id, &did, &name, &platform, &created, &seen, &version); err != nil {
			apierr.Write(w, r, apierr.Wrap(apierr.Internal, s.text(r, msgInternal), err))
			return
		}
		plat := oapi.Platform(platform)
		current := id == c.SessionID
		item := oapi.UserSession{
			Id: id.String(), DeviceId: did.String(), DeviceName: &name, Platform: &plat,
			CreatedAt: created, LastSeenAt: &seen, StateVersion: version, Current: &current,
		}
		list = append(list, item)
	}
	if err := rows.Err(); err != nil {
		apierr.Write(w, r, apierr.Wrap(apierr.Internal, s.text(r, msgInternal), err))
		return
	}
	var next *string
	if len(list) > limit {
		list = list[:limit]
		last := list[len(list)-1]
		cur := encodeCursor(last.CreatedAt, uuid.MustParse(last.Id))
		next = &cur
	}
	if list == nil {
		list = []oapi.UserSession{}
	}
	s.writePage(w, r, list, next, total)
}

func (s *Service) writePage(w http.ResponseWriter, r *http.Request, data any, next *string, total int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(struct {
		Data any           `json:"data"`
		Meta oapi.PageMeta `json:"meta"`
	}{Data: data, Meta: oapi.PageMeta{AsOf: s.now(), RequestId: apierr.RequestID(r.Context()), NextCursor: next, Total: &total}})
}

func encodeCursor(t time.Time, id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(t.UTC().Format(time.RFC3339Nano) + "|" + id.String()))
}

func decodeCursor(raw string) (time.Time, uuid.UUID, error) {
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return time.Time{}, uuid.Nil, err
	}
	timePart, idPart, ok := strings.Cut(string(b), "|")
	if !ok {
		return time.Time{}, uuid.Nil, errors.New("cursor")
	}
	t, err := time.Parse(time.RFC3339Nano, timePart)
	if err != nil {
		return time.Time{}, uuid.Nil, err
	}
	id, err := uuid.Parse(idPart)
	return t, id, err
}

func (s *Service) endSession(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.NotFound, s.text(r, msgNotFound)))
		return
	}
	expected, present, err := ifmatch.Parse(r, false)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	view, err := s.loadSession(r.Context(), id)
	if err != nil || view.Ended {
		apierr.Write(w, r, apierr.New(apierr.NotFound, s.text(r, msgNotFound)))
		return
	}
	if view.UserID != c.Account.ID && !isSystemManager(c.Account.Roles) {
		apierr.Write(w, r, apierr.New(apierr.NotFound, s.text(r, msgNotFound)))
		return
	}
	if present {
		if err := ifmatch.Check(expected, int64(view.Version), userSession(view, view.ID == c.SessionID)); err != nil {
			apierr.Write(w, r, err)
			return
		}
	}
	p := rls.Principal{UserID: c.Account.ID, CompanyID: c.Account.CompanyID, Roles: c.Account.Roles}
	err = rls.Tx(r.Context(), s.pool, sqlPrincipal(p), func(tx pgx.Tx) error {
		return s.endSessionTx(r.Context(), tx, id, view.UserID)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		apierr.Write(w, r, apierr.New(apierr.NotFound, s.text(r, msgNotFound)))
		return
	}
	if err != nil {
		apierr.Write(w, r, apierr.Wrap(apierr.Internal, s.text(r, msgInternal), err))
		return
	}
	httpx.JSON(w, r, http.StatusOK, struct{}{})
}

func userSession(view sessionView, current bool) oapi.UserSession {
	plat := oapi.Platform(view.Platform)
	name := view.DeviceName
	seen := view.LastSeen
	return oapi.UserSession{
		Id: view.ID.String(), DeviceId: view.DeviceID.String(), DeviceName: &name, Platform: &plat,
		CreatedAt: view.CreatedAt, LastSeenAt: &seen, StateVersion: view.Version, Current: &current,
	}
}

func (s *Service) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, err := s.authContext(r)
		if err != nil {
			apierr.Write(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Service) authContext(r *http.Request) (context.Context, error) {
	raw, fromCookie := bearerOrCookie(r)
	if raw == "" {
		return nil, apierr.New(apierr.AuthRequired, s.text(r, msgAuthFailed))
	}
	if fromCookie && r.Method != http.MethodGet && r.Method != http.MethodHead {
		csrf, _ := r.Cookie("erp_csrf")
		if csrf == nil || csrf.Value == "" || csrf.Value != r.Header.Get("X-CSRF-Token") {
			return nil, apierr.New(apierr.AuthRequired, s.text(r, msgAuthFailed))
		}
	}
	tok, err := s.parseAccess(r.Context(), raw)
	if err != nil {
		return nil, apierr.New(apierr.AuthRequired, s.text(r, msgAuthFailed))
	}
	row, err := s.loadAccess(r.Context(), tok.JwtID())
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apierr.New(apierr.TokenRevoked, s.text(r, msgTokenRevoked))
	}
	if err != nil {
		return nil, apierr.Wrap(apierr.Internal, s.text(r, msgInternal), err)
	}
	acct, aerr := s.dir.ByID(r.Context(), row.UserID)
	disabled := aerr == nil && acct.Disabled
	if row.Revoked || row.Ended || row.DevRevoked || disabled || errors.Is(aerr, ErrUnknown) {
		return nil, apierr.New(apierr.TokenRevoked, s.text(r, msgTokenRevoked))
	}
	if aerr != nil {
		return nil, apierr.Wrap(apierr.Internal, s.text(r, msgInternal), aerr)
	}
	now := s.now()
	if !row.ExpiresAt.After(now) || !tok.Expiration().After(now) || (row.Platform == string(oapi.Console) && !row.IdleUntil.After(now)) {
		return nil, apierr.New(apierr.TokenExpired, s.text(r, msgTokenExpired))
	}
	if cnfJKT(tok) != row.JKT {
		return nil, apierr.New(apierr.DeviceMismatch, s.text(r, msgDeviceMismatch))
	}
	if row.Platform != string(oapi.Console) || r.Header.Get("DPoP") != "" {
		if err := s.verifyDPoP(r, row.JKT, raw); err != nil {
			return nil, err
		}
	}
	sess, err := s.loadSession(r.Context(), row.SessionID)
	if err != nil {
		return nil, apierr.Wrap(apierr.Internal, s.text(r, msgInternal), err)
	}
	_, _ = s.pool.Exec(r.Context(), `UPDATE erp.identity_sessions SET last_seen_at=$2 WHERE id=$1`, row.SessionID, now.UTC())
	c := caller{Account: acct, SessionID: row.SessionID, DeviceID: row.DeviceID, JKT: row.JKT, Platform: row.Platform, BrokerID: sess.BrokerID, Version: row.Version}
	ctx := context.WithValue(r.Context(), callerCtx{}, c)
	ctx = rls.WithPrincipal(ctx, rls.Principal{UserID: acct.ID, CompanyID: acct.CompanyID, Roles: acct.Roles})
	return ctx, nil
}

func bearerOrCookie(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer "), false
	}
	if c, err := r.Cookie("erp_access"); err == nil && c.Value != "" {
		return c.Value, true
	}
	return "", false
}

func callerFrom(ctx context.Context) (caller, bool) {
	c, ok := ctx.Value(callerCtx{}).(caller)
	return c, ok
}
