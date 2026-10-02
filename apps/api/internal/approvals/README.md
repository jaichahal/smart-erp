# approvals

Approval engine for task P1.7 (requirements R2.1 to R2.11, acceptance tests D1 to D14).

Requests and decisions are immutable version rows. A transition takes
`pg_advisory_xact_lock` on the request id (the application role cannot
`SELECT FOR UPDATE` an immutable table), then inserts the next `state_version`.

The posting service consumes a single-use five-minute token through
`PostingGate`. Step-up uses `StepUpVerifier`. Until identity ships that
verifier, `StubStepUp` accepts the token `stepup.<action>.<userID>`.

Delegate and snooze are service operations. Their HTTP paths wait for a
contracts-only pull request. Wire the routes with `approvals.Mount` under
`/api/v1` in `cmd/api` (that file is outside this task).
