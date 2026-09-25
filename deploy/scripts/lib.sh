# shellcheck shell=bash
# Shared helpers for deploy/scripts/*.sh. Source, do not execute.
#
#   source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

set -euo pipefail

SCRIPTS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(cd "${SCRIPTS_DIR}/.." && pwd)"
COMPOSE_DIR="${ERP_COMPOSE_DIR:-${DEPLOY_DIR}/compose}"
NUC_CONFIG_DIR="${ERP_CONFIG_DIR:-/etc/smart-erp}"
DRIVE_ALLOWLIST="${ERP_DRIVE_ALLOWLIST:-${NUC_CONFIG_DIR}/backup-drives.yaml}"
DRIVE_MOUNT_ROOT="${ERP_DRIVE_MOUNT_ROOT:-/mnt/erpbak}"
STAGING_DIR="${ERP_BACKUP_STAGING:-/var/lib/smart-erp/staging}"
REPORT_DIR="${ERP_REPORT_DIR:-/var/lib/smart-erp/reports}"

OS="$(uname -s)"

log()  { printf '%s [%s] %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "${SCRIPT_NAME:-$(basename "$0")}" "$*" >&2; }
die()  { log "ERROR: $*"; exit 1; }
warn() { log "WARN: $*"; }
todo() { log "TODO(go): $*"; }

require_cmd() {
  local c
  for c in "$@"; do
    command -v "$c" >/dev/null 2>&1 || die "required command not found: $c"
  done
}

is_linux() { [[ "${OS}" == "Linux" ]]; }
is_macos() { [[ "${OS}" == "Darwin" ]]; }

# Run docker compose in the deploy/compose directory with the project's env.
compose() {
  (cd "${COMPOSE_DIR}" && docker compose "$@")
}

# Run a one-off Go binary command inside the api image, sharing the stack's
# network and env. Usage: erp_run <subcommand and args...>
#   e.g. erp_run backup create --target offsite
# The Go side owns the actual work; these scripts only orchestrate.
erp_run() {
  compose run --rm --no-deps -T api "$@"
}

# Read a scalar from the drive allow-list without a YAML parser:
#   drives:
#     - label: ERPBAK-A
#       luks_uuid: ...
#       key_file: ...
# allowlist_field <label> <field>
allowlist_field() {
  local label="$1" field="$2"
  [[ -r "${DRIVE_ALLOWLIST}" ]] || return 1
  awk -v label="${label}" -v field="${field}" '
    /^[[:space:]]*-[[:space:]]*label:/ { inblock = ($3 == label) ; next }
    inblock && $1 == field":" { sub(/^[[:space:]]*[a-z_]+:[[:space:]]*/, ""); gsub(/["'\'']/, ""); print; exit }
  ' "${DRIVE_ALLOWLIST}"
}

allowlist_labels() {
  [[ -r "${DRIVE_ALLOWLIST}" ]] || return 0
  awk '/^[[:space:]]*-[[:space:]]*label:/ { print $3 }' "${DRIVE_ALLOWLIST}"
}

timestamp_id() { date -u +%Y%m%dT%H%M%SZ; }
