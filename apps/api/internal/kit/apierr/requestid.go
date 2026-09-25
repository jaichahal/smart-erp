package apierr

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

type ctxKey struct{}

// RequestIDMiddleware assigns a request id (or honours X-Request-ID) and echoes it back.
func RequestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := r.Header.Get("X-Request-ID")
		if rid == "" || len(rid) > 64 {
			var b [12]byte
			_, _ = rand.Read(b[:])
			rid = hex.EncodeToString(b[:])
		}
		w.Header().Set("X-Request-ID", rid)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, rid)))
	})
}

// RequestID returns the id for the request, or "" outside a request.
func RequestID(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKey{}).(string); ok {
		return v
	}
	return ""
}
