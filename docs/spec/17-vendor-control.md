# 17 Vendor control

Phase 6 purchase to pay already onboards a vendor and issues an LPO only to an approved vendor (P6.1, R8.1, R8.3). This file extends that same vendor. It does not add a second vendor master. Approved SKUs, blacklist, and the trade licence sit on the vendor from P2.2 and P6.1.

This is purchasing. It is not CRM. It does not belong in `14-crm.md`.

P6.5 was free. P6.4 is the last Phase 6 task in `07-tracks-and-tasks.md`. Tasks here are P6.5 to P6.9.

When these tasks are built, tests are written first and must fail before implementation, following Test-driven development in `10-execution-playbook.md`. VND1, VND2, VND3, and VND4 are the four that fail first. This change writes no application code, no migration, and no test.

## Requirements

IDs start at R22.1 so they do not collide with R1 through R21.

- R22.1 A raw-material purchase line SHALL name a vendor approved for that SKU. The documents are the requisition, the quote, the LPO, and the supplier invoice. A vendor who is approved in general and is not approved for that SKU SHALL be refused on the line. Item class `raw_material` is this strict path (R9.1).
- R22.2 A purchase line whose SKU item class is `finished_goods` or `both` SHALL keep the existing general vendor approval (R8.3, P6.1). The spec already separates item classes (R9.1). Those classes are not the per-SKU path. The approved-SKU list, the blacklist, and the trade licence SHALL be data on the existing vendor. There SHALL NOT be a second vendor master.
- R22.3 New vendor onboarding, and adding a SKU to a vendor's approved list, SHALL be approved only by a CFO or a Partner after review. The Accountant and the finance manager MAY prepare the file. They SHALL NOT approve it. No other role SHALL approve it. CFO and Partner are titles the company configures on users who already hold Stakeholder. They are not new roles (R1.2). Holding Stakeholder is not enough: the finance manager, the CTO, and any other Stakeholder fail this check. Approver, System Manager, Auditor, and every operational role fail it. This is the final gate in R2.2 and R2.9. It SHALL NOT be delegable. The approver set SHALL NOT be widened to Stakeholder, finance manager, Approver, Accountant, CTO, System Manager, or any other role. A later change that adds an approver is outside this spec until R22.3 is amended. Who is CFO and who is Partner is an audited configuration change (R3.12). After go-live, only a current CFO or Partner can change that list.
- R22.4 A vendor bank-account change SHALL stay on the existing four-eyes task (R8.1, R1.6, P2.2, P6.1, acceptance P2 and C16). The user who entered the change SHALL NOT approve it. Step-up SHALL still be required. The previous version SHALL stay for documents that already used it. This file SHALL NOT weaken that task and SHALL NOT replace it with the R22.3 gate.
- R22.5 A vendor MAY be blacklisted. Blacklist SHALL be decided only by a CFO or a Partner. It is the same final gate as R22.3: not delegable, and not open to any other role. A blacklisted vendor SHALL NOT be selected on a new LPO or a new supplier invoice. Existing open documents SHALL NOT be deleted, voided, or rewritten. Their numbers and any posted ledger lines stay as they are.
- R22.6 Every supplier invoice for a blacklisted vendor, including a historical posted invoice, SHALL be flagged on the payables list and on the document itself. The flag is containment. It SHALL be a new annotation or flag row. It SHALL NOT update the registered invoice and SHALL NOT rewrite the posted ledger.
- R22.7 A new payment of a supplier invoice flagged under R22.6 SHALL be blocked until a CFO or a Partner releases that payment. The release covers that payment only. It SHALL NOT clear the blacklist and SHALL NOT remove the flag. The supplier payment run (P6.3) and the outgoing-money second Stakeholder (R7.6) still apply. The user who prepared the vendor still cannot pay that vendor (R1.13, create vendor and pay vendor).
- R22.8 A vendor dashboard SHALL be on the React console and on both native phone apps (Android and iOS). It SHALL be visible to every persona in `06-ux-spec.md`. Actions are limited by role. CFO and Partner SHALL see the approve action and the blacklist action. Every other persona, including the finance manager, the CTO, the Accountant, and the other Stakeholder titles, SHALL see status, counts, and prices, and SHALL NOT see the approve action or the blacklist action. The API SHALL refuse those actions for anyone who is not CFO or Partner. Counts SHALL use the same permission set as the rows (R1.11). On this dashboard the prices in R22.9 are visible to every persona.
- R22.9 The dashboard SHALL show, per vendor and per SKU, how many supplier invoices are active and how many are past. Active means not posted and not closed. Past means posted or closed. A posted invoice that is still unpaid is past. An invoice counts once on the vendor and once for each SKU on its lines. A filter SHALL accept a raw-material SKU, list the vendors approved for that SKU, and drill to the best price. Best price means the lowest approved quote or price agreement for that SKU that is currently effective. The response SHALL show the source document (id, number, unit price, currency, effective window). An unapproved vendor, an unapproved quote, and a blacklisted vendor SHALL NOT be ranked as the best price. They MAY appear in a separate blocked group, with the reason. When two sources use different currencies, they SHALL NOT be compared as plain numbers. The rank uses the AED amount at the fixed rate stored on that source (R8.3, R4.2). A source with no stored rate is shown and is not selected as best.
- R22.10 Onboarding hides nothing. The reviewer of a new-vendor request or of a request to add a SKU SHALL see the trade licence, the bank account, the SKUs requested, and any prior invoices. An empty prior-invoice list SHALL be shown as empty. The trade licence SHALL be a write-once attachment on the existing onboarding approval document (R3.10, P1.9). The bank account shown here is the account on that request. A later bank-account change still follows R22.4.
- R22.11 The API SHALL refuse a vendor insert, an approved-SKU link, or a blacklist write that skips the approval request, including a direct database-style call that stores the row as already approved. The call is refused. There is no such registered endpoint. Sensitive master mutations already create an approval request (`04-api-contracts.md`). Decision fields SHALL be taken from the approval engine, not from the client (R2.5). Registration SHALL consume the single-use posting token (R2.8). Row-level security SHALL still apply (R1.10). The application role SHALL NOT have a path that inserts the approved row outside that registration. This requirement enforces the existing approval path. It does not add a path around it.

