// worker runs River jobs. Wave 1 modules register their workers in registerWorkers.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/config"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/obs"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/outbox"
)

func main() {
	if err := run(); err != nil {
		slog.Error("worker exited", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(config.WorkerProfile)
	if err != nil {
		return err
	}
	log := obs.Logger(cfg.LogLevel, "worker", cfg.Version)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	shutdownTracing, err := obs.Tracing(ctx, "worker", cfg.Version, cfg.OTLPEndpoint)
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

	workers := river.NewWorkers()
	river.AddWorker(workers, &outbox.EchoWorker{Log: log})
	registerWorkers(workers, log)

	client, err := outbox.NewClient(pool, workers, log)
	if err != nil {
		return fmt.Errorf("river: %w", err)
	}
	if err := client.Start(ctx); err != nil {
		return fmt.Errorf("river start: %w", err)
	}
	log.Info("worker started")
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.Stop(shutdownCtx); err != nil {
		return fmt.Errorf("river stop: %w", err)
	}
	log.Info("worker stopped")
	return nil
}

// registerWorkers is where Wave 1 agents add river.AddWorker lines, alphabetical.
func registerWorkers(w *river.Workers, log *slog.Logger) {
	_ = w
	_ = log
}
