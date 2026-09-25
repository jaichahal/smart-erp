# Spare NUC swap

When the production NUC fails (will not boot, NVMe dead, repeated kernel panics) the spare on the shelf becomes production. Target: back in service within one business day (ADR-16). If there is no spare, this page starts at step 3 after procurement, which is why the lead time is written on the shelf label.

Keep the spare useful: it runs `bootstrap.sh` once a quarter, holds current images (`docker compose --profile prod pull`), and is the machine the monthly off-site-only drill runs on, so its readiness is proven rather than assumed.

## 1. Decide and announce

1. Confirm the failure is hardware, not power or a full disk (`ups.md`, `disk-full.md`). A NUC that boots but is slow is not a swap.
2. Tell the Accountant and Stakeholders: the ERP is down, phones will show offline, approvals queue on the devices.
3. Pull the `ERPBAK-*` drive from the dead NUC. Do not plug it into anything yet.

## 2. Salvage what you can

1. If the NVMe is readable in the spare (USB enclosure), copy `/etc/smart-erp/` (secrets, allow-list, operator key) and `/var/lib/smart-erp/reports/`. Nothing else on the disk is needed; the data comes from backups.
2. If the NVMe is dead, the operator key comes from the operator's hardware token and the management envelope; the allow-list is rebuilt from `backup-drives.yaml.example` and `cryptsetup luksUUID` on each drive.

## 3. Bring the spare up

1. `sudo /opt/smart-erp/deploy/nuc/bootstrap.sh` (idempotent) and `tailscale up`. Move the Tailscale machine name and any DNS or tunnel record to the spare.
2. Restore `/etc/smart-erp/env.age`, `operator.key`, `backup-drives.yaml`, and the drive key files. `chmod 0400` the keys.
3. Plug in the drive; `just drive-check` must show allow-listed yes and mounted yes.
4. Restore per `restore-runbook.md`: `just restore --target drive --backup <latest id> --approval <token>`, then verify against the off-site anchor. If the drive is older than the last off-site backup, use `--target offsite` instead; the anchor decides.

## 4. Reopen

1. `systemctl start smart-erp.service`; status page green; a phone logs in through the tunnel; `just anchor-now` writes a new anchor.
2. `just backup-now` so the spare has its own first backup tonight regardless of the timer.
3. Order a replacement spare the same day. Update the shelf label, the asset register in the console, and the quarterly rehearsal record with the measured swap time.
