# 01 Requirements

Each requirement has an ID, a statement, and where useful an acceptance note. IDs are stable; never renumber, only append. Sections map to tracks in `07-tracks-and-tasks.md`.

## R1 Platform and identity

- R1.1 The system SHALL support one company in v1 with `company_id` on every business row so that adding a second company does not require a schema rewrite.
- R1.2 Roles SHALL be data, not code. Minimum roles: Sales Agent, Collection Agent, Delivery Driver, Accountant, Approver, Credit Controller, Stock Counter, Production Supervisor, Petty Cash Custodian, Auditor (read-only), Stakeholder, System Manager. A user MAY hold several roles.
- R1.3 User identity and factors SHALL be managed by Zitadel run headless and unmodified as a separate service; the ERP API SHALL be the token issuer for its own audience. MFA SHALL be available to every user and mandatory for Approver, Stakeholder, Accountant, and System Manager roles.
- R1.4 Mobile credentials SHALL be bound to a device: the device generates a key pair in Android Keystore or iOS Secure Enclave, the server stores the public key, and every request from that device is signed. A copied token SHALL NOT authenticate from another device.
- R1.5 Access tokens SHALL be short-lived (15 minutes or less); refresh tokens SHALL rotate on use and be revocable per device. Revocation SHALL take effect on the next request.
- R1.6 Step-up authentication (second factor or biometric plus server verification) SHALL be required for: approvals at or above the configured threshold, payment release, vendor bank-account changes, corrections, break-glass, and role changes.
- R1.7 Authentication failures SHALL be indistinguishable on the wire; the real reason SHALL be written to the audit log and committed before the failure response is sent.
- R1.8 Login SHALL be rate-limited per user and per IP and SHALL honour an account lockout policy; both SHALL return the standard error envelope.
- R1.9 Users SHALL never be deleted, only disabled; historical actors SHALL remain resolvable forever.
- R1.10 Every list and detail endpoint SHALL enforce row-level security in the database (company, territory, ownership) and field-level permissions in serialisation (for example cost and margin hidden from Sales Agent).
- R1.11 Summaries and counts SHALL be computed over the same permission-filtered set as the rows they summarise.
- R1.12 A user with no recognised role SHALL resolve to the least-privileged experience and SHALL never be guessed into a privileged one.
- R1.13 A segregation-of-duties matrix SHALL exist as data listing incompatible role pairs and incompatible actions on one document (enter and approve; receive and count; create vendor and pay vendor; request correction and approve correction). The engine SHALL enforce it at the API.
- R1.14 A quarterly access review SHALL be produced listing every user, roles, last login, and require a Stakeholder confirmation per user; unconfirmed users after the grace period SHALL be flagged on the exceptions report.
- R1.15 Sessions SHALL be limited per user and listable; a user or System Manager SHALL be able to end any session.

## R2 Approval engine

- R2.1 Every governed document (sales invoice, LPO, supplier invoice, payment run, credit and debit note, vendor onboarding and bank change, production entry above wastage allowance, stock adjustment, correction request, period close, master data flagged sensitive) SHALL pass through the approval engine before registration.
- R2.2 The approval matrix SHALL be configuration per document type: an amount threshold; below it any one configured approver registers the document; at or above it an independent first approver (not in the Accounts department when the initiator is) followed by a named final-gate role. The company MAY add a voting tier ("any two of N").
- R2.3 The submitter SHALL NOT be an approver of their own document; an Accountant SHALL NOT be able to assign themselves as approver; the original approver of a document SHALL be flagged when asked to approve its correction.
- R2.4 Rejection SHALL require a non-empty reason at every layer (API, UI, workflow). Approval MAY carry an optional comment.
- R2.5 Decision fields SHALL be restored from storage before every transition is evaluated; client-supplied state SHALL never influence a gate.
- R2.6 Transitions SHALL be serialised per request with a row lock and a state version; a concurrent second decision SHALL receive the already-decided state, not an error.
- R2.7 Every transition and every refused attempt SHALL write an audit event; refused attempts SHALL be committed before the refusal is raised.
- R2.8 Posting after approval SHALL use a single-use posting token issued by the approval service and validated by the posting service; no process-global flag.
- R2.9 Delegation SHALL be allowed for the first stage only, time-boxed, logged, and visible to the requester. The final gate SHALL NOT be delegable.
- R2.10 The approval card SHALL show fraud hints: amount variance over a configured percentage against the original or the usual, round amounts above a configured value, repeated rejections in a chain, corrections raised by the same user this month, and whether the approver approved the original.
- R2.11 The content hash of the draft at submission SHALL be recorded; each decision SHALL be recorded against that hash; registration SHALL fail if the posted content does not reproduce it.

