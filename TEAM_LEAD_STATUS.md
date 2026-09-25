Team Lead Status - 2026-09-25 22:15 UTC+4 live-check round (on top of 54909ee)

No product code changed this round. No merges. No test runs (no throwaways needed).

Live stack work (dev erp DB + API on :8080):
- `just migrate` on dev erp: applied 301xx + 31100, then STOPPED at 32100 on the
  known stock_reservations P0 (31100 vs 32100 duplicate). Dev goose version now
  31100. Forward-only; no reset, no down, erp_template untouched. Tables for
  32100/33100/34100/35100 are NOT on dev erp: authenticated calls into
  stock/sales/receivables/purchase handlers will 500 until the P0 resolves.
- API restarted: killed /tmp/smarterp-api pid 52431 (stale, pre-merge build),
  rebuilt from integration/e2e, new pid 2775, health 200. Same binary path, same
  worktree cwd, same host env.

Per-endpoint live status (unauthenticated curl; 401 = route live behind auth):
- POST /api/v1/sales-orders -> 401 LIVE (was 404). Genuine (exact static route).
- POST /api/v1/receipts -> 401 LIVE (was 404, receivables owns it). Genuine.
- GET /api/v1/vendors/dashboard?sku= -> 401 FALSE POSITIVE. No such route;
  chi matches masters /vendors/{id} with id=dashboard. Correction: purchase
  dashboard is live at GET /api/v1/purchase/dashboard (401), masters best-price
  at GET /api/v1/vendors/best-price (401). Owner purchase track: add the
  /vendors/dashboard shape or confirm clients use /purchase/dashboard.
- GET /api/v1/lpos -> 404, genuinely code-missing. Owner purchase track.

client-api-needs.md updated with the above (corrects the earlier over-claim
that dashboard+LPOs had merged; only /purchase/dashboard exists).

Tracks: unchanged since 21:55 sweep (ledger/stock/purchase/sales/receivables/
masters merged; P1.7 HOLD + reservations P0 stand; masters 62d396e was already
merged as e9ac89c). Clients QA sweep 2 Android 404s should clear for
sales-orders/receipts on re-run against the restarted API; dashboard/LPO specs
still blocked on purchase-track routes above.

Needs Jai (standing):
1. P1.7 HOLD: bearer-only vs header-auth compat.
2. P0: which stock_reservations shape wins.
3. Relay client agent to task/build-clients.
4. QA split decision.
