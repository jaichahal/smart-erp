package stock

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/audit"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/canon"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// Service posts the stock ledger and answers availability.
type Service struct {
	pool    *pgxpool.Pool
	catalog Catalog
}

// New builds a service that reads the snapshot catalog.
// Call UseCatalog with the masters reader when P2.2 is merged.
func New(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool, catalog: MastersCatalog{}}
}

// UseCatalog replaces the snapshot reader. Masters owns item and warehouse facts.
func (s *Service) UseCatalog(c Catalog) {
	if c != nil {
		s.catalog = c
	}
}

func (s *Service) inTx(ctx context.Context, p rls.Principal, fn func(pgx.Tx) error) error {
	if p.CompanyID == uuid.Nil || p.UserID == "" {
		return apierr.New(apierr.AuthRequired, "authentication required")
	}
	ctx = rls.WithPrincipal(ctx, p)
	return rls.Tx(ctx, s.pool, p, fn)
}

// RegisterItem stores SKU facts until masters P2.2 is merged.
func (s *Service) RegisterItem(ctx context.Context, p rls.Principal, it Item) error {
	if it.ID == uuid.Nil || strings.TrimSpace(it.SKU) == "" || strings.TrimSpace(it.UOM) == "" {
		return apierr.New(apierr.ValidationError, "sku, item class, and uom are required")
	}
	switch it.ItemClass {
	case ClassRaw, ClassFinished, ClassBoth:
	default:
		return apierr.New(apierr.ValidationError, "item class must be raw_material, finished_goods, or both")
	}
	err := s.inTx(ctx, p, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO erp.skus (
				id, company_id, code, name, item_class,
				base_uom, purchase_uom, stock_uom, production_uom, sales_uom,
				floor_price, status, state_version, created_by)
			VALUES ($1, $2, $3, $3, $4, $5, $5, $5, $5, $5, 0, 'approved', 1, $6)`,
			it.ID, p.CompanyID, it.SKU, it.ItemClass, it.UOM, p.UserID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO erp.stock_skus (company_id, sku_id, sku, item_class, uom)
			VALUES ($1, $2, $3, $4, $5)`, p.CompanyID, it.ID, it.SKU, it.ItemClass, it.UOM)
		return err
	})
	return mapWrite(err)
}

// RegisterWarehouse stores a warehouse until masters P2.2 is merged.
func (s *Service) RegisterWarehouse(ctx context.Context, p rls.Principal, wh Warehouse) error {
	if wh.ID == uuid.Nil || strings.TrimSpace(wh.Code) == "" {
		return apierr.New(apierr.ValidationError, "warehouse code is required")
	}
	err := s.inTx(ctx, p, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO erp.warehouses (id, company_id, code, name)
			VALUES ($1, $2, $3, $3)`, wh.ID, p.CompanyID, wh.Code); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO erp.stock_warehouses (company_id, warehouse_id, code)
			VALUES ($1, $2, $3)`, p.CompanyID, wh.ID, wh.Code)
		return err
	})
	return mapWrite(err)
}

// Post appends one immutable ledger line and updates the moving average when the line is a receipt.
func (s *Service) Post(ctx context.Context, p rls.Principal, m Move) (Line, error) {
	var line Line
	err := s.inTx(ctx, p, func(tx pgx.Tx) error {
		var err error
		line, err = s.postTx(ctx, tx, p, m)
		return err
	})
	return line, err
}

// Reserve holds quantity for an order line in this transaction's company.
// Pending reservations are recorded and do not reduce available.
func (s *Service) Reserve(ctx context.Context, p rls.Principal, req ReserveRequest) (Reservation, error) {
	var out Reservation
	err := s.inTx(ctx, p, func(tx pgx.Tx) error {
		var err error
		out, err = s.reserveTx(ctx, tx, p, req)
		return err
	})
	return out, err
}

// Release returns an active or pending reservation without posting a movement.
func (s *Service) Release(ctx context.Context, p rls.Principal, orderLineID string) error {
	return s.inTx(ctx, p, func(tx pgx.Tx) error {
		return releaseTx(ctx, tx, p, orderLineID)
	})
}

