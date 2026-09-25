# 10 Execution Playbook

How to turn this spec into software with a team of AI agents working in parallel, a single Mac as the development machine, and one Linux NUC as phase 1 production. This file is written for the agents as much as for the human running them; every agent reads it before its first task.

## The two concrete targets

| | Development (this machine) | Phase 1 production |
| --- | --- | --- |
| Hardware | Apple Silicon Mac, arm64, 10 cores, 16 GB RAM | One ASUS x86_64 Linux NUC, 8+ cores, 64 GB RAM preferred (32 minimum), 2 TB NVMe plus a second disk or USB NAS for backup staging |
| OS | macOS with Docker Desktop | Ubuntu Server LTS (or Debian stable), unattended security updates |
| Runtime | `docker compose --profile dev` | `docker compose --profile prod` under systemd |
| Images | `linux/arm64` | `linux/amd64` |
| Ingress | Caddy with self-signed TLS on `*.localhost` | Caddy with automatic TLS on a public hostname, or Cloudflare Tunnel or Tailscale Funnel if no inbound port |
| Off-site | Second local MinIO bucket `offsite-sim` and a local signing key, to exercise the code paths | Cloud S3-compatible compliance-mode bucket, off-prem KMS or Vault Transit |
| Operator access | Local | Tailscale only |
| Differences allowed | Secrets, hostnames, resource limits, off-site endpoints. Nothing else. | |

Same services, same versions, same compose file. Parity is a test (I10), not a convention.

## Machine preparation (once, before any agent starts)

Development Mac, checked on 2026-09-25: Docker Desktop is installed but the daemon was not running; Docker CLI 20.10 and Compose 2.13 are old; Go is 1.21 and the spec targets 1.26.

1. Update Docker Desktop to a current release (Compose 2.24 or later is needed for `include` and `develop.watch`). In Docker Desktop settings give it 6 CPUs and 8 GB RAM; leave the rest for the agents' editors, Go builds, and Xcode or Android Studio.
2. `brew install go@1.26 golangci-lint sqlc goose buf oapi-codegen just age` and confirm `go version` reports 1.26.
3. `gh auth login` for the repository; agents open pull requests through `gh`.
4. Create a GitHub organisation or repository and enable the merge queue and required checks.
5. Provision the off-site pieces the agents cannot create for you: a cloud S3-compatible bucket with object lock in compliance mode (anchors, WAL, backups), a KMS asymmetric signing key or Vault Transit key off-prem, and a managed observability sink (any OpenTelemetry-compatible SaaS). Put their endpoints and least-privilege credentials into the `prod` secrets file; dev uses local simulations.

NUC, before go-live (Track I task P1.13 does this; listed here so procurement can start now): buy the NUC per the sizing table, a matching spare or a written procurement lead time accepted by management, a UPS with USB signalling, and the second disk or NAS. Install Ubuntu Server LTS, Docker Engine with Compose plugin, Tailscale, and the systemd units from `deploy/systemd/`.

## Repository layout

One monorepo. Ownership is by directory so parallel agents rarely touch the same files.

```
smart-erp/
  spec/                      this folder, moved in; the source of truth
  contracts/
    openapi/                 OpenAPI 3.1, one file per resource family, bundled in CI
    events/                  JSON Schema for outbox and push events
    fixtures/                canonical-JSON and hash test vectors shared by server and apps
  apps/
    api/                     Go: cmd/api, cmd/worker, cmd/scheduler, internal/<module>, internal/kit
    console/                 React
    android/                 Kotlin
    ios/                     Swift
  packages/
    tokens/                  design tokens exported to Compose, SwiftUI, CSS
  deploy/
    compose/                 docker-compose.yml, compose.dev.yml, compose.prod.yml, Caddyfile, env templates
    systemd/                 smart-erp.service, smart-erp-backup.timer, smart-erp-anchor.timer
    nuc/                     bootstrap.sh for a fresh NUC, restore-runbook.md, ups.md, updates.md
    scripts/                 backup.sh, restore.sh, anchor.sh, verify-chain.sh
  .github/workflows/         ci.yml (lint, test, licence, sbom), build.yml (multi-arch push), contracts.yml (drift)
  CODEOWNERS                 directory to track mapping
  Justfile                   the commands below
```

`apps/api/internal/kit` is the shared Go kit owned by Track A: error envelope, idempotency, If-Match, RLS session scoping, audit emission, immutable-table migration helper, hash canonicaliser, outbox enqueue. Every module imports it; nobody but Track A changes it without a kit PR.

