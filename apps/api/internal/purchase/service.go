package purchase

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jaichahal/smart-erp/apps/api/internal/approvals"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/audit"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// Service is the purchase API. Approvals owns the request, the hash, and the
// single-use posting token. This service decides who may approve.
type Service struct {
	Pool      *pgxpool.Pool
	Approvals *approvals.Service
	now       func() time.Time
}

// New returns a purchase service. The approvals service must share the same pool.
func New(pool *pgxpool.Pool, appr *approvals.Service) *Service {
	return &Service{Pool: pool, Approvals: appr, now: time.Now}
}

// SetClock sets the clock used for effective prices.
func (s *Service) SetClock(now func() time.Time) { s.now = now }

func (s *Service) clock() time.Time {
	if s.now == nil {
		return time.Now().UTC()
	}
	return s.now().UTC()
}

// EnsureActor writes the approvals directory row for a person.
func (s *Service) EnsureActor(ctx context.Context, p rls.Principal, a approvals.Actor) error {
	return s.Approvals.UpsertActor(ctx, p, a)
}

// PutTitle records that a Stakeholder is the CFO or the Partner.
// The first title bootstraps the list. After that, only a current CFO or Partner may change it.
func (s *Service) PutTitle(ctx context.Context, p rls.Principal, userID, title string) error {
	title = strings.ToLower(strings.TrimSpace(title))
	if title != "cfo" && title != "partner" {
		return apierr.New(apierr.ValidationError, msg("validation")).WithDetails(map[string]any{"field": "title"})
	}
	return rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM erp.purchase_titles WHERE company_id=$1`, p.CompanyID).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			ok, err := s.titled(ctx, tx, p.CompanyID, p.UserID)
			if err != nil {
				return err
			}
			if !ok {
				return apierr.New(apierr.PermissionDenied, msg("title.bootstrap"))
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO erp.purchase_titles(company_id, user_id, title) VALUES ($1,$2,$3)
			ON CONFLICT (company_id, user_id) DO UPDATE SET title=EXCLUDED.title`, p.CompanyID, userID, title); err != nil {
			return err
		}
		var roles []string
		err := tx.QueryRow(ctx, `SELECT roles FROM erp.approval_actors WHERE company_id=$1 AND user_id=$2`, p.CompanyID, userID).Scan(&roles)
		if err != nil {
			roles = []string{"stakeholder"}
		}
		roles = addRole(addRole(roles, "stakeholder"), title)
		if _, err := tx.Exec(ctx, `INSERT INTO erp.approval_actors(company_id, user_id, name, department, roles) VALUES ($1,$2,$3,'',$4)
			ON CONFLICT (company_id, user_id) DO UPDATE SET roles=EXCLUDED.roles`, p.CompanyID, userID, userID, roles); err != nil {
			return err
		}
		_, err = audit.EmitAs(ctx, tx, p, audit.Event{
			Type: "purchase.title_set", ReferenceType: "purchase_title", ReferenceID: userID,
			After: map[string]any{"title": title},
		})
		return err
	})
}

// PutSKU stores an item the purchase documents can name.
func (s *Service) PutSKU(ctx context.Context, p rls.Principal, id uuid.UUID, code, class string) error {
	switch class {
	case classRaw, classFinished, classBoth:
	default:
		return apierr.New(apierr.ValidationError, msg("validation")).WithDetails(map[string]any{"field": "item_class"})
	}
	return rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO erp.purchase_skus(company_id, sku_id, code, item_class) VALUES ($1,$2,$3,$4)
			ON CONFLICT (company_id, sku_id) DO UPDATE SET code=EXCLUDED.code, item_class=EXCLUDED.item_class`,
			p.CompanyID, id, code, class)
		return err
	})
}

// PrepareOnboarding stores a new-vendor file and opens the final-gate request.
func (s *Service) PrepareOnboarding(ctx context.Context, p rls.Principal, in OnboardingInput) (Gate, error) {
	if strings.TrimSpace(in.Name) == "" {
		return Gate{}, apierr.New(apierr.ValidationError, msg("validation")).WithDetails(map[string]any{"field": "name"})
	}
	skus := make([]any, len(in.SKUIDs))
	for i, id := range in.SKUIDs {
		skus[i] = id.String()
	}
	snap := map[string]any{
		"kind": kindOnboarding, "name": in.Name, "bank_account": in.BankAccount,
		"trade_licence_key": in.TradeLicenceKey, "sku_ids": skus,
	}
	return s.open(ctx, p, kindOnboarding, docOnboarding, uuid.Nil, in.Name, snap)
}

// PrepareSKUAdd asks to approve one more SKU for a vendor.
func (s *Service) PrepareSKUAdd(ctx context.Context, p rls.Principal, vendorID, skuID uuid.UUID) (Gate, error) {
	var bank, licence string
	err := rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT bank_account, trade_licence_key FROM erp.purchase_vendors WHERE company_id=$1 AND id=$2`,
			p.CompanyID, vendorID).Scan(&bank, &licence)
	})
	if err != nil {
		return Gate{}, apierr.New(apierr.NotFound, msg("not.found"))
	}
	snap := map[string]any{
		"kind": kindSKU, "vendor_id": vendorID.String(), "sku_id": skuID.String(),
		"bank_account": bank, "trade_licence_key": licence, "sku_ids": []any{skuID.String()},
	}
	return s.open(ctx, p, kindSKU, docSKU, vendorID, vendorID.String(), snap)
}