## R3 Immutability and audit

- R3.1 Every row carrying money, stock, or a decision SHALL be immutable once registered: journal headers and lines, stock ledger lines, registered documents and lines, approval decisions, audit events, notification log, number allocations, attachments metadata.
- R3.2 The application database role SHALL have INSERT and SELECT only on immutable tables and SHALL NOT own them. A separate migration role SHALL own them. Superuser SHALL be vaulted with check-out logged externally.
- R3.3 BEFORE UPDATE and BEFORE DELETE triggers SHALL reject changes on every immutable table. A DDL event trigger SHALL log every schema change to an external sink. CI SHALL refuse migrations that alter immutable tables' triggers or columns without a named exception.
- R3.4 Every immutable row SHALL carry `prev_hash` and `hash = sha256(canonical_json(row incl. child rows and referenced ledger line ids) || prev_hash)`. Appends SHALL be serialised per company so the chain cannot fork. Timestamps SHALL be database-assigned.
- R3.5 A verifier SHALL re-walk each chain nightly and on demand; a break SHALL push a Critical to all Stakeholders and appear on the exceptions report. The Auditor role SHALL be able to run the verifier.
- R3.6 The chain head (hash, count, timestamp) SHALL be written hourly to object-lock storage in compliance mode with seven-year retention, signed with a KMS key the database operator cannot use, and emailed daily to Stakeholders and the external auditor address.
- R3.7 Corrections SHALL be new documents referencing the original. There SHALL be no cancel or edit operation on registered rows. See R11.
- R3.8 Master data used by documents (customer, vendor, SKU, price, credit limit, tax code, bank account, BOM, posting rules) SHALL be effective-dated; a registered document SHALL reference the version it used.
- R3.9 The rendered tax invoice PDF SHALL be stored at registration with its hash on the document.
- R3.10 Attachments SHALL be stored write-once with SHA-256 recorded on the referencing row and verified on read.
- R3.11 Backups SHALL go to write-once storage; a restore SHALL be an approved, logged event and SHALL NOT be declared complete until the chain head matches an externally anchored head and ledger debits equal credits.
- R3.12 Every configuration change SHALL be an audit event with before and after.
- R3.13 Records and attachments SHALL be retained a minimum of seven years; nothing SHALL be hard-deleted.
- R3.14 A break-glass path for System Manager SHALL exist, SHALL push a Critical to every Stakeholder that preferences cannot suppress, and SHALL appear on the exceptions report.

## R4 Accounting core

- R4.1 The ledger SHALL be double-entry; a posting SHALL be rejected by a database constraint if debits do not equal credits per posting per currency.
- R4.2 The ledger currency SHALL be AED in v1. Foreign-currency documents SHALL carry a fixed exchange rate and post AED; realised gain or loss SHALL post on settlement.
- R4.3 A chart of accounts, tax codes (standard 5 percent, zero-rated, exempt, reverse charge, out of scope), cost centres, and configurable accounting dimensions SHALL be seeded for a UAE trading company and editable with four-eyes.
- R4.4 Posting rules SHALL be configuration: document type plus tax code plus item class plus dimension resolves to accounts; rules are versioned and approved.
- R4.5 Document numbers SHALL be one gap-free sequence per document type per fiscal year, allocated inside the registration transaction; voided numbers SHALL be recorded as void and never reused.
- R4.6 Fiscal periods SHALL exist with soft close (Accountant) and hard close (Stakeholder). Nothing SHALL post to a hard-closed period. Posting to a prior open period SHALL require a named role and appear on the exceptions report.
- R4.7 Sub-ledgers (receivables, payables, stock) SHALL close in sequence before the general ledger.
- R4.8 A thirteenth audit-adjustment period per year SHALL exist, opened and closed by Stakeholder approval, accepting only journals tagged as audit adjustments.
- R4.9 Year-end SHALL close income and expense to retained earnings and carry forward open items, stock by SKU at cost, and asset net book values.
- R4.10 Manual journals SHALL require a reason and an attachment and SHALL be listed by user on the exceptions report.
- R4.11 Accruals and prepayments SHALL be documents with automatic reversal in the following period. Recurring journals SHALL post on schedule after one approval at setup.
- R4.12 Discounts SHALL be supported at line and document level and posted to a discount account; invoice totals SHALL round to the fils with a rounding account.
- R4.13 Negative stock and negative cash SHALL be impossible to post.

