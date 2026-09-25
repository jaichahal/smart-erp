Team Lead Status - 2026-09-25 22:30 UTC+4 sweep (on top of ccdc80a)

Integration new commit this round:
- ccdc80a "Merge task/fix-approvals-bearer into integration/e2e." Bearer fix
  9cc1b38 verified first (D1-D14 pass on throwaway, 6.6s). Test-only change
  (b1_accept.go, b1_world.go: DPoP bearer instead of X-User headers); router
  untouched. Merged clean, no conflicts.

P1.7 HOLD LIFTED. Wave-1 D1-D14 all pass on integration. Bearer-only approvals
stands as the decided semantics (no header-auth restore).

E2E-WAVE1 RESULT: FAIL with C1-C5 only (known reservations P0: 32100 collides
after 31100, identical signature confirmed). Zero P1.7 lines in the output.
Everything else in the filter passes.

Noted for next sweeps:
- Backend QA closed its loop: all six tracks PASS, report at
  qa-reviews/backend.md. Earlier per-track P0s are superseded by that report;
  the reservations P0 and shared-DB harness follow-ups recorded here stand
  (they postdate / extend the QA report scope on integration).
- LPO list + vendor-dashboard-shape now owned by purchase track (parent routing
  directly). Live check stands: /purchase/dashboard live, /lpos + exact
  /vendors/dashboard shape missing.

Tracks: ledger/stock/purchase/sales/receivables/masters merged (same reds as
before: C1-C5 P0 on integration package runs for masters/stock; stale
erp_sales/erp_ar for sales/receivables). Clients tip f55adfd.

Plan source (task/plan-index): tip ce8cb65, unchanged.

Merges done: task/fix-approvals-bearer 9cc1b38 -> integration/e2e ccdc80a.
Unblocks made: none.
E2E: e2e-wave1 FAIL (C1-C5 P0 only; P1.7 green).

Needs Jai (standing):
1. P0: which stock_reservations shape wins.
2. Relay client agent to task/build-clients.
3. QA split decision (backend loop closed; split may be moot).
