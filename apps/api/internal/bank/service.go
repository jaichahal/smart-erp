package bank

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/audit"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/ifmatch"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// Service is statement matching, petty cash, and the daily cash position.
// It does not allocate receipts to invoices.
type Service struct {
	pool *pgxpool.Pool
}

// New returns a service bound to the application pool.
func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

func (s *Service) tx(ctx context.Context, p rls.Principal, fn func(pgx.Tx) error) error {
	ctx = rls.WithPrincipal(ctx, p)
	err := rls.Tx(ctx, s.pool, p, fn)
	return asAPI(err)
}

func asAPI(err error) error {
	if err == nil {
		return nil
	}
	var ae *apierr.Error
	if errors.As(err, &ae) {
		return ae
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return apierr.New(apierr.Conflict, "That record already exists.")
	}
	return apierr.Wrap(apierr.Internal, "The request could not be completed.", err)
}

// AccountInput opens a bank, cash, or petty account and posts the opening balance when it is not zero.
type AccountInput struct {
	Name        string
	Kind        string
	Opening     int64
	OpeningDate time.Time
	CustodianID string
	Float       int64
}

// AccountRef is a stored account.
type AccountRef struct {
	ID string `json:"id"`
}

// AccountView is the cash-book balance of one account.
type AccountView struct {
	ID        string `json:"id"`
	Balance   Money  `json:"balance"`
	LineCount int    `json:"line_count"`
}

// OpenAccount stores the account. A zero opening posts nothing.
func (s *Service) OpenAccount(ctx context.Context, p rls.Principal, in AccountInput) (AccountRef, error) {
	if in.Name == "" || (in.Kind != "bank" && in.Kind != "cash" && in.Kind != "petty") {
		return AccountRef{}, apierr.New(apierr.ValidationError, "Account name and kind are required.")
	}
	id := uuid.New()
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO erp.bank_accounts (company_id, id, name, kind, custodian_id, float_fils)
			VALUES ($1, $2, $3, $4, $5, $6)`, p.CompanyID, id, in.Name, in.Kind, in.CustodianID, in.Float)
		if err != nil || in.Opening == 0 {
			return err
		}
		role := in.Kind
		if in.Kind == "petty" {
			role = "petty_cash"
		}
		acct := id
		_, err = insertBooks(ctx, tx, p.CompanyID, []bookLine{
			{role: role, account: &acct, debit: in.Opening, docType: "opening", docID: id.String(), postedOn: in.OpeningDate},
			{role: "opening", credit: in.Opening, docType: "opening", docID: id.String(), postedOn: in.OpeningDate},
		})
		return err
	})
	return AccountRef{ID: id.String()}, err
}

// Account returns the balance and the number of book lines on the account.
func (s *Service) Account(ctx context.Context, p rls.Principal, id uuid.UUID) (AccountView, error) {
	var out AccountView
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		bal, count, err := accountBalance(ctx, tx, p.CompanyID, id)
		if err != nil {
			return err
		}
		out = AccountView{ID: id.String(), Balance: AED(bal), LineCount: count}
		return nil
	})
	return out, err
}

// ImportResult counts lines brought in and lines skipped because the bank reference was already stored.
type ImportResult struct {
	Imported          int `json:"imported"`
	SkippedDuplicates int `json:"skipped_duplicates"`
	LineCount         int `json:"line_count"`
}

// ImportStatement loads CSV or MT940 lines and skips a repeated bank reference.
func (s *Service) ImportStatement(ctx context.Context, p rls.Principal, account uuid.UUID, format, body string) (ImportResult, error) {
	parsed, err := parseStatement(format, body)
	if err != nil {
		return ImportResult{}, err
	}
	return s.storeLines(ctx, p, account, parsed)
}

// GiveConsent records an open-finance consent and its expiry.
func (s *Service) GiveConsent(ctx context.Context, p rls.Principal, account uuid.UUID, expires time.Time, provider string) error {
	return s.tx(ctx, p, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO erp.bank_feed (company_id, account_id, provider, consent_expires, status, alert)
			VALUES ($1, $2, $3, $4, 'ok', false)
			ON CONFLICT (company_id, account_id) DO UPDATE
			SET provider = EXCLUDED.provider, consent_expires = EXCLUDED.consent_expires, status = 'ok', alert = false`,
			p.CompanyID, account, provider, expires)
		return err
	})
}