## R5 Sales to cash

- R5.1 A Sales Agent SHALL see finished-goods SKUs with available, reserved, and on-hand, live over a socket while the app is open and as of last sync when offline.
- R5.2 A customer SHALL have a credit limit, payment terms, price list or price agreements, addresses, contacts, and a territory. Sales orders and invoices SHALL check credit; over-limit orders SHALL be held for the Credit Controller with a logged override.
- R5.3 Each SKU SHALL have a floor price and minimum margin. An order line below floor SHALL be held for approval; the override SHALL be logged and reported.
- R5.4 A Price Agreement per customer, SKU, period, and quantity SHALL be approvable once; orders inside it SHALL need no override.
- R5.5 A sales order SHALL reserve stock in the same transaction. Offline orders SHALL be marked pending reservation and the agent told.
- R5.6 The Accountant SHALL generate a tax invoice draft from an order with lines, prices, tax codes, and due date copied. The draft SHALL NOT be submittable until every FTA Article 59 field is present.
- R5.7 Registration SHALL allocate the number, post receivable, revenue, and VAT output, store the rendered PDF, and enable print and gate pass. Print and gate pass SHALL be disabled before registration.
- R5.8 The gate pass SHALL create the delivery note, which SHALL post cost of goods sold at moving-average cost and release the reservation. Partial delivery and backorders SHALL be supported.
- R5.9 A Delivery Driver SHALL capture signature, photo, time, and location per stop; the capture SHALL close the delivery-note clock. If not closed within the configured window (default 24 business hours) Stakeholders SHALL receive a Critical.
- R5.10 Returns and price reductions SHALL be credit notes referencing the original invoice, optionally restoring stock.
- R5.11 Cash sales to a walk-in customer SHALL be supported as invoice plus receipt in one step.
- R5.12 Sales targets per agent per period and commission computed on collected invoices SHALL be supported and visible to the agent.

## R6 Collections and receivables

- R6.1 The receivables dashboard SHALL show customer, invoice date, due date, number, open amount, PDC-covered amount, and an aging chip by due date (current, 1 to 30, 31 to 60, 61 to 90, 90 plus), filterable by due today, this week, this month, overdue, and PDC-covered, updated live.
- R6.2 Receipts SHALL support cash, current cheque, post-dated cheque (number, bank, date, image), and transfer, allocation across invoices with on-account remainder, and advances.
- R6.3 A PDC SHALL post to PDC in hand on receipt, to bank on deposit, and SHALL reverse and reopen the invoice on bounce with a flag. Issued PDCs SHALL mirror this as a liability.
- R6.4 Customer statements SHALL be printable and sendable by email and WhatsApp share; party ledgers with running balance SHALL exist for every customer and supplier.
- R6.5 Dunning reminders SHALL run on configurable days overdue and be logged on the invoice.
- R6.6 Optional overdue interest per customer SHALL produce a debit note the Accountant chooses to issue.
- R6.7 Receivable concentration above a configured share SHALL show as a risk indicator; a credit review queue SHALL surface customers over 90 days or over 80 percent of limit.

## R7 Bank and cash

