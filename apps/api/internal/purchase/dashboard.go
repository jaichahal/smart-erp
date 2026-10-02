package purchase

import (
	"context"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// Dashboard lists vendors, invoice counts, and the best effective approved price.
func (s *Service) Dashboard(ctx context.Context, p rls.Principal, sku *uuid.UUID) (Dashboard, error) {
	out := Dashboard{
		Actions:       []string{},
		Vendors:       []VendorRow{},
		Blocked:       []BlockedPrice{},
		ShownUnranked: []PriceSource{},
		AsOf:          s.clock(),
	}
	ok, err := s.hasTitle(ctx, p, p.UserID)
	if err != nil {
		return Dashboard{}, err
	}
	if ok {
		out.Actions = []string{"approve", "blacklist"}
	}
	err = rls.Tx(ctx, s.Pool, p, func(tx pgx.Tx) error {
		vendors, err := s.loadVendors(ctx, tx, p)
		if err != nil {
			return err
		}
		counts, err := s.invoiceCounts(ctx, tx, p)
		if err != nil {
			return err
		}
		var class string
		approved := map[uuid.UUID]bool{}
		if sku != nil {
			if err := tx.QueryRow(ctx, `SELECT item_class FROM erp.purchase_skus WHERE company_id=$1 AND sku_id=$2`, p.CompanyID, *sku).Scan(&class); err != nil {
				return err
			}
			rows, err := tx.Query(ctx, `SELECT vendor_id FROM erp.purchase_vendor_skus WHERE company_id=$1 AND sku_id=$2`, p.CompanyID, *sku)
			if err != nil {
				return err
			}
			for rows.Next() {
				var id uuid.UUID
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return err
				}
				approved[id] = true
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
		}
		if sku != nil && class != classRaw {
			for id := range vendors {
				approved[vendors[id].VendorID] = true
			}
		}
		for _, v := range vendors {
			v.SKUs = []SKUCount{}
			if c := counts[v.VendorID]; c != nil {
				v.ActiveInvoices = c.vendorActive
				v.PastInvoices = c.vendorPast
				if c.skus != nil {
					v.SKUs = c.skus
				}
			}
			if sku != nil && class == classRaw && !approved[v.VendorID] {
				out.Blocked = append(out.Blocked, BlockedPrice{VendorID: v.VendorID, Reason: "not_approved_for_sku"})
				continue
			}
			out.Vendors = append(out.Vendors, v)
		}
		if sku == nil {
			return nil
		}
		return s.rank(ctx, tx, p, *sku, approved, &out)
	})
	return out, err
}

type tallies struct {
	vendorActive int
	vendorPast   int
	skus         []SKUCount
}

func (s *Service) loadVendors(ctx context.Context, tx pgx.Tx, p rls.Principal) ([]VendorRow, error) {
	rows, err := tx.Query(ctx, `SELECT id, name, blacklisted FROM erp.purchase_vendors WHERE company_id=$1 ORDER BY name`, p.CompanyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VendorRow
	for rows.Next() {
		var v VendorRow
		if err := rows.Scan(&v.VendorID, &v.Name, &v.Blacklisted); err != nil {
			return nil, err
		}
		v.SKUs = []SKUCount{}
		out = append(out, v)
	}
	if out == nil {
		out = []VendorRow{}
	}
	return out, rows.Err()
}

func (s *Service) invoiceCounts(ctx context.Context, tx pgx.Tx, p rls.Principal) (map[uuid.UUID]*tallies, error) {
	rows, err := tx.Query(ctx, `SELECT d.id, d.vendor_id, d.status, l.sku_id
		FROM erp.purchase_docs d
		LEFT JOIN erp.purchase_lines l ON l.doc_id=d.id
		WHERE d.company_id=$1 AND d.doc_type='supplier_invoice'`, p.CompanyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type seen struct {
		vendor bool
		skus   map[uuid.UUID]bool
	}
	once := map[uuid.UUID]*seen{}
	out := map[uuid.UUID]*tallies{}
	for rows.Next() {
		var docID, vendor uuid.UUID
		var status string
		var sku *uuid.UUID
		if err := rows.Scan(&docID, &vendor, &status, &sku); err != nil {
			return nil, err
		}
		if out[vendor] == nil {
			out[vendor] = &tallies{}
		}
		if once[docID] == nil {
			once[docID] = &seen{skus: map[uuid.UUID]bool{}}
		}
		past := status == statusPosted || status == statusClosed
		if !once[docID].vendor {
			once[docID].vendor = true
			if past {
				out[vendor].vendorPast++
			} else {
				out[vendor].vendorActive++
			}
		}
		if sku == nil || once[docID].skus[*sku] {
			continue
		}
		once[docID].skus[*sku] = true
		out[vendor].skus = addCount(out[vendor].skus, *sku, past)
	}
	return out, rows.Err()
}

func addCount(list []SKUCount, sku uuid.UUID, past bool) []SKUCount {
	for i := range list {
		if list[i].SKUID == sku {
			if past {
				list[i].Past++
			} else {
				list[i].Active++
			}
			return list
		}
	}
	c := SKUCount{SKUID: sku}
	if past {
		c.Past = 1
	} else {
		c.Active = 1
	}
	return append(list, c)
}

func (s *Service) rank(ctx context.Context, tx pgx.Tx, p rls.Principal, sku uuid.UUID, approved map[uuid.UUID]bool, out *Dashboard) error {
	rows, err := tx.Query(ctx, `SELECT d.id, d.number, d.doc_type, d.status, d.vendor_id, d.currency, coalesce(d.fx_rate::text,''),
		coalesce(d.effective_from::text,''), coalesce(d.effective_to::text,''), l.unit_price::text, v.blacklisted
		FROM erp.purchase_lines l
		JOIN erp.purchase_docs d ON d.id=l.doc_id
		JOIN erp.purchase_vendors v ON v.id=d.vendor_id
		WHERE d.company_id=$1 AND l.sku_id=$2 AND d.doc_type IN ('quote','price_agreement')`, p.CompanyID, sku)
	if err != nil {
		return err
	}
	defer rows.Close()
	var best *big.Rat
	now := s.clock()
	for rows.Next() {
		var src PriceSource
		var status, from, to, rate string
		var blacklisted bool
		if err := rows.Scan(&src.DocumentID, &src.Number, &src.DocType, &status, &src.VendorID, &src.Currency, &rate, &from, &to, &src.UnitPrice, &blacklisted); err != nil {
			return err
		}
		src.FXRate = rate
		src.EffectiveFrom = from
		src.EffectiveTo = to
		if blacklisted {
			cp := src
			out.Blocked = append(out.Blocked, BlockedPrice{VendorID: src.VendorID, Reason: "blacklisted", Source: &cp})
			continue
		}
		if !approved[src.VendorID] {
			cp := src
			out.Blocked = append(out.Blocked, BlockedPrice{VendorID: src.VendorID, Reason: "not_approved_for_sku", Source: &cp})
			continue
		}
		if status != statusApproved {
			cp := src
			out.Blocked = append(out.Blocked, BlockedPrice{VendorID: src.VendorID, Reason: "quote_not_approved", Source: &cp})
			continue
		}
		if !effective(from, to, now) {
			continue
		}
		aed, ok := aedAmount(src.UnitPrice, src.Currency, rate)
		if !ok {
			out.ShownUnranked = append(out.ShownUnranked, src)
			continue
		}
		src.AED = aed.FloatString(4)
		if best == nil || aed.Cmp(best) < 0 {
			best = aed
			cp := src
			out.Best = &cp
		}
	}
	return rows.Err()
}

func effective(from, to string, now time.Time) bool {
	day := now.Format("2006-01-02")
	if from != "" && day < from {
		return false
	}
	if to != "" && day > to {
		return false
	}
	return true
}

func aedAmount(price, currency, rate string) (*big.Rat, bool) {
	p, ok := new(big.Rat).SetString(price)
	if !ok {
		return nil, false
	}
	if currency == "AED" && rate == "" {
		return p, true
	}
	if rate == "" {
		return nil, false
	}
	r, ok := new(big.Rat).SetString(rate)
	if !ok || r.Sign() <= 0 {
		return nil, false
	}
	return new(big.Rat).Mul(p, r), true
}
