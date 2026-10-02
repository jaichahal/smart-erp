package e2e

import (
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jaichahal/smart-erp/apps/api/internal/app"
	"github.com/jaichahal/smart-erp/apps/api/internal/identity"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/httpx"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/outbox"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/testdb"
)

// newLiveStack is the composed API with a wall clock, so access tokens stay
// inside their fifteen-minute lifetime while the acceptance cases run.
func newLiveStack(t *testing.T) *stack {
	t.Helper()
	db := testdb.New(t)
	broker := newMemBroker()
	dir := &memDir{byLogin: map[string]identity.Account{}, byID: map[string]identity.Account{}}
	river, err := outbox.NewClient(db.App, nil, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("river: %v", err)
	}
	policy := identity.DefaultPolicy()
	policy.RatePerLogin = 1000
	policy.RatePerIP = 1000
	handler, err := app.Handler(httpx.Deps{
		Pool: db.App, River: river, Log: slog.New(slog.DiscardHandler), StartedAt: time.Now(),
	}, app.WithIdentity(
		identity.WithBroker(broker),
		identity.WithDirectory(dir),
		identity.WithPolicy(policy),
		identity.WithClock(func() time.Time { return time.Now().UTC() }),
	))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &stack{db: db, srv: srv, broker: broker, dir: dir}
}
