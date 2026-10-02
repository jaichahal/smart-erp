package stock_test

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/httpx"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
	"github.com/jaichahal/smart-erp/apps/api/internal/stock"
)

func TestH1_AvailableIsOnHandMinusActiveReservations(t *testing.T) {
	env := newEnv(t)
	sku := env.item(t, "FG-H1", stock.ClassFinished, "ea")
	wh := env.warehouse(t, "MAIN")
	ctx := context.Background()
	if _, err := env.svc.Post(ctx, env.actor, stock.Move{
		SKUID: sku, WarehouseID: wh, QtyDelta: "200", UnitCost: "3.50",
		MovementType: stock.MoveReceipt, SourceDocID: "grn-h1",
	}); err != nil {
		t.Fatal(err)
	}

	type hold struct {
		line string
		qty  string
	}
	var active []hold
	rng := rand.New(rand.NewSource(2303))
	pendingLine := "pending-h1"
	if _, err := env.svc.Reserve(ctx, env.actor, stock.ReserveRequest{
		OrderLineID: pendingLine, SKUID: sku, WarehouseID: wh, Qty: "9", Pending: true,
	}); err != nil {
		t.Fatal(err)
	}
	assertAvailable(t, env, sku, wh)

	for i := 0; i < 48; i++ {
		switch rng.Intn(4) {
		case 0:
			pos := env.position(t, sku, wh)
			free := qtyInt(t, pos.Available)
			if free < 1 {
				break
			}
			n := 1 + rng.Intn(free)
			line := uuid.NewString()
			if _, err := env.svc.Reserve(ctx, env.actor, stock.ReserveRequest{
				OrderLineID: line, SKUID: sku, WarehouseID: wh, Qty: itoa(n),
			}); err != nil {
				t.Fatal(err)
			}
			active = append(active, hold{line: line, qty: itoa(n)})
		case 1:
			if len(active) == 0 {
				break
			}
			j := rng.Intn(len(active))
			if err := env.svc.Release(ctx, env.actor, active[j].line); err != nil {
				t.Fatal(err)
			}
			active = append(active[:j], active[j+1:]...)
		case 2:
			if len(active) == 0 {
				break
			}
			j := rng.Intn(len(active))
			if _, err := env.svc.Consume(ctx, env.actor, active[j].line, "dn-"+active[j].line); err != nil {
				t.Fatal(err)
			}
			active = append(active[:j], active[j+1:]...)
		default:
			n := 1 + rng.Intn(5)
			if _, err := env.svc.Post(ctx, env.actor, stock.Move{
				SKUID: sku, WarehouseID: wh, QtyDelta: itoa(n), UnitCost: "4.00",
				MovementType: stock.MoveReceipt, SourceDocID: "grn-" + uuid.NewString(),
			}); err != nil {
				t.Fatal(err)
			}
		}
		assertAvailable(t, env, sku, wh)
	}
	var pendingStatus string
	err := rls.Tx(ctx, env.db.App, env.actor, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT status FROM erp.stock_reservations WHERE company_id=$1 AND order_line_id=$2`, env.actor.CompanyID, pendingLine).Scan(&pendingStatus)
	})
	if err != nil {
		t.Fatal(err)
	}
	if pendingStatus != stock.StatusPending {
		t.Fatalf("pending reservation status %s", pendingStatus)
	}
}

func TestH2_MovingAverageRecomputesOnReceiptsNeverOnIssues(t *testing.T) {
	env := newEnv(t)
	sku := env.item(t, "FG-H2", stock.ClassFinished, "ea")
	w1 := env.warehouse(t, "W1")
	w2 := env.warehouse(t, "W2")
	ctx := context.Background()

	post := func(wh uuid.UUID, delta, cost, kind, src string) stock.Line {
		t.Helper()
		line, err := env.svc.Post(ctx, env.actor, stock.Move{
			SKUID: sku, WarehouseID: wh, QtyDelta: delta, UnitCost: cost,
			MovementType: kind, SourceDocID: src,
		})
		if err != nil {
			t.Fatal(err)
		}
		return line
	}
	avg := func(wh uuid.UUID) string {
		t.Helper()
		return env.position(t, sku, wh).UnitCost
	}

	post(w1, "10", "4", stock.MoveReceipt, "r1")
	if !sameQty(avg(w1), "4") {
		t.Fatalf("first receipt average %s", avg(w1))
	}
	post(w1, "10", "8", stock.MoveReceipt, "r2")
	if !sameQty(avg(w1), "6") {
		t.Fatalf("second receipt average %s, want 6", avg(w1))
	}
	beforeIssue := avg(w1)
	issued := post(w1, "-5", "", stock.MoveDelivery, "d1")
	if !sameQty(issued.UnitCost, "6") {
		t.Fatalf("issue cost %s, want moving average 6", issued.UnitCost)
	}
	if sameQty(issued.UnitCost, "4") {
		t.Fatal("issue cost matches the first receipt; that is FIFO")
	}
	if !sameQty(avg(w1), beforeIssue) {
		t.Fatalf("delivery changed average from %s to %s", beforeIssue, avg(w1))
	}
	before := avg(w1)
	post(w1, "-2", "", stock.MoveWriteOff, "wo1")
	post(w1, "-1", "", stock.MoveConsume, "pc1")
	post(w1, "-1", "", stock.MoveJobOut, "jo1")
	if !sameQty(avg(w1), before) {
		t.Fatalf("issues changed average from %s to %s", before, avg(w1))
	}
	post(w1, "10", "2", stock.MoveReceipt, "r3")
	if sameQty(avg(w1), before) {
		t.Fatal("receipt at a new cost left the average unchanged")
	}
	recomputed := avg(w1)

	post(w2, "10", "3", stock.MoveReceipt, "r-w2")
	if !sameQty(avg(w2), "3") {
		t.Fatalf("warehouse 2 average %s", avg(w2))
	}
	if !sameQty(avg(w1), recomputed) {
		t.Fatal("warehouse 2 receipt changed warehouse 1 average")
	}

	other := env.item(t, "RM-H2", stock.ClassRaw, "kg")
	postKind := func(delta, cost, kind, src string) {
		t.Helper()
		if _, err := env.svc.Post(ctx, env.actor, stock.Move{
			SKUID: other, WarehouseID: w1, QtyDelta: delta, UnitCost: cost,
			MovementType: kind, SourceDocID: src,
		}); err != nil {
			t.Fatal(err)
		}
	}
	otherAvg := func() string { return env.position(t, other, w1).UnitCost }
	postKind("10", "5", stock.MoveOpening, "op1")
	if !sameQty(otherAvg(), "5") {
		t.Fatalf("opening average %s", otherAvg())
	}
	postKind("10", "7", stock.MoveJobIn, "ji1")
	if !sameQty(otherAvg(), "6") {
		t.Fatalf("job in average %s, want 6", otherAvg())
	}
	held := otherAvg()
	postKind("-4", "", stock.MoveJobOut, "jo2")
	if !sameQty(otherAvg(), held) {
		t.Fatalf("job out changed average from %s to %s", held, otherAvg())
	}
	postKind("4", "10", stock.MoveProduce, "pp1")
	if sameQty(otherAvg(), held) {
		t.Fatal("production receipt left the average unchanged")
	}
	held = otherAvg()
	postKind("10", "2", stock.MoveAdjustment, "adj-in")
	if sameQty(otherAvg(), held) {
		t.Fatal("positive adjustment left the average unchanged")
	}
	held = otherAvg()
	postKind("-10", "", stock.MoveAdjustment, "adj-out")
	if !sameQty(otherAvg(), held) {
		t.Fatalf("negative adjustment changed average from %s to %s", held, otherAvg())
	}

	pos := env.position(t, sku, w1)
	_, err := env.svc.Post(ctx, env.actor, stock.Move{
		SKUID: sku, WarehouseID: w1, QtyDelta: "-100000", MovementType: stock.MoveDelivery, SourceDocID: "too-big",
	})
	if codeOf(t, err) != apierr.NegativeStock {
		t.Fatalf("over issue code %v", err)
	}
	after := env.position(t, sku, w1)
	if !sameQty(after.OnHand, pos.OnHand) || !sameQty(after.UnitCost, pos.UnitCost) {
		t.Fatalf("refused issue changed position from %+v to %+v", pos, after)
	}
}

func TestH3_ReservationConsumedOnceByDeliveryNote(t *testing.T) {
	env := newEnv(t)
	sku := env.item(t, "FG-H3", stock.ClassFinished, "ea")
	wh := env.warehouse(t, "MAIN")
	ctx := context.Background()
	if _, err := env.svc.Post(ctx, env.actor, stock.Move{
		SKUID: sku, WarehouseID: wh, QtyDelta: "8", UnitCost: "2",
		MovementType: stock.MoveReceipt, SourceDocID: "grn-h3",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.Reserve(ctx, env.actor, stock.ReserveRequest{
		OrderLineID: "ol-1", SKUID: sku, WarehouseID: wh, Qty: "3",
	}); err != nil {
		t.Fatal(err)
	}
	before := env.position(t, sku, wh)
	line, err := env.svc.Consume(ctx, env.actor, "ol-1", "dn-1")
	if err != nil {
		t.Fatal(err)
	}
	if line.MovementType != stock.MoveDelivery || line.SourceDocID != "dn-1" {
		t.Fatalf("delivery line %+v", line)
	}
	if !sameQty(line.QtyDelta, "-3") || !sameQty(line.UnitCost, "2") {
		t.Fatalf("delivery cost %+v", line)
	}
	after := env.position(t, sku, wh)
	if !sameQty(after.OnHand, "5") || !sameQty(after.Reserved, "0") || !sameQty(after.Available, "5") {
		t.Fatalf("after consume %+v", after)
	}
	if !sameQty(after.Available, before.Available) {
		t.Fatalf("available changed from %s to %s", before.Available, after.Available)
	}
	_, err = env.svc.Consume(ctx, env.actor, "ol-1", "dn-1")
	if codeOf(t, err) != apierr.Conflict {
		t.Fatalf("second consume %v", err)
	}
	_, err = env.svc.Consume(ctx, env.actor, "ol-1", "dn-2")
	if codeOf(t, err) != apierr.Conflict {
		t.Fatalf("second note %v", err)
	}
	again := env.position(t, sku, wh)
	if !sameQty(again.OnHand, after.OnHand) || !sameQty(again.Reserved, "0") {
		t.Fatalf("second consume changed %+v", again)
	}
	var n int
	err = rls.Tx(ctx, env.db.App, env.actor, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM erp.stock_ledger_lines WHERE company_id=$1 AND source_doc_id=$2`, env.actor.CompanyID, "dn-1").Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("delivery lines %d", n)
	}
}

