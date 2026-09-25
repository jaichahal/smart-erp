package httpx

import (
	"context"
	"net/http"
	"time"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
)

// Health is readiness: the database answers. Unauthenticated by design.
func Health(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := d.Pool.Ping(ctx); err != nil {
			apierr.Write(w, r, apierr.Wrap(apierr.Internal, "database unreachable", err))
			return
		}
		JSON(w, r, 200, map[string]any{"status": "ready", "version": d.Config.Version, "commit": d.Config.Commit})
	}
}

// StatusResponse mirrors the OpenAPI StatusResponse schema (04, R17.4). Fields that
// no worker has populated yet are null, never fabricated.
type StatusResponse struct {
	Env                   string     `json:"env"`
	Version               string     `json:"version"`
	UptimeSeconds         int64      `json:"uptime_seconds"`
	DBClockDriftMs        int64      `json:"clock_drift_ms"`
	QueueDepth            int64      `json:"queue_depth"`
	ImmutableTables       int64      `json:"immutable_tables"`
	TablesMissingGuard    []string   `json:"tables_missing_guard"`
	LastBackup            *time.Time `json:"last_backup"`
	LastChainVerification *time.Time `json:"last_chain_verification"`
	LastBankFeed          *time.Time `json:"last_bank_feed"`
	RecoveryPointInForce  *string    `json:"recovery_point_in_force"`
	// Warnings lists every probe that failed. A failed probe leaves its field null
	// or -1; it never reports a plausible zero (R14.8, "nothing invented").
	Warnings []string `json:"warnings"`
}

// Status is the operator status page source. Wave 0 fills what the kit knows;
// Track I and B2 populate backup, anchor, and feed fields through their tables.
// Authorisation (System Manager only) is added by A2's middleware; until then the
// route is mounted but returns only non-sensitive counters.
func Status(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		var dbNow time.Time
		if err := d.Pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&dbNow); err != nil {
			apierr.Write(w, r, apierr.Wrap(apierr.Internal, "database unreachable", err))
			return
		}
		res := StatusResponse{Env: d.Config.Env, Version: d.Config.Version, UptimeSeconds: int64(time.Since(d.StartedAt).Seconds()),
			DBClockDriftMs: time.Since(dbNow).Milliseconds(), TablesMissingGuard: []string{}, Warnings: []string{}, ImmutableTables: -1, QueueDepth: -1}
		if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM erp.immutable_tables`).Scan(&res.ImmutableTables); err != nil {
			res.Warnings = append(res.Warnings, "immutable_tables: "+err.Error())
		}
		if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE state IN ('available','scheduled','retryable','running')`).Scan(&res.QueueDepth); err != nil {
			res.Warnings = append(res.Warnings, "queue_depth: "+err.Error())
		}
		rows, err := d.Pool.Query(ctx, `SELECT * FROM erp.immutable_tables_missing_guard()`)
		if err != nil {
			res.Warnings = append(res.Warnings, "guard_check: "+err.Error())
		} else {
			for rows.Next() {
				var name string
				if rows.Scan(&name) == nil {
					res.TablesMissingGuard = append(res.TablesMissingGuard, name)
				}
			}
			rows.Close()
		}
		if res.ImmutableTables == 0 {
			res.Warnings = append(res.Warnings, "no immutable tables registered: migrations have not run against this database")
		}
		JSON(w, r, 200, res)
	}
}
