# Contributing to Smart ERP

This file is the agent operating protocol from `docs/spec/10-execution-playbook.md`, rewritten as
instructions to you, the contributor. Agents and humans follow the same rules; the rules are what
make many contributors safe to run in parallel on one repository. Read this once before your
first task and again whenever CI rejects something you did not expect.

## 1. Start from a task card

You never start from an idea; you start from one task in `docs/spec/07-tracks-and-tasks.md`.
Your card gives you:

- the task ID and its text (for example `P1.7`);
- the requirement IDs it satisfies (`R2.1` to `R2.11`), from `docs/spec/01-requirements.md`;
- the acceptance test IDs it must make pass (`D1` to `D14`), from `docs/spec/08-acceptance-tests.md`;
- the directories you own for this task;
- the directories you must not touch;
- the contract files you may read but not change.

Wave 1 cards are in `WAVE1.md`. If your card is missing any of the above, stop and ask; do not
guess a scope. New requirements go into `docs/spec/01-requirements.md` with a new R-ID first, in
their own PR, before any code that depends on them.

## 2. One task, one branch, one worktree

Every task gets its own branch and its own working directory so contributors never share a
checkout:

```sh
cd /Users/jaichahal/Projects/smart-erp
git fetch origin
git worktree add ../smart-erp-<task> -b task/<id>-<slug> origin/main
cd ../smart-erp-<task>
```

Example: `git worktree add ../smart-erp-b1 -b task/P1.7-approval-engine origin/main`.

Branch names are `task/<id>-<slug>`: the task ID exactly as in `07`, then a short kebab-case
slug. Work only inside your owned directories plus your own test files. Touching any other
directory is rejected by `.github/CODEOWNERS` before a human looks at it. When the PR merges,
`git worktree remove ../smart-erp-<task>`.

Never push to `main`. Never force-push a shared branch.

## 3. Contract first

`docs/spec/04-api-contracts.md` and `contracts/` change before code (ADR-09).

If your task needs a shape another team owns, a new endpoint, a new field, or a new event type:

1. Open a PR that changes **only** `contracts/` (and `docs/spec/04-api-contracts.md` with a
   version bump: minor for additive, major for breaking, with a migration note). The same PR
   must also commit the regenerated `apps/api/internal/kit/oapi/gen.go` from `just gen`, and
   no other file under `apps/`.
2. Wait for it to merge. Every contract PR is human-reviewed.
3. Then open your code PR against the merged contract.

`.github/workflows/contracts.yml` enforces this and more on every pull request:

| Job | Fails when |
| --- | --- |
| `openapi-lint` | `contracts/openapi/openapi.yaml` does not pass `redocly lint` |
| `event-schema` | an example in `contracts/events/examples/` does not validate against `event.schema.json` |
| `openapi-drift` | `go generate ./...` in `apps/api` produces a different `internal/kit/oapi/gen.go` than the one committed. Run `just gen` and commit; never hand-edit generated files |
| `contract-first` | the PR changes files under `contracts/` **and** files under `apps/` other than `apps/api/internal/kit/oapi/gen.go`. The regenerated file from `just gen` travels with the contract. Any other `apps/` file still fails. Message: *contract changes must land in their own PR* |
| `immutable-change` | see section 4 |
| `spec-traceability` | the PR body does not name at least one requirement ID (`R<n>.<n>`) and one acceptance test ID (`A1`, `D7`, `I16`, ...) |

You may read any contract file. You may change a contract file only in a contracts-only PR.

## 4. Migrations and the `immutable-change` label

