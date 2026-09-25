# 08 Acceptance Tests

Invariants stated as one-line assertions. Each is automated (unit, integration, contract, or end-to-end) and named in CI by its ID. Task acceptance in `07` references these IDs. Add new IDs; never renumber.

## A Identity and access

- A1 Two failed logins with different causes (wrong password, disabled user, unknown user, MFA required) return byte-identical bodies and status.
- A2 Every failed login writes an audit row that exists after the request's transaction is rolled back.
- A3 A request signed by a device key other than the one enrolled for the refresh token is rejected with `DEVICE_MISMATCH`.
- A4 A reused refresh token revokes the device and every token issued to it.
- A5 An access token older than its lifetime is rejected with `TOKEN_EXPIRED`; a revoked one with `TOKEN_REVOKED`; revocation wins when both apply.
- A6 A step-up-required action without a valid `step_up_token` returns `STEP_UP_REQUIRED`; a token is single-use and expires in two minutes.
- A7 Rate limiting returns the standard error envelope with `RATE_LIMITED`.
- A8 Lockout after the configured failures prevents login even with the correct password until unlock.
- A9 Ending a session from another device stops the first device's next request.
- A10 MFA-mandatory roles cannot complete login without a second factor.
- A11 A Sales Agent cannot read cost or margin fields on any endpoint, including embedded objects and exports.
- A12 A Sales Agent listing customers sees only their territory; the total count equals the number of rows returned across pages.
- A13 An unknown role resolves to the least-privileged persona and no privileged tab or endpoint.
- A14 Assigning an incompatible role pair from the SoD matrix requires an override approval and is audited.
- A15 A disabled user remains resolvable in history and cannot authenticate.
- A16 An access review with an unconfirmed user past the grace period appears on the exceptions report.
- A17 Every error response, including gateway and 429, matches the error envelope schema.
- A18 A repeated request with the same `Idempotency-Key` and body returns the stored response; a different body returns 409.
- A19 A mutation with a stale `If-Match` returns 409 with the current state.
- A20 `/status` reports last backup, chain verification, bank feed, queue depth, and clock drift, and is denied to non-System-Managers.
- A21 Console sessions from outside the IP allow-list are refused when the allow-list is enabled.
- A22 Session count per user is capped; the oldest is ended when exceeded.
- A23 SoD matrix changes are approvable documents and audit events.
- A24 Access review report lists every enabled user with roles and last login.

## B Immutability and audit chain

- B1 UPDATE or DELETE on any immutable table by the application role fails at the database.
- B2 UPDATE or DELETE via a superuser bypass is logged by the DDL or pgaudit sink within one minute (integration test with a sink stub).
- B3 A migration that alters an immutable table's trigger fails CI without the labelled exception.
- B4 Every immutable row's `hash` equals sha256(canonical row including child rows and ledger line ids plus `prev_hash`).
- B5 Two concurrent appends produce a linear chain with distinct `chain_seq` and no shared `prev_hash`.
- B6 Mutating any hashed field, `prev_hash`, or `hash` directly in the database is reported by the verifier with the first break identified.
- B7 The verifier does not cascade-flag valid rows after a single break.
- B8 Timestamps on immutable rows are database-assigned; a client-supplied timestamp is ignored.
- B9 The hourly anchor is written to a compliance-mode bucket and cannot be deleted or overwritten by the writer identity (attempt fails).
- B10 The anchor signature verifies with the KMS public key; the database operator identity is denied `Sign`.
- B11 A recomputed head that differs from an anchored head at the same `chain_seq` is reported as a break and pushes a Critical that quiet hours do not suppress.
- B12 An attachment whose stored SHA-256 differs from its content fails the read with an integrity event.
- B13 A registered document's stored PDF reprints byte-identical years later regardless of later print-format changes.
- B14 Configuration changes produce audit events with before and after.

## C Ledger, periods, masters, compliance

