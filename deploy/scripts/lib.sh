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

# Run a one-off command inside the api image. Prefer auditctl for anchor,
# backup, restore, and chain verification; it runs the Go package directly.
erp_run() {
  compose run --rm --no-deps -T api "$@"
}

# Run a Go subcommand from the audit module. Usage: auditctl <args...>
# The scripts only orchestrate; the audit package owns the behaviour.
auditctl() {
  local api="${ERP_API_DIR:-$(cd "${DEPLOY_DIR}/../apps/api" && pwd)}"
  local envfile="${ERP_HOST_ENV:-${COMPOSE_DIR}/.env.dev.host}"
  (
    if [[ -f "${envfile}" ]]; then
      set -a
      # shellcheck disable=SC1090
      source "${envfile}"
      set +a
    fi
    cd "${api}"
    go run ./internal/audit/cmd/auditctl "$@"
  )
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
