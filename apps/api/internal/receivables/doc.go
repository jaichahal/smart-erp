// Package receivables posts customer receipts, allocations, and post-dated cheques.
// A cheque in hand is not bank cash. An unallocated receipt does not clear an invoice.
// Sales and the general ledger are not in this tree; invoices enter through RegisterInvoice
// and postings land on erp.book_lines by account role.
package receivables