- C1 A document number is allocated only inside a successful registration; a failed registration records a void and the next number continues.
- C2 Two concurrent registrations of the same document type receive distinct consecutive numbers.
- C3 A posting into a hard-closed period is refused with `PERIOD_CLOSED`.
- C4 A posting into a prior open period by a role without the back-dating permission is refused; with it, it appears on the exceptions report.
- C5 A clock started at 16:00 on the day before the weekend with a 24-business-hour window is due at 16:00 on the second working day.
- C6 A journal with unequal debits and credits per currency cannot commit.
- C7 A posting rule change is versioned and requires approval; documents registered before the change still resolve to the old accounts on reprint and audit.
- C8 A manual journal without a reason and an attachment cannot be submitted; all manual journals appear by user on the exceptions report.
- C9 An accrual auto-reverses on the first day of the next period.
- C10 A recurring journal posts on schedule after one setup approval and stops after its end date.
- C11 Line and document discounts post to the discount account and appear on the tax invoice.
- C12 Invoice totals round to the fils with the difference posted to the rounding account; printed totals equal ledger totals.
- C13 A stock movement that would make on-hand negative is refused with `NEGATIVE_STOCK`.
- C14 A cash payment that would make a cash account negative is refused.
- C15 Master data changes create a new version; existing documents reference the prior version id.
- C16 A vendor bank-account change entered by user X cannot be approved by user X.
- C17 A customer over credit limit has orders held; a Credit Controller override is logged with reason.
- C18 A SKU floor price below which an order line is entered causes a hold; override is logged and reported.
- C19 A price agreement inside its period and quantity suppresses the floor hold.
- C20 Unit conversions round consistently between purchase, stock, production, and sales units.
- C21 A BOM change requires approval and is versioned; production entries reference the BOM version.
- C22 A SKU flagged raw material is not visible in the sales stock view; finished goods are not purchasable without the explicit both flag.
- C23 An import preview lists every rejected row with a reason and commits none until confirmed.
- C24 The Go-Live journey refuses to unlock the company while the opening trial balance is non-zero.
- C25 Opening customer invoices import individually and age correctly by due date on day one.
- C26 Opening stock imports per SKU with cost and produces the correct moving average.
- C27 Monthly depreciation posts straight-line per asset and stops at residual value.
- C28 Asset disposal posts gain or loss and removes the asset from the register with history retained.
- C29 An expense claim is approved in the standard queue and posts expense and VAT input.
- C30 A staff advance is tracked and cleared against claims.
- C31 The asset register reconciles to the fixed asset accounts at close.
- C32 Sub-ledgers must be closed in order before the general ledger soft close is enabled.
- C33 Soft close by Accountant blocks further sub-ledger postings; hard close by Stakeholder blocks everything.
- C34 The audit adjustment period accepts only journals tagged audit adjustment and requires Stakeholder open and close.
- C35 Year-end close moves income and expense to retained earnings and the opening balances of the new year equal the closing balances.
- C36 Open items, stock by SKU at cost, and asset net book values carry forward exactly.
- C37 The VAT payment document clears the VAT control account and the filed-return document freezes the figures.
- C38 Month-end checklist items record owner and timestamp; hard close is disabled until all items are ticked.
- C39 A correction posted after a soft close lands in the current open period, never in the closed one.
- C40 The management pack generated at close is stored with a hash and identical on re-download.
- C41 A tax invoice draft missing any Article 59 field cannot be submitted; the error names the field.
- C42 The FAF export for a period validates against the FTA-prescribed structure and totals reconcile to the VAT return file.
- C43 The VAT return file is non-editable and its totals equal the VAT control accounts' movement for the period.
- C44 Every registered invoice stores all PINT AE mandatory fields.
- C45 Import receipts carry a Bill of Entry number and post reverse-charge VAT to both input and output.
- C46 The corporate tax provision applies 0 percent up to the threshold and the statutory rate above it, and non-deductible tags exclude expenses from the base.
- C47 The e-invoice XML validates against the PINT AE schema.
- C48 A provider failure queues a retry and alerts; it never marks the invoice transmitted.
- C49 Transmission status is recorded as new rows; the invoice row is unchanged.
- C50 Credit notes transmit with a reference to the original invoice.

## D Approvals and corrections

