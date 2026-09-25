#!/usr/bin/env bash
# smart-erp external backup drive check (`just drive-check`).
# Prints, per drive labelled ERPBAK-*: label, LUKS UUID, encryption state,
# unlock/mount state, free space, and whether it is on the allow-list
# (/etc/smart-erp/backup-drives.yaml).
#
#   drive-check.sh [--allowlist <path>] [--json]
#
# Works on Linux (lsblk/blkid). On macOS it explains the dev equivalent
# (`just dev-drive-create` image-backed target) and exits 0.

SCRIPT_NAME=drive-check.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

JSON=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --allowlist) DRIVE_ALLOWLIST="${2:?}"; shift 2 ;;
    --allowlist=*) DRIVE_ALLOWLIST="${1#*=}"; shift ;;
    --json) JSON=1; shift ;;
    -h|--help) sed -n '2,10p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done

if is_macos; then
  cat >&2 <<'EOF'
drive-check: macOS has no LUKS and no udev, so the NUC drive path cannot run here.

  Dev equivalent (spec 10, "Mac dev equivalent"):
    just dev-drive-create              # 20 GB sparse, age-encrypted image at ~/.smart-erp/erpbak-dev.img
    just dev-drive-adopt /Volumes/<n>  # or adopt a real USB stick
    just drill --target drive          # exercises the same allow-list and re-read verification paths

  Dev drive image: ~/.smart-erp/erpbak-dev.img
EOF
  if [[ -f "${HOME}/.smart-erp/erpbak-dev.img" ]]; then
    echo "  status: present ($(du -h "${HOME}/.smart-erp/erpbak-dev.img" | cut -f1) allocated)" >&2
  else
    echo "  status: not created yet" >&2
  fi
  exit 0
fi

is_linux || die "unsupported OS: ${OS}"
require_cmd lsblk blkid

if [[ ! -r "${DRIVE_ALLOWLIST}" ]]; then
  warn "allow-list ${DRIVE_ALLOWLIST} not found or unreadable (run as root or set ERP_DRIVE_ALLOWLIST); every drive will show allowlisted=no"
fi

# blkid -o export needs root to read superblocks of unmounted devices.
if [[ ${EUID} -ne 0 ]]; then warn "not root: some fields may be empty; re-run with sudo"; fi

found=0
[[ "${JSON}" -eq 1 ]] && printf '['
first=1
while read -r dev; do
  [[ -n "${dev}" ]] || continue
  label="$(blkid -o value -s LABEL "${dev}" 2>/dev/null || true)"
  fstype="$(blkid -o value -s TYPE "${dev}" 2>/dev/null || true)"
  [[ "${label}" == ERPBAK-* ]] || continue
  found=$((found+1))
  uuid="$(blkid -o value -s UUID "${dev}" 2>/dev/null || true)"
  encrypted=no; [[ "${fstype}" == "crypto_LUKS" ]] && encrypted=yes
  mapper="/dev/mapper/$(echo "${label}" | tr '[:upper:]' '[:lower:]')"
  unlocked=no; [[ -e "${mapper}" ]] && unlocked=yes
  mnt="${DRIVE_MOUNT_ROOT}/${label}"
  mounted=no; free="-"; perms="-"
  if mountpoint -q "${mnt}" 2>/dev/null; then
    mounted=yes
    free="$(df -h --output=avail "${mnt}" | tail -1 | tr -d ' ')"
    perms="$(stat -c '%a %U' "${mnt}")"
  fi
  allow_uuid="$(allowlist_field "${label}" luks_uuid || true)"
  allowlisted=no
  if [[ -n "${allow_uuid}" && "${allow_uuid}" == "${uuid}" ]]; then allowlisted=yes
  elif [[ -n "${allow_uuid}" ]]; then allowlisted="no (uuid mismatch: allow-list has ${allow_uuid})"; fi
  size="$(lsblk -dn -o SIZE "${dev}" 2>/dev/null || true)"

  if [[ "${JSON}" -eq 1 ]]; then
    [[ ${first} -eq 1 ]] || printf ','
    first=0
    printf '{"device":"%s","label":"%s","luks_uuid":"%s","size":"%s","encrypted":"%s","unlocked":"%s","mounted":"%s","free":"%s","mount_perms":"%s","allowlisted":"%s"}' \
      "${dev}" "${label}" "${uuid}" "${size}" "${encrypted}" "${unlocked}" "${mounted}" "${free}" "${perms}" "${allowlisted}"
  else
    cat <<EOF
Drive        : ${label}   (${dev}, ${size})
LUKS UUID    : ${uuid:-unknown}
Encrypted    : ${encrypted}$( [[ "${encrypted}" == no ]] && echo "   <-- NOT LUKS: do not use for backups" )
Unlocked     : ${unlocked}   (${mapper})
Mounted      : ${mounted}   (${mnt}, perms ${perms}; expected 700 erpbackup)
Free space   : ${free}
Allow-listed : ${allowlisted}

EOF
  fi
done < <(lsblk -pnr -o NAME,TYPE 2>/dev/null | awk '$2=="part" || $2=="disk" {print $1}')
[[ "${JSON}" -eq 1 ]] && printf ']\n'

if [[ ${found} -eq 0 ]]; then
  log "no drive labelled ERPBAK-* is connected"
  [[ "${JSON}" -eq 1 ]] || exit 1
fi
exit 0
