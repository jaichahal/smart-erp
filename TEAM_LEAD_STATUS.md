Team Lead Status - 2026-09-25 23:20 UTC+4 QA-correction round (on top of a8bdf16)

No merges. No product code changed. No push.

QA sweep 3 finding REBUTTED by grep on integration/e2e HEAD:
- d6fc873 contains exactly: JourneyUiTest.kt + Shell.kt (Android-only, confirmed
  via first-parent diff). It carries the Android inbox screen.
- iOS inbox arrived earlier via 249e0e5 (+e34d03b rework), not via d6fc873.
- Both phones HAVE the strings: Android "Approval inbox" (Shell.kt:104) +
  "Review request" (Shell.kt:218); iOS "Approval inbox" (SignedInHome.swift:26)
  + test asserts "Approve request" / "Live state pending". No camelCase
  approvalInbox identifier exists anywhere; QA likely asserted an identifier
  that was never the contract, or read a stale tree.
- Recorded correct display strings in client-api-needs.md for QA re-assertion.

Client-track queue (recorded in needs file, not implemented): console
sales-order If-Match/state_version; receipts JSON body shape; dashboard
validity handling; Android dashboard 401-vs-400 inconsistency. All owned by
client track.

Tracks: unchanged (all merged at b6fc9e8; wave-1 green at that snapshot; dev
rebuilt). No new branch commits checked this round.

Needs Jai (standing):
1. Relay client agent to task/build-clients.
2. QA split decision (likely moot).
