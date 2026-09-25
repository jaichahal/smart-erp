# 06 UX Specification

Design brief for Phase 0. Output is a token library, a component library, clickable prototypes for the ten highest-traffic flows, and usability results per persona. No persona screen is built before its prototype passes.

## Principles

1. One glance, one action. Each screen answers one question and puts the next action in thumb reach.
2. Live by default; honest about staleness. Every figure shows `as_of` and one of: live, updated just now, cached at HH:MM, offline since HH:MM.
3. Act where you are notified. Notifications are task cards with actions, subject to the safeguards in `05`.
4. Nothing invented. Absent data renders as a dash and a reason; empty series render as a sentence.
5. One design system for console and phone: tokens for colour, type, spacing, radius, elevation, icons, chart palette, and status semantics. A status chip means the same thing everywhere.
6. English and Arabic with RTL, dark mode, dynamic type, WCAG 2.2 AA, 44-point targets, haptics on commit, from the token layer up.
7. Fast: cold start to useful home under two seconds; skeletons on every fetch; optimistic UI only where the server cannot reject (acknowledge, snooze).

## Personas and jobs

| Persona | Device | Top jobs |
| --- | --- | --- |
| Stakeholder (partner, CFO, CTO) | Phone | Know the company's position in one minute; approve or reject what waits; catch exceptions |
| Accountant | Console, phone for capture and alerts | Enter and register documents fast; allocate money; close the month; fix mistakes safely |
| Sales Agent | Phone, often offline | See what can be sold at what price; take an order; track it to payment |
| Collection Agent | Phone | Know who to chase today; record what was collected; send statements |
| Delivery Driver | Phone, offline | Run the trip; prove delivery |
| Credit Controller | Console and phone | Decide holds fast with context |
| Stock Counter, Production Supervisor | Phone or tablet | Count and record production without accounting knowledge |
| Auditor | Console, read-only | Reconstruct any event; export |
| System Manager | Console | Keep it running; never touch the books |

## Navigation

Mobile tabs by persona (server-enforced):

- Stakeholder: Brief, Approvals, Activity, Reports, Profile.
- Sales Agent: My Day, Stock, Orders, Activity, Profile.
- Collection Agent: Receivables, Receipts, Customers, Activity, Profile.
- Driver: Trip, Activity, Profile.
- Accountant: Queue, Capture, Approvals, Activity, Profile.
- Multi-role users pick a persona at login and can switch from Profile.

Console: left rail (Work, Documents, Money, Stock, Reports, Admin), global command bar (Ctrl or Cmd+K: search anything, jump anywhere, create anything), right notification drawer, breadcrumbs, documents open in place.

## Management brief (Stakeholder home)

A vertically scrolling brief. Each block: a headline sentence with the figure, a spark line or mini chart, an `as_of` chip, and a tap-through to the underlying list. Period picker at the top applies to all blocks (today, week, month, quarter, year, custom). Pull to refresh; background refresh on new snapshot version.

Block order and contents:

1. Needs you. Count of approvals waiting on me; the oldest as an inline task card with Approve, Reject, Open.
2. Cash. Available cash now; thirteen-week forecast low point and date; runway in months at trailing three-month net burn, or "cash-positive" when burn is negative. Mini chart: projected balance line with floor.
3. Profit. Gross profit and margin MTD versus last month and budget; net profit; four largest margin movers by SKU group with direction.
4. Receivables. Total, split current, overdue, PDC-covered; top three exposures with days overdue; collections today. Mini chart: aging bars.
5. Payables. Total; due this week; due next week; next payment run amount and its approval state.
6. Sales. Today, WTD, MTD versus target with gap in currency; by agent and by SKU group toggles.
7. Forecast. Weekly and monthly toggle; receipts expected, payments expected, projected bank line; expected versus conservative case.
8. Stock. Inventory value; days on hand; dead stock value; production yield and wastage this month.
9. Exceptions. Counts this week for price overrides, credit overrides, corrections, missed clocks, bypass attempts, with one example line each.
10. Integrity. Last bank feed, last reconciliation, last chain verification result, last backup, each with a green or amber state.

Reports tab: ratio analysis, the full management pack, saved reports; each exportable as PDF rendered on device from the snapshot with the snapshot hash in the footer; share sheet.

## Notification interaction

Anatomy (shade, in-app feed, console drawer): severity bar; document type and number; party; amount with currency; requester; waiting time; up to two action buttons plus Open.

Shade actions:

- Approve: opens the approval sheet (see below). Below threshold: sheet plus biometric, done. At or above threshold, or for bank change, payment release, correction: sheet plus step-up.
- Reject: opens the sheet with mandatory reason, quick-pick reasons configured by the company, and free text.
- Acknowledge: FYI events; one tap; logged.
- Open: deep link to the exact document or brief section.

