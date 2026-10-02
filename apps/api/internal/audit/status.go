package audit

import (
	"context"
	"fmt"
	"time"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/oapi"
)

// StatusContribution is what /status must show for backup, chain verification,
// and the recovery point. kit/httpx owns the route; this is the feed for those fields.
type StatusContribution struct {
	LastBackup            oapi.StatusRun
	LastChainVerification oapi.StatusRun
	RecoveryPointInForce  oapi.StatusResponseRecoveryPointInForce
	WALLag                time.Duration
}

// Status reads the latest backup, the latest verification run, and the WAL lag.
func (s *Service) Status(ctx context.Context) (StatusContribution, error) {
	out := StatusContribution{
		LastBackup:            neverRun(),
		LastChainVerification: neverRun(),
		RecoveryPointInForce:  oapi.N24h,
	}
	var backupAt *time.Time
	var backupResult, backupDetail string
	err := s.Pool.QueryRow(ctx, `SELECT finished_at, result, detail FROM erp.backup_runs WHERE finished_at IS NOT NULL ORDER BY finished_at DESC LIMIT 1`).Scan(&backupAt, &backupResult, &backupDetail)
	if err == nil {
		out.LastBackup = runFrom(backupAt, backupResult, backupDetail)
	}
	var verAt *time.Time
	var intact bool
	err = s.Pool.QueryRow(ctx, `SELECT ran_at, intact FROM erp.verification_runs ORDER BY ran_at DESC LIMIT 1`).Scan(&verAt, &intact)
	if err == nil {
		result := "failed"
		if intact {
			result = "ok"
		}
		out.LastChainVerification = runFrom(verAt, result, "")
	}
	lag, point, err := s.ArchiveLag(ctx, s.now())
	if err != nil {
		return out, err
	}
	out.WALLag = lag
	out.RecoveryPointInForce = oapi.StatusResponseRecoveryPointInForce(point)
	lagText := Text(s.lang(), "status.wal_lag", lag.Round(time.Second).String(), point)
	if out.LastBackup.Detail != nil && *out.LastBackup.Detail != "" {
		joined := *out.LastBackup.Detail + "; " + lagText
		out.LastBackup.Detail = &joined
	} else {
		out.LastBackup.Detail = &lagText
	}
	return out, nil
}

func neverRun() oapi.StatusRun {
	return oapi.StatusRun{Result: oapi.StatusRunResultNever}
}

func runFrom(at *time.Time, result, detail string) oapi.StatusRun {
	run := oapi.StatusRun{At: at, Result: oapi.StatusRunResult(result)}
	if detail != "" {
		run.Detail = &detail
	}
	switch result {
	case "ok", "failed", "never":
	default:
		run.Result = oapi.StatusRunResultFailed
	}
	return run
}

// String renders the contribution the way the status page describes it.
func (c StatusContribution) String() string {
	return fmt.Sprintf("backup=%s chain=%s recovery=%s lag=%s", c.LastBackup.Result, c.LastChainVerification.Result, c.RecoveryPointInForce, c.WALLag)
}
