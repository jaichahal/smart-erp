# UPS power-loss response

Setup: the NUC is on a UPS with USB signalling, managed by `nut` (or `apcupsd`). Runtime at full load is measured, not assumed: the number lives on the status page and in the quarterly rehearsal record. Shutdown is automatic at 20 percent battery or 5 minutes remaining, whichever comes first.

## Install (once, Track I)

1. `apt-get install nut` and configure `/etc/nut/ups.conf` with the `usbhid-ups` driver; `upsc <name>` must show `battery.charge` and `ups.status`.
2. In `/etc/nut/upsmon.conf`: `SHUTDOWNCMD "/sbin/shutdown -h +0"`, `FINALDELAY 5`, and a `NOTIFYCMD` that calls `docker compose --profile prod exec -T api /app status set ups=<state>` so the status page shows it.
3. `nut` reports `OB` (on battery) and `LB` (low battery); the ERP status page turns amber on `OB` and red on `LB`.
4. Test with the UPS self-test button, then a real unplug for 60 seconds, then a full drain in the quarterly rehearsal.

## During a power loss

1. Nothing to do for the first minutes. PostgreSQL and MinIO are safe on clean shutdown; the stack has `stop_grace_period` set so in-flight jobs finish.
2. If mains is not back at 50 percent battery, stop the write load early: `systemctl stop smart-erp.service`. This gives the database its clean shutdown while there is plenty of battery.
3. `nut` will halt the NUC at the threshold. The UPS then turns off its outlets and back on when mains returns, which powers the NUC back on if the BIOS is set to "power on after loss" (checked in bootstrap notes).

## After power returns

1. `systemctl status smart-erp.service docker.service`; `docker compose --profile prod ps` must show every service healthy. If postgres is in recovery, wait; do not restart it.
2. Check the last anchor and WAL archive: `deploy/scripts/verify-chain.sh --against offsite`. Exit 2 means anchoring is behind; run `just anchor-now`.
3. If the outage crossed 02:00, the backup timer has `Persistent=true` and runs on boot; confirm with `systemctl list-timers 'smart-erp-*'` and the status page's off-site and drive results.
4. If the drive was connected during the outage, run `just drive-check` and a re-read verify (`deploy/scripts/backup.sh --target drive`) before trusting it.
5. Record the event: duration, battery minimum, whether the shutdown was clean. Replace the battery when measured runtime falls below 15 minutes.
