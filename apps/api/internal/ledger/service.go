package ledger

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jaichahal/smart-erp/apps/api/internal/ledger/periods"
)

// Service posts journals and resolves versioned posting rules.
type Service struct {
	pool    *pgxpool.Pool
	periods *periods.Service
}

// New returns the ledger service. Period status is checked through the periods package.
func New(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool, periods: periods.New(pool, nil)}
}
