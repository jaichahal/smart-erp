#!/usr/bin/env bash
# smart-erp audit-chain anchor (ADR-06). Signs the current chain head with the
# off-prem KMS / Vault Transit key and writes the anchor to the compliance-mode
# off-site bucket (dev: offsite-sim). Hourly via smart-erp-anchor.timer;
# `just anchor-now` calls it by hand.
#
#   anchor.sh [--target offsite|local|all] [--dry-run]

SCRIPT_NAME=anchor.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

TARGET=offsite
DRY_RUN=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --target) TARGET="${2:?}"; shift 2 ;;
    --target=*) TARGET="${1#*=}"; shift ;;
    --dry-run) DRY_RUN=1; shift ;;
    -h|--help) sed -n '2,8p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done

case "${TARGET}" in offsite|local|all) ;; *) die "--target must be offsite, local, or all" ;; esac
require_cmd docker

ANCHOR_ID="$(timestamp_id)"
log "anchor ${ANCHOR_ID}: target=${TARGET} dry-run=${DRY_RUN}"

# The Go worker owns: read chain head under a snapshot, canonicalise, sign with
# KMS, PUT with object lock, record the anchor row, email the daily digest.
todo "erp_run anchor write --id ${ANCHOR_ID} --target ${TARGET}"
# [[ "${DRY_RUN}" -eq 1 ]] || erp_run anchor write --id "${ANCHOR_ID}" --target "${TARGET}"

todo "erp_run anchor verify --id ${ANCHOR_ID}   (re-read the object, check signature, compare head)"
todo "on failure: erp_run status set anchor=failed and alert; see deploy/nuc/anchor-failure.md"
log "anchor ${ANCHOR_ID} finished (skeleton)"