// RunFeed imports a nightly file only while the consent is in force.
func (s *Service) RunFeed(ctx context.Context, p rls.Principal, account uuid.UUID, format, body string, asOf time.Time) (ImportResult, error) {
	var expired bool
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		var expires *time.Time
		err := tx.QueryRow(ctx, `SELECT consent_expires FROM erp.bank_feed WHERE company_id = $1 AND account_id = $2`, p.CompanyID, account).Scan(&expires)
		if errors.Is(err, pgx.ErrNoRows) || expires == nil || asOf.After(*expires) {
			expired = true
			return nil
		}
		return err
	})
	if err != nil {
		return ImportResult{}, err
	}
	if expired {
		return ImportResult{}, apierr.New(apierr.Conflict, "Open-finance consent has expired.")
	}
	parsed, err := parseStatement(format, body)
	if err != nil {
		return ImportResult{}, err
	}
	return s.storeLines(ctx, p, account, parsed)
}

// FailFeed records a feed failure so the status page can show the alert.
func (s *Service) FailFeed(ctx context.Context, p rls.Principal, account uuid.UUID, message string) error {
	if message == "" {
		return apierr.New(apierr.ValidationError, "A feed failure needs a message.")
	}
	return s.tx(ctx, p, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO erp.bank_feed (company_id, account_id, status, message, alert)
			VALUES ($1, $2, 'failed', $3, true)
			ON CONFLICT (company_id, account_id) DO UPDATE
			SET status = 'failed', message = EXCLUDED.message, alert = true`, p.CompanyID, account, message)
		return err
	})
}

// FeedStatus is the bank feed row for one account.
type FeedStatus struct {
	ConsentExpires string `json:"consent_expires,omitempty"`
	Status         string `json:"status"`
	Alert          bool   `json:"alert"`
	Message        string `json:"message,omitempty"`
}

// Feed returns consent expiry and the last failure.
func (s *Service) Feed(ctx context.Context, p rls.Principal, account uuid.UUID) (FeedStatus, error) {
	var out FeedStatus
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		var expires *time.Time
		err := tx.QueryRow(ctx, `
			SELECT consent_expires, status, alert, message FROM erp.bank_feed
			WHERE company_id = $1 AND account_id = $2`, p.CompanyID, account).Scan(&expires, &out.Status, &out.Alert, &out.Message)
		if errors.Is(err, pgx.ErrNoRows) {
			out.Status = "ok"
			return nil
		}
		if err != nil {
			return err
		}
		if expires != nil {
			out.ConsentExpires = expires.Format("2006-01-02")
		}
		return nil
	})
	return out, err
}

func (s *Service) storeLines(ctx context.Context, p rls.Principal, account uuid.UUID, lines []importedLine) (ImportResult, error) {
	var out ImportResult
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		for _, ln := range lines {
			tag, err := tx.Exec(ctx, `
				INSERT INTO erp.bank_statement_lines (
					company_id, id, account_id, bank_reference, amount_fils, booked_on, description, status
				) VALUES ($1, $2, $3, $4, $5, $6, $7, 'open')
				ON CONFLICT (company_id, account_id, bank_reference) DO NOTHING`,
				p.CompanyID, uuid.New(), account, ln.reference, ln.amount, ln.bookedOn, ln.text)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 1 {
				out.Imported++
			} else {
				out.SkippedDuplicates++
			}
		}
		return tx.QueryRow(ctx, `SELECT COUNT(*) FROM erp.bank_statement_lines WHERE company_id = $1 AND account_id = $2`, p.CompanyID, account).Scan(&out.LineCount)
	})
	return out, err
}

// SaveRule stores an auto-match rule for an account.
func (s *Service) SaveRule(ctx context.Context, p rls.Principal, account uuid.UUID, kind string) error {
	if kind != "amount_and_date" {
		return apierr.New(apierr.ValidationError, "Match rule is not supported.")
	}
	return s.tx(ctx, p, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO erp.bank_match_rules (company_id, account_id, kind) VALUES ($1, $2, $3)
			ON CONFLICT (company_id, account_id) DO UPDATE SET kind = EXCLUDED.kind`, p.CompanyID, account, kind)
		return err
	})
}