- R7.1 Bank statements SHALL be imported by file (CSV, MT940) and, where consented, pulled nightly through a UAE Open Finance provider.
- R7.2 Bank reconciliation SHALL match statement lines to book entries with configurable auto-match rules; unmatched lines SHALL remain visible until cleared; reconciliation status per account SHALL be on the month-end checklist.
- R7.3 Payment reconciliation (allocating existing receipts and credits to invoices) SHALL be separate from bank reconciliation.
- R7.4 Petty cash SHALL be an account with custodian, float, and approved vouchers. Cash deposit and withdrawal (contra) SHALL be supported.
- R7.5 A cheque register SHALL account for every cheque number: issued, voided, cleared, stopped; a gap SHALL be an exception.
- R7.6 Outgoing money above a configured threshold SHALL require a second Stakeholder before a bank file or cheque is released. The payment run SHALL produce the bank's bulk-payment file format.
- R7.7 A thirteen-week cash forecast SHALL be computed nightly from receivables by due and PDC date, payables by due date, committed LPOs, and recurring outflows, with expected and conservative cases; a projected balance below the floor SHALL push a Critical.
- R7.8 Bank facilities (trust receipts, overdraft, letters of credit) SHALL be tracked with limit, utilisation, maturity, and interest posting.
- R7.9 A daily cash position SHALL be available to Stakeholders: opening, receipts, payments, PDCs due this week, closing, unreconciled count.

## R8 Purchase to pay

- R8.1 Vendors SHALL be onboarded through an approval document; bank-account changes SHALL re-enter approval and SHALL NOT be approvable by the user who entered them.
- R8.2 Purchase requisitions SHALL be raised by warehouse or production and turned into LPOs by the Accountant; two or three supplier quotes MAY be recorded against a requisition.
- R8.3 An LPO SHALL name only an approved vendor, MAY be in a foreign currency with a fixed rate, SHALL check a monthly cap per cost centre, and SHALL pass the approval matrix.
- R8.4 A registered LPO SHALL be printable and emailable to the vendor's stored address; the email SHALL be stored against the document; failures SHALL retry and log.
- R8.5 Goods receipt SHALL post raw-material inventory against received-not-billed at LPO cost and update moving average; landed cost (freight, duty, clearing) SHALL be allocatable to receipts; import receipts SHALL carry the customs Bill of Entry number and reverse-charge VAT.
- R8.6 Supplier invoices SHALL match LPO and receipt within tolerance (three-way match); a mismatch SHALL block posting and flag Stakeholders. Duplicate detection (same vendor and invoice number, or same vendor and amount within seven days) SHALL block until confirmed.
- R8.7 Attachment clocks (supplier invoice, signed delivery note) SHALL default to 24 business hours and flag Stakeholders on miss.
- R8.8 Supplier advances against an LPO, debit notes, and a payment run with batch approval SHALL be supported.
- R8.9 A supplier scorecard (on-time, quantity variance, price variance, mismatch rate) SHALL be computed from documents.
- R8.10 An inbound mailbox SHALL create draft supplier invoices from emailed attachments with the vendor inferred from the sender.

## R9 Inventory and production

- R9.1 SKUs SHALL have item class (raw material, finished goods, or explicitly both), base unit and conversions, moving-average valuation per warehouse, reorder level, floor price, and groups.
- R9.2 Every stock movement SHALL be a ledger line with quantity, cost, and reference; on-hand and available SHALL be derivable from lines.
- R9.3 A bill of materials per finished SKU SHALL list raw-material quantities and an allowed wastage percentage; BOMs SHALL be four-eyes master data.
- R9.4 A Production Entry raised by a Production Supervisor SHALL consume raw material at moving average and receive finished goods at summed cost per good unit; consumption above BOM plus allowance SHALL require Stakeholder approval and post production variance. Work in progress SHALL NOT carry across periods; a draft older than the configured window SHALL flag Stakeholders.
- R9.5 Stock count SHALL be recorded by a Stock Counter who does not hold only the Accountant role; variances SHALL post only after Stakeholder approval.
- R9.6 Stock write-offs (samples, marketing, damage, expiry) SHALL be documents with reason codes and expense accounts, approved above a threshold, distinct from count variances.
- R9.7 Reorder suggestions, dead stock (no movement 90 days), stock ageing bands, and a monthly per-SKU report (opening, in, out, adjustments, closing, yield, wastage) SHALL be available to Accountant and Stakeholders.
- R9.8 Job work (material out to and in from a third-party processor) MAY be enabled if any conversion step is outsourced.

## R10 Fixed assets and expenses

- R10.1 A fixed asset register with cost, life, monthly straight-line depreciation journal, and disposal SHALL exist.
- R10.2 Employee expense claims and staff advances SHALL be documents approved in the standard queue.

