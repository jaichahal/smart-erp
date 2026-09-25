# Wave 1 hand-off cards

Seven agents run in parallel with no directory overlap (`docs/spec/10-execution-playbook.md`
"Waves"). Each section below is one complete task card as defined in `CONTRIBUTING.md` section 1:
task text from `docs/spec/07-tracks-and-tasks.md`, owned and forbidden directories, contracts it
may read, R-IDs, `08` test IDs, kit dependencies, and a prompt you can paste to start the agent.

Rules that apply to every card:

- Owned directories are the only place you write code. Your test files live next to your code.
- Forbidden means everything else. Two directories are called out on every card because they are
  the ones agents most often want to touch: `apps/api/internal/kit/**` (Track A, kit PR only) and
  `apps/api/migrations/**` (Track B, `immutable-change` label, or the `migrations: mutable-only`
  line for plain tables, see `CONTRIBUTING.md` section 4). A module needs tables, so you *will*
  add migrations; you do it in the same PR, with the label or the escape hatch line, and a Track B
  review follows automatically.
- Contracts are read-only. A needed change goes in a contracts-only PR first (`CONTRIBUTING.md`
  section 3).
- Kit packages (`apps/api/internal/kit/*`) are consumed, never modified. If the kit lacks what you
  need, open an issue titled `kit: <need>` tagged Track A and stub behind an interface meanwhile.
- Wave 0 must have merged (`Justfile`, kit, compose, contract bundle 1.0.0) before you start.
- Branch `task/<P-id>-<slug>` in a worktree; commits `<P-id>: <imperative summary>`; PR through
  `gh pr create` with the template filled in; the Integrator lands it after checks and review.

Kit package reference (all under `apps/api/internal/kit/`):

| Package | Gives you |
| --- | --- |
| `kit/apierr` | the error envelope and the 19 codes; `apierr.New(code, msg)`, HTTP status mapping |
| `kit/idempotency` | `Idempotency-Key` store and replay middleware (A18) |
| `kit/ifmatch` | `If-Match` parsing and `state_version` comparison, 409 with current state (A19) |
| `kit/rls` | per-request PostgreSQL session variables for row-level security (R1.10) |
| `kit/audit` | audit event emission inside and outside the business transaction (A2, D8) |
| `kit/canon` | canonical JSON and `sha256(canonical || prev_hash)` (B4) |
| `kit/immutable` | immutable-table migration helper, chain append with advisory lock (B1, B5) |
| `kit/outbox` | River enqueue in the business transaction (E1, ADR-04) |
| `kit/testdb` | clones `erp_template` into a per-package `erp_test_<random>` database, or reuses `$ERP_TEST_DATABASE` |
| `kit/httpx` | success envelope `JSON`, `Deps` passed to every `Mount`, health and status handlers |
| `kit/oapi` | wire types generated from `contracts/openapi/openapi.yaml` (`gen.go`); never hand-edit |
| `kit/config`, `kit/obs` | fail-fast env loading (I1), logging, tracing, metrics |

---

## A1: identity broker, token issuer, DPoP gateway, step-up

**Task P1.2 (Track A).** Zitadel headless deployment (unmodified, pinned), gRPC clients from
Apache protos, session-brokering endpoints, ERP token issuer with KMS-held signing keys and JWKS,
MFA enforcement by role, device enrolment with DPoP verification at the gateway, refresh rotation
with reuse detection, step-up tokens, console custom login proxying OIDC. A. R1.3 to R1.8, R1.15.
Accept: tests A1 to A10 in `08`.

| | |
| --- | --- |
| Owns | `apps/api/internal/identity/**` |
| Forbidden | everything else; especially `apps/api/internal/kit/**` (kit PR only) and `apps/api/migrations/**` without the `immutable-change` label or the `migrations: mutable-only` line; never `internal/authz` (that is A2, agree the interface by issue) |
| Contracts, read-only | `contracts/openapi/openapi.yaml` paths `/auth/session`, `/auth/session/{id}/check`, `/auth/token`, `/auth/refresh`, `/auth/logout`, `/auth/step-up`, `/auth/device/enroll`, `/me`; schemas `AuthSession`, `TokenResponse`, `User`, securitySchemes `bearer`, `dpop` |
| Requirements | R1.3, R1.4, R1.5, R1.6, R1.7, R1.8, R1.15 |
| Acceptance tests | A1, A2, A3, A4, A5, A6, A7, A8, A9, A10 |
| Kit dependencies | `kit/apierr` (indistinguishable failures, A1; `RATE_LIMITED` envelope, A7), `kit/idempotency`, `kit/audit` (failed login audited and committed before the failure response, A2), `kit/rls`, `kit/outbox` (device.registered, user session events), `kit/immutable` (audit of auth failures), `kit/testdb`, `kit/httpx`, `kit/config`, `kit/obs` |
| Also reads | `docs/spec/05-security-and-audit.md` (identity section), `deploy/compose/` for the Zitadel service and the machine-user PAT (read-only; ask Track I for changes) |

