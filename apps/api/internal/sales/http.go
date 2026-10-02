package sales

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/httpx"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/idempotency"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/ifmatch"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// Mount registers the sales HTTP API on the /api/v1 router.
func Mount(r chi.Router, deps httpx.Deps) {
	h := &handler{svc: New(deps.Pool)}
	idem := idempotency.Middleware(deps.Pool)
	r.With(idem).Post("/sales-orders", h.createOrder)
	r.Get("/sales-orders/{id}", h.getOrder)
	r.With(idem).Post("/sales-orders/{id}/release", h.release)
	r.With(idem).Post("/sales-orders/{id}/cancel", h.cancel)
	r.Get("/sales-orders/{id}/timeline", h.timeline)
	r.With(idem).Post("/sales-invoices", h.draftInvoice)
	r.Get("/sales-invoices/{id}", h.getInvoice)
	r.With(idem).Put("/sales-invoices/{id}", h.updateInvoice)
	r.With(idem).Post("/sales-invoices/{id}/submit", h.submit)
	r.With(idem).Post("/sales-invoices/{id}/approve", h.approve)
	r.With(idem).Post("/sales-invoices/{id}/register", h.register)
	r.Get("/sales-invoices/{id}/print", h.printInvoice)
	r.With(idem).Post("/gate-passes", h.gate)
	r.With(idem).Post("/delivery-notes/{id}/proof", h.proof)
	r.With(idem).Post("/credit-notes", h.credit)
	r.With(idem).Post("/cash-sales", h.cash)
	r.With(idem).Post("/receipts", h.receipt)
	r.Get("/sales-targets", h.attainment)
	r.Get("/sales-commission", h.commission)
}

type handler struct{ svc *Service }

func (h *handler) principal(w http.ResponseWriter, r *http.Request) (rls.Principal, bool) {
	p, err := rls.FromContext(r.Context())
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.AuthRequired, "authentication required"))
		return rls.Principal{}, false
	}
	return p, true
}

func (h *handler) version(w http.ResponseWriter, r *http.Request) (int64, bool) {
	v, _, err := ifmatch.Parse(r, true)
	if err != nil {
		apierr.Write(w, r, err)
		return 0, false
	}
	return v, true
}

func (h *handler) createOrder(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	var body struct {
		CustomerID  string      `json:"customer_id"`
		WarehouseID string      `json:"warehouse_id"`
		Currency    string      `json:"currency"`
		Offline     bool        `json:"offline"`
		AsOf        time.Time   `json:"as_of"`
		Lines       []LineInput `json:"lines"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	order, err := h.svc.CreateOrder(r.Context(), p, OrderInput{
		CustomerID: body.CustomerID, WarehouseID: body.WarehouseID, Currency: body.Currency,
		Offline: body.Offline, AsOf: body.AsOf, IdempotencyKey: r.Header.Get(idempotency.Header), Lines: body.Lines,
	})
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	if containsHold(order.Holds, HoldCredit) {
		apierr.Write(w, r, apierr.New(apierr.CreditHold, "order held for the credit controller").WithDetails(map[string]any{"order_id": order.ID, "holds": order.Holds}))
		return
	}
	if containsHold(order.Holds, HoldPriceFloor) {
		apierr.Write(w, r, apierr.New(apierr.PriceFloorHold, "order held for a price floor override").WithDetails(map[string]any{"order_id": order.ID, "holds": order.Holds}))
		return
	}
	httpx.JSON(w, r, http.StatusCreated, order)
}

func (h *handler) getOrder(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	order, err := h.svc.load(r.Context(), p, chi.URLParam(r, "id"))
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, order)
}

func (h *handler) release(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	version, ok := h.version(w, r)
	if !ok {
		return
	}
	var body struct {
		Hold   string `json:"hold"`
		Reason string `json:"reason"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	order, err := h.svc.ReleaseHold(r.Context(), p, chi.URLParam(r, "id"), body.Hold, body.Reason, version)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, order)
}

func (h *handler) cancel(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	version, ok := h.version(w, r)
	if !ok {
		return
	}
	order, err := h.svc.CancelOrder(r.Context(), p, chi.URLParam(r, "id"), version)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, order)
}

func (h *handler) timeline(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	docs, err := h.svc.Timeline(r.Context(), p, chi.URLParam(r, "id"))
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, docs)
}

