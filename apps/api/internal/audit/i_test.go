package audit

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/testdb"
)

func backupID(t *testing.T) string {
	t.Helper()
	return "b-" + uuid.NewString()
}

func mustBackup(t *testing.T, svc *Service, id string) *Manifest {
	t.Helper()
	m, err := svc.RunBackup(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func offsiteVerified(t *testing.T, db *testdb.DB, id string) bool {
	t.Helper()
	var ok bool
	var result string
	if err := db.App.QueryRow(context.Background(), `SELECT offsite_verified, result FROM erp.backup_runs WHERE id=$1`, id).Scan(&ok, &result); err != nil {
		t.Fatal(err)
	}
	return ok && result == "ok"
}

// I2: archives are written, then the manifest; an interrupted run leaves no manifest.
func TestI2_InterruptedBackupLeavesNoManifest(t *testing.T) {
	db := testdb.New(t)
	on, off := newMemStore(), newMemStore()
	svc := newSvc(t, db, on, off)
	id := backupID(t)
	if _, err := svc.WriteArchives(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	exists, err := on.Exists(context.Background(), manifestObjectKey(id))
	if err != nil || exists {
		t.Fatalf("interrupted run must not leave a manifest: exists=%v err=%v", exists, err)
	}
	exists, err = off.Exists(context.Background(), manifestObjectKey(id))
	if err != nil || exists {
		t.Fatalf("off-site manifest must be absent: exists=%v err=%v", exists, err)
	}
	var result, manifest string
	if err := db.App.QueryRow(context.Background(), `SELECT result, manifest_json FROM erp.backup_runs WHERE id=$1`, id).Scan(&result, &manifest); err != nil {
		t.Fatal(err)
	}
	if result == "ok" || manifest != "" {
		t.Fatalf("run must not be successful, got %s %q", result, manifest)
	}
}

// I3: per-file SHA-256 and the aggregate checksum verify, and counts and ledger totals are recorded.
func TestI3_ManifestChecksumsCountsAndLedger(t *testing.T) {
	db := testdb.New(t)
	svc := newSvc(t, db, newMemStore(), newMemStore())
	p := principal()
	emit(t, db, p)
	m := mustBackup(t, svc, backupID(t))
	if err := verifyManifestFiles(context.Background(), svc.Offsite, m); err != nil {
		t.Fatal(err)
	}
	if m.AggregateSHA256 == "" || len(m.Files) != 3 {
		t.Fatalf("manifest files: %+v", m)
	}
	if m.Counts["erp.audit_events"] < 1 || m.Ledger.Debits == "" || m.Ledger.Credits == "" || m.Ledger.Currency != "AED" {
		t.Fatalf("counts or ledger missing: %+v %+v", m.Counts, m.Ledger)
	}
	if m.Ledger.Debits != m.Ledger.Credits {
		t.Fatalf("ledger totals must balance, got %+v", m.Ledger)
	}
}

// I4: restore refuses on each named integrity failure; force overrides only site mismatch.
func TestI4_RestoreRefusesNamedIntegrityFailures(t *testing.T) {
	db := testdb.New(t)
	on, off := newMemStore(), newMemStore()
	svc := newSvc(t, db, on, off)
	svc.ObjectRetain = -time.Hour
	ctx := context.Background()
	p := principal("stakeholder")
	p.Roles = []string{"stakeholder"}
	emit(t, db, p)

	t.Run("MANIFEST_MISSING", func(t *testing.T) {
		res, err := svc.Restore(ctx, RestoreOpts{BackupID: "missing", Target: "offsite", Actor: p})
		if err != nil || res.RefusalCode != RefuseManifestMissing || res.WritesEnabled {
			t.Fatalf("%+v %v", res, err)
		}
	})

	t.Run("MANIFEST_INCOMPLETE", func(t *testing.T) {
		id := backupID(t)
		if err := off.PutCompliance(ctx, manifestObjectKey(id), []byte("{"), time.Now().Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
		res, err := svc.Restore(ctx, RestoreOpts{BackupID: id, Target: "offsite", Actor: p})
		if err != nil || res.RefusalCode != RefuseManifestIncomplete {
			t.Fatalf("%+v %v", res, err)
		}
	})

	base := mustBackup(t, svc, backupID(t))
	token, err := svc.ApproveRestore(ctx, p, base.BackupID)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("FILE_CHECKSUM", func(t *testing.T) {
		id := mustBackup(t, svc, backupID(t)).BackupID
		off.corrupt(archiveObjectKey(id, "meta/counts.json"))
		res, err := svc.Restore(ctx, RestoreOpts{BackupID: id, Target: "offsite", Force: true, ApprovalToken: token.String(), Actor: p})
		if err != nil || res.RefusalCode != RefuseFileChecksum || res.WritesEnabled {
			t.Fatalf("%+v %v", res, err)
		}
	})

	t.Run("AGGREGATE_CHECKSUM", func(t *testing.T) {
		m := mustBackup(t, svc, backupID(t))
		m.AggregateSHA256 = strings.Repeat("0", 64)
		if err := svc.PutManifest(ctx, "offsite", m); err != nil {
			t.Fatal(err)
		}
		res, err := svc.Restore(ctx, RestoreOpts{BackupID: m.BackupID, Target: "offsite", Force: true, Actor: p})
		if err != nil || res.RefusalCode != RefuseAggregateChecksum {
			t.Fatalf("%+v %v", res, err)
		}
	})

	t.Run("SITE_MISMATCH", func(t *testing.T) {
		m := mustBackup(t, svc, backupID(t))
		m.Site = "other-site"
		if err := svc.PutManifest(ctx, "offsite", m); err != nil {
			t.Fatal(err)
		}
		res, err := svc.Restore(ctx, RestoreOpts{BackupID: m.BackupID, Target: "offsite", Actor: p})
		if err != nil || res.RefusalCode != RefuseSiteMismatch {
			t.Fatalf("without force: %+v %v", res, err)
		}
		res, err = svc.Restore(ctx, RestoreOpts{BackupID: m.BackupID, Target: "offsite", Force: true, Actor: p})
		if err != nil || res.RefusalCode != RefuseApprovalRequired {
			t.Fatalf("force must pass the site check and stop on approval: %+v %v", res, err)
		}
	})

	t.Run("LEDGER_MISMATCH", func(t *testing.T) {
		m := mustBackup(t, svc, backupID(t))
		m.Ledger.Debits = "1.00"
		if err := svc.PutManifest(ctx, "offsite", m); err != nil {
			t.Fatal(err)
		}
		tok, err := svc.ApproveRestore(ctx, p, m.BackupID)
		if err != nil {
			t.Fatal(err)
		}
		res, err := svc.Restore(ctx, RestoreOpts{BackupID: m.BackupID, Target: "offsite", ApprovalToken: tok.String(), Actor: p})
		if err != nil || res.RefusalCode != RefuseLedgerMismatch {
			t.Fatalf("%+v %v", res, err)
		}
	})

	t.Run("COUNT_MISMATCH", func(t *testing.T) {
		m := mustBackup(t, svc, backupID(t))
		emit(t, db, p)
		tok, err := svc.ApproveRestore(ctx, p, m.BackupID)
		if err != nil {
			t.Fatal(err)
		}
		res, err := svc.Restore(ctx, RestoreOpts{BackupID: m.BackupID, Target: "offsite", ApprovalToken: tok.String(), Actor: p})
		if err != nil || res.RefusalCode != RefuseCountMismatch {
			t.Fatalf("%+v %v", res, err)
		}
	})

	t.Run("CHAIN_BREAK", func(t *testing.T) {
		m := mustBackup(t, svc, backupID(t))
		var mine []ChainProof
		for _, c := range m.Chains {
			if c.CompanyID == p.CompanyID.String() {
				mine = append(mine, c)
			}
		}
		m.Chains = mine
		if err := svc.PutManifest(ctx, "offsite", m); err != nil {
			t.Fatal(err)
		}
		var id string
		if err := db.App.QueryRow(ctx, `SELECT id::text FROM erp.audit_events WHERE company_id=$1 ORDER BY chain_seq DESC LIMIT 1`, p.CompanyID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Admin.Exec(ctx, `ALTER TABLE erp.audit_events DISABLE TRIGGER immutable_guard`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Admin.Exec(ctx, `UPDATE erp.audit_events SET hash=repeat('ab', 32) WHERE id=$1::uuid`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Admin.Exec(ctx, `ALTER TABLE erp.audit_events ENABLE TRIGGER immutable_guard`); err != nil {
			t.Fatal(err)
		}
		tok, err := svc.ApproveRestore(ctx, p, m.BackupID)
		if err != nil {
			t.Fatal(err)
		}
		res, err := svc.Restore(ctx, RestoreOpts{BackupID: m.BackupID, Target: "offsite", ApprovalToken: tok.String(), Actor: p})
		if err != nil || res.RefusalCode != RefuseChainBreak {
			t.Fatalf("%+v %v", res, err)
		}
		cleanupCompany(t, db, p.CompanyID)
	})

	t.Run("ANCHOR_MISMATCH", func(t *testing.T) {
		fresh := principal("stakeholder")
		emit(t, db, fresh)
		m := mustBackup(t, svc, backupID(t))
		tok, err := svc.ApproveRestore(ctx, fresh, m.BackupID)
		if err != nil {
			t.Fatal(err)
		}
		res, err := svc.Restore(ctx, RestoreOpts{BackupID: m.BackupID, Target: "offsite", ApprovalToken: tok.String(), Actor: fresh})
		if err != nil || res.RefusalCode != RefuseAnchorMismatch || res.WritesEnabled {
			t.Fatalf("%+v %v", res, err)
		}
	})
}

// I5: post-restore counts and ledger totals are re-derived, and the chain matches an anchor at or before the restore point.
func TestI5_PostRestoreRederivesAndMatchesAnchor(t *testing.T) {
	db := testdb.New(t)
	svc := newSvc(t, db, newMemStore(), newMemStore())
	p := principal("stakeholder")
	ctx := context.Background()
	emit(t, db, p)
	emit(t, db, p)
	anchorAll(t, svc)
	m := mustBackup(t, svc, backupID(t))
	tok, err := svc.ApproveRestore(ctx, p, m.BackupID)
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.Restore(ctx, RestoreOpts{BackupID: m.BackupID, Target: "offsite", ApprovalToken: tok.String(), Actor: p})
	if err != nil || !res.WritesEnabled || res.RefusalCode != "" {
		t.Fatalf("%+v %v", res, err)
	}
	counts, err := svc.snapshotCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for table, want := range m.Counts {
		if table == "erp.audit_events" || table == "erp.chain_head_log" {
			if counts[table] != want+1 {
				t.Fatalf("re-derived %s = %d, manifest %d plus the restore audit row", table, counts[table], want)
			}
			continue
		}
		if counts[table] != want {
			t.Fatalf("re-derived %s = %d, manifest %d", table, counts[table], want)
		}
	}
	ledger, err := svc.snapshotLedger(ctx)
	if err != nil || ledger != m.Ledger {
		t.Fatalf("ledger %+v vs %+v (%v)", ledger, m.Ledger, err)
	}
}

// I6: restore requires Stakeholder approval before writes are served, and the attempt is logged.
func TestI6_RestoreRequiresStakeholderApproval(t *testing.T) {
	db := testdb.New(t)
	svc := newSvc(t, db, newMemStore(), newMemStore())
	ctx := context.Background()
	holder := principal("stakeholder")
	emit(t, db, holder)
	anchorAll(t, svc)
	refused := mustBackup(t, svc, backupID(t))
	res, err := svc.Restore(ctx, RestoreOpts{BackupID: refused.BackupID, Target: "offsite", Actor: holder})
	if err != nil || res.WritesEnabled || res.RefusalCode != RefuseApprovalRequired {
		t.Fatalf("%+v %v", res, err)
	}
	open, err := svc.ServingWrites(ctx)
	if err != nil || open {
		t.Fatalf("environment must not serve writes, open=%v err=%v", open, err)
	}
	anchorAll(t, svc)
	m := mustBackup(t, svc, backupID(t))
	mgr := holder
	mgr.Roles = []string{"system_manager"}
	if _, err := svc.ApproveRestore(ctx, mgr, m.BackupID); err == nil {
		t.Fatal("system manager must not approve a restore")
	}
	tok, err := svc.ApproveRestore(ctx, holder, m.BackupID)
	if err != nil {
		t.Fatal(err)
	}
	res, err = svc.Restore(ctx, RestoreOpts{BackupID: m.BackupID, Target: "offsite", ApprovalToken: tok.String(), Actor: holder})
	if err != nil || !res.WritesEnabled {
		t.Fatalf("%+v %v", res, err)
	}
	open, err = svc.ServingWrites(ctx)
	if err != nil || !open {
		t.Fatalf("approved restore must serve writes, open=%v err=%v", open, err)
	}
	var n int
	if err := db.App.QueryRow(ctx, `SELECT count(*) FROM erp.audit_events WHERE company_id=$1 AND event_type='restore.applied'`, holder.CompanyID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("restore must be logged, n=%d err=%v", n, err)
	}
}

// I7: retention prunes only after a verified successful backup.
func TestI7_RetentionPrunesOnlyAfterVerifiedSuccess(t *testing.T) {
	db := testdb.New(t)
	on := newMemStore()
	svc := newSvc(t, db, on, newMemStore())
	svc.RetainFor = time.Hour
	ctx := context.Background()
	oldKey := "backups/old/meta/counts.json"
	if err := on.PutCompliance(ctx, oldKey, []byte("old"), time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	keys, _ := json.Marshal([]string{oldKey})
	oldID := backupID(t)
	blockID := backupID(t)
	if _, err := db.App.Exec(ctx, `INSERT INTO erp.backup_runs(id, result, offsite_verified, site, file_keys, started_at, finished_at)
		VALUES ($1,'ok',true,'dev',$2, now()-interval '2 days', now()-interval '2 days')`, oldID, string(keys)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.App.Exec(ctx, `INSERT INTO erp.backup_runs(id, result, offsite_verified, site, started_at, finished_at)
		VALUES ($1,'failed',false,'dev', now(), (SELECT coalesce(max(finished_at), clock_timestamp()) FROM erp.backup_runs) + interval '1 day')`, blockID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Prune(ctx, time.Now()); !errors.Is(err, ErrRetentionBlocked) {
		t.Fatalf("prune before verified success: %v", err)
	}
	if ok, _ := on.Exists(ctx, oldKey); !ok {
		t.Fatal("object was pruned before a verified success")
	}
	if _, err := db.App.Exec(ctx, `UPDATE erp.backup_runs SET result='ok', offsite_verified=true WHERE id=$1`, blockID); err != nil {
		t.Fatal(err)
	}
	n, err := svc.Prune(ctx, time.Now())
	if err != nil || n < 1 {
		t.Fatalf("prune after verified success: n=%d err=%v", n, err)
	}
	if ok, _ := on.Exists(ctx, oldKey); ok {
		t.Fatal("local object should have been pruned")
	}
}

// I8: a failed backup alerts System Managers and Stakeholders and appears on the status page.
func TestI8_FailedBackupAlertsAndStatus(t *testing.T) {
	db := testdb.New(t)
	svc := newSvc(t, db, newMemStore(), failStore{})
	id := backupID(t)
	if _, err := svc.RunBackup(context.Background(), id); err == nil {
		t.Fatal("expected backup failure")
	}
	if offsiteVerified(t, db, id) {
		t.Fatal("failed backup must not be reported successful")
	}
	if _, err := db.App.Exec(context.Background(), `UPDATE erp.backup_runs SET finished_at = (SELECT max(finished_at) FROM erp.backup_runs) + interval '1 day' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	st, err := svc.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.LastBackup.Result != "failed" {
		t.Fatalf("status last_backup: %+v", st.LastBackup)
	}
	found := false
	for _, args := range eventsOfType(t, db, "backup.failed") {
		var ev DomainEvent
		if err := json.Unmarshal(args.Payload, &ev); err != nil {
			t.Fatal(err)
		}
		if ev.Subject.DocID != id {
			continue
		}
		aud, _ := ev.Context["audience"].([]any)
		joined := ""
		for _, a := range aud {
			name, ok := a.(string)
			if !ok {
				t.Fatalf("audience entry %T", a)
			}
			joined += name + ","
		}
		if !strings.Contains(joined, "system_manager") || !strings.Contains(joined, "stakeholder") {
			t.Fatalf("audience %v", ev.Context["audience"])
		}
		found = true
	}
	if !found {
		t.Fatal("expected backup.failed for system managers and stakeholders")
	}
}

// I15: a backup is successful only after the off-site copy verifies; an off-site anchor failure for two hours is a Critical.
func TestI15_OffsiteVerifyAndStaleAnchorCritical(t *testing.T) {
	db := testdb.New(t)
	on, off := newMemStore(), newMemStore()
	svc := newSvc(t, db, on, off)
	ctx := context.Background()
	p := principal()
	emit(t, db, p)

	good := mustBackup(t, svc, backupID(t))
	if !offsiteVerified(t, db, good.BackupID) {
		t.Fatal("backup must be successful only after off-site verification")
	}

	id := backupID(t)
	m, err := svc.WriteArchives(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Replicate(ctx, m); err != nil {
		t.Fatal(err)
	}
	off.corrupt(archiveObjectKey(id, "meta/counts.json"))
	if err := svc.VerifyOffsite(ctx, m); err == nil {
		t.Fatal("corrupt off-site copy must not verify")
	}
	if offsiteVerified(t, db, id) {
		t.Fatal("unverified off-site copy must not be reported successful")
	}

	past := time.Now().Add(-3 * time.Hour)
	if _, err := db.App.Exec(ctx, `INSERT INTO erp.anchor_attempts(company_id, attempted_at, offsite_ok, detail) VALUES ($1,$2,true,''), ($1,$3,false,'timeout')`, p.CompanyID, past, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := svc.RaiseIfAnchorStale(ctx, p.CompanyID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, args := range eventsOfType(t, db, "chain.broken") {
		if !args.QuietHoursExempt {
			continue
		}
		var ev DomainEvent
		if err := json.Unmarshal(args.Payload, &ev); err != nil {
			t.Fatal(err)
		}
		if ev.CompanyID == p.CompanyID.String() && ev.Severity == "CRITICAL" && ev.Context["reason"] == "offsite_anchor_stale" {
			found = true
		}
	}
	if !found {
		t.Fatal("two-hour off-site anchor failure must raise a Critical")
	}
}

// I16: WAL reaches the off-site bucket within 15 minutes, and the status page shows lag and the recovery point in force.
func TestI16_WALArchiveLagAndRecoveryPoint(t *testing.T) {
	db := testdb.New(t)
	off := newMemStore()
	svc := newSvc(t, db, newMemStore(), off)
	ctx := context.Background()
	if _, err := db.App.Exec(ctx, `UPDATE erp.wal_segments SET archived_at = written_at WHERE archived_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	name := "00000001000000000000000" + uuid.NewString()[:6]
	written := time.Now().UTC()
	if err := svc.NoteWAL(ctx, name, written, []byte("wal-bytes")); err != nil {
		t.Fatal(err)
	}
	if err := svc.ArchiveWAL(ctx, name); err != nil {
		t.Fatal(err)
	}
	body, err := off.Get(ctx, "wal/"+name)
	if err != nil || string(body) != "wal-bytes" {
		t.Fatalf("segment not off-site: %q %v", body, err)
	}
	var archived time.Time
	if err := db.App.QueryRow(ctx, `SELECT archived_at FROM erp.wal_segments WHERE name=$1`, name).Scan(&archived); err != nil {
		t.Fatal(err)
	}
	if archived.Sub(written) > WALArchiveFresh {
		t.Fatalf("segment took %s to reach off-site", archived.Sub(written))
	}
	lag, point, err := svc.ArchiveLag(ctx, time.Now().UTC())
	if err != nil || point != "15m" || lag > WALArchiveFresh {
		t.Fatalf("fresh lag=%s point=%s err=%v", lag, point, err)
	}
	st, err := svc.Status(ctx)
	if err != nil || st.RecoveryPointInForce != "15m" || st.LastBackup.Detail == nil || !strings.Contains(*st.LastBackup.Detail, "15m") {
		t.Fatalf("status: %+v %v", st, err)
	}

	stale := "00000001000000000000000" + uuid.NewString()[:6]
	if err := svc.NoteWAL(ctx, stale, time.Now().Add(-20*time.Minute), []byte("late")); err != nil {
		t.Fatal(err)
	}
	lag, point, err = svc.ArchiveLag(ctx, time.Now().UTC())
	if err != nil || point != "24h" || lag < 15*time.Minute {
		t.Fatalf("stale lag=%s point=%s err=%v", lag, point, err)
	}
	st, err = svc.Status(ctx)
	if err != nil || st.RecoveryPointInForce != "24h" {
		t.Fatalf("status recovery point: %+v %v", st, err)
	}
}

func anchorAll(t *testing.T, svc *Service) {
	t.Helper()
	ctx := context.Background()
	rows, err := svc.Pool.Query(ctx, `SELECT company_id FROM erp.chain_head`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if err := svc.Anchor(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	// The backup that follows must be at or after these anchors (I5).
	time.Sleep(1100 * time.Millisecond)
}
