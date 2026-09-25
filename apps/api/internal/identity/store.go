package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/lestrrat-go/jwx/v2/jwt"
	"github.com/riverqueue/river"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/audit"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/outbox"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

type loginRow struct {
	ID          uuid.UUID
	LoginName   string
	UserID      string
	CompanyID   uuid.UUID
	BrokerID    string
	PasswordOK  bool
	SecondOK    bool
	MFARequired bool
	Shadow      bool
	ExpiresAt   time.Time
}

type deviceRow struct {
	ID        uuid.UUID
	UserID    string
	CompanyID uuid.UUID
	JKT       string
	Platform  string
	Name      string
	Revoked   bool
}

type sessionView struct {
	ID         uuid.UUID
	UserID     string
	CompanyID  uuid.UUID
	DeviceID   uuid.UUID
	BrokerID   string
	CreatedAt  time.Time
	LastSeen   time.Time
	Ended      bool
	Version    int
	IdleUntil  time.Time
	DeviceName string
	Platform   string
	JKT        string
	DevRevoked bool
}

type accessView struct {
	JTI        string
	SessionID  uuid.UUID
	DeviceID   uuid.UUID
	UserID     string
	CompanyID  uuid.UUID
	ExpiresAt  time.Time
	Revoked    bool
	Ended      bool
	DevRevoked bool
	Platform   string
	JKT        string
	IdleUntil  time.Time
	Version    int
}

type refreshRow struct {
	ID        uuid.UUID
	SessionID uuid.UUID
	DeviceID  uuid.UUID
	UserID    string
	ExpiresAt time.Time
	Absolute  time.Time
	Used      bool
	Revoked   bool
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func randomToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func (s *Service) rememberJTI(ctx context.Context, jti string, exp time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx, `INSERT INTO erp.identity_dpop_jti(jti, expires_at) VALUES ($1,$2) ON CONFLICT DO NOTHING`, jti, exp.UTC())
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 0, nil
}

func (s *Service) allow(ctx context.Context, key string, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	start := s.now().UTC().Truncate(s.policy.RateWindow)
	var hits int
	err := s.pool.QueryRow(ctx, `INSERT INTO erp.identity_rate_buckets(bucket_key, window_start, hits) VALUES ($1,$2,1)
		ON CONFLICT (bucket_key, window_start) DO UPDATE SET hits = erp.identity_rate_buckets.hits + 1
		RETURNING hits`, key, start).Scan(&hits)
	if err != nil {
		return 0, err
	}
	if hits <= limit {
		return 0, nil
	}
	retry := int(start.Add(s.policy.RateWindow).Sub(s.now()) / time.Second)
	if retry < 1 {
		retry = 1
	}
	return retry, nil
}

func (s *Service) locked(ctx context.Context, login string) (bool, error) {
	var until *time.Time
	err := s.pool.QueryRow(ctx, `SELECT locked_until FROM erp.identity_lockouts WHERE login_name=$1`, login).Scan(&until)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return until != nil && until.After(s.now()), nil
}

func (s *Service) noteFailure(ctx context.Context, login string) error {
	p := rls.Principal{UserID: login, CompanyID: platformCompanyID}
	return rls.Tx(ctx, s.pool, sqlPrincipal(p), func(tx pgx.Tx) error {
		var failures int
		var until *time.Time
		err := tx.QueryRow(ctx, `SELECT failures, locked_until FROM erp.identity_lockouts WHERE login_name=$1 FOR UPDATE`, login).Scan(&failures, &until)
		now := s.now()
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			failures = 0
		case err != nil:
			return err
		case until != nil && !until.After(now):
			failures = 0
		}
		failures++
		var locked any
		if failures >= s.policy.FailuresBeforeLock {
			extra := failures - s.policy.FailuresBeforeLock
			if extra > 6 {
				extra = 6
			}
			locked = now.Add(s.policy.LockFor << extra)
		}
		_, err = tx.Exec(ctx, `INSERT INTO erp.identity_lockouts(login_name, failures, locked_until) VALUES ($1,$2,$3)
			ON CONFLICT (login_name) DO UPDATE SET failures = $2, locked_until = $3`, login, failures, locked)
		return err
	})
}

