package stock

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/httpx"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// Mount registers stock queries. Cost fields are included only when the role
// has field:cost:read. A Sales Agent payload has on-hand, reserved, and available.
func Mount(r chi.Router, deps httpx.Deps) {
	s := New(deps.Pool)
	r.Get("/stock/availability", s.handleAvailability)
	r.Get("/stock/ledger", s.handleLedger)
}

func (s *Service) handleAvailability(w http.ResponseWriter, r *http.Request) {
	p, err := rls.FromContext(r.Context())
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.AuthRequired, "authentication required"))
		return
	}
	q := Query{Class: r.URL.Query().Get("class"), Text: r.URL.Query().Get("q")}
	if raw := r.URL.Query().Get("warehouse"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			apierr.Write(w, r, apierr.New(apierr.ValidationError, "warehouse is not an id"))
			return
		}
		q.WarehouseID = id
	}
	rows, seeCost, err := s.list(r.Context(), p, q)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		item := map[string]any{
			"sku": row.SKU, "sku_id": row.SKUID.String(), "warehouse_id": row.WarehouseID.String(),
			"on_hand": row.OnHand, "reserved": row.Reserved, "available": row.Available, "uom": row.UOM,
		}
		if seeCost {
			item["unit_cost"] = row.UnitCost
			item["value"] = row.Value
		}
		items = append(items, item)
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"items": items})
}

func (s *Service) handleLedger(w http.ResponseWriter, r *http.Request) {
	p, err := rls.FromContext(r.Context())
	if err != nil {
		apierr.Write(w, r, apierr.New(apierr.AuthRequired, "authentication required"))
		return
	}
	var sku *uuid.UUID
	if raw := r.URL.Query().Get("sku"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			apierr.Write(w, r, apierr.New(apierr.ValidationError, "sku is not an id"))
			return
		}
		sku = &id
	}
	from, to, err := dateWindow(r.URL.Query().Get("from"), r.URL.Query().Get("to"))
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	lines, seeCost, err := s.ledger(r.Context(), p, sku, from, to)
	if err != nil {
		apierr.Write(w, r, err)
		return
	}
	out := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		row := map[string]any{
			"id": line.ID.String(), "sku_id": line.SKUID.String(), "warehouse_id": line.WarehouseID.String(),
			"qty_delta": line.QtyDelta, "movement_type": line.MovementType, "source_doc_id": line.SourceDocID,
		}
		if seeCost {
			row["unit_cost"] = line.UnitCost
			row["value_delta"] = line.ValueDelta
		}
		out = append(out, row)
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"lines": out})
}

func dateWindow(fromRaw, toRaw string) (time.Time, time.Time, error) {
	var from, to time.Time
	var err error
	if fromRaw != "" {
		from, err = time.Parse("2006-01-02", fromRaw)
		if err != nil {
			return time.Time{}, time.Time{}, apierr.New(apierr.ValidationError, "from is not a date")
		}
	}
	if toRaw != "" {
		to, err = time.Parse("2006-01-02", toRaw)
		if err != nil {
			return time.Time{}, time.Time{}, apierr.New(apierr.ValidationError, "to is not a date")
		}
		to = to.Add(24 * time.Hour)
	}
	return from, to, nil
}
