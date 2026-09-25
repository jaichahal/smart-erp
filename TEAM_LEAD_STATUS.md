Team Lead Status - 2026-09-25 21:05 UTC+4 sweep (on top of e34d03b)

Integration new commits this round:
- 00ebf85 "Merge task/build-sales into integration/e2e." Sales f9cac2d
  verified green first on a throwaway DB (go test ./internal/sales/ ok, 4.0s).
  Router conflict was import-hunk only; mounts auto-merged with sales.Mount
  inside the auth group. .golangci.yml auto-merged (sales added to module deny
  lists per repo convention). Build + vet clean.
- e34d03b "Client UI work in progress: iOS home layout rework." New direct
  edits on integration/e2e by client agent (SignedInHome scroll/card rework +
  JourneyUITests swipe fallback). Swift parse clean, identifiers unchanged.
  Redirect to task/build-clients standing; parent relays.

Sales seams: WIRED none, OPEN six (merged with sales-local intact, no rewrites):
1. ledger-posting: pgLedger writes erp.sales_postings, not ledger journals.
2. stock-reservation: pgStock uses erp.sales_stock/sales_reservations, not the
   stock module (moving-average logic duplicated).
3. numbering: erp.sales_sequences local; gap-free doc numbering not unified.
4. approval-token: invoice submit/approve/register sales-local (S1-S27, D23-D25
   green locally); no approvals-module token wiring.
5. period-close: sales-local clocks (calendar.go) vs periods/clocks packages.
6. tax-invoice-pdf: printInvoice local vs ledger tax-invoice path.
Ports (Stock, Ledger, CreditChecker in ports.go) are the boundaries to wire later.

E2E-WAVE1 RESULT: known-only red. P1.7 D1-D14 401s (HOLD, untouched). No new
failures after the sales merge.

client-api-needs.md appended: POST /sales-orders code-wired (merge 00ebf85);
live 404-gone check needs API restart + 33100 on dev DB. Dashboard + LPOs
merged earlier (7c90932). Still missing: POST /receipts (receivables).

Tracks (branch tip / worktree / tests / blocked-on):
- ledger: 5faa71d merged. Clean. Done.
- masters: tip c9b2a46, uncommitted pkg + 31100. RED (P0 migration). Next: fix
  31100, green, merge. Still blocks stock placeholder + sales credit/masters use.
- stock: 7ab160a merged. Clean. Waits on masters UseCatalog.
- sales: f9cac2d MERGED as 00ebf85. Worktree clean. Done from lead view; six
  open seams above are sales-track follow-ups.
- receivables: tip c9b2a46, uncommitted bank/ + receivables/ + 34100. RED (P0
  mounts). Next: Mount fns + green + merge; then POST /receipts closes the last
  client-api-needs row.
- purchase: 43fdcf9 merged. Clean. Done (posting gap open).
- clients: tip f55adfd, worktree state unchecked this sweep (no new branch
  commit). Direct integration edits continue; redirect standing.

Plan source (task/plan-index): tip ce8cb65, unchanged.

Merges done: task/build-sales f9cac2d -> integration/e2e 00ebf85.
Unblocks made: none (no track stuck).
E2E: e2e-wave1 FAIL known-only (P1.7 HOLD).

QA SCALE TRIGGER: no change this sweep (backend 3 P0s already reported; sales
P0 now self-resolved by f9cac2d but masters/receivables P0s stand; no new QA
report). Earlier FIRE recommendation stands; awaiting parent/Jai decision.

Needs Jai (standing):
1. P1.7 HOLD: bearer-only vs header-auth compat.
2. Relay client agent to task/build-clients.
3. QA split decision.