## Local stack (docker compose)

Services and pinned multi-arch images. Versions are pinned by digest in the real file; tags here for readability.

| Service | Image | Purpose | Dev notes |
| --- | --- | --- | --- |
| postgres | `postgres:16` | data, ledger, River jobs, Zitadel database | one instance, separate databases `erp`, `zitadel`, plus `erp_test_<n>` per agent |
| valkey | `valkey/valkey:8` | pub/sub, JTI replay, rate limits | |
| minio | `cgr.dev/chainguard/minio:latest` (upstream MinIO rebuilt unmodified; `minio/minio` is no longer published on Docker Hub) | attachments, PDFs, snapshots, local backup staging; buckets `erp-files`, `erp-backups`, `erp-anchors`, and in dev `offsite-sim` with object lock | object lock requires buckets created with lock enabled at creation |
| zitadel | `ghcr.io/zitadel/zitadel:v3.4.15` (latest v3.x; v4 is a separate major) | identity, run unmodified, headless | bootstrap creates a machine user and PAT for the API and a `dev` org with seeded personas |
| caddy | `caddy:2-alpine` | TLS termination and reverse proxy | dev binds 8443 (HTTPS) and 8880 (HTTP) because 8080 is the API; prod automatic TLS or tunnel |
| api, worker, scheduler | built from `apps/api` | the product | `develop.watch` rebuilds on save in dev |
| headless-shell | `chromedp/headless-shell` | PDF rendering | multi-arch |
| mailpit | `axllent/mailpit` | outbound mail catcher and inbound IMAP for the supplier-invoice mailbox | dev only; prod uses the company relay and mailbox |
| push-sink | built from `apps/api/cmd/pushsink` | logs FCM v1 and APNs payloads instead of sending | dev only; validates payloads against `contracts/events` |
| otel-collector | `otel/opentelemetry-collector-contrib` | local buffer, exports to the off-prem sink in prod | dev exports to a local file |

Profiles: `dev` adds mailpit, push-sink, and the `offsite-sim` bucket bootstrap; `prod` adds the anchor and backup timers, the off-site endpoints, and resource limits.

Resource budget on the Mac: the stack idles around 3 GB and peaks near 5 GB with Chromium rendering. Agents run tests against the shared PostgreSQL using a fresh database cloned from a template per run (`CREATE DATABASE erp_test_<agent> TEMPLATE erp_template`), not testcontainers, so six agents testing at once do not start six PostgreSQL containers. CI uses testcontainers.

Justfile targets every agent uses: `just up`, `just down`, `just reset` (drop and re-migrate), `just migrate`, `just gen` (sqlc, oapi-codegen, event types, token exports), `just test <module>`, `just test-all`, `just lint`, `just licence`, `just db <agent>` (create a test database), `just seed` (personas, chart of accounts, sample SKUs and parties), `just anchor-now`, `just backup-now`, `just restore-rehearsal <bucket>`.

## Agent operating protocol

Agents are interchangeable workers; the rules make them safe to run in parallel.

Task card. Every agent starts from one task in `07-tracks-and-tasks.md` and receives: the task ID and text, the R-IDs, the acceptance test IDs from `08`, the directories it owns for this task, the directories it must not touch, and the contract files it may read but not change.

One task, one branch, one worktree. Branch `task/<id>-<slug>` in its own `git worktree` so agents never share a working directory. The agent works only inside its owned directories plus its own test files.

Contract first. If the task needs a shape another team owns or a new endpoint or event, the agent's first pull request changes only `contracts/` and `spec/04-api-contracts.md`, with a version bump. It waits for that to merge before writing code against it. An agent that changes a contract and code in one PR is rejected by the `contracts.yml` check.

Immutable tables are guarded. Any migration touching a table created with the immutable helper requires the `immutable-change` label and a Track B reviewer; CI blocks it otherwise.

Definition of done is mechanical. The PR description lists R-IDs and `08` test IDs; CI runs lint, the module's tests, the full contract suite, the licence scan, and the SBOM; the acceptance tests named in the task must pass; new invariants get new `08` IDs in the same PR; strings are externalised; any new screen has an RTL and accessibility check; any new operational component has a `deploy/nuc/*.md` runbook entry.

Merge queue, not merge buttons. PRs land through the queue with the full suite re-run on the merged result; agents never push to `main`.

Two standing agents. An Integrator runs continuously: it watches the queue, resolves trivial conflicts in generated files, re-runs `just gen`, and flags real conflicts back to the owning agents. A Nightly agent runs `just test-all`, the chain verifier against `offsite-sim`, a backup and restore rehearsal into a scratch database, and files issues for anything red.

