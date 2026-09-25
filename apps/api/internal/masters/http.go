package masters

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/httpx"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/idempotency"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/ifmatch"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/outbox"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// Mount registers master-data routes on the /api/v1 router.
func Mount(r chi.Router, deps httpx.Deps) {
	jobs := deps.River
	if jobs == nil && deps.Pool != nil {
		log := deps.Log
		if log == nil {
			log = slog.Default()
		}
		client, err := outbox.NewClient(deps.Pool, nil, log)
		if err == nil {
			jobs = client
		}
	}
	h := handler{svc: New(deps.Pool, jobs)}
	idem := idempotency.Middleware(deps.Pool)
	post := func(pattern string, fn http.HandlerFunc) {
		r.With(idem).Post(pattern, fn)
	}
	post("/customers", h.createCustomer)
	post("/customers/{id}/changes", h.changeCustomer)
	r.Get("/customers/{id}", h.getCustomer)
	post("/vendors", h.createVendor)
	post("/vendors/{id}/bank-accounts", h.changeBank)
	post("/vendors/{id}/approved-skus", h.addSKU)
	post("/vendors/{id}/blacklist", h.blacklist)
	r.Get("/vendors/best-price", h.bestPrice)
	r.Get("/vendors/{id}", h.getVendor)
	post("/skus", h.createSKU)
	post("/skus/{id}/changes", h.changeSKU)
	r.Get("/skus/{id}", h.getSKU)
	r.Get("/skus/{id}/convert", h.convert)
	post("/price-agreements", h.createAgreement)
	post("/boms", h.createBOM)
	post("/boms/{id}/changes", h.changeBOM)
	r.Get("/boms/{id}/versions", h.bomVersions)
	post("/master-approvals/{request_id}", h.decide)
	post("/executive-titles", h.grantTitle)
	post("/document-versions", h.pin)
	r.Get("/document-versions/{doc_type}/{doc_id}", h.document)
	post("/sales-orders", h.createOrder)
	post("/sales-orders/{id}/credit-override", h.creditOverride)
	post("/sales-orders/{id}/floor-override", h.floorOverride)
	r.Get("/master-overrides", h.overrides)
	post("/warehouses", h.warehouse)
	post("/stock/receipts", h.receive)
	post("/stock/issues", h.issue)
	post("/stock/reservations", h.reserve)
	r.Get("/stock/availability", h.availability)
	post("/production-entries", h.produce)
	r.Get("/production-entries/{id}", h.production)
	post("/purchase-documents", h.purchase)
	r.Get("/purchase-documents/{id}", h.purchaseDoc)
	post("/vendor-payments", h.pay)
	post("/vendor-payments/{id}/release", h.release)
}

type handler struct{ svc *Service }

type produceIn struct {
	BOMID string `json:"bom_id"`
	Qty   string `json:"qty"`
}

type decideIn struct {
	Decision    string `json:"decision"`
	Reason      string `json:"reason"`
	StepUpToken string `json:"step_up_token"`
	ToUserID    string `json:"to_user_id"`
}

type titleIn struct {
	UserID string `json:"user_id"`
	Title  string `json:"title"`
}

func (h handler) createCustomer(w http.ResponseWriter, r *http.Request) {
	p, match, ok := prep(w, r)
	if !ok {
		return
	}
	var in customerIn
	if !bind(w, r, &in) {
		return
	}
	h.send(w, r, http.StatusCreated)(h.svc.CreateCustomer(r.Context(), p, in, match))
}

func (h handler) changeCustomer(w http.ResponseWriter, r *http.Request) {
	p, id, match, ok := prepID(w, r)
	if !ok {
		return
	}
	var in customerIn
	if !bind(w, r, &in) {
		return
	}
	h.send(w, r, http.StatusAccepted)(h.svc.ChangeCustomer(r.Context(), p, id, in, match))
}

func (h handler) getCustomer(w http.ResponseWriter, r *http.Request) {
	p, id, ok := readID(w, r)
	if !ok {
		return
	}
	h.send(w, r, http.StatusOK)(h.svc.Customer(r.Context(), p, id))
}

