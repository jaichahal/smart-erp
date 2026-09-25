// Package stock is the inventory ledger: moving average per SKU per warehouse,
// reservations, and the availability query.
//
// Valuation is moving average. A receipt-type line adds quantity at its own
// unit cost and the average is value divided by quantity. An issue takes the
// current average and does not recompute it. Receipt layers are not stored.
//
// Masters P2.2 owns erp.skus and erp.warehouses. New reads those tables.
// RegisterItem and RegisterWarehouse also mirror into the snapshot tables
// the ledger balance foreign keys still use.
package stock