- D1 The submitter cannot approve their own document at any stage.
- D2 An Accountant cannot add themselves to an approver list; the attempt is audited.
- D3 Below threshold, one configured approver registers the document; above, an independent first approver and the final gate are both required.
- D4 An initiator in Accounts cannot be first-approved by anyone in Accounts.
- D5 A rejection with an empty or whitespace reason is refused at API, UI, and workflow layers.
- D6 A client payload containing `state: Approved` does not change the evaluated state; the server restores decision fields from storage.
- D7 Two concurrent approvals: exactly one succeeds; the other receives the decided state and `ALREADY_DECIDED`, not an error.
- D8 Every transition writes an audit event; every refusal writes one that survives the aborted transaction.
- D9 The posting token is single-use and expires; a second use or an expired token is refused and audited.
- D10 A draft edited after approval fails registration because the posted hash differs from the approved hash.
- D11 Delegation is time-boxed, logged, visible to the requester, and impossible for the final gate.
- D12 Fraud hints appear on the card when thresholds are met and are configurable.
- D13 The approval card shows when the approver approved the original document being corrected.
- D14 Step-up is required at or above threshold and for bank changes, payment release, corrections, and break-glass.
- D15 A cosmetic correction creates an annotation and no ledger effect.
- D16 A partial correction creates a credit or debit note for the difference referencing the original.
- D17 A structural correction creates a reversal and a replacement, all three linked, original marked reversed and still printable.
- D18 A duplicate-posting correction creates a reversal only.
- D19 A quantity reduction on a delivered invoice requires a return document; the wizard refuses otherwise.
- D20 A correction of an invoice with allocated receipts moves the allocation to the replacement.
- D21 A correction never posts into a closed period; the replacement carries the correct supply date.
- D22 Correction requests run the matrix on the larger of original amount and net change; every correction needs at least one approver.
- D23 A user cannot register a document above their posting tolerance even if they may create it.
- D24 A blocking validation rule prevents submission and names the rule; an advisory rule warns.
- D25 A receipt inside the early-payment window applies the discount and posts it; outside it does not.
- D26 Break-glass elevation pushes a Critical to all Stakeholders regardless of preferences and appears on the exceptions report.
- D27 Correction rate per user, customer, and vendor appears on the monthly exceptions report.
- D28 Manual journals and back-dated postings appear on the exceptions report with user and reason.
- D29 Bypass attempts (direct write to a registered row) are audited with actor and refused.
- D30 A repeated-rejection chain (three or more) is flagged on the card and the exceptions report.

## E Notifications, real time, analytics, journeys

- E1 A business write and its outbox row commit atomically; a crash between them leaves neither.
- E2 An outbox event is delivered at least once and consumers deduplicate by `event_id`.
- E3 Push payloads are data-only; a message with a `notification` block fails schema validation in CI.
- E4 A payload with an amount and no currency is refused before enqueue.
- E5 The actor and disabled users are never recipients; role-derived recipients merge with explicit ones.
- E6 No preference row means opted in; quiet hours suppress all but Critical; the weekly digest batches FYI events.
- E7 A provider response of unregistered or invalid token deletes the token; tokens unseen for 60 days are pruned.
- E8 Delivery failure records a Failed row with error and retries with backoff; email fallback creates its own row.
- E9 An action taken on one surface clears the item on a second connected surface within one second.
- E10 An approve from the shade fetches the live document and refuses to commit if the state version changed.
- E11 Alert dispatch failure never rolls back the business write.
- E12 Alert rules in blocking mode prevent submission; advisory rules notify only.
- E13 Journey state from the client cannot change server-evaluated permissions or workflow state.
- E14 An await step reports pending honestly and stops the run on rejection.
- E15 A journey instance is resumable after the server restarts and after days.
- E16 Trial balance debits equal credits for any date.
- E17 AR aging total equals the receivable control account balance; AP aging equals payables.
- E18 Stock valuation report equals the inventory accounts' balance.
- E19 The Day Book for a date lists every registered document and journal with that posting date.
- E20 Party ledger running balance ends at the statement balance.
- E21 A saved view reproduces identical rows and totals when scheduled by email.
- E22 Tally XML export imports into Tally without errors for a sample month (manual acceptance).
- E23 The auditor bundle contains ledgers, sub-ledgers, attachments, and the audit chain for the range, and the chain verifies standalone.
- E24 The exceptions report includes every control event category defined in the spec.
- E25 A snapshot carries `as_of_date` and `computed_at` and a hash; each section has its own `as_of`.
- E26 Runway equals cash divided by trailing three-month net burn; a non-positive burn renders as cash-positive, never a division.
- E27 The forecast low point and date equal the minimum of the projected balance series.
- E28 Every section figure links to a list whose total equals the figure.
- E29 A snapshot is immutable once published; a new computation produces a new version.
- E30 The on-device PDF renders from the cached snapshot without network and prints the snapshot hash in the footer.
- E31 Absent data renders as a dash with a reason; no zero is fabricated in any section.
- E32 The management pack push deep-links to the pack, not the home screen.
- E33 DSO, DPO, DIO, and cash conversion cycle match the documented formulas on a fixture month.
- E34 Margin per SKU uses produced cost for finished goods, not raw purchase price.
- E35 Receivable concentration flags a customer above the configured share.
- E36 Debtor payment performance equals average days from invoice due to receipt per customer.