func (h handler) createVendor(w http.ResponseWriter, r *http.Request) {
	p, match, ok := prep(w, r)
	if !ok {
		return
	}
	var in vendorIn
	if !bind(w, r, &in) {
		return
	}
	h.send(w, r, http.StatusCreated)(h.svc.CreateVendor(r.Context(), p, in, match))
}

func (h handler) changeBank(w http.ResponseWriter, r *http.Request) {
	p, id, match, ok := prepID(w, r)
	if !ok {
		return
	}
	var in bankIn
	if !bind(w, r, &in) {
		return
	}
	h.send(w, r, http.StatusAccepted)(h.svc.ChangeBank(r.Context(), p, id, in, match))
}

func (h handler) addSKU(w http.ResponseWriter, r *http.Request) {
	p, id, match, ok := prepID(w, r)
	if !ok {
		return
	}
	var in skuApprovalIn
	if !bind(w, r, &in) {
		return
	}
	h.send(w, r, http.StatusAccepted)(h.svc.AddVendorSKU(r.Context(), p, id, in, match))
}

func (h handler) blacklist(w http.ResponseWriter, r *http.Request) {
	p, id, match, ok := prepID(w, r)
	if !ok {
		return
	}
	var in reasonIn
	if !bind(w, r, &in) {
		return
	}
	h.send(w, r, http.StatusAccepted)(h.svc.BlacklistVendor(r.Context(), p, id, in, match))
}

func (h handler) getVendor(w http.ResponseWriter, r *http.Request) {
	p, id, ok := readID(w, r)
	if !ok {
		return
	}
	h.send(w, r, http.StatusOK)(h.svc.Vendor(r.Context(), p, id))
}

func (h handler) bestPrice(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	h.send(w, r, http.StatusOK)(h.svc.BestPrice(r.Context(), p, r.URL.Query().Get("sku_id")))
}

func (h handler) createSKU(w http.ResponseWriter, r *http.Request) {
	p, match, ok := prep(w, r)
	if !ok {
		return
	}
	var in skuIn
	if !bind(w, r, &in) {
		return
	}
	h.send(w, r, http.StatusCreated)(h.svc.CreateSKU(r.Context(), p, in, match))
}

func (h handler) changeSKU(w http.ResponseWriter, r *http.Request) {
	p, id, match, ok := prepID(w, r)
	if !ok {
		return
	}
	var in skuIn
	if !bind(w, r, &in) {
		return
	}
	h.send(w, r, http.StatusAccepted)(h.svc.ChangeSKU(r.Context(), p, id, in, match))
}

func (h handler) getSKU(w http.ResponseWriter, r *http.Request) {
	p, id, ok := readID(w, r)
	if !ok {
		return
	}
	h.send(w, r, http.StatusOK)(h.svc.SKU(r.Context(), p, id))
}

