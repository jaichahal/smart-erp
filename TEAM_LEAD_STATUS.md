Team Lead Status - 2026-09-25 20:45 UTC+4 sweep (on top of 7c90932)

Integration: previous HEAD 7c90932, plus this status commit on top.
New commits this round:
- 7c90932 "Merge task/build-purchase into integration/e2e." Purchase 43fdcf9
  verified green first on a throwaway DB (go test ./internal/purchase/
  -count=1 ok, 11.2s; track also reports -race pass). Router conflict resolved
  keeping all mounts in decided groups: ledger outside auth; approvals + stock
  + purchase + notifications inside auth (purchase branch's outside-approvals
  line dropped per Jai's approvals-inside decision). Builds + vets clean.
  Purchase records a posting gap (no ledger journal on match/duplicate) instead
  of writing journals; ledger seam stays open.

E2E-WAVE1 RESULT: known-only red. P1.7 D1-D14 401s (HOLD, untouched). No new
failures after the purchase merge.

Backend QA sweep 1 read (base efb5ef7). Routed defects to tracks:
- masters P0: 31100 migration never applies (goose splits DO $$ block at interior
  semicolons; unterminated dollar-quote). Fix: StatementBegin/End or plain DDL.
- sales P0: package does not compile (Draft/Submit/Approve/RegisterInvoice,
  GatePass undefined). Nothing runnable until it builds.
- receivables P0: no receivables.Mount / bank.Mount; R1-R16 cannot execute.
- purchase watch item RESOLVED by 43fdcf9: TestVND1 SOD-at-Delegate now passes
  (verified on throwaway DB). Ruling recorded: delegation by/over submitter
  refused at Delegate time with SOD_VIOLATION.
- sales test hygiene flag: harness reuses shared erp_sales DB instead of
  throwaway DBs; fix once package compiles.

Clients QA sweep 1 read (branch f55adfd). Routed:
- defect 1 (missing routes): PARTIALLY CLOSED by purchase merge. Dashboard +
  LPO list now on integration; sales-orders + receipts still 404 (sales,
  receivables tracks). Clients QA re-runs after those land.
- defect 2 (journeys.spec placeholders: Delivery note, Aging, bank line, LPO
  payment, Stock count, Approval inbox, Notifications): console track, unscheduled.
- defect 3 (parallel Playwright sign-in flake): console track watch item.
- defect 4 (integration client dirs stale vs branch): needs branch merge; client
  agent should commit remaining branch work then propose merge.

QA SCALE TRIGGER: FIRED. Backend QA lists 3 P0 defects (masters, sales,
receivables) plus Clients QA lists 4 items. Evidence above.
Recommendation to parent: split Backend QA into A (ledger/masters/stock) vs B
(sales/receivables/purchase). Clients QA web-vs-mobile split not yet needed
(console done; Android/iOS pending sweep 2). Lead cannot launch agents.

Tracks (branch tip / worktree / tests / blocked-on):
- ledger: 5faa71d merged. Clean. Done.
- masters: tip c9b2a46, uncommitted pkg + 31100. RED (P0 migration). Next: fix
  31100 per backend QA defect 1, then green + merge. Blocks stock placeholder.
- stock: 7ab160a merged. Clean. Waits on masters UseCatalog.
- sales: tip c9b2a46, uncommitted pkg + 33100. RED (P0 compile). Next: invoice
  methods + green; then POST /sales-orders for clients defect 1.
- receivables: tip c9b2a46, uncommitted bank/ + receivables/ + 34100. RED (P0
  mounts). Next: Mount fns + green; then POST /receipts.
- purchase: 43fdcf9 MERGED as 7c90932. Worktree clean. Done from lead view
  (posting gap documented, ledger seam open).
- clients: tip f55adfd, clean. No new direct edits on integration/e2e (only
  untracked 10042). Redirect standing.

Plan source (task/plan-index): tip ce8cb65, unchanged.

Merges done: task/build-purchase 43fdcf9 -> integration/e2e 7c90932.
Unblocks made: none (no track stuck; SOD ruling already made by purchase track).
E2E: e2e-wave1 FAIL known-only (P1.7 HOLD).

Needs Jai (standing):
1. P1.7 HOLD: bearer-only vs header-auth compat.
2. Relay client agent to task/build-clients.
3. QA split decision per trigger above.
