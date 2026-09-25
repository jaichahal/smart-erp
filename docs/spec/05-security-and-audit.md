# 05 Security and Audit

The goal, stated precisely: every registered entry is tamper-resistant against every application user including administrators, and tamper-evident against the database operator, so that any alteration is detectable by a party who did not make it. Nothing is tamper-proof against someone who controls the database, the object store, the KMS, and the email path together; that combination is prevented organisationally by splitting who holds each.

## Identity and sessions

- Zitadel instance per environment, run headless and unmodified. Zitadel owns users, organisation, factors, and lockout; it verifies password, TOTP, and passkeys through the Session API v2 called by our API. MFA mandatory for Approver, Stakeholder, Accountant, System Manager, enforced by our API refusing to mint a token unless the Zitadel session shows the required factors verified within the window.
- Token issuance is ours. After factor verification, the API mints an ES256 access token for the ERP audience (15 minutes) with `cnf.jkt` bound to the enrolled device key, plus a rotating refresh token bound to the device id. Signing keys live in KMS or Vault Transit with rotation; JWKS is published by the API. The console gets the same tokens through a custom login page that proxies OIDC to Zitadel and finalises the auth request with the session.
- Mobile device enrolment: the app generates an EC P-256 key pair in Keystore (StrongBox where available) or Secure Enclave; the public key is enrolled with the device record; every API request carries a DPoP proof (JWT signed by the device key over method, URL, timestamp, access-token hash). The gateway verifies the proof against `cnf.jkt` and the enrolled key, with JTI replay protection in Redis. Reuse of a rotated refresh token revokes the device.
- Access token lifetime 15 minutes; refresh 30 days sliding, 90 days absolute; console session 8 hours idle.
- Step-up: a fresh biometric assertion on the device (signed challenge verified by our API) or a factor re-check on the Zitadel session (TOTP or passkey) yields a `step_up_token` valid for two minutes and one action. Required for R1.6 actions.
- Login controls: per-user and per-IP rate limits, lockout after configurable failures with exponential unlock, uniform error responses, failed attempts logged and committed before the response.
- Sessions listable and revocable; revocation propagates on the next request through a short-TTL revocation cache.
- No shared or service accounts for humans. Integration clients use client credentials with scoped API keys and IP allow-lists.

## Authorisation

- Roles to permissions mapping is data; permissions are `resource:action` with optional scope (own, territory, warehouse, company).
- PostgreSQL row-level security on every business table keyed by `company_id` and, where relevant, `territory_id`, `owner_id`, `warehouse_id`; the API sets session variables per request from the token; the application role cannot bypass RLS.
- Field-level permissions applied in serialisers: cost, margin, other agents' data, bank details, salary-like fields. Tested by role in the contract test suite.
- Segregation-of-duties matrix evaluated at role assignment (warn and require override approval) and at action time (block).
- Counts and summaries computed under the same RLS session as the rows.
- Fail closed: unknown role, missing scope, or missing permission mapping resolves to deny and the least-privileged UI.

## Immutable tables

Applies to: journal headers and lines, stock ledger lines, registered document tables and lines, approval decisions, audit events, notification log, number allocations and voids, attachment metadata, snapshot metadata, chain heads, anchors.

- Ownership: tables owned by `erp_migrator`; application connects as `erp_app` with `INSERT, SELECT` only on immutable tables and `INSERT, SELECT, UPDATE` on mutable ones; no `DELETE` anywhere except explicit soft-delete columns.
- Triggers: `BEFORE UPDATE OR DELETE ... RAISE EXCEPTION 'immutable'` on every immutable table, created by migration and asserted by a nightly check that lists tables missing the trigger and alerts.
- DDL guard: an event trigger on `ddl_command_start` and `sql_drop` logs statement, role, and client to an external sink; a CI rule blocks migrations touching immutable tables unless the PR carries an `immutable-change` label approved by two maintainers.
- `session_replication_role` and trigger disabling are superuser-only; superuser is vaulted; every check-out is a ticket and an external log entry.
- pgaudit: log DDL, ROLE, and all statements from any role other than `erp_app`, shipped off-host.

## Hash chain