func (h handler) convert(w http.ResponseWriter, r *http.Request) {
	p, id, ok := readID(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	h.send(w, r, http.StatusOK)(h.svc.Convert(r.Context(), p, id, q.Get("qty"), q.Get("from"), q.Get("to")))
}

func (h handler) createAgreement(w http.ResponseWriter, r *http.Request) {
	p, match, ok := prep(w, r)
	if !ok {
		return
	}
	var in agreementIn
	if !bind(w, r, &in) {
		return
	}
	h.send(w, r, http.StatusCreated)(h.svc.CreateAgreement(r.Context(), p, in, match))
}

func (h handler) createBOM(w http.ResponseWriter, r *http.Request) {
	p, match, ok := prep(w, r)
	if !ok {
		return
	}
	var in bomIn
	if !bind(w, r, &in) {
		return
	}
	h.send(w, r, http.StatusCreated)(h.svc.CreateBOM(r.Context(), p, in, match))
}

func (h handler) changeBOM(w http.ResponseWriter, r *http.Request) {
	p, id, match, ok := prepID(w, r)
	if !ok {
		return
	}
	var in bomIn
	if !bind(w, r, &in) {
		return
	}
	h.send(w, r, http.StatusAccepted)(h.svc.ChangeBOM(r.Context(), p, id, in, match))
}

func (h handler) bomVersions(w http.ResponseWriter, r *http.Request) {
	p, id, ok := readID(w, r)
	if !ok {
		return
	}
	h.send(w, r, http.StatusOK)(h.svc.BOMVersions(r.Context(), p, id))
}

func (h handler) decide(w http.ResponseWriter, r *http.Request) {
	p, match, ok := prep(w, r)
	if !ok {
		return
	}
	var in decideIn
	if !bind(w, r, &in) {
		return
	}
	h.send(w, r, http.StatusOK)(h.svc.Decide(r.Context(), p, chi.URLParam(r, "request_id"), match, in.Decision, in.Reason, in.StepUpToken, in.ToUserID))
}

func (h handler) grantTitle(w http.ResponseWriter, r *http.Request) {
	p, _, ok := prep(w, r)
	if !ok {
		return
	}
	var in titleIn
	if !bind(w, r, &in) {
		return
	}
	h.send(w, r, http.StatusOK)(h.svc.GrantTitle(r.Context(), p, in.UserID, in.Title))
}

func (h handler) pin(w http.ResponseWriter, r *http.Request) {
	p, _, ok := prep(w, r)
	if !ok {
		return
	}
	var in pinIn
	if !bind(w, r, &in) {
		return
	}
	h.send(w, r, http.StatusCreated)(h.svc.Pin(r.Context(), p, in))
}

func (h handler) document(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	h.send(w, r, http.StatusOK)(h.svc.Document(r.Context(), p, chi.URLParam(r, "doc_type"), chi.URLParam(r, "doc_id")))
}

func (h handler) createOrder(w http.ResponseWriter, r *http.Request) {
	p, _, ok := prep(w, r)
	if !ok {
		return
	}
	var in orderIn
	if !bind(w, r, &in) {
		return
	}
	h.send(w, r, http.StatusCreated)(h.svc.CreateOrder(r.Context(), p, in))
}

func (h handler) creditOverride(w http.ResponseWriter, r *http.Request) {
	p, id, match, ok := prepID(w, r)
	if !ok {
		return
	}
	var in overrideIn
	if !bind(w, r, &in) {
		return
	}
	h.send(w, r, http.StatusOK)(h.svc.Override(r.Context(), p, id, "credit", in, match))
}

func (h handler) floorOverride(w http.ResponseWriter, r *http.Request) {
	p, id, match, ok := prepID(w, r)
	if !ok {
		return
	}
	var in overrideIn
	if !bind(w, r, &in) {
		return
	}
	h.send(w, r, http.StatusOK)(h.svc.Override(r.Context(), p, id, "price_floor", in, match))
}

func (h handler) overrides(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	h.send(w, r, http.StatusOK)(h.svc.Overrides(r.Context(), p, r.URL.Query().Get("kind")))
}

func (h handler) warehouse(w http.ResponseWriter, r *http.Request) {
	p, _, ok := prep(w, r)
	if !ok {
		return
	}
	var in warehouseIn
	if !bind(w, r, &in) {
		return
	}
	h.send(w, r, http.StatusCreated)(h.svc.Warehouse(r.Context(), p, in))
}

func (h handler) receive(w http.ResponseWriter, r *http.Request) {
	p, _, ok := prep(w, r)
	if !ok {
		return
	}
	var in stockIn
	if !bind(w, r, &in) {
		return
	}
	h.send(w, r, http.StatusCreated)(h.svc.Receive(r.Context(), p, in))
}

func (h handler) issue(w http.ResponseWriter, r *http.Request) {
	p, _, ok := prep(w, r)
	if !ok {
		return
	}
	var in stockIn
	if !bind(w, r, &in) {
		return
	}
	h.send(w, r, http.StatusCreated)(h.svc.Issue(r.Context(), p, in))
}

func (h handler) reserve(w http.ResponseWriter, r *http.Request) {
	p, _, ok := prep(w, r)
	if !ok {
		return
	}
	var in stockIn
	if !bind(w, r, &in) {
		return
	}
	h.send(w, r, http.StatusCreated)(h.svc.Reserve(r.Context(), p, in))
}

func (h handler) availability(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	h.send(w, r, http.StatusOK)(h.svc.Availability(r.Context(), p, q.Get("warehouse"), q.Get("sku"), q.Get("view"), q.Get("class")))
}

func (h handler) produce(w http.ResponseWriter, r *http.Request) {
	p, _, ok := prep(w, r)
	if !ok {
		return
	}
	var in produceIn
	if !bind(w, r, &in) {
		return
	}
	id, err := uuid.Parse(in.BOMID)
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, "bom_id is required"))
		return
	}
	h.send(w, r, http.StatusCreated)(h.svc.Produce(r.Context(), p, id, in.Qty))
}

