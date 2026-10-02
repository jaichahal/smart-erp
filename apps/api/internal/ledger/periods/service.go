package periods

import (
	"context"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

const (
	// RoleAccountant may soft-close a period (R4.6).
	RoleAccountant = "Accountant"
	// RoleStakeholder may hard-close a period, with an approval (R4.6).
	RoleStakeholder = "Stakeholder"
	// RoleBackdate is the named permission for posting into a prior open period (R4.6, C4).
	RoleBackdate = "backdate"

	statusOpen = "open"
	statusSoft = "soft_closed"
	statusHard = "hard_closed"
	kindMonth  = "month"
	kindAudit  = "audit_adjustment"

	reasonBackdated = "backdated_posting"
)

var docTypeRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// ApprovalGate is the hard-close approval, implemented by the approvals module.
// A nil gate refuses hard close.
type ApprovalGate interface {
	Approved(ctx context.Context, companyID, periodID uuid.UUID, approvalID string) error
}

// Service is the company, period, and numbering API.
type Service struct {
	pool      *pgxpool.Pool
	river     *river.Client[pgx.Tx]
	Approvals ApprovalGate
}

// New returns a service. river may be nil only for callers that never close a period,
// back-date, or register a failure path that emits an event. Close and back-dating require it.
func New(pool *pgxpool.Pool, riverClient *river.Client[pgx.Tx]) *Service {
	return &Service{pool: pool, river: riverClient}
}

// Period is the wire shape of a fiscal period (contract Period).
type Period struct {
	ID           string `json:"id"`
	FiscalYear   int    `json:"fiscal_year"`
	Month        *int   `json:"month"`
	Kind         string `json:"kind"`
	Status       string `json:"status"`
	StartDate    string `json:"start_date"`
	EndDate      string `json:"end_date"`
	StateVersion int64  `json:"state_version"`
}

// Posting is a guard check. The journal itself belongs to a later ledger task.
type Posting struct {
	PostingDate time.Time
	DocType     string
	DocID       string
}

// Exception is one exceptions-report row.
type Exception struct {
	ID          uuid.UUID
	Kind        string
	PeriodID    uuid.UUID
	PostingDate time.Time
	DocType     string
	DocID       string
	ActorID     string
	ReasonCode  string
	Reason      string
	OccurredAt  time.Time
}

// Registration allocates a number inside the caller's business write.
type Registration struct {
	DocType    string
	DocID      string
	FiscalYear int
	Apply      func(ctx context.Context, tx pgx.Tx, number int64) error
}

// Allocation is the number taken for a registration. Voided means the business write
// was rolled back and the number was recorded as void.
type Allocation struct {
	Number     int64
	DocType    string
	FiscalYear int
	Voided     bool
}

func fail(lang string, code apierr.Code, key string) error {
	return apierr.New(code, tr(lang, key))
}

func hasRole(p rls.Principal, role string) bool {
	for _, r := range p.Roles {
		if r == role {
			return true
		}
	}
	return false
}
