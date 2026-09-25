Team Lead Status - 2026-09-25 21:30 UTC+4 sweep (on top of 8def507)

Integration new commits this round:
- 8def507 "Merge task/build-receivables into integration/e2e." Receivables
  86f4b46 verified green on its branch first (go test ./internal/receivables/
  ok 3.0s; bank has no own test files, covered via bank_accept_test.go).
  Router: imports merged; receivables.Mount + bank.Mount moved INSIDE the auth
  group (both handlers use rls.FromContext like sales/purchase/stock; branch
  had them outside only because it predates the auth layout).
  /receipts reconciliation: receivables OWNS POST /receipts. Removed sales'
  shadowing registration (one line in sales/http.go; handler method left for
  the sales track to remove/rename). No other route collisions (bank/*,
  receivables/*, pdcs, advances, collectors, customers/{id} all disjoint;
  authz /customers list path unaffected). Build + vet clean.

E2E-WAVE1 RESULT: known-only red. P1.7 D1-D14 401s (HOLD, untouched). No new
failures. Wave-1 also proves the full migration set (301xx/31100?/32100/33100/
34100/35100) applies cleanly on fresh throwaway DBs.

NEW FINDING (investigated, not ignored): sales + receivables package tests fail
ON INTEGRATION with goose "missing migrations" (erp_sales missing 301xx/32100
below 33100; erp_ar missing 301xx/32100/33100 below 34100). Cause: both tracks'
harnesses use SHARED named DBs (erp_sales, erp_ar) with goose.Up instead of
kit/testdb throwaways (backend QA already flagged erp_sales). Every merged
migration band breaks them. NOT a product regression: bands are disjoint new
files, both packages were green on their branches with consistent DBs, and the
full set migrates clean on fresh DBs (wave-1 proof). Shared DBs NOT wiped or
touched. Follow-ups (track-owned):
- sales: switch harness from erp_sales to testdb throwaways.
- receivables: switch harness from erp_ar to testdb throwaways.
Until then, package-green must be read on the track branches, not integration.

client-api-needs.md appended: all four rows now code-wired (receipts in
8def507). Live 404-gone checks need API restart + migrations on dev DB.
Clients QA: re-run all new-screen specs after next stack restart.

Tracks (branch tip / worktree / tests / blocked-on):
- ledger: 5faa71d merged. Clean. Done.
- masters: NEW commit 62d396e "Add master data..." (full implementation,
  TDD note for C15, worktree clean). Next sweep: verify green, merge. May also
  use shared erp_masters DB (commit msg cites ERP_TEST_DATABASE=erp_masters) -
  same coupling watch. Still the critical path (stock UseCatalog + P2.2 gates).
- stock: 7ab160a merged. Clean. Waits on masters.
- sales: f9cac2d merged. Branch-green. Integration package run red ONLY via
  stale erp_sales (see finding). Open seams 1-6 stand, plus NEW seam 7: local
  /receipts handler unregistered (receivables owns route; sales track to
  remove/rename its Receipt flow or re-point it).
- receivables: 86f4b46 MERGED as 8def507. Branch-green. Integration package run
  red ONLY via stale erp_ar (see finding). Follow-up: testdb throwaways.
- purchase: 43fdcf9 merged. Clean. Done.
- clients: tip f55adfd. No new branch commit checked this sweep.

Plan source (task/plan-index): tip ce8cb65, unchanged.

Merges done: task/build-receivables 86f4b46 -> integration/e2e 8def507.
Unblocks made: /receipts reconciliation (receivables owns; sales unregistered).
E2E: e2e-wave1 FAIL known-only (P1.7 HOLD).

QA SCALE TRIGGER: no new report this sweep; earlier FIRE stands.

Needs Jai (standing):
1. P1.7 HOLD: bearer-only vs header-auth compat.
2. Relay client agent to task/build-clients.
3. QA split decision.
4. Note: sales/receivables shared-DB harnesses (erp_sales/erp_ar) break on every
   migration merge; tracks must move to throwaways (no wipe performed).