// AutoMatch proposes matches for open lines. A proposal stays visible until it is matched or explained.
func (s *Service) AutoMatch(ctx context.Context, p rls.Principal, account uuid.UUID) error {
	return s.tx(ctx, p, func(tx pgx.Tx) error {
		var kind string
		err := tx.QueryRow(ctx, `SELECT kind FROM erp.bank_match_rules WHERE company_id = $1 AND account_id = $2`, p.CompanyID, account).Scan(&kind)
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.New(apierr.ValidationError, "No match rule is configured.")
		}
		if err != nil {
			return err
		}
		q, err := tx.Query(ctx, `
			SELECT id, amount_fils, booked_on FROM erp.bank_statement_lines
			WHERE company_id = $1 AND account_id = $2 AND status = 'open'`, p.CompanyID, account)
		if err != nil {
			return err
		}
		defer q.Close()
		type openLine struct {
			id     uuid.UUID
			amount int64
			day    time.Time
		}
		var open []openLine
		for q.Next() {
			var ln openLine
			if err := q.Scan(&ln.id, &ln.amount, &ln.day); err != nil {
				return err
			}
			open = append(open, ln)
		}
		if err := q.Err(); err != nil {
			return err
		}
		for _, ln := range open {
			var book uuid.UUID
			err := tx.QueryRow(ctx, `
				SELECT b.id FROM erp.book_lines b
				WHERE b.company_id = $1 AND b.account_id = $2 AND b.posted_on = $3
				  AND (b.debit_fils - b.credit_fils) = $4
				  AND NOT EXISTS (
				    SELECT 1 FROM erp.bank_statement_lines s
				    WHERE s.company_id = b.company_id
				      AND (s.matched_book_line_id = b.id OR s.proposed_book_line_id = b.id)
				      AND s.status IN ('matched', 'proposed')
				  )
				ORDER BY b.id LIMIT 1`, p.CompanyID, account, ln.day, ln.amount).Scan(&book)
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				UPDATE erp.bank_statement_lines SET status = 'proposed', proposed_book_line_id = $3
				WHERE company_id = $1 AND id = $2`, p.CompanyID, ln.id, book); err != nil {
				return err
			}
		}
		return nil
	})
}

// Match links a statement line to an existing book line. It does not post a new bank movement.
func (s *Service) Match(ctx context.Context, p rls.Principal, lineID, bookID uuid.UUID) error {
	return s.tx(ctx, p, func(tx pgx.Tx) error {
		var status string
		err := tx.QueryRow(ctx, `SELECT status FROM erp.bank_statement_lines WHERE company_id = $1 AND id = $2 FOR UPDATE`, p.CompanyID, lineID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.New(apierr.NotFound, "Statement line not found.")
		}
		if err != nil {
			return err
		}
		if status == "matched" {
			return apierr.New(apierr.Conflict, "The statement line is already matched.")
		}
		tag, err := tx.Exec(ctx, `UPDATE erp.bank_statement_lines SET status = 'matched', matched_book_line_id = $3 WHERE company_id = $1 AND id = $2`, p.CompanyID, lineID, bookID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return apierr.New(apierr.NotFound, "Statement line not found.")
		}
		return nil
	})
}

// Unmatch clears a match. It requires a reason and writes an audit event.
func (s *Service) Unmatch(ctx context.Context, p rls.Principal, lineID uuid.UUID, reason string) error {
	if reason == "" {
		return apierr.New(apierr.ValidationError, "Unmatch requires a reason.")
	}
	return s.tx(ctx, p, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE erp.bank_statement_lines
			SET status = 'open', matched_book_line_id = NULL, proposed_book_line_id = NULL
			WHERE company_id = $1 AND id = $2 AND status = 'matched'`, p.CompanyID, lineID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return apierr.New(apierr.Conflict, "Only a matched line can be unmatched.")
		}
		_, err = audit.Emit(ctx, tx, audit.Event{
			Type: "bank.line_unmatched", ReferenceType: "bank_statement_line", ReferenceID: lineID.String(),
			Reason: reason, After: map[string]any{"request_id": apierr.RequestID(ctx), "line_id": lineID.String()},
		})
		return err
	})
}