- Per company, one chain covering all immutable rows in append order. Each row stores `prev_hash`, `hash`, and `chain_seq`.
- Canonical form: JSON with sorted keys, no whitespace, decimals as strings, timestamps as UTC ISO, child rows included as arrays sorted by line number, referenced ledger line ids included, `prev_hash` and `hash` excluded.
- `hash = sha256(canonical || prev_hash)`.
- Serialisation: the appending transaction takes `pg_advisory_xact_lock(company_chain_key)` and reads the chain head from a single-row `chain_head` table updated in the same transaction; no fork is possible.
- Timestamps come from `clock_timestamp()` in the database; hosts run NTP; the status page shows drift.
- Verification: nightly job and on-demand endpoint walk the chain, recompute each hash, compare each `prev_hash` to the previous stored hash, and report the first break and the count; a break pushes a Critical that preferences cannot suppress and appears on the exceptions report. The Auditor role can run it.

## External anchoring

- Off-site is mandatory. Production runs on company-premises NUCs; the on-prem MinIO bucket with object lock is the primary store but it sits on hardware the database operator can also reach, so it is not an anchor. The tamper-evidence goal in this document is met only when the anchor is written to a location outside the premises and outside the operator's control. On-prem-only anchoring does not meet the goal and must not be accepted as an interim state in production.
- Hourly: write `{ company_id, chain_seq, head_hash, row_count, at }` to both the on-prem bucket and an off-site cloud S3-compatible bucket with object lock in compliance mode, retention seven years, versioning on; the writing identity has `PutObject` only on the off-site bucket; the connection is outbound only from the NUC. A failed off-site write within two hours raises a Critical to Stakeholders.
- Sign the anchor with an asymmetric key held off-prem: a cloud KMS key, or Vault Transit on a host outside the company premises. The key policy denies `Sign` to the database and application operators' identities and grants it only to the anchor worker's identity; `Verify` is public. The private key never exists on a NUC.
- Daily: email the latest anchor to the Stakeholder list and the external auditor address; the email is sent through a provider account not administered by the database operator.
- Verification compares the recomputed head at each anchored `chain_seq` with the anchored value; mismatch is treated as a break.
- Restore is not complete until the restored chain matches an anchor at or before the restore point.

## Approved equals posted

- On submit, the draft's canonical content hash is stored on the approval request and shown to approvers.
- Each decision records the hash the approver saw.
- Registration recomputes the hash of the content it is about to post and refuses if it differs from the approved hash.
- The posting token is issued by the approval service at final approval, is single-use, expires in five minutes, and is validated and consumed by the posting service in the registration transaction.

## Master data and rendered output

- Every master table is versioned with `valid_from`, `valid_to`, `approved_by`, `change_reason`; documents reference version ids.
- Rendered tax invoice, credit note, LPO, and statement PDFs are stored at registration with SHA-256 on the document row; reprint serves the stored file.
- Print formats are versioned; a document records the format version used.

## Attachments and backups

- MinIO bucket with object lock compliance mode; the application writes once and reads; SHA-256 stored on the referencing row and verified on read; a mismatch is an integrity event.
- Backups (base plus WAL) go to a local object-lock bucket on the second disk or NAS for fast restore, and to the off-site compliance-mode bucket for survival of theft, fire, or a hostile operator; a backup is not reported successful until the off-site copy is verified. Manifests carry per-file SHA-256, aggregate checksum, row counts per immutable table, ledger debit and credit totals, chain head. WAL is archived to the off-site bucket continuously so the recovery point is minutes, not a day, on a single node.
- External drive target: a second copy of database and object backups is written to an external drive attached to the NUC, detected by UUID from an allow-list. The drive is a LUKS volume unlocked with a key from the NUC secrets store, or holds age-encrypted archives; the key is never on the drive, so a lost or stolen drive discloses nothing. The drive copy carries the same manifest and is verified by re-reading from the drive before the safe-to-remove signal. It never replaces the off-site bucket; a backup counts as successful only when the off-site copy verifies, and the drive status is reported separately (R17.13).
- Chain of custody for the carried drive: two drives rotate, one on-site and one off-site. Every hand-over is an audit event recording the drive UUID, who took it, when, and where it is kept, entered from the console or phone by the person taking it and acknowledged by the System Manager. The exceptions report lists drives not seen for longer than the rotation period. A drive that returns with a manifest whose chain head does not match the anchor for its backup time is quarantined and reported.
- Restore: verify manifest and files first; refuse on any mismatch with a named code; force overrides only a site-name mismatch; after apply re-derive counts and totals, verify the chain, and confirm the restored chain head equals the off-site anchor at or before the backup time. A restore is not complete until chain and anchor match; a mismatch is reported as a break, the restored environment is not allowed to serve writes, and Stakeholders are notified. Production restore requires Stakeholder approval and is logged as a restore audit event. The weekly and monthly restore drills (R17.14 to R17.18) exercise this exact path into an isolated environment so the production procedure is never run for the first time under pressure.