## F Mobile

- F1 Credentials exist only in Keystore or Keychain; a filesystem scan of the app sandbox finds no token material.
- F2 A release build refuses cleartext and refuses a certificate not in the pin set; pin rotation succeeds via the backup pin.
- F3 `allowBackup` is false; the offline store and caches are excluded from device transfer.
- F4 Logout and 401 wipe every local cache; a second user on the same device never sees the first user's data.
- F5 The app locks after the configured idle period for the configured roles and unlocks with biometric or PIN.
- F6 A rooted or jailbroken device shows a notice and disables write actions.
- F7 Release builds contain no debug logging; debug builds redact Authorization headers and auth request bodies.
- F8 Token expiry is enforced locally; a refresh happens before expiry; a failed refresh returns to login once.
- F9 Push token registers on login and on rotation, is idempotent for an unchanged token, and unregisters on logout and 401; unregister failure does not block logout.
- F10 A data-only push renders a local notification on the correct severity channel with the correct actions for the recipient's allowed actions.
- F11 Approve from the shade opens the sheet, fetches live state, requires biometric, and shows the decided state if beaten.
- F12 Reject from the shade cannot commit without a reason.
- F13 An unauthenticated deep link tap is stashed and resumed after login to the exact target.
- F14 Unknown severity renders as HIGH; an unparseable push shows nothing and does not crash.
- F15 The stock view never contains cost or margin fields for a Sales Agent, verified by inspecting the parsed payload.
- F16 An order below floor shows the hold warning before submit and submits as held.
- F17 An offline order is saved to the outbox with an idempotency key, shown as pending reservation, and replays exactly once on reconnect.
- F18 A server rejection on replay is shown to the agent with the reason and the local copy retained for reference.
- F19 The order timeline reflects each downstream document within one second while online.
- F20 The driver trip works with no network from first stop to last and syncs all captures on reconnect.
- F21 A delivery capture includes signature, photo, time, and location; the delivery-note clock closes on sync.
- F22 Partial delivery records shortfall and reason and creates a backorder.
- F23 Cold start to a useful home is under two seconds on the reference device.
- F24 All list screens hold 60 frames per second on the reference device with 1,000 rows.
- F25 The receivables list filters and aging chips match the server payload; totals equal the sum of rows.
- F26 A customer statement shares through the platform share sheet as PDF.
- F27 A cheque receipt requires number, bank, date, and photo before submit.
- F28 A receipt hand-over produces a receipt number shown on screen.
- F29 Arabic locale renders RTL layouts with correct number formatting and no clipped labels on any prototype screen.
- F30 A stock count cannot be submitted by a user whose only role is Accountant.
- F31 A production entry proposes BOM consumption and flags consumption above allowance before submit.
- F32 Counter and supervisor screens work offline and sync.
- F33 The brief shows every section in the specified order with its own `as_of` chip and live or cached state.
- F34 Each brief figure opens its list in one tap.
- F35 The brief renders from the last snapshot with no network and labels it cached.
- F36 The management pack renders on device from the same snapshot as the screen; figures match.
- F37 Dark mode and dynamic type produce no truncated figures on the brief.
- F38 A push for a section deep-links to that section and shows pushed versus live values if they differ.

## G Console

