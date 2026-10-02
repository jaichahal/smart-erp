Roles and permissions are rows, not code. This package decides what a caller may
see and do:

- Row policies in `apps/api/migrations/10013_authz.sql` use the session variables
  `kit/rls` sets (`erp.company_id`, `erp.user_id`, `erp.roles`) and, when set,
  `erp.territory_id`, `erp.owner_id`, `erp.warehouse_id`.
- `Service.Redact` strips cost and margin fields the caller's permissions do not
  allow, including nested objects and exports.
- An unknown token role resolves to the `least_privileged` persona.
- `Service.AssignRoles` refuses an active SoD pair unless `OverrideApprover`
  grants it, and writes the refusal before returning the error.
- Users are disabled with `Service.DisableUser`. There is no delete path. A bearer
  access token for a disabled user is refused; the directory row stays.
- `Service.RunScheduledAccessReviewForCompany` is what `cmd/scheduler` calls
  once per company each quarter. HTTP callers with the system role can run the
  same job for their company.

Identity (P1.2) calls `LoginGate` before minting a token. Approvals (P1.7)
implement `OverrideApprover` and `MatrixApprover`. Both seams are filed as issues
because those modules cannot be imported here.
