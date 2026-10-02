package audit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// ManifestSchema is the backup manifest version. P1.17 drive copies reuse this document.
const ManifestSchema = "smart-erp.backup.manifest/v1"

// ChainProof is one company's chain head recorded in a manifest.
type ChainProof struct {
	CompanyID string `json:"company_id"`
	ChainSeq  int64  `json:"chain_seq"`
	HeadHash  string `json:"head_hash"`
	RowCount  int64  `json:"row_count"`
}

// ManifestFile is one archive in the backup.
type ManifestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// LedgerTotals are debit and credit sums as decimal strings.
type LedgerTotals struct {
	Debits   string `json:"debits"`
	Credits  string `json:"credits"`
	Currency string `json:"currency"`
}

// Manifest is written after the archives, in one object, so a crash leaves none (I2).
type Manifest struct {
	Schema          string           `json:"schema"`
	BackupID        string           `json:"backup_id"`
	Site            string           `json:"site"`
	CreatedAt       time.Time        `json:"created_at"`
	Chains          []ChainProof     `json:"chains"`
	Counts          map[string]int64 `json:"counts"`
	Ledger          LedgerTotals     `json:"ledger"`
	Files           []ManifestFile   `json:"files"`
	AggregateSHA256 string           `json:"aggregate_sha256"`
}

func manifestObjectKey(id string) string { return "backups/" + id + "/manifest.json" }

func archiveObjectKey(id, path string) string { return "backups/" + id + "/" + path }

func fileSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func aggregateSHA256(files []ManifestFile) string {
	cp := append([]ManifestFile(nil), files...)
	sort.Slice(cp, func(i, j int) bool { return cp[i].Path < cp[j].Path })
	var buf bytes.Buffer
	for _, f := range cp {
		buf.WriteString(f.Path)
		buf.WriteByte('\n')
		buf.WriteString(f.SHA256)
		buf.WriteByte('\n')
	}
	return fileSHA256(buf.Bytes())
}

// WriteArchives stores archive objects and returns the manifest in memory.
// It does not write the manifest object (I2).
func (s *Service) WriteArchives(ctx context.Context, id string) (*Manifest, error) {
	if _, err := s.Pool.Exec(ctx, `INSERT INTO erp.backup_runs(id, result, site) VALUES ($1, 'running', $2)
		ON CONFLICT (id) DO UPDATE SET result='running', offsite_verified=false, manifest_json=''`, id, s.Site); err != nil {
		return nil, err
	}
	counts, err := s.snapshotCounts(ctx)
	if err != nil {
		return nil, err
	}
	ledger, err := s.snapshotLedger(ctx)
	if err != nil {
		return nil, err
	}
	chains, err := s.snapshotChains(ctx)
	if err != nil {
		return nil, err
	}
	payloads := map[string]any{
		"meta/counts.json": counts,
		"meta/ledger.json": ledger,
		"meta/chains.json": chains,
	}
	var files []ManifestFile
	var keys []string
	retain := s.now().Add(s.objectRetain())
	for _, path := range []string{"meta/chains.json", "meta/counts.json", "meta/ledger.json"} {
		body, err := json.Marshal(payloads[path])
		if err != nil {
			return nil, err
		}
		key := archiveObjectKey(id, path)
		if err := s.OnPrem.PutCompliance(ctx, key, body, retain); err != nil {
			return nil, err
		}
		files = append(files, ManifestFile{Path: path, SHA256: fileSHA256(body), Bytes: int64(len(body))})
		keys = append(keys, key)
	}
	m := &Manifest{
		Schema: ManifestSchema, BackupID: id, Site: s.Site, CreatedAt: s.now().Truncate(time.Second),
		Chains: chains, Counts: counts, Ledger: ledger, Files: files, AggregateSHA256: aggregateSHA256(files),
	}
	rawKeys, err := json.Marshal(keys)
	if err != nil {
		return nil, err
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE erp.backup_runs SET file_keys=$2 WHERE id=$1`, id, string(rawKeys)); err != nil {
		return nil, err
	}
	return m, nil
}

// Replicate copies archive bytes to the off-site bucket. It does not copy the manifest.
func (s *Service) Replicate(ctx context.Context, m *Manifest) error {
	retain := s.now().Add(s.objectRetain())
	for _, f := range m.Files {
		key := archiveObjectKey(m.BackupID, f.Path)
		body, err := s.OnPrem.Get(ctx, key)
		if err != nil {
			return err
		}
		if err := s.Offsite.PutCompliance(ctx, key, body, retain); err != nil {
			return err
		}
	}
	return nil
}

// VerifyOffsite re-reads the off-site copies and checks per-file and aggregate checksums (I3, I15).
func (s *Service) VerifyOffsite(ctx context.Context, m *Manifest) error {
	return verifyManifestFiles(ctx, s.Offsite, m)
}

func verifyManifestFiles(ctx context.Context, store ObjectStore, m *Manifest) error {
	for _, f := range m.Files {
		body, err := store.Get(ctx, archiveObjectKey(m.BackupID, f.Path))
		if err != nil {
			return fmt.Errorf("%s: %w", RefuseFileChecksum, err)
		}
		if fileSHA256(body) != f.SHA256 || int64(len(body)) != f.Bytes {
			return fmt.Errorf("%s: %s", RefuseFileChecksum, f.Path)
		}
	}
	if aggregateSHA256(m.Files) != m.AggregateSHA256 {
		return fmt.Errorf("%s", RefuseAggregateChecksum)
	}
	return nil
}

// CommitManifest writes the manifest object last, via a single put (I2).
func (s *Service) CommitManifest(ctx context.Context, m *Manifest) error {
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	retain := s.now().Add(s.objectRetain())
	key := manifestObjectKey(m.BackupID)
	if err := s.OnPrem.PutCompliance(ctx, key, body, retain); err != nil {
		return err
	}
	if err := s.Offsite.PutCompliance(ctx, key, body, retain); err != nil {
		return err
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE erp.backup_runs SET manifest_json=$2 WHERE id=$1`, m.BackupID, string(body)); err != nil {
		return err
	}
	off, err := s.Offsite.Get(ctx, key)
	if err != nil {
		return err
	}
	if fileSHA256(off) != fileSHA256(body) {
		return fmt.Errorf("%s: manifest", RefuseFileChecksum)
	}
	return nil
}