// Consume marks the reservation consumed and posts the delivery at moving-average cost.
// A second consume of the same order line does not post again.
func (s *Service) Consume(ctx context.Context, p rls.Principal, orderLineID, deliveryNoteID string) (Line, error) {
	var line Line
	err := s.inTx(ctx, p, func(tx pgx.Tx) error {
		if strings.TrimSpace(orderLineID) == "" || strings.TrimSpace(deliveryNoteID) == "" {
			return apierr.New(apierr.ValidationError, "order line and delivery note are required")
		}
		var sku, wh uuid.UUID
		var qty string
		var status string
		err := tx.QueryRow(ctx, `
			SELECT sku_id, warehouse_id, qty::text, status
			FROM erp.stock_reservations
			WHERE company_id = $1 AND order_line_id = $2
			FOR UPDATE`, p.CompanyID, orderLineID).Scan(&sku, &wh, &qty, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.New(apierr.NotFound, "reservation not found")
		}
		if err != nil {
			return err
		}
		if status != StatusActive {
			return apierr.New(apierr.Conflict, "reservation is already consumed")
		}
		tag, err := tx.Exec(ctx, `
			UPDATE erp.stock_reservations
			SET status = $3, delivery_note_id = $4
			WHERE company_id = $1 AND order_line_id = $2 AND status = 'active'`,
			p.CompanyID, orderLineID, StatusConsumed, deliveryNoteID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return apierr.New(apierr.Conflict, "reservation is already consumed")
		}
		q, err := parseQty(qty)
		if err != nil {
			return err
		}
		line, err = s.postTx(ctx, tx, p, Move{
			SKUID: sku, WarehouseID: wh, QtyDelta: formatQty(neg(q)),
			MovementType: MoveDelivery, SourceDocID: deliveryNoteID,
		})
		return err
	})
	return line, err
}

// Availability returns on-hand, reserved, and available for the query.
func (s *Service) Availability(ctx context.Context, p rls.Principal, q Query) ([]Position, error) {
	rows, _, err := s.list(ctx, p, q)
	return rows, err
}