// Unlock clears the local lockout so a correct password can succeed again (A8).
func (s *Service) Unlock(ctx context.Context, login string) error {
	_, err := s.pool.Exec(ctx, `UPDATE erp.identity_lockouts SET failures = 0, locked_until = NULL WHERE login_name = $1`, login)
	return err
}

func (s *Service) clearFailures(ctx context.Context, login string) {
	_, _ = s.pool.Exec(ctx, `UPDATE erp.identity_lockouts SET failures = 0, locked_until = NULL WHERE login_name = $1`, login)
}

func (s *Service) rollbackAttempt(ctx context.Context, p rls.Principal, login, cause string) error {
	tx, err := rls.Begin(ctx, s.pool, sqlPrincipal(p))
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO erp.identity_auth_attempts(id, login_name, cause) VALUES ($1,$2,$3)`, uuid.New(), login, cause); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Rollback(ctx)
}

func (s *Service) failLogin(w http.ResponseWriter, r *http.Request, login, cause string, acct *Account) {
	p := rls.Principal{UserID: login, CompanyID: platformCompanyID}
	if acct != nil && acct.ID != "" {
		p = rls.Principal{UserID: acct.ID, CompanyID: acct.CompanyID, Roles: acct.Roles}
		if p.CompanyID == uuid.Nil {
			p.CompanyID = platformCompanyID
		}
	}
	ctx := r.Context()
	if err := s.rollbackAttempt(ctx, p, login, cause); err != nil {
		s.log.Error("rollback attempt", "err", err)
		apierr.Write(w, r, apierr.Wrap(apierr.Internal, s.text(r, msgInternal), err))
		return
	}
	if _, err := audit.EmitCommitted(ctx, s.pool, sqlPrincipal(p), audit.Event{
		Type: "auth.login_failed", ReferenceType: "login", ReferenceID: login, Reason: cause,
	}); err != nil {
		s.log.Error("audit login failure", "err", err)
		apierr.Write(w, r, apierr.Wrap(apierr.Internal, s.text(r, msgInternal), err))
		return
	}
	if err := s.noteFailure(ctx, login); err != nil {
		s.log.WarnContext(ctx, "lockout counter", "err", err)
	}
	apierr.Write(w, r, apierr.New(apierr.AuthRequired, s.text(r, msgAuthFailed)))
}

func (s *Service) insertLogin(ctx context.Context, row loginRow) error {
	p := rls.Principal{UserID: row.LoginName, CompanyID: platformCompanyID}
	if row.UserID != "" {
		p.UserID = row.UserID
		p.CompanyID = row.CompanyID
	}
	return rls.Tx(ctx, s.pool, sqlPrincipal(p), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO erp.identity_login_sessions(id, login_name, user_id, company_id, broker_session_id, password_ok, second_ok, mfa_required, shadow, expires_at)
			VALUES ($1,$2,NULLIF($3,''),$4,$5,$6,$7,$8,$9,$10)`,
			row.ID, row.LoginName, row.UserID, nullUUID(row.CompanyID), row.BrokerID, row.PasswordOK, row.SecondOK, row.MFARequired, row.Shadow, row.ExpiresAt.UTC())
		return err
	})
}

func nullUUID(id uuid.UUID) any {
	if id == uuid.Nil {
		return nil
	}
	return id
}

func (s *Service) loadLogin(ctx context.Context, id uuid.UUID) (loginRow, error) {
	var row loginRow
	var company *uuid.UUID
	var user *string
	err := s.pool.QueryRow(ctx, `SELECT id, login_name, user_id, company_id, broker_session_id, password_ok, second_ok, mfa_required, shadow, expires_at
		FROM erp.identity_login_sessions WHERE id=$1`, id).Scan(
		&row.ID, &row.LoginName, &user, &company, &row.BrokerID, &row.PasswordOK, &row.SecondOK, &row.MFARequired, &row.Shadow, &row.ExpiresAt)
	if user != nil {
		row.UserID = *user
	}
	if company != nil {
		row.CompanyID = *company
	}
	return row, err
}

