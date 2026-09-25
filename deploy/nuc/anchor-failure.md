# Off-site anchor failure

The hourly anchor (`smart-erp-anchor.timer`) signs the audit-chain head with the off-prem KMS key and writes it to the compliance-mode off-site bucket (ADR-06). Two very different failures share this page: the anchor cannot be written (an operational problem) and the anchor does not match the chain (an integrity problem). Decide which one you have first.

## Cannot write: exit 2 from `verify-chain.sh`, status page "anchor stale"

1. `journalctl -u smart-erp-anchor.service --since "3 hours ago"` and `docker compose --profile prod logs --since 3h worker | grep anchor`.
2. Off-site reachability: `curl -sI $ERP_OFFSITE_S3_ENDPOINT` from the NUC; if it fails, it is the internet link or the provider; the chain keeps growing locally and the next successful anchor covers the gap, so nothing is lost, only the tamper-evidence window widens.
3. Credentials: `403` means the off-site access key was rotated or the bucket policy changed. Update `/etc/smart-erp/env.age` (see `bootstrap.sh` next steps) and `systemctl reload smart-erp.service`.
4. KMS or Vault Transit: `Sign` denied means the key policy or the token expired; the anchor job must never fall back to a local key. Fix the policy, then `just anchor-now`.
5. Object lock: `The bucket is missing Object Lock Configuration` means someone recreated the bucket without lock. Stop. This is a change to the audit posture and needs the Stakeholders; recreate with lock and compliance retention and record it.
6. Once `just anchor-now` succeeds, run `deploy/scripts/verify-chain.sh --against offsite` and confirm exit 0. If the gap exceeded 24 hours, note it in the monthly chain report.

## Does not match: exit 3, status page "chain mismatch"

1. Do not restart anything, do not run corrections, do not run `just restore`. Freeze the period in the console (Admin, Periods, Freeze) so no postings land.
2. Save evidence: `deploy/scripts/verify-chain.sh --against offsite --report /var/lib/smart-erp/reports/mismatch-$(date +%F).json` and copy the report to the drive and to a laptop.
3. Determine the first bad link: the report lists the last anchored head that verifies and the first row whose hash breaks. Compare with the last drill report and the last backup manifest.
4. Call the Stakeholders and the Accountant. Possible causes in order of likelihood: a restore was done from a backup older than the last anchor (check `restore-*.json` reports), a migration touched an immutable table without the `immutable-change` process, database corruption (check `dmesg` and `pg_stat_database` checksum failures), deliberate tampering.
5. Recovery is a decision, not a script: either restore to the last anchored state (`restore-runbook.md`, the anchor decides the target) and re-enter what was lost from documents, or accept the current state and re-anchor with a signed Stakeholder resolution recorded in the chain.
6. Whatever is decided, the next anchor must verify with exit 0 and the incident report goes to the Stakeholders with the anchor hash.
