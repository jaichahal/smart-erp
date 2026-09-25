package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/canon"
)

type tokenRow struct {
	Token    string
	Platform string
	DeviceID string
}

func upsertToken(ctx context.Context, tx pgx.Tx, company, userID, deviceID, token, platform, appVersion string, seen time.Time) error {
	if _, err := tx.Exec(ctx, `DELETE FROM erp.device_tokens WHERE token = $1 AND NOT (user_id = $2 AND device_id = $3)`, token, userID, deviceID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO erp.device_tokens (company_id, user_id, device_id, token, platform, app_version, last_seen_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (user_id, device_id) DO UPDATE
		SET token = EXCLUDED.token,
		    platform = EXCLUDED.platform,
		    app_version = EXCLUDED.app_version,
		    company_id = EXCLUDED.company_id,
		    last_seen_at = EXCLUDED.last_seen_at`,
		company, userID, deviceID, token, platform, appVersion, seen)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			_, err = tx.Exec(ctx, `DELETE FROM erp.device_tokens WHERE token = $1 AND NOT (user_id = $2 AND device_id = $3)`, token, userID, deviceID)
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `
				INSERT INTO erp.device_tokens (company_id, user_id, device_id, token, platform, app_version, last_seen_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7)
				ON CONFLICT (user_id, device_id) DO UPDATE
				SET token = EXCLUDED.token, platform = EXCLUDED.platform, app_version = EXCLUDED.app_version,
				    company_id = EXCLUDED.company_id, last_seen_at = EXCLUDED.last_seen_at`,
				company, userID, deviceID, token, platform, appVersion, seen)
		}
	}
	return err
}

func deleteToken(ctx context.Context, tx pgx.Tx, userID, deviceID string) error {
	_, err := tx.Exec(ctx, `DELETE FROM erp.device_tokens WHERE user_id = $1 AND device_id = $2`, userID, deviceID)
	return err
}

func deleteTokenValue(ctx context.Context, tx pgx.Tx, token string) error {
	_, err := tx.Exec(ctx, `DELETE FROM erp.device_tokens WHERE token = $1`, token)
	return err
}

func listTokens(ctx context.Context, tx pgx.Tx, company, userID string) ([]tokenRow, error) {
	rows, err := tx.Query(ctx, `SELECT token, platform, device_id FROM erp.device_tokens WHERE company_id = $1 AND user_id = $2`, company, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []tokenRow
	for rows.Next() {
		var t tokenRow
		if err := rows.Scan(&t.Token, &t.Platform, &t.DeviceID); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// PruneTokens deletes tokens unseen for 60 days (E7, R13.6).
func PruneTokens(ctx context.Context, tx pgx.Tx, now time.Time) (int64, error) {
	cutoff := now.Add(-60 * 24 * time.Hour)
	tag, err := tx.Exec(ctx, `DELETE FROM erp.device_tokens WHERE last_seen_at <= $1`, cutoff)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func channelEnabled(ctx context.Context, tx pgx.Tx, company, userID, eventType, channel, deviceID string) (bool, error) {
	var enabled bool
	err := tx.QueryRow(ctx, `
		SELECT enabled FROM erp.notification_preferences
		WHERE company_id = $1 AND user_id = $2
		  AND event_type IN ($3, '*')
		  AND channel = $4
		  AND device_id IN ($5, '*')
		ORDER BY (event_type = $3) DESC, (device_id = $5) DESC
		LIMIT 1`, company, userID, eventType, channel, deviceID).Scan(&enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	return enabled, err
}

type quietWindow struct {
	StartMin int
	EndMin   int
	Zone     string
}

func loadQuiet(ctx context.Context, tx pgx.Tx, company, userID string) (quietWindow, bool, error) {
	var q quietWindow
	err := tx.QueryRow(ctx, `SELECT start_min, end_min, zone FROM erp.notification_quiet_hours WHERE company_id = $1 AND user_id = $2`, company, userID).
		Scan(&q.StartMin, &q.EndMin, &q.Zone)
	if errors.Is(err, pgx.ErrNoRows) {
		return quietWindow{}, false, nil
	}
	return q, err == nil, err
}

func inQuiet(q quietWindow, now time.Time) bool {
	loc, err := time.LoadLocation(q.Zone)
	if err != nil {
		loc = time.UTC
	}
	local := now.In(loc)
	minute := local.Hour()*60 + local.Minute()
	if q.StartMin == q.EndMin {
		return false
	}
	if q.StartMin < q.EndMin {
		return minute >= q.StartMin && minute < q.EndMin
	}
	return minute >= q.StartMin || minute < q.EndMin
}

func saveQuiet(ctx context.Context, tx pgx.Tx, company, userID string, q quietWindow) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO erp.notification_quiet_hours (company_id, user_id, start_min, end_min, zone)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (company_id, user_id) DO UPDATE
		SET start_min = EXCLUDED.start_min, end_min = EXCLUDED.end_min, zone = EXCLUDED.zone`,
		company, userID, q.StartMin, q.EndMin, q.Zone)
	return err
}

func savePreference(ctx context.Context, tx pgx.Tx, company, userID, eventType, channel, deviceID string, enabled bool) error {
	if eventType == "" {
		eventType = "*"
	}
	if deviceID == "" {
		deviceID = "*"
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO erp.notification_preferences (company_id, user_id, event_type, channel, device_id, enabled)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (company_id, user_id, event_type, channel, device_id) DO UPDATE
		SET enabled = EXCLUDED.enabled, updated_at = clock_timestamp()`,
		company, userID, eventType, channel, deviceID, enabled)
	return err
}

type preferenceRow struct {
	EventType string `json:"event_type"`
	Channel   string `json:"channel"`
	DeviceID  string `json:"device_id"`
	Enabled   bool   `json:"enabled"`
}

func listPreferences(ctx context.Context, tx pgx.Tx, company, userID string) ([]preferenceRow, error) {
	rows, err := tx.Query(ctx, `SELECT event_type, channel, device_id, enabled FROM erp.notification_preferences WHERE company_id = $1 AND user_id = $2 ORDER BY event_type, channel, device_id`, company, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []preferenceRow
	for rows.Next() {
		var p preferenceRow
		if err := rows.Scan(&p.EventType, &p.Channel, &p.DeviceID, &p.Enabled); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func markConsumed(ctx context.Context, tx pgx.Tx, eventID, company string) (bool, error) {
	tag, err := tx.Exec(ctx, `INSERT INTO erp.notification_dedupe (event_id, company_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, eventID, company)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func alreadyConsumed(ctx context.Context, tx pgx.Tx, eventID string) (bool, error) {
	var n int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM erp.notification_dedupe WHERE event_id = $1`, eventID).Scan(&n)
	return n > 0, err
}

func insertInbox(ctx context.Context, tx pgx.Tx, company, userID string, ev Event, group string) error {
	payload, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO erp.notifications (company_id, user_id, event_id, event_type, severity, grp, payload)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (company_id, user_id, event_id) DO NOTHING`,
		company, userID, ev.EventID, ev.Type, ev.Severity, group, payload)
	return err
}

func clearInbox(ctx context.Context, tx pgx.Tx, company, userID, eventID string, at time.Time) (bool, error) {
	tag, err := tx.Exec(ctx, `UPDATE erp.notifications SET cleared_at = $4, acked_at = $4 WHERE company_id = $1 AND user_id = $2 AND event_id = $3 AND cleared_at IS NULL`,
		company, userID, eventID, at)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() > 0 {
		return true, nil
	}
	var n int
	err = tx.QueryRow(ctx, `SELECT count(*) FROM erp.notifications WHERE company_id = $1 AND user_id = $2 AND event_id = $3`, company, userID, eventID).Scan(&n)
	return n > 0, err
}

func queueDigest(ctx context.Context, tx pgx.Tx, company, userID string, ev Event) error {
	payload, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO erp.notification_digest_items (company_id, user_id, event_id, payload)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT DO NOTHING`, company, userID, ev.EventID, payload)
	return err
}

type digestBatch struct {
	UserID   string
	EventIDs []string
	Payloads []json.RawMessage
}

func unflushedDigest(ctx context.Context, tx pgx.Tx, company string) ([]digestBatch, error) {
	rows, err := tx.Query(ctx, `
		SELECT user_id, event_id, payload FROM erp.notification_digest_items
		WHERE company_id = $1 AND flushed_at IS NULL
		ORDER BY user_id, queued_at`, company)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byUser := map[string]*digestBatch{}
	var order []string
	for rows.Next() {
		var user, eventID string
		var payload []byte
		if err := rows.Scan(&user, &eventID, &payload); err != nil {
			return nil, err
		}
		b, ok := byUser[user]
		if !ok {
			b = &digestBatch{UserID: user}
			byUser[user] = b
			order = append(order, user)
		}
		b.EventIDs = append(b.EventIDs, eventID)
		b.Payloads = append(b.Payloads, payload)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]digestBatch, 0, len(order))
	for _, id := range order {
		out = append(out, *byUser[id])
	}
	return out, nil
}

func markDigestFlushed(ctx context.Context, tx pgx.Tx, company, userID string, at time.Time) error {
	_, err := tx.Exec(ctx, `UPDATE erp.notification_digest_items SET flushed_at = $3 WHERE company_id = $1 AND user_id = $2 AND flushed_at IS NULL`, company, userID, at)
	return err
}

type delivery struct {
	EventID   string
	Channel   string
	UserID    string
	Status    string
	Attempt   int
	ErrorCode string
}

func recordDelivery(ctx context.Context, tx pgx.Tx, company string, d delivery, at time.Time) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7232, hashtext($1))`, company); err != nil {
		return err
	}
	var seq int64
	var prev string
	err := tx.QueryRow(ctx, `SELECT chain_seq, hash FROM erp.delivery_log WHERE company_id = $1 ORDER BY chain_seq DESC LIMIT 1`, company).Scan(&seq, &prev)
	if errors.Is(err, pgx.ErrNoRows) {
		seq, prev, err = 0, "", nil
	}
	if err != nil {
		return err
	}
	id := uuid.New()
	body := map[string]any{
		"id": id.String(), "company_id": company, "event_id": d.EventID, "channel": d.Channel,
		"recipient_user_id": d.UserID, "status": d.Status, "attempt": d.Attempt, "error": d.ErrorCode,
		"occurred_at": at.UTC(),
	}
	canonical, hash, err := canon.MarshalAndHash(body, prev)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO erp.delivery_log
		(id, company_id, event_id, channel, recipient_user_id, status, attempt, error, occurred_at, canonical, chain_seq, prev_hash, hash)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		id, company, d.EventID, d.Channel, d.UserID, d.Status, d.Attempt, d.ErrorCode, at, string(canonical), seq+1, prev, hash)
	return err
}