// PrepareBlacklist asks a CFO or Partner to blacklist a vendor.
func (s *Service) PrepareBlacklist(ctx context.Context, p rls.Principal, vendorID uuid.UUID, reason string) (Gate, error) {
	if strings.TrimSpace(reason) == "" {
		return Gate{}, apierr.New(apierr.ValidationError, msg("validation")).WithDetails(map[string]any{"field": "reason"})
	}
	snap := map[string]any{"kind": kindBlacklist, "vendor_id": vendorID.String(), "reason": reason}
	return s.open(ctx, p, kindBlacklist, docBlacklist, vendorID, vendorID.String(), snap)
}

// Approve decides a prepared file. Only a CFO or Partner may approve, and the caller cannot be the preparer.
func (s *Service) Approve(ctx context.Context, p rls.Principal, id uuid.UUID, stateVersion int, stepToken string) (*approvals.Outcome, error) {
	row, err := s.loadGate(ctx, p, id)
	if err != nil {
		return nil, err
	}
	if !finalKind(row.kind) {
		return nil, apierr.New(apierr.ValidationError, msg("validation"))
	}
	ok, err := s.hasTitle(ctx, p, p.UserID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, apierr.New(apierr.PermissionDenied, msg("title.required"))
	}
	if row.preparedBy == p.UserID {
		return nil, apierr.New(apierr.SoDViolation, msg("prepare.only"))
	}
	if row.kind == kindPayment {
		if err := s.Approvals.StepUp.Verify(ctx, p, stepToken, "payment_release"); err != nil {
			return nil, err
		}
	}
	return s.Approvals.Approve(ctx, p, approvals.DecisionInput{
		RequestID: row.approvalID, StateVersion: stateVersion, StepUpToken: stepToken, ClientState: "approved",
	})
}

// Delegate is refused for onboarding, a SKU addition, blacklist, and a blacklist payment release.
func (s *Service) Delegate(ctx context.Context, p rls.Principal, id uuid.UUID, toUser string, until time.Time, stateVersion int) error {
	row, err := s.loadGate(ctx, p, id)
	if err != nil {
		return err
	}
	if finalKind(row.kind) {
		return apierr.New(apierr.PermissionDenied, msg("not.delegable")).WithDetails(map[string]any{"kind": row.kind})
	}
	_, err = s.Approvals.Delegate(ctx, p, row.approvalID, toUser, until, stateVersion)
	return err
}

// Register consumes the posting token and writes the approved row.
// Client decision fields are ignored.
func (s *Service) Register(ctx context.Context, p rls.Principal, id uuid.UUID, token string, client map[string]any) (any, error) {
	_ = client
	row, err := s.loadGate(ctx, p, id)
	if err != nil {
		return nil, err
	}
	if err := s.gateHeld(ctx, p, row); err != nil {
		return nil, err
	}
	if err := s.Approvals.ConsumePostingToken(ctx, p, token, row.snap); err != nil {
		return nil, err
	}
	switch row.kind {
	case kindOnboarding:
		return s.insertVendor(ctx, p, row)
	case kindSKU:
		return s.insertSKU(ctx, p, row)
	case kindBlacklist:
		return s.applyBlacklist(ctx, p, row)
	case kindPayment:
		return s.insertRelease(ctx, p, row)
	default:
		return nil, apierr.New(apierr.ValidationError, msg("validation"))
	}
}