- G1 Every form is completable with keyboard only; Tab and Enter order follows the visual order.
- G2 The command bar finds documents by number, amount, cheque number, party, and TRN.
- G3 Live badges on the work queue update within one second of a server event.
- G4 The approval split view commits with the same sheet and safeguards as mobile.
- G5 Admin settings show current value, last change, and history; sensitive changes route to approval.
- G6 The notification drawer mirrors the mobile notification centre groups and actions.
- G7 Print preview renders the exact stored PDF for registered documents and a preview for drafts.
- G8 Print format versions are recorded on documents; changing a format does not alter existing reprints.
- G9 Day Book, Cash Book, and Bank Book drill to the document in one click.
- G10 Saved views persist filters, grouping, and columns per user.
- G11 A scheduled report emails the saved view on time with identical totals.
- G12 Duplicate-last and voucher templates prefill a document in one action.
- G13 The correction wizard shows before and after, proposed documents, and reality checks before submit.
- G14 The document flow graph shows all ancestors and descendants and opens any node.
- G15 The work queue lists every category in R15.2 and each row opens in place.
- G16 Bulk print of fifty invoices produces fifty stored PDFs in one action.
- G17 Autocomplete on party and SKU responds within 100 ms on 10,000 records.
- G18 A registered document shows Correct and Annotate and no Edit.
- G19 The allocation screen allocates across invoices by keyboard and applies early-payment discount inside the window.
- G20 Payment reconciliation allocates existing receipts without creating a bank movement.
- G21 Bulk approve of petty cash vouchers respects per-voucher SoD checks.
- G22 The inbound mailbox queue shows draft supplier invoices with the source email attached.
- G23 Three-way match mismatches are highlighted with the tolerance and blocked from posting.

## H Stock and production

- H1 Available equals on-hand minus active reservations at all times, verified after a randomised sequence of orders, deliveries, and releases.
- H2 Moving average recomputes on every receipt-type movement and never on issues.
- H3 A reservation is consumed by its delivery note and cannot be consumed twice.
- H4 Two agents ordering the last unit: exactly one reservation succeeds.
- H5 Stock ledger lines are immutable; adjustments are new lines.
- H6 Availability payloads for Sales Agent contain no cost fields.
- H7 A production entry consumes raw material and receives finished goods at summed cost per good unit.
- H8 Consumption above BOM plus allowance requires approval and posts production variance.
- H9 A production draft older than the window flags Stakeholders.
- H10 Yield percentage equals produced units over BOM-expected units for consumed input.
- H11 Work in progress is zero at every period end.
- H12 Margin per finished SKU uses produced cost.
- H13 A count variance posts only after Stakeholder approval.
- H14 A write-off requires a reason code and posts to the mapped expense account; above threshold it requires approval.
- H15 Reorder suggestions appear when available falls below the reorder level.
- H16 Dead stock lists SKUs with no movement for 90 days with carrying value.
- H17 Stock ageing bands allocate quantities by receipt date.
- H18 The monthly per-SKU report reconciles opening plus in minus out plus adjustments to closing.
- H19 Job work material out reduces on-hand at the main location and increases it at the job-worker location; material in reverses.
- H20 Negative stock is impossible under concurrent operations.

## I Operations

