#!/usr/bin/env bash
# smart-erp nightly backup orchestrator (spec 10, "Nightly order on the NUC").
#
#   backup.sh --target all|offsite|drive [--label ERPBAK-A] [--dry-run]
#
# Order: database + object archives to local staging -> manifest -> upload to
# the off-site bucket and verify -> write to the external drive if present and
# re-read verify -> prune per drive policy -> sync and unmount -> emit
# safe-to-remove -> status page gets off-site and drive results separately.
#
# The heavy lifting (pg_basebackup/WAL, MinIO mirror, manifest hashing,
# signatures, verification, status update) lives in the Go binary; this script
# is the systemd-facing wrapper. `just backup-now` calls it with --target all.

SCRIPT_NAME=backup.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

TARGET=all
LABEL=""
DRY_RUN=0

usage() {
  sed -n '2,20p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
  exit "${1:-0}"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --target) TARGET="${2:?--target needs a value}"; shift 2 ;;
    --target=*) TARGET="${1#*=}"; shift ;;
    --label) LABEL="${2:?--label needs a value}"; shift 2 ;;
    --label=*) LABEL="${1#*=}"; shift ;;
    --dry-run) DRY_RUN=1; shift ;;
    -h|--help) usage 0 ;;
    *) die "unknown argument: $1 (see --help)" ;;
  esac
done

case "${TARGET}" in
  all|offsite|drive) ;;
  *) die "--target must be all, offsite, or drive" ;;
esac

require_cmd go
BACKUP_ID="$(timestamp_id)"
mkdir -p "${STAGING_DIR}" "${REPORT_DIR}" 2>/dev/null || true
log "backup ${BACKUP_ID} starting (target=${TARGET}, dry-run=${DRY_RUN})"

run() {
  if [[ "${DRY_RUN}" -eq 1 ]]; then log "dry-run: $*"; else "$@"; fi
}

# Archives, off-site copy, verification, then the manifest. Success is recorded
# only after the off-site copy verifies.
if [[ "${TARGET}" == "all" || "${TARGET}" == "offsite" ]]; then
  if [[ "${DRY_RUN}" -eq 1 ]]; then
    log "dry-run: auditctl backup run --id ${BACKUP_ID}"
  else
    run auditctl backup run --id "${BACKUP_ID}"
    run auditctl backup prune
  fi
fi

# 3. External drive (P1.17). This task does not copy to the drive; the off-site
# result above is independent and is not marked successful because the drive
# is absent.
if [[ "${TARGET}" == "drive" ]]; then
  die "drive backup is P1.17 and is not implemented here; use --target offsite or all"
fi
if [[ "${TARGET}" == "all" ]]; then
  warn "external drive copy is P1.17 and was not performed; the off-site result stands on its own"
fi

log "backup ${BACKUP_ID} finished"
