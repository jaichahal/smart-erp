package masters

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

type customerIn struct {
	Name           string `json:"name"`
	TRN            string `json:"trn"`
	CreditLimit    string `json:"credit_limit"`
	PaymentTermsID string `json:"payment_terms_id"`
	PriceListID    string `json:"price_list_id"`
	TerritoryID    string `json:"territory_id"`
	Group          string `json:"group"`
	Addresses      []any  `json:"addresses"`
	Contacts       []any  `json:"contacts"`
	OnHold         bool   `json:"on_hold"`
	InterestRule   string `json:"interest_rule"`
	DunningProfile string `json:"dunning_profile"`
	ChangeReason   string `json:"change_reason"`
}

type bankIn struct {
	Holder       string `json:"holder"`
	BankName     string `json:"bank_name"`
	IBAN         string `json:"iban"`
	ChangeReason string `json:"change_reason"`
}

type vendorIn struct {
	Name           string  `json:"name"`
	TRN            string  `json:"trn"`
	Group          string  `json:"group"`
	Currency       string  `json:"currency"`
	PaymentTermsID string  `json:"payment_terms_id"`
	Addresses      []any   `json:"addresses"`
	Contacts       []any   `json:"contacts"`
	Bank           *bankIn `json:"bank"`
	Status         string  `json:"status"`
	ChangeReason   string  `json:"change_reason"`
}

type unitIn struct {
	UOM    string `json:"uom"`
	Factor string `json:"factor_to_base"`
}

type skuIn struct {
	Code          string   `json:"code"`
	Name          string   `json:"name"`
	ItemClass     string   `json:"item_class"`
	BaseUOM       string   `json:"base_uom"`
	FloorPrice    string   `json:"floor_price"`
	MinMargin     string   `json:"min_margin"`
	ReorderLevel  string   `json:"reorder_level"`
	Group         string   `json:"sku_group"`
	PurchaseUOM   string   `json:"purchase_uom"`
	StockUOM      string   `json:"stock_uom"`
	ProductionUOM string   `json:"production_uom"`
	SalesUOM      string   `json:"sales_uom"`
	Units         []unitIn `json:"units"`
	ChangeReason  string   `json:"change_reason"`
}

type bomLineIn struct {
	RawSKUID   string `json:"raw_sku_id"`
	QtyPerUnit string `json:"qty_per_unit"`
	UOM        string `json:"uom"`
	WastagePct string `json:"wastage_pct"`
}

type bomIn struct {
	FinishedSKUID string      `json:"finished_sku_id"`
	Lines         []bomLineIn `json:"lines"`
	ChangeReason  string      `json:"change_reason"`
}

type agreementIn struct {
	CustomerID   string `json:"customer_id"`
	SKUID        string `json:"sku_id"`
	ValidFrom    string `json:"valid_from"`
	ValidTo      string `json:"valid_to"`
	MinQty       string `json:"min_qty"`
	MaxQty       string `json:"max_qty"`
	Price        string `json:"price"`
	Currency     string `json:"currency"`
	ChangeReason string `json:"change_reason"`
}

type skuApprovalIn struct {
	SKUID        string `json:"sku_id"`
	Price        string `json:"price"`
	Currency     string `json:"currency"`
	ChangeReason string `json:"change_reason"`
}

type reasonIn struct {
	ChangeReason string `json:"change_reason"`
}

func (s *Service) CreateCustomer(ctx context.Context, p rls.Principal, in customerIn, match int64) (row, error) {
	if err := checkCustomer(in); err != nil {
		return nil, err
	}
	id := uuid.New()
	return s.propose(ctx, p, proposal{
		DocType: docCustomer, Subject: id, Match: match, Party: in.Name, Payload: in, New: true,
		Insert: func(ctx context.Context, tx pgx.Tx, requestID string) error {
			_, err := tx.Exec(ctx, `INSERT INTO erp.customers
				(id, company_id, name, trn, territory_id, owner_id, status, state_version, approval_request_id)
				VALUES ($1,$2,$3,$4,$5,$6,'pending_approval',1,$7)`,
				id, p.CompanyID, in.Name, in.TRN, nullUUID(in.TerritoryID), p.UserID, requestID)
			return err
		},
	})
}

func (s *Service) ChangeCustomer(ctx context.Context, p rls.Principal, id uuid.UUID, in customerIn, match int64) (row, error) {
	if err := checkCustomer(in); err != nil {
		return nil, err
	}
	return s.propose(ctx, p, proposal{DocType: docCustomer, Subject: id, Match: match, Party: in.Name, Payload: in})
}