// Explain clears an unmatched line without pretending it matches the book.
func (s *Service) Explain(ctx context.Context, p rls.Principal, lineID uuid.UUID, reason string) error {
	if reason == "" {
		return apierr.New(apierr.ValidationError, "An explanation requires a reason.")
	}
	return s.tx(ctx, p, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE erp.bank_statement_lines SET status = 'explained', explain_reason = $3
			WHERE company_id = $1 AND id = $2 AND status IN ('open', 'proposed')`, p.CompanyID, lineID, reason)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return apierr.New(apierr.Conflict, "The line is not waiting to be explained.")
		}
		return nil
	})
}

// StatementView is one imported line.
type StatementView struct {
	ID        string `json:"id"`
	Reference string `json:"reference"`
	Status    string `json:"status"`
	Amount    Money  `json:"amount"`
}

// Reconciliation is the book against the statement at a date.
type Reconciliation struct {
	Difference     Money           `json:"difference"`
	Status         string          `json:"status"`
	BlocksMonthEnd bool            `json:"blocks_month_end"`
	Lines          []StatementView `json:"lines"`
}

// Reconcile compares the statement total with the account book and reports unmatched lines.
func (s *Service) Reconcile(ctx context.Context, p rls.Principal, account uuid.UUID, asOf time.Time) (Reconciliation, error) {
	var out Reconciliation
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		var book int64
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(SUM(debit_fils - credit_fils), 0) FROM erp.book_lines
			WHERE company_id = $1 AND account_id = $2 AND posted_on <= $3`, p.CompanyID, account, asOf).Scan(&book); err != nil {
			return err
		}
		var stmt int64
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(SUM(amount_fils), 0) FROM erp.bank_statement_lines
			WHERE company_id = $1 AND account_id = $2 AND booked_on <= $3`, p.CompanyID, account, asOf).Scan(&stmt); err != nil {
			return err
		}
		q, err := tx.Query(ctx, `
			SELECT id, bank_reference, status, amount_fils FROM erp.bank_statement_lines
			WHERE company_id = $1 AND account_id = $2 ORDER BY booked_on, bank_reference`, p.CompanyID, account)
		if err != nil {
			return err
		}
		defer q.Close()
		blocked := false
		for q.Next() {
			var id uuid.UUID
			var ref, status string
			var amount int64
			if err := q.Scan(&id, &ref, &status, &amount); err != nil {
				return err
			}
			if status == "open" || status == "proposed" {
				blocked = true
			}
			out.Lines = append(out.Lines, StatementView{ID: id.String(), Reference: ref, Status: status, Amount: AED(amount)})
		}
		if err := q.Err(); err != nil {
			return err
		}
		diff := stmt - book
		out.Difference = AED(diff)
		out.BlocksMonthEnd = blocked
		out.Status = "open"
		if diff == 0 && !blocked {
			out.Status = "reconciled"
		}
		return nil
	})
	if out.Lines == nil {
		out.Lines = []StatementView{}
	}
	return out, err
}

// VoucherInput is a petty cash expense. It cannot spend more than the cash still in the float.
type VoucherInput struct {
	AccountID uuid.UUID
	Expense   int64
	VAT       int64
	PostedOn  time.Time
}

// VoucherResult is the posting.
type VoucherResult struct {
	ID       string    `json:"id"`
	Postings []Posting `json:"postings"`
}

// PostVoucher debits expense and VAT and credits petty cash.
func (s *Service) PostVoucher(ctx context.Context, p rls.Principal, in VoucherInput) (VoucherResult, error) {
	total := in.Expense + in.VAT
	if in.Expense < 0 || in.VAT < 0 || total <= 0 {
		return VoucherResult{}, apierr.New(apierr.ValidationError, "Voucher amount is required.")
	}
	var out VoucherResult
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		var kind string
		var floatAmt int64
		err := tx.QueryRow(ctx, `SELECT kind, float_fils FROM erp.bank_accounts WHERE company_id = $1 AND id = $2`, p.CompanyID, in.AccountID).Scan(&kind, &floatAmt)
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.New(apierr.NotFound, "Account not found.")
		}
		if err != nil {
			return err
		}
		if kind != "petty" {
			return apierr.New(apierr.ValidationError, "Vouchers post to the petty cash account.")
		}
		bal, _, err := accountBalance(ctx, tx, p.CompanyID, in.AccountID)
		if err != nil {
			return err
		}
		if total > bal || (floatAmt > 0 && total > floatAmt) {
			return apierr.New(apierr.ValidationError, "The voucher cannot exceed the petty cash float.")
		}
		id := uuid.New()
		acct := in.AccountID
		lines := []bookLine{{role: "expense", debit: in.Expense, docType: "petty_voucher", docID: id.String(), postedOn: in.PostedOn}}
		if in.VAT > 0 {
			lines = append(lines, bookLine{role: "vat_input", debit: in.VAT, docType: "petty_voucher", docID: id.String(), postedOn: in.PostedOn})
		}
		lines = append(lines, bookLine{role: "petty_cash", account: &acct, credit: total, docType: "petty_voucher", docID: id.String(), postedOn: in.PostedOn})
		if _, err := insertBooks(ctx, tx, p.CompanyID, lines); err != nil {
			return err
		}
		out = VoucherResult{ID: id.String(), Postings: toPostings(lines)}
		return nil
	})
	return out, err
}

// ContraInput moves cash to the bank or the bank to cash.
type ContraInput struct {
	Kind          string
	CashAccountID uuid.UUID
	BankAccountID uuid.UUID
	Amount        int64
	PostedOn      time.Time
}

// PostContra deposits cash at the bank or withdraws cash.
func (s *Service) PostContra(ctx context.Context, p rls.Principal, in ContraInput) error {
	if in.Amount <= 0 || (in.Kind != "deposit" && in.Kind != "withdraw") {
		return apierr.New(apierr.ValidationError, "Contra kind and amount are required.")
	}
	return s.tx(ctx, p, func(tx pgx.Tx) error {
		cash := in.CashAccountID
		acct := in.BankAccountID
		id := uuid.NewString()
		var lines []bookLine
		if in.Kind == "deposit" {
			lines = []bookLine{
				{role: "bank", account: &acct, debit: in.Amount, docType: "contra", docID: id, postedOn: in.PostedOn},
				{role: "cash", account: &cash, credit: in.Amount, docType: "contra", docID: id, postedOn: in.PostedOn},
			}
		} else {
			lines = []bookLine{
				{role: "cash", account: &cash, debit: in.Amount, docType: "contra", docID: id, postedOn: in.PostedOn},
				{role: "bank", account: &acct, credit: in.Amount, docType: "contra", docID: id, postedOn: in.PostedOn},
			}
		}
		_, err := insertBooks(ctx, tx, p.CompanyID, lines)
		return err
	})
}

// PaymentInput is money leaving a bank or cash account. It is not a receipt allocation.
type PaymentInput struct {
	AccountID uuid.UUID
	Amount    int64
	PostedOn  time.Time
}

// PostPayment credits the cash book.
func (s *Service) PostPayment(ctx context.Context, p rls.Principal, in PaymentInput) error {
	if in.Amount <= 0 {
		return apierr.New(apierr.ValidationError, "Payment amount is required.")
	}
	return s.tx(ctx, p, func(tx pgx.Tx) error {
		var kind string
		err := tx.QueryRow(ctx, `SELECT kind FROM erp.bank_accounts WHERE company_id = $1 AND id = $2`, p.CompanyID, in.AccountID).Scan(&kind)
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.New(apierr.NotFound, "Account not found.")
		}
		if err != nil {
			return err
		}
		role := kind
		if kind == "petty" {
			role = "petty_cash"
		}
		acct := in.AccountID
		id := uuid.NewString()
		_, err = insertBooks(ctx, tx, p.CompanyID, []bookLine{
			{role: "expense", debit: in.Amount, docType: "payment", docID: id, postedOn: in.PostedOn},
			{role: role, account: &acct, credit: in.Amount, docType: "payment", docID: id, postedOn: in.PostedOn},
		})
		return err
	})
}

// ChequeInput registers one cheque number in one state.
type ChequeInput struct {
	AccountID uuid.UUID
	Number    int64
	State     string
}

// ChequeRef is the stored cheque.
type ChequeRef struct {
	ID           string `json:"id"`
	State        string `json:"state"`
	StateVersion string `json:"state_version"`
}

// RegisterCheque records a cheque number. A second row for the same number is refused.
func (s *Service) RegisterCheque(ctx context.Context, p rls.Principal, in ChequeInput) (ChequeRef, error) {
	if in.Number <= 0 {
		return ChequeRef{}, apierr.New(apierr.ValidationError, "Cheque number is required.")
	}
	id := uuid.New()
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		var existing string
		err := tx.QueryRow(ctx, `
			SELECT state FROM erp.bank_cheques WHERE company_id = $1 AND account_id = $2 AND number = $3`,
			p.CompanyID, in.AccountID, in.Number).Scan(&existing)
		if err == nil {
			return apierr.New(apierr.Conflict, "That cheque number is already in a state.")
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if in.State != "issued" {
			return apierr.New(apierr.ValidationError, "A new cheque starts as issued.")
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO erp.bank_cheques (company_id, id, account_id, number, state) VALUES ($1, $2, $3, $4, 'issued')`,
			p.CompanyID, id, in.AccountID, in.Number)
		return err
	})
	return ChequeRef{ID: id.String(), State: "issued", StateVersion: "1"}, err
}

