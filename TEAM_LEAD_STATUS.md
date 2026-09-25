Team Lead Status - 2026-09-25 19:55 UTC+4 sweep

Integration (integration/e2e worktree): HEAD 977b045. New commits this round:
- 249e0e5 "Client UI work in progress: persona home and journey screens."
  (17 files: Android Home/Shell + journey test, iOS SignedInHome + UI updates,
  console home/shell modules + journeys spec, router approvals-inside-auth,
  rls spaces-in-roles). Verified before commit: go vet clean on changed Go
  pkgs, e2e test binary compiles. login.spec not run (needs Playwright stack).
  Migration 10042 left untracked as instructed.
- 977b045 "Merge task/build-stock into integration/e2e." Router conflict
  resolved keeping both: approvals.Mount(authed) + stock.Mount(authed).
  Resolution builds clean.

E2E-WAVE1 RESULT: FAIL. 14 subtests fail, all P1.7 D1-D14:
POST /api/v1/approvals/actors as initiator -> 401 AUTH_REQUIRED.
Everything else in the wave-1 filter passes.
Root cause: the approvals mount now sits behind id.Authenticate, which rejects
requests without a bearer token. The P1.7 harness (e2e/b1_world.go) authenticates
via X-User/X-Company/X-Roles headers through approvals.RequestPrincipal and sends
no bearer token, so every D-case 401s.
Causality proof: P1.7 subset passes on pristine c9b2a46 (temp worktree, since
removed) and fails on 977b045. Stock merge is not the cause (it only adds a mount).
Jai confirmed the auth move is intentional, so the lead did NOT revert it.
Fix owner: approvals track (task/P1.7-approval-engine) or notifications track to
reconcile RequestPrincipal header auth with bearer Authenticate on approvals
routes (options: dual principal resolution inside approvals handlers, or bearer
tokens in the P1.7 harness). Needs Jai decision; auth semantics reserved for Jai.

Tracks (branch tip / worktree / tests / blocked-on):
- ledger (task/build-ledger): tip c9b2a46, uncommitted ledger pkg + migrations
  30100-30130, and NOW also a modified router.go (watch for conflicts with
  integration router). Still compile-RED c11 (TDD). Active. Blocked-on: nothing.
- masters (task/build-masters): tip c9b2a46, uncommitted masters pkg + NEW
  migration 31100_masters.sql (progress). Stub RED phase. Blocked-on: nothing
  (it blocks stock placeholder removal + purchase/sales seams).
- stock (task/build-stock): tip 7ab160a, MERGED into integration/e2e as 977b045.
  Placeholder SnapshotCatalog stays until masters P2.2 lands via UseCatalog
  (Jai confirmed). Done from lead view.
- sales (task/build-sales): tip c9b2a46, uncommitted sales pkg + 33100. Active.
  Blocked-on: masters seams (unconfirmed).
- receivables (task/build-receivables): tip c9b2a46, NEW internal/bank/ dir +
  migration now 34100_receivables_and_bank.sql (progress). Active.
- purchase (task/build-purchase): tip c9b2a46, uncommitted purchase pkg +
  35100, and NOW also a modified router.go (watch for conflicts). Active.
- clients (task/build-clients): NEW commit f55adfd "Add vendor, order, receipt,
  and purchase screens on the console and both phones." Worktree clean. Active.
  Note: agent 2e4bf2a2 still also works directly on integration/e2e; remind it to
  use its branch going forward.

Plan source (task/plan-index): tip ce8cb65, unchanged.

Merges done: task/build-stock 7ab160a -> integration/e2e 977b045.
Unblocks made: none (no track stuck).
E2E: e2e-wave1 FAIL (P1.7 D1-D14 401s, see above).

Needs Jai:
1. P1.7 401s: keep approvals-behind-auth (breaks P1.7 harness) or restore
   header-auth compatibility? Owner: approvals track once decided.
2. Remind client agent to work on task/build-clients, not integration/e2e.
3. Masters P2.2 seam confirmed already; no action.