Notes for A1: Zitadel is run unmodified as a separate service (AGPL-3.0, ADR-15); you talk to
its Session API over gRPC and never embed or patch it. DPoP is verified in this API, not in
Zitadel. Signing keys are held in KMS (dev: local simulation from the compose stack) and exposed
via JWKS. `/me/sessions` and `DELETE /me/sessions/{id}` are in `04` but not yet in
`openapi.yaml`; add them in a contracts-only PR before implementing A9.

Prompt to paste:

```
You are agent A1 on the Smart ERP repo at /Users/jaichahal/Projects/smart-erp. Read CONTRIBUTING.md, WAVE1.md section A1, and docs/spec/07-tracks-and-tasks.md task P1.2, then docs/spec/01-requirements.md R1.3 to R1.8 and R1.15, docs/spec/08-acceptance-tests.md A1 to A10, docs/spec/04-api-contracts.md "Identity", and docs/spec/05-security-and-audit.md.
Create a worktree: git worktree add ../smart-erp-a1 -b task/P1.2-identity origin/main, and work only there.
You own apps/api/internal/identity/** and nothing else. Do not edit apps/api/internal/kit/**; consume kit/apierr, kit/idempotency, kit/audit, kit/rls, kit/outbox, kit/testdb, kit/httpx, kit/oapi. Migrations you need go in apps/api/migrations with the immutable-change label or the exact PR-body line "migrations: mutable-only".
Contracts are read-only: implement the generated server interfaces for the /auth/* and /me paths in contracts/openapi/openapi.yaml exactly. If the contract must change, open a contracts-only PR first and wait for it.
Implement: session brokering to Zitadel's Session API over gRPC, ERP token issuer with KMS-held keys and JWKS, MFA enforcement by role, device enrolment and DPoP verification at the gateway, refresh rotation with reuse detection, single-use two-minute step-up tokens, per-user session cap and listing.
Failed logins must be byte-identical on the wire and audited before the failure returns (A1, A2). Rate limiting and lockout return the standard envelope (A7, A8).
Write tests for A1 to A10 named by their IDs, using kit/testdb (run `just db a1` and export the ERP_TEST_DATABASE line it prints).
Definition of done: R-IDs and A1 to A10 listed in the PR body; just lint, just test ./internal/identity/..., contracts.yml, just licence, just sbom all green; new invariants get new 08 IDs appended; strings externalised; no cross-module import.
Commit as "P1.2: <imperative summary>" and open the PR with gh pr create using the template; do not merge; the Integrator merges after checks and review (CONTRIBUTING.md "Merging on GitHub Free").
```

---

## A2: roles, permissions, RLS policies, SoD matrix, access review

**Task P1.3 (Track A).** Users, roles, permissions as data, RLS session variables, field-level
serialiser permissions, SoD matrix, access review job. A. R1.2, R1.9 to R1.14. Accept: tests A11
to A16.

| | |
| --- | --- |
| Owns | `apps/api/internal/authz/**` |
| Forbidden | everything else; especially `apps/api/internal/kit/**` (kit PR only; `kit/rls` is the session-variable mechanism, you write the policies that use it) and `apps/api/migrations/**` without the `immutable-change` label or the `migrations: mutable-only` line; never `internal/identity` (A1) |
| Contracts, read-only | `contracts/openapi/openapi.yaml` schema `User` (roles, personas), `ErrorCode` (`PERMISSION_DENIED`, `SOD_VIOLATION`); `docs/spec/04-api-contracts.md` "Masters" for `/sod-matrix`, `/approval-matrix` (not yet in `openapi.yaml`: contracts-only PR first) |
| Requirements | R1.2, R1.9, R1.10, R1.11, R1.12, R1.13, R1.14 |
| Acceptance tests | A11, A12, A13, A14, A15, A16 |
| Kit dependencies | `kit/rls` (set company, territory, ownership session variables per request), `kit/apierr`, `kit/audit` (SoD override audited, A14), `kit/idempotency`, `kit/ifmatch` (matrix edits carry `state_version`), `kit/outbox` (role.changed, user.disabled), `kit/testdb`, `kit/httpx` |
| Also reads | `docs/spec/02-architecture.md` "Module boundaries" (identity owns users, roles, SoD; you implement authorisation over them), `docs/spec/05-security-and-audit.md` |

Notes for A2: roles are data, not code (R1.2). RLS policies are SQL in your migrations; they use
the session variables `kit/rls` sets. Field-level permissions are enforced in the serialiser so a
Sales Agent never receives cost or margin anywhere, including embedded objects and exports (A11).
Counts are computed over the same filtered set as rows (A12, R1.11). An unknown role resolves to
the least-privileged persona (A13). Users are never deleted (A15, R1.9). The access review is a
scheduled job (`cmd/scheduler` wiring is Track A; expose a function they can call).

