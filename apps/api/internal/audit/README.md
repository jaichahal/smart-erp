# audit

Anchoring, chain verification, and backup/restore (P1.6, P1.15).

The hourly anchor is `{company_id, chain_seq, head_hash, row_count, at}`, signed
with the off-prem key (`Sign` is denied to the database operator) and written to
the on-prem bucket and the off-site compliance-mode bucket. In dev those are
MinIO `erp-anchors` and `offsite-sim`, and the key is a local ed25519 simulation.

`deploy/scripts/anchor.sh`, `backup.sh`, `restore.sh`, and `verify-chain.sh` call
`auditctl`. Drive copies and isolated restore drills stay with later tasks
(P1.17, P1.19). The manifest schema is `smart-erp.backup.manifest/v1` so those
tasks can reuse it.

Status fields `last_backup`, `last_chain_verification`, and
`recovery_point_in_force` are produced by `Service.Status`. The HTTP `/status`
route is owned by `kit/httpx` and does not call this yet.
