package authz

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// Issuer and audience match identity's defaults. Authz verifies the access
// token identity already issued; it does not import that package.
const (
	tokenIssuer   = "smart-erp"
	tokenAudience = "erp"
)

func (h handler) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := rls.FromContext(r.Context()); err == nil {
			next.ServeHTTP(w, r)
			return
		}
		actor, err := h.svc.principalFromBearer(r.Context(), bearerToken(r))
		if err != nil {
			h.writeErr(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(rls.WithPrincipal(r.Context(), actor)))
	})
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) < len("Bearer ") || !strings.EqualFold(h[:len("Bearer ")], "Bearer ") {
		return ""
	}
	return strings.TrimSpace(h[len("Bearer "):])
}

func (s *Service) principalFromBearer(ctx context.Context, raw string) (rls.Principal, error) {
	if raw == "" {
		return rls.Principal{}, s.fail(ctx, apierr.AuthRequired, "auth.required")
	}
	set, err := s.signingKeys(ctx)
	if err != nil {
		return rls.Principal{}, apierr.Wrap(apierr.Internal, s.messages.text(langOf(ctx), "internal"), err)
	}
	tok, err := jwt.Parse([]byte(raw), jwt.WithKeySet(set), jwt.WithValidate(true), jwt.WithIssuer(tokenIssuer), jwt.WithAudience(tokenAudience))
	if err != nil {
		return rls.Principal{}, s.fail(ctx, apierr.AuthRequired, "auth.required")
	}
	var rowUser string
	var expires time.Time
	var revoked *time.Time
	err = s.pool.QueryRow(ctx, `SELECT user_id, expires_at, revoked_at FROM erp.identity_access_tokens WHERE jti = $1`, tok.JwtID()).Scan(&rowUser, &expires, &revoked)
	if errors.Is(err, pgx.ErrNoRows) || revoked != nil || !expires.After(time.Now()) || rowUser == "" || rowUser != tok.Subject() {
		return rls.Principal{}, s.fail(ctx, apierr.AuthRequired, "auth.required")
	}
	if err != nil {
		return rls.Principal{}, apierr.Wrap(apierr.Internal, s.messages.text(langOf(ctx), "internal"), err)
	}
	company, err := uuid.Parse(stringClaim(tok, "company_id"))
	if err != nil {
		return rls.Principal{}, s.fail(ctx, apierr.AuthRequired, "auth.required")
	}
	actor := rls.Principal{UserID: tok.Subject(), CompanyID: company, Roles: stringListClaim(tok, "roles")}
	disabled, err := s.userDisabled(ctx, actor)
	if err != nil {
		return rls.Principal{}, err
	}
	if disabled {
		return rls.Principal{}, s.fail(ctx, apierr.AuthRequired, "auth.required")
	}
	return actor, nil
}

func (s *Service) userDisabled(ctx context.Context, actor rls.Principal) (bool, error) {
	var disabled bool
	err := rls.Tx(ctx, s.pool, actor, func(tx pgx.Tx) error {
		var at *time.Time
		err := tx.QueryRow(ctx, `SELECT disabled_at FROM erp.users WHERE id = $1`, actor.UserID).Scan(&at)
		if errors.Is(err, pgx.ErrNoRows) {
			disabled = false
			return nil
		}
		if err != nil {
			return err
		}
		disabled = at != nil
		return nil
	})
	if err != nil {
		return false, apierr.Wrap(apierr.Internal, s.messages.text(langOf(ctx), "internal"), err)
	}
	return disabled, nil
}

func (s *Service) signingKeys(ctx context.Context) (jwk.Set, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT public_jwk FROM erp.identity_signing_keys
		WHERE retired_at IS NULL OR retired_at > clock_timestamp() - interval '15 minutes'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	set := jwk.NewSet()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		key, err := jwk.ParseKey(raw)
		if err != nil {
			return nil, err
		}
		if err := set.AddKey(key); err != nil {
			return nil, err
		}
	}
	return set, rows.Err()
}

func stringClaim(tok jwt.Token, name string) string {
	v, ok := tok.Get(name)
	if !ok || v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

func stringListClaim(tok jwt.Token, name string) []string {
	v, ok := tok.Get(name)
	if !ok || v == nil {
		return []string{}
	}
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			s, ok := item.(string)
			if ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return []string{}
	}
}