// MarkSuccess records the backup as successful only after the caller has verified the off-site copy (I15).
func (s *Service) MarkSuccess(ctx context.Context, m *Manifest) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE erp.backup_runs SET result='ok', offsite_verified=true, finished_at=clock_timestamp(), detail='' WHERE id=$1 AND manifest_json <> ''`, m.BackupID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("refusing to mark backup %s successful before the manifest is stored", m.BackupID)
	}
	return s.publishBackup(ctx, m, true, "")
}

// MarkFailed records a failed backup and alerts System Managers and Stakeholders (I8).
func (s *Service) MarkFailed(ctx context.Context, id, detail string) error {
	if _, err := s.Pool.Exec(ctx, `UPDATE erp.backup_runs SET result='failed', offsite_verified=false, finished_at=clock_timestamp(), detail=$2 WHERE id=$1`, id, detail); err != nil {
		return err
	}
	return s.publishBackup(ctx, &Manifest{BackupID: id, Chains: nil}, false, detail)
}

// RunBackup is the full engine: archives, off-site copy, verify, then manifest and success.
func (s *Service) RunBackup(ctx context.Context, id string) (*Manifest, error) {
	m, err := s.WriteArchives(ctx, id)
	if err != nil {
		_ = s.fail(ctx, id, err)
		return nil, err
	}
	if err := s.Replicate(ctx, m); err != nil {
		_ = s.fail(ctx, id, err)
		return m, err
	}
	if err := s.VerifyOffsite(ctx, m); err != nil {
		_ = s.fail(ctx, id, err)
		return m, err
	}
	if err := s.CommitManifest(ctx, m); err != nil {
		_ = s.fail(ctx, id, err)
		return m, err
	}
	if err := s.MarkSuccess(ctx, m); err != nil {
		return m, err
	}
	return m, nil
}

func (s *Service) fail(ctx context.Context, id string, cause error) error {
	return s.MarkFailed(ctx, id, cause.Error())
}

func (s *Service) publishBackup(ctx context.Context, m *Manifest, ok bool, detail string) error {
	if s.River == nil {
		return nil
	}
	company := "system"
	if len(m.Chains) > 0 {
		company = m.Chains[0].CompanyID
	}
	now := s.now()
	if ok {
		ev, err := newEvent(now, "backup.completed", "LOW", company, "backup", m.BackupID, "smarterp://backup/"+m.BackupID, nil, map[string]any{"offsite_verified": true})
		if err != nil {
			return err
		}
		return s.publish(ctx, ev, false)
	}
	ev, err := newEvent(now, "backup.failed", "HIGH", company, "backup", m.BackupID, "smarterp://backup/"+m.BackupID, []string{"acknowledge", "open"}, map[string]any{
		"detail":   detail,
		"audience": []string{"system_manager", "stakeholder"},
	})
	if err != nil {
		return err
	}
	return s.publish(ctx, ev, false)
}

func (s *Service) objectRetain() time.Duration {
	if s.ObjectRetain != 0 {
		return s.ObjectRetain
	}
	return complianceRetain
}

func (s *Service) snapshotCounts(ctx context.Context) (map[string]int64, error) {
	out := map[string]int64{}
	for _, table := range []string{"erp.audit_events", "erp.chain_head_log", "erp.anchors", "erp.verification_runs"} {
		var n int64
		if err := s.Pool.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s`, table)).Scan(&n); err != nil {
			return nil, err
		}
		out[table] = n
	}
	return out, nil
}

