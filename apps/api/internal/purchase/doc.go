// Package purchase is vendor control and purchase documents (Phase 6, R22).
//
// CFO or Partner is the only approver for onboarding, adding a SKU, and
// blacklist. That gate is not delegable. The accountant prepares those files
// and cannot approve them. A raw-material line on a requisition, quote, LPO,
// or supplier invoice requires a vendor approved for that SKU.
//
// Goods receipt and supplier-invoice acceptance record a posting gap. This
// branch has no inventory, received-not-billed, VAT input, or payable journal
// interface, so those documents do not write ledger lines.
package purchase