// TransitionCheque moves a cheque to stopped, cleared, or voided. A stopped cheque cannot clear.
func (s *Service) TransitionCheque(ctx context.Context, p rls.Principal, id uuid.UUID, expected int64, next string) (ChequeRef, error) {
	if next != "stopped" && next != "cleared" && next != "voided" {
		return ChequeRef{}, apierr.New(apierr.ValidationError, "Cheque state is not supported.")
	}
	var out ChequeRef
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		var state string
		var version int64
		err := tx.QueryRow(ctx, `SELECT state, state_version FROM erp.bank_cheques WHERE company_id = $1 AND id = $2 FOR UPDATE`, p.CompanyID, id).Scan(&state, &version)
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.New(apierr.NotFound, "Cheque not found.")
		}
		if err != nil {
			return err
		}
		if err := ifmatch.Check(expected, version, map[string]any{"state_version": fmt.Sprint(version)}); err != nil {
			return err
		}
		if state == "stopped" && next == "cleared" {
			return apierr.New(apierr.Conflict, "A stopped cheque cannot clear.")
		}
		if state != "issued" {
			return apierr.New(apierr.Conflict, "The cheque is already in a final state.")
		}
		ver := version + 1
		if _, err := tx.Exec(ctx, `UPDATE erp.bank_cheques SET state = $3, state_version = $4 WHERE company_id = $1 AND id = $2`, p.CompanyID, id, next, ver); err != nil {
			return err
		}
		out = ChequeRef{ID: id.String(), State: next, StateVersion: fmt.Sprint(ver)}
		return nil
	})
	return out, err
}