func deliverySent(ctx context.Context, tx pgx.Tx, company, eventID, userID, channel string) (bool, error) {
	var n int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM erp.delivery_log WHERE company_id = $1 AND event_id = $2 AND recipient_user_id = $3 AND channel = $4 AND status = 'sent'`,
		company, eventID, userID, channel).Scan(&n)
	return n > 0, err
}

// AlertRule is one admin-editable row (R13.7, E12). Built-in alerts are rows
// with Builtin set.
type AlertRule struct {
	ID             uuid.UUID
	CompanyID      string
	DocumentType   string
	Condition      map[string]any
	RecipientRoles []string
	Channel        string
	Severity       string
	Mode           string
	Enabled        bool
	Builtin        bool
	StateVersion   int
}

// ErrBlockingAlert refuses submission when a blocking rule matches (E12).
var ErrBlockingAlert = errors.New("blocking alert")

func insertRule(ctx context.Context, tx pgx.Tx, rule AlertRule) (uuid.UUID, error) {
	if rule.ID == uuid.Nil {
		rule.ID = uuid.New()
	}
	cond, err := json.Marshal(rule.Condition)
	if err != nil {
		return uuid.Nil, err
	}
	if rule.Mode != "blocking" && rule.Mode != "advisory" {
		return uuid.Nil, fmt.Errorf("notifications: alert mode: %w", errInvalid)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO erp.alert_rules
		(id, company_id, document_type, condition, recipient_roles, channel, severity, mode, enabled, builtin)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		rule.ID, rule.CompanyID, rule.DocumentType, cond, rule.RecipientRoles, rule.Channel, rule.Severity, rule.Mode, rule.Enabled, rule.Builtin)
	return rule.ID, err
}

func listRules(ctx context.Context, tx pgx.Tx, company, docType string) ([]AlertRule, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, company_id, document_type, condition, recipient_roles, channel, severity, mode, enabled, builtin, state_version
		FROM erp.alert_rules
		WHERE company_id = $1 AND ($2 = '' OR document_type = $2) AND enabled`, company, docType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AlertRule
	for rows.Next() {
		var rule AlertRule
		var cond []byte
		if err := rows.Scan(&rule.ID, &rule.CompanyID, &rule.DocumentType, &cond, &rule.RecipientRoles, &rule.Channel, &rule.Severity, &rule.Mode, &rule.Enabled, &rule.Builtin, &rule.StateVersion); err != nil {
			return nil, err
		}
		rule.Condition = map[string]any{}
		if err := json.Unmarshal(cond, &rule.Condition); err != nil {
			return nil, err
		}
		out = append(out, rule)
	}
	return out, rows.Err()
}