func (h handler) production(w http.ResponseWriter, r *http.Request) {
	p, id, ok := readID(w, r)
	if !ok {
		return
	}
	h.send(w, r, http.StatusOK)(h.svc.Production(r.Context(), p, id))
}

func (h handler) purchase(w http.ResponseWriter, r *http.Request) {
	p, _, ok := prep(w, r)
	if !ok {
		return
	}
	var in purchaseIn
	if !bind(w, r, &in) {
		return
	}
	h.send(w, r, http.StatusCreated)(h.svc.Purchase(r.Context(), p, in))
}

func (h handler) purchaseDoc(w http.ResponseWriter, r *http.Request) {
	p, id, ok := readID(w, r)
	if !ok {
		return
	}
	h.send(w, r, http.StatusOK)(h.svc.PurchaseDoc(r.Context(), p, id))
}

func (h handler) pay(w http.ResponseWriter, r *http.Request) {
	p, _, ok := prep(w, r)
	if !ok {
		return
	}
	var in paymentIn
	if !bind(w, r, &in) {
		return
	}
	h.send(w, r, http.StatusCreated)(h.svc.Pay(r.Context(), p, in))
}

func (h handler) release(w http.ResponseWriter, r *http.Request) {
	p, id, _, ok := prepID(w, r)
	if !ok {
		return
	}
	h.send(w, r, http.StatusOK)(h.svc.ReleasePayment(r.Context(), p, id))
}

func (h handler) send(w http.ResponseWriter, r *http.Request, status int) func(row, error) {
	return func(data row, err error) { write(w, r, status, data, err) }
}

func write(w http.ResponseWriter, r *http.Request, status int, data row, err error) {
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	httpx.JSON(w, r, status, data)
}

func bind(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := httpx.DecodeJSON(r, dst); err != nil {
		apierr.Write(w, r, err)
		return false
	}
	return true
}

func principal(w http.ResponseWriter, r *http.Request) (rls.Principal, bool) {
	p, err := rls.FromContext(r.Context())
	if err != nil || p.UserID == "" {
		apierr.Write(w, r, apierr.New(apierr.AuthRequired, "authentication required"))
		return rls.Principal{}, false
	}
	return p, true
}

func prep(w http.ResponseWriter, r *http.Request) (rls.Principal, int64, bool) {
	p, ok := principal(w, r)
	if !ok {
		return rls.Principal{}, 0, false
	}
	match, _, err := ifmatch.Parse(r, true)
	if err != nil {
		apierr.Write(w, r, err)
		return rls.Principal{}, 0, false
	}
	return p, match, true
}

func prepID(w http.ResponseWriter, r *http.Request) (rls.Principal, uuid.UUID, int64, bool) {
	p, match, ok := prep(w, r)
	if !ok {
		return rls.Principal{}, uuid.Nil, 0, false
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, "id must be a uuid"))
		return rls.Principal{}, uuid.Nil, 0, false
	}
	return p, id, match, true
}

func readID(w http.ResponseWriter, r *http.Request) (rls.Principal, uuid.UUID, bool) {
	p, ok := principal(w, r)
	if !ok {
		return rls.Principal{}, uuid.Nil, false
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.ValidationError, "id must be a uuid"))
		return rls.Principal{}, uuid.Nil, false
	}
	return p, id, true
}