// ProposeBank opens the existing four-eyes bank-change task. It does not use the CFO gate.
func (s *Service) ProposeBank(ctx context.Context, p rls.Principal, vendorID uuid.UUID, account string) (Gate, error) {
	if strings.TrimSpace(account) == "" {
		return Gate{}, apierr.New(apierr.ValidationError, msg("validation")).WithDetails(map[string]any{"field": "bank_account"})
	}
	snap := map[string]any{"kind": kindBank, "vendor_id": vendorID.String(), "bank_account": account}
	return s.open(ctx, p, kindBank, docBank, vendorID, vendorID.String(), snap)
}

// ApproveBank requires someone other than the preparer and a step-up token.
func (s *Service) ApproveBank(ctx context.Context, p rls.Principal, id uuid.UUID, stateVersion int, stepToken string) error {
	row, err := s.loadGate(ctx, p, id)
	if err != nil {
		return err
	}
	if row.kind != kindBank {
		return apierr.New(apierr.ValidationError, msg("validation"))
	}
	out, err := s.Approvals.Approve(ctx, p, approvals.DecisionInput{
		RequestID: row.approvalID, StateVersion: stateVersion, StepUpToken: stepToken,
	})
	if err != nil {
		return err
	}
	if err := s.Approvals.ConsumePostingToken(ctx, p, out.PostingToken, row.snap); err != nil {
		return err
	}
	account, _ := row.snap["bank_account"].(string)
	return rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE erp.purchase_vendors SET bank_account=$3, bank_request_id=$4
			WHERE company_id=$1 AND id=$2`, p.CompanyID, row.vendorID, account, row.approvalID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return apierr.New(apierr.NotFound, msg("not.found"))
		}
		_, err = tx.Exec(ctx, `UPDATE erp.purchase_requests SET status='registered' WHERE id=$1`, id)
		return err
	})
}

// Review shows the trade licence, the bank account, the SKUs requested, and prior invoices.
func (s *Service) Review(ctx context.Context, p rls.Principal, id uuid.UUID) (Review, error) {
	row, err := s.loadGate(ctx, p, id)
	if err != nil {
		return Review{}, err
	}
	out := Review{
		RequestID:       id,
		TradeLicenceKey: str(row.snap["trade_licence_key"]),
		BankAccount:     str(row.snap["bank_account"]),
		SKUIDs:          strs(row.snap["sku_ids"]),
		PriorInvoices:   []InvoiceBrief{},
	}
	if row.vendorID == uuid.Nil {
		return out, nil
	}
	err = rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT d.id, d.number, d.status, d.supplier_number,
			EXISTS (SELECT 1 FROM erp.purchase_invoice_flags f WHERE f.invoice_id=d.id)
			FROM erp.purchase_docs d
			WHERE d.company_id=$1 AND d.vendor_id=$2 AND d.doc_type='supplier_invoice'
			ORDER BY d.created_at`, p.CompanyID, row.vendorID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b InvoiceBrief
			if err := rows.Scan(&b.ID, &b.Number, &b.Status, &b.SupplierNumber, &b.Flagged); err != nil {
				return err
			}
			out.PriorInvoices = append(out.PriorInvoices, b)
		}
		return rows.Err()
	})
	return out, err
}

// RefuseDirectVendor reports that there is no endpoint that inserts an approved vendor by itself.
func (s *Service) RefuseDirectVendor(ctx context.Context, p rls.Principal) error {
	_ = ctx
	_ = p
	return apierr.New(apierr.PermissionDenied, msg("vendor.skip"))
}

