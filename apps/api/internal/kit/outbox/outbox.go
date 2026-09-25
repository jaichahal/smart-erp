// Package outbox wires River as the transactional outbox (ADR-04, ADR-14).
//
// A job inserted with InsertTx inside a business transaction is committed or rolled
// back with it, which is the outbox guarantee. Workers register their River job
// types with a Workers set and run in cmd/worker.
package outbox

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/riverqueue/river/rivertype"
)

// Queues the platform defines. Modules add their own constants next to their jobs.
const (
	QueueDefault       = river.QueueDefault
	QueueNotifications = "notifications"
	QueueSnapshots     = "snapshots"
	QueueAnchors       = "anchors"
)

// NewClient builds a River client. Pass workers=nil for an insert-only client (api).
func NewClient(pool *pgxpool.Pool, workers *river.Workers, logger *slog.Logger) (*river.Client[pgx.Tx], error) {
	cfg := &river.Config{Logger: logger}
	if workers != nil {
		cfg.Workers = workers
		cfg.Queues = map[string]river.QueueConfig{
			QueueDefault:       {MaxWorkers: 10},
			QueueNotifications: {MaxWorkers: 20},
			QueueSnapshots:     {MaxWorkers: 2},
			QueueAnchors:       {MaxWorkers: 1},
		}
		cfg.JobTimeout = 5 * time.Minute
		cfg.MaxAttempts = 10
	}
	return river.NewClient(riverpgxv5.New(pool), cfg)
}

// InsertTx enqueues a job in the caller's transaction. This is the only way modules
// should enqueue from a request path.
func InsertTx(ctx context.Context, c *river.Client[pgx.Tx], tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	return c.InsertTx(ctx, tx, args, opts)
}

// Migrate applies River's own schema using the migrator connection.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	m, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return err
	}
	_, err = m.Migrate(ctx, rivermigrate.DirectionUp, nil)
	return err
}

// EchoArgs is the reference job: it proves transactional enqueue end to end and is
// used by tests and the status page's queue self-check.
type EchoArgs struct {
	Message string `json:"message"`
}

// Kind is River's job type name.
func (EchoArgs) Kind() string { return "kit.echo" }

// EchoWorker logs the message.
type EchoWorker struct {
	river.WorkerDefaults[EchoArgs]
	Log *slog.Logger
}

// Work logs the message and succeeds.
func (w *EchoWorker) Work(ctx context.Context, job *river.Job[EchoArgs]) error {
	w.Log.InfoContext(ctx, "echo job", "message", job.Args.Message, "attempt", job.Attempt)
	return nil
}