// list is Availability plus whether this role may see cost fields.
func (s *Service) list(ctx context.Context, p rls.Principal, q Query) ([]Position, bool, error) {
	var rows []Position
	var seeCost bool
	err := s.inTx(ctx, p, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT erp.permission_scope('field:cost:read') IS NOT NULL`).Scan(&seeCost); err != nil {
			return err
		}
		classes, empty, err := allowedClasses(p, seeCost, q.Class)
		if err != nil {
			return err
		}
		if empty {
			rows = []Position{}
			return nil
		}
		var skuArg, whArg any
		if q.SKUID != uuid.Nil {
			skuArg = q.SKUID
		}
		if q.WarehouseID != uuid.Nil {
			whArg = q.WarehouseID
		}
		pgRows, err := tx.Query(ctx, `
			SELECT s.sku, s.sku_id, w.warehouse_id, s.item_class, s.uom,
			       COALESCE(b.qty, 0)::text, COALESCE(b.value, 0)::text,
			       COALESCE(r.reserved, 0)::text
			FROM erp.stock_skus s
			JOIN erp.stock_warehouses w ON w.company_id = s.company_id
			LEFT JOIN erp.stock_balances b
			  ON b.company_id = s.company_id AND b.sku_id = s.sku_id AND b.warehouse_id = w.warehouse_id
			LEFT JOIN (
			    SELECT sku_id, warehouse_id, SUM(qty) AS reserved
			    FROM erp.stock_reservations
			    WHERE company_id = $1 AND status = 'active'
			    GROUP BY sku_id, warehouse_id
			) r ON r.sku_id = s.sku_id AND r.warehouse_id = w.warehouse_id
			WHERE s.company_id = $1
			  AND ($2::uuid IS NULL OR s.sku_id = $2)
			  AND ($3::uuid IS NULL OR w.warehouse_id = $3)
			  AND s.item_class = ANY($4::text[])
			  AND ($5 = '' OR s.sku ILIKE '%' || $5 || '%')
			ORDER BY s.sku, w.code`, p.CompanyID, skuArg, whArg, classes, q.Text)
		if err != nil {
			return err
		}
		defer pgRows.Close()
		rows = []Position{}
		for pgRows.Next() {
			var pos Position
			var onHand, value, reserved string
			if err := pgRows.Scan(&pos.SKU, &pos.SKUID, &pos.WarehouseID, &pos.ItemClass, &pos.UOM, &onHand, &value, &reserved); err != nil {
				return err
			}
			oh, err := parseQty(onHand)
			if err != nil {
				return err
			}
			val, err := parseQty(value)
			if err != nil {
				return err
			}
			res, err := parseQty(reserved)
			if err != nil {
				return err
			}
			pos.OnHand = formatQty(oh)
			pos.Reserved = formatQty(res)
			pos.Available = formatQty(sub(oh, res))
			pos.Value = formatQty(val)
			if oh.Sign() == 0 {
				pos.UnitCost = formatQty(new(big.Rat))
			} else {
				pos.UnitCost = formatQty(quo(val, oh))
			}
			rows = append(rows, pos)
		}
		return pgRows.Err()
	})
	return rows, seeCost, err
}

func (s *Service) reserveTx(ctx context.Context, tx pgx.Tx, p rls.Principal, req ReserveRequest) (Reservation, error) {
	if strings.TrimSpace(req.OrderLineID) == "" {
		return Reservation{}, apierr.New(apierr.ValidationError, "order line is required")
	}
	qty, err := parseQty(req.Qty)
	if err != nil {
		return Reservation{}, err
	}
	if qty.Sign() <= 0 {
		return Reservation{}, apierr.New(apierr.ValidationError, "reservation quantity must be positive")
	}
	if err := lockSKU(ctx, tx, p.CompanyID, req.SKUID, req.WarehouseID); err != nil {
		return Reservation{}, err
	}
	if _, err := s.catalog.Item(ctx, tx, p.CompanyID, req.SKUID); err != nil {
		return Reservation{}, err
	}
	if _, err := s.catalog.Warehouse(ctx, tx, p.CompanyID, req.WarehouseID); err != nil {
		return Reservation{}, err
	}
	status := StatusActive
	if req.Pending {
		status = StatusPending
	} else {
		onHand, reserved, err := loadQty(ctx, tx, p.CompanyID, req.SKUID, req.WarehouseID)
		if err != nil {
			return Reservation{}, err
		}
		if sub(onHand, reserved).Cmp(qty) < 0 {
			return Reservation{}, apierr.New(apierr.NegativeStock, "available quantity is not enough")
		}
	}
	id := uuid.New()
	_, err = tx.Exec(ctx, `
		INSERT INTO erp.stock_reservations (id, company_id, order_line_id, sku_id, warehouse_id, qty, status)
		VALUES ($1, $2, $3, $4, $5, $6::numeric, $7)`,
		id, p.CompanyID, req.OrderLineID, req.SKUID, req.WarehouseID, formatQty(qty), status)
	if err != nil {
		return Reservation{}, mapWrite(err)
	}
	return Reservation{
		ID: id, OrderLineID: req.OrderLineID, SKUID: req.SKUID, WarehouseID: req.WarehouseID,
		Qty: formatQty(qty), Status: status,
	}, nil
}

func releaseTx(ctx context.Context, tx pgx.Tx, p rls.Principal, orderLineID string) error {
	if strings.TrimSpace(orderLineID) == "" {
		return apierr.New(apierr.ValidationError, "order line is required")
	}
	var sku, wh uuid.UUID
	err := tx.QueryRow(ctx, `
		SELECT sku_id, warehouse_id FROM erp.stock_reservations
		WHERE company_id = $1 AND order_line_id = $2 AND status IN ('active', 'pending')
		FOR UPDATE`, p.CompanyID, orderLineID).Scan(&sku, &wh)
	if errors.Is(err, pgx.ErrNoRows) {
		return apierr.New(apierr.Conflict, "reservation is not active")
	}
	if err != nil {
		return err
	}
	if err := lockSKU(ctx, tx, p.CompanyID, sku, wh); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE erp.stock_reservations SET status = 'released'
		WHERE company_id = $1 AND order_line_id = $2 AND status IN ('active', 'pending')`,
		p.CompanyID, orderLineID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return apierr.New(apierr.Conflict, "reservation is not active")
	}
	return nil
}