- I1 Boot fails with a named list when any required secret is missing.
- I2 A backup writes archives, then the manifest atomically; an interrupted run leaves no manifest.
- I3 The manifest's per-file SHA-256 and aggregate checksum verify; counts and ledger totals are recorded.
- I4 Restore refuses on each named integrity failure; `force` overrides only site mismatch.
- I5 Post-restore counts and ledger totals are re-derived and compared; the chain verifies against an anchor at or before the restore point.
- I6 Restore requires Stakeholder approval before the environment serves writes and is logged.
- I7 Retention prunes only after a verified successful backup.
- I8 A failed backup alerts System Managers and Stakeholders and appears on the status page.
- I9 The DDL sink receives every schema change with role and client within one minute.
- I10 Every image in the compose file resolves for both `linux/amd64` and `linux/arm64`; the same compose file starts on the Mac with the `dev` profile and on the NUC with the `prod` profile with no service or version differences.
- I11 The compose stack restarts under systemd after a NUC reboot and every service reports healthy within five minutes.
- I12 The API is reachable from a mobile device on the internet only through the reverse proxy or tunnel; no other inbound port answers.
- I13 Disk at 85 percent, clock drift over 500 ms, WAL archive lag over 15 minutes, and UPS on battery each raise an alert that reaches the status page and a System Manager phone.
- I14 The observability sink outside the premises receives logs, metrics, and pgaudit events; stopping the NUC-local buffer loses nothing older than the buffer window.
- I15 A backup is reported successful only after the off-site copy verifies; an off-site anchor write failing for two hours raises a Critical.
- I16 WAL segments reach the off-site bucket within 15 minutes of being written under load; the status page shows the archive lag and which recovery point (15 minutes or 24 hours) is in force.
- I17 The restore runbook, executed on a second machine from the off-site bucket alone, brings up a serving stack within one business day in the rehearsal, with manifest verified, ledger totals re-derived, and the chain verified against the latest off-site anchor.
- I18 The certificate pin set accepts an automatically rotated leaf certificate; rotating the backup pin via the signed API response succeeds without an app release.
- I19 A drive whose UUID is on the allow-list is detected and mounted; a drive with an unknown UUID is ignored and logged, never written to.
- I20 A backup run with the drive missing, read-only, or below the required free space marks the drive target Missing or Failed, raises an alert to System Managers, and still reports the off-site result independently; no silent skip.
- I21 An archive on the drive is unreadable without the key: mounting the raw device on another machine yields ciphertext, and an age archive fails to decrypt with any key other than the configured one.
- I22 Re-read verification detects a single flipped byte injected into an archive on the drive after write and marks the backup Failed.
- I23 The safe-to-remove signal is emitted only after re-read verification passes and the filesystem is synced and unmounted; pulling the drive before the signal is recorded as incomplete.
- I24 Per-drive retention prunes only after the current backup on that drive verifies, and never prunes below the configured daily and weekly counts.
- I25 A drive hand-over creates an audit event with UUID, person, time, and location; a drive unseen past the rotation period appears on the exceptions report.
- I26 A returning drive whose manifest chain head does not match the anchor for its backup time is quarantined and reported; its archives are not used for a restore.
- I27 The weekly drill runs unattended from the timer in the maintenance window and produces a signed report with duration and per-proof results.
- I28 The drill fails loudly and reports the exact proof on each of: a tampered archive, an aggregate checksum mismatch, a chain break in the restored data, and an injected ledger imbalance.
- I29 The drill restores into the isolated project only: production row counts recorded before and after are identical, and no production data directory, database, or bucket prefix is opened for write.
- I30 A drill restore boots the API against the restored data and the smoke suite passes: login, list, open a registered document, render its stored PDF, run the chain verifier.
- I31 A drill overdue by more than seven days (weekly) or 35 days (monthly), or a failed drill, raises a Critical to Stakeholders and System Managers that quiet hours do not suppress.
- I32 The status page and the monthly exceptions report show the last drill result and age per target.
- I33 A production restore cannot serve writes without a recorded Stakeholder approval, and it logs a restore audit event naming backup id, target, approver, and operator.
- I34 Restoring the same backup from the external drive and from the off-site bucket produces byte-identical chain heads, and both match the off-site anchor at or before the backup time.

## K Bank and cash

- K1 CSV and MT940 statements import with line-level dedupe by bank reference.
- K2 The open-finance feed imports nightly after consent and marks the consent expiry on the status page.
- K3 Auto-match rules propose matches; an unmatched line stays visible until matched or explained.
- K4 A reconciled account shows zero difference between book and statement at the reconciliation date.
- K5 Reconciliation status per account gates the month-end checklist item.
- K6 Payment reconciliation never creates a bank movement.
- K7 A matched line cannot be unmatched without a reason and an audit event.
- K8 Bank feed failure alerts and appears on the status page.
- K9 Petty cash vouchers post against the custodian's account and cannot exceed the float.
- K10 A contra posts cash to bank and bank to cash correctly.
- K11 Every cheque number is in exactly one state; a gap is reported as an exception.
- K12 A stopped cheque cannot clear.
- K13 Facilities show utilisation against limit and post interest on schedule.
- K14 A payment release above threshold requires a second Stakeholder and step-up.
- K15 The bank file matches the bank's format on a sample and its total equals the run total.
- K16 A released run cannot be released twice.
- K17 The thirteen-week forecast sums receivables by due and PDC date, payables by due date, committed LPOs, and recurring outflows per week.
- K18 A projected balance below the floor pushes a Critical.
- K19 The conservative case slips collections by the configured days and lowers the low point accordingly.
- K20 The daily cash position reconciles to the bank and cash accounts.

## P Purchase

