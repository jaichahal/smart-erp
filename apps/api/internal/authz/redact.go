package authz

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// Redact removes cost and margin fields the caller is not allowed to read,
// including nested objects, arrays, and export rows (A11, R1.10).
func (s *Service) Redact(ctx context.Context, actor rls.Principal, payload any) (any, error) {
	if payload == nil {
		return nil, nil
	}
	normal, err := normalizeJSON(payload)
	if err != nil {
		return nil, err
	}
	var hidden map[string]struct{}
	err = rls.Tx(ctx, s.pool, actor, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT field_name FROM erp.restricted_fields
			WHERE erp.permission_scope(permission) IS NULL`)
		if err != nil {
			return err
		}
		defer rows.Close()
		hidden = map[string]struct{}{}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return err
			}
			hidden[strings.ToLower(name)] = struct{}{}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return strip(normal, hidden), nil
}

func normalizeJSON(payload any) (any, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return v, nil
}

func strip(v any, hidden map[string]struct{}) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for key, val := range t {
			if _, drop := hidden[strings.ToLower(key)]; drop {
				continue
			}
			out[key] = strip(val, hidden)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i := range t {
			out[i] = strip(t[i], hidden)
		}
		return out
	default:
		return v
	}
}
