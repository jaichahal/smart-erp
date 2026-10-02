package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	kitaudit "github.com/jaichahal/smart-erp/apps/api/internal/kit/audit"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// RestoreResult is the outcome of a restore attempt. WritesEnabled is true only
// after Stakeholder approval, integrity checks, and an anchor match (R3.11, I6).
type RestoreResult struct {
	WritesEnabled bool
	RefusalCode   string
	Detail        string
}

// RestoreOpts selects a backup and the approval token that authorises it.
type RestoreOpts struct {
	BackupID      string
	Target        string
	ApprovalToken string
	Force         bool
	Actor         rls.Principal
}

// ApproveRestore records a Stakeholder approval for one backup. Other roles are refused.
func (s *Service) ApproveRestore(ctx context.Context, p rls.Principal, backupID string) (uuid.UUID, error) {
	if !hasRole(p, "stakeholder") {
		return uuid.Nil, fmt.Errorf("%s", Text(s.lang(), "restore.approval_required"))
	}
	token := uuid.New()
	if err := rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO erp.restore_approvals(token, backup_id, approver_id, approver_role) VALUES ($1,$2,$3,'stakeholder')`,
			token, backupID, p.UserID)
		return err
	}); err != nil {
		return uuid.Nil, err
	}
	return token, nil
}

// Restore checks the manifest and files, then re-derives counts and the chain.
func (s *Service) Restore(ctx context.Context, opts RestoreOpts) (RestoreResult, error) {
	store := s.Offsite
	if opts.Target == "local" || opts.Target == "onprem" {
		store = s.OnPrem
	}
	res := RestoreResult{}
	body, err := store.Get(ctx, manifestObjectKey(opts.BackupID))
	if err != nil {
		res.RefusalCode = RefuseManifestMissing
		res.Detail = Text(s.lang(), "restore.refused", RefuseManifestMissing)
		s.logRestore(ctx, opts, res)
		return res, nil
	}
	var m Manifest
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&m); err != nil || m.BackupID == "" || m.Schema == "" {
		res.RefusalCode = RefuseManifestIncomplete
		res.Detail = Text(s.lang(), "restore.refused", RefuseManifestIncomplete)
		s.logRestore(ctx, opts, res)
		return res, nil
	}
	if code := s.integrityCodes(ctx, store, &m, opts.Force); code != "" {
		res.RefusalCode = code
		res.Detail = Text(s.lang(), "restore.refused", code)
		s.logRestore(ctx, opts, res)
		return res, nil
	}
	if err := s.checkApproval(ctx, opts); err != nil {
		res.RefusalCode = RefuseApprovalRequired
		res.Detail = Text(s.lang(), "restore.approval_required")
		s.logRestore(ctx, opts, res)
		return res, nil
	}
	if code, err := s.rederive(ctx, &m); err != nil {
		return res, err
	} else if code != "" {
		res.RefusalCode = code
		res.Detail = Text(s.lang(), "restore.refused", code)
		s.notifyBreak(ctx, opts, code)
		s.logRestore(ctx, opts, res)
		return res, nil
	}
	res.WritesEnabled = true
	res.Detail = "ok"
	s.logRestore(ctx, opts, res)
	return res, nil
}

func (s *Service) integrityCodes(ctx context.Context, store ObjectStore, m *Manifest, force bool) string {
	var codes []string
	if err := verifyManifestFiles(ctx, store, m); err != nil {
		msg := err.Error()
		if strings.HasPrefix(msg, RefuseAggregateChecksum) {
			codes = append(codes, RefuseAggregateChecksum)
		} else {
			codes = append(codes, RefuseFileChecksum)
		}
	}
	if m.Site != s.Site {
		codes = append(codes, RefuseSiteMismatch)
	}
	if force {
		filtered := codes[:0]
		for _, c := range codes {
			if c != RefuseSiteMismatch {
				filtered = append(filtered, c)
			}
		}
		codes = filtered
	}
	if len(codes) == 0 {
		return ""
	}
	return codes[0]
}

func (s *Service) checkApproval(ctx context.Context, opts RestoreOpts) error {
	if opts.ApprovalToken == "" {
		return errors.New(RefuseApprovalRequired)
	}
	token, err := uuid.Parse(opts.ApprovalToken)
	if err != nil {
		return errors.New(RefuseApprovalRequired)
	}
	var role, backupID string
	err = s.Pool.QueryRow(ctx, `SELECT approver_role, backup_id FROM erp.restore_approvals WHERE token=$1`, token).Scan(&role, &backupID)
	if err != nil || role != "stakeholder" || backupID != opts.BackupID {
		return errors.New(RefuseApprovalRequired)
	}
	return nil
}

func (s *Service) rederive(ctx context.Context, m *Manifest) (string, error) {
	counts, err := s.snapshotCounts(ctx)
	if err != nil {
		return "", err
	}
	for table, want := range m.Counts {
		if counts[table] != want {
			return RefuseCountMismatch, nil
		}
	}
	ledger, err := s.snapshotLedger(ctx)
	if err != nil {
		return "", err
	}
	if ledger.Debits != m.Ledger.Debits || ledger.Credits != m.Ledger.Credits {
		return RefuseLedgerMismatch, nil
	}
	for _, chain := range m.Chains {
		company, err := uuid.Parse(chain.CompanyID)
		if err != nil {
			return RefuseChainBreak, nil
		}
		kit, err := kitaudit.Verify(ctx, s.Pool, company)
		if err != nil {
			return "", err
		}
		if !kit.Intact {
			return RefuseChainBreak, nil
		}
		got, err := s.HeadAt(ctx, company, chain.ChainSeq)
		if err != nil || got != chain.HeadHash {
			return RefuseChainBreak, nil
		}
		var anchored string
		err = s.Pool.QueryRow(ctx, `SELECT head_hash FROM erp.anchors WHERE company_id=$1 AND chain_seq=$2 AND anchored_at <= $3`,
			company, chain.ChainSeq, m.CreatedAt).Scan(&anchored)
		if err != nil || anchored != chain.HeadHash {
			return RefuseAnchorMismatch, nil
		}
	}
	return "", nil
}

func (s *Service) notifyBreak(ctx context.Context, opts RestoreOpts, code string) {
	company := opts.Actor.CompanyID.String()
	if company == uuid.Nil.String() || company == "" {
		company = "system"
	}
	ev, err := newEvent(s.now(), "chain.broken", "CRITICAL", company, "backup", opts.BackupID, "smarterp://backup/"+opts.BackupID,
		[]string{"acknowledge", "open"}, map[string]any{
			"reason":                 code,
			"quiet_hours_suppressed": false,
			"audience":               []string{"stakeholder", "system_manager"},
		})
	if err != nil || s.River == nil {
		return
	}
	_ = s.publish(ctx, ev, true)
}

func (s *Service) logRestore(ctx context.Context, opts RestoreOpts, res RestoreResult) {
	token := opts.ApprovalToken
	result := "refused"
	if res.WritesEnabled {
		result = "ok"
	}
	_, _ = s.Pool.Exec(ctx, `INSERT INTO erp.restore_log(backup_id, approval_token, approved_by, writes_enabled, result, refusal_code, detail)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		opts.BackupID, token, opts.Actor.UserID, res.WritesEnabled, result, res.RefusalCode, res.Detail)
	if opts.Actor.CompanyID == uuid.Nil {
		return
	}
	evType := "restore.refused"
	if res.WritesEnabled {
		evType = "restore.applied"
	}
	_ = rls.Tx(ctx, s.Pool, opts.Actor, func(tx pgx.Tx) error {
		_, err := kitaudit.EmitAs(ctx, tx, opts.Actor, kitaudit.Event{
			Type: evType, ReferenceType: "backup", ReferenceID: opts.BackupID, Reason: res.RefusalCode,
		})
		return err
	})
}

// ServingWrites reports whether the latest restore left the environment open for writes (I6).
// An environment that has never been restored is open.
func (s *Service) ServingWrites(ctx context.Context) (bool, error) {
	var enabled bool
	err := s.Pool.QueryRow(ctx, `SELECT writes_enabled FROM erp.restore_log ORDER BY occurred_at DESC, id DESC LIMIT 1`).Scan(&enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return enabled, nil
}

func hasRole(p rls.Principal, role string) bool {
	for _, r := range p.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// LoadManifest reads a manifest from the chosen target.
func (s *Service) LoadManifest(ctx context.Context, target, id string) (*Manifest, error) {
	store := s.Offsite
	if target == "local" || target == "onprem" {
		store = s.OnPrem
	}
	body, err := store.Get(ctx, manifestObjectKey(id))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// PutManifest stores a manifest using the service retention window.
func (s *Service) PutManifest(ctx context.Context, target string, m *Manifest) error {
	store := s.Offsite
	if target == "local" || target == "onprem" {
		store = s.OnPrem
	}
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return store.PutCompliance(ctx, manifestObjectKey(m.BackupID), body, s.now().Add(s.objectRetain()))
}