Prompt to paste:

```
You are agent A2 on the Smart ERP repo at /Users/jaichahal/Projects/smart-erp. Read CONTRIBUTING.md, WAVE1.md section A2, and docs/spec/07-tracks-and-tasks.md task P1.3, then docs/spec/01-requirements.md R1.2 and R1.9 to R1.14, docs/spec/08-acceptance-tests.md A11 to A16, and docs/spec/05-security-and-audit.md.
Create a worktree: git worktree add ../smart-erp-a2 -b task/P1.3-authz origin/main, and work only there.
You own apps/api/internal/authz/** and nothing else. Do not edit apps/api/internal/kit/**; consume kit/rls, kit/apierr, kit/audit, kit/idempotency, kit/ifmatch, kit/outbox, kit/testdb, kit/httpx, kit/oapi. RLS policies and the roles, permissions and SoD tables are migrations in apps/api/migrations with the immutable-change label or the exact PR-body line "migrations: mutable-only".
Contracts are read-only. /sod-matrix and /approval-matrix endpoints are in docs/spec/04 but not yet in contracts/openapi/openapi.yaml: open a contracts-only PR adding them first, then implement.
Implement: roles and permissions as data, RLS policies over the kit/rls session variables for company, territory and ownership, field-level serialiser permissions (cost and margin hidden from Sales Agent everywhere), least-privileged fallback for unknown roles, SoD matrix with override approval and audit, disable-not-delete for users, quarterly access review job producing the report and exceptions entries.
Write tests for A11 to A16 named by their IDs, using kit/testdb (run `just db a2` and export the ERP_TEST_DATABASE line it prints). A12 must assert the count equals the rows across pages.
Definition of done: R-IDs and A11 to A16 listed in the PR body; just lint, just test ./internal/authz/..., contracts.yml, just licence, just sbom all green; new invariants get new 08 IDs appended; strings externalised; no cross-module import (identity is a separate module: define the interface you need and file an issue for A1).
Commit as "P1.3: <imperative summary>" and open the PR with gh pr create using the template; do not merge; the Integrator merges after checks and review (CONTRIBUTING.md "Merging on GitHub Free").
```

---

## B1: approval engine

**Task P1.7 (Track B).** Approval engine: matrix config, request lifecycle with row lock and
state version, independence and SoD checks, reason enforcement, decision-field restoration,
posting token, delegation, fraud hints, audit at every transition, commit-before-raise. B. R2.1 to
R2.11. Accept: tests D1 to D14.

| | |
| --- | --- |
| Owns | `apps/api/internal/approvals/**` |
| Forbidden | everything else; especially `apps/api/internal/kit/**` (kit PR only) and `apps/api/migrations/**` without the `immutable-change` label (approval requests and decisions are immutable tables, so you will need the label); never `internal/notifications` (E1 consumes your events) |
| Contracts, read-only | `contracts/openapi/openapi.yaml` paths `/approvals/inbox`, `/approvals/{id}`, `/approvals/{id}/approve`, `/approvals/{id}/reject`; schemas `ApprovalCard`, `ApprovalDetail`, `ApprovalDecision`, `DecisionMeta`, `Severity`, `AllowedAction`; `contracts/events/event.schema.json` types `approval.requested`, `approval.decided`, `approval.delegated`, `approval.snoozed`; `docs/spec/04` "Approvals" for `/approvals/{id}/delegate` and `/snooze` (not yet in `openapi.yaml`: contracts-only PR first) |
| Requirements | R2.1, R2.2, R2.3, R2.4, R2.5, R2.6, R2.7, R2.8, R2.9, R2.10, R2.11 |
| Acceptance tests | D1, D2, D3, D4, D5, D6, D7, D8, D9, D10, D11, D12, D13, D14 |
| Kit dependencies | `kit/ifmatch` and `state_version` (D7), `kit/idempotency`, `kit/apierr` (`ALREADY_DECIDED` in `meta.notice`, `VALIDATION_ERROR` for empty reason, `STEP_UP_REQUIRED`), `kit/audit` (every transition and every refusal, refusals committed before the error is raised, D8), `kit/canon` (content hash at submission, D10), `kit/immutable` (request and decision tables), `kit/outbox` (raise events after commit, "commit-before-raise"), `kit/rls`, `kit/testdb`, `kit/httpx` |
| Also reads | `docs/spec/05-security-and-audit.md` "Approved equals posted", `docs/spec/03-domain-model.md` approval request |

Notes for B1: every PR from Track B is human-reviewed. Transitions take a row lock and bump
`state_version`; the losing concurrent decision gets 200 with the decided state and
`meta.notice = ALREADY_DECIDED`, never an error (D7). Decision fields are restored from storage
before every gate; client state is ignored (D6). The posting token is single-use, five minutes,
issued at final approval and consumed by the posting service (D9). Delegation is first stage only
and never the final gate (D11). Step-up is required at or above threshold (D14) using the
`step_up_token` A1 issues; agree the verification interface by issue and stub until A1 lands.

