package ledger

import (
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
)

func mapPostErr(err error) error {
	if err == nil {
		return nil
	}
	var pg *pgconn.PgError
	if !errors.As(err, &pg) {
		return err
	}
	switch {
	case strings.Contains(pg.Message, "NEGATIVE_STOCK"):
		return apierr.New(apierr.NegativeStock, "stock on hand cannot be negative")
	case strings.Contains(strings.ToLower(pg.Message), "negative"):
		return apierr.New(apierr.ValidationError, "cash account cannot be negative")
	case strings.Contains(pg.Message, "debits must equal credits"):
		return apierr.New(apierr.ValidationError, "debits must equal credits per currency")
	case strings.Contains(pg.Message, "PERIOD_CLOSED"):
		return apierr.New(apierr.PeriodClosed, "period is closed")
	default:
		return err
	}
}
