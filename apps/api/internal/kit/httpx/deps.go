package httpx

import (
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/config"
)

// Deps is what every module's Mount(r, deps) receives.
type Deps struct {
	Pool      *pgxpool.Pool
	River     *river.Client[pgx.Tx]
	Config    *config.Config
	Log       *slog.Logger
	StartedAt time.Time
}