Prompt to paste:

```
You are agent B1 on the Smart ERP repo at /Users/jaichahal/Projects/smart-erp. Read CONTRIBUTING.md, WAVE1.md section B1, and docs/spec/07-tracks-and-tasks.md task P1.7, then docs/spec/01-requirements.md R2.1 to R2.11, docs/spec/08-acceptance-tests.md D1 to D14, docs/spec/04-api-contracts.md "Approvals", and docs/spec/05-security-and-audit.md "Approved equals posted".
Create a worktree: git worktree add ../smart-erp-b1 -b task/P1.7-approval-engine origin/main, and work only there.
You own apps/api/internal/approvals/** and nothing else. Do not edit apps/api/internal/kit/**; consume kit/ifmatch, kit/idempotency, kit/apierr, kit/audit, kit/canon, kit/immutable, kit/outbox, kit/rls, kit/testdb, kit/httpx, kit/oapi. Approval requests and decisions are immutable tables created with kit/immutable; their migrations go in apps/api/migrations with the immutable-change label (a Track B reviewer follows).
Contracts are read-only: implement /approvals/inbox, /approvals/{id}, /approvals/{id}/approve and /approvals/{id}/reject exactly as generated from contracts/openapi/openapi.yaml, and emit approval.* events that validate against contracts/events/event.schema.json. Delegate and snooze endpoints need a contracts-only PR first.
Implement: matrix configuration per document type with threshold, request lifecycle with row lock and state_version, independence and SoD checks, non-empty reason at API and workflow layers, decision-field restoration from storage, single-use five-minute posting token, first-stage time-boxed delegation, fraud hints, audit at every transition with refusals committed before the error is raised, and commit-before-raise for outbox events.
Write tests for D1 to D14 named by their IDs, using kit/testdb (run `just db b1` and export the ERP_TEST_DATABASE line it prints). D7 must run two concurrent approvals.
Definition of done: R-IDs and D1 to D14 listed in the PR body; just lint, just test ./internal/approvals/..., contracts.yml, just licence, just sbom all green; new invariants get new 08 IDs appended; strings externalised; no cross-module import (identity step-up verification and posting are interfaces you define).
Commit as "P1.7: <imperative summary>" and open the PR with gh pr create using the template and request the human reviewer; do not merge; the Integrator merges after checks and review (CONTRIBUTING.md "Merging on GitHub Free").
```

---

## B2: anchoring worker, verifier, off-site bucket and KMS wiring, backup and restore scripts

**Task P1.6 (Track B, I).** External anchoring worker, KMS signing, compliance-mode bucket, daily
anchor email, verifier with anchor comparison. R3.6. Accept: tests B9 to B11.

**Task P1.15 (Track I, B).** Off-site bucket, KMS, backup, restore, and anchoring: local
object-lock bucket on the second disk for staging, off-site cloud S3-compatible compliance-mode
bucket and off-prem KMS or Vault Transit key, continuous WAL archiving to off-site, backup engine
with manifests and refusal codes and success only after off-site verification, anchor worker
signing with the off-prem key, anchor and backup failure alerts, chain verification against
anchors, spare-NUC or procurement lead time recorded with management. R3.6, R3.11, R17.2, R17.3.
Accept: tests B9 to B11, I2 to I8, I15, I16.

| | |
| --- | --- |
| Owns | `apps/api/internal/audit/**`, `deploy/scripts/**` (`backup.sh`, `restore.sh`, `anchor.sh`, `verify-chain.sh`) |
| Forbidden | everything else; especially `apps/api/internal/kit/**` (kit PR only; `kit/canon` and `kit/immutable` define the chain, you verify it) and `apps/api/migrations/**` without the `immutable-change` label; `deploy/compose/**`, `deploy/systemd/**`, `deploy/nuc/**` are Track I (P1.13): request bucket, KMS simulation and timer units by issue, and write the runbook entry text into the PR body for Track I to place |
| Contracts, read-only | `contracts/openapi/openapi.yaml` paths `/audit/verify`, `/audit/events`, `/status` fields `last_backup`, `last_chain_verification`, `recovery_point_in_force`; schemas `ChainVerification`, `AuditEvent`, `StatusRun`; `contracts/events/event.schema.json` types `chain.verified`, `chain.broken`, `backup.completed`, `backup.failed`; `contracts/fixtures/canonical/` vectors |
| Requirements | R3.6, R3.11, R17.2, R17.3 |
| Acceptance tests | B9, B10, B11, I2, I3, I4, I5, I6, I7, I8, I15, I16 |
| Kit dependencies | `kit/canon` (recompute hashes), `kit/immutable` (chain head, anchors and verification runs are immutable tables), `kit/audit`, `kit/outbox` (`chain.broken` is a Critical that quiet hours do not suppress, B11; `backup.failed`, I8), `kit/apierr`, `kit/idempotency`, `kit/rls`, `kit/config` (off-site endpoints and key ids fail fast when missing, I1), `kit/obs`, `kit/testdb`, `kit/httpx` |
| Also reads | `docs/spec/05-security-and-audit.md` "Hash chain", "External anchoring", "Backups"; `docs/spec/10-execution-playbook.md` "External backup drives and restore drills" (P1.17 and P1.19 are later tasks; design the manifest so they can reuse it) |

