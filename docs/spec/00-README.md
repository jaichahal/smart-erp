# Smart ERP Specification

Working name: Smart ERP. A proprietary ERP for a single UAE trading and light-conversion company, with an accountant console, and Android and iOS apps for sales agents, collectors, drivers, accountants, and management. Built from scratch on permissive-licence components. No ERPNext, Frappe, or other GPL code in the runtime.

This folder is the hand-off to the delivery team. The rationale behind every decision lives in the planning document at `~/.cursor/plans/smart_erp_blueprint_4ff601aa.plan.md`; this spec states what to build and how to prove it is built.

## Files

- `01-requirements.md`: numbered, testable requirements. Every task and every acceptance test traces to one or more R-IDs.
- `02-architecture.md`: system shape, technology choices, architecture decision records, non-functional targets.
- `03-domain-model.md`: entities, states, posting rules, numbering, periods, item classes.
- `04-api-contracts.md`: the contract-first API surface, envelopes, error codes, event payloads. Frozen per version; changes land here before code.
- `05-security-and-audit.md`: identity, authorisation, immutability, hash chain, external anchoring, device security, operational controls.
- `06-ux-spec.md`: personas, information architecture, screen inventory, notification interaction, design system requirements.
- `07-tracks-and-tasks.md`: workstreams, phases, tasks with acceptance criteria and dependencies, team shape.
- `08-acceptance-tests.md`: invariants that must hold, as one-line assertions grouped by area, to be automated first.
- `09-glossary.md`: terms, roles, abbreviations.
- `10-execution-playbook.md`: how agents turn this spec into software. Test-driven development, Working baseline, and Mobile UI automation in that file are the execution rules.
- `14-crm.md`: Phase 10 CRM. Leads and pipeline that convert into the existing customer master. Starts after Phase 3.
- `15-hr.md`: Phase 11 HR. Employees, documents expiry, and leave on the existing approval engine. Starts after the approval engine and cost centres exist.
- `16-payroll.md`: Phase 12 Payroll. UAE monthly pay, WPS file, and a gratuity or pension path, posted through the existing ledger. Depends on Phase 11.

## How to use this spec

1. Read `01` and `02` fully before anything else.
2. Contracts in `04` are the only cross-team dependency. A team that needs a shape another team owns writes the change into `04` first and notifies the owner.
3. Every pull request names the R-IDs it satisfies and the tests in `08` it makes pass.
4. Anything not in this spec is out of scope until it is added here with an R-ID.
5. Follow `10-execution-playbook.md` for execution: Test-driven development, Working baseline, and Mobile UI automation.

## Conventions

- SHALL: mandatory. SHOULD: expected unless a documented reason exists. MAY: optional.
- Money is always a decimal with a currency code. No bare numbers are money.
- Timestamps are UTC ISO 8601 on the wire; the database assigns them.
- "Registered" means a document has reached its terminal, immutable state and has consumed a document number.
- "Stakeholder" means a user holding the management role (partners, CFO, CTO, or whoever the company configures).

## Status

Version 1.0.0, 2026-09-25. Derived from planning revision 3 with experience-design, hardening, and tamper-evidence sections included.