Human checkpoints. You review: every contract PR, every `immutable-change` PR, every PR from Track B (approvals, audit, corrections), the Phase 0 prototypes before the matching build task starts, and each phase's acceptance walk-through with the Accountant. Everything else is reviewed by the Integrator and a second agent.

## Waves: what runs in parallel

Wave 0, sequential, one or two agents, roughly the first week. Nothing else starts until this merges, because everything depends on it.

1. Repository skeleton, Justfile, CI workflows, CODEOWNERS, licence scan, multi-arch build (P1.1).
2. Compose stack with all services healthy on the Mac, `dev` and `prod` profiles, seed script, per-agent test databases (P1.13 dev half).
3. The Go kit: envelope, idempotency, If-Match, RLS scoping, audit emission, immutable-table helper with triggers and hash chain, canonical JSON with shared fixtures, River outbox enqueue (P1.5 core, P1.10).
4. Contract bundle v1.0.0 generated and published as Go, TypeScript, Kotlin, and Swift types.

Wave 1, up to seven agents in parallel, no directory overlap:

- A1: identity broker to Zitadel, token issuer, DPoP gateway, step-up (P1.2). Owns `internal/identity`.
- A2: roles, permissions, RLS policies, SoD matrix, access review (P1.3). Owns `internal/authz`.
- B1: approval engine (P1.7). Owns `internal/approvals`.
- B2: anchoring worker, verifier, off-site bucket and KMS wiring, backup and restore scripts (P1.6, P1.15). Owns `internal/audit`, `deploy/scripts`.
- C1: company, periods, holiday calendar, clocks, numbering (P1.4). Owns `internal/ledger/periods`, `internal/clocks`.
- E1: outbox consumers, WebSocket hub, FCM v1 and APNs senders, preferences, alert rules (P1.8). Owns `internal/notifications`.
- E2: journey engine (P1.14). Owns `internal/journeys`.
- G1 and F1 and F2: console shell, Android shell, iOS shell (P1.12, P1.11), against the published contract types and a mock server generated from OpenAPI until the real endpoints land.
- H: Phase 0 design work proceeds in Figma throughout Wave 1.
- I: NUC procurement, OS build, systemd units, Caddy or tunnel, Tailscale, UPS, unattended updates, off-site bucket and KMS provisioning, observability sink (P1.13 prod half).

Wave 2, Phase 2 tasks, similar shape: C owns chart of accounts, posting rules, journals, masters, import and Go-Live; D owns stock ledger and reservations; G owns master screens, print designer, Day Book; E owns saved views and scheduled reports.

Wave 3 onward follows the phase order in `07`. Phases 4 and 6 run in parallel after Phase 3 and Phase 2 respectively. Mobile agents for a persona start the moment its Phase 0 prototype passes.

Rule of thumb for how many agents: one per owned directory that has a ready task, plus the Integrator and the Nightly agent. On a 16 GB Mac the practical ceiling is about eight concurrent agents with the stack running; beyond that builds and tests start thrashing.

## Go-live sequence on the NUC

1. Restore drills pass onto a clean machine from BOTH targets, the external drive and the off-site bucket, with identical chain heads matching the anchor (I17, I34), and the signed drill reports are stored. Do not go live without this.
2. Bootstrap the NUC with `deploy/nuc/bootstrap.sh`: Docker, Tailscale, systemd units, secrets unlocked by the operator key, `docker compose --profile prod pull` by digest.
3. Bring the stack up; the status page shows every service healthy, off-site anchor written, WAL archiving current, backup timer armed, UPS reporting.
4. Run the Go-Live journey with the Accountant: chart of accounts and opening balances, open invoices, opening stock, assets, bank balances; the company unlocks only at a zero trial balance with Stakeholder approval (R18.1).
5. Enrol the first devices, verify a push reaches a phone through the tunnel, approve one real document from the shade.
6. First close at month-end with the checklist; first management pack; first chain verification report to the Stakeholders with the anchor hash.

Runbooks that must exist in `deploy/nuc/` before go-live: restore onto a replacement NUC, disk-full response, UPS power-loss response, OS update window and rollback, certificate or tunnel failure, off-site anchor failure, spare-NUC swap, and the quarterly rehearsal checklist.

## External backup drives and restore drills

Two drives, labelled `ERPBAK-A` and `ERPBAK-B`, rotate: one stays connected to the NUC, the other is carried off-site by a named person. Every hand-over is entered in the console or phone and becomes an audit event.

