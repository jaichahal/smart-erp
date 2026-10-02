package sales

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// Service is the sales document state machine.
// A sales order posts nothing. Registration of the invoice is the first revenue posting.
type Service struct {
	pool   *pgxpool.Pool
	stock  Stock
	ledger Ledger
	credit CreditChecker
}

// Option replaces a port. A nil port uses the Postgres adapter in this package.
type Option func(*Service)

// WithStock replaces the reservation adapter.
func WithStock(s Stock) Option { return func(svc *Service) { svc.stock = s } }

// WithLedger replaces the posting adapter.
func WithLedger(l Ledger) Option { return func(svc *Service) { svc.ledger = l } }

// WithCredit replaces the credit-limit check.
func WithCredit(c CreditChecker) Option { return func(svc *Service) { svc.credit = c } }

// New builds a sales service.
func New(pool *pgxpool.Pool, opts ...Option) *Service {
	s := &Service{pool: pool}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *Service) stocks() Stock {
	if s.stock != nil {
		return s.stock
	}
	return pgStock{}
}

func (s *Service) ledgers() Ledger {
	if s.ledger != nil {
		return s.ledger
	}
	return pgLedger{}
}

func (s *Service) credits() CreditChecker {
	if s.credit != nil {
		return s.credit
	}
	return pgCredit{}
}

func (s *Service) tx(ctx context.Context, p rls.Principal, fn func(pgx.Tx) error) error {
	ctx = rls.WithPrincipal(ctx, p)
	return rls.Tx(ctx, s.pool, p, fn)
}

func invalid(msg string) error {
	return apierr.New(apierr.ValidationError, msg)
}

func invalidField(field string) error {
	return apierr.New(apierr.ValidationError, field+" is required").WithDetails(map[string]any{"field": field})
}

func hasRole(p rls.Principal, role string) bool {
	for _, r := range p.Roles {
		if strings.EqualFold(strings.TrimSpace(r), role) {
			return true
		}
	}
	return false
}