func (s *Service) postTx(ctx context.Context, tx pgx.Tx, p rls.Principal, m Move) (Line, error) {
	if m.SKUID == uuid.Nil || m.WarehouseID == uuid.Nil || strings.TrimSpace(m.SourceDocID) == "" {
		return Line{}, apierr.New(apierr.ValidationError, "sku, warehouse, and source document are required")
	}
	delta, err := parseQty(m.QtyDelta)
	if err != nil {
		return Line{}, err
	}
	if delta.Sign() == 0 {
		return Line{}, apierr.New(apierr.ValidationError, "quantity must not be zero")
	}
	receipt, err := receiptType(m.MovementType, delta)
	if err != nil {
		return Line{}, err
	}
	var unit *big.Rat
	if receipt {
		if strings.TrimSpace(m.UnitCost) == "" {
			return Line{}, apierr.New(apierr.ValidationError, "unit cost is required on a receipt")
		}
		unit, err = parseQty(m.UnitCost)
		if err != nil {
			return Line{}, apierr.New(apierr.ValidationError, "unit cost is not a decimal")
		}
		if unit.Sign() < 0 {
			return Line{}, apierr.New(apierr.ValidationError, "unit cost must not be negative")
		}
		unit = roundHalfUp(unit, 6)
	}
	if err := lockSKU(ctx, tx, p.CompanyID, m.SKUID, m.WarehouseID); err != nil {
		return Line{}, err
	}
	if _, err := s.catalog.Item(ctx, tx, p.CompanyID, m.SKUID); err != nil {
		return Line{}, err
	}
	if _, err := s.catalog.Warehouse(ctx, tx, p.CompanyID, m.WarehouseID); err != nil {
		return Line{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO erp.stock_balances (company_id, sku_id, warehouse_id, qty, value)
		VALUES ($1, $2, $3, 0, 0)
		ON CONFLICT (company_id, sku_id, warehouse_id) DO NOTHING`, p.CompanyID, m.SKUID, m.WarehouseID); err != nil {
		return Line{}, err
	}
	onHand, onValue, err := loadBalance(ctx, tx, p.CompanyID, m.SKUID, m.WarehouseID)
	if err != nil {
		return Line{}, err
	}
	reserved, err := sumReserved(ctx, tx, p.CompanyID, m.SKUID, m.WarehouseID)
	if err != nil {
		return Line{}, err
	}
	var cost, valueDelta *big.Rat
	if receipt {
		cost = unit
		valueDelta = roundHalfUp(mul(delta, unit), 6)
	} else {
		if onHand.Sign() == 0 || sub(onHand, neg(delta)).Sign() < 0 {
			return Line{}, apierr.New(apierr.NegativeStock, "on-hand would become negative")
		}
		avg := quo(onValue, onHand)
		cost = roundHalfUp(avg, 6)
		out := neg(delta)
		if out.Cmp(onHand) == 0 {
			valueDelta = neg(onValue)
		} else {
			valueDelta = roundHalfUp(neg(mul(out, cost)), 6)
		}
	}
	newQty := add(onHand, delta)
	newValue := add(onValue, valueDelta)
	if newQty.Sign() < 0 || newValue.Sign() < 0 || newQty.Cmp(reserved) < 0 {
		return Line{}, apierr.New(apierr.NegativeStock, "on-hand would become negative")
	}
	if newQty.Sign() == 0 {
		newValue = new(big.Rat)
	}
	line, err := insertLine(ctx, tx, p, m, delta, cost, valueDelta)
	if err != nil {
		return Line{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE erp.stock_balances SET qty = $4::numeric, value = $5::numeric
		WHERE company_id = $1 AND sku_id = $2 AND warehouse_id = $3`,
		p.CompanyID, m.SKUID, m.WarehouseID, formatQty(newQty), formatQty(newValue)); err != nil {
		return Line{}, mapWrite(err)
	}
	if _, err := audit.EmitAs(ctx, tx, p, audit.Event{
		Type: "stock.moved", ReferenceType: "stock_ledger_line", ReferenceID: line.ID.String(),
		After: map[string]string{
			"sku_id": line.SKUID.String(), "warehouse_id": line.WarehouseID.String(),
			"qty_delta": line.QtyDelta, "unit_cost": line.UnitCost, "movement_type": line.MovementType,
			"source_doc_id": line.SourceDocID,
		},
	}); err != nil {
		return Line{}, err
	}
	return line, nil
}

func insertLine(ctx context.Context, tx pgx.Tx, p rls.Principal, m Move, delta, cost, valueDelta *big.Rat) (Line, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(32101, hashtext($1))`, p.CompanyID.String()); err != nil {
		return Line{}, err
	}
	var seq int64
	var prev string
	err := tx.QueryRow(ctx, `
		SELECT chain_seq, hash FROM erp.stock_ledger_lines
		WHERE company_id = $1 ORDER BY chain_seq DESC LIMIT 1`, p.CompanyID).Scan(&seq, &prev)
	if errors.Is(err, pgx.ErrNoRows) {
		seq, prev, err = 0, "", nil
	}
	if err != nil {
		return Line{}, err
	}
	var at time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&at); err != nil {
		return Line{}, err
	}
	id := uuid.New()
	line := Line{
		ID: id, SKUID: m.SKUID, WarehouseID: m.WarehouseID,
		QtyDelta: formatQty(delta), UnitCost: formatQty(cost), ValueDelta: formatQty(valueDelta),
		MovementType: m.MovementType, SourceDocID: m.SourceDocID,
	}
	body := map[string]any{
		"id": id.String(), "company_id": p.CompanyID.String(),
		"sku_id": m.SKUID.String(), "warehouse_id": m.WarehouseID.String(),
		"qty_delta": line.QtyDelta, "unit_cost": line.UnitCost, "value_delta": line.ValueDelta,
		"movement_type": m.MovementType, "source_doc_id": m.SourceDocID,
		"created_by": p.UserID, "occurred_at": at.UTC(),
	}
	canonical, hash, err := canon.MarshalAndHash(body, prev)
	if err != nil {
		return Line{}, err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO erp.stock_ledger_lines (
			id, company_id, sku_id, warehouse_id, qty_delta, unit_cost, value_delta,
			movement_type, source_doc_id, created_by, occurred_at, canonical, chain_seq, prev_hash, hash)
		VALUES ($1,$2,$3,$4,$5::numeric,$6::numeric,$7::numeric,$8,$9,$10,$11,$12,$13,$14,$15)`,
		id, p.CompanyID, m.SKUID, m.WarehouseID, line.QtyDelta, line.UnitCost, line.ValueDelta,
		m.MovementType, m.SourceDocID, p.UserID, at, string(canonical), seq+1, prev, hash)
	if err != nil {
		return Line{}, mapWrite(err)
	}
	return line, nil
}