func (s *Service) open(ctx context.Context, p rls.Principal, kind, docType string, vendorID uuid.UUID, party string, snap map[string]any) (Gate, error) {
	if err := s.ensureMatrix(ctx, p); err != nil {
		return Gate{}, err
	}
	id := uuid.New()
	out, err := s.Approvals.Submit(ctx, p, approvals.SubmitInput{
		DocID: id.String(), DocType: docType, Party: party, Amount: "1", Currency: "AED", Snapshot: snap,
		ClientState: "approved",
	})
	if err != nil {
		return Gate{}, err
	}
	var storedVendor any
	if vendorID != uuid.Nil {
		storedVendor = vendorID
	}
	err = rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		raw, err := json.Marshal(snap)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO erp.purchase_requests(id, company_id, kind, vendor_id, snapshot, approval_request_id, prepared_by, status)
			VALUES ($1,$2,$3,$4,$5,$6,$7,'pending')`,
			id, p.CompanyID, kind, storedVendor, raw, out.Decision.RequestId, p.UserID)
		return err
	})
	if err != nil {
		return Gate{}, err
	}
	return Gate{ID: id, ApprovalID: out.Decision.RequestId, StateVersion: out.Decision.StateVersion, Kind: kind}, nil
}

func (s *Service) ensureMatrix(ctx context.Context, p rls.Principal) error {
	final := approvals.Matrix{
		ThresholdAmount: "1000000000", Currency: "AED", AboveMode: "first_then_final",
		BelowRoles: []string{"cfo", "partner"}, FirstRoles: []string{"cfo", "partner"}, FinalRole: "cfo",
	}
	for _, doc := range []string{docOnboarding, docSKU, docBlacklist, docPayRelease} {
		final.DocType = doc
		if err := s.Approvals.PutMatrix(ctx, p, final); err != nil {
			return err
		}
	}
	return s.Approvals.PutMatrix(ctx, p, approvals.Matrix{
		DocType: docBank, ThresholdAmount: "1000000000", Currency: "AED", AboveMode: "first_then_final",
		BelowRoles: []string{"approver", "stakeholder", "cfo", "partner"},
		FirstRoles: []string{"approver", "stakeholder"}, FinalRole: "stakeholder",
	})
}

type gateRow struct {
	id, vendorID     uuid.UUID
	kind, approvalID string
	preparedBy       string
	snap             map[string]any
}

func (s *Service) loadGate(ctx context.Context, p rls.Principal, id uuid.UUID) (gateRow, error) {
	var row gateRow
	var raw []byte
	var vendor *uuid.UUID
	err := rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id, kind, vendor_id, snapshot, approval_request_id, prepared_by
			FROM erp.purchase_requests WHERE company_id=$1 AND id=$2`, p.CompanyID, id).
			Scan(&row.id, &row.kind, &vendor, &raw, &row.approvalID, &row.preparedBy)
	})
	if err != nil {
		return gateRow{}, apierr.New(apierr.NotFound, msg("not.found"))
	}
	if vendor != nil {
		row.vendorID = *vendor
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&row.snap); err != nil {
		return gateRow{}, err
	}
	return row, nil
}

func (s *Service) gateHeld(ctx context.Context, p rls.Principal, row gateRow) error {
	if !finalKind(row.kind) {
		return apierr.New(apierr.ValidationError, msg("validation"))
	}
	var delegated int
	var actors []string
	err := rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM erp.approval_decisions WHERE company_id=$1 AND request_id=$2 AND decision='delegated'`,
			p.CompanyID, row.approvalID).Scan(&delegated); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT actor_id FROM erp.approval_decisions WHERE company_id=$1 AND request_id=$2 AND decision='approved'`,
			p.CompanyID, row.approvalID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			actors = append(actors, id)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	if delegated > 0 || len(actors) == 0 {
		return apierr.New(apierr.PermissionDenied, msg("not.delegable"))
	}
	for _, id := range actors {
		ok, err := s.hasTitle(ctx, p, id)
		if err != nil {
			return err
		}
		if !ok {
			return apierr.New(apierr.PermissionDenied, msg("title.required"))
		}
	}
	return nil
}

func (s *Service) insertVendor(ctx context.Context, p rls.Principal, row gateRow) (Vendor, error) {
	id := uuid.New()
	name := str(row.snap["name"])
	bank := str(row.snap["bank_account"])
	licence := str(row.snap["trade_licence_key"])
	v := Vendor{ID: id, Name: name, BankAccount: bank, TradeLicenceKey: licence, PreparedBy: row.preparedBy, ApprovalRequest: row.approvalID}
	err := rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO erp.purchase_vendors(id, company_id, name, status, bank_account, trade_licence_key, prepared_by, approval_request_id)
			VALUES ($1,$2,$3,'approved',$4,$5,$6,$7)`, id, p.CompanyID, name, bank, licence, row.preparedBy, row.approvalID); err != nil {
			return err
		}
		for _, sku := range strs(row.snap["sku_ids"]) {
			sid, err := uuid.Parse(sku)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO erp.purchase_vendor_skus(company_id, vendor_id, sku_id, approval_request_id) VALUES ($1,$2,$3,$4)`,
				p.CompanyID, id, sid, row.approvalID); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `UPDATE erp.purchase_requests SET status='registered', vendor_id=$2 WHERE id=$1`, row.id, id)
		return err
	})
	return v, err
}