## R11 Corrections

- R11.1 A Correction Request SHALL be the only way to change the effect of a registered document. It SHALL classify the mistake as cosmetic (annotation only, no ledger effect), partial (credit or debit note for the difference), or structural (full reversal plus replacement, or reversal only for duplicates).
- R11.2 The wizard SHALL show before and after side by side, list the exact documents it will post, check physical reality (goods delivered, receipts allocated, invoice already sent) and require returns or reallocation accordingly, and require a reason.
- R11.3 Corrections SHALL post in the current open period and SHALL never reopen a closed period; the replacement SHALL carry the correct supply date for VAT.
- R11.4 Every correction SHALL require at least one approver; the matrix SHALL run on the larger of the original amount and the net change; cross-VAT-period, closed-month, and party changes SHALL go to the final gate.
- R11.5 The original SHALL remain printable as registered and SHALL display its reversal and replacement links. Correction rate per user, customer, and vendor SHALL be on the exceptions report.

## R12 Tax and compliance (UAE)

- R12.1 Tax invoices, credit notes, and debit notes SHALL satisfy VAT Executive Regulation Article 59 fields and rounding rules.
- R12.2 Every invoice SHALL store the fields required by the FTA PINT AE e-invoice data dictionary so that the 2027 ASP connector is an exporter.
- R12.3 The system SHALL generate the FTA Audit File (FAF) and a non-editable VAT return file in the FTA-prescribed formats without vendor assistance, per the FTA Requirements Document for Tax Accounting Software.
- R12.4 VAT payment to the FTA and the filed return SHALL be recorded as documents freezing the filed figures.
- R12.5 A corporate tax provision at the statutory rate above the threshold SHALL be computed quarterly; non-deductible expenses SHALL be taggable.
- R12.6 An e-invoicing connector to an Accredited Service Provider (PINT AE over Peppol) SHALL be delivered before the deadline applicable to the company's revenue band; provider failure SHALL queue and alert, never simulate.

## R13 Notifications and real time

- R13.1 Every business write SHALL commit with an outbox row; a worker SHALL fan out to WebSocket subscribers and to FCM HTTP v1 and APNs as data-only messages, with an event id for idempotence and at-least-once delivery with backoff.
- R13.2 Recipients SHALL derive from roles and configuration, exclude the actor and disabled users, and respect per-user preferences (event type, channel, quiet hours with Critical override, weekly digest, per-device toggle). Preferences SHALL never suppress the audit record.
- R13.3 A notification SHALL carry: event id, severity, document type and number, party, amount with currency, requester, waiting time, deep link, allowed actions for this recipient.
- R13.4 Actions from the notification (approve, reject, acknowledge, snooze, delegate, open) SHALL be available on Android, iOS, and console. Approve and reject SHALL open an authenticated sheet that fetches the live document, shows fraud hints, and requires biometric confirmation, or step-up at or above threshold. Reject SHALL require a reason. Actions SHALL commit against the state version; a decided request SHALL show the decision.
- R13.5 An action on any surface SHALL clear the item on every surface within one second while online.
- R13.6 Provider responses indicating an invalid or unregistered token SHALL delete the token; tokens unseen for 60 days SHALL be pruned. Register SHALL be idempotent and bound to the session user; unregister SHALL affect only the caller's token and SHALL run on logout and on 401.
- R13.7 An admin-editable rule table SHALL define alerts: document type, condition, recipients by role, channel, severity, blocking or advisory. Built-in alerts SHALL be rows in the same table.
- R13.8 Attachment and approval clocks SHALL count business hours against a company holiday calendar.

## R14 Analytics and reporting