Notes for B2: human-reviewed (Track B). The anchor is `{ company_id, chain_seq, head_hash,
row_count, at }` written hourly to the on-prem bucket and the off-site compliance-mode bucket,
signed with an off-prem asymmetric key whose `Sign` is denied to operator identities (B10). In dev
the off-site bucket is the local `offsite-sim` bucket and the key is a local simulation; the code
path is identical. The verifier walks the chain, reports the first break and count without
cascade-flagging (B6, B7 belong to P1.5 but your verifier is what runs them) and compares
recomputed heads with anchored heads (B11). Backups write archives then the manifest atomically
(I2), are successful only after off-site verification (I15), and restore refuses on named
integrity codes with `force` overriding only site mismatch (I4). Scripts in `deploy/scripts` are
thin wrappers over `cmd/worker` subcommands or the API; keep logic in Go so it is tested.

Prompt to paste:

```
You are agent B2 on the Smart ERP repo at /Users/jaichahal/Projects/smart-erp. Read CONTRIBUTING.md, WAVE1.md section B2, and docs/spec/07-tracks-and-tasks.md tasks P1.6 and P1.15, then docs/spec/01-requirements.md R3.6, R3.11, R17.2, R17.3, docs/spec/08-acceptance-tests.md B9 to B11, I2 to I8, I15, I16, and docs/spec/05-security-and-audit.md "Hash chain", "External anchoring" and "Backups".
Create a worktree: git worktree add ../smart-erp-b2 -b task/P1.6-anchoring-backup origin/main, and work only there.
You own apps/api/internal/audit/** and deploy/scripts/** and nothing else. Do not edit apps/api/internal/kit/**; consume kit/canon, kit/immutable, kit/audit, kit/outbox, kit/apierr, kit/idempotency, kit/rls, kit/config, kit/obs, kit/testdb, kit/httpx, kit/oapi. Anchors and verification runs are immutable tables; their migrations go in apps/api/migrations with the immutable-change label. deploy/compose, deploy/systemd and deploy/nuc belong to Track I: file issues for the offsite-sim bucket, KMS simulation and timers, and put your runbook text in the PR body.
Contracts are read-only: implement POST /audit/verify and GET /audit/events from contracts/openapi/openapi.yaml, feed the /status fields last_backup, last_chain_verification and recovery_point_in_force, and emit chain.verified, chain.broken, backup.completed and backup.failed events that validate against contracts/events/event.schema.json.
Implement: hourly anchor worker writing the signed chain head to the on-prem and off-site compliance-mode buckets with PutObject-only identity, daily anchor email, verifier with first-break reporting and anchor comparison, WAL archive lag measurement, backup engine with atomic manifest, per-file SHA-256 and aggregate checksum, refusal codes, success only after off-site verification, retention pruning after verification, restore with Stakeholder approval and post-restore re-derivation, and the deploy/scripts wrappers.
Write tests for B9 to B11, I2 to I8, I15 and I16 named by their IDs, using kit/testdb (run `just db b2` and export the ERP_TEST_DATABASE line it prints) and the compose MinIO offsite-sim bucket.
Definition of done: R-IDs and the test IDs listed in the PR body; just lint, just test ./internal/audit/..., contracts.yml, just licence, just sbom all green; new invariants get new 08 IDs appended; strings externalised; runbook entry text for deploy/nuc included; no cross-module import.
Commit as "P1.6: <imperative summary>" or "P1.15: <imperative summary>" and open the PR with gh pr create using the template and request the human reviewer; do not merge; the Integrator merges after checks and review (CONTRIBUTING.md "Merging on GitHub Free").
```

---

## C1: company, periods, holiday calendar, clocks, numbering

**Task P1.4 (Track C).** Company, fiscal periods, holiday calendar, business-hours clock service,
numbering sequences. C. R4.5, R4.6, R13.8. Accept: tests C1 to C5.