func checkCustomer(in customerIn) error {
	if in.Name == "" {
		return apierr.New(apierr.ValidationError, "name is required")
	}
	if _, err := mustMoney(in.CreditLimit, "credit_limit"); err != nil {
		return err
	}
	return nil
}

func (s *Service) CreateVendor(ctx context.Context, p rls.Principal, in vendorIn, match int64) (row, error) {
	if in.Status != "" {
		return nil, apierr.New(apierr.ValidationError, "vendor insert requires an approval request")
	}
	if err := checkVendor(in); err != nil {
		return nil, err
	}
	id := uuid.New()
	return s.propose(ctx, p, proposal{
		DocType: docVendor, Subject: id, Match: match, Party: in.Name, Payload: in, New: true,
		Insert: func(ctx context.Context, tx pgx.Tx, requestID string) error {
			_, err := tx.Exec(ctx, `INSERT INTO erp.vendors
				(id, company_id, name, trn, vendor_group, currency, payment_terms_id, status, state_version, approval_request_id, created_by)
				VALUES ($1,$2,$3,$4,$5,$6,$7,'pending_approval',1,$8,$9)`,
				id, p.CompanyID, in.Name, in.TRN, in.Group, currency(in.Currency), nullUUID(in.PaymentTermsID), requestID, p.UserID)
			return err
		},
	})
}

func checkVendor(in vendorIn) error {
	if in.Name == "" {
		return apierr.New(apierr.ValidationError, "name is required")
	}
	if in.Bank != nil && (in.Bank.Holder == "" || in.Bank.BankName == "" || in.Bank.IBAN == "") {
		return apierr.New(apierr.ValidationError, "bank holder, bank name, and IBAN are required")
	}
	return nil
}

func (s *Service) ChangeBank(ctx context.Context, p rls.Principal, vendor uuid.UUID, in bankIn, match int64) (row, error) {
	if in.Holder == "" || in.BankName == "" || in.IBAN == "" {
		return nil, apierr.New(apierr.ValidationError, "bank holder, bank name, and IBAN are required")
	}
	return s.propose(ctx, p, proposal{DocType: docBank, Subject: vendor, Match: match, Party: vendor.String(), Payload: in})
}

func (s *Service) AddVendorSKU(ctx context.Context, p rls.Principal, vendor uuid.UUID, in skuApprovalIn, match int64) (row, error) {
	if _, err := uuid.Parse(in.SKUID); err != nil {
		return nil, apierr.New(apierr.ValidationError, "sku_id is required")
	}
	if in.Price != "" {
		if _, err := mustMoney(in.Price, "price"); err != nil {
			return nil, err
		}
	}
	if in.Currency == "" {
		in.Currency = "AED"
	}
	return s.propose(ctx, p, proposal{DocType: docVendorSKU, Subject: vendor, Match: match, Party: vendor.String(), Payload: in})
}

func (s *Service) BlacklistVendor(ctx context.Context, p rls.Principal, vendor uuid.UUID, in reasonIn, match int64) (row, error) {
	return s.propose(ctx, p, proposal{DocType: docBlacklist, Subject: vendor, Match: match, Party: vendor.String(), Payload: in})
}

func (s *Service) CreateSKU(ctx context.Context, p rls.Principal, in skuIn, match int64) (row, error) {
	if err := checkSKU(in); err != nil {
		return nil, err
	}
	id := uuid.New()
	return s.propose(ctx, p, proposal{
		DocType: docSKU, Subject: id, Match: match, Party: in.Code, Payload: in, New: true,
		Insert: func(ctx context.Context, tx pgx.Tx, requestID string) error {
			floor, _ := mustMoney(in.FloorPrice, "floor_price")
			margin, _ := mustMoney(zeroMoney(in.MinMargin), "min_margin")
			_, err := tx.Exec(ctx, `INSERT INTO erp.skus
				(id, company_id, code, name, item_class, base_uom, purchase_uom, stock_uom, production_uom, sales_uom,
				 floor_price, min_margin, reorder_level, sku_group, status, state_version, approval_request_id, created_by)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::numeric,$12::numeric,$13::numeric,$14,'pending_approval',1,$15,$16)`,
				id, p.CompanyID, in.Code, in.Name, in.ItemClass, in.BaseUOM, in.PurchaseUOM, in.StockUOM, in.ProductionUOM, in.SalesUOM,
				floor, margin, zeroQty(in.ReorderLevel), in.Group, requestID, p.UserID)
			return err
		},
	})
}