func TestH4_LastUnitGoesToExactlyOneReservation(t *testing.T) {
	env := newEnv(t)
	sku := env.item(t, "FG-H4", stock.ClassFinished, "ea")
	wh := env.warehouse(t, "MAIN")
	ctx := context.Background()
	if _, err := env.svc.Post(ctx, env.actor, stock.Move{
		SKUID: sku, WarehouseID: wh, QtyDelta: "1", UnitCost: "9",
		MovementType: stock.MoveReceipt, SourceDocID: "grn-h4",
	}); err != nil {
		t.Fatal(err)
	}
	var ready, goOn sync.WaitGroup
	ready.Add(2)
	goOn.Add(1)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		line := "agent-" + string(rune('a'+i))
		go func() {
			ready.Done()
			goOn.Wait()
			_, err := env.svc.Reserve(ctx, env.actor, stock.ReserveRequest{
				OrderLineID: line, SKUID: sku, WarehouseID: wh, Qty: "1",
			})
			errs <- err
		}()
	}
	ready.Wait()
	goOn.Done()
	var won, lost int
	for i := 0; i < 2; i++ {
		err := <-errs
		if err == nil {
			won++
			continue
		}
		lost++
		if codeOf(t, err) != apierr.NegativeStock {
			t.Fatalf("losing reservation %v", err)
		}
	}
	if won != 1 || lost != 1 {
		t.Fatalf("won %d lost %d", won, lost)
	}
	pos := env.position(t, sku, wh)
	if !sameQty(pos.OnHand, "1") || !sameQty(pos.Reserved, "1") || !sameQty(pos.Available, "0") {
		t.Fatalf("last unit %+v", pos)
	}
}

