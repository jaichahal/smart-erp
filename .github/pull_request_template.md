<!--
Title format: <task-id>: <imperative summary>      e.g.  P1.7: add approval request state machine
CI reads this body. Keep the section headings. Text inside HTML comments is ignored by the checks.
-->

## Task ID

<!-- One task from docs/spec/07-tracks-and-tasks.md, e.g. P1.7. One task per PR. -->

## Requirements (R-IDs)

<!-- Every requirement this PR satisfies or advances, e.g. R2.1, R2.6. CI fails without at least one. -->

## Acceptance tests (08 IDs)

<!-- Every 08 test this PR implements or makes pass, e.g. D1, D7. New invariants get new IDs in the same PR. CI fails without at least one. -->

## Contract changes

<!-- "no", or "yes" with a link to the already-merged contract PR. A PR may change contracts/ OR apps/, never both. -->

- Changes `contracts/`: no
- Contract PR:

## Migrations

<!-- Exactly one of:
       none
       migrations: mutable-only        (touches no table created with the immutable helper; reviewer confirms)
       immutable-change                (add the `immutable-change` label; Track B reviewer required)
     The literal line `migrations: mutable-only` is what CI looks for. -->

none

## Runbook entry

<!-- Required if this PR adds or changes an operational component. Link the deploy/nuc/*.md entry, or write "n/a". -->

n/a

## Screens

<!-- Required if this PR adds or changes a screen. Otherwise "n/a". -->

- RTL check: n/a
- Accessibility check: n/a
- Strings externalised: n/a

## Definition of done

<!-- Mirrors docs/spec/10-execution-playbook.md "Definition of done is mechanical". Tick every line or explain why it does not apply. -->

- [ ] One task, one branch (`task/<id>-<slug>`), one worktree; only owned directories touched
- [ ] R-IDs and 08 test IDs listed above
- [ ] Lint passes (`just lint`)
- [ ] The module's tests and the acceptance tests named in the task pass (`just test ./internal/<module>/...`)
- [ ] Full contract suite passes (contracts.yml: openapi-drift, contract-first, immutable-change, spec-traceability)
- [ ] Licence scan and SBOM pass (`just licence`, `just sbom`)
- [ ] New invariants have new 08 IDs in this PR (docs/spec/08-acceptance-tests.md, append only, never renumber)
- [ ] Contract changes, if any, landed first in their own PR with a version bump in docs/spec/04-api-contracts.md
- [ ] Migrations touching immutable tables carry the `immutable-change` label
- [ ] Strings externalised; no user-facing literal in code
- [ ] New screen has an RTL and accessibility check
- [ ] New operational component has a `deploy/nuc/*.md` runbook entry
- [ ] Generated files were produced by `just gen`, not edited by hand
- [ ] No cross-module import; only `internal/kit/**` is shared code
- [ ] Commits use `<task-id>: <imperative summary>`
