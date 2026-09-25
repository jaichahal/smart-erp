#!/usr/bin/env bash
# Called by deploy/systemd/erpbak@.service. Unlocks and mounts (or unmounts and
# closes) an allow-listed external backup drive by LUKS2 label.
#
#   erpbak-mount.sh mount  ERPBAK-A
#   erpbak-mount.sh umount ERPBAK-A
#
# Allow-list: /etc/smart-erp/backup-drives.yaml (see deploy/nuc/backup-drives.yaml.example)
# Key file  : from the allow-list entry (key_file), 0400 root, never on the drive.
# Mount     : /mnt/erpbak/<label>, 0700, owner erpbackup.

SCRIPT_NAME=erpbak-mount.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

ACTION="${1:-}"
LABEL="${2:-}"
[[ "${ACTION}" == "mount" || "${ACTION}" == "umount" ]] || die "usage: $0 mount|umount <LABEL>"
[[ "${LABEL}" == ERPBAK-* ]] || die "label must match ERPBAK-*: '${LABEL}'"
is_linux || die "LUKS drives are Linux-only"
[[ ${EUID} -eq 0 ]] || die "must run as root (systemd runs it as root)"
require_cmd cryptsetup blkid mount umount

BACKUP_USER="${ERP_BACKUP_USER:-erpbackup}"
MAPPER_NAME="$(echo "${LABEL}" | tr '[:upper:]' '[:lower:]')"   # erpbak-a
MAPPER_DEV="/dev/mapper/${MAPPER_NAME}"
MOUNT_POINT="${DRIVE_MOUNT_ROOT}/${LABEL}"

do_umount() {
  if mountpoint -q "${MOUNT_POINT}"; then
    sync
    umount "${MOUNT_POINT}" || { warn "busy, lazy-unmounting ${MOUNT_POINT}"; umount -l "${MOUNT_POINT}"; }
    log "unmounted ${MOUNT_POINT}"
  fi
  if [[ -e "${MAPPER_DEV}" ]]; then
    cryptsetup close "${MAPPER_NAME}"
    log "closed ${MAPPER_DEV}"
  fi
  log "${LABEL}: safe to remove"
}

do_mount() {
  local allow_uuid key_file dev dev_uuid
  allow_uuid="$(allowlist_field "${LABEL}" luks_uuid || true)"
  key_file="$(allowlist_field "${LABEL}" key_file || true)"
  if [[ -z "${allow_uuid}" ]]; then
    log "${LABEL} is not on the allow-list ${DRIVE_ALLOWLIST}; ignoring drive"
    exit 0
  fi
  [[ -n "${key_file}" ]] || key_file="${NUC_CONFIG_DIR}/keys/${LABEL}.key"
  [[ -r "${key_file}" ]] || die "key file ${key_file} missing or unreadable"

  dev="/dev/disk/by-uuid/${allow_uuid}"
  [[ -e "${dev}" ]] || die "no device with LUKS UUID ${allow_uuid} is connected (label ${LABEL})"
  dev_uuid="$(blkid -o value -s UUID "${dev}")"
  [[ "${dev_uuid}" == "${allow_uuid}" ]] || die "UUID mismatch for ${LABEL}: device ${dev_uuid} vs allow-list ${allow_uuid}"
  [[ "$(blkid -o value -s TYPE "${dev}")" == "crypto_LUKS" ]] || die "${dev} is not a LUKS device; refusing"

  if [[ ! -e "${MAPPER_DEV}" ]]; then
    cryptsetup open --key-file "${key_file}" --allow-discards "${dev}" "${MAPPER_NAME}"
    log "unlocked ${dev} as ${MAPPER_DEV}"
  fi

  install -d -m 0700 -o "${BACKUP_USER}" -g "${BACKUP_USER}" "${MOUNT_POINT}"
  if ! mountpoint -q "${MOUNT_POINT}"; then
    mount -o noatime,nodev,nosuid,noexec "${MAPPER_DEV}" "${MOUNT_POINT}"
  fi
  chown "${BACKUP_USER}:${BACKUP_USER}" "${MOUNT_POINT}"
  chmod 0700 "${MOUNT_POINT}"
  log "mounted ${LABEL} at ${MOUNT_POINT} (0700 ${BACKUP_USER}), free $(df -h --output=avail "${MOUNT_POINT}" | tail -1 | tr -d ' ')"
}

case "${ACTION}" in
  mount) do_mount ;;
  umount) do_umount ;;
esac
