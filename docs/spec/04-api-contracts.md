# 04 API Contracts

Version 1.1.0. This file changes before code does. Additive changes bump the minor version; breaking changes bump the major version and carry a migration note. Server and clients generate types from the OpenAPI document that this file governs; the OpenAPI file is the machine form, this file is the human form and wins on conflict until the OpenAPI is regenerated.

## Changelog

### 1.1.0

Additive. Combines the identity session routes and the authorisation matrices. No migration for clients of 1.0.0.

- `GET /me/sessions` and `DELETE /me/sessions/{id}` with schema `UserSession`. Session listing and remote revocation (R1.15, A9).
- `/sod-matrix` and `/approval-matrix`, previously named under Masters with no operation or schema. Writes send `If-Match` with `state_version` (`0` on create) and are stored as `pending_approval` until an approval request is decided. No existing schema changes shape.

## Transport

- Base path: `/api/v1`. JSON over HTTPS. All timestamps UTC ISO 8601. All money as `{ "amount": "1234.56", "currency": "AED" }` with amount as a decimal string.
- Authentication: `Authorization: Bearer <access_token>` plus `DPoP: <proof>` signed by the device key for mobile; console uses a session cookie with CSRF token. Access tokens expire in 15 minutes; `POST /auth/refresh` rotates the refresh token.
- Idempotency: every mutating request accepts `Idempotency-Key` (UUID). The server stores the response for 24 hours and replays it for a repeated key with the same body; a different body with the same key is a 409.
- Pagination: `?cursor=&limit=` on every list; response carries `next_cursor` and `total` where cheap.
- Concurrency: every mutable resource carries `state_version`; mutating requests send `If-Match: <state_version>`; a mismatch is 409 with the current state in the body.
- Localisation: `Accept-Language: en | ar` affects messages and print formats, never data.

## Envelopes

Success:

```json
{ "data": { }, "meta": { "as_of": "2026-09-25T10:00:00Z", "request_id": "..." } }
```

Error, on every failure path including 429 and gateway errors:

```json
{ "error": { "code": "PERMISSION_DENIED", "message": "human readable", "details": { }, "request_id": "..." } }
```

Codes: `AUTH_REQUIRED`, `TOKEN_EXPIRED`, `TOKEN_REVOKED`, `DEVICE_MISMATCH`, `STEP_UP_REQUIRED`, `PERMISSION_DENIED`, `VALIDATION_ERROR`, `MISSING_CONFIG`, `NOT_FOUND`, `CONFLICT`, `ALREADY_DECIDED`, `IMMUTABLE`, `PERIOD_CLOSED`, `SOD_VIOLATION`, `CREDIT_HOLD`, `PRICE_FLOOR_HOLD`, `NEGATIVE_STOCK`, `RATE_LIMITED`, `INTERNAL_ERROR`.

## Resource families

Each family lists its endpoints, then any shape that is not obvious from the domain model.

### Identity

- `POST /auth/device/enroll` body `{ public_key, platform, app_version, device_name }` returns `{ device_id }`.
- `POST /auth/session` body `{ login_name }` returns `{ session_id, challenges: { passkey?, totp_required? } }`; `POST /auth/session/{id}/check` body `{ password? , totp?, webauthn_assertion? }` advances factors; when the required factors are verified, `POST /auth/token` body `{ session_id, device_id }` returns `{ access_token, refresh_token, expires_in, user: { id, name, roles[], personas[], company_id }, step_up_methods[] }`. The API brokers these to Zitadel's Session API; clients never call Zitadel directly.
- `POST /auth/refresh`, `POST /auth/logout` (revokes device refresh token, unregisters push token).
- `POST /auth/step-up` body `{ method, code }` returns a short-lived `step_up_token` for one action.
- `GET /me`.
- `GET /me/sessions` returns `{ id, device_id, device_name, platform, created_at, last_seen_at, current, state_version }[]` with `next_cursor` and `total`.
- `DELETE /me/sessions/{id}` ends that session. The owner or a System Manager may call it. A stale `If-Match` is `409 CONFLICT` with the current session in `error.details.current`. Revocation is visible on the next request from the ended device.

### Documents (generic)

All document types share these routes under their family prefix (`/sales-orders`, `/sales-invoices`, `/credit-notes`, `/gate-passes`, `/delivery-notes`, `/receipts`, `/requisitions`, `/lpos`, `/goods-receipts`, `/supplier-invoices`, `/debit-notes`, `/payment-runs`, `/production-entries`, `/stock-counts`, `/stock-adjustments`, `/stock-write-offs`, `/journals`, `/petty-cash-vouchers`, `/expense-claims`, `/asset-*`):

