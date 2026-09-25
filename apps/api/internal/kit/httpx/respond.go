// Package httpx holds the success envelope and small HTTP helpers shared by all modules.
package httpx

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
)

// Meta is the success-envelope metadata (04 "Envelopes").
type Meta struct {
	AsOf      time.Time      `json:"as_of"`
	RequestID string         `json:"request_id"`
	Notice    string         `json:"notice,omitempty"`
	Extra     map[string]any `json:"extra,omitempty"`
}

type envelope struct {
	Data any  `json:"data"`
	Meta Meta `json:"meta"`
}

// JSON writes {data, meta} with the given status.
func JSON(w http.ResponseWriter, r *http.Request, status int, data any) {
	JSONWithMeta(w, r, status, data, Meta{})
}

// JSONWithMeta writes {data, meta} letting the caller set notice or extra.
func JSONWithMeta(w http.ResponseWriter, r *http.Request, status int, data any, meta Meta) {
	if meta.AsOf.IsZero() {
		meta.AsOf = time.Now().UTC()
	}
	meta.RequestID = apierr.RequestID(r.Context())
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(envelope{Data: data, Meta: meta}); err != nil {
		slog.WarnContext(r.Context(), "write response", "err", err)
	}
}

// DecodeJSON decodes a body of at most 1 MiB, rejecting unknown fields.
func DecodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return apierr.Wrap(apierr.ValidationError, "invalid JSON body", err)
	}
	return nil
}
