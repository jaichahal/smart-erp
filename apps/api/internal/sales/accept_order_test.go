package sales

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
)

func TestOrderPostsNothing(t *testing.T) {
	w := newWorld(t)
	led := &countingLedger{}
	w.svc = New(w.pool, WithLedger(led))
	order, err := w.svc.CreateOrder(context.Background(), w.p, w.orderInput())
	if err != nil {
		t.Fatalf("create order: %v", err)
	}
	if order.Status != StatusReserved {
		t.Fatalf("status %s", order.Status)
	}
	if led.n != 0 {
		t.Fatal("an order posted to the ledger")
	}
	if w.count(t, `SELECT count(*) FROM erp.sales_postings WHERE company_id=$1`, w.p.CompanyID) != 0 {
		t.Fatal("an order wrote a posting row")
	}
}

func TestReservationIsNotSkipped(t *testing.T) {
	w := newWorld(t)
	stock := &countingStock{fail: errors.New("no quantity")}
	w.svc = New(w.pool, WithStock(stock))
	_, err := w.svc.CreateOrder(context.Background(), w.p, w.orderInput())
	if stock.calls == 0 {
		t.Fatal("reservation was skipped")
	}
	if !stock.saw {
		t.Fatalf("reservation was not in the order transaction: %v", err)
	}
	if err == nil {
		t.Fatal("failed reservation must fail the order")
	}
	if w.count(t, `SELECT count(*) FROM erp.sales_orders WHERE company_id=$1`, w.p.CompanyID) != 0 {
		t.Fatal("order row survived a failed reservation")
	}
}

