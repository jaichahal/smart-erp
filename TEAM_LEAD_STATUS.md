Team Lead Status - 2026-09-25 22:50 UTC+4 sweep (on top of d6fc873)

Integration new commit this round:
- d6fc873 "Merge task/build-clients-parity into integration/e2e." Android-only
  verified by file list (JourneyUiTest.kt + Shell.kt, zero apps/api files).
  Merged clean. Approval inbox tap opens review sheet; CFO/Partner gating from
  dashboard actions.

HELD (not merged): task/fix-reservations-shape 8d39533. Migrations reviewed:
31000/32000/32101 handle all three DB states (fresh, erp_stock-applied,
31100-applied) toward one table (stock lifecycle + real FKs, Jai's ruling).
BUT verification failed: stock green on throwaway (8.2s), masters RED on
TestMovingAverageAndAvailable (INTERNAL_ERROR). Cause: masters stock.go:112
still INSERTs the SHORT shape (no order_line_id) into the unified lifecycle
table (order_line_id NOT NULL). The migration fix is sound; masters' own write
path was not adapted. Owner: masters track (adapt stock.go reservation INSERT
to lifecycle shape). Lead did not patch product code. C1-C5 were not re-proven
on the branch (blocked behind the same red); merge when masters pkg is green.

E2E-WAVE1 RESULT: FAIL with C1-C5 only (P0 stands). P1.7 green. No new reds.

Live stack: API pid 7276 (built from a36d969, includes LPO routes) still up;
no restart needed (no Go changes since). All four endpoints re-verified 401
(zero 404s). client-api-needs.md: Clients QA to re-run all three surfaces.

Tracks: ledger/stock/purchase/sales/receivables/masters merged (masters/stock
integration runs red via P0; sales/receivables via stale shared DBs). Clients
parity merged; tip f55adfd branch work continues.

Plan source (task/plan-index): tip ce8cb65, unchanged.

Merges done: task/build-clients-parity 13c9e1b -> integration/e2e d6fc873.
Merges held: task/fix-reservations-shape 8d39533 (masters stock.go:112).
Unblocks made: none.
E2E: e2e-wave1 FAIL (C1-C5 P0 only).

Needs Jai (standing):
1. P0 shape convergence in progress; masters write-path fix pending with
   masters track.
2. Relay client agent to task/build-clients.
3. QA split decision.
