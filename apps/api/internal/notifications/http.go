package notifications

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/audit"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/httpx"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/idempotency"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// DeviceHeader carries the session device until identity puts it on the principal.
const DeviceHeader = "X-Device-ID"

// Mount registers the notification routes. Call it from /api/v1.
// cmd/api owns the call site.
func Mount(r chi.Router, deps httpx.Deps, opts ...Option) error {
	svc, err := New(deps, opts...)
	if err != nil {
		return err
	}
	r.Group(func(r chi.Router) {
		r.Use(idempotency.Middleware(deps.Pool))
		r.Post("/devices/push-token", svc.registerPushToken)
		r.Delete("/devices/push-token", svc.deletePushToken)
		r.Put("/me/notification-preferences", svc.putPreferences)
		r.Post("/notifications/{event_id}/acknowledge", svc.acknowledge)
		r.Post("/alert-rules", svc.postAlertRule)
	})
	r.Get("/notifications", svc.listNotifications)
	r.Get("/me/notification-preferences", svc.getPreferences)
	r.Get("/alert-rules", svc.listAlertRules)
	r.Get("/ws", svc.serveWS)
	return nil
}

func requestLang(r *http.Request) string {
	return r.Header.Get("Accept-Language")
}

func (s *Service) registerPushToken(w http.ResponseWriter, r *http.Request) {
	p, device, ok := s.caller(w, r)
	if !ok {
		return
	}
	var body struct {
		Token      string `json:"token"`
		Platform   string `json:"platform"`
		AppVersion string `json:"app_version"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	if body.Token == "" || body.AppVersion == "" {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, text(requestLang(r), "validation.token")))
		return
	}
	if body.Platform != "android" && body.Platform != "ios" && body.Platform != "console" {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, text(requestLang(r), "validation.platform")))
		return
	}
	err := rls.Tx(r.Context(), s.Pool, p, func(tx pgx.Tx) error {
		return upsertToken(r.Context(), tx, p.CompanyID.String(), p.UserID, device, body.Token, body.Platform, body.AppVersion, s.Now())
	})
	if err != nil {
		apierr.Write(w, r, apierr.Wrap(apierr.Internal, text(requestLang(r), "validation.token"), err))
		return
	}
	httpx.JSON(w, r, http.StatusOK, struct{}{})
}

func (s *Service) deletePushToken(w http.ResponseWriter, r *http.Request) {
	p, device, ok := s.caller(w, r)
	if !ok {
		return
	}
	err := rls.Tx(r.Context(), s.Pool, p, func(tx pgx.Tx) error {
		return deleteToken(r.Context(), tx, p.UserID, device)
	})
	if err != nil {
		apierr.Write(w, r, apierr.Wrap(apierr.Internal, text(requestLang(r), "validation.token"), err))
		return
	}
	httpx.JSON(w, r, http.StatusOK, struct{}{})
}

type preferencesBody struct {
	QuietHours *struct {
		Start string `json:"start"`
		End   string `json:"end"`
		Zone  string `json:"zone"`
	} `json:"quiet_hours"`
	Channels []preferenceRow `json:"channels"`
}

func (s *Service) putPreferences(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	var body preferencesBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	err := rls.Tx(r.Context(), s.Pool, p, func(tx pgx.Tx) error {
		if body.QuietHours != nil {
			start, err := parseClock(body.QuietHours.Start)
			if err != nil {
				return apierr.New(apierr.ValidationError, text(requestLang(r), "validation.event"))
			}
			end, err := parseClock(body.QuietHours.End)
			if err != nil {
				return apierr.New(apierr.ValidationError, text(requestLang(r), "validation.event"))
			}
			zone := body.QuietHours.Zone
			if zone == "" {
				zone = "Asia/Dubai"
			}
			if _, err := time.LoadLocation(zone); err != nil {
				return apierr.New(apierr.ValidationError, text(requestLang(r), "validation.event"))
			}
			if err := saveQuiet(r.Context(), tx, p.CompanyID.String(), p.UserID, quietWindow{StartMin: start, EndMin: end, Zone: zone}); err != nil {
				return err
			}
		}
		for _, ch := range body.Channels {
			if err := savePreference(r.Context(), tx, p.CompanyID.String(), p.UserID, ch.EventType, ch.Channel, ch.DeviceID, ch.Enabled); err != nil {
				return err
			}
		}
		_, err := audit.Emit(r.Context(), tx, audit.Event{
			Type: "config.changed", ReferenceType: "notification_preferences", ReferenceID: p.UserID,
		})
		return err
	})
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, struct{}{})
}

func (s *Service) getPreferences(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	var rows []preferenceRow
	var quiet *quietWindow
	err := rls.Tx(r.Context(), s.Pool, p, func(tx pgx.Tx) error {
		var err error
		rows, err = listPreferences(r.Context(), tx, p.CompanyID.String(), p.UserID)
		if err != nil {
			return err
		}
		q, ok, err := loadQuiet(r.Context(), tx, p.CompanyID.String(), p.UserID)
		if err != nil {
			return err
		}
		if ok {
			quiet = &q
		}
		return nil
	})
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"channels": rows, "quiet_hours": quietView(quiet)})
}

func quietView(q *quietWindow) any {
	if q == nil {
		return nil
	}
	return map[string]any{
		"start": fmtMin(q.StartMin),
		"end":   fmtMin(q.EndMin),
		"zone":  q.Zone,
	}
}

func (s *Service) listNotifications(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	group := r.URL.Query().Get("group")
	if group != "" && group != "needs_me" && group != "waiting" && group != "fyi" {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, text(requestLang(r), "validation.event")))
		return
	}
	var items []map[string]any
	err := rls.Tx(r.Context(), s.Pool, p, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
			SELECT event_id, severity, payload, created_at FROM erp.notifications
			WHERE company_id = $1 AND user_id = $2 AND cleared_at IS NULL AND ($3 = '' OR grp = $3)
			ORDER BY created_at, event_id`, p.CompanyID.String(), p.UserID, group)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var eventID, severity string
			var payload []byte
			var created time.Time
			if err := rows.Scan(&eventID, &severity, &payload, &created); err != nil {
				return err
			}
			var ev Event
			if err := json.Unmarshal(payload, &ev); err != nil {
				return err
			}
			item := map[string]any{
				"event_id":        eventID,
				"severity":        severity,
				"doc_type":        ev.Subject.DocType,
				"doc_number":      ev.Subject.DocNumber,
				"party":           ev.Subject.Party,
				"amount":          ev.Amount,
				"requester":       ev.Actor,
				"waiting_since":   created.UTC().Format(time.RFC3339),
				"deep_link":       ev.DeepLink,
				"allowed_actions": ev.AllowedActions,
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	if items == nil {
		items = []map[string]any{}
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"items": items})
}

