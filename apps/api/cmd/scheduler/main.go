// scheduler enqueues periodic jobs (River periodic jobs) and runs the nightly
// immutable-table guard check. Wave 1 modules add their periodic jobs in
// registerPeriodic. It holds no business workers; cmd/worker executes the jobs.
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
		slog.Error("scheduler exited", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(config.WorkerProfile)
	if err != nil {
		return err
	}
	log := obs.Logger(cfg.LogLevel, "scheduler", cfg.Version)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer pool.Close()

	// River requires at least one queue to start; the scheduler owns a tiny private
	// queue for its own heartbeat and leaves every business queue to cmd/worker.
	workers := river.NewWorkers()
	river.AddWorker(workers, &outbox.EchoWorker{Log: log})
	client, err := river.NewClient(outboxDriver(pool), &river.Config{
		Logger:  log,
		Workers: workers,
		Queues:  map[string]river.QueueConfig{"scheduler": {MaxWorkers: 1}},
		PeriodicJobs: append([]*river.PeriodicJob{
			river.NewPeriodicJob(river.PeriodicInterval(time.Hour), func() (river.JobArgs, *river.InsertOpts) {
				return outbox.EchoArgs{Message: "scheduler heartbeat"}, &river.InsertOpts{Queue: "scheduler"}
			}, &river.PeriodicJobOpts{RunOnStart: true}),
		}, registerPeriodic(log)...),
	})
	if err != nil {
		return fmt.Errorf("river: %w", err)
	}
	if err := client.Start(ctx); err != nil {
		return fmt.Errorf("river start: %w", err)
	}
	go guardCheckLoop(ctx, pool, log)
	log.Info("scheduler started")
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := client.Stop(shutdownCtx); err != nil {
		return fmt.Errorf("river stop: %w", err)
	}
	return nil
}

// guardCheckLoop is the nightly immutable-table presence check (05); it runs at
// start and then every 24 hours, logging loudly when a table has lost its guard.
func guardCheckLoop(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) {
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		rows, err := pool.Query(ctx, `SELECT * FROM erp.immutable_tables_missing_guard()`)
		if err != nil {
			log.Error("guard check query", "err", err)
		} else {
			var missing []string
			for rows.Next() {
				var n string
				if rows.Scan(&n) == nil {
					missing = append(missing, n)
				}
			}
			rows.Close()
			if len(missing) > 0 {
				log.Error("IMMUTABLE TABLES MISSING GUARD TRIGGER", "tables", missing)
			} else {
				log.Info("immutable guard check ok")
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// registerPeriodic is where Wave 1 agents add their periodic jobs, alphabetical.
func registerPeriodic(log *slog.Logger) []*river.PeriodicJob {
	_ = log
	return nil
}