| | |
| --- | --- |
| Owns | `apps/api/internal/ledger/periods/**`, `apps/api/internal/clocks/**` |
| Forbidden | everything else; especially `apps/api/internal/kit/**` (kit PR only) and `apps/api/migrations/**` without the `immutable-change` label or the `migrations: mutable-only` line; `apps/api/internal/ledger/**` outside `periods/` is Wave 2 (chart of accounts, journals): do not pre-empt it |
| Contracts, read-only | `contracts/openapi/openapi.yaml` schema `ErrorCode` (`PERIOD_CLOSED`); `docs/spec/04` "Admin and operations" for `/periods/{id}/soft-close`, `/periods/{id}/hard-close`, `/periods/{year}/audit-adjustment/open` and "Masters" `/holiday-calendar` (not yet in `openapi.yaml`: contracts-only PR first); `contracts/events/event.schema.json` type `clock.expired` |
| Requirements | R4.5, R4.6, R13.8 |
| Acceptance tests | C1, C2, C3, C4, C5 |
| Kit dependencies | `kit/apierr` (`PERIOD_CLOSED`, `PERMISSION_DENIED` for back-dating), `kit/idempotency`, `kit/ifmatch` (period and calendar edits), `kit/audit` (closes and back-dated postings on the exceptions report, C4), `kit/immutable` (number allocations and voids are append-only), `kit/outbox` (`clock.expired`, `period.closed`), `kit/rls`, `kit/testdb`, `kit/httpx` |
| Also reads | `docs/spec/03-domain-model.md` company, period, numbering; `docs/spec/02-architecture.md` ADR-12 business-hours clocks |

Notes for C1: number allocation happens only inside a successful registration transaction and a
failed registration records a void so the sequence stays gap-free with the next number continuing
(C1, R4.5); two concurrent registrations get distinct consecutive numbers (C2) so use a row lock
per sequence, not a Postgres sequence. Soft close is Accountant, hard close is Stakeholder with
approval (C3, R4.6); back-dating into a prior open period needs a permission and lands on the
exceptions report (C4). Clocks count business hours against the company holiday calendar and
weekend definition (C5, R13.8, ADR-12); expose `Due(start, window)` and a scheduler hook that
emits `clock.expired`. Company and period are plain (mutable, versioned) tables: `migrations:
mutable-only` applies to those; allocations and voids are immutable.

Prompt to paste:

```
You are agent C1 on the Smart ERP repo at /Users/jaichahal/Projects/smart-erp. Read CONTRIBUTING.md, WAVE1.md section C1, and docs/spec/07-tracks-and-tasks.md task P1.4, then docs/spec/01-requirements.md R4.5, R4.6, R13.8, docs/spec/08-acceptance-tests.md C1 to C5, docs/spec/03-domain-model.md for company, period and numbering, and docs/spec/02-architecture.md ADR-12.
Create a worktree: git worktree add ../smart-erp-c1 -b task/P1.4-periods-clocks origin/main, and work only there.
You own apps/api/internal/ledger/periods/** and apps/api/internal/clocks/** and nothing else; the rest of internal/ledger is a Wave 2 task. Do not edit apps/api/internal/kit/**; consume kit/apierr, kit/idempotency, kit/ifmatch, kit/audit, kit/immutable, kit/outbox, kit/rls, kit/testdb, kit/httpx, kit/oapi. Migrations go in apps/api/migrations: the immutable-change label for number allocations and voids, or the exact PR-body line "migrations: mutable-only" for company, period and calendar tables.
Contracts are read-only. The period close endpoints and /holiday-calendar are in docs/spec/04 but not yet in contracts/openapi/openapi.yaml: open a contracts-only PR adding them first, then implement. Emit clock.expired events that validate against contracts/events/event.schema.json.
Implement: company record, fiscal years and periods with soft close (Accountant) and hard close (Stakeholder, approval) and PERIOD_CLOSED refusal, back-dating permission with exceptions-report audit, holiday calendar and weekend definition, business-hours clock service with Due(start, window) and a scheduler hook, gap-free per-type per-year numbering allocated inside the registration transaction with voids recorded on failure and a row lock per sequence.
Write tests for C1 to C5 named by their IDs, using kit/testdb (run `just db c1` and export the ERP_TEST_DATABASE line it prints). C2 must allocate concurrently. C5 must use a calendar with a weekend and assert 16:00 on the second working day.
Definition of done: R-IDs and C1 to C5 listed in the PR body; just lint, just test ./internal/ledger/periods/... and just test ./internal/clocks/..., contracts.yml, just licence, just sbom all green; new invariants get new 08 IDs appended; strings externalised; no cross-module import.
Commit as "P1.4: <imperative summary>" and open the PR with gh pr create using the template; do not merge; the Integrator merges after checks and review (CONTRIBUTING.md "Merging on GitHub Free").
```

---

## E1: outbox consumers, WebSocket hub, FCM v1 and APNs senders, preferences, alert rules

**Task P1.8 (Track E).** Outbox table and worker, event schema validation, WebSocket hub with
per-user subscriptions and acks, FCM v1 and APNs senders, data-only payloads, token lifecycle,
retries, dedupe, preferences, quiet hours, digest, alert rule table. E. R13.1 to R13.7. Accept:
tests E1 to E12.