func (s *Service) markLogin(ctx context.Context, id uuid.UUID, password, second bool) error {
	_, err := s.pool.Exec(ctx, `UPDATE erp.identity_login_sessions SET password_ok = password_ok OR $2, second_ok = second_ok OR $3 WHERE id=$1`, id, password, second)
	return err
}

func (s *Service) loadDevice(ctx context.Context, id uuid.UUID) (deviceRow, error) {
	var row deviceRow
	var user *string
	var company *uuid.UUID
	var revoked *time.Time
	err := s.pool.QueryRow(ctx, `SELECT id, user_id, company_id, jkt, platform, device_name, revoked_at FROM erp.identity_devices WHERE id=$1`, id).
		Scan(&row.ID, &user, &company, &row.JKT, &row.Platform, &row.Name, &revoked)
	if user != nil {
		row.UserID = *user
	}
	if company != nil {
		row.CompanyID = *company
	}
	row.Revoked = revoked != nil
	return row, err
}

func (s *Service) bindDevice(ctx context.Context, tx pgx.Tx, id uuid.UUID, userID string, company uuid.UUID) error {
	tag, err := tx.Exec(ctx, `UPDATE erp.identity_devices SET user_id=$2, company_id=$3 WHERE id=$1 AND revoked_at IS NULL AND (user_id IS NULL OR user_id=$2)`, id, userID, company)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errDeviceBound
	}
	return nil
}

var errDeviceBound = errors.New("device bound to another user")

func (s *Service) enqueue(ctx context.Context, tx pgx.Tx, args river.JobArgs) error {
	if s.river == nil {
		return nil
	}
	_, err := outbox.InsertTx(ctx, s.river, tx, args, nil)
	return err
}

func (s *Service) issuePair(ctx context.Context, tx pgx.Tx, acct Account, device deviceRow, brokerID string, absolute time.Time) (sessionView, string, string, int, error) {
	if err := s.enforceCap(ctx, tx, acct.ID); err != nil {
		return sessionView{}, "", "", 0, err
	}
	if err := s.bindDevice(ctx, tx, device.ID, acct.ID, acct.CompanyID); err != nil {
		return sessionView{}, "", "", 0, err
	}
	now := s.now()
	sid := uuid.New()
	idle := now.Add(s.policy.ConsoleIdle)
	_, err := tx.Exec(ctx, `INSERT INTO erp.identity_sessions(id, user_id, company_id, device_id, broker_session_id, created_at, last_seen_at, state_version, idle_expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$6,1,$7)`, sid, acct.ID, acct.CompanyID, device.ID, brokerID, now.UTC(), idle.UTC())
	if err != nil {
		return sessionView{}, "", "", 0, err
	}
	access, refresh, expIn, err := s.mint(ctx, tx, acct, sid, device, absolute, now)
	if err != nil {
		return sessionView{}, "", "", 0, err
	}
	if err := s.enqueue(ctx, tx, sessionStartedArgs{SessionID: sid.String(), UserID: acct.ID, DeviceID: device.ID.String()}); err != nil {
		return sessionView{}, "", "", 0, err
	}
	view := sessionView{ID: sid, UserID: acct.ID, CompanyID: acct.CompanyID, DeviceID: device.ID, BrokerID: brokerID, CreatedAt: now, LastSeen: now, Version: 1, IdleUntil: idle, DeviceName: device.Name, Platform: device.Platform, JKT: device.JKT}
	return view, access, refresh, expIn, nil
}