func (s *Service) insertSKU(ctx context.Context, p rls.Principal, row gateRow) (Vendor, error) {
	sku, err := uuid.Parse(str(row.snap["sku_id"]))
	if err != nil {
		return Vendor{}, apierr.New(apierr.ValidationError, msg("validation"))
	}
	err = rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO erp.purchase_vendor_skus(company_id, vendor_id, sku_id, approval_request_id) VALUES ($1,$2,$3,$4)`,
			p.CompanyID, row.vendorID, sku, row.approvalID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE erp.purchase_requests SET status='registered' WHERE id=$1`, row.id)
		return err
	})
	if err != nil {
		return Vendor{}, err
	}
	return s.GetVendor(ctx, p, row.vendorID)
}

func (s *Service) applyBlacklist(ctx context.Context, p rls.Principal, row gateRow) (Vendor, error) {
	err := rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE erp.purchase_vendors SET blacklisted=true, blacklist_request_id=$3 WHERE company_id=$1 AND id=$2`,
			p.CompanyID, row.vendorID, row.approvalID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO erp.purchase_invoice_flags(id, company_id, invoice_id, vendor_id, reason)
			SELECT gen_random_uuid(), company_id, id, vendor_id, 'blacklisted'
			FROM erp.purchase_docs
			WHERE company_id=$1 AND vendor_id=$2 AND doc_type='supplier_invoice'
			ON CONFLICT DO NOTHING`, p.CompanyID, row.vendorID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE erp.purchase_requests SET status='registered' WHERE id=$1`, row.id)
		return err
	})
	if err != nil {
		return Vendor{}, err
	}
	return s.GetVendor(ctx, p, row.vendorID)
}

func (s *Service) insertRelease(ctx context.Context, p rls.Principal, row gateRow) (uuid.UUID, error) {
	pay, err := uuid.Parse(str(row.snap["payment_id"]))
	if err != nil {
		return uuid.Nil, apierr.New(apierr.ValidationError, msg("validation"))
	}
	err = rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO erp.purchase_payment_releases(payment_id, company_id, vendor_id, released_by, request_id)
			VALUES ($1,$2,$3,$4,$5)`, pay, p.CompanyID, row.vendorID, p.UserID, row.approvalID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE erp.purchase_requests SET status='registered' WHERE id=$1`, row.id)
		return err
	})
	return pay, err
}

// GetVendor reads one vendor. Another company sees nothing.
func (s *Service) GetVendor(ctx context.Context, p rls.Principal, id uuid.UUID) (Vendor, error) {
	var v Vendor
	err := rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id, name, bank_account, trade_licence_key, blacklisted, prepared_by, approval_request_id
			FROM erp.purchase_vendors WHERE company_id=$1 AND id=$2`, p.CompanyID, id).
			Scan(&v.ID, &v.Name, &v.BankAccount, &v.TradeLicenceKey, &v.Blacklisted, &v.PreparedBy, &v.ApprovalRequest)
	})
	if err != nil {
		return Vendor{}, apierr.New(apierr.NotFound, msg("not.found"))
	}
	return v, nil
}

func (s *Service) hasTitle(ctx context.Context, p rls.Principal, userID string) (bool, error) {
	var ok bool
	err := rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		var err error
		ok, err = s.titled(ctx, tx, p.CompanyID, userID)
		return err
	})
	return ok, err
}

func (s *Service) titled(ctx context.Context, tx pgx.Tx, company uuid.UUID, userID string) (bool, error) {
	var n int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM erp.purchase_titles WHERE company_id=$1 AND user_id=$2 AND title IN ('cfo','partner')`,
		company, userID).Scan(&n)
	return n > 0, err
}

func finalKind(kind string) bool {
	switch kind {
	case kindOnboarding, kindSKU, kindBlacklist, kindPayment:
		return true
	default:
		return false
	}
}

func addRole(roles []string, role string) []string {
	for _, r := range roles {
		if strings.EqualFold(r, role) {
			return roles
		}
	}
	out := append([]string{}, roles...)
	return append(out, role)
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func strs(v any) []string {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return t
	default:
		return []string{}
	}
}
