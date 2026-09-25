Team Lead Status - 2026-09-25 20:15 UTC+4 sweep

Integration (integration/e2e worktree): HEAD e76b090. New commits this round:
- 59f436e "Merge task/build-ledger into integration/e2e." Ledger 5faa71d
  verified green first (go test ./internal/ledger/... ok, incl. periods).
  Router auto-merged correctly: ledger.Mount outside auth, approvals + stock +
  notifications inside auth. Builds clean.
- 13066fc status file (this file).

No new uncommitted client work on integration/e2e this sweep (only untracked
10042, left alone). Nothing to commit as WIP. Standing redirect: client agent
2e4bf2a2 should work on task/build-clients, not integration/e2e. Parent relays.

E2E-WAVE1 RESULT: same known red only. P1.7 D1-D14 fail 401 (HOLD per Jai, auth
semantics untouched). No new reds after the ledger merge. Ledger merge did not
regress the wave-1 filter.

QA reviews read: qa-reviews/backend.md and clients.md do not exist yet (no QA
defect backlog). qa-reviews/client-api-needs.md lists 4 missing API routes that
keep client UI tests red (404 route not found, verified vs Docker API):
- purchase track: GET /api/v1/vendors/dashboard?sku=, GET /api/v1/lpos
- sales track: POST /api/v1/sales-orders
- receivables track: POST /api/v1/receipts
All wait on masters/ledger seams (SKUs, vendors, posting). Routed here as next
steps; Clients QA re-runs after routes exist. Also noted: one Playwright
"Too many requests" flake on role-gating spec, passed solo; rate-limit look if
it recurs.

QA SCALE TRIGGER: NOT fired. Only 1 track newly green this sweep (ledger), and
no QA defect report with 3+ open defects exists (backend.md/clients.md absent;
client-api-needs.md is a 4-item build dependency list with clear owners, not a
QA defect backlog). No split recommended yet.

Tracks (branch tip / worktree / tests / blocked-on):
- ledger: 5faa71d MERGED as 59f436e. Worktree clean. Done from lead view.
- masters: tip c9b2a46, uncommitted pkg + 31100_masters.sql. Stub RED. Next:
  implement to green, then merge. Blocks stock placeholder + purchase/sales.
- stock: 7ab160a merged earlier. Clean. Waiting on masters UseCatalog.
- sales: tip c9b2a46, uncommitted pkg + 33100. Next: POST /api/v1/sales-orders
  (client-api-needs). Blocked-on: masters seams.
- receivables: tip c9b2a46, uncommitted bank/ + receivables/ + 34100. Next:
  POST /api/v1/receipts (client-api-needs).
- purchase: tip c9b2a46, uncommitted pkg + 35100 + modified router.go (conflict
  watch at merge). Next: vendor dashboard + LPO list (client-api-needs).
  Blocked-on: masters vendor/SKU approval seam.
- clients: tip f55adfd, worktree clean. UI waits on the 4 API routes above.

Plan source (task/plan-index): tip ce8cb65, unchanged.

Merges done: task/build-ledger 5faa71d -> integration/e2e 59f436e.
Unblocks made: none (no track stuck).
E2E: e2e-wave1 FAIL known-only (P1.7 D1-D14 on HOLD).

Needs Jai (standing):
1. P1.7 401s HOLD: bearer-only approvals vs header-auth compat.
2. Relay to client agent: use task/build-clients.
3. Relay client-api-needs owners: purchase (dashboard, LPOs), sales (orders),
   receivables (receipts) after masters/ledger seams.