func (s *Service) acknowledge(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	eventID := chi.URLParam(r, "event_id")
	var found bool
	err := rls.Tx(r.Context(), s.Pool, p, func(tx pgx.Tx) error {
		var err error
		found, err = clearInbox(r.Context(), tx, p.CompanyID.String(), p.UserID, eventID, s.Now())
		return err
	})
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	if !found {
		apierr.Write(w, r, apierr.New(apierr.NotFound, text(requestLang(r), "notfound.notification")))
		return
	}
	s.broadcastClear(p.UserID, eventID)
	httpx.JSON(w, r, http.StatusOK, struct{}{})
}

func (s *Service) serveWS(w http.ResponseWriter, r *http.Request) {
	p, err := rls.FromContext(r.Context())
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.AuthRequired, text(requestLang(r), "auth.required")))
		return
	}
	conn, err := acceptWS(w, r)
	if err != nil {
		return
	}
	defer conn.close()
	s.Hub.add(p.UserID, conn)
	defer s.Hub.remove(p.UserID, conn)
	for {
		msg, err := conn.readText()
		if err != nil {
			return
		}
		var in struct {
			Ack string `json:"ack"`
		}
		if err := json.Unmarshal(msg, &in); err != nil || in.Ack == "" {
			continue
		}
		_ = rls.Tx(r.Context(), s.Pool, p, func(tx pgx.Tx) error {
			_, err := clearInbox(r.Context(), tx, p.CompanyID.String(), p.UserID, in.Ack, s.Now())
			return err
		})
		s.broadcastClear(p.UserID, in.Ack)
	}
}

