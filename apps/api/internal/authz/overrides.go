package authz

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// settingsOverride treats a row in erp.authz_settings as an approval grant.
// Approvals (P1.7) writes sod.override.<approval_id> = user_id; until that
// module is wired, nothing inserts those rows through this package.
type settingsOverride struct {
	pool *pgxpool.Pool
}

// Granted reports whether approvals stored a grant for this user and approval id.
func (o settingsOverride) Granted(ctx context.Context, approvalID, userID string, _ []string) (bool, error) {
	if approvalID == "" || userID == "" || o.pool == nil {
		return false, nil
	}
	var stored string
	err := o.pool.QueryRow(ctx, `SELECT value FROM erp.authz_settings WHERE key = $1`, overrideKey(approvalID)).Scan(&stored)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return stored == userID, nil
}

func overrideKey(approvalID string) string {
	return "sod.override." + approvalID
}
