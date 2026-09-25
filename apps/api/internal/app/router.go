// Package app is the composition root for the HTTP API. Feature modules stay
// independent; this package is the one place that mounts them together.
package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jaichahal/smart-erp/apps/api/internal/approvals"
	"github.com/jaichahal/smart-erp/apps/api/internal/audit"
	"github.com/jaichahal/smart-erp/apps/api/internal/authz"
	"github.com/jaichahal/smart-erp/apps/api/internal/clocks"
	"github.com/jaichahal/smart-erp/apps/api/internal/identity"
	"github.com/jaichahal/smart-erp/apps/api/internal/journeys"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/httpx"
	"github.com/jaichahal/smart-erp/apps/api/internal/ledger"
	periods "github.com/jaichahal/smart-erp/apps/api/internal/ledger/periods"
	"github.com/jaichahal/smart-erp/apps/api/internal/notifications"
	"github.com/jaichahal/smart-erp/apps/api/internal/stock"
)

// Option tunes composition. Production uses none; tests inject the Zitadel broker port.
type Option func(*wire)

type wire struct {
	identity []identity.Option
}

// WithIdentity passes options through to the identity module.
func WithIdentity(opts ...identity.Option) Option {
	return func(w *wire) {
		w.identity = append(w.identity, opts...)
	}
}

// Handler is the production HTTP API: kit middleware, health, status, and every mounted module.
func Handler(deps httpx.Deps, opts ...Option) (http.Handler, error) {
	var w wire
	for _, opt := range opts {
		opt(&w)
	}
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	r := chi.NewRouter()
	r.Use(apierr.RequestIDMiddleware, middleware.Recoverer, skipTimeoutForWebSocket)
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		apierr.Write(w, r, apierr.New(apierr.NotFound, "route not found"))
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, "method not allowed"))
	})
	r.Get("/health", httpx.Health(deps))
	var mountErr error
	r.Route("/api/v1", func(v1 chi.Router) {
		v1.Get("/health", httpx.Health(deps))
		// B2 route: status includes last backup, chain verification, and the recovery point.
		v1.Get("/status", audit.StatusHandler(deps))
		v1.Get("/design/tokens", designTokens(deps.Pool))
		id := identity.Mount(v1, deps, w.identity...)
		clocks.Mount(v1, deps)
		periods.Mount(v1, deps, approvalGate{pool: deps.Pool})
		ledger.Mount(v1, deps)
		// Periods and authz both register GET /exceptions. Chi keeps the later
		// route. Access-review exceptions must stay reachable with the bearer
		// token authz verifies; period exceptions stay on the periods server.
		authz.Mount(v1, deps)
		audit.Mount(v1, deps)
		v1.Group(func(authed chi.Router) {
			authed.Use(id.Authenticate)
			approvals.Mount(authed, deps)
			stock.Mount(authed, deps)
			if err := notifications.Mount(authed, deps); err != nil {
				mountErr = err
			}
		})
		if mountErr != nil {
			return
		}
		journeys.Mount(v1, deps)
	})
	if mountErr != nil {
		return nil, mountErr
	}
	if err := ensureDesignTokens(context.Background(), deps.Pool); err != nil {
		return nil, err
	}
	return r, nil
}

// skipTimeoutForWebSocket keeps /ws hijackable. The 30s timeout cancels the
// request context, which would drop an acknowledgement on a connected surface.
func skipTimeoutForWebSocket(next http.Handler) http.Handler {
	timed := middleware.Timeout(30 * time.Second)(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			next.ServeHTTP(w, r)
			return
		}
		timed.ServeHTTP(w, r)
	})
}

// approvalGate confirms a hard close cites an approved approval request (R4.6).
type approvalGate struct {
	pool *pgxpool.Pool
}

func (g approvalGate) Approved(ctx context.Context, companyID, _ uuid.UUID, approvalID string) error {
	if approvalID == "" || g.pool == nil {
		return errors.New("approval required")
	}
	var state string
	err := g.pool.QueryRow(ctx, `
		SELECT state FROM erp.approval_requests
		WHERE company_id = $1 AND request_id = $2
		ORDER BY state_version DESC LIMIT 1`, companyID, approvalID).Scan(&state)
	if err != nil {
		return err
	}
	if state != "approved" {
		return errors.New("approval is not approved")
	}
	return nil
}