## Mobile client controls (MASVS L2)

- Storage: credentials, refresh token, device key handle in Keystore or Keychain; offline store encrypted with a key wrapped by the platform keystore; no secrets in preferences, files, or logs.
- Network: TLS 1.2 or later; certificate pinning with a backup pin and a remote-rotatable pin set; cleartext disabled in release.
- Build: R8 or Swift optimisation with symbol stripping; debug logging compiled out of release; `allowBackup=false`; file-level exclusion from device transfer; screenshots blocked on screens showing amounts for Stakeholder role (configurable).
- Session: enforce token expiry locally; app lock after idle; wipe caches on logout and 401; no cross-user cache fallback.
- Integrity: root and jailbreak detection, debugger detection in release; degrade to read-only with a notice rather than refuse.
- Push: data-only messages; notification content built locally; approve and reject from the shade always open an authenticated sheet that fetches live state; no action commits from a notification without the live view and a biometric assertion.
- Deep links: verified App Links and Universal Links; custom scheme as fallback; unauthenticated taps are stashed and resumed after login.

## Operational security

- On-premises posture: NUCs behind the company firewall with no inbound ports except the reverse proxy (or none at all when a tunnel is used); operator access only through Tailscale with device authorisation and MFA, never SSH exposed to the internet; unattended OS security updates in a nightly maintenance window with automatic reboot, announced on the status page; UPS with monitored battery and clean shutdown; physical access to the NUC cabinet logged.
- Mobile TLS: automatic certificate rotation by Caddy or the tunnel provider means leaf pinning would break the apps. Apps pin a set containing the intermediate CA public keys plus a remote-rotatable backup pin delivered signed by the API; a pin-set change is itself an audited configuration change.
- Telemetry, pgaudit, and DDL logs ship to an off-prem sink; a NUC-only log store is not acceptable because the operator could erase it. Disk-full (85 percent), clock-drift (over 500 ms), WAL-archive-lag (over 15 minutes), off-site-anchor-failure, UPS-on-battery, and backup-failure alerts push to System Managers and Stakeholders and appear on the status page, which the System Manager can open from the phone over the tunnel.
- Secrets in a vault or age-encrypted files unlocked at boot by an operator-held key; environment fails fast if any required secret is missing.
- Least-privilege identities per worker (anchor writer, backup writer, mail sender, push sender).
- Dependency and container scanning in CI; licence scan in CI; SBOM per release.
- Structured audit of admin actions in the console: every setting change is an approval-eligible document and an audit event.
- Break-glass: a System Manager action with mandatory reason, time-boxed elevated role, Critical push to all Stakeholders, and an exceptions-report entry; the elevation itself is anchored in the chain.
- Quarterly access review; annual external penetration test; incident runbook with a 72-hour disclosure path if personal data is involved.

## Threat model summary

| Threat | Control |
| --- | --- |
| Accountant edits a registered invoice | No update path; triggers; correction request only |
| Approver approves own document | SoD at API; independence rule; audit |
| Client smuggles Approved state | Decision fields restored from storage; state version |
| Two approvers race | Row lock; state version; already-decided response |
| Draft changed after approval | Approved hash must equal posted hash |
| Stolen mobile token | Device-bound DPoP; short access token; refresh rotation with reuse detection |
| Shared device shows previous user's data | Cache wipe on logout; no cross-user fallback; app lock |
| DBA rewrites rows and recomputes hashes | Chain head anchored hourly in compliance-mode storage with KMS signature; daily email to auditor |
| DBA drops triggers | Superuser vaulted; DDL event trigger to external sink; nightly trigger presence check |
| Restore from a doctored backup | Manifest verification; anchor comparison; Stakeholder approval before serving writes |
| Vendor bank account swapped | Change is an approval document; entering user cannot approve; step-up; exceptions report |
| Push body used to trick an approver | Data-only push; live fetch before any action |
| Backdated posting after review | Period status; named role for prior-open-period; exceptions report; closed periods refuse |