func (s *Service) broadcastClear(userID, eventID string) {
	body, err := json.Marshal(socketMessage{
		EventID: eventID,
		Type:    "notification.cleared",
		Payload: map[string]any{"cleared": true},
		At:      s.Now(),
	})
	if err != nil {
		return
	}
	s.Hub.Publish(userID, body)
}

func (s *Service) postAlertRule(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	var body struct {
		DocumentType   string         `json:"document_type"`
		Condition      map[string]any `json:"condition"`
		RecipientRoles []string       `json:"recipient_roles"`
		Channel        string         `json:"channel"`
		Severity       string         `json:"severity"`
		Mode           string         `json:"mode"`
		Builtin        bool           `json:"builtin"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	rule := AlertRule{
		CompanyID: p.CompanyID.String(), DocumentType: body.DocumentType, Condition: body.Condition,
		RecipientRoles: body.RecipientRoles, Channel: body.Channel, Severity: body.Severity,
		Mode: body.Mode, Enabled: true, Builtin: body.Builtin,
	}
	var id uuid.UUID
	err := rls.Tx(r.Context(), s.Pool, p, func(tx pgx.Tx) error {
		if s.MasterApproval != nil {
			if err := s.MasterApproval.Request(r.Context(), tx, "alert_rule", rule); err != nil {
				return err
			}
		}
		var err error
		id, err = insertRule(r.Context(), tx, rule)
		if err != nil {
			return err
		}
		_, err = audit.Emit(r.Context(), tx, audit.Event{
			Type: "config.changed", ReferenceType: "alert_rule", ReferenceID: id.String(),
		})
		return err
	})
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"id": id.String()})
}

func (s *Service) listAlertRules(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	var rules []AlertRule
	err := rls.Tx(r.Context(), s.Pool, p, func(tx pgx.Tx) error {
		var err error
		rules, err = listRules(r.Context(), tx, p.CompanyID.String(), r.URL.Query().Get("document_type"))
		return err
	})
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"items": rules})
}

func (s *Service) principal(w http.ResponseWriter, r *http.Request) (rls.Principal, bool) {
	p, err := rls.FromContext(r.Context())
	if err != nil || p.UserID == "" {
		apierr.Write(w, r, apierr.New(apierr.AuthRequired, text(requestLang(r), "auth.required")))
		return rls.Principal{}, false
	}
	return p, true
}

func (s *Service) caller(w http.ResponseWriter, r *http.Request) (rls.Principal, string, bool) {
	p, ok := s.principal(w, r)
	if !ok {
		return rls.Principal{}, "", false
	}
	device := r.Header.Get(DeviceHeader)
	if device == "" {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, text(requestLang(r), "validation.device")))
		return rls.Principal{}, "", false
	}
	return p, device, true
}

func parseClock(s string) (int, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, errInvalid
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, err
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, err
	}
	if h < 0 || h > 24 || m < 0 || m > 59 || (h == 24 && m != 0) {
		return 0, errInvalid
	}
	return h*60 + m, nil
}

func fmtMin(m int) string {
	if m < 0 {
		m = 0
	}
	return strconv.Itoa(m/60) + ":" + two(m%60)
}

func two(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}
