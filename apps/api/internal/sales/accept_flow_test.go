package sales

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

func (w *world) register(t *testing.T, in OrderInput) Invoice {
	t.Helper()
	order, err := w.svc.CreateOrder(context.Background(), w.p, in)
	if err != nil {
		t.Fatal(err)
	}
	if order.Status != StatusReserved {
		t.Fatalf("order status %s", order.Status)
	}
	draft, err := w.svc.DraftInvoice(context.Background(), w.p, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	submitted, err := w.svc.SubmitInvoice(context.Background(), w.p, draft.ID, draft.StateVersion)
	if err != nil {
		t.Fatal(err)
	}
	approved, err := w.svc.ApproveInvoice(context.Background(), w.p, submitted.ID, submitted.StateVersion)
	if err != nil {
		t.Fatal(err)
	}
	registered, err := w.svc.RegisterInvoice(context.Background(), w.p, approved.ID, approved.StateVersion)
	if err != nil {
		t.Fatal(err)
	}
	return registered
}

func TestS7_TimelineShowsDownstreamDocuments(t *testing.T) {
	w := newWorld(t)
	registered := w.register(t, w.orderInput())
	if _, err := w.svc.GatePass(context.Background(), w.p, GateInput{InvoiceID: registered.ID, At: dubaiMorning()}); err != nil {
		t.Fatal(err)
	}
	flow, err := w.svc.Timeline(context.Background(), w.p, registered.OrderID)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, doc := range flow {
		seen[doc.DocType] = true
	}
	for _, kind := range []string{"sales_order", "sales_invoice", "gate_pass", "delivery_note"} {
		if !seen[kind] {
			t.Fatalf("timeline missing %s in %+v", kind, flow)
		}
	}
}

func TestS9_DraftCopiesOrder(t *testing.T) {
	w := newWorld(t)
	order, err := w.svc.CreateOrder(context.Background(), w.p, w.orderInput())
	if err != nil {
		t.Fatal(err)
	}
	inv, err := w.svc.DraftInvoice(context.Background(), w.p, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if inv.DueDate != "2026-10-15" || inv.InvoiceDate != "2026-09-15" || inv.SupplyDate != "2026-09-15" {
		t.Fatalf("dates invoice %s supply %s due %s", inv.InvoiceDate, inv.SupplyDate, inv.DueDate)
	}
	if len(inv.Lines) != 1 || inv.Lines[0].UnitPrice != "20.00" || inv.Lines[0].TaxCode != "VAT5" {
		t.Fatalf("line %+v", inv.Lines)
	}
	if inv.PrintEnabled || inv.GatePassEnabled {
		t.Fatal("print and gate pass must stay disabled on a draft")
	}
}

func TestS11_RegistrationPostsAfterArticle59Rejection(t *testing.T) {
	w := newWorld(t)
	w.supplier("Acme Trading LLC", "Dubai", "")
	order, err := w.svc.CreateOrder(context.Background(), w.p, w.orderInput())
	if err != nil {
		t.Fatal(err)
	}
	draft, err := w.svc.DraftInvoice(context.Background(), w.p, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.svc.SubmitInvoice(context.Background(), w.p, draft.ID, draft.StateVersion)
	var ae *apierr.Error
	if !errors.As(err, &ae) || ae.Details["field"] != "supplier_trn" {
		t.Fatalf("rejection before happy path: %v", err)
	}
	w.supplier("Acme Trading LLC", "Dubai", "100234567800003")
	submitted, err := w.svc.SubmitInvoice(context.Background(), w.p, draft.ID, draft.StateVersion)
	if err != nil {
		t.Fatal(err)
	}
	approved, err := w.svc.ApproveInvoice(context.Background(), w.p, submitted.ID, submitted.StateVersion)
	if err != nil {
		t.Fatal(err)
	}
	registered, err := w.svc.RegisterInvoice(context.Background(), w.p, approved.ID, approved.StateVersion)
	if err != nil {
		t.Fatal(err)
	}
	if registered.Status != StatusRegistered || !registered.PrintEnabled || !registered.GatePassEnabled {
		t.Fatalf("registered %+v", registered.PrintEnabled)
	}
	got := w.roles(t, registered.ID)
	for _, role := range []string{"receivable", "revenue", "vat_output", "discount", "rounding"} {
		if _, ok := got[role]; !ok {
			t.Fatalf("missing posting role %s in %v", role, got)
		}
	}
	if got["receivable"].Debit != "42.00" || got["revenue"].Credit != "40.00" || got["vat_output"].Credit != "2.00" {
		t.Fatalf("amounts %+v", got)
	}
}

func TestS12_ReprintIsByteIdentical(t *testing.T) {
	w := newWorld(t)
	draftOrder, err := w.svc.CreateOrder(context.Background(), w.p, w.orderInput())
	if err != nil {
		t.Fatal(err)
	}
	draft, err := w.svc.DraftInvoice(context.Background(), w.p, draftOrder.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.PrintInvoice(context.Background(), w.p, draft.ID); err == nil {
		t.Fatal("print must fail before registration")
	}
	registered := w.register(t, w.orderInput())
	first, err := w.svc.PrintInvoice(context.Background(), w.p, registered.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := w.svc.PrintInvoice(context.Background(), w.p, registered.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Body, second.Body) || first.Hash == "" || first.Hash != second.Hash || first.Hash != registered.PDFHash {
		t.Fatal("reprint was not the stored PDF")
	}
}

func TestS13_GatePassDisabledUntilRegistration(t *testing.T) {
	w := newWorld(t)
	order, err := w.svc.CreateOrder(context.Background(), w.p, w.orderInput())
	if err != nil {
		t.Fatal(err)
	}
	draft, err := w.svc.DraftInvoice(context.Background(), w.p, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.GatePass(context.Background(), w.p, GateInput{InvoiceID: draft.ID, At: dubaiMorning()}); err == nil {
		t.Fatal("gate pass must fail before registration")
	}
	registered := w.register(t, w.orderInput())
	if !registered.PrintEnabled || !registered.GatePassEnabled {
		t.Fatal("registration did not enable print and gate pass")
	}
}

func TestS14_ChangedInvoiceFailsRegistration(t *testing.T) {
	w := newWorld(t)
	order, err := w.svc.CreateOrder(context.Background(), w.p, w.orderInput())
	if err != nil {
		t.Fatal(err)
	}
	draft, err := w.svc.DraftInvoice(context.Background(), w.p, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	submitted, err := w.svc.SubmitInvoice(context.Background(), w.p, draft.ID, draft.StateVersion)
	if err != nil {
		t.Fatal(err)
	}
	approved, err := w.svc.ApproveInvoice(context.Background(), w.p, submitted.ID, submitted.StateVersion)
	if err != nil {
		t.Fatal(err)
	}
	edited, err := w.svc.UpdateInvoice(context.Background(), w.p, approved.ID, approved.StateVersion, InvoicePatch{Lines: []LineInput{{SKU: "SKU1", UnitPrice: "21.00"}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.svc.RegisterInvoice(context.Background(), w.p, edited.ID, edited.StateVersion)
	if err == nil {
		t.Fatal("registration must fail when the posted hash differs from the approved hash")
	}
	if w.count(t, `SELECT count(*) FROM erp.sales_postings WHERE company_id=$1`, w.p.CompanyID) != 0 {
		t.Fatal("failed registration posted")
	}
	if w.scalar(t, `SELECT number FROM erp.sales_invoices WHERE company_id=$1 AND id=$2`, w.p.CompanyID, edited.ID) != "" {
		t.Fatal("failed registration allocated a number")
	}
}

func TestS15_InvoiceNumbersAreGapFree(t *testing.T) {
	w := newWorld(t)
	first := w.register(t, w.orderInput())
	order, err := w.svc.CreateOrder(context.Background(), w.p, w.orderInput())
	if err != nil {
		t.Fatal(err)
	}
	draft, err := w.svc.DraftInvoice(context.Background(), w.p, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.RegisterInvoice(context.Background(), w.p, draft.ID, draft.StateVersion); err == nil {
		t.Fatal("unapproved invoice must not take a number")
	}
	second := w.register(t, w.orderInput())
	if first.Number != "INV-2026-000001" || second.Number != "INV-2026-000002" {
		t.Fatalf("numbers %s %s", first.Number, second.Number)
	}
}

func TestS16_RegistrationStoresVersionsInForce(t *testing.T) {
	w := newWorld(t)
	order, err := w.svc.CreateOrder(context.Background(), w.p, w.orderInput())
	if err != nil {
		t.Fatal(err)
	}
	draft, err := w.svc.DraftInvoice(context.Background(), w.p, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	w.customer("cust-1", "Al Noor", "Dubai", "100111111100003", "1000.00", 30, 0, "0", "cust-v2", true, false)
	w.sku("SKU1", "Widget", "10.00", "VAT5", "tax-v2", "0.05")
	submitted, err := w.svc.SubmitInvoice(context.Background(), w.p, draft.ID, draft.StateVersion)
	if err != nil {
		t.Fatal(err)
	}
	approved, err := w.svc.ApproveInvoice(context.Background(), w.p, submitted.ID, submitted.StateVersion)
	if err != nil {
		t.Fatal(err)
	}
	registered, err := w.svc.RegisterInvoice(context.Background(), w.p, approved.ID, approved.StateVersion)
	if err != nil {
		t.Fatal(err)
	}
	if registered.CustomerVersionID != "cust-v2" || registered.TaxCodeVersionID != "tax-v2" || registered.PriceVersionID != "list" {
		t.Fatalf("versions customer %s tax %s price %s", registered.CustomerVersionID, registered.TaxCodeVersionID, registered.PriceVersionID)
	}
}

func TestS17_GatePassPostsMovingAverageCOGS(t *testing.T) {
	w := newWorld(t)
	registered := w.register(t, w.orderInput())
	delivery, err := w.svc.GatePass(context.Background(), w.p, GateInput{InvoiceID: registered.ID, At: dubaiMorning()})
	if err != nil {
		t.Fatal(err)
	}
	if delivery.GatePassID == "" || delivery.ID == "" {
		t.Fatal("gate pass did not create a delivery note")
	}
	got := w.roles(t, delivery.ID)
	if got["cogs"].Debit != "10.00" || got["inventory"].Credit != "10.00" {
		t.Fatalf("cogs %+v", got)
	}
	if len(delivery.Lines) != 1 || delivery.Lines[0].UnitCost != "5.00" {
		t.Fatalf("lines %+v", delivery.Lines)
	}
}

func TestS18_PartialDeliveryAndBackorder(t *testing.T) {
	w := newWorld(t)
	in := w.orderInput()
	in.Lines[0].Qty = "4"
	registered := w.register(t, in)
	delivery, err := w.svc.GatePass(context.Background(), w.p, GateInput{
		InvoiceID: registered.ID, At: dubaiMorning(),
		Lines: []LineInput{{SKU: "SKU1", Qty: "1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := w.roles(t, delivery.ID)
	if got["cogs"].Debit != "5.00" {
		t.Fatalf("partial cogs %+v", got)
	}
	if len(delivery.Backorder) != 1 || delivery.Backorder[0].Qty != "3" {
		t.Fatalf("backorder %+v", delivery.Backorder)
	}
}

func TestS19_DeliveryClockUsesBusinessHoursAndEscalates(t *testing.T) {
	w := newWorld(t)
	registered := w.register(t, w.orderInput())
	delivery, err := w.svc.GatePass(context.Background(), w.p, GateInput{InvoiceID: registered.ID, At: dubaiMorning()})
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 23, 16, 0, 0, 0, dubai()).UTC()
	if !delivery.ClockDue.Equal(want) {
		t.Fatalf("due %s want %s", delivery.ClockDue, want)
	}
	n, err := w.svc.EscalateClocks(context.Background(), w.p, want.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("escalated %d", n)
	}
	if w.count(t, `SELECT count(*) FROM erp.sales_events WHERE company_id=$1 AND type='clock.expired'`, w.p.CompanyID) != 1 {
		t.Fatal("missed clock did not escalate")
	}
}

func TestS20_ProofClosesClockAndEntersTheChain(t *testing.T) {
	w := newWorld(t)
	registered := w.register(t, w.orderInput())
	delivery, err := w.svc.GatePass(context.Background(), w.p, GateInput{InvoiceID: registered.ID, At: dubaiMorning()})
	if err != nil {
		t.Fatal(err)
	}
	proved, err := w.svc.ProveDelivery(context.Background(), w.p, delivery.ID, Proof{
		Signature: "signed", PhotoSHA: "abc", At: dubaiMorning().Add(time.Hour), Lat: "25.2", Lng: "55.2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !proved.ClockClosed || len(proved.ProofHash) != 64 {
		t.Fatalf("closed %v hash %s", proved.ClockClosed, proved.ProofHash)
	}
	if w.count(t, `SELECT count(*) FROM erp.audit_events WHERE company_id=$1 AND reference_id=$2 AND length(hash)=64`, w.p.CompanyID, delivery.ID) != 1 {
		t.Fatal("proof was not hashed into the audit chain")
	}
	n, err := w.svc.EscalateClocks(context.Background(), w.p, dubaiMorning().Add(72*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("closed clock escalated")
	}
}

func TestS21_DeliveryCannotExceedReservedQuantity(t *testing.T) {
	w := newWorld(t)
	registered := w.register(t, w.orderInput())
	_, err := w.svc.GatePass(context.Background(), w.p, GateInput{
		InvoiceID: registered.ID, At: dubaiMorning(),
		Lines: []LineInput{{SKU: "SKU1", Qty: "9"}},
	})
	if err == nil {
		t.Fatal("delivery exceeded the reservation")
	}
	if w.count(t, `SELECT count(*) FROM erp.sales_deliveries WHERE company_id=$1`, w.p.CompanyID) != 0 {
		t.Fatal("over-delivery created a note")
	}
}

func TestS22_DeliveryConfirmationIsImmediate(t *testing.T) {
	w := newWorld(t)
	registered := w.register(t, w.orderInput())
	delivery, err := w.svc.GatePass(context.Background(), w.p, GateInput{InvoiceID: registered.ID, At: dubaiMorning()})
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now().UTC()
	if _, err := w.svc.ProveDelivery(context.Background(), w.p, delivery.ID, Proof{Signature: "s", PhotoSHA: "p", At: before, Lat: "1", Lng: "2"}); err != nil {
		t.Fatal(err)
	}
	var at time.Time
	if err := rls.Tx(context.Background(), w.pool, w.p, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT occurred_at FROM erp.sales_events WHERE company_id=$1 AND type='delivery.confirmed'`, w.p.CompanyID).Scan(&at)
	}); err != nil {
		t.Fatal(err)
	}
	if at.Before(before.Add(-time.Second)) || time.Since(at) > time.Second {
		t.Fatalf("confirmation at %s", at)
	}
}

func TestS23_CreditNoteReversesWithoutEditingTheInvoice(t *testing.T) {
	w := newWorld(t)
	registered := w.register(t, w.orderInput())
	before := w.scalar(t, `SELECT total FROM erp.sales_invoices WHERE company_id=$1 AND id=$2`, w.p.CompanyID, registered.ID)
	note, err := w.svc.CreditNote(context.Background(), w.p, CreditInput{
		InvoiceID: registered.ID, Reason: "return", Lines: []LineInput{{SKU: "SKU1", Qty: "2"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if note.OriginalInvoiceID != registered.ID {
		t.Fatalf("original %s", note.OriginalInvoiceID)
	}
	after := w.scalar(t, `SELECT total FROM erp.sales_invoices WHERE company_id=$1 AND id=$2`, w.p.CompanyID, registered.ID)
	if before != after {
		t.Fatalf("invoice total changed from %s to %s", before, after)
	}
	got := w.roles(t, note.ID)
	if got["revenue"].Debit != "40.00" || got["vat_output"].Debit != "2.00" || got["receivable"].Credit != "42.00" {
		t.Fatalf("reversal %+v", got)
	}
}

func TestS24_CashSalePostsInvoiceAndReceiptTogether(t *testing.T) {
	w := newWorld(t)
	w.customer("cash", "Walk In", "Counter", "", "0.00", 0, 0, "0", "cash-v1", false, true)
	w.sku("SKU1", "Widget", "10.00", "VAT5", "tax-v1", "0")
	sale, err := w.svc.CashSale(context.Background(), w.p, CashInput{
		CustomerID: "cash", WarehouseID: "WH", AsOf: time.Date(2026, 9, 15, 9, 0, 0, 0, time.UTC),
		Lines: []LineInput{{SKU: "SKU1", Qty: "2", UnitPrice: "20.00"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if sale.InvoiceID == "" || sale.ReceiptID == "" || sale.InvoiceNumber == "" {
		t.Fatalf("sale %+v", sale)
	}
	if _, ok := w.roles(t, sale.InvoiceID)["receivable"]; !ok {
		t.Fatal("cash invoice did not post receivable")
	}
	if w.roles(t, sale.ReceiptID)["cash"].Debit != "40.00" {
		t.Fatalf("receipt %+v", w.roles(t, sale.ReceiptID))
	}
}

func TestS24_ReceiptFailureRollsBackTheInvoice(t *testing.T) {
	w := newWorld(t)
	w.customer("cash", "Walk In", "Counter", "", "0.00", 0, 0, "0", "cash-v1", false, true)
	w.sku("SKU1", "Widget", "10.00", "VAT5", "tax-v1", "0")
	led := &failSecondPost{}
	w.svc = New(w.pool, WithLedger(led))
	_, err := w.svc.CashSale(context.Background(), w.p, CashInput{
		CustomerID: "cash", WarehouseID: "WH", AsOf: time.Date(2026, 9, 15, 9, 0, 0, 0, time.UTC),
		Lines: []LineInput{{SKU: "SKU1", Qty: "1", UnitPrice: "20.00"}},
	})
	if led.n < 2 {
		t.Fatal("cash sale must post the invoice and the receipt")
	}
	if err == nil {
		t.Fatal("receipt failure must fail the cash sale")
	}
	if w.count(t, `SELECT count(*) FROM erp.sales_invoices WHERE company_id=$1`, w.p.CompanyID) != 0 {
		t.Fatal("receipt failure kept the invoice")
	}
}

type failSecondPost struct{ n int }

func (f *failSecondPost) Post(context.Context, pgx.Tx, uuid.UUID, Journal) error {
	f.n++
	if f.n > 1 {
		return errors.New("second posting failed")
	}
	return nil
}

func TestS25_AttainmentUsesRegisteredInvoices(t *testing.T) {
	w := newWorld(t)
	w.sku("SKU1", "Widget", "10.00", "VAT5", "tax-v1", "0")
	w.exec(t, `INSERT INTO erp.sales_targets (company_id, agent_id, period, target_amount, commission_rate)
		VALUES ($1,$2,'2026-09','1000.00','0.10')`, w.p.CompanyID, w.p.UserID)
	_ = w.register(t, w.orderInput())
	_ = w.register(t, w.orderInput())
	order, err := w.svc.CreateOrder(context.Background(), w.p, w.orderInput())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.DraftInvoice(context.Background(), w.p, order.ID); err != nil {
		t.Fatal(err)
	}
	got, err := w.svc.Attainment(context.Background(), w.p, w.p.UserID, "2026-09")
	if err != nil {
		t.Fatal(err)
	}
	if got != "80.00" {
		t.Fatalf("attainment %s", got)
	}
}

func TestS26_CommissionUsesCollectedInvoicesOnly(t *testing.T) {
	w := newWorld(t)
	w.sku("SKU1", "Widget", "10.00", "VAT5", "tax-v1", "0")
	w.exec(t, `INSERT INTO erp.sales_targets (company_id, agent_id, period, target_amount, commission_rate)
		VALUES ($1,$2,'2026-09','1000.00','0.10')`, w.p.CompanyID, w.p.UserID)
	collected := w.register(t, w.orderInput())
	_ = w.register(t, w.orderInput())
	if _, err := w.svc.Receipt(context.Background(), w.p, ReceiptInput{InvoiceID: collected.ID, Amount: "40.00", PaidOn: time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	got, err := w.svc.Commission(context.Background(), w.p, w.p.UserID, "2026-09")
	if err != nil {
		t.Fatal(err)
	}
	if got != "4.00" {
		t.Fatalf("commission %s", got)
	}
}

func TestS27_ReturnRestoresOriginalDeliveredCost(t *testing.T) {
	w := newWorld(t)
	w.sku("SKU1", "Widget", "10.00", "VAT5", "tax-v1", "0")
	registered := w.register(t, w.orderInput())
	if _, err := w.svc.GatePass(context.Background(), w.p, GateInput{InvoiceID: registered.ID, At: dubaiMorning()}); err != nil {
		t.Fatal(err)
	}
	w.stock("WH", "98", "980.00")
	note, err := w.svc.CreditNote(context.Background(), w.p, CreditInput{
		InvoiceID: registered.ID, RestoreStock: true, Reason: "return",
		Lines: []LineInput{{SKU: "SKU1", Qty: "2"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if w.scalar(t, `SELECT value FROM erp.sales_stock WHERE company_id=$1 AND sku='SKU1' AND warehouse_id='WH'`, w.p.CompanyID) != "990.00" {
		t.Fatalf("stock value %s", w.scalar(t, `SELECT value FROM erp.sales_stock WHERE company_id=$1 AND sku='SKU1' AND warehouse_id='WH'`, w.p.CompanyID))
	}
	if w.roles(t, note.ID)["inventory"].Debit != "10.00" || w.roles(t, note.ID)["cogs"].Credit != "10.00" {
		t.Fatalf("cogs reversal %+v", w.roles(t, note.ID))
	}
}

func TestD23_ToleranceBlocksRegistrationOnly(t *testing.T) {
	w := newWorld(t)
	w.exec(t, `INSERT INTO erp.sales_tolerances (company_id, user_id, max_amount) VALUES ($1,$2,'10.00')`, w.p.CompanyID, w.p.UserID)
	order, err := w.svc.CreateOrder(context.Background(), w.p, w.orderInput())
	if err != nil {
		t.Fatal(err)
	}
	draft, err := w.svc.DraftInvoice(context.Background(), w.p, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if draft.Status != StatusDraft {
		t.Fatal("tolerance blocked the draft")
	}
	submitted, err := w.svc.SubmitInvoice(context.Background(), w.p, draft.ID, draft.StateVersion)
	if err != nil {
		t.Fatal(err)
	}
	approved, err := w.svc.ApproveInvoice(context.Background(), w.p, submitted.ID, submitted.StateVersion)
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.svc.RegisterInvoice(context.Background(), w.p, approved.ID, approved.StateVersion)
	var ae *apierr.Error
	if !errors.As(err, &ae) || ae.Code != apierr.PermissionDenied {
		t.Fatalf("register err = %v", err)
	}
}

func TestD24_BlockingRuleNamesItselfAndAdvisoryWarns(t *testing.T) {
	w := newWorld(t)
	w.exec(t, `INSERT INTO erp.sales_rules (company_id, id, doc_type, name, mode, field, op, value)
		VALUES ($1,'r1','sales_invoice','block_total','blocking','total','max','1.00')`, w.p.CompanyID)
	order, err := w.svc.CreateOrder(context.Background(), w.p, w.orderInput())
	if err != nil {
		t.Fatal(err)
	}
	draft, err := w.svc.DraftInvoice(context.Background(), w.p, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.svc.SubmitInvoice(context.Background(), w.p, draft.ID, draft.StateVersion)
	var ae *apierr.Error
	if !errors.As(err, &ae) || ae.Details["rule"] != "block_total" {
		t.Fatalf("blocking err = %v", err)
	}

	w2 := newWorld(t)
	w2.exec(t, `INSERT INTO erp.sales_rules (company_id, id, doc_type, name, mode, field, op, value)
		VALUES ($1,'r2','sales_invoice','warn_total','advisory','total','max','1.00')`, w2.p.CompanyID)
	order2, err := w2.svc.CreateOrder(context.Background(), w2.p, w2.orderInput())
	if err != nil {
		t.Fatal(err)
	}
	draft2, err := w2.svc.DraftInvoice(context.Background(), w2.p, order2.ID)
	if err != nil {
		t.Fatal(err)
	}
	submitted, err := w2.svc.SubmitInvoice(context.Background(), w2.p, draft2.ID, draft2.StateVersion)
	if err != nil {
		t.Fatal(err)
	}
	if submitted.Status != StatusSubmitted || !has(submitted.Warnings, "warn_total") {
		t.Fatalf("status %s warnings %v", submitted.Status, submitted.Warnings)
	}
}

func TestD25_EarlyPaymentDiscountInsideTheWindowOnly(t *testing.T) {
	w := newWorld(t)
	w.customer("cust-1", "Al Noor", "Dubai", "100111111100003", "1000.00", 30, 10, "0.02", "cust-v1", true, false)
	w.sku("SKU1", "Widget", "10.00", "VAT5", "tax-v1", "0")
	in := w.orderInput()
	in.AsOf = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	in.Lines[0].Qty = "1"
	in.Lines[0].UnitPrice = "100.00"
	inside := w.register(t, in)
	receipt, err := w.svc.Receipt(context.Background(), w.p, ReceiptInput{InvoiceID: inside.ID, Amount: "98.00", PaidOn: time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Discount != "2.00" {
		t.Fatalf("discount %s", receipt.Discount)
	}
	if w.roles(t, receipt.ID)["discount"].Debit != "2.00" {
		t.Fatalf("discount posting %+v", w.roles(t, receipt.ID))
	}

	outsideInv := w.register(t, in)
	outside, err := w.svc.Receipt(context.Background(), w.p, ReceiptInput{InvoiceID: outsideInv.ID, Amount: "100.00", PaidOn: time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if outside.Discount != "0.00" {
		t.Fatalf("outside discount %s", outside.Discount)
	}
	if _, ok := w.roles(t, outside.ID)["discount"]; ok {
		t.Fatal("discount posted outside the window")
	}
}

func dubai() *time.Location {
	loc, err := time.LoadLocation("Asia/Dubai")
	if err != nil {
		panic(err)
	}
	return loc
}

func dubaiMorning() time.Time {
	return time.Date(2026, 9, 21, 8, 0, 0, 0, dubai())
}
