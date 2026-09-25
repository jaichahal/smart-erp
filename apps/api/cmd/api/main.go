// api serves the HTTP API. Wave 0 mounts only the kit middleware, /health, /status,
// and the audit verifier; Wave 1 modules register their routers in registerModules.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/config"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/httpx"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/obs"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/outbox"
)

func main() {
	if err := run(); err != nil {
		slog.Error("api exited", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(config.APIProfile)
	if err != nil {
		return err
	}
	log := obs.Logger(cfg.LogLevel, "api", cfg.Version)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	shutdownTracing, err := obs.Tracing(ctx, "api", cfg.Version, cfg.OTLPEndpoint)
	if err != nil {
		return fmt.Errorf("tracing: %w", err)
	}
	defer func() {
		if err := shutdownTracing(context.Background()); err != nil {
			log.Warn("tracing shutdown", "err", err)
		}
	}()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("database ping: %w", err)
	}
	river, err := outbox.NewClient(pool, nil, log)
	if err != nil {
		return fmt.Errorf("river: %w", err)
	}

	r := chi.NewRouter()
	// Client IP is taken from the reverse proxy's headers only when the identity
	// module (A1) installs its trusted-proxy middleware; never from RealIP blindly.
	r.Use(apierr.RequestIDMiddleware, middleware.Recoverer, middleware.Timeout(30*time.Second))
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		apierr.Write(w, r, apierr.New(apierr.NotFound, "route not found"))
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, "method not allowed"))
	})

	deps := httpx.Deps{Pool: pool, River: river, Config: cfg, Log: log, StartedAt: time.Now()}
	r.Get("/health", httpx.Health(deps))
	r.Route("/api/v1", func(v1 chi.Router) {
		v1.Get("/health", httpx.Health(deps))
		v1.Get("/status", httpx.Status(deps))
		registerModules(v1, deps)
	})

	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: r, ReadHeaderTimeout: 10 * time.Second}
	errCh := make(chan error, 1)
	go func() {
		log.Info("api listening", "addr", cfg.HTTPAddr, "env", cfg.Env)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	select {
	case err := <-errCh:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	log.Info("api stopped")
	return nil
}

// registerModules is the single place Wave 1 agents add their routers, one line each,
// in their own PR. Keep alphabetical to avoid merge conflicts.
func registerModules(r chi.Router, deps httpx.Deps) {
	_ = r
	_ = deps
	// approvals.Mount(r, deps)
	// audit.Mount(r, deps)
	// authz.Mount(r, deps)
	// identity.Mount(r, deps)
	// journeys.Mount(r, deps)
	// notifications.Mount(r, deps)
	// periods.Mount(r, deps)
}