func (s *Service) mint(ctx context.Context, tx pgx.Tx, acct Account, sid uuid.UUID, device deviceRow, absolute time.Time, now time.Time) (string, string, int, error) {
	jti := uuid.NewString()
	exp := now.Add(s.policy.accessTTL())
	rawAccess, err := s.accessJWT(ctx, acct, sid.String(), device.ID.String(), device.JKT, jti, exp)
	if err != nil {
		return "", "", 0, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO erp.identity_access_tokens(jti, session_id, device_id, user_id, expires_at) VALUES ($1,$2,$3,$4,$5)`,
		jti, sid, device.ID, acct.ID, exp.UTC()); err != nil {
		return "", "", 0, err
	}
	refresh, err := randomToken()
	if err != nil {
		return "", "", 0, err
	}
	if absolute.IsZero() {
		absolute = now.Add(s.policy.RefreshAbsolute)
	}
	sliding := now.Add(s.policy.RefreshSliding)
	if sliding.After(absolute) {
		sliding = absolute
	}
	if _, err := tx.Exec(ctx, `INSERT INTO erp.identity_refresh_tokens(id, session_id, device_id, user_id, token_hash, issued_at, expires_at, absolute_expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, uuid.New(), sid, device.ID, acct.ID, hashToken(refresh), now.UTC(), sliding.UTC(), absolute.UTC()); err != nil {
		return "", "", 0, err
	}
	return rawAccess, refresh, int(s.policy.accessTTL().Seconds()), nil
}

func (s *Service) accessJWT(ctx context.Context, acct Account, sid, did, jkt, jti string, exp time.Time) (string, error) {
	tok, err := jwt.NewBuilder().
		Issuer(s.issuer).
		Audience([]string{s.audience}).
		Subject(acct.ID).
		IssuedAt(s.now()).
		Expiration(exp).
		JwtID(jti).
		Claim("sid", sid).
		Claim("device_id", did).
		Claim("company_id", acct.CompanyID.String()).
		Claim("roles", rolesOrEmpty(acct.Roles)).
		Claim("personas", rolesOrEmpty(acct.Personas)).
		Claim("name", acct.Name).
		Claim("cnf", map[string]string{"jkt": jkt}).
		Build()
	if err != nil {
		return "", err
	}
	return s.kms.sign(ctx, tok)
}

func (s *Service) enforceCap(ctx context.Context, tx pgx.Tx, userID string) error {
	for {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM erp.identity_sessions WHERE user_id=$1 AND ended_at IS NULL`, userID).Scan(&n); err != nil {
			return err
		}
		if n < s.policy.MaxSessions {
			return nil
		}
		var oldest uuid.UUID
		var owner string
		if err := tx.QueryRow(ctx, `SELECT id, user_id FROM erp.identity_sessions WHERE user_id=$1 AND ended_at IS NULL ORDER BY created_at ASC, seq ASC LIMIT 1`, userID).Scan(&oldest, &owner); err != nil {
			return err
		}
		if err := s.endSessionTx(ctx, tx, oldest, owner); err != nil {
			return err
		}
	}
}

func (s *Service) endSessionTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, userID string) error {
	now := s.now()
	tag, err := tx.Exec(ctx, `UPDATE erp.identity_sessions SET ended_at=$2, state_version=state_version+1 WHERE id=$1 AND ended_at IS NULL`, id, now.UTC())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	if _, err := tx.Exec(ctx, `UPDATE erp.identity_refresh_tokens SET revoked_at=$2 WHERE session_id=$1 AND revoked_at IS NULL`, id, now.UTC()); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE erp.identity_access_tokens SET revoked_at=$2 WHERE session_id=$1 AND revoked_at IS NULL`, id, now.UTC()); err != nil {
		return err
	}
	return s.enqueue(ctx, tx, sessionEndedArgs{SessionID: id.String(), UserID: userID})
}

func (s *Service) revokeDeviceTx(ctx context.Context, tx pgx.Tx, deviceID uuid.UUID) error {
	now := s.now()
	if _, err := tx.Exec(ctx, `UPDATE erp.identity_devices SET revoked_at=$2 WHERE id=$1 AND revoked_at IS NULL`, deviceID, now.UTC()); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT id, user_id FROM erp.identity_sessions WHERE device_id=$1 AND ended_at IS NULL`, deviceID)
	if err != nil {
		return err
	}
	type pair struct {
		id   uuid.UUID
		user string
	}
	var open []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.id, &p.user); err != nil {
			rows.Close()
			return err
		}
		open = append(open, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, p := range open {
		if err := s.endSessionTx(ctx, tx, p.id, p.user); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE erp.identity_access_tokens SET revoked_at=$2 WHERE device_id=$1 AND revoked_at IS NULL`, deviceID, now.UTC())
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE erp.identity_refresh_tokens SET revoked_at=$2 WHERE device_id=$1 AND revoked_at IS NULL`, deviceID, now.UTC())
	return err
}

func (s *Service) loadRefresh(ctx context.Context, tx pgx.Tx, raw string) (refreshRow, error) {
	var row refreshRow
	var used, revoked *time.Time
	err := tx.QueryRow(ctx, `SELECT id, session_id, device_id, user_id, expires_at, absolute_expires_at, used_at, revoked_at
		FROM erp.identity_refresh_tokens WHERE token_hash=$1 FOR UPDATE`, hashToken(raw)).Scan(
		&row.ID, &row.SessionID, &row.DeviceID, &row.UserID, &row.ExpiresAt, &row.Absolute, &used, &revoked)
	row.Used = used != nil
	row.Revoked = revoked != nil
	return row, err
}

func (s *Service) loadAccess(ctx context.Context, jti string) (accessView, error) {
	var row accessView
	var revoked, ended, devRevoked *time.Time
	err := s.pool.QueryRow(ctx, `SELECT a.jti, a.session_id, a.device_id, a.user_id, s.company_id, a.expires_at, a.revoked_at, s.ended_at, d.revoked_at, d.platform, d.jkt, s.idle_expires_at, s.state_version
		FROM erp.identity_access_tokens a
		JOIN erp.identity_sessions s ON s.id = a.session_id
		JOIN erp.identity_devices d ON d.id = a.device_id
		WHERE a.jti=$1`, jti).Scan(&row.JTI, &row.SessionID, &row.DeviceID, &row.UserID, &row.CompanyID, &row.ExpiresAt, &revoked, &ended, &devRevoked, &row.Platform, &row.JKT, &row.IdleUntil, &row.Version)
	row.Revoked = revoked != nil
	row.Ended = ended != nil
	row.DevRevoked = devRevoked != nil
	return row, err
}

func (s *Service) loadSession(ctx context.Context, id uuid.UUID) (sessionView, error) {
	var row sessionView
	var ended *time.Time
	var devRevoked *time.Time
	err := s.pool.QueryRow(ctx, `SELECT s.id, s.user_id, s.company_id, s.device_id, s.broker_session_id, s.created_at, s.last_seen_at, s.ended_at, s.state_version, s.idle_expires_at, d.device_name, d.platform, d.jkt, d.revoked_at
		FROM erp.identity_sessions s JOIN erp.identity_devices d ON d.id = s.device_id WHERE s.id=$1`, id).Scan(
		&row.ID, &row.UserID, &row.CompanyID, &row.DeviceID, &row.BrokerID, &row.CreatedAt, &row.LastSeen, &ended, &row.Version, &row.IdleUntil, &row.DeviceName, &row.Platform, &row.JKT, &devRevoked)
	row.Ended = ended != nil
	row.DevRevoked = devRevoked != nil
	return row, err
}

func cnfJKT(tok jwt.Token) string {
	v, ok := tok.Get("cnf")
	if !ok {
		return ""
	}
	switch t := v.(type) {
	case map[string]string:
		return t["jkt"]
	case map[string]any:
		s, _ := t["jkt"].(string)
		return s
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return ""
		}
		var m map[string]string
		if json.Unmarshal(b, &m) != nil {
			return ""
		}
		return m["jkt"]
	}
}
