# OS update window and rollback

Two kinds of updates, two procedures. Security updates to Ubuntu are automatic; application releases and Docker Engine are manual and happen only in the window.

Window: Sunday 01:30 to 04:00 (apt at 01:30, reboot at 02:30 if required, drill at 03:00). The nightly backup at 02:00 must not overlap a reboot: `smart-erp-backup.service` touches `/run/smart-erp/backup.running`; if it is present at 02:30 unattended-upgrades still reboots, so on Sundays the backup timer is expected to finish in under 30 minutes. If backups grow beyond that, move `Automatic-Reboot-Time` to 03:30 and the drill to 04:00 in `bootstrap.sh` and re-run it.

## Automatic security updates

1. Configured by `bootstrap.sh`: security origins only, Docker packages blacklisted, reboot at 02:30 when `/var/run/reboot-required` exists.
2. Monday check: `journalctl -u unattended-upgrades --since "2 days ago"` and `docker compose --profile prod ps` all healthy. The status page shows the last reboot time.
3. Rollback of a bad kernel: hold Shift at boot, pick the previous kernel in GRUB, then `apt-mark hold linux-image-*` until the fix lands.

## Application release (new image digests)

1. Prerequisites: CI green on the tag, last night's backup verified (status page), last anchor fresh (`deploy/scripts/verify-chain.sh`).
2. `just backup-now` first. Always. Then note the current digests: `docker compose --profile prod images > /var/lib/smart-erp/reports/release-<date>-before.txt`.
3. Edit `/opt/smart-erp/deploy/compose/docker-compose.yml` (or the env `ERP_IMAGE_TAG`) to the new digests; `docker compose --profile prod pull`.
4. `systemctl reload smart-erp.service` runs `up -d --wait`; `migrate` runs expand-contract migrations as `erp_migrator` before the new `api` starts.
5. Prove: status page healthy, Accountant opens the Day Book, one PDF renders, `just anchor-now` succeeds.
6. Rollback: restore the previous digests from the `before.txt` file and `systemctl reload smart-erp.service`. Migrations are expand-contract, so the previous binary runs against the new schema; the contract step ships only in the release after next.

## Docker Engine

1. Only inside the window and after `just backup-now`. `apt-get install docker-ce docker-ce-cli containerd.io docker-compose-plugin`.
2. `live-restore` is on, so containers keep running through the daemon restart; still run `docker compose --profile prod ps` afterwards.
3. Rollback: `apt-get install docker-ce=<previous>` from `apt-cache madison docker-ce`.