- R14.1 A stakeholder snapshot SHALL be precomputed on a schedule and after every close, versioned, and served to the phone; each section SHALL carry `as_of_date` and `computed_at` and a link to its underlying list.
- R14.2 Sections: needs-you approvals; cash now, thirteen-week low point and date, runway months at trailing three-month net burn; gross profit and margin MTD versus last month and budget, net profit, margin movers by SKU group; receivables current, overdue, PDC-covered, top exposures, collections today; payables due this week and next, next payment run; sales today, WTD, MTD versus target by agent and SKU group; weekly and monthly receipts, payments, projected bank line with expected and conservative cases; inventory value, days on hand, dead stock, yield and wastage; exceptions summary; integrity (bank feed, reconciliation, chain verification, backup).
- R14.3 Ratio analysis (current, quick, debt to equity, gross and net margin, inventory turnover, DSO, DPO, DIO, cash conversion cycle) SHALL be computed at close.
- R14.4 Reports: trial balance, P&L by dimension, balance sheet, cash flow, AR and AP aging, sales and purchase registers, general ledger, stock valuation and ageing, Day Book, cash and bank books, party ledgers, exceptions report, access review, chain verification.
- R14.5 The management pack SHALL be generated at close, stored as files with hashes, and pushed to Stakeholders; per-section and full-pack PDF SHALL render on the device from the same snapshot with the snapshot hash in the footer.
- R14.6 Users SHALL be able to save filtered and grouped views of any list and schedule any saved view by email.
- R14.7 Exports SHALL include CSV, Excel, PDF, Tally XML for general ledger and vouchers, FAF, and an auditor bundle (ledgers, sub-ledgers, attachments, audit chain with hashes) for a date range.
- R14.8 Derived ratios SHALL never divide by zero; absent data SHALL render as absent, never as zero.

## R15 Console and mobile experience

- R15.1 Console SHALL be keyboard-first: every form navigable with Tab and Enter, autocomplete on party and SKU, duplicate-last-document, voucher templates, global command bar.
- R15.2 The Accountant home SHALL be a work queue: drafts owned, submissions waiting, clocks expiring today, unallocated receipts, unmatched bank lines, assigned items, rejected drafts with reasons.
- R15.3 Document forms SHALL show the document flow graph, attachments, activity, and comments in a side panel; registered documents SHALL show a Correct button and a print preview.
- R15.4 Bulk actions (allocate, print, email statements, approve a batch) and Excel import with preview and row-level rejection reasons SHALL be supported.
- R15.5 Mobile home per persona: management brief (R14.2 sections in order), sales agent "my day", collection list, driver trip, accountant work queue. Tabs SHALL be role-filtered with server enforcement.
- R15.6 The mobile app SHALL support English and Arabic with right-to-left layout, dark mode, dynamic type, WCAG AA contrast, 44-point targets, and haptics on commit actions.
- R15.7 Cold start to useful home SHALL be under two seconds on a mid-range Android device; lists SHALL scroll at 60 frames per second.
- R15.8 Sales agent, collector, and driver SHALL work offline with an encrypted local store, a client-keyed outbox replayed on reconnect, server-authoritative conflict resolution, and wipe on logout.
- R15.9 Every screen SHALL show `as_of` for its figures and distinguish live, cached, and offline states.
- R15.10 The management brief SHALL be usable one-handed and SHALL require no more than one tap from any figure to its underlying list.

## R16 Mobile security

- R16.1 Credentials SHALL be stored only in Android Keystore or iOS Keychain; never in preferences, files, or logs.
- R16.2 Release builds SHALL be HTTPS-only with certificate pinning to the API; cleartext SHALL be permitted in debug builds only for local hosts.
- R16.3 Release builds SHALL be minified and obfuscated; `allowBackup` SHALL be false; local stores SHALL be excluded from device transfer.
- R16.4 All local caches SHALL be wiped on logout and on 401; no cross-user fallback SHALL exist.
- R16.5 App lock (biometric or PIN) after a configurable idle period SHALL be enforced for Stakeholder, Approver, and Accountant roles.
- R16.6 Rooted or jailbroken devices SHALL be detected and downgraded to read-only with a visible notice.
- R16.7 HTTP logging in debug SHALL redact Authorization headers and request bodies of authentication endpoints.

## R17 Operations