func receiptType(kind string, delta *big.Rat) (bool, error) {
	switch kind {
	case MoveReceipt, MoveOpening, MoveProduce, MoveJobIn:
		if delta.Sign() <= 0 {
			return false, apierr.New(apierr.ValidationError, "receipt quantity must be positive")
		}
		return true, nil
	case MoveDelivery, MoveConsume, MoveWriteOff, MoveJobOut:
		if delta.Sign() >= 0 {
			return false, apierr.New(apierr.ValidationError, "issue quantity must be negative")
		}
		return false, nil
	case MoveAdjustment:
		if delta.Sign() == 0 {
			return false, apierr.New(apierr.ValidationError, "quantity must not be zero")
		}
		return delta.Sign() > 0, nil
	default:
		return false, apierr.New(apierr.ValidationError, "movement type is not valid")
	}
}

func allowedClasses(p rls.Principal, seeCost bool, class string) ([]string, bool, error) {
	salesOnly := !seeCost && hasRole(p.Roles, "sales_agent")
	if salesOnly {
		switch class {
		case "", ClassFinished, ClassBoth:
			return []string{ClassFinished, ClassBoth}, false, nil
		case ClassRaw:
			return nil, true, nil
		default:
			return nil, false, apierr.New(apierr.ValidationError, "item class is not valid")
		}
	}
	switch class {
	case "":
		return []string{ClassRaw, ClassFinished, ClassBoth}, false, nil
	case ClassFinished:
		return []string{ClassFinished, ClassBoth}, false, nil
	case ClassRaw:
		return []string{ClassRaw, ClassBoth}, false, nil
	case ClassBoth:
		return []string{ClassBoth}, false, nil
	default:
		return nil, false, apierr.New(apierr.ValidationError, "item class is not valid")
	}
}

func hasRole(roles []string, want string) bool {
	for _, r := range roles {
		if r == want {
			return true
		}
	}
	return false
}

