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

require_cmd docker
BACKUP_ID="$(timestamp_id)"
mkdir -p "${STAGING_DIR}" "${REPORT_DIR}" 2>/dev/null || true
log "backup ${BACKUP_ID} starting (target=${TARGET}, dry-run=${DRY_RUN})"

run() {
  if [[ "${DRY_RUN}" -eq 1 ]]; then log "dry-run: $*"; else "$@"; fi
}

# 1. Archives to local staging + manifest.
todo "erp_run backup create --id ${BACKUP_ID} --staging /staging  (pg + objects + manifest.json, signed)"
# run erp_run backup create --id "${BACKUP_ID}" --staging /staging

# 2. Off-site upload and verify.
if [[ "${TARGET}" == "all" || "${TARGET}" == "offsite" ]]; then
  todo "erp_run backup push --id ${BACKUP_ID} --target offsite && erp_run backup verify --id ${BACKUP_ID} --target offsite"
  # run erp_run backup push   --id "${BACKUP_ID}" --target offsite
  # run erp_run backup verify --id "${BACKUP_ID}" --target offsite
fi

# 3. External drive, only if one on the allow-list is mounted.
if [[ "${TARGET}" == "all" || "${TARGET}" == "drive" ]]; then
  mounted=""
  for l in $( [[ -n "${LABEL}" ]] && echo "${LABEL}" || allowlist_labels ); do
    if mountpoint -q "${DRIVE_MOUNT_ROOT}/${l}" 2>/dev/null; then mounted="${l}"; break; fi
  done
  if [[ -z "${mounted}" ]]; then
    if [[ "${TARGET}" == "drive" ]]; then die "no allow-listed drive mounted under ${DRIVE_MOUNT_ROOT}"; fi
    warn "no backup drive mounted; skipping drive target (status page will show drive=absent)"
    todo "erp_run status set backup.drive=absent"
  else
    log "drive ${mounted} mounted at ${DRIVE_MOUNT_ROOT}/${mounted}"
    todo "erp_run backup push --id ${BACKUP_ID} --target drive --path /drive  (bind ${DRIVE_MOUNT_ROOT}/${mounted} into the run)"
    todo "erp_run backup verify --id ${BACKUP_ID} --target drive --reread"
    todo "erp_run backup prune --target drive --policy from-allowlist"
    # run compose run --rm --no-deps -T -v "${DRIVE_MOUNT_ROOT}/${mounted}:/drive" api backup push --id "${BACKUP_ID}" --target drive --path /drive
    run sync
    if [[ "${ERP_BACKUP_UNMOUNT_AFTER:-1}" == "1" ]] && is_linux; then
      run systemctl stop "erpbak@${mounted}.service" || warn "could not stop erpbak@${mounted}; unmount manually"
      log "SAFE TO REMOVE: ${mounted}"
      todo "erp_run status set backup.drive=safe-to-remove --label ${mounted}"
    fi
  fi
fi

# 4. Report.
todo "erp_run backup report --id ${BACKUP_ID} --out /reports/backup-${BACKUP_ID}.json (signed, also written to the chain)"
log "backup ${BACKUP_ID} finished"