- `GET /{family}` list with filters, saved-view id, cursor.
- `POST /{family}` create draft.
- `GET /{family}/{id}` detail with `flow` (ancestors and descendants), `attachments`, `activity`, `annotations`, `clocks`, `approval`.
- `PUT /{family}/{id}` update draft (If-Match).
- `POST /{family}/{id}/submit` returns approval request.
- `POST /{family}/{id}/annotations` append note or attachment on a registered document.
- `GET /{family}/{id}/print?format=pdf&template=` returns stored PDF if registered, preview if draft.
- `POST /{family}/{id}/correct` opens a correction request (see Corrections).

Draft body shape per family follows `03-domain-model.md`; the OpenAPI file carries field lists.

### Approvals

- `GET /approvals/inbox?state=needs_me|waiting_on_others|fyi` returns cards: `{ request_id, doc_type, doc_number, party, amount, requester, waiting_since, severity, fraud_hints[], allowed_actions[], state_version, deep_link }`.
- `GET /approvals/{id}` live view including the document snapshot at submission and its hash.
- `POST /approvals/{id}/approve` body `{ comment?, state_version, step_up_token? }`.
- `POST /approvals/{id}/reject` body `{ reason, state_version }`; empty reason is 400 `VALIDATION_ERROR`.
- `POST /approvals/{id}/delegate` body `{ to_user_id, until }`.
- `POST /approvals/{id}/snooze` body `{ until }`.
- `POST /notifications/{event_id}/acknowledge`.
- Responses: `{ request_id, state, state_version, decided_by?, decided_at? }`; an already-decided request returns 200 with `ALREADY_DECIDED` in `meta.notice`, not an error.

### Corrections

- `POST /corrections/preview` body `{ original_doc_id, kind, changes{} , reason }` returns `{ before, after, proposed_docs[], reality_checks[], required_approval }`.
- `POST /corrections` commits the request for approval; on approval the server posts the generated documents and returns their ids on `GET /corrections/{id}`.

### Stock and production

- `GET /stock/availability?warehouse=&class=finished_goods&q=` returns per SKU `{ sku, on_hand, reserved, available, uom, price_for_customer?, floor_indicator }`; cost fields are omitted unless the role permits.
- `GET /stock/ledger?sku=&from=&to=`, `GET /stock/ageing`, `GET /stock/dead`, `GET /stock/monthly?month=`.
- `GET /boms`, `POST /boms` (approval), `GET /boms/{id}/versions`.

### Receivables and bank

- `GET /receivables?bucket=&due=today|week|month|overdue&pdc_covered=`.
- `POST /receipts` with `allocations[]` and `on_account`.
- `POST /pdcs/{id}/deposit`, `POST /pdcs/{id}/bounce` (reason).
- `GET /customers/{id}/statement?from=&to=&format=pdf`, `POST /customers/{id}/statement/send`.
- `POST /bank/statements/import`, `GET /bank/reconciliation?account=`, `POST /bank/reconciliation/match`, `POST /bank/reconciliation/rules`.
- `GET /bank/forecast?horizon=13w&case=expected|conservative`.
- `GET /bank/cash-position?date=`.
- `POST /payment-runs/{id}/release` (step-up, second approver above threshold) returns bank file object key.

### Masters

- `GET|POST /customers`, `/vendors`, `/skus`, `/price-lists`, `/price-agreements`, `/tax-codes`, `/accounts`, `/dimensions`, `/posting-rules`, `/holiday-calendar`, `/print-formats`, `/alert-rules`, `/approval-matrix`, `/sod-matrix`. Mutations on sensitive masters create an approval request. `GET /{master}/{id}/versions`.
- `GET|POST /sod-matrix`. A row is `{ id, kind: role_pair|action_pair, left_code, right_code, state_version, status: active|pending_approval, approval_request_id? }`. `role_pair` lists two roles one person must not hold. `action_pair` lists two actions one person must not both perform on a document: enter and approve; receive and count; create vendor and pay vendor; request correction and approve correction. `POST` body is `{ kind, left_code, right_code }` with `If-Match: <state_version>` (`0` on create) and `Idempotency-Key`. The stored status is `pending_approval` until the approval engine decides. Assigning a pair that an `active` rule forbids returns `403 SOD_VIOLATION` unless the caller cites an override approval; the override and the refusal are audited (A14).
- `GET|POST /approval-matrix`. A row is `{ id, document_type, threshold: Money, below_threshold_role, first_approver_role, final_gate_role, voting_any?, voting_of?, state_version, status, approval_request_id? }`. Below the threshold any one holder of `below_threshold_role` may register; at or above it, `first_approver_role` then `final_gate_role` (R2.2). `voting_any` and `voting_of` are the optional "any N of M" tier. `POST` uses the same idempotency and `If-Match` rules as `/sod-matrix` and stays `pending_approval` until decided.
- `POST /imports` multipart with `type`, returns `{ import_id, preview[], rejected[] }`; `POST /imports/{id}/commit`.