func ruleMatches(rule AlertRule, amount string) bool {
	if !rule.Enabled {
		return false
	}
	op, _ := rule.Condition["op"].(string)
	if op == "" || op == "always" {
		return true
	}
	if op != "amount_gt" {
		return false
	}
	value, _ := rule.Condition["value"].(string)
	return decimalGreater(amount, value)
}

func decimalGreater(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return cmpDecimal(a, b) > 0
}

func cmpDecimal(a, b string) int {
	// Compare decimal strings without a third-party library.
	af, bf := splitDec(a), splitDec(b)
	if af.neg != bf.neg {
		if af.neg {
			return -1
		}
		return 1
	}
	sign := 1
	if af.neg {
		sign = -1
	}
	ai, bi := trimZeros(af.intp), trimZeros(bf.intp)
	if len(ai) != len(bi) {
		if len(ai) > len(bi) {
			return sign
		}
		return -sign
	}
	if ai != bi {
		if ai > bi {
			return sign
		}
		return -sign
	}
	frac := func(s string) string {
		for len(s) < 8 {
			s += "0"
		}
		if len(s) > 8 {
			s = s[:8]
		}
		return s
	}
	fa, fb := frac(af.frac), frac(bf.frac)
	if fa == fb {
		return 0
	}
	if fa > fb {
		return sign
	}
	return -sign
}

type decParts struct {
	neg  bool
	intp string
	frac string
}

func splitDec(s string) decParts {
	neg := false
	if s != "" && s[0] == '-' {
		neg = true
		s = s[1:]
	}
	intp, frac := s, ""
	if i := indexByte(s, '.'); i >= 0 {
		intp, frac = s[:i], s[i+1:]
	}
	if intp == "" {
		intp = "0"
	}
	return decParts{neg: neg, intp: intp, frac: frac}
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

func trimZeros(s string) string {
	for len(s) > 1 && s[0] == '0' {
		s = s[1:]
	}
	return s
}

func insertSubmission(ctx context.Context, tx pgx.Tx, company, docType, docID, state string) (uuid.UUID, error) {
	id := uuid.New()
	_, err := tx.Exec(ctx, `INSERT INTO erp.notification_submissions (id, company_id, document_type, document_id, state) VALUES ($1,$2,$3,$4,$5)`,
		id, company, docType, docID, state)
	return id, err
}