NUC drive setup (Track I, P1.17), per drive:

1. Partition and format: `cryptsetup luksFormat /dev/sdX1` with a key file generated by the secrets store and never copied to the drive; add a second passphrase held by management in a sealed envelope for a total-loss scenario. `mkfs.ext4 -L ERPBAK-A /dev/mapper/erpbak-a`.
2. Record the LUKS UUID in `deploy/nuc/backup-drives.yaml` (the allow-list); unknown drives are ignored.
3. Install the systemd units from `deploy/systemd/`: `erpbak@.service` unlocks and mounts by UUID under `/mnt/erpbak/<label>` with mode 0700 owned by the backup user; a udev rule triggers the unit on plug-in and `smart-erp-backup.timer` runs nightly after the off-site upload. Never rely on auto-mount by the desktop stack.
4. Permissions: only the backup user can read the mount; the API and workers cannot see it.
5. Verify with `just drive-check`: prints label, UUID, free space, encryption state, and whether the drive is on the allow-list.

Nightly order on the NUC: database and object archives to local staging, manifest, upload to the off-site bucket and verify, write to the drive if present and re-read verify, prune per drive policy, sync and unmount, emit safe-to-remove, update the status page with off-site and drive results separately.

Drill schedule: weekly automatic on the NUC (`smart-erp-drill.timer`, maintenance window, restores from the drive if present, otherwise from the off-site bucket, and alternates targets week to week); monthly onto a separate machine from the off-site bucket alone (the Mac or a borrowed NUC) run by the System Manager. Both produce signed drill reports stored off-site and in the chain.

Manual drill, System Manager, from the console: Admin, Operations, Run restore drill, choose target and backup id, confirm; progress and the per-proof results stream to the screen, and the signed report is linked when done. From the CLI on the NUC: `just drill --target drive|offsite [--backup <id>]`. The drill uses compose project `smart-erp-drill`, PostgreSQL on port 5433 with its own data directory under `/var/lib/smart-erp-drill`, MinIO prefix `drill/`, and it records production row counts before and after so I29 can be asserted every time. A production restore is `just restore --target <t> --backup <id> --approval <token>` and refuses without a Stakeholder approval token.

Mac dev equivalent, so the drive and drill code is exercised every day: `just dev-drive-create` makes a 20 GB sparse image at `~/.smart-erp/erpbak-dev.img`, formats it as an age-encrypted archive target (macOS has no LUKS; the engine's archive-encryption mode is used) with a dev key, and mounts it via a loopback so the same UUID allow-list and re-read verification paths run. A real USB stick can be used instead with `just dev-drive-adopt /Volumes/<name>`. `just drill --target drive` on the Mac restores into the `smart-erp-drill` compose project against the `offsite-sim` bucket and the dev drive, and the Nightly agent runs it. Tests I19 to I34 run in CI against the image-backed drive.

## Risks specific to running agents in parallel, and the mitigations already in place

- Duplicated helpers: the kit is the only place for cross-cutting code, and the module boundary lint fails a PR that reimplements an envelope, an idempotency check, or a hash.
- Contract drift: generated types are checked into `contracts/` outputs and CI fails on drift; agents cannot hand-edit generated files.
- Flaky tests from shared infrastructure: per-agent template databases and deterministic seeds; the Nightly agent quarantines a flaky test by ID and opens an issue rather than retrying silently.
- Silent scope creep: a PR that touches a directory outside its task card is rejected by CODEOWNERS; new requirements go into `01` with an R-ID first.
- Security regressions: every PR runs the `08` A and B suites regardless of area; the immutable-change label and Track B review stand between any agent and the audit chain.
- Single-node production forgotten in design: any task that would need a second node (replication, failover, load balancing) is out of scope by ADR-16 and is filed as a note, not a task; the architecture keeps state only in PostgreSQL and MinIO so that note stays cheap to act on later.

## Day one checklist for the human

- [ ] Docker Desktop updated and running with 6 CPUs and 8 GB
- [ ] Go 1.26 and the tools above installed
- [ ] Repository created, merge queue and required checks enabled, `CODEOWNERS` committed
- [ ] Off-site bucket, KMS key, and observability sink provisioned; credentials in the `prod` secrets file, never in the repo
- [ ] NUC, spare or lead time, UPS, second disk, and two external backup drives ordered
- [ ] Accountant, one Stakeholder, one agent, one collector, and one driver booked for Phase 0 usability sessions and the real-month-of-paper walkthrough
- [ ] Wave 0 agent started on P1.1
