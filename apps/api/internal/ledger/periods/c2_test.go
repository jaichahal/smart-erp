package periods

import (
	"context"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
)

// C2: two concurrent registrations of the same document type receive distinct consecutive numbers.
func TestC2(t *testing.T) {
	_, svc, p := newService(t)
	start := make(chan struct{})
	var wg sync.WaitGroup
	got := make([]int64, 2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			alloc, err := svc.Register(context.Background(), p, Registration{
				DocType: "sales_invoice", DocID: "concurrent-" + string(rune('a'+i)), FiscalYear: 2026,
				Apply: func(context.Context, pgx.Tx, int64) error { return nil },
			}, "en")
			got[i] = alloc.Number
			errs[i] = err
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("registration %d: %v", i, err)
		}
	}
	if got[0] == got[1] {
		t.Fatalf("numbers not distinct: %v", got)
	}
	lo, hi := got[0], got[1]
	if hi < lo {
		lo, hi = hi, lo
	}
	if lo != 1 || hi != 2 {
		t.Fatalf("numbers = %d and %d, want 1 and 2", lo, hi)
	}
}