Migrations live in `apps/api/migrations/*.sql` (goose) and are owned by Track B. Tables created
with the kit's immutable helper (`internal/kit/immutable`) carry grants, `BEFORE UPDATE OR
DELETE` triggers and hash-chain columns; a migration that alters one of them is the single most
dangerous change in this repository (`docs/spec/05-security-and-audit.md`, test B3).

Rule: **any** PR that changes a file under `apps/api/migrations/` must carry the GitHub label
`immutable-change`. With the label, a Track B reviewer is required by `CODEOWNERS` and the human
reviews it personally.

Escape hatch for migrations that touch no immutable table (a new plain table, an index, a
lookup-table seed): instead of the label, put this exact line on its own in the PR body:

```
migrations: mutable-only
```

CI then passes the `immutable-change` job with a notice, and the reviewer is expected to confirm
the assertion. Using the escape hatch on a migration that does touch an immutable table is a
review failure and the PR is reverted.

## 5. Module boundaries

`apps/api/internal/kit/**` is the only shared Go code: error envelope (`kit/apierr`), idempotency
(`kit/idempotency`), `If-Match` (`kit/ifmatch`), RLS session scoping (`kit/rls`), audit emission
(`kit/audit`), canonical JSON and hashing (`kit/canon`), immutable-table helper (`kit/immutable`),
outbox enqueue via River (`kit/outbox`), per-test databases (`kit/testdb`), config (`kit/config`),
observability (`kit/obs`) and the generated OpenAPI wire types (`kit/oapi`) and the success envelope and Deps (`kit/httpx`).

- A module under `apps/api/internal/<module>` may import the kit, the standard library and
  third-party modules. It may **not** import another `internal/<module>`. `depguard` in
  `apps/api/.golangci.yml` fails the build if it does (P1.1 "module boundary lint").
- Cross-module reads go through a service interface or a read model, never a table join
  (`docs/spec/02-architecture.md` "Module boundaries").
- If you find yourself writing an envelope, an idempotency check, a hash or an outbox insert, stop:
  it belongs in the kit. Open a kit PR (Track A reviews it) or ask Track A. Do not reimplement.
- `apps/api/cmd/**` wires modules together and may import anything under `internal/`.

## 6. Definition of done

Done is mechanical. Your PR is done when every line below is true; the PR template lists them as
a checklist and CI checks the ones it can.

- The PR body lists the R-IDs and the `08` test IDs (CI: `spec-traceability`).
- `just lint` passes (CI: `ci.yml` lint).
- The module's tests pass with `-race` and the acceptance tests named in your task pass
  (CI: `ci.yml` test).
- The contract suite passes (CI: `contracts.yml`).
- The licence scan and the SBOM pass (CI: `ci.yml` licence, sbom). A GPL or LGPL dependency fails
  the build (ADR-01); MPL-2.0 (River) is allowed because it is used unmodified (ADR-14).
- New invariants got new `08` IDs in the same PR. Append; never renumber.
- Strings are externalised; no user-facing literal in code.
- Any new screen has an RTL and an accessibility check recorded in the PR.
- Any new operational component has a `deploy/nuc/*.md` runbook entry.
- Generated files came from `just gen`.
- Commits follow `<task-id>: <imperative summary>` (section 10).

## 7. Merge queue, not merge buttons

PRs land through the GitHub merge queue. The queue re-runs the full suite on the merged result
and only then fast-forwards `main`. You mark the PR ready and add it to the queue; you do not
click merge, and you never push to `main`.

Two standing roles keep the queue moving. The **Integrator** watches the queue, resolves trivial
conflicts in generated files by re-running `just gen`, and hands real conflicts back to the owning
contributor. The **Nightly** runs `just test-all`, the chain verifier against `offsite-sim`, and a
backup and restore rehearsal into a scratch database, and files issues for anything red. If the
Nightly quarantines one of your tests as flaky it opens an issue with the test ID; fix the test,
do not retry it silently.

## 8. What humans review

The human owner personally reviews:

- every contract PR (`contracts/`, `docs/spec/04-api-contracts.md`);
- every PR with the `immutable-change` label;
- every PR from Track B (`internal/approvals`, `internal/audit`, corrections);
- the Phase 0 prototypes before the matching build task starts;
- each phase's acceptance walk-through with the Accountant.

Everything else is reviewed by the Integrator and one other contributor. Request review from the
CODEOWNERS entry for your directory; the mapping is in `.github/CODEOWNERS`.

## 9. Local commands (Justfile)

The `Justfile` at the repository root is the only supported way to run things locally; CI calls
the same targets so a green Mac means a green CI.

| Target | Purpose |
| --- | --- |
| `just up` / `just down` | start and stop the compose stack (`dev` profile) |
| `just reset` | drop and re-migrate the `erp` database |
| `just migrate` | run goose migrations from `apps/api/migrations` as `erp_migrator` |
| `just template` | create `erp_template` from `erp`; tests clone it per run |
| `just db <name>` | create your own test database `erp_test_<name>` from the template; prints the `export` line |
| `just gen` | `go generate ./...` (oapi-codegen and friends) plus canonical fixture regeneration; commit the output |
| `just openapi-check` | `redocly lint` on `contracts/openapi/openapi.yaml` |
| `just test [pkg]` / `just test-unit` / `just test-all` | tests for one package pattern (default `./...`) with `-race`; unit only with `-short`; everything |
| `just lint` | golangci-lint with `apps/api/.golangci.yml`, including the module boundary rules |
| `just licence` | go-licenses check with the policy in `.github/workflows/ci.yml` |
| `just sbom` | syft SPDX JSON for `apps/api` |
| `just seed` | personas, chart of accounts, sample SKUs and parties |
| `just anchor-now`, `just backup-now`, `just restore-rehearsal <bucket>` | operations, see `deploy/nuc/` |

### Your own test database

Tests run against the shared PostgreSQL from the compose stack, not testcontainers, so eight
contributors testing at once do not start eight PostgreSQL servers. Each contributor uses a fresh
database cloned from `erp_template`:

```sh
just up
just migrate
just template
just db b1                            # CREATE DATABASE erp_test_b1 TEMPLATE erp_template
export ERP_TEST_DATABASE=erp_test_b1  # the full database name; `just db` prints this line
just test ./internal/approvals/...
```

Without `ERP_TEST_DATABASE`, `kit/testdb` creates a throwaway `erp_test_<random>` database per
test package and drops it afterwards; set the variable to reuse one and inspect it after a failure
(`ERP_TEST_KEEP=1` keeps a throwaway one).

Environment the tests and tools read (the compose stack and CI use the same values):

```
ERP_ADMIN_DATABASE_URL=postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable
ERP_MIGRATOR_DATABASE_URL=postgres://erp_migrator:erp_migrator@localhost:5432/erp?sslmode=disable
ERP_DATABASE_URL=postgres://erp_app:erp_app@localhost:5432/erp?sslmode=disable
ERP_TEST_DATABASE=erp_test_<your name>     # optional, see above
```

The `just` targets source these from `deploy/compose/.env.dev.host` (created from the `.example`
on first run); CI sets the same values directly.

## 10. Commit messages and PR titles

```
<task-id>: <imperative summary>
```

Examples: `P1.7: add approval request state machine`, `P1.7: refuse whitespace rejection reason`,
`P1.1: pin golangci-lint to v2.14`. The PR title follows the same form. The PR body follows
`.github/pull_request_template.md`; do not delete its headings, CI reads them.

## 11. Tooling on the Mac

Go 1.27.1 (`go.mod` says `go 1.27.1`), golangci-lint 2.14, sqlc, goose, oapi-codegen, just,
age, Docker Desktop with Compose 2.24 or later, `gh` logged in. `brew install go golangci-lint sqlc
goose oapi-codegen just age` then `gh auth login`. Open pull requests with `gh pr create`.

## 12. When CI rejects you

| Check | What to do |
| --- | --- |
| `contract-first` | Split the PR: contracts first, code after it merges |
| `immutable-change` | Add the label, or add `migrations: mutable-only` to the body if the assertion is true |
| `spec-traceability` | Fill in the Requirements and Acceptance tests sections |
| `openapi-drift` | `just gen`, commit `gen.go` |
| `lint` with `depguard` | You imported another module; go through an interface or move the code to the kit |
| `licence` | Replace the dependency; GPL and LGPL are forbidden (ADR-01) |
| CODEOWNERS review missing | You touched a directory outside your card; move the change to a PR owned by that track |

## Merging on GitHub Free

The repository is private on a GitHub Free plan, which does not offer branch protection,
rulesets, required status checks, or the merge queue. The equivalent is enforced by
convention and tooling until the plan changes:

- Nobody pushes to `main`. Run `just hooks` once per clone; the `pre-push` hook refuses
  it. Agents are told the same in their task prompts.
- Every change is a pull request from a `task/<id>-<slug>` branch in its own worktree.
- The Integrator agent is the only merger. It merges with
  `gh pr merge <n> --squash --auto --delete-branch`, and only when `gh pr checks <n>`
  shows every job green (lint, test, licence, sbom, openapi drift, contract-first,
  immutable-change label, spec traceability) and the PR has one approving review from
  someone other than its author. `--auto` waits for checks before merging.
- Human review is required on: any PR touching `contracts/`, any PR carrying
  `immutable-change`, any PR from Track B, and any PR touching `apps/api/internal/kit`.
  The Integrator does not merge those without a human approval.
- When the repository moves to GitHub Pro or an organisation plan, apply the ruleset in
  `docs/github-ruleset.json` with
  `gh api -X POST repos/jaichahal/smart-erp/rulesets --input docs/github-ruleset.json`
  and delete this section.