- R17.1 Boot SHALL fail fast if any required environment variable or secret is missing, naming it.
- R17.2 Backups SHALL run on the configured cadence, write archives then a manifest atomically with per-file SHA-256 and an aggregate checksum, record counts and ledger totals, and alert System Managers and Stakeholders on failure. Retention SHALL prune only after verified success.
- R17.3 Restore SHALL verify every file against the manifest before applying, refuse on any integrity failure with a named code, and re-derive counts and ledger totals after apply.
- R17.4 A status page SHALL show last backup, last chain verification, last bank feed, queue depths, worker health, and clock drift.
- R17.5 Structured logs, metrics, and traces SHALL be shipped off-host; database DDL and elevated sessions SHALL be logged externally.
- R17.6 Every environment SHALL be reproducible from infrastructure as code; production SHALL be deployable and rollback-able without downtime for reads.
- R17.7 The backup engine SHALL support an external drive physically connected to the NUC as a second backup target for the database and MinIO objects, in addition to the off-site bucket. The drive SHALL be detected by filesystem label or UUID from a configured allow-list; an unknown drive SHALL be ignored and logged.
- R17.8 Backups on the external drive SHALL be encrypted at rest, either on a LUKS volume whose key is held by the NUC's secrets store or as age-encrypted archives, so that a removed or stolen drive is unreadable without the key. The key SHALL never be stored on the drive.
- R17.9 A drive backup SHALL carry the same manifest as the off-site backup: per-file SHA-256, aggregate checksum, row counts per immutable table, ledger debit and credit totals, and chain head. The write SHALL be verified by re-reading every archive from the drive and recomputing the hashes; a mismatch SHALL mark the backup Failed.
- R17.10 The engine SHALL emit a safe-to-remove signal (status page, LED or console message where hardware allows, and a push to the System Manager) only after verification completes and the filesystem is synced and unmounted; removing the drive before the signal SHALL be recorded as an incomplete backup.
- R17.11 Rotation SHALL support at least two drives, one on-site and one carried off-site, each with its own retention policy (default: keep the last 14 daily and 8 weekly on each drive), pruning only after the current backup on that drive verifies.
- R17.12 A missing, unrecognised, read-only, or full drive at backup time SHALL raise an alert to System Managers and appear on the status page and the exceptions report; it SHALL never be a silent skip.
- R17.13 Precedence: in phase 1 a backup SHALL be reported Successful only when the off-site copy verifies. The drive copy SHALL be reported separately as Present-and-verified, Missing, or Failed. The off-site bucket is mandatory because it survives theft, fire, and a hostile operator; the drive is a fast local restore path and an air-gapped second copy, and a missing drive for one night must not hide the fact that the off-site copy is fine, nor may a present drive excuse a failed off-site upload. Both targets present and verified is the expected nightly state and anything less is on the exceptions report.
- R17.14 A restore-drill command SHALL restore the latest backup from either target into an isolated environment on the same NUC: a separate compose project name, a separate PostgreSQL data directory and port, and a separate MinIO bucket prefix. The drill SHALL never read from or write to production data directories, databases, or buckets, and SHALL prove this by recording production row counts before and after.
- R17.15 The drill SHALL run the integrity proofs on the restored data: manifest and file checksums, row counts against the manifest, ledger debits equal credits, chain verification against the off-site anchor at or before the backup time, and a random sample of attachment hashes against object content. It SHALL then boot the API against the restored data and run the smoke suite (login, list documents, open a registered document, render a stored PDF, run the chain verifier endpoint).
- R17.16 The drill SHALL write a drill report containing target, backup id, start and end time, duration, each proof's result, and pass or fail, signed with the anchor signing key and stored in the off-site bucket and the audit chain.
- R17.17 Drills SHALL be scheduled: weekly automatic on the NUC in the maintenance window, and monthly onto a separate machine from the off-site bucket alone. The last drill's result and age per target SHALL appear on the status page and on the monthly exceptions report.
- R17.18 A failed drill, or a drill overdue by more than seven days for the weekly or 35 days for the monthly, SHALL raise a Critical to Stakeholders and System Managers that preferences cannot suppress.
- R17.19 A full production restore SHALL follow the same verified path as the drill, SHALL require Stakeholder approval before the restored environment serves writes, and SHALL be recorded as a restore audit event naming the backup id, target, approver, and operator.

## R18 Go-live and data migration

- R18.1 A Go-Live journey SHALL import the chart of accounts with opening balances, open customer invoices individually so aging is correct, open supplier invoices, opening stock per SKU with cost, opening assets, and bank balances, and SHALL refuse to unlock the company until the opening trial balance nets to zero and a Stakeholder approves.
- R18.2 Master data import (SKUs, prices, customers, vendors) SHALL validate and preview before commit with rejected rows explained.
