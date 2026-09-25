package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/audit"
	kitaudit "github.com/jaichahal/smart-erp/apps/api/internal/kit/audit"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/canon"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// B9: the hourly anchor is written to a compliance-mode bucket and the writer cannot delete or overwrite it.
func TestB9_ComplianceAnchorCannotBeDeletedOrOverwritten(t *testing.T) {
	e := newB2(t)
	ctx := context.Background()
	e.emit(t)
	if err := e.svc.Anchor(ctx, e.p.CompanyID); err != nil {
		t.Fatal(err)
	}
	key := "anchors/" + e.p.CompanyID.String() + "/1.json"
	body, err := e.svc.Offsite.Get(ctx, key)
	if err != nil || len(body) == 0 {
		t.Fatalf("off-site anchor missing: %v", err)
	}
	if _, err := e.svc.OnPrem.Get(ctx, key); err != nil {
		t.Fatalf("on-prem anchor missing: %v", err)
	}
	if err := e.svc.Offsite.PutCompliance(ctx, key, []byte("overwrite"), time.Now().Add(24*time.Hour)); !errors.Is(err, audit.ErrImmutableObject) {
		t.Fatalf("writer overwrite must fail, got %v", err)
	}
	if err := e.svc.Offsite.Delete(ctx, key); !errors.Is(err, audit.ErrImmutableObject) {
		t.Fatalf("writer delete must fail, got %v", err)
	}
	got, err := e.svc.Offsite.Get(ctx, key)
	if err != nil || string(got) != string(body) {
		t.Fatal("locked anchor bytes changed")
	}
	versionKey := "b2-e2e/" + uuid.NewString() + ".json"
	client := offsiteClient(t)
	version, err := client.PutLockedVersion(ctx, versionKey, []byte("anchor-body"), time.Now().Add(24*time.Hour))
	if err != nil || version == "" {
		t.Fatalf("put version: %q %v", version, err)
	}
	if err := client.PutCompliance(ctx, versionKey, []byte("overwrite"), time.Now().Add(24*time.Hour)); !errors.Is(err, audit.ErrImmutableObject) {
		t.Fatalf("offsite overwrite must fail, got %v", err)
	}
	if err := client.DeleteVersion(ctx, versionKey, version); !errors.Is(err, audit.ErrImmutableObject) {
		t.Fatalf("deleting the locked version must fail, got %v", err)
	}
	still, err := client.GetVersion(ctx, versionKey, version)
	if err != nil || string(still) != "anchor-body" {
		t.Fatalf("locked version must still be readable: %q %v", still, err)
	}
}