func TestCreditCheckIsNotSkipped(t *testing.T) {
	w := newWorld(t)
	credit := &countingCredit{}
	w.svc = New(w.pool, WithCredit(credit), WithStock(&countingStock{}))
	_, err := w.svc.CreateOrder(context.Background(), w.p, w.orderInput())
	if credit.calls == 0 {
		t.Fatal("credit check was skipped")
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestS1_OrderReservesInTheSameTransaction(t *testing.T) {
	w := newWorld(t)
	order, err := w.svc.CreateOrder(context.Background(), w.p, w.orderInput())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if order.Status != StatusReserved {
		t.Fatalf("status %s", order.Status)
	}
	if w.count(t, `SELECT count(*) FROM erp.sales_reservations WHERE company_id=$1 AND order_id=$2 AND status='active'`, w.p.CompanyID, order.ID) != 1 {
		t.Fatal("reserved quantity was not stored with the order")
	}
}

func TestS2_OverLimitHoldAndLoggedOverride(t *testing.T) {
	w := newWorld(t)
	w.customer("cust-1", "Al Noor", "Dubai", "100111111100003", "10.00", 30, 0, "0", "cust-v1", true, false)
	order, err := w.svc.CreateOrder(context.Background(), w.p, w.orderInput())
	if err != nil {
		t.Fatal(err)
	}
	if order.Status != StatusHeld || !has(order.Holds, HoldCredit) {
		t.Fatalf("status %s holds %v", order.Status, order.Holds)
	}
	if w.count(t, `SELECT count(*) FROM erp.sales_reservations WHERE company_id=$1 AND order_id=$2`, w.p.CompanyID, order.ID) != 0 {
		t.Fatal("held order reserved stock")
	}
	if _, err := w.svc.ReleaseHold(context.Background(), w.p, order.ID, HoldCredit, "limit lift", order.StateVersion); err == nil {
		t.Fatal("sales agent must not override a credit hold")
	}
	controller := w.p
	controller.Roles = []string{RoleCreditController}
	controller.UserID = "controller"
	released, err := w.svc.ReleaseHold(context.Background(), controller, order.ID, HoldCredit, "limit lift", order.StateVersion)
	if err != nil {
		t.Fatal(err)
	}
	if released.Status != StatusReserved {
		t.Fatalf("released status %s", released.Status)
	}
	if w.count(t, `SELECT count(*) FROM erp.sales_overrides WHERE company_id=$1 AND order_id=$2 AND hold=$3`, w.p.CompanyID, order.ID, HoldCredit) != 1 {
		t.Fatal("override was not recorded")
	}
	if w.count(t, `SELECT count(*) FROM erp.audit_events WHERE company_id=$1 AND event_type='sales.override' AND reference_id=$2`, w.p.CompanyID, order.ID) != 1 {
		t.Fatal("override was not audited")
	}
}

func TestS3_BelowFloorHoldAndLoggedOverride(t *testing.T) {
	w := newWorld(t)
	w.sku("SKU1", "Widget", "30.00", "VAT5", "tax-v1", "0.05")
	order, err := w.svc.CreateOrder(context.Background(), w.p, w.orderInput())
	if err != nil {
		t.Fatal(err)
	}
	if order.Status != StatusHeld || !has(order.Holds, HoldPriceFloor) {
		t.Fatalf("status %s holds %v", order.Status, order.Holds)
	}
	if w.count(t, `SELECT count(*) FROM erp.sales_reservations WHERE company_id=$1 AND order_id=$2`, w.p.CompanyID, order.ID) != 0 {
		t.Fatal("below-floor order reserved stock")
	}
	approver := w.p
	approver.Roles = []string{RolePriceApprover}
	approver.UserID = "price-approver"
	released, err := w.svc.ReleaseHold(context.Background(), approver, order.ID, HoldPriceFloor, "floor exception", order.StateVersion)
	if err != nil {
		t.Fatal(err)
	}
	if released.Status != StatusReserved {
		t.Fatalf("status %s", released.Status)
	}
	if w.count(t, `SELECT count(*) FROM erp.sales_overrides WHERE company_id=$1 AND order_id=$2`, w.p.CompanyID, order.ID) != 1 {
		t.Fatal("floor override was not reported")
	}
}

func TestS4_AgreementPrefillsPriceAndSuppressesHold(t *testing.T) {
	w := newWorld(t)
	w.sku("SKU1", "Widget", "30.00", "VAT5", "tax-v1", "0.05")
	w.exec(t, `INSERT INTO erp.sales_agreements (company_id, id, customer_id, sku, qty_min, price, valid_from, valid_to, version_id)
		VALUES ($1, 'agr-1', 'cust-1', 'SKU1', '1', '25.00', '2026-01-01', '2026-12-31', 'price-v1')`, w.p.CompanyID)
	in := w.orderInput()
	in.Lines[0].UnitPrice = "1.00"
	order, err := w.svc.CreateOrder(context.Background(), w.p, in)
	if err != nil {
		t.Fatal(err)
	}
	if order.Status != StatusReserved || len(order.Holds) != 0 {
		t.Fatalf("status %s holds %v", order.Status, order.Holds)
	}
	if len(order.Lines) != 1 || order.Lines[0].UnitPrice != "25.00" || order.Lines[0].AgreementID != "agr-1" {
		t.Fatalf("line %+v", order.Lines)
	}
}

func TestS5_OfflineOrderSyncsOnceAndStaysPending(t *testing.T) {
	w := newWorld(t)
	in := w.orderInput()
	in.Offline = true
	in.IdempotencyKey = uuid.NewString()
	first, err := w.svc.CreateOrder(context.Background(), w.p, in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := w.svc.CreateOrder(context.Background(), w.p, in)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == "" || first.ID != second.ID {
		t.Fatalf("ids %s %s", first.ID, second.ID)
	}
	if first.Status != StatusPendingReservation || !strings.Contains(first.AgentNotice, NoticePendingReservation) {
		t.Fatalf("status %s notice %q", first.Status, first.AgentNotice)
	}
	if w.count(t, `SELECT count(*) FROM erp.sales_orders WHERE company_id=$1`, w.p.CompanyID) != 1 {
		t.Fatal("idempotency key created more than one order")
	}
	if w.count(t, `SELECT count(*) FROM erp.sales_reservations WHERE company_id=$1`, w.p.CompanyID) != 0 {
		t.Fatal("offline order reserved stock")
	}
}

func TestS6_CancelReleasesReservation(t *testing.T) {
	w := newWorld(t)
	order, err := w.svc.CreateOrder(context.Background(), w.p, w.orderInput())
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := w.svc.CancelOrder(context.Background(), w.p, order.ID, order.StateVersion)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != StatusCancelled {
		t.Fatalf("status %s", cancelled.Status)
	}
	if w.count(t, `SELECT count(*) FROM erp.sales_reservations WHERE company_id=$1 AND order_id=$2 AND status='active'`, w.p.CompanyID, order.ID) != 0 {
		t.Fatal("active reservation remained after cancel")
	}
}

func TestS8_HeldOrderDoesNotReserveUntilRelease(t *testing.T) {
	w := newWorld(t)
	w.customer("cust-1", "Al Noor", "Dubai", "100111111100003", "10.00", 30, 0, "0", "cust-v1", true, false)
	order, err := w.svc.CreateOrder(context.Background(), w.p, w.orderInput())
	if err != nil {
		t.Fatal(err)
	}
	if w.count(t, `SELECT count(*) FROM erp.sales_reservations WHERE company_id=$1 AND order_id=$2 AND status='active'`, w.p.CompanyID, order.ID) != 0 {
		t.Fatal("held order reserved stock")
	}
	controller := w.p
	controller.Roles = []string{RoleCreditController}
	controller.UserID = "controller"
	if _, err := w.svc.ReleaseHold(context.Background(), controller, order.ID, HoldCredit, "released", order.StateVersion); err != nil {
		t.Fatal(err)
	}
	if w.count(t, `SELECT count(*) FROM erp.sales_reservations WHERE company_id=$1 AND order_id=$2 AND status='active'`, w.p.CompanyID, order.ID) != 1 {
		t.Fatal("release did not reserve stock")
	}
}

func TestS10_MissingArticle59FieldCannotBeSubmitted(t *testing.T) {
	w := newWorld(t)
	w.supplier("Acme Trading LLC", "Dubai", "")
	order, err := w.svc.CreateOrder(context.Background(), w.p, w.orderInput())
	if err != nil {
		t.Fatal(err)
	}
	inv, err := w.svc.DraftInvoice(context.Background(), w.p, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.svc.SubmitInvoice(context.Background(), w.p, inv.ID, inv.StateVersion)
	var ae *apierr.Error
	if !errors.As(err, &ae) || ae.Details["field"] != "supplier_trn" {
		t.Fatalf("submit err = %v", err)
	}
}

func has(holds []string, want string) bool {
	for _, h := range holds {
		if h == want {
			return true
		}
	}
	return false
}
