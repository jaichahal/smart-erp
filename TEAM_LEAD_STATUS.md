Team Lead Status - 2026-09-25 19:35 UTC+4 sweep

Integration (integration/e2e worktree): HEAD c9b2a46. Worktree DIRTY (uncommitted):
modified router.go (approvals mount moved inside authed group), rls.go (allow
spaces in role names), console/android/ios UI files, plus untracked migration
10042_phase0_usability.sql, home/shell client files. Owner of that dirty work
must commit or stash it before any merge. No merges done this sweep.

Tracks (branch tip / worktree / tests / blocked-on):
- ledger (task/build-ledger): tip c9b2a46, worktree has uncommitted
  internal/ledger/*.go + migrations 30100-30130. go test ./internal/ledger
  is compile-RED (c11_test expects TaxCodes, Dimensions, PostSalesInvoice,
  etc. not yet implemented). TDD red phase, files touched 19:21-19:32 today,
  active. Not stuck. Blocked-on: nothing.
- masters (task/build-masters): tip c9b2a46, worktree has uncommitted
  internal/masters/{accept_test.go,doc.go,http.go}. http.go is a NotFound stub;
  accept_test covers C15-C22, vendor gates, blacklist, moving average. TDD red
  phase by design. Not stuck. Blocked-on: nothing (it is the blocker for others).
- stock (task/build-stock): tip 7ab160a "Post stock at moving average per SKU
  per warehouse." Worktree clean. Re-verified: go test ./internal/stock ok
  (throwaway DB from erp_template, 4.8s). READY TO MERGE. Not merged because
  integration worktree is dirty and router.go overlaps semantically (stock adds
  stock.Mount inside authed group on base with approvals outside; dirty tree
  moves approvals inside). Needs Jai: confirm owner of dirty integration work,
  commit it, then merge stock. Also noted: stock uses SnapshotCatalog
  placeholder (erp.stock_skus / erp.stock_warehouses) until masters P2.2 lands;
  Catalog interface in internal/stock/masters.go is the seam masters must replace.
- sales (task/build-sales): tip c9b2a46, worktree has uncommitted
  internal/sales/*.go + migration 33100_sales.sql. Early, active (dir 19:18).
  Tests not run this sweep. Blocked-on: probably masters SKUs/customers (confirm
  with track, no action taken).
- receivables (task/build-receivables): tip c9b2a46, worktree has uncommitted
  internal/receivables/accept_test.go (726 lines). Early, active. Tests not run.
  Blocked-on: nothing confirmed.
- purchase (task/build-purchase): tip c9b2a46, worktree has uncommitted
  internal/purchase/*.go + migration 35100_purchase.sql. Early, active (19:21).
  Tests not run. Blocked-on: masters vendor/SKU approval seam (C22/P2.2).
- clients (task/build-clients): tip c9b2a46, worktree has modified
  DeviceSession/MainActivity/App/session/ContentView files plus new e2e specs
  (collection-receipt, persona-home, purchase-list, sales-order, vendor-dashboard)
  and ClientUi/Documents/Home/PurchaseList/VendorDashboard sources. Active.
  Tests not run (mobile e2e needs device harness). Blocked-on: nothing confirmed.

Plan source (task/plan-index): tip ce8cb65, includes 14-crm, 15-hr, 16-payroll,
17-vendor-control merges. No action.

Merges done: none (stock held for dirty-tree reason above).
Unblocks made: none (no track stuck; all touched within last ~30 min; gaps
already documented via Catalog interface).
E2E run: none (no Go files merged into integration/e2e).

Needs Jai:
1. Who owns the dirty integration-e2e work (approvals-auth move, rls spaces,
   client UI, 10042 migration)? Commit or revert before stock merge.
2. Confirm masters P2.2 seam: stock SnapshotCatalog -> real masters facts
   (item class, warehouse). No code changed by lead.
3. Auth semantics if the approvals-mount move is intentional (it changes which
   routes require bearer auth).