Approval sheet: compact, over lock screen when allowed by platform policy; shows live figures fetched now, the submission hash short form, fraud hints, who else has decided, and the action button; on submit shows the resulting state; if already decided, shows who and when.

Activity (notification centre): groups Needs my action, Waiting on others, FYI; filters by document type and severity; swipe actions matching the shade; unread badge on the tab; the requester sees each request's stage and who it waits on.

Preferences: per event type and channel; quiet hours with Critical override; weekly digest; per-device toggle.

Console: the same event stream drives the drawer, a toast for new items, live badges on the work queue, and a split-view approval inbox (list left, live document right, same sheet).

## Sales Agent screens

- My Day: customers to visit or call ordered by route or overdue, each with credit headroom, last order, open balance; my numbers (today, MTD versus target, commission on collections).
- Stock: finished goods, search and group filters, per SKU available, reserved, on hand, agreed price if an agreement exists, floor indicator ("needs approval below X") with no cost shown.
- Take order: customer, credit headroom banner; SKU scan or search; quantity with unit picker; price prefilled with instant below-floor warning; running total with VAT; submit; confirmation with order number and reservation state; offline shows "pending reservation" in amber.
- Order timeline: order, invoice, approval, gate pass, delivery, payment, with timestamps and who.

## Collection Agent screens

- Receivables: list with aging chips; filters due today, this week, this month, overdue, PDC-covered; sort by amount or age; customer statement one tap, share by WhatsApp or email.
- Record receipt: cash, cheque (number, bank, date, photo), transfer; allocate to invoices with on-account remainder; hand-over to Accountant creates a receipt number to show the customer.

## Driver screens

- Trip: stops in order; per stop items and quantities; deliver: signature on screen, photo of stamped note, automatic time and location; partial delivery with shortfall reason; fully offline; sync indicator.

## Accountant phone

- Queue: drafts, waiting, rejected with reasons, clocks expiring today.
- Capture: photograph and attach to a draft or as an annotation to a registered document; QR hand-off from console.
- Approvals to acknowledge; Activity feed.

## Console screens

- Work queue (home): sections for drafts I own, waiting on others, rejected, clocks expiring today, unallocated receipts, unmatched bank lines, assigned to me; each row opens in place.
- Day Book: chronological vouchers for a day, filter by type, drill to document; Cash Book and Bank Book with running balance.
- Document form: header, lines grid (keyboard-first, autocomplete, unit conversion, discounts), totals; right panel tabs Flow, Attachments, Activity, Comments; footer actions Save, Submit, Print preview; registered documents show Correct and Annotate.
- Document flow graph: nodes for ancestors and descendants, click to open.
- Allocation screen: receipt on the left, open invoices on the right, keyboard allocate, on-account remainder, early-payment discount applied inside window.
- Bank reconciliation: statement lines left, book entries right, suggested matches highlighted, one-key confirm, rule creation from a match.
- Month-end checklist: ordered items with owner, tick, timestamp, blockers; sub-ledger close order enforced; soft close and hard close buttons gated.
- Correction wizard: choose kind; before and after side by side; proposed documents listed; reality checks with required actions; reason; submit to approval.
- Admin: settings organised by question (who approves what; what alerts go where; how documents print; what the clocks are; who can do what); each with current value, last change, and history; changes go through approval when sensitive.
- Auditor view: read-only everything; chain verification; exports.

## Component and token requirements

- Tokens: colour (semantic: success, warning, critical, info, neutral; data palette of eight colourblind-safe series), type scale (supports Arabic and Latin), spacing, radius, elevation, motion durations.
- Components: status chip, money (with currency, RTL-safe), as-of chip, KPI block, spark line, bar, line, stacked bar, donut with legend, task card, approval sheet, list row with swipe actions, filter bar, period picker, data grid (console), command palette, form field set (text, decimal, date, party picker, SKU picker, unit picker), document flow graph, checklist item, empty state with sentence, error state with retry, offline banner.
- Chart rules: never render an empty axis; label the unit and currency; series colours from the data palette; tooltips on tap; accessible summary text for every chart.

## Prototypes to test in Phase 0

1. Take an order (agent, online and offline).
2. Approve from the shade (Stakeholder, below and above threshold).
3. Record a receipt and hand over (collector).
4. Deliver a stop (driver, offline).
5. Correct a registered invoice (accountant).
6. Allocate a receipt across invoices (accountant, keyboard only).
7. Month-end checklist to soft close (accountant).
8. Read the management brief and drill to a list (Stakeholder).
9. Triage the notification centre (Stakeholder and accountant).
10. Work the console queue for one morning of documents (accountant).

Usability protocol: one participant per persona, tasks scripted from the real month of paper, measure time on task, errors, and satisfaction; iterate until each task passes on the first attempt by a participant who has not seen the prototype.
