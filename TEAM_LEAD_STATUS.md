Team Lead Status - 2026-09-25 23:15 UTC+4 dev-rebuild round (on top of e99b575)

DEV DATABASE REBUILT CLEANLY. Exact commands (all documented just targets):
1. `OLDPID=$(lsof -ti :8080); kill $OLDPID` (stopped pid 11476; no go-test
   processes running; erp had 0 backends after stop).
2. `just reset` (documented: drops erp_template + erp WITH FORCE, recreates
   erp owned by erp_migrator, then `go run ./cmd/migrate up`). Full set applied
   to goose 35100, including 31000/32000/32101 convergence. Migrate tool also
   auto-refreshed erp_template.
3. `just seed` -> "seed: no seed data yet (Phase 2, task P2.1/P2.2)". No-op per
   repo; no credentials invented; no users/companies hand-created.
4. `just template` (idempotent re-snapshot; verified template holds converged
   stock_reservations + versions 31000/31100/32000/32100/32101).
5. `go build -o /tmp/smarterp-api ./cmd/api` from b6fc9e8 tree; restarted with
   host env, worktree cwd; new pid 12151.
Named per-track DBs (erp_ar, erp_sales, ...) NOT touched. No push. No down.

Live verdict on fresh DB (pid 12151, health 200):
- dashboard, lpos, sales-orders, receipts: all 401, zero 404s, zero 500s on
  unauthenticated shape checks. Dev-erp 500 caveat RESOLVED by the rebuild
  (tables now present).
Note: dev user/company for sign-in flows are seeded per-run by the console
login.spec (directory seed), not by repo seed; Clients QA sign-in unaffected.

Tracks: unchanged (all merged at b6fc9e8; wave-1 green). Next: client re-runs.

Needs Jai (standing, reduced):
1. Relay client agent to task/build-clients.
2. QA split decision (likely moot: backend loop closed, wave green).