func TestH5_LedgerLinesAreImmutableAdjustmentsAreNewLines(t *testing.T) {
	env := newEnv(t)
	sku := env.item(t, "FG-H5", stock.ClassFinished, "ea")
	wh := env.warehouse(t, "MAIN")
	ctx := context.Background()
	first, err := env.svc.Post(ctx, env.actor, stock.Move{
		SKUID: sku, WarehouseID: wh, QtyDelta: "6", UnitCost: "1.25",
		MovementType: stock.MoveOpening, SourceDocID: "open-h5",
	})
	if err != nil {
		t.Fatal(err)
	}
	// The owner role reaches the guard trigger. The app role is refused earlier by grants.
	if _, err = env.db.App.Exec(ctx, `UPDATE erp.stock_ledger_lines SET qty_delta = qty_delta + 1 WHERE id = $1`, first.ID); err == nil {
		t.Fatal("app role updated a ledger line")
	}
	if _, err = env.db.Migrator.Exec(ctx, `UPDATE erp.stock_ledger_lines SET qty_delta = qty_delta + 1 WHERE id = $1`, first.ID); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("update err %v", err)
	}
	if _, err = env.db.Migrator.Exec(ctx, `DELETE FROM erp.stock_ledger_lines WHERE id = $1`, first.ID); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("delete err %v", err)
	}
	var qty string
	err = rls.Tx(ctx, env.db.App, env.actor, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT qty_delta::text FROM erp.stock_ledger_lines WHERE id = $1`, first.ID).Scan(&qty)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sameQty(qty, "6") {
		t.Fatalf("line changed to %s", qty)
	}
	second, err := env.svc.Post(ctx, env.actor, stock.Move{
		SKUID: sku, WarehouseID: wh, QtyDelta: "-1", MovementType: stock.MoveAdjustment, SourceDocID: "adj-h5",
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID {
		t.Fatal("adjustment reused the original line")
	}
	var n int
	err = rls.Tx(ctx, env.db.App, env.actor, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM erp.stock_ledger_lines WHERE company_id = $1 AND sku_id = $2`, env.actor.CompanyID, sku).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("lines %d", n)
	}
	pos := env.position(t, sku, wh)
	if !sameQty(pos.OnHand, "5") {
		t.Fatalf("on hand %s", pos.OnHand)
	}
}

