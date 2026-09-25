#!/usr/bin/env bash
# smart-erp restore and restore drill.
#
#   restore.sh --drill   --target drive|offsite|auto [--backup <id>]
#   restore.sh [--restore] --target drive|offsite    --backup <id> --approval <token>
#
# Without --drill the script is a production restore (what `just restore` runs).
#
# --drill restores into the isolated compose project `smart-erp-drill`
# (PostgreSQL on 5433, data under /var/lib/smart-erp-drill, MinIO prefix
# drill/), verifies the chain head against the anchor, records production row
# counts before and after (I29), and writes a signed drill report. It never
# touches production data. `just drill --target drive|offsite` calls this.
#
# --restore is a PRODUCTION restore. It refuses without a Stakeholder approval
# token and stops the stack first. `just restore --target <t> --backup <id>
# --approval <token>` calls this. Read deploy/nuc/restore-runbook.md first.
#
# --target auto (drill only): drive if an allow-listed drive is mounted,
# otherwise offsite; alternates week to week when both are available.

SCRIPT_NAME=restore.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

MODE=restore
TARGET=""
BACKUP_ID="latest"
APPROVAL=""
DRILL_PROJECT="${ERP_DRILL_PROJECT:-smart-erp-drill}"
DRILL_DATA="${ERP_DRILL_DATA:-/var/lib/smart-erp-drill}"

usage() { sed -n '2,22p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit "${1:-0}"; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --drill) MODE=drill; shift ;;
    --restore) MODE=restore; shift ;;
    --target) TARGET="${2:?}"; shift 2 ;;
    --target=*) TARGET="${1#*=}"; shift ;;
    --backup) BACKUP_ID="${2-}"; shift 2 ;;
    --backup=*) BACKUP_ID="${1#*=}"; shift ;;
    --approval) APPROVAL="${2-}"; shift 2 ;;
    --approval=*) APPROVAL="${1#*=}"; shift ;;
    -h|--help) usage 0 ;;
    *) die "unknown argument: $1 (see --help)" ;;
  esac
done

[[ -n "${TARGET}" ]] || die "--target is required (drive|offsite|auto)"
require_cmd docker

mounted_drive() {
  local l
  for l in $(allowlist_labels); do
    if mountpoint -q "${DRIVE_MOUNT_ROOT}/${l}" 2>/dev/null; then echo "${l}"; return 0; fi
  done
  return 1
}

resolve_target() {
  case "${TARGET}" in
    drive|offsite) ;;
    auto)
      [[ "${MODE}" == "drill" ]] || die "--target auto is only valid with --drill"
      if drive="$(mounted_drive)"; then
        # Alternate: even ISO week -> drive, odd -> offsite, so both paths get exercised.
        if (( $(date +%V) % 2 == 0 )); then TARGET=drive; else TARGET=offsite; fi
        log "drive ${drive} present; week $(date +%V) selects target=${TARGET}"
      else
        TARGET=offsite
        log "no drive mounted; target=offsite"
      fi ;;
    *) die "--target must be drive, offsite, or auto" ;;
  esac
  if [[ "${TARGET}" == "drive" ]]; then
    DRIVE_LABEL="$(mounted_drive)" || die "no allow-listed drive mounted under ${DRIVE_MOUNT_ROOT}"
  fi
}

resolve_target
RUN_ID="$(timestamp_id)"
mkdir -p "${REPORT_DIR}" 2>/dev/null || true

if [[ "${MODE}" == "drill" ]]; then
  log "drill ${RUN_ID}: target=${TARGET} backup=${BACKUP_ID} project=${DRILL_PROJECT}"
  todo "erp_run backup resolve --target ${TARGET} --backup ${BACKUP_ID}  (-> concrete backup id + manifest)"
  todo "erp_run status snapshot --out /reports/drill-${RUN_ID}-before.json   (production row counts, I29)"
  todo "docker compose -p ${DRILL_PROJECT} -f drill.compose.yml up -d postgres minio  (5433, ${DRILL_DATA}, prefix drill/)"
  todo "erp_run restore run --into ${DRILL_PROJECT} --target ${TARGET} --backup ${BACKUP_ID}  (pg restore + WAL replay + objects)"
  todo "erp_run chain verify --database drill --against anchor  (heads must match, I17/I34)"
  todo "erp_run status snapshot --out /reports/drill-${RUN_ID}-after.json && diff before/after == 0 rows changed"
  todo "erp_run drill report --id ${RUN_ID} --sign --out /reports/drill-${RUN_ID}.json  (stored off-site and in the chain)"
  todo "docker compose -p ${DRILL_PROJECT} down -v"
  log "drill ${RUN_ID} finished (skeleton)"
  exit 0
fi

# ---- production restore --------------------------------------------------
[[ -n "${BACKUP_ID}" && "${BACKUP_ID}" != "latest" ]] || die "production restore needs an explicit --backup <id>"
[[ -n "${APPROVAL}" ]] || die "REFUSED: production restore requires --approval <Stakeholder token>"

log "PRODUCTION RESTORE ${RUN_ID}: target=${TARGET} backup=${BACKUP_ID}"
todo "erp_run approvals verify --kind restore --token '${APPROVAL}' --backup ${BACKUP_ID}  (exit non-zero => abort)"
log "stopping api/worker/scheduler so no writes land during restore"
todo "compose --profile prod stop api worker scheduler"
todo "erp_run status snapshot --out /reports/restore-${RUN_ID}-before.json"
todo "erp_run restore run --into production --target ${TARGET} --backup ${BACKUP_ID} --approval-token '${APPROVAL}'"
todo "erp_run chain verify --against anchor   (must match before the stack is reopened)"
todo "compose --profile prod up -d --wait"
todo "erp_run restore report --id ${RUN_ID} --sign --out /reports/restore-${RUN_ID}.json"
log "production restore ${RUN_ID} finished (skeleton)"