| | |
| --- | --- |
| Owns | `apps/api/internal/notifications/**` |
| Forbidden | everything else; especially `apps/api/internal/kit/**` (kit PR only; `kit/outbox` is the River enqueue side, owned by Track A; you own the consumers) and `apps/api/migrations/**` without the `immutable-change` label or the `migrations: mutable-only` line; `apps/api/cmd/pushsink` is Track A (it validates payloads against `contracts/events`; ask for changes by issue); never `internal/approvals` (B1 produces the events you consume) |
| Contracts, read-only | `contracts/events/event.schema.json` (every payload you send validates against it, E3); `contracts/openapi/openapi.yaml` paths `/devices/push-token` (POST and DELETE), schema `Platform`, `Severity`, `AllowedAction`; `docs/spec/04` "Devices and notifications" for `/notifications`, `/me/notification-preferences`, `/ws`, `/notifications/{event_id}/acknowledge` and "Masters" `/alert-rules` (not yet in `openapi.yaml`: contracts-only PR first) |
| Requirements | R13.1, R13.2, R13.3, R13.4, R13.5, R13.6, R13.7 |
| Acceptance tests | E1, E2, E3, E4, E5, E6, E7, E8, E9, E10, E11, E12 |
| Kit dependencies | `kit/outbox` (River jobs to consume; E1 atomicity is proven here end to end), `kit/apierr`, `kit/idempotency` (push-token register is idempotent), `kit/ifmatch` (E10 approve-from-shade refuses on changed `state_version`), `kit/audit`, `kit/rls` (per-user subscriptions), `kit/canon` (dedupe key), `kit/testdb`, `kit/httpx`, `kit/config` (FCM and APNs credentials fail fast), `kit/obs` |
| Also reads | `docs/spec/02-architecture.md` ADR-04, ADR-07; `docs/spec/06-ux-spec.md` notification shade behaviour |

Notes for E1: payloads are data-only (ADR-07); a message with a `notification` block fails schema
validation in CI (E3) and `cmd/pushsink` in dev rejects it. An amount without a currency is
refused before enqueue (E4). Recipients derive from roles and configuration, exclude the actor and
disabled users (E5). No preference row means opted in; quiet hours suppress all but Critical; the
weekly digest batches FYI (E6). Unregistered or invalid token responses delete the token; tokens
unseen for 60 days are pruned (E7). Delivery failures record a Failed row and retry with backoff;
email fallback has its own row (E8). An action on one surface clears the item on every connected
surface within one second (E9). Alert dispatch failure never rolls back the business write (E11).
Alert rules are an admin-editable table with blocking or advisory mode (E12, R13.7).

Prompt to paste:

```
You are agent E1 on the Smart ERP repo at /Users/jaichahal/Projects/smart-erp. Read CONTRIBUTING.md, WAVE1.md section E1, and docs/spec/07-tracks-and-tasks.md task P1.8, then docs/spec/01-requirements.md R13.1 to R13.7, docs/spec/08-acceptance-tests.md E1 to E12, docs/spec/04-api-contracts.md "Devices and notifications" and "Event payload", and docs/spec/02-architecture.md ADR-04 and ADR-07.
Create a worktree: git worktree add ../smart-erp-e1 -b task/P1.8-notifications origin/main, and work only there.
You own apps/api/internal/notifications/** and nothing else. Do not edit apps/api/internal/kit/** or apps/api/cmd/pushsink; consume kit/outbox, kit/apierr, kit/idempotency, kit/ifmatch, kit/audit, kit/rls, kit/canon, kit/config, kit/obs, kit/testdb, kit/httpx, kit/oapi. Migrations for device tokens, preferences, delivery log and alert rules go in apps/api/migrations with the immutable-change label (delivery log) or the exact PR-body line "migrations: mutable-only" (tokens, preferences, rules).
Contracts are read-only: every payload you send must validate against contracts/events/event.schema.json with no notification block; implement POST and DELETE /devices/push-token from contracts/openapi/openapi.yaml. /notifications, /me/notification-preferences, /ws, acknowledge and /alert-rules need a contracts-only PR first.
Implement: River consumers with dedupe by event_id, WebSocket hub with per-user subscriptions and acks, FCM HTTP v1 and APNs senders with data-only flattened payloads and context JSON-encoded, token lifecycle with deletion on unregistered responses and 60-day pruning, retries with backoff and a Failed row per attempt, email fallback, recipient derivation excluding actor and disabled users, preferences with opt-in default, quiet hours that never suppress Critical, weekly FYI digest, alert rule table with blocking and advisory modes, and approve-from-shade that refetches and refuses on a changed state_version.
Write tests for E1 to E12 named by their IDs, using kit/testdb (run `just db e1` and export the ERP_TEST_DATABASE line it prints) and the dev push-sink. E9 must prove a second connected surface clears within one second.
Definition of done: R-IDs and E1 to E12 listed in the PR body; just lint, just test ./internal/notifications/..., contracts.yml, just licence, just sbom all green; new invariants get new 08 IDs appended; strings externalised; no cross-module import (approvals and identity are interfaces you define).
Commit as "P1.8: <imperative summary>" and open the PR with gh pr create using the template; do not merge; the Integrator merges after checks and review (CONTRIBUTING.md "Merging on GitHub Free").
```

