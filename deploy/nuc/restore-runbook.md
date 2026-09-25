# Restore runbook: production restore onto this or a replacement NUC

Who: System Manager. Needs: a Stakeholder approval token, the operator key, and either an `ERPBAK-*` drive or off-site bucket credentials. Expected duration: 1 to 3 hours for a 100 GB database; the weekly drill report (`/var/lib/smart-erp/reports/drill-*.json`) gives the real number.

Before you start, decide which target: the drive is faster and works without internet; the off-site bucket is the authority when the two disagree (it holds the anchors).

## 1. Freeze

1. Announce the outage in the console banner and to the Accountant.
2. `systemctl stop smart-erp-backup.timer smart-erp-anchor.timer smart-erp-drill.timer` so nothing writes while you work.
3. `cd /opt/smart-erp/deploy/compose && docker compose --profile prod stop api worker scheduler` (keep postgres and minio up for forensics until step 3).
4. Record the last anchor: `./../scripts/verify-chain.sh --against offsite --report /var/lib/smart-erp/reports/pre-restore.json`. Keep this file; the post-restore head must match it or be an ancestor of it.

## 2. Get an approval

1. A Stakeholder opens Admin, Operations, Approve restore, picks the backup id, and receives a token valid for 2 hours.
2. Without the token `just restore` refuses. There is no bypass; if no Stakeholder is reachable, wait.

## 3. Restore

1. Fresh NUC only: run `deploy/nuc/bootstrap.sh`, unlock the secrets (`/etc/smart-erp/env.age`), `docker compose --profile prod pull`. Do not start the stack.
2. Pick the backup: `just drill --target offsite` lists ids; `ls /mnt/erpbak/ERPBAK-*/backups/` for the drive.
3. Run: `just restore --target drive --backup 20260924T020000Z --approval <token>` (or `--target offsite`). The script stops the app containers, restores PostgreSQL (base backup plus WAL to the latest archived segment), restores objects, then verifies the chain head against the latest anchor before it reopens anything.
4. If the verifier exits 3 (head mismatch), stop. Do not reopen. Call the Stakeholder and follow `anchor-failure.md`.

## 4. Reopen and prove

1. `systemctl start smart-erp.service`; every service healthy in `docker compose ps` and on the status page.
2. Accountant checks: trial balance equals the last close, last three journals visible, one PDF renders, one push arrives on a phone.
3. `just anchor-now` and confirm a new anchor appears off-site.
4. `systemctl start smart-erp-backup.timer smart-erp-anchor.timer smart-erp-drill.timer`; run `just backup-now` immediately so the first post-restore backup exists tonight regardless.
5. File the signed restore report (`/var/lib/smart-erp/reports/restore-*.json`) with the Stakeholders and record the incident in the console.
