package authz

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/httpx"
)

// Service is the authorisation API for HTTP handlers and the scheduler.
type Service struct {
	pool      *pgxpool.Pool
	river     *river.Client[pgx.Tx]
	messages  catalog
	overrides OverrideApprover
	matrix    MatrixApprover
}

// New builds a service from the shared dependencies. SoD overrides are granted
// only when a sod.override row exists; matrix edits stay pending until the
// approvals module supplies a request id.
func New(deps httpx.Deps) *Service {
	return &Service{
		pool:      deps.Pool,
		river:     deps.River,
		messages:  loadCatalog(),
		overrides: settingsOverride{pool: deps.Pool},
		matrix:    pendingMatrix{},
	}
}

// WithOverrideApprover installs the approvals-module check for SoD overrides.
func (s *Service) WithOverrideApprover(a OverrideApprover) *Service {
	if a != nil {
		s.overrides = a
	}
	return s
}

// WithMatrixApprover installs the approvals-module request id for matrix edits.
func (s *Service) WithMatrixApprover(a MatrixApprover) *Service {
	if a != nil {
		s.matrix = a
	}
	return s
}

func (s *Service) fail(ctx context.Context, code apierr.Code, id string) *apierr.Error {
	return apierr.New(code, s.messages.text(langOf(ctx), id))
}

func (s *Service) scope(ctx context.Context, tx pgx.Tx, permission string) (string, error) {
	var scope *string
	err := tx.QueryRow(ctx, `SELECT erp.permission_scope($1)`, permission).Scan(&scope)
	if err != nil {
		return "", err
	}
	if scope == nil {
		return "", nil
	}
	return *scope, nil
}

func (s *Service) requireScope(ctx context.Context, tx pgx.Tx, permission, want string) error {
	got, err := s.scope(ctx, tx, permission)
	if err != nil {
		return apierr.Wrap(apierr.Internal, s.messages.text(langOf(ctx), "internal"), err)
	}
	if got == "" || (want != "" && got != want) {
		return s.fail(ctx, apierr.PermissionDenied, "permission.denied")
	}
	return nil
}

var _ LoginGate = (*Service)(nil)