func lockSKU(ctx context.Context, tx pgx.Tx, company, sku, wh uuid.UUID) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(32100, hashtext($1))`, company.String()+"|"+sku.String()+"|"+wh.String())
	return err
}

func loadQty(ctx context.Context, tx pgx.Tx, company, sku, wh uuid.UUID) (*big.Rat, *big.Rat, error) {
	if _, err := tx.Exec(ctx, `
		INSERT INTO erp.stock_balances (company_id, sku_id, warehouse_id, qty, value)
		VALUES ($1, $2, $3, 0, 0)
		ON CONFLICT (company_id, sku_id, warehouse_id) DO NOTHING`, company, sku, wh); err != nil {
		return nil, nil, err
	}
	onHand, _, err := loadBalance(ctx, tx, company, sku, wh)
	if err != nil {
		return nil, nil, err
	}
	reserved, err := sumReserved(ctx, tx, company, sku, wh)
	if err != nil {
		return nil, nil, err
	}
	return onHand, reserved, nil
}

func loadBalance(ctx context.Context, tx pgx.Tx, company, sku, wh uuid.UUID) (*big.Rat, *big.Rat, error) {
	var qty, value string
	err := tx.QueryRow(ctx, `
		SELECT qty::text, value::text FROM erp.stock_balances
		WHERE company_id = $1 AND sku_id = $2 AND warehouse_id = $3
		FOR UPDATE`, company, sku, wh).Scan(&qty, &value)
	if err != nil {
		return nil, nil, err
	}
	q, err := parseQty(qty)
	if err != nil {
		return nil, nil, err
	}
	v, err := parseQty(value)
	if err != nil {
		return nil, nil, err
	}
	return q, v, nil
}

func sumReserved(ctx context.Context, tx pgx.Tx, company, sku, wh uuid.UUID) (*big.Rat, error) {
	var s string
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(qty), 0)::text FROM erp.stock_reservations
		WHERE company_id = $1 AND sku_id = $2 AND warehouse_id = $3 AND status = 'active'`,
		company, sku, wh).Scan(&s)
	if err != nil {
		return nil, err
	}
	return parseQty(s)
}

func (s *Service) ledger(ctx context.Context, p rls.Principal, sku *uuid.UUID, from, to time.Time) ([]Line, bool, error) {
	var lines []Line
	var seeCost bool
	err := s.inTx(ctx, p, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT erp.permission_scope('field:cost:read') IS NOT NULL`).Scan(&seeCost); err != nil {
			return err
		}
		var skuArg, fromArg, toArg any
		if sku != nil {
			skuArg = *sku
		}
		if !from.IsZero() {
			fromArg = from
		}
		if !to.IsZero() {
			toArg = to
		}
		rows, err := tx.Query(ctx, `
			SELECT id, sku_id, warehouse_id, qty_delta::text, unit_cost::text, value_delta::text,
			       movement_type, source_doc_id
			FROM erp.stock_ledger_lines
			WHERE company_id = $1
			  AND ($2::uuid IS NULL OR sku_id = $2)
			  AND ($3::timestamptz IS NULL OR occurred_at >= $3)
			  AND ($4::timestamptz IS NULL OR occurred_at < $4)
			ORDER BY occurred_at, chain_seq`, p.CompanyID, skuArg, fromArg, toArg)
		if err != nil {
			return err
		}
		defer rows.Close()
		lines = []Line{}
		for rows.Next() {
			var line Line
			if err := rows.Scan(&line.ID, &line.SKUID, &line.WarehouseID, &line.QtyDelta, &line.UnitCost, &line.ValueDelta, &line.MovementType, &line.SourceDocID); err != nil {
				return err
			}
			lines = append(lines, line)
		}
		return rows.Err()
	})
	return lines, seeCost, err
}

func mapWrite(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return apierr.New(apierr.Conflict, "stock row already exists")
		case "23514":
			return apierr.New(apierr.NegativeStock, "on-hand would become negative")
		case "42501":
			return apierr.New(apierr.Immutable, "stock ledger lines are immutable")
		}
	}
	if strings.Contains(err.Error(), "immutable") {
		return apierr.New(apierr.Immutable, "stock ledger lines are immutable")
	}
	return fmt.Errorf("stock: %w", err)
}