### Analytics

- `GET /analytics/snapshot?as_of=` returns the latest snapshot at or before the date: `{ version, as_of_date, computed_at, hash, sections: { needs_you, cash, profit, receivables, payables, sales, forecast, stock, exceptions, integrity } }`. Each section: `{ as_of, figures{}, series{ labels[], ... }, links{}, hash }`.
- `GET /analytics/snapshot/{version}/pack?format=pdf` returns the stored management pack.
- `GET /analytics/ratios?period=`.
- `GET /reports/{name}?params` for the report list in R14.4; `POST /saved-views`, `POST /saved-views/{id}/schedule`.
- `GET /exports/{kind}?from=&to=` for csv, xlsx, tally-xml, faf, vat-return, auditor-bundle; long-running exports return `{ job_id }` and a `GET /jobs/{id}`.

### Journeys

- `GET /journeys?persona=` grouped definitions.
- `POST /journeys/{slug}/instances` returns `{ instance_id, step }`.
- `POST /journeys/instances/{id}/step` body `{ step_id, input }` returns `{ ok, code?, message?, data?, problems[], next_step? }`.
- `GET /journeys/instances/{id}` resumable state.

### Devices and notifications

- `POST /devices/push-token` body `{ token, platform, app_version }` idempotent, bound to session user and device.
- `DELETE /devices/push-token` current device.
- `GET /notifications?group=needs_me|waiting|fyi&cursor=`; `GET|PUT /me/notification-preferences`.
- `GET /ws` WebSocket; server pushes `{ event_id, type, payload, at }` for the authenticated user's subscriptions; client acks with `{ ack: event_id }`.

### Admin and operations

- `GET /health` (readiness), `GET /status` (last backup, chain verification, bank feed, queue depth, clock drift; System Manager only).
- `POST /audit/verify` (Auditor, System Manager) returns `{ intact, count, first_break?, anchored_head_matches }`.
- `GET /audit/events?ref=&from=&to=&type=`.
- `GET /exceptions?month=`.
- `POST /periods/{id}/soft-close`, `POST /periods/{id}/hard-close` (approval), `POST /periods/{year}/audit-adjustment/open`.
- `POST /go-live/...` steps per R18.

## Event payload (outbox, socket, push data)

```json
{
  "event_id": "01J...",
  "type": "approval.requested",
  "severity": "HIGH",
  "company_id": "...",
  "occurred_at": "2026-09-25T10:00:00Z",
  "actor": { "id": "...", "name": "..." },
  "subject": { "doc_type": "sales_invoice", "doc_id": "...", "doc_number": "INV-2026-000311", "party": "Al Noor Trading LLC" },
  "amount": { "amount": "14350.00", "currency": "AED" },
  "deep_link": "smarterp://approval/01J...",
  "allowed_actions": ["approve", "reject", "open"],
  "state_version": 3,
  "context": { }
}
```

Push messages are data-only and carry exactly this object flattened to string values with `context` JSON-encoded. Severity in {LOW, MEDIUM, HIGH, CRITICAL}; unknown severity is treated as HIGH by clients.

Event types (initial): `approval.requested|decided|delegated|snoozed`, `document.registered`, `delivery.confirmed`, `clock.expired`, `receipt.posted`, `pdc.bounced`, `stock.received`, `stock.count.approved`, `production.posted`, `correction.posted`, `payment.released`, `chain.verified|broken`, `backup.completed|failed`, `bank.feed.completed|failed`, `forecast.below_floor`, `exception.raised`, `config.changed`, `break_glass.used`.

## Deep link scheme

`smarterp://{route}/{id}` with routes: `approval`, `document/{doc_type}`, `brief/{section}`, `customer`, `vendor`, `sku`, `trip`, `notification`. Universal Links and App Links map `https://app.<domain>/l/...` to the same routes. An unauthenticated tap stashes the link and resumes it after login.

## Change control

1. Propose the change in this file with a version bump and rationale.
2. Regenerate OpenAPI and client types; CI fails if they drift.
3. Notify dependent teams named in `07-tracks-and-tasks.md`.
4. Breaking changes keep the previous version served for one mobile release cycle; the API enforces a minimum app version with a grace period.