// Gaps lists missing cheque numbers between the lowest and highest issued number.
type Gaps struct {
	Gaps []string `json:"gaps"`
}

// ChequeGaps reports numbers that were never registered.
func (s *Service) ChequeGaps(ctx context.Context, p rls.Principal, account uuid.UUID) (Gaps, error) {
	var out Gaps
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		q, err := tx.Query(ctx, `SELECT number FROM erp.bank_cheques WHERE company_id = $1 AND account_id = $2 ORDER BY number`, p.CompanyID, account)
		if err != nil {
			return err
		}
		defer q.Close()
		var nums []int64
		for q.Next() {
			var n int64
			if err := q.Scan(&n); err != nil {
				return err
			}
			nums = append(nums, n)
		}
		if err := q.Err(); err != nil {
			return err
		}
		if len(nums) == 0 {
			return nil
		}
		seen := map[int64]bool{}
		for _, n := range nums {
			seen[n] = true
		}
		for n := nums[0]; n <= nums[len(nums)-1]; n++ {
			if !seen[n] {
				out.Gaps = append(out.Gaps, strconv.FormatInt(n, 10))
			}
		}
		return nil
	})
	if out.Gaps == nil {
		out.Gaps = []string{}
	}
	return out, err
}

// FacilityInput tracks a bank facility.
type FacilityInput struct {
	Name         string
	Limit        int64
	AnnualRateBP int
	Maturity     time.Time
}