// B10: the anchor signature verifies with the KMS public key, and the database operator cannot Sign.
func TestB10_SignatureVerifiesAndOperatorCannotSign(t *testing.T) {
	e := newB2(t)
	ctx := context.Background()
	e.emit(t)
	if err := e.svc.Anchor(ctx, e.p.CompanyID); err != nil {
		t.Fatal(err)
	}
	var payload, sigText string
	if err := e.db.App.QueryRow(ctx, `SELECT payload, signature FROM erp.anchors WHERE company_id=$1`, e.p.CompanyID).Scan(&payload, &sigText); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.KMS.Sign(audit.IdentityDBOperator, []byte(payload)); !errors.Is(err, audit.ErrSignDenied) {
		t.Fatalf("database operator Sign must be denied, got %v", err)
	}
	sig, err := e.svc.KMS.Sign(audit.IdentityAnchorWorker, []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if !e.svc.KMS.Verify([]byte(payload), sig) {
		t.Fatal("signature does not verify with the KMS public key")
	}
	body, err := e.svc.Offsite.Get(ctx, "anchors/"+e.p.CompanyID.String()+"/1.json")
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(body, &env); err != nil || env.Signature == "" {
		t.Fatalf("off-site anchor has no signature: %s", body)
	}
	if env.Signature != sigText {
		t.Fatal("off-site signature does not match the anchored signature")
	}
}

// B11: a recomputed head that differs from the anchored head is a break and a Critical quiet hours do not suppress.
func TestB11_AnchorMismatchIsCritical(t *testing.T) {
	e := newB2(t)
	ctx := context.Background()
	for range 3 {
		e.emit(t)
	}
	if err := e.svc.Anchor(ctx, e.p.CompanyID); err != nil {
		t.Fatal(err)
	}
	id, canonical, prev, _ := tamperHash(t, e.db, e.p.CompanyID, true)
	rewritten := strings.Replace(canonical, `"reason":""`, `"reason":"rewritten"`, 1)
	rewriteRow(t, e.db, id, rewritten, canon.Hash([]byte(rewritten), prev))
	for range 2 {
		e.emit(t)
	}
	res := e.verifyHTTP(t)
	if res.Intact || res.AnchoredHeadMatches || res.FirstBreak == nil || res.FirstBreak.ChainSeq != 3 {
		t.Fatalf("expected first break at seq 3: %+v", res)
	}
	if res.Count != 5 {
		t.Fatalf("verifier must count every row, got %d", res.Count)
	}
	if res.FirstBreak.Reason == nil || !strings.Contains(strings.ToLower(*res.FirstBreak.Reason), "anchor") {
		t.Fatalf("reason should name the anchor mismatch: %+v", res.FirstBreak)
	}
	if broken := criticalBreaks(t, e, e.p.CompanyID.String()); broken == 0 {
		t.Fatal("expected a chain.broken Critical that quiet hours do not suppress")
	}
	if err := e.svc.SendDailyAnchorEmail(ctx); err != nil {
		t.Fatal(err)
	}
}

// I2: archives are written, then the manifest; an interrupted run leaves no manifest.
func TestI2_InterruptedBackupLeavesNoManifest(t *testing.T) {
	e := newB2(t)
	ctx := context.Background()
	id := "b2-" + uuid.NewString()
	if _, err := e.svc.WriteArchives(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.LoadManifest(ctx, "offsite", id); err == nil {
		t.Fatal("interrupted run must not leave an off-site manifest")
	}
	if _, err := e.svc.LoadManifest(ctx, "onprem", id); err == nil {
		t.Fatal("interrupted run must not leave an on-prem manifest")
	}
	var result, manifest string
	if err := e.db.App.QueryRow(ctx, `SELECT result, manifest_json FROM erp.backup_runs WHERE id=$1`, id).Scan(&result, &manifest); err != nil {
		t.Fatal(err)
	}
	if result == "ok" || manifest != "" {
		t.Fatalf("run must not be successful, got %s %q", result, manifest)
	}
}

// I3: per-file SHA-256 and the aggregate checksum verify, and counts and ledger totals are recorded.
func TestI3_ManifestChecksumsCountsAndLedger(t *testing.T) {
	e := newB2(t)
	e.emit(t)
	m := mustRunBackup(t, e)
	if err := e.svc.VerifyOffsite(context.Background(), m); err != nil {
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
	e := newB2(t, "stakeholder")
	ctx := context.Background()
	e.emit(t)
	p := e.p

	t.Run("MANIFEST_MISSING", func(t *testing.T) {
		res, err := e.svc.Restore(ctx, audit.RestoreOpts{BackupID: "missing-" + uuid.NewString(), Target: "offsite", Actor: p})
		if err != nil || res.RefusalCode != audit.RefuseManifestMissing || res.WritesEnabled {
			t.Fatalf("%+v %v", res, err)
		}
	})

	t.Run("MANIFEST_INCOMPLETE", func(t *testing.T) {
		id := "b2-" + uuid.NewString()
		if err := e.svc.Offsite.PutCompliance(ctx, "backups/"+id+"/manifest.json", []byte("{"), time.Now().Add(24*time.Hour)); err != nil {
			t.Fatal(err)
		}
		res, err := e.svc.Restore(ctx, audit.RestoreOpts{BackupID: id, Target: "offsite", Actor: p})
		if err != nil || res.RefusalCode != audit.RefuseManifestIncomplete {
			t.Fatalf("%+v %v", res, err)
		}
	})

	t.Run("FILE_CHECKSUM", func(t *testing.T) {
		m := stagedManifest(t, e)
		m.Files[0].SHA256 = strings.Repeat("ab", 32)
		if err := e.svc.PutManifest(ctx, "offsite", m); err != nil {
			t.Fatal(err)
		}
		res, err := e.svc.Restore(ctx, audit.RestoreOpts{BackupID: m.BackupID, Target: "offsite", Force: true, Actor: p})
		if err != nil || res.RefusalCode != audit.RefuseFileChecksum || res.WritesEnabled {
			t.Fatalf("%+v %v", res, err)
		}
	})

	t.Run("AGGREGATE_CHECKSUM", func(t *testing.T) {
		m := stagedManifest(t, e)
		m.AggregateSHA256 = strings.Repeat("0", 64)
		if err := e.svc.PutManifest(ctx, "offsite", m); err != nil {
			t.Fatal(err)
		}
		res, err := e.svc.Restore(ctx, audit.RestoreOpts{BackupID: m.BackupID, Target: "offsite", Force: true, Actor: p})
		if err != nil || res.RefusalCode != audit.RefuseAggregateChecksum {
			t.Fatalf("%+v %v", res, err)
		}
	})

	t.Run("SITE_MISMATCH", func(t *testing.T) {
		m := stagedManifest(t, e)
		m.Site = "other-site"
		if err := e.svc.PutManifest(ctx, "offsite", m); err != nil {
			t.Fatal(err)
		}
		res, err := e.svc.Restore(ctx, audit.RestoreOpts{BackupID: m.BackupID, Target: "offsite", Actor: p})
		if err != nil || res.RefusalCode != audit.RefuseSiteMismatch {
			t.Fatalf("without force: %+v %v", res, err)
		}
		res, err = e.svc.Restore(ctx, audit.RestoreOpts{BackupID: m.BackupID, Target: "offsite", Force: true, Actor: p})
		if err != nil || res.RefusalCode != audit.RefuseApprovalRequired {
			t.Fatalf("force must pass the site check and stop on approval: %+v %v", res, err)
		}
	})

	t.Run("LEDGER_MISMATCH", func(t *testing.T) {
		m := stagedManifest(t, e)
		m.Ledger.Debits = "1.00"
		if err := e.svc.PutManifest(ctx, "offsite", m); err != nil {
			t.Fatal(err)
		}
		tok, err := e.svc.ApproveRestore(ctx, p, m.BackupID)
		if err != nil {
			t.Fatal(err)
		}
		res, err := e.svc.Restore(ctx, audit.RestoreOpts{BackupID: m.BackupID, Target: "offsite", ApprovalToken: tok.String(), Actor: p})
		if err != nil || res.RefusalCode != audit.RefuseLedgerMismatch {
			t.Fatalf("%+v %v", res, err)
		}
	})

	t.Run("COUNT_MISMATCH", func(t *testing.T) {
		m := mustRunBackup(t, e)
		e.emit(t)
		tok, err := e.svc.ApproveRestore(ctx, p, m.BackupID)
		if err != nil {
			t.Fatal(err)
		}
		res, err := e.svc.Restore(ctx, audit.RestoreOpts{BackupID: m.BackupID, Target: "offsite", ApprovalToken: tok.String(), Actor: p})
		if err != nil || res.RefusalCode != audit.RefuseCountMismatch {
			t.Fatalf("%+v %v", res, err)
		}
	})

	t.Run("CHAIN_BREAK", func(t *testing.T) {
		e.anchorEveryone(t)
		m := mustRunBackup(t, e)
		id, _, _, _ := tamperHash(t, e.db, p.CompanyID, true)
		rewriteRow(t, e.db, id, "not-canonical", strings.Repeat("ab", 32))
		tok, err := e.svc.ApproveRestore(ctx, p, m.BackupID)
		if err != nil {
			t.Fatal(err)
		}
		res, err := e.svc.Restore(ctx, audit.RestoreOpts{BackupID: m.BackupID, Target: "offsite", ApprovalToken: tok.String(), Actor: p})
		if err != nil || res.RefusalCode != audit.RefuseChainBreak {
			t.Fatalf("%+v %v", res, err)
		}
	})

	t.Run("ANCHOR_MISMATCH", func(t *testing.T) {
		e.anchorEveryone(t)
		fresh := rls.Principal{UserID: "b2-" + uuid.NewString(), CompanyID: uuid.New(), Roles: []string{"stakeholder"}}
		err := emitAs(t, e, fresh)
		if err != nil {
			t.Fatal(err)
		}
		saved := e.p
		e.p = fresh
		m := mustRunBackup(t, e)
		e.p = saved
		tok, err := e.svc.ApproveRestore(ctx, fresh, m.BackupID)
		if err != nil {
			t.Fatal(err)
		}
		res, err := e.svc.Restore(ctx, audit.RestoreOpts{BackupID: m.BackupID, Target: "offsite", ApprovalToken: tok.String(), Actor: fresh})
		if err != nil || res.RefusalCode != audit.RefuseAnchorMismatch || res.WritesEnabled {
			t.Fatalf("%+v %v", res, err)
		}
	})
}

// I5: post-restore counts and ledger totals are re-derived, and the chain matches an anchor at or before the restore point.
func TestI5_PostRestoreRederivesAndMatchesAnchor(t *testing.T) {
	e := newB2(t, "stakeholder")
	ctx := context.Background()
	e.emit(t)
	e.emit(t)
	e.anchorEveryone(t)
	if err := e.svc.Anchor(ctx, e.p.CompanyID); err != nil {
		t.Fatal(err)
	}
	m := mustRunBackup(t, e)
	tok, err := e.svc.ApproveRestore(ctx, e.p, m.BackupID)
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.Restore(ctx, audit.RestoreOpts{BackupID: m.BackupID, Target: "offsite", ApprovalToken: tok.String(), Actor: e.p})
	if err != nil || res.RefusalCode != "" || !res.WritesEnabled {
		t.Fatalf("restore must re-derive and match the anchor: %+v %v", res, err)
	}
}

// I6: restore requires Stakeholder approval before the environment serves writes, and the attempt is logged.
func TestI6_RestoreRequiresStakeholderApproval(t *testing.T) {
	e := newB2(t, "stakeholder")
	ctx := context.Background()
	e.emit(t)
	e.anchorEveryone(t)
	m := mustRunBackup(t, e)
	res, err := e.svc.Restore(ctx, audit.RestoreOpts{BackupID: m.BackupID, Target: "offsite", Actor: e.p})
	if err != nil || res.RefusalCode != audit.RefuseApprovalRequired || res.WritesEnabled {
		t.Fatalf("unapproved: %+v %v", res, err)
	}
	open, err := e.svc.ServingWrites(ctx)
	if err != nil || open {
		t.Fatalf("unapproved restore must not serve writes, open=%v err=%v", open, err)
	}
	e.anchorEveryone(t)
	m = mustRunBackup(t, e)
	accountant := e.p
	accountant.Roles = []string{"accountant"}
	if _, err := e.svc.ApproveRestore(ctx, accountant, m.BackupID); err == nil {
		t.Fatal("only a Stakeholder may approve a restore")
	}
	tok, err := e.svc.ApproveRestore(ctx, e.p, m.BackupID)
	if err != nil {
		t.Fatal(err)
	}
	res, err = e.svc.Restore(ctx, audit.RestoreOpts{BackupID: m.BackupID, Target: "offsite", ApprovalToken: tok.String(), Actor: e.p})
	if err != nil || !res.WritesEnabled {
		t.Fatalf("approved restore must serve writes: %+v %v", res, err)
	}
	var n int
	if err := e.db.App.QueryRow(ctx, `SELECT count(*) FROM erp.audit_events WHERE company_id=$1 AND event_type='restore.applied'`, e.p.CompanyID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("restore must be logged, n=%d err=%v", n, err)
	}
}

// I7: retention prunes only after a verified successful backup.
func TestI7_RetentionPrunesOnlyAfterVerifiedSuccess(t *testing.T) {
	e := newB2(t)
	e.svc.RetainFor = time.Hour
	ctx := context.Background()
	oldKey := "backups/b2-old-" + uuid.NewString() + "/meta/counts.json"
	if err := e.svc.OnPrem.PutCompliance(ctx, oldKey, []byte("old"), time.Now().Add(-time.Hour)); err != nil {
		if err = e.svc.OnPrem.PutCompliance(ctx, oldKey, []byte("old"), time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	keys, _ := json.Marshal([]string{oldKey})
	oldID := "b2-" + uuid.NewString()
	blockID := "b2-" + uuid.NewString()
	if _, err := e.db.App.Exec(ctx, `INSERT INTO erp.backup_runs(id, result, offsite_verified, site, file_keys, started_at, finished_at)
		VALUES ($1,'ok',true,'dev',$2, now()-interval '2 days', now()-interval '2 days')`, oldID, string(keys)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.App.Exec(ctx, `INSERT INTO erp.backup_runs(id, result, offsite_verified, site, started_at, finished_at)
		VALUES ($1,'failed',false,'dev', now(), (SELECT coalesce(max(finished_at), clock_timestamp()) FROM erp.backup_runs) + interval '1 day')`, blockID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Prune(ctx, time.Now()); !errors.Is(err, audit.ErrRetentionBlocked) {
		t.Fatalf("prune before verified success: %v", err)
	}
	if ok, _ := e.svc.OnPrem.Exists(ctx, oldKey); !ok {
		t.Fatal("object was pruned before a verified success")
	}
	if _, err := e.db.App.Exec(ctx, `UPDATE erp.backup_runs SET result='ok', offsite_verified=true WHERE id=$1`, blockID); err != nil {
		t.Fatal(err)
	}
	n, err := e.svc.Prune(ctx, time.Now())
	if err != nil || n < 1 {
		t.Fatalf("prune after verified success: n=%d err=%v", n, err)
	}
	var pruned bool
	if err := e.db.App.QueryRow(ctx, `SELECT pruned_at IS NOT NULL FROM erp.backup_runs WHERE id=$1`, oldID).Scan(&pruned); err != nil || !pruned {
		t.Fatalf("old backup must be marked pruned: %v", err)
	}
	if ok, _ := e.svc.OnPrem.Exists(ctx, oldKey); ok {
		if err := e.svc.OnPrem.Delete(ctx, oldKey); !errors.Is(err, audit.ErrImmutableObject) {
			t.Fatalf("a remaining object must still be compliance-locked, delete: %v", err)
		}
	}
}

// I8: a failed backup alerts System Managers and Stakeholders and appears on the status page.
func TestI8_FailedBackupAlertsAndStatus(t *testing.T) {
	e := newB2(t)
	e.svc.Offsite = audit.NewS3Client(osGetenv("ERP_OFFSITE_S3_ENDPOINT"), "us-east-1", osGetenv("ERP_S3_ACCESS_KEY"), osGetenv("ERP_S3_SECRET_KEY"), "b2-missing-bucket")
	id := "b2-" + uuid.NewString()
	if _, err := e.svc.RunBackup(context.Background(), id); err == nil {
		t.Fatal("expected backup failure")
	}
	var verified bool
	var result string
	if err := e.db.App.QueryRow(context.Background(), `SELECT offsite_verified, result FROM erp.backup_runs WHERE id=$1`, id).Scan(&verified, &result); err != nil {
		t.Fatal(err)
	}
	if verified || result == "ok" {
		t.Fatal("failed backup must not be reported successful")
	}
	if _, err := e.db.App.Exec(context.Background(), `UPDATE erp.backup_runs SET finished_at = (SELECT coalesce(max(finished_at), clock_timestamp()) FROM erp.backup_runs) + interval '1 second' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	st := e.statusHTTP(t)
	if st.LastBackup.Result != "failed" {
		t.Fatalf("status last_backup: %+v", st.LastBackup)
	}
	if !failedBackupAlert(t, e, id) {
		t.Fatal("expected backup.failed for system managers and stakeholders")
	}
}

// I15: a backup is successful only after the off-site copy verifies; an off-site anchor failure for two hours is a Critical.
func TestI15_OffsiteVerifyAndStaleAnchorCritical(t *testing.T) {
	e := newB2(t)
	ctx := context.Background()
	e.emit(t)
	good := mustRunBackup(t, e)
	var verified bool
	var result string
	if err := e.db.App.QueryRow(ctx, `SELECT offsite_verified, result FROM erp.backup_runs WHERE id=$1`, good.BackupID).Scan(&verified, &result); err != nil {
		t.Fatal(err)
	}
	if !verified || result != "ok" {
		t.Fatal("backup must be successful only after off-site verification")
	}
	id := "b2-" + uuid.NewString()
	m, err := e.svc.WriteArchives(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Replicate(ctx, m); err != nil {
		t.Fatal(err)
	}
	m.Files[0].SHA256 = strings.Repeat("cd", 32)
	if err := e.svc.VerifyOffsite(ctx, m); err == nil {
		t.Fatal("an off-site copy that does not match its checksum must not verify")
	}
	if err := e.db.App.QueryRow(ctx, `SELECT offsite_verified, result FROM erp.backup_runs WHERE id=$1`, id).Scan(&verified, &result); err != nil {
		t.Fatal(err)
	}
	if verified || result == "ok" {
		t.Fatal("unverified off-site copy must not be reported successful")
	}
	past := time.Now().Add(-3 * time.Hour)
	if _, err := e.db.App.Exec(ctx, `INSERT INTO erp.anchor_attempts(company_id, attempted_at, offsite_ok, detail) VALUES ($1,$2,true,''), ($1,$3,false,'timeout')`, e.p.CompanyID, past, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RaiseIfAnchorStale(ctx, e.p.CompanyID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if criticalBreaks(t, e, e.p.CompanyID.String()) == 0 {
		t.Fatal("a two-hour off-site anchor failure must raise a Critical")
	}
}

// I16: WAL segments reach the off-site bucket within 15 minutes, and the status page shows lag and the recovery point.
func TestI16_WALArchiveLagAndRecoveryPoint(t *testing.T) {
	e := newB2(t)
	ctx := context.Background()
	if _, err := e.db.Admin.Exec(ctx, `DELETE FROM erp.wal_segments`); err != nil {
		t.Fatal(err)
	}
	name := "b2-" + uuid.NewString()
	if err := e.svc.NoteWAL(ctx, name, time.Now().UTC(), []byte("wal-bytes")); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.ArchiveWAL(ctx, name); err != nil {
		t.Fatal(err)
	}
	body, err := e.svc.Offsite.Get(ctx, "wal/"+name)
	if err != nil || string(body) != "wal-bytes" {
		t.Fatalf("WAL segment did not reach the off-site bucket: %q %v", body, err)
	}
	lag, point, err := e.svc.ArchiveLag(ctx, time.Now().UTC())
	if err != nil || point != "15m" || lag > audit.WALArchiveFresh {
		t.Fatalf("fresh archive lag=%s point=%s err=%v", lag, point, err)
	}
	st := e.statusHTTP(t)
	if st.RecoveryPointInForce != "15m" {
		t.Fatalf("status recovery point: %+v", st)
	}
	if st.LastBackup.Detail == nil || !strings.Contains(*st.LastBackup.Detail, "15m") {
		t.Fatalf("status page must show the archive lag: %+v", st.LastBackup)
	}
	stale := "b2-stale-" + uuid.NewString()
	if err := e.svc.NoteWAL(ctx, stale, time.Now().Add(-20*time.Minute), []byte("late")); err != nil {
		t.Fatal(err)
	}
	lag, point, err = e.svc.ArchiveLag(ctx, time.Now().UTC())
	if err != nil || point != "24h" || lag <= audit.WALArchiveFresh {
		t.Fatalf("stale lag=%s point=%s err=%v", lag, point, err)
	}
	st = e.statusHTTP(t)
	if st.RecoveryPointInForce != "24h" {
		t.Fatalf("status recovery point after lag: %+v", st.RecoveryPointInForce)
	}
}

func mustRunBackup(t *testing.T, e *b2Env) *audit.Manifest {
	t.Helper()
	m, err := e.svc.RunBackup(context.Background(), "b2-"+uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func stagedManifest(t *testing.T, e *b2Env) *audit.Manifest {
	t.Helper()
	ctx := context.Background()
	m, err := e.svc.WriteArchives(ctx, "b2-"+uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Replicate(ctx, m); err != nil {
		t.Fatal(err)
	}
	return m
}

func criticalBreaks(t *testing.T, e *b2Env, company string) int {
	t.Helper()
	n := 0
	for _, args := range e.jobs(t, "chain.broken") {
		if !args.QuietHoursExempt {
			t.Fatal("chain.broken must be exempt from quiet hours")
		}
		var ev audit.DomainEvent
		if err := json.Unmarshal(args.Payload, &ev); err != nil {
			t.Fatal(err)
		}
		if ev.CompanyID != company {
			continue
		}
		if ev.Severity != "CRITICAL" {
			t.Fatalf("severity %s", ev.Severity)
		}
		if suppressed, _ := ev.Context["quiet_hours_suppressed"].(bool); suppressed {
			t.Fatal("quiet hours must not suppress the critical")
		}
		n++
	}
	return n
}

func failedBackupAlert(t *testing.T, e *b2Env, id string) bool {
	t.Helper()
	for _, args := range e.jobs(t, "backup.failed") {
		var ev audit.DomainEvent
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
		if strings.Contains(joined, "system_manager") && strings.Contains(joined, "stakeholder") {
			return true
		}
	}
	return false
}

func emitAs(t *testing.T, e *b2Env, p rls.Principal) error {
	t.Helper()
	return rls.Tx(context.Background(), e.db.App, p, func(tx pgx.Tx) error {
		_, err := kitaudit.EmitAs(context.Background(), tx, p, kitaudit.Event{Type: "TEST", ReferenceType: "doc", ReferenceID: uuid.NewString()})
		return err
	})
}

func offsiteClient(t *testing.T) *audit.S3Client {
	t.Helper()
	return audit.NewS3Client(osGetenv("ERP_OFFSITE_S3_ENDPOINT"), "us-east-1", osGetenv("ERP_S3_ACCESS_KEY"), osGetenv("ERP_S3_SECRET_KEY"), osGetenv("ERP_OFFSITE_S3_BUCKET"))
}

func osGetenv(k string) string { return envOr(k, "") }
