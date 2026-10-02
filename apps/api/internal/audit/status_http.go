package audit

import (
	"context"
	"net/http"
	"time"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/httpx"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/oapi"
)

// StatusHandler is GET /api/v1/status for the operator page (A20, I8, I16).
// Backup, chain verification, WAL lag, and the recovery point come from this
// module. Bank feed stays "never" until the bank module records a run.
func StatusHandler(deps httpx.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		contrib := StatusContribution{
			LastBackup:            neverRun(),
			LastChainVerification: neverRun(),
			RecoveryPointInForce:  oapi.N24h,
		}
		health := oapi.StatusResponseWorkerHealthOk
		if deps.Pool == nil {
			health = oapi.StatusResponseWorkerHealthDown
		} else {
			svc := NewFromDeps(deps)
			got, err := svc.Status(ctx)
			if err != nil {
				health = oapi.StatusResponseWorkerHealthDegraded
			} else {
				contrib = got
			}
		}
		var drift int64
		var depth int64
		if deps.Pool != nil {
			var dbNow time.Time
			if err := deps.Pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&dbNow); err != nil {
				health = oapi.StatusResponseWorkerHealthDegraded
			} else {
				drift = time.Since(dbNow).Milliseconds()
			}
			if err := deps.Pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE state IN ('available','scheduled','retryable','running')`).Scan(&depth); err != nil {
				health = oapi.StatusResponseWorkerHealthDegraded
			}
		}
		if depth < 0 {
			depth = 0
		}
		httpx.JSON(w, r, http.StatusOK, oapi.StatusResponse{
			ClockDriftMs:          int(drift),
			LastBackup:            contrib.LastBackup,
			LastBankFeed:          neverRun(),
			LastChainVerification: contrib.LastChainVerification,
			QueueDepth:            int(depth),
			RecoveryPointInForce:  contrib.RecoveryPointInForce,
			WorkerHealth:          health,
		})
	}
}