// FacilityRef is the stored facility.
type FacilityRef struct {
	ID           string `json:"id"`
	StateVersion string `json:"state_version"`
}

// OpenFacility stores the limit.
func (s *Service) OpenFacility(ctx context.Context, p rls.Principal, in FacilityInput) (FacilityRef, error) {
	if in.Name == "" || in.Limit <= 0 {
		return FacilityRef{}, apierr.New(apierr.ValidationError, "Facility name and limit are required.")
	}
	id := uuid.New()
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO erp.bank_facilities (company_id, id, name, limit_fils, annual_rate_bp, maturity)
			VALUES ($1, $2, $3, $4, $5, $6)`, p.CompanyID, id, in.Name, in.Limit, in.AnnualRateBP, in.Maturity)
		return err
	})
	return FacilityRef{ID: id.String(), StateVersion: "1"}, err
}

// DrawResult is utilisation after a draw.
type DrawResult struct {
	Utilisation  Money  `json:"utilisation"`
	StateVersion string `json:"state_version"`
}

// Draw increases utilisation and refuses a draw past the limit.
func (s *Service) Draw(ctx context.Context, p rls.Principal, id uuid.UUID, expected, amount int64) (DrawResult, error) {
	if amount <= 0 {
		return DrawResult{}, apierr.New(apierr.ValidationError, "Draw amount is required.")
	}
	var out DrawResult
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		var limit, used, version int64
		err := tx.QueryRow(ctx, `
			SELECT limit_fils, utilisation_fils, state_version FROM erp.bank_facilities
			WHERE company_id = $1 AND id = $2 FOR UPDATE`, p.CompanyID, id).Scan(&limit, &used, &version)
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.New(apierr.NotFound, "Facility not found.")
		}
		if err != nil {
			return err
		}
		if err := ifmatch.Check(expected, version, map[string]any{"state_version": fmt.Sprint(version)}); err != nil {
			return err
		}
		if used+amount > limit {
			return apierr.New(apierr.ValidationError, "The draw exceeds the facility limit.")
		}
		next := version + 1
		if _, err := tx.Exec(ctx, `
			UPDATE erp.bank_facilities SET utilisation_fils = utilisation_fils + $3, state_version = $4
			WHERE company_id = $1 AND id = $2`, p.CompanyID, id, amount, next); err != nil {
			return err
		}
		out = DrawResult{Utilisation: AED(used + amount), StateVersion: fmt.Sprint(next)}
		return nil
	})
	return out, err
}

// InterestResult is interest posted on the facility schedule.
type InterestResult struct {
	Amount   Money     `json:"amount"`
	Postings []Posting `json:"postings"`
}

// PostInterest posts expense against payable for the current utilisation.
func (s *Service) PostInterest(ctx context.Context, p rls.Principal, id uuid.UUID, days int) (InterestResult, error) {
	var out InterestResult
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		var used int64
		var rate int
		err := tx.QueryRow(ctx, `SELECT utilisation_fils, annual_rate_bp FROM erp.bank_facilities WHERE company_id = $1 AND id = $2`, p.CompanyID, id).Scan(&used, &rate)
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.New(apierr.NotFound, "Facility not found.")
		}
		if err != nil {
			return err
		}
		amount := SimpleInterest(used, int64(rate), int64(days))
		if amount <= 0 {
			return apierr.New(apierr.ValidationError, "There is no interest to post.")
		}
		lines := []bookLine{
			{role: "expense", debit: amount, docType: "facility_interest", docID: id.String(), postedOn: time.Now().UTC()},
			{role: "payable", credit: amount, docType: "facility_interest", docID: id.String(), postedOn: time.Now().UTC()},
		}
		if _, err := insertBooks(ctx, tx, p.CompanyID, lines); err != nil {
			return err
		}
		out = InterestResult{Amount: AED(amount), Postings: toPostings(lines)}
		return nil
	})
	return out, err
}

// CashPosition is the daily cash position. PDC in hand is reported beside the balance and is not part of closing cash.
type CashPosition struct {
	Opening           Money `json:"opening"`
	Receipts          Money `json:"receipts"`
	Payments          Money `json:"payments"`
	PDCsDueThisWeek   Money `json:"pdcs_due_this_week"`
	Closing           Money `json:"closing"`
	BankAndCash       Money `json:"bank_and_cash"`
	UnreconciledCount int   `json:"unreconciled_count"`
}

// Position reconciles opening, receipts, and payments to the bank and cash accounts.
// Cheques still in hand are reported separately and are not closing cash.
func (s *Service) Position(ctx context.Context, p rls.Principal, day time.Time) (CashPosition, error) {
	var out CashPosition
	err := s.tx(ctx, p, func(tx pgx.Tx) error {
		var opening, receipts, payments, book, pdc int64
		var unmatched int
		if err := tx.QueryRow(ctx, `
			SELECT
				COALESCE(SUM(debit_fils - credit_fils) FILTER (WHERE posted_on < $2), 0),
				COALESCE(SUM(debit_fils) FILTER (WHERE posted_on = $2 AND doc_type = 'receipt'), 0),
				COALESCE(SUM(credit_fils) FILTER (WHERE posted_on = $2 AND doc_type = 'payment'), 0),
				COALESCE(SUM(debit_fils - credit_fils) FILTER (WHERE posted_on <= $2), 0)
			FROM erp.book_lines
			WHERE company_id = $1 AND role IN ('bank', 'cash')`, p.CompanyID, day).Scan(&opening, &receipts, &payments, &book); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(SUM(amount_fils), 0) FROM erp.ar_pdcs
			WHERE company_id = $1 AND direction = 'received' AND status = 'in_hand'
			  AND cheque_date >= $2 AND cheque_date < $3`, p.CompanyID, day, day.AddDate(0, 0, 7)).Scan(&pdc); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			SELECT COUNT(*) FROM erp.bank_statement_lines
			WHERE company_id = $1 AND status IN ('open', 'proposed')`, p.CompanyID).Scan(&unmatched); err != nil {
			return err
		}
		out = CashPosition{
			Opening: AED(opening), Receipts: AED(receipts), Payments: AED(payments),
			PDCsDueThisWeek: AED(pdc), Closing: AED(opening + receipts - payments),
			BankAndCash: AED(book), UnreconciledCount: unmatched,
		}
		return nil
	})
	return out, err
}