func TestH6_SalesAgentAvailabilityHasNoCostFields(t *testing.T) {
	env := newEnv(t)
	finished := env.item(t, "FG-H6", stock.ClassFinished, "ea")
	raw := env.item(t, "RM-H6", stock.ClassRaw, "kg")
	wh := env.warehouse(t, "MAIN")
	ctx := context.Background()
	for _, sku := range []uuid.UUID{finished, raw} {
		if _, err := env.svc.Post(ctx, env.actor, stock.Move{
			SKUID: sku, WarehouseID: wh, QtyDelta: "4", UnitCost: "11.5",
			MovementType: stock.MoveReceipt, SourceDocID: "grn-" + sku.String(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	sales := env.actor
	sales.Roles = []string{"sales_agent"}
	sales.UserID = "sales-" + env.actor.CompanyID.String()
	body := availabilityHTTP(t, env, sales, "")
	rawJSON, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if costFieldPresent(body) || bytesHasCost(rawJSON) {
		t.Fatalf("sales payload contains cost fields: %s", rawJSON)
	}
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("sales items %d, body %s", len(items), rawJSON)
	}
	row, _ := items[0].(map[string]any)
	if row["sku"] != "FG-H6" {
		t.Fatalf("row %v", row)
	}
	for _, key := range []string{"on_hand", "reserved", "available", "uom"} {
		if _, ok := row[key]; !ok {
			t.Fatalf("missing %s in %v", key, row)
		}
	}
	if !sameQty(asString(row["on_hand"]), "4") || !sameQty(asString(row["available"]), "4") || !sameQty(asString(row["reserved"]), "0") {
		t.Fatalf("quantities %v", row)
	}
	rawBody := availabilityHTTP(t, env, sales, "raw_material")
	rawItems, _ := rawBody["items"].([]any)
	if len(rawItems) != 0 {
		t.Fatalf("sales agent saw raw material: %v", rawBody)
	}

	accountant := env.actor
	accountant.Roles = []string{"accountant"}
	accountant.UserID = "acct-" + env.actor.CompanyID.String()
	acct := availabilityHTTP(t, env, accountant, "finished_goods")
	acctItems, _ := acct["items"].([]any)
	if len(acctItems) != 1 {
		t.Fatalf("accountant items %v", acct)
	}
	acctRow, _ := acctItems[0].(map[string]any)
	if _, ok := acctRow["unit_cost"]; !ok {
		t.Fatalf("accountant payload omitted unit_cost: %v", acctRow)
	}
	if !sameQty(asString(acctRow["unit_cost"]), "11.5") {
		t.Fatalf("unit cost %v", acctRow["unit_cost"])
	}
}

func assertAvailable(t *testing.T, env *env, sku, wh uuid.UUID) {
	t.Helper()
	pos := env.position(t, sku, wh)
	var onHand, reserved string
	err := rls.Tx(context.Background(), env.db.App, env.actor, func(tx pgx.Tx) error {
		if err := tx.QueryRow(context.Background(), `
			SELECT COALESCE(SUM(qty_delta), 0)::text
			FROM erp.stock_ledger_lines
			WHERE company_id = $1 AND sku_id = $2 AND warehouse_id = $3`,
			env.actor.CompanyID, sku, wh).Scan(&onHand); err != nil {
			return err
		}
		return tx.QueryRow(context.Background(), `
			SELECT COALESCE(SUM(qty), 0)::text
			FROM erp.stock_reservations
			WHERE company_id = $1 AND sku_id = $2 AND warehouse_id = $3 AND status = 'active'`,
			env.actor.CompanyID, sku, wh).Scan(&reserved)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sameQty(pos.OnHand, onHand) || !sameQty(pos.Reserved, reserved) {
		t.Fatalf("position on_hand %s reserved %s, ledger on_hand %s active %s", pos.OnHand, pos.Reserved, onHand, reserved)
	}
	want := new(big.Rat).Sub(parseQty(t, onHand), parseQty(t, reserved))
	if parseQty(t, pos.Available).Cmp(want) != 0 {
		t.Fatalf("available %s, on hand %s minus reserved %s", pos.Available, onHand, reserved)
	}
}

func availabilityHTTP(t *testing.T, env *env, p rls.Principal, class string) map[string]any {
	t.Helper()
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := rls.WithPrincipal(r.Context(), p)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	stock.Mount(r, httpx.Deps{Pool: env.db.App})
	req := httptest.NewRequest(http.MethodGet, "/stock/availability?class="+class, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("availability %d %s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data
}

var costKeys = map[string]struct{}{
	"cost": {}, "unit_cost": {}, "total_cost": {}, "line_cost": {}, "value": {},
	"moving_average": {}, "margin": {}, "unit_margin": {}, "margin_amount": {},
	"margin_percent": {}, "gross_margin": {},
}

func costFieldPresent(v any) bool {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if _, ok := costKeys[strings.ToLower(k)]; ok {
				return true
			}
			if costFieldPresent(child) {
				return true
			}
		}
	case []any:
		for _, child := range t {
			if costFieldPresent(child) {
				return true
			}
		}
	}
	return false
}

func bytesHasCost(raw []byte) bool {
	s := strings.ToLower(string(raw))
	for key := range costKeys {
		if strings.Contains(s, `"`+key+`"`) {
			return true
		}
	}
	return false
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func codeOf(t *testing.T, err error) apierr.Code {
	t.Helper()
	var ae *apierr.Error
	if !errors.As(err, &ae) {
		t.Fatalf("expected api error, got %v", err)
	}
	return ae.Code
}

func sameQty(a, b string) bool {
	ra, ok1 := new(big.Rat).SetString(a)
	rb, ok2 := new(big.Rat).SetString(b)
	return ok1 && ok2 && ra.Cmp(rb) == 0
}

func parseQty(t *testing.T, s string) *big.Rat {
	t.Helper()
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		t.Fatalf("bad qty %q", s)
	}
	return r
}

func qtyInt(t *testing.T, s string) int {
	t.Helper()
	r := parseQty(t, s)
	if !r.IsInt() {
		t.Fatalf("qty %s is not integral", s)
	}
	return int(r.Num().Int64())
}

func itoa(n int) string {
	return big.NewInt(int64(n)).String()
}
