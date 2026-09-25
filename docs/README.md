# Documentation

Everything about what the system must do and why lives in `spec/`. Everything about how to
contribute lives at the repository root (`CONTRIBUTING.md`, `WAVE1.md`). Operational runbooks live
in `deploy/nuc/`.

## The specification (`spec/`)

| File | Contents |
| --- | --- |
| `spec/00-README.md` | how to read the spec, conventions |
| `spec/01-requirements.md` | numbered requirements, `R<section>.<n>`; every PR cites the ones it satisfies |
| `spec/02-architecture.md` | system shape, technology choices, module boundaries, ADRs |
| `spec/03-domain-model.md` | entities and document types |
| `spec/04-api-contracts.md` | transport, envelopes, error codes, endpoints, event payload; governs `contracts/` |
| `spec/05-security-and-audit.md` | identity, immutability, hash chain, external anchoring, approvals |
| `spec/06-ux-spec.md` | personas, the ten flows, console and mobile behaviour |
| `spec/07-tracks-and-tasks.md` | tracks, phases, task IDs `P<phase>.<n>` with R-IDs and test IDs |
| `spec/08-acceptance-tests.md` | one-line invariants, `A1`, `B9`, `D7`, ... named in CI |
| `spec/09-glossary.md` | terms |
| `spec/10-execution-playbook.md` | targets, compose stack, agent protocol, waves, go-live |

## Architecture decision records

All ADRs are in `spec/02-architecture.md` "Architecture decision records". Summary:

| ADR | Decision |
| --- | --- |
| ADR-01 | Build the domain; do not fork an ERP (ERPNext is GPL-3.0) |
| ADR-02 | Modular monolith before services |
| ADR-03 | PostgreSQL is the ledger; immutability by grants and triggers |
| ADR-04 | Outbox for every side effect |
| ADR-05 | Approvals are a state machine with a posting token |
| ADR-06 | Hash chain plus external anchor |
| ADR-07 | Data-only push |
| ADR-08 | Snapshot analytics |
| ADR-09 | Contract first |
| ADR-10 | Two native mobile apps (Kotlin and Compose, Swift and SwiftUI) |
| ADR-11 | Posting rules are configuration |
| ADR-12 | Business-hours clocks |
| ADR-13 | Go for the backend |
| ADR-14 | River as the outbox (MPL-2.0, used unmodified) |
| ADR-15 | Zitadel headless instead of Keycloak (AGPL-3.0, run unmodified as a service) |
| ADR-16 | Single on-premises NUC for phase 1 |

## Changing the spec

- New behaviour: add an R-ID to `spec/01-requirements.md` in its own PR before code.
- New invariant: append a test ID to `spec/08-acceptance-tests.md`; never renumber.
- New endpoint or event: change `spec/04-api-contracts.md` with a version bump and `contracts/` in
  one contracts-only PR (`CONTRIBUTING.md` section 3).
- Every spec PR is human-reviewed (`.github/CODEOWNERS`).
