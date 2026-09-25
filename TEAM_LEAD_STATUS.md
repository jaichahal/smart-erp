Team Lead Status - 2026-09-25 22:40 UTC+4 sweep (on top of a36d969)

Integration new commit this round:
- a36d969 "Merge task/build-purchase LPO routes into integration/e2e."
  Purchase fe19660 verified first on throwaway (12.5s). Adds GET /lpos +
  GET /vendors/dashboard at top level inside the existing purchase Mount
  (already inside auth group); static /vendors/dashboard beats masters
  /vendors/{id} in chi. Merged clean, no conflicts, no router change.

Live stack: API rebuilt from a36d969 (pid 7276). All four client endpoints
return 401 unauthenticated (live, zero 404s): dashboard, lpos, sales-orders,
receipts. client-api-needs.md updated: every row live; Clients QA to re-run
all new-screen specs.

E2E-WAVE1 RESULT: FAIL with C1-C5 only (reservations P0). P1.7 green.

Tracks: ledger/stock/purchase/sales/receivables/masters merged. Purchase done
(LPO + dashboard shape delivered). Clients tip f55adfd.

Plan source (task/plan-index): tip ce8cb65, unchanged.

Merges done: task/build-purchase fe19660 -> integration/e2e a36d969.
Unblocks made: none.
E2E: e2e-wave1 FAIL (C1-C5 P0 only).

Needs Jai (standing):
1. P0: which stock_reservations shape wins.
2. Relay client agent to task/build-clients.
3. QA split decision (backend loop closed; may be moot).
