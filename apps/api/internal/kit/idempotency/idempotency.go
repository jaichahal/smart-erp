// Package idempotency replays stored responses for repeated Idempotency-Key requests
// (04 "Transport"): same key and same body replays; same key and different body is
// 409 CONFLICT. Keys are scoped to the principal so two users cannot collide.
package idempotency

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// Header is the request header name.
const Header = "Idempotency-Key"

// Middleware enforces the key on mutating methods. It requires a principal on the
// context (from the auth middleware); unauthenticated mutating routes must not use it.
func Middleware(pool *pgxpool.Pool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}
			keyRaw := r.Header.Get(Header)
			key, err := uuid.Parse(keyRaw)
			if err != nil {
				apierr.Write(w, r, apierr.New(apierr.ValidationError, "Idempotency-Key header (UUID) is required on mutating requests"))
				return
			}
			p, err := rls.FromContext(r.Context())
			if err != nil {
				apierr.Write(w, r, apierr.New(apierr.AuthRequired, "authentication required"))
				return
			}
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
			if err != nil {
				apierr.Write(w, r, apierr.Wrap(apierr.ValidationError, "body too large or unreadable", err))
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			sum := sha256.Sum256(append([]byte(r.Method+" "+r.URL.Path+"\n"), body...))
			reqHash := hex.EncodeToString(sum[:])

			var storedHash, ctype string
			var status int
			var resp []byte
			err = pool.QueryRow(r.Context(), `SELECT request_hash, status_code, response_body, content_type FROM erp.idempotency_keys WHERE key=$1 AND principal_id=$2 AND expires_at > clock_timestamp()`, key, p.UserID).
				Scan(&storedHash, &status, &resp, &ctype)
			switch {
			case err == nil:
				if storedHash != reqHash {
					apierr.Write(w, r, apierr.New(apierr.Conflict, "Idempotency-Key was already used with a different request"))
					return
				}
				w.Header().Set("Content-Type", ctype)
				w.Header().Set("Idempotent-Replayed", "true")
				w.WriteHeader(status)
				_, _ = w.Write(resp)
				return
			case errors.Is(err, pgx.ErrNoRows):
				// first time: fall through and record
			default:
				apierr.Write(w, r, apierr.Wrap(apierr.Internal, "idempotency lookup failed", err))
				return
			}

			rec := &recorder{ResponseWriter: w, status: 200}
			next.ServeHTTP(rec, r)
			// Only 2xx and 4xx business outcomes are worth replaying; 5xx must be retried.
			if rec.status >= 500 {
				return
			}
			_, err = pool.Exec(context.WithoutCancel(r.Context()), `INSERT INTO erp.idempotency_keys(key, principal_id, request_hash, status_code, response_body, content_type) VALUES ($1,$2,$3,$4,$5,$6)`,
				key, p.UserID, reqHash, rec.status, rec.buf.Bytes(), rec.Header().Get("Content-Type"))
			var pgErr *pgconn.PgError
			if err != nil && (!errors.As(err, &pgErr) || pgErr.Code != "23505") {
				slog.WarnContext(r.Context(), "idempotency store failed", "err", err)
			}
			// 23505 is a race on the same key: the other writer's row wins.
		})
	}
}

type recorder struct {
	http.ResponseWriter
	status int
	buf    bytes.Buffer
}

// WriteHeader records the status before passing it through.
func (r *recorder) WriteHeader(code int) { r.status = code; r.ResponseWriter.WriteHeader(code) }

// Write buffers the body so it can be replayed later.
func (r *recorder) Write(b []byte) (int, error) {
	r.buf.Write(b)
	return r.ResponseWriter.Write(b)
}
