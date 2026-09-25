package audit

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
)

// AnchorArgs is the hourly anchor job.
type AnchorArgs struct {
	CompanyID string `json:"company_id"`
}

// Kind is the River job name.
func (AnchorArgs) Kind() string { return "audit.anchor" }

// AnchorWorker signs and stores one company's chain head.
type AnchorWorker struct {
	river.WorkerDefaults[AnchorArgs]
	Svc *Service
	Log *slog.Logger
}

// Work anchors the company on the job.
func (w *AnchorWorker) Work(ctx context.Context, job *river.Job[AnchorArgs]) error {
	id, err := uuid.Parse(job.Args.CompanyID)
	if err != nil {
		return err
	}
	if err := w.Svc.Anchor(ctx, id); err != nil {
		return err
	}
	if w.Log != nil {
		w.Log.InfoContext(ctx, "anchored chain head", "company", id.String())
	}
	return w.Svc.RaiseIfAnchorStale(ctx, id, w.Svc.now())
}

// VerifyArgs asks the verifier to walk one company.
type VerifyArgs struct {
	CompanyID string `json:"company_id"`
}

// Kind is the River job name.
func (VerifyArgs) Kind() string { return "audit.verify" }

// VerifyWorker runs the chain verifier.
type VerifyWorker struct {
	river.WorkerDefaults[VerifyArgs]
	Svc *Service
}

// Work verifies the company and emits chain.verified or chain.broken.
func (w *VerifyWorker) Work(ctx context.Context, job *river.Job[VerifyArgs]) error {
	id, err := uuid.Parse(job.Args.CompanyID)
	if err != nil {
		return err
	}
	_, err = w.Svc.Verify(ctx, id)
	return err
}

// BackupArgs runs one backup.
type BackupArgs struct {
	BackupID string `json:"backup_id"`
}

// Kind is the River job name.
func (BackupArgs) Kind() string { return "audit.backup" }

// BackupWorker runs the backup engine.
type BackupWorker struct {
	river.WorkerDefaults[BackupArgs]
	Svc *Service
}

// Work runs the backup.
func (w *BackupWorker) Work(ctx context.Context, job *river.Job[BackupArgs]) error {
	_, err := w.Svc.RunBackup(ctx, job.Args.BackupID)
	return err
}

// EmailInterval is the daily anchor email.
const EmailInterval = 24 * time.Hour

// Register adds audit workers to a River set. cmd/worker calls this once it is allowed to import the module.
func Register(workers *river.Workers, svc *Service, log *slog.Logger) {
	river.AddWorker(workers, &AnchorWorker{Svc: svc, Log: log})
	river.AddWorker(workers, &VerifyWorker{Svc: svc})
	river.AddWorker(workers, &BackupWorker{Svc: svc})
}
