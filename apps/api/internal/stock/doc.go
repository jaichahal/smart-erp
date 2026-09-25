// Package stock is the inventory ledger: moving average per SKU per warehouse,
// reservations, and the availability query.
//
// Valuation is moving average. A receipt-type line adds quantity at its own
// unit cost and the average is value divided by quantity. An issue takes the
// current average and does not recompute it. Receipt layers are not stored.
//
// Masters (P2.2) are not on this branch. RegisterItem and RegisterWarehouse
// write the snapshot tables the default Catalog reads. When masters merge,
// pass that reader to New instead of SnapshotCatalog.
package stock
