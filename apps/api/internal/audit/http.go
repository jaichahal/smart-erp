package audit

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/httpx"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/idempotency"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/oapi"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// Mount registers POST /audit/verify and GET /audit/events on the API router.
func Mount(r chi.Router, deps httpx.Deps) {
	svc := NewFromDeps(deps)
	r.With(idempotency.Middleware(deps.Pool)).Post("/audit/verify", svc.handleVerify)
	r.Get("/audit/events", svc.handleList)
}

func (s *Service) handleVerify(w http.ResponseWriter, r *http.Request) {
	p, err := requireRoles(r, "auditor", "system_manager")
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	res, err := s.Verify(r.Context(), p.CompanyID)
	if err != nil {
		apierr.Write(w, r, apierr.Wrap(apierr.Internal, Text(requestLang(r), "error.internal"), err))
		return
	}
	httpx.JSON(w, r, http.StatusOK, res)
}

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	p, err := requireRoles(r, "auditor", "system_manager", "stakeholder", "accountant")
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	q := r.URL.Query()
	limit := 50
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			apierr.Write(w, r, apierr.New(apierr.ValidationError, Text(requestLang(r), "error.validation")))
			return
		}
		limit = n
	}
	var cursor int64
	if raw := q.Get("cursor"); raw != "" {
		b, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil {
			apierr.Write(w, r, apierr.New(apierr.ValidationError, Text(requestLang(r), "error.validation")))
			return
		}
		cursor, err = strconv.ParseInt(string(b), 10, 64)
		if err != nil {
			apierr.Write(w, r, apierr.New(apierr.ValidationError, Text(requestLang(r), "error.validation")))
			return
		}
	}
	var from, to *time.Time
	for _, pair := range []struct {
		name string
		dst  **time.Time
	}{{"from", &from}, {"to", &to}} {
		if raw := q.Get(pair.name); raw != "" {
			t, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				apierr.Write(w, r, apierr.New(apierr.ValidationError, Text(requestLang(r), "error.validation")))
				return
			}
			*pair.dst = &t
		}
	}
	rows, err := s.Pool.Query(r.Context(), `SELECT id::text, event_type, occurred_at, actor_id, reference_id, before_state, after_state, chain_seq, prev_hash, hash
		FROM erp.audit_events
		WHERE company_id=$1
		  AND ($2 = '' OR event_type = $2)
		  AND ($3 = '' OR reference_id = $3 OR reference_type = $3)
		  AND ($4::timestamptz IS NULL OR occurred_at >= $4)
		  AND ($5::timestamptz IS NULL OR occurred_at <= $5)
		  AND ($6 = 0 OR chain_seq < $6)
		ORDER BY chain_seq DESC
		LIMIT $7`, p.CompanyID, q.Get("type"), q.Get("ref"), from, to, cursor, limit+1)
	if err != nil {
		apierr.Write(w, r, apierr.Wrap(apierr.Internal, Text(requestLang(r), "error.internal"), err))
		return
	}
	defer rows.Close()
	var events []oapi.AuditEvent
	for rows.Next() {
		var ev oapi.AuditEvent
		var actor, ref, prev string
		var before, after []byte
		var seq int64
		if err := rows.Scan(&ev.EventId, &ev.Type, &ev.At, &actor, &ref, &before, &after, &seq, &prev, &ev.Hash); err != nil {
			apierr.Write(w, r, apierr.Wrap(apierr.Internal, Text(requestLang(r), "error.internal"), err))
			return
		}
		ev.Actor = oapi.Actor{Id: actor, Name: actor}
		ev.ChainSeq = int(seq)
		ev.PrevHash = &prev
		if ref != "" {
			ev.Ref = &ref
		}
		if beforeMap := decodeObject(before); beforeMap != nil {
			ev.Before = &beforeMap
		}
		if afterMap := decodeObject(after); afterMap != nil {
			ev.After = &afterMap
		}
		events = append(events, ev)
	}
	if err := rows.Err(); err != nil {
		apierr.Write(w, r, apierr.Wrap(apierr.Internal, Text(requestLang(r), "error.internal"), err))
		return
	}
	var next *string
	if len(events) > limit {
		events = events[:limit]
		raw := base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(events[len(events)-1].ChainSeq)))
		next = &raw
	}
	if events == nil {
		events = []oapi.AuditEvent{}
	}
	writePage(w, r, events, next)
}

func decodeObject(b []byte) map[string]interface{} {
	if len(b) == 0 {
		return nil
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	return m
}

func writePage(w http.ResponseWriter, r *http.Request, data any, next *string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(struct {
		Data any `json:"data"`
		Meta struct {
			AsOf       time.Time `json:"as_of"`
			RequestID  string    `json:"request_id"`
			NextCursor *string   `json:"next_cursor,omitempty"`
		} `json:"meta"`
	}{
		Data: data,
		Meta: struct {
			AsOf       time.Time `json:"as_of"`
			RequestID  string    `json:"request_id"`
			NextCursor *string   `json:"next_cursor,omitempty"`
		}{AsOf: time.Now().UTC(), RequestID: apierr.RequestID(r.Context()), NextCursor: next},
	})
}

func requireRoles(r *http.Request, roles ...string) (rls.Principal, error) {
	p, err := rls.FromContext(r.Context())
	if err != nil || p.UserID == "" {
		return rls.Principal{}, apierr.New(apierr.AuthRequired, Text(requestLang(r), "error.auth_required"))
	}
	for _, want := range roles {
		if hasRole(p, want) {
			return p, nil
		}
	}
	return rls.Principal{}, apierr.New(apierr.PermissionDenied, Text(requestLang(r), "error.forbidden"))
}

func requestLang(r *http.Request) string {
	if strings.Contains(strings.ToLower(r.Header.Get("Accept-Language")), "ar") {
		return "ar"
	}
	return "en"
}