---

## E2: journey engine

**Task P1.14 (Track E).** Journey engine: definitions, instances, step protocol, resumability,
persona visibility. E. Accept: tests E13 to E15.

| | |
| --- | --- |
| Owns | `apps/api/internal/journeys/**` |
| Forbidden | everything else; especially `apps/api/internal/kit/**` (kit PR only) and `apps/api/migrations/**` without the `immutable-change` label or the `migrations: mutable-only` line; never the modules whose steps you orchestrate (`approvals`, `identity`, `authz`, `periods`): call them through interfaces |
| Contracts, read-only | `docs/spec/04` "Journeys" for `GET /journeys?persona=`, `POST /journeys/{slug}/instances`, `POST /journeys/instances/{id}/step`, `GET /journeys/instances/{id}` (not yet in `openapi.yaml`: contracts-only PR first, including the step result shape `{ ok, code?, message?, data?, problems[], next_step? }`); `contracts/openapi/openapi.yaml` schemas `ErrorCode`, `User` (personas) |
| Requirements | none assigned directly in `07`. Cite the requirements the engine serves: R15.5 (persona-filtered home with server enforcement) and R18.1 (the Go-Live journey). The `spec-traceability` check needs at least one R-ID in the PR body; use those. If the engine needs a requirement of its own, add an R-ID to `docs/spec/01-requirements.md` in its own PR first |
| Acceptance tests | E13, E14, E15 |
| Kit dependencies | `kit/apierr`, `kit/idempotency` (step submissions are idempotent), `kit/ifmatch` (instance `state_version`), `kit/rls` (persona visibility), `kit/audit` (step completions), `kit/outbox` (`journey.step.completed`), `kit/testdb`, `kit/httpx` |
| Also reads | `docs/spec/06-ux-spec.md` the ten flows (journeys are their server-side backbone); `docs/spec/02-architecture.md` "Module boundaries" row `journeys` |

Notes for E2: journey state from the client can never change server-evaluated permissions or
workflow state (E13); the engine stores its own state and re-evaluates permissions on every step.
An await step (for example waiting on an approval) reports pending honestly and stops the run on
rejection (E14). Instances are resumable after a server restart and after days (E15): persist
every transition, no in-memory state. Definitions are data (slug, persona, ordered steps with
type, input schema, guard); ship the Go-Live journey definition skeleton as the first fixture.
`journey.step.completed` is not yet an event type in `contracts/events/event.schema.json`; add it
in a contracts-only PR.

Prompt to paste:

```
You are agent E2 on the Smart ERP repo at /Users/jaichahal/Projects/smart-erp. Read CONTRIBUTING.md, WAVE1.md section E2, and docs/spec/07-tracks-and-tasks.md task P1.14, then docs/spec/08-acceptance-tests.md E13 to E15, docs/spec/04-api-contracts.md "Journeys", docs/spec/06-ux-spec.md, and docs/spec/02-architecture.md "Module boundaries".
Create a worktree: git worktree add ../smart-erp-e2 -b task/P1.14-journeys origin/main, and work only there.
You own apps/api/internal/journeys/** and nothing else. Do not edit apps/api/internal/kit/**; consume kit/apierr, kit/idempotency, kit/ifmatch, kit/rls, kit/audit, kit/outbox, kit/testdb, kit/httpx, kit/oapi. Migrations for definitions and instances go in apps/api/migrations with the exact PR-body line "migrations: mutable-only", or the immutable-change label if you make step history append-only.
Contracts are read-only. The /journeys endpoints and the journey.step.completed event type are in docs/spec/04 but not yet in contracts/openapi/openapi.yaml or contracts/events/event.schema.json: open one contracts-only PR adding them (with a minor version bump in docs/spec/04) first, wait for it, then implement.
Implement: journey definitions as data (slug, persona, ordered steps with type, input schema, guard), instances with persisted state and state_version, the step protocol returning { ok, code, message, data, problems, next_step }, await steps that report pending and stop on rejection, resumability across restarts and days, persona visibility through kit/rls, and the Go-Live journey definition skeleton as a fixture. Client-supplied state never influences permissions or workflow state.
Write tests for E13 to E15 named by their IDs, using kit/testdb (run `just db e2` and export the ERP_TEST_DATABASE line it prints). E15 must restart the engine mid-instance and resume.
Definition of done: R15.5, R18.1 and acceptance tests E13 to E15 listed in the PR body; just lint, just test ./internal/journeys/..., contracts.yml, just licence, just sbom all green; new invariants get new 08 IDs appended; strings externalised; no cross-module import (approvals, identity and authz are interfaces you define).
Commit as "P1.14: <imperative summary>" and open the PR with gh pr create using the template; do not merge; the Integrator merges after checks and review (CONTRIBUTING.md "Merging on GitHub Free").
```