func (h *handler) draftInvoice(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	var body struct {
		OrderID string `json:"order_id"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	inv, err := h.svc.DraftInvoice(r.Context(), p, body.OrderID)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusCreated, inv)
}

func (h *handler) getInvoice(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	inv, err := h.svc.loadInv(r.Context(), p, chi.URLParam(r, "id"))
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, inv)
}

func (h *handler) updateInvoice(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	version, ok := h.version(w, r)
	if !ok {
		return
	}
	var body struct {
		Lines []LineInput `json:"lines"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	inv, err := h.svc.UpdateInvoice(r.Context(), p, chi.URLParam(r, "id"), version, InvoicePatch{Lines: body.Lines})
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, inv)
}

func (h *handler) submit(w http.ResponseWriter, r *http.Request) {
	h.act(w, r, h.svc.SubmitInvoice)
}

func (h *handler) approve(w http.ResponseWriter, r *http.Request) {
	h.act(w, r, h.svc.ApproveInvoice)
}

func (h *handler) register(w http.ResponseWriter, r *http.Request) {
	h.act(w, r, h.svc.RegisterInvoice)
}

func (h *handler) act(w http.ResponseWriter, r *http.Request, fn func(ctx context.Context, p rls.Principal, id string, version int64) (Invoice, error)) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	version, ok := h.version(w, r)
	if !ok {
		return
	}
	inv, err := fn(r.Context(), p, chi.URLParam(r, "id"), version)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, inv)
}

func (h *handler) printInvoice(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	out, err := h.svc.PrintInvoice(r.Context(), p, chi.URLParam(r, "id"))
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("X-Content-SHA256", out.Hash)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out.Body)
}

func (h *handler) gate(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	var body struct {
		InvoiceID string      `json:"invoice_id"`
		At        time.Time   `json:"at"`
		Lines     []LineInput `json:"lines"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	out, err := h.svc.GatePass(r.Context(), p, GateInput{InvoiceID: body.InvoiceID, At: body.At, Lines: body.Lines})
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusCreated, out)
}

func (h *handler) proof(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	var body struct {
		Signature string    `json:"signature"`
		PhotoSHA  string    `json:"photo_sha"`
		At        time.Time `json:"at"`
		Lat       string    `json:"lat"`
		Lng       string    `json:"lng"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	out, err := h.svc.ProveDelivery(r.Context(), p, chi.URLParam(r, "id"), Proof{Signature: body.Signature, PhotoSHA: body.PhotoSHA, At: body.At, Lat: body.Lat, Lng: body.Lng})
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

func (h *handler) credit(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	var body struct {
		InvoiceID    string      `json:"invoice_id"`
		RestoreStock bool        `json:"restore_stock"`
		Reason       string      `json:"reason"`
		Lines        []LineInput `json:"lines"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	out, err := h.svc.CreditNote(r.Context(), p, CreditInput{InvoiceID: body.InvoiceID, RestoreStock: body.RestoreStock, Reason: body.Reason, Lines: body.Lines})
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusCreated, out)
}

func (h *handler) cash(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	var body struct {
		CustomerID  string      `json:"customer_id"`
		WarehouseID string      `json:"warehouse_id"`
		AsOf        time.Time   `json:"as_of"`
		Lines       []LineInput `json:"lines"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	out, err := h.svc.CashSale(r.Context(), p, CashInput{CustomerID: body.CustomerID, WarehouseID: body.WarehouseID, AsOf: body.AsOf, Lines: body.Lines})
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusCreated, out)
}

func (h *handler) receipt(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	var body struct {
		InvoiceID string    `json:"invoice_id"`
		Amount    string    `json:"amount"`
		PaidOn    time.Time `json:"paid_on"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apierr.Write(w, r, err)
		return
	}
	out, err := h.svc.Receipt(r.Context(), p, ReceiptInput{InvoiceID: body.InvoiceID, Amount: body.Amount, PaidOn: body.PaidOn})
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusCreated, out)
}

func (h *handler) attainment(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	got, err := h.svc.Attainment(r.Context(), p, r.URL.Query().Get("agent"), r.URL.Query().Get("period"))
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]string{"attainment": got})
}

func (h *handler) commission(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	got, err := h.svc.Commission(r.Context(), p, r.URL.Query().Get("agent"), r.URL.Query().Get("period"))
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]string{"commission": got})
}