func (s *Service) snapshotLedger(ctx context.Context) (LedgerTotals, error) {
	totals := LedgerTotals{Debits: "0.00", Credits: "0.00", Currency: "AED"}
	var rel *string
	if err := s.Pool.QueryRow(ctx, `SELECT to_regclass('erp.journal_lines')::text`).Scan(&rel); err != nil {
		return totals, err
	}
	if rel == nil {
		return totals, nil
	}
	if err := s.Pool.QueryRow(ctx, `SELECT coalesce(sum(debit)::text, '0.00'), coalesce(sum(credit)::text, '0.00') FROM erp.journal_lines`).Scan(&totals.Debits, &totals.Credits); err != nil {
		return totals, err
	}
	return totals, nil
}

func (s *Service) snapshotChains(ctx context.Context) ([]ChainProof, error) {
	rows, err := s.Pool.Query(ctx, `SELECT company_id::text, chain_seq, head_hash FROM erp.chain_head ORDER BY company_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChainProof
	for rows.Next() {
		var c ChainProof
		if err := rows.Scan(&c.CompanyID, &c.ChainSeq, &c.HeadHash); err != nil {
			return nil, err
		}
		if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM erp.audit_events WHERE company_id=$1`, c.CompanyID).Scan(&c.RowCount); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if out == nil {
		out = []ChainProof{}
	}
	return out, rows.Err()
}

// Prune deletes local staging objects older than RetainFor only when the latest backup verified (I7).
// Off-site compliance objects that are still locked are left in place.
func (s *Service) Prune(ctx context.Context, now time.Time) (int, error) {
	var result string
	var verified bool
	var latest string
	err := s.Pool.QueryRow(ctx, `SELECT id, result, offsite_verified FROM erp.backup_runs ORDER BY coalesce(finished_at, started_at) DESC LIMIT 1`).Scan(&latest, &result, &verified)
	if err != nil {
		return 0, err
	}
	if result != "ok" || !verified {
		return 0, ErrRetentionBlocked
	}
	cutoff := now.Add(-s.retainWindow())
	rows, err := s.Pool.Query(ctx, `SELECT id, file_keys FROM erp.backup_runs WHERE result='ok' AND pruned_at IS NULL AND finished_at < $1 AND id <> $2`, cutoff, latest)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	type item struct{ id, keys string }
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.keys); err != nil {
			return 0, err
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, it := range items {
		var keys []string
		if err := json.Unmarshal([]byte(it.keys), &keys); err != nil {
			return n, err
		}
		for _, key := range keys {
			if err := s.OnPrem.Delete(ctx, key); err != nil && !isLockedOrMissing(err) {
				return n, err
			}
			if s.Offsite != nil {
				if err := s.Offsite.Delete(ctx, key); err != nil && !isLockedOrMissing(err) {
					return n, err
				}
			}
			n++
		}
		if _, err := s.Pool.Exec(ctx, `UPDATE erp.backup_runs SET pruned_at=$2 WHERE id=$1`, it.id, now); err != nil {
			return n, err
		}
	}
	return n, nil
}

func (s *Service) retainWindow() time.Duration {
	if s.RetainFor > 0 {
		return s.RetainFor
	}
	return 24 * time.Hour
}

func isLockedOrMissing(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, ErrImmutableObject) || errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "object not found")
}
