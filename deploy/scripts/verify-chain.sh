#!/usr/bin/env bash
# smart-erp hash-chain verifier. Walks the immutable tables, recomputes the
# chain, and compares the head with the latest anchor in the off-site bucket
# (dev: offsite-sim). The Nightly agent runs this; Stakeholders receive the
# monthly report produced from it.
#
#   verify-chain.sh [--since <anchor-id|date>] [--database erp|drill] [--against offsite|local] [--report <path>]

SCRIPT_NAME=verify-chain.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

SINCE=""
DATABASE=erp
AGAINST=offsite
REPORT=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --since) SINCE="${2:?}"; shift 2 ;;
    --since=*) SINCE="${1#*=}"; shift ;;
    --database) DATABASE="${2:?}"; shift 2 ;;
    --database=*) DATABASE="${1#*=}"; shift ;;
    --against) AGAINST="${2:?}"; shift 2 ;;
    --against=*) AGAINST="${1#*=}"; shift ;;
    --report) REPORT="${2:?}"; shift 2 ;;
    --report=*) REPORT="${1#*=}"; shift ;;
    -h|--help) sed -n '2,8p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done

case "${DATABASE}" in erp|drill) ;; *) die "--database must be erp or drill" ;; esac
case "${AGAINST}" in offsite|local) ;; *) die "--against must be offsite or local" ;; esac
require_cmd docker

RUN_ID="$(timestamp_id)"
REPORT="${REPORT:-${REPORT_DIR}/chain-${RUN_ID}.json}"
log "verify ${RUN_ID}: database=${DATABASE} against=${AGAINST} since=${SINCE:-genesis}"

todo "erp_run chain verify --database ${DATABASE} --against ${AGAINST} ${SINCE:+--since ${SINCE}} --report /reports/$(basename "${REPORT}")"
# Exit codes the Go verifier must use, so systemd and the Nightly agent can act:
#   0 chain intact and head == anchor
#   2 chain intact but no anchor newer than the grace window (anchoring stalled)
#   3 head mismatch or broken link  -> page the System Manager, freeze corrections
todo "map exit code: 2 -> warn + status anchor=stale, 3 -> alert + deploy/nuc/anchor-failure.md"
log "verify ${RUN_ID} finished (skeleton); report ${REPORT}"
