# Disk-full response

Symptoms: status page shows disk red, PostgreSQL logs `No space left on device`, MinIO returns 507, `smart-erp-backup.service` failed with ENOSPC, or the console refuses uploads.

The NVMe holds PostgreSQL, MinIO, Docker images and logs. Backup staging is on the second disk (`/var/lib/smart-erp/staging`); if that fills, only backups stop, the product keeps running.

## Triage (5 minutes)

1. `df -h / /var/lib/docker /var/lib/smart-erp /var/lib/smart-erp-drill` to see which filesystem is full.
2. `docker system df` and `du -xsh /var/lib/docker/containers/*/*-json.log | sort -h | tail` for image and log bloat.
3. `docker compose exec postgres psql -U postgres -c "select pg_size_pretty(pg_database_size('erp'))"` and `docker compose exec minio mc du erp/erp-files` for the data.
4. `ls -la /var/lib/smart-erp-drill` in case a drill left its data directory behind.

## Reclaim, safest first

1. Drill leftovers: `docker compose -p smart-erp-drill down -v && rm -rf /var/lib/smart-erp-drill/*` (drill data is disposable by design).
2. Docker: `docker image prune -af --filter "until=168h"` and `docker builder prune -af`. Container logs are capped at 5x20 MB by compose.prod.yml; if a stray container is not capped, `truncate -s 0` its log.
3. Staging: completed backups already verified off-site can go: `find /var/lib/smart-erp/staging -maxdepth 1 -mtime +2 -exec rm -rf {} +`.
4. Never delete PostgreSQL WAL by hand. If `pg_wal` is what grew, WAL archiving to the off-site bucket has stalled: check `smart-erp-anchor.service` and off-site credentials first, then `docker compose exec postgres psql -U postgres -c "select * from pg_stat_archiver"`.
5. Never delete from `erp-anchors` or the off-site bucket; compliance-mode object lock would refuse anyway.

## After

1. Confirm services healthy and run `just backup-now`; a failed nightly must be replaced the same day.
2. If usage is above 70 percent after cleanup, order storage now (spare NUC sizing in spec 10) and set the status page threshold alert to 80 percent.
3. Record the incident in the console so it appears in the quarterly rehearsal review.