## Tasks

Format matches `07`: ID, title, track, depends on, requirements, acceptance.

### Phase 6 vendor control

Depends on the existing vendor (P2.2, P6.1), the approval engine (P1.7, R2), and, for payment hold, the supplier payment run (P6.3). The dashboard also depends on the console shell (P1.12) and both phone shells (P1.11).

- P6.5 Raw-material SKU check on the existing vendor. A requisition, quote, LPO, or supplier invoice line for a raw-material SKU names only a vendor approved for that SKU. Finished goods and both stay on general approval. No second vendor master. D. Depends: P6.1, P2.2. R22.1, R22.2. Accept: VND2, VND5.
- P6.6 CFO or Partner final gate for new vendor onboarding and for adding a SKU. Accountant and finance manager prepare and cannot approve. No other role. Not delegable. Reviewer sees trade licence, bank account, SKUs requested, and prior invoices. D, B. Depends: P6.1, P1.7. R22.3, R22.10. Accept: VND1, VND6.
- P6.7 API refuses a vendor, approved-SKU link, or blacklist write that skips the approval request. RLS and the posting token stay as they are. A. Depends: P6.6, P1.3, P1.7, P1.10. R22.11. Accept: VND8, VND9.
- P6.8 Blacklist by CFO or Partner only. New LPO and new supplier invoice refused. Existing open documents kept. Supplier invoices flagged, including posted history. New payment blocked until CFO or Partner releases that payment. Bank-account change stays the existing four-eyes task. D, B. Depends: P6.1, P6.3, P2.2. R22.4, R22.5, R22.6, R22.7. Accept: VND3, VND7, VND10 to VND13.
- P6.9 Vendor dashboard on the React console and on Android and iOS. Every persona can open it. CFO and Partner see approve and blacklist. Other personas see status, counts, and prices. Active and past counts per vendor and per SKU. Raw-material SKU filter, approved vendors, best price with source document, blocked group separate. G, F. Depends: P6.5, P6.6, P6.8, P1.11, P1.12. R22.8, R22.9. Accept: VND4, VND14 to VND17.

## Acceptance

One-line cases, same shape as `08`. Written as failing tests when the task is built. Not implemented now.

VND1, VND2, VND3, and VND4 are written first, and seen failing, before any implementation of the behaviour they name.

- VND1 An Accountant or a finance manager can prepare vendor onboarding and a SKU addition and cannot approve either, and no role other than CFO or Partner can approve them. Delegation of that decision is refused.
- VND2 A raw-material line on a requisition, a quote, an LPO, or a supplier invoice is refused when the vendor is not approved for that SKU, including when the vendor is approved in general.
- VND3 A blacklisted vendor is refused on a new LPO and on a new supplier invoice, and a new payment of that vendor's supplier invoice is refused until a CFO or a Partner releases that payment.
- VND4 The best price for a raw-material SKU is the lowest currently effective approved quote or price agreement among vendors approved for that SKU, the response names the source document, and an unapproved or blacklisted vendor is not ranked as best and is listed in the blocked group.
- VND5 A finished-goods or both SKU on a purchase document still accepts a generally approved vendor, and the write uses the existing vendor master.
- VND6 The onboarding review shows the trade licence, the bank account, the SKUs requested, and prior invoices, and shows an empty invoice list when there are none.
- VND7 A vendor bank-account change entered by one user is still refused for that same user, and still requires step-up.
- VND8 A create that stores a vendor as approved, adds an approved SKU, or sets blacklist with no approval request is refused.
- VND9 A client-supplied decision field or a client-supplied posting token does not approve a vendor, and row-level security still applies to the vendor row.
- VND10 Blacklist does not delete or rewrite an existing open requisition, quote, LPO, or supplier invoice.
- VND11 A historical posted supplier invoice for a vendor who is later blacklisted stays posted, and the payables list and the document both show the flag.
- VND12 Releasing one payment does not clear the blacklist and does not remove the invoice flag.
- VND13 A user who is not CFO or Partner cannot blacklist a vendor and cannot release a held payment.
- VND14 The vendor dashboard opens on the React console, on Android, and on iOS, for every persona in `06-ux-spec.md`.
- VND15 CFO and Partner see the approve and blacklist actions. Every other persona sees status, counts, and prices, and does not see those actions. The API refuses the actions for that other persona.
- VND16 Active and past counts per vendor and per SKU equal the supplier invoices in that pair. Past includes a posted invoice that is still unpaid.
- VND17 Choosing a raw-material SKU lists the vendors approved for it, and a vendor who is only generally approved is absent from that list.

## Test-driven development

When this is built, VND1 (approval restriction), VND2 (SKU filter), VND3 (blacklist block), and VND4 (best-price ranking) are each a failing test first. The other VND cases are written the same way before the behaviour they name. Backend cases are Go tests against the handler and Postgres. The console case is a Vitest or Playwright test. The phone cases cover both native apps. The order is the failing test, the failure for the missing behaviour, the minimum code that passes, then a refactor that keeps the assertion. This document adds none of that code.