func (s *Service) ChangeSKU(ctx context.Context, p rls.Principal, id uuid.UUID, in skuIn, match int64) (row, error) {
	if err := checkSKU(in); err != nil {
		return nil, err
	}
	return s.propose(ctx, p, proposal{DocType: docSKU, Subject: id, Match: match, Party: in.Code, Payload: in})
}

func checkSKU(in skuIn) error {
	switch in.ItemClass {
	case "raw_material", "finished_goods", "both":
	default:
		return apierr.New(apierr.ValidationError, "item_class must be raw_material, finished_goods, or both")
	}
	if in.Code == "" || in.Name == "" || in.BaseUOM == "" {
		return apierr.New(apierr.ValidationError, "code, name, and base_uom are required")
	}
	if _, err := mustMoney(in.FloorPrice, "floor_price"); err != nil {
		return err
	}
	if len(in.Units) == 0 {
		return apierr.New(apierr.ValidationError, "at least one unit conversion is required")
	}
	return nil
}

func (s *Service) CreateBOM(ctx context.Context, p rls.Principal, in bomIn, match int64) (row, error) {
	if _, err := uuid.Parse(in.FinishedSKUID); err != nil || len(in.Lines) == 0 {
		return nil, apierr.New(apierr.ValidationError, "finished_sku_id and lines are required")
	}
	id := uuid.New()
	return s.propose(ctx, p, proposal{
		DocType: docBOM, Subject: id, Match: match, Party: in.FinishedSKUID, Payload: in, New: true,
		Insert: func(ctx context.Context, tx pgx.Tx, requestID string) error {
			_, err := tx.Exec(ctx, `INSERT INTO erp.boms
				(id, company_id, finished_sku_id, status, state_version, approval_request_id, created_by)
				VALUES ($1,$2,$3,'pending_approval',1,$4,$5)`,
				id, p.CompanyID, in.FinishedSKUID, requestID, p.UserID)
			return err
		},
	})
}

func (s *Service) ChangeBOM(ctx context.Context, p rls.Principal, id uuid.UUID, in bomIn, match int64) (row, error) {
	if len(in.Lines) == 0 {
		return nil, apierr.New(apierr.ValidationError, "lines are required")
	}
	return s.propose(ctx, p, proposal{DocType: docBOM, Subject: id, Match: match, Party: id.String(), Payload: in})
}

func (s *Service) CreateAgreement(ctx context.Context, p rls.Principal, in agreementIn, match int64) (row, error) {
	price, err := mustMoney(in.Price, "price")
	if err != nil {
		return nil, err
	}
	minQ, err := mustQty(in.MinQty, "min_qty")
	if err != nil {
		return nil, err
	}
	maxQ, err := mustQty(in.MaxQty, "max_qty")
	if err != nil {
		return nil, err
	}
	cust, err1 := uuid.Parse(in.CustomerID)
	sku, err2 := uuid.Parse(in.SKUID)
	if err1 != nil || err2 != nil || in.ValidFrom == "" || in.ValidTo == "" {
		return nil, apierr.New(apierr.ValidationError, "customer, sku, and period are required")
	}
	id := uuid.New()
	in.Price, in.MinQty, in.MaxQty = price, minQ, maxQ
	if in.Currency == "" {
		in.Currency = "AED"
	}
	return s.propose(ctx, p, proposal{
		DocType: docAgreement, Subject: id, Match: match, Party: in.CustomerID, Payload: in, New: true,
		Insert: func(ctx context.Context, tx pgx.Tx, requestID string) error {
			_, err := tx.Exec(ctx, `INSERT INTO erp.price_agreements
				(id, company_id, customer_id, sku_id, valid_from, valid_to, min_qty, max_qty, price, currency,
				 status, state_version, version_no, change_reason, approval_request_id, created_by)
				VALUES ($1,$2,$3,$4,$5::date,$6::date,$7::numeric,$8::numeric,$9::numeric,$10,'pending_approval',1,1,$11,$12,$13)`,
				id, p.CompanyID, cust, sku, in.ValidFrom, in.ValidTo, minQ, maxQ, price, in.Currency, in.ChangeReason, requestID, p.UserID)
			return err
		},
	})
}

func currency(s string) string {
	if s == "" {
		return "AED"
	}
	return s
}

func zeroMoney(s string) string {
	if s == "" {
		return "0"
	}
	return s
}

func zeroQty(s string) string {
	if s == "" {
		return "0"
	}
	return s
}

func decodePayload(raw []byte, dst any) error {
	return json.Unmarshal(raw, dst)
}
