package outbox_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/outbox"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/testdb"
)

// E1: a business write and its outbox row commit atomically; a rollback leaves neither.
func TestE1_TransactionalEnqueue(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	client, err := outbox.NewClient(db.App, nil, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	p := rls.Principal{UserID: "u", CompanyID: uuid.New()}

	// Rolled back: no job.
	tx, err := rls.Begin(ctx, db.App, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := outbox.InsertTx(ctx, client, tx, outbox.EchoArgs{Message: "rolled back"}, nil); err != nil {
		t.Fatal(err)
	}
	_ = tx.Rollback(ctx)

	// Committed: exactly one job.
	err = rls.Tx(ctx, db.App, p, func(tx pgx.Tx) error {
		_, err := outbox.InsertTx(ctx, client, tx, outbox.EchoArgs{Message: "committed"}, nil)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.App.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind = 'kit.echo'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected exactly 1 job, got %d", n)
	}
}
