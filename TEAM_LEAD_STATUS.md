Team Lead Status - 2026-09-25 21:55 UTC+4 sweep (on top of 10eb050)

Integration new commits this round:
- e9ac89c "Merge task/build-masters into integration/e2e." Masters 62d396e
  verified green on its branch first on a throwaway (go test ./internal/masters/
  ok 8.8s; harness uses kit/testdb, ERP_TEST_DATABASE unset). Router
  auto-merged with masters.Mount outside auth; moved INSIDE (handlers use
  rls.FromContext like all other modules) and amended into the merge. Build +
  vet clean.
- 10eb050 "Merge task/build-purchase fix-forward into integration/e2e."
  Purchase 99ac877 verified green on throwaway (10.3s). Test-only change
  (three-way gap assertion counts journal rows for the doc instead of failing
  on any journal table). Merged clean, no conflicts. Obsoletes the known
  purchase gap-test red.

CRITICAL PATH UPDATE: masters P2.2 is on integration. Stock follow-up recorded
(not done by lead): swap stock's SnapshotCatalog placeholder for the real
masters reader via the Catalog interface (internal/stock/masters.go). Note: now
entangled with the P0 below (reservations ownership); land together.

P0 NEW: duplicate erp.stock_reservations (masters vs stock). 31100_masters.sql:360
and 32100_stock_ledger.sql:57 both CREATE TABLE it with DIFFERENT shapes:
masters keys by real FKs (skus, warehouses), no order-line linkage; stock keys
by snapshot catalog (stock_skus, stock_warehouses) with order_line_id unique +
pending/active/released/consumed lifecycle. Goose runs 31100 first, 32100
collides: relation already exists. Blast radius check: this is the ONLY
duplicated table across all migration bands. Impact on integration: e2e C1-C5
red + masters/stock package runs red (identical signature, confirmed). Both
packages green on their branches. NOT fixed by lead (editing applied migrations
is forbidden; shape choice is semantic). Owners: stock track + masters track to
converge on ONE owning migration; needs Jai ruling on which shape wins.
Merges KEPT (reverting masters would lose P2.2; C-tests are e2e-track owned).

E2E-WAVE1 RESULT: FAIL with P1.7 HOLD (D1-D14, untouched) PLUS new C1-C5 reds
from the P0 above (all five: relation stock_reservations already exists at
32100). Investigated, cause pinned, owners recorded.

Shared-DB coupling (standing follow-ups, DBs untouched): sales erp_sales,
receivables erp_ar harnesses still break on integration (missing-migrations).
Masters/purchase harnesses use testdb throwaways (good pattern). No wipes.

Tracks (branch tip / worktree / tests / blocked-on):
- ledger: 5faa71d merged. Clean. Done.
- masters: 62d396e MERGED as e9ac89c. Branch-green. Integration package run red
  via P0 only. P2.2 routes live (inside auth).
- stock: 7ab160a merged. Branch-green. Integration run red via P0 only.
  Follow-ups: UseCatalog swap + reservations convergence (with masters track).
- sales: f9cac2d merged. Branch-green. Integration run red via stale erp_sales
  only. Seams 1-7 stand.
- receivables: 86f4b46 merged. Branch-green. Integration run red via stale
  erp_ar only. Follow-up: testdb throwaways.
- purchase: 99ac877 MERGED as 10eb050. Branch-green (plain + ledger-migrated
  DBs per track). Gap-test red obsoleted. Done.
- clients: tip f55adfd. No new branch commit checked this sweep.

Plan source (task/plan-index): tip ce8cb65, unchanged.

Merges done: task/build-masters 62d396e -> e9ac89c; task/build-purchase
99ac877 -> 10eb050.
Unblocks made: none (P0 needs track owners + Jai ruling).
E2E: e2e-wave1 FAIL (P1.7 HOLD + new C1-C5 P0).

QA SCALE TRIGGER: earlier FIRE stands (no new QA report this sweep).

Needs Jai (standing + new):
1. P1.7 HOLD: bearer-only vs header-auth compat.
2. NEW P0: which stock_reservations shape wins (masters FK shape vs stock
   lifecycle shape)? Ruling needed before tracks converge.
3. Relay client agent to task/build-clients.
4. QA split decision.