- P1 A vendor cannot receive an LPO until approved.
- P2 A vendor bank change requires approval by someone other than the enterer and step-up.
- P3 A requisition converts to an LPO with lines preserved; quotes are stored against it.
- P4 An LPO above the cost-centre monthly cap is held.
- P5 A foreign-currency LPO stores the fixed rate and posts AED.
- P6 A registered LPO email is stored against the document; failure retries and logs.
- P7 LPO registration allocates a gap-free number.
- P8 An LPO to an unapproved vendor is refused.
- P9 LPO approval follows the matrix.
- P10 Goods receipt posts raw material inventory against received-not-billed at LPO cost and updates moving average.
- P11 Landed cost allocates to receipt lines by the chosen basis and revalues inventory.
- P12 Import receipts require a Bill of Entry and post reverse-charge VAT.
- P13 A supplier invoice outside tolerance on quantity or price is blocked and flagged.
- P14 A duplicate supplier invoice (same vendor and number, or same vendor and amount within seven days) is blocked until confirmed.
- P15 A supplier invoice clears received-not-billed and posts VAT input and payable.
- P16 A debit note reduces payable and inventory or received-not-billed.
- P17 A supplier advance posts and applies to the invoice when received.
- P18 Missing supplier invoice or delivery note within the business-hours window flags Stakeholders.
- P19 A short delivery creates a backorder on the LPO.
- P20 Foreign-currency settlement posts realised gain or loss.
- P21 A payment run selects due invoices, requires batch approval, and produces the bank file.
- P22 A payment run above threshold requires a second Stakeholder.
- P23 The supplier scorecard computes on-time, quantity variance, price variance, and mismatch rate from documents.
- P24 An emailed attachment becomes a draft supplier invoice with the vendor inferred from the sender and the email attached.
- P25 An inbound email from an unknown sender lands in a review queue, not a draft.
- P26 Payment release is audited with releaser and approvers.

## R Receivables

- R1 The receivables list shows open amount and PDC-covered amount separately; their sum equals the invoice balance.
- R2 Aging buckets are computed by due date, not invoice date.
- R3 A receipt allocates across invoices with an on-account remainder that appears as customer credit.
- R4 An advance is applied to a later invoice and reduces the receivable.
- R5 A PDC posts to PDC in hand on receipt, to bank on deposit, and reverses on bounce with the invoice reopened and flagged.
- R6 An issued PDC posts as a liability and clears on presentation.
- R7 A cash receipt updates the collector's dashboard within one second.
- R8 A cheque receipt requires number, bank, and date.
- R9 Unallocated receipts appear in the Accountant queue.
- R10 Receipt allocation cannot exceed the invoice balance.
- R11 A customer statement lists invoices, receipts, credits, and running balance and matches the party ledger.
- R12 Dunning reminders send at the configured days overdue and log on the invoice.
- R13 Overdue interest produces a draft debit note the Accountant chooses to issue.
- R14 The credit review queue lists customers over 90 days or over 80 percent of limit.
- R15 Receivable concentration above the share flags the customer.
- R16 Statements share by email and WhatsApp with the PDF attached.

## S Sales

- S1 A sales order reserves stock in the same transaction; a failed reservation fails the order.
- S2 An over-limit order is held and pushed to the Credit Controller; override is logged.
- S3 A below-floor line holds the order; override is logged and reported.
- S4 A price agreement in force prefills the price and suppresses the hold.
- S5 An offline order syncs with the same idempotency key exactly once.
- S6 Reserved quantity releases when an order is cancelled before delivery.
- S7 The order timeline shows every downstream document.
- S8 A held order does not reserve stock until released by the controller.
- S9 A draft invoice copies lines, prices, tax codes, and due date from the order.
- S10 A draft missing any Article 59 field cannot be submitted.
- S11 Registration posts receivable, revenue, VAT output, discount, and rounding per posting rules.
- S12 Registration stores the PDF with hash; reprint is byte-identical.
- S13 Print and gate pass are disabled before registration and enabled after.
- S14 The approved hash equals the posted hash; a change between them fails registration.
- S15 Invoice numbers are gap-free per fiscal year.
- S16 The invoice references the customer, price, and tax code versions in force at registration.
- S17 The gate pass creates the delivery note and posts COGS at moving average.
- S18 Partial delivery posts COGS for delivered quantity only and creates a backorder.
- S19 The delivery-note clock starts at registration in business hours and escalates on miss.
- S20 Driver proof of delivery closes the clock and is hashed into the chain.
- S21 A delivery note cannot exceed the reserved quantity.
- S22 The delivery confirmation event reaches the collector's app within one second.
- S23 A credit note references the original, reverses revenue and VAT, and optionally restores stock with COGS reversal.
- S24 A cash sale creates invoice and receipt in one transaction against the cash customer.
- S25 Sales targets per agent per period compute attainment from registered invoices.
- S26 Commission computes on collected invoices only and shows on the agent's dashboard.
- S27 A returned item restores stock at the original delivered cost.
