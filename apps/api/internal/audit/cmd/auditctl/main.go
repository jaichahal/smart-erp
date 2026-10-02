// Command auditctl is the operator entry point for anchor, backup, restore, and chain verification.
// deploy/scripts wraps it; the logic stays in the audit package so it is tested.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jaichahal/smart-erp/apps/api/internal/audit"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/config"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/httpx"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/obs"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/outbox"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		var code *exitCode
		if errors.As(err, &code) {
			os.Exit(code.n)
		}
		fmt.Fprintln(os.Stderr, "auditctl:", err)
		os.Exit(1)
	}
}

// exitCode is a verification result (2 stale, 3 broken) that is not an operator error.
type exitCode struct{ n int }

func (e *exitCode) Error() string { return fmt.Sprintf("verification exit %d", e.n) }

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: auditctl anchor|backup|chain|restore|wal|secrets")
	}
	switch args[0] {
	case "secrets":
		missing := audit.MissingAnchorEnv()
		if len(missing) > 0 {
			return fmt.Errorf("%s", audit.Text("en", "config.missing", join(missing)))
		}
		fmt.Println("ok")
		return nil
	case "anchor":
		return cmdAnchor(args[1:])
	case "backup":
		return cmdBackup(args[1:])
	case "chain":
		return cmdChain(args[1:])
	case "restore":
		return cmdRestore(args[1:])
	case "wal":
		return cmdWAL(args[1:])
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func join(v []string) string {
	out := ""
	for i, s := range v {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

func openService(ctx context.Context) (*audit.Service, *pgxpool.Pool, error) {
	if miss := audit.MissingAnchorEnv(); len(miss) > 0 && os.Getenv("ERP_AUDIT_ALLOW_UNSIGNED") == "" {
		return nil, nil, fmt.Errorf("%s", audit.Text("en", "config.missing", join(miss)))
	}
	cfg, err := config.Load(config.WorkerProfile)
	if err != nil {
		return nil, nil, err
	}
	log := obs.Logger(cfg.LogLevel, "auditctl", cfg.Version)
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, nil, err
	}
	river, err := outbox.NewClient(pool, nil, log)
	if err != nil {
		pool.Close()
		return nil, nil, err
	}
	deps := httpx.Deps{Pool: pool, River: river, Config: cfg, Log: log, StartedAt: time.Now()}
	return audit.NewFromDeps(deps), pool, nil
}

func cmdAnchor(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: auditctl anchor write|email")
	}
	fs := flag.NewFlagSet("anchor", flag.ContinueOnError)
	target := fs.String("target", "all", "all, offsite, or local")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	ctx := context.Background()
	svc, pool, err := openService(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	switch args[0] {
	case "email":
		return svc.SendDailyAnchorEmail(ctx)
	case "write":
		rows, err := pool.Query(ctx, `SELECT company_id FROM erp.chain_head`)
		if err != nil {
			return err
		}
		defer rows.Close()
		var n int
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				return err
			}
			switch *target {
			case "local":
				svc.SkipOffsite = true
			case "offsite", "all":
				svc.SkipOffsite = false
				svc.SkipOnPrem = false
			default:
				return fmt.Errorf("target must be all, offsite, or local")
			}
			if err := svc.Anchor(ctx, id); err != nil {
				return err
			}
			if err := svc.RaiseIfAnchorStale(ctx, id, time.Now().UTC()); err != nil {
				return err
			}
			n++
		}
		fmt.Printf("anchored %d companies target=%s\n", n, *target)
		return rows.Err()
	default:
		return fmt.Errorf("unknown anchor command %q", args[0])
	}
}

func cmdBackup(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: auditctl backup run|prune --id")
	}
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	id := fs.String("id", "", "backup id")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	ctx := context.Background()
	svc, pool, err := openService(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	switch args[0] {
	case "run":
		if *id == "" {
			return fmt.Errorf("backup run needs --id")
		}
		if _, err := svc.RunBackup(ctx, *id); err != nil {
			return err
		}
		fmt.Println("backup", *id, "verified off-site")
		return nil
	case "prune":
		n, err := svc.Prune(ctx, time.Now().UTC())
		if err != nil {
			return err
		}
		fmt.Println("pruned", n, "objects")
		return nil
	default:
		return fmt.Errorf("unknown backup command %q", args[0])
	}
}

func cmdChain(args []string) error {
	fs := flag.NewFlagSet("chain", flag.ContinueOnError)
	report := fs.String("report", "", "write JSON report here")
	_ = fs.String("against", "offsite", "offsite or local")
	_ = fs.String("database", "erp", "erp or drill")
	_ = fs.String("since", "", "optional lower bound")
	if len(args) == 0 || args[0] != "verify" {
		return fmt.Errorf("usage: auditctl chain verify --report path")
	}
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	ctx := context.Background()
	svc, pool, err := openService(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	rows, err := pool.Query(ctx, `SELECT company_id FROM erp.chain_head`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type item struct {
		CompanyID string
		Code      int
		Intact    bool
	}
	var items []item
	worst := 0
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return err
		}
		res, err := svc.Verify(ctx, id)
		if err != nil {
			return err
		}
		code, err := svc.VerifyExitCode(ctx, id, res)
		if err != nil {
			return err
		}
		if code > worst {
			worst = code
		}
		items = append(items, item{CompanyID: id.String(), Code: code, Intact: res.Intact})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(items) == 0 {
		worst = 2
	}
	body, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	if *report != "" {
		if err := os.WriteFile(*report, body, 0o600); err != nil {
			return err
		}
	} else {
		fmt.Println(string(body))
	}
	if worst != 0 {
		return &exitCode{n: worst}
	}
	return nil
}

func cmdRestore(args []string) error {
	if len(args) == 0 || args[0] != "run" {
		return fmt.Errorf("usage: auditctl restore run --backup id --approval token --target offsite")
	}
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	backupID := fs.String("backup", "", "backup id")
	approval := fs.String("approval", "", "stakeholder approval token")
	target := fs.String("target", "offsite", "offsite or local")
	force := fs.Bool("force", false, "override a site-name mismatch only")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	ctx := context.Background()
	svc, pool, err := openService(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	res, err := svc.Restore(ctx, audit.RestoreOpts{BackupID: *backupID, Target: *target, ApprovalToken: *approval, Force: *force})
	if err != nil {
		return err
	}
	if res.RefusalCode != "" {
		return fmt.Errorf("%s", audit.Text("en", "restore.refused", res.RefusalCode))
	}
	fmt.Println("restore ok writes", res.WritesEnabled)
	return nil
}

func cmdWAL(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: auditctl wal lag")
	}
	ctx := context.Background()
	svc, pool, err := openService(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	switch args[0] {
	case "lag":
		st, err := svc.Status(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("lag=%s recovery_point=%s\n", st.WALLag, st.RecoveryPointInForce)
		return nil
	default:
		return fmt.Errorf("unknown wal command %q", args[0])
	}
}
