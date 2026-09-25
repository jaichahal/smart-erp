#!/usr/bin/env bash
# smart-erp NUC bootstrap. Idempotent; safe to re-run after every change to
# deploy/. Ubuntu Server LTS (22.04 / 24.04) on x86_64.
#
#   sudo ./bootstrap.sh [--repo-dir /path/to/smart-erp] [--no-tailscale] [--no-updates]
#
# What it does (spec 10, "Go-live sequence", step 2; ADR-16):
#   1. apt prerequisites, Docker Engine + compose/buildx plugins (Docker's apt repo)
#   2. Tailscale (operator access is Tailscale-only)
#   3. cryptsetup, age, jq and friends
#   4. erpbackup system user; /opt/smart-erp, /etc/smart-erp (0700), /mnt/erpbak,
#      /var/lib/smart-erp, /var/lib/smart-erp-drill
#   5. copies deploy/ into /opt/smart-erp/deploy (rsync, preserves .env)
#   6. installs systemd units + timers and the udev rule, enables them
#   7. unattended-upgrades with automatic reboot at 02:30
#   8. prints the remaining manual steps (secrets, drives, Tailscale login)
# It never starts the stack: that needs the unlocked .env, which only the
# operator can provide.

set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
INSTALL_TAILSCALE=1
CONFIGURE_UPDATES=1
TARGET_ROOT=/opt/smart-erp
CONFIG_DIR=/etc/smart-erp
BACKUP_USER=erpbackup

while [[ $# -gt 0 ]]; do
  case "$1" in
    --repo-dir) REPO_DIR="$(cd "${2:?}" && pwd)"; shift 2 ;;
    --no-tailscale) INSTALL_TAILSCALE=0; shift ;;
    --no-updates) CONFIGURE_UPDATES=0; shift ;;
    -h|--help) sed -n '2,20p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

log()  { printf '\n\033[1;34m==> %s\033[0m\n' "$*"; }
ok()   { printf '    \033[0;32m%s\033[0m\n' "$*"; }
die()  { printf '\033[0;31mERROR: %s\033[0m\n' "$*" >&2; exit 1; }

[[ ${EUID} -eq 0 ]] || die "run as root: sudo $0"
[[ -f /etc/os-release ]] && . /etc/os-release
[[ "${ID:-}" == "ubuntu" || "${ID_LIKE:-}" == *debian* ]] || die "this script targets Ubuntu Server / Debian (found ${ID:-unknown})"
[[ "$(uname -m)" == "x86_64" ]] || echo "WARNING: expected x86_64, found $(uname -m); images are multi-arch so continuing"
[[ -d "${REPO_DIR}/deploy/compose" ]] || die "cannot find deploy/compose under ${REPO_DIR}; pass --repo-dir"

export DEBIAN_FRONTEND=noninteractive
ARCH="$(dpkg --print-architecture)"
CODENAME="${VERSION_CODENAME:-$(lsb_release -cs)}"

# ---------------------------------------------------------------------------
log "1/8 apt prerequisites"
apt-get update -qq
apt-get install -y -qq --no-install-recommends \
  ca-certificates curl gnupg lsb-release apt-transport-https \
  cryptsetup age jq rsync util-linux smartmontools unattended-upgrades apt-listchanges \
  >/dev/null
ok "base packages present"

# ---------------------------------------------------------------------------
log "2/8 Docker Engine + compose plugin"
install -m 0755 -d /etc/apt/keyrings
if [[ ! -f /etc/apt/keyrings/docker.asc ]]; then
  curl -fsSL "https://download.docker.com/linux/${ID}/gpg" -o /etc/apt/keyrings/docker.asc
  chmod a+r /etc/apt/keyrings/docker.asc
fi
DOCKER_LIST="deb [arch=${ARCH} signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/${ID} ${CODENAME} stable"
if [[ ! -f /etc/apt/sources.list.d/docker.list ]] || ! grep -qF "${DOCKER_LIST}" /etc/apt/sources.list.d/docker.list; then
  echo "${DOCKER_LIST}" > /etc/apt/sources.list.d/docker.list
  apt-get update -qq
fi
apt-get install -y -qq docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin >/dev/null
# Daemon defaults: bounded logs, live-restore so a dockerd upgrade does not stop containers.
if [[ ! -f /etc/docker/daemon.json ]]; then
  cat > /etc/docker/daemon.json <<'EOF'
{
  "log-driver": "json-file",
  "log-opts": { "max-size": "20m", "max-file": "5" },
  "live-restore": true,
  "default-address-pools": [ { "base": "172.30.0.0/16", "size": 24 } ]
}
EOF
  systemctl restart docker
fi
systemctl enable --now docker >/dev/null
ok "docker $(docker --version | awk '{print $3}' | tr -d ,), compose $(docker compose version --short)"

# ---------------------------------------------------------------------------
log "3/8 Tailscale"
if [[ ${INSTALL_TAILSCALE} -eq 1 ]]; then
  if ! command -v tailscale >/dev/null 2>&1; then
    curl -fsSL "https://pkgs.tailscale.com/stable/${ID}/${CODENAME}.noarmor.gpg" -o /usr/share/keyrings/tailscale-archive-keyring.gpg
    curl -fsSL "https://pkgs.tailscale.com/stable/${ID}/${CODENAME}.tailscale-keyring.list" -o /etc/apt/sources.list.d/tailscale.list
    apt-get update -qq
    apt-get install -y -qq tailscale >/dev/null
  fi
  systemctl enable --now tailscaled >/dev/null
  if tailscale status >/dev/null 2>&1; then ok "tailscale up: $(tailscale ip -4 2>/dev/null | head -1)"; else ok "tailscale installed; not logged in yet (see next steps)"; fi
else
  ok "skipped (--no-tailscale)"
fi

# ---------------------------------------------------------------------------
log "4/8 users and directories"
if ! id -u "${BACKUP_USER}" >/dev/null 2>&1; then
  useradd --system --home-dir /var/lib/smart-erp --create-home --shell /usr/sbin/nologin "${BACKUP_USER}"
fi
# The backup user drives compose (docker group is root-equivalent; accepted on
# this single-purpose host, documented in restore-runbook.md).
usermod -aG docker "${BACKUP_USER}"
install -d -m 0755 "${TARGET_ROOT}"
install -d -m 0700 -o root -g root "${CONFIG_DIR}" "${CONFIG_DIR}/keys"
install -d -m 0755 -o root -g root /mnt/erpbak
install -d -m 0750 -o "${BACKUP_USER}" -g "${BACKUP_USER}" /var/lib/smart-erp /var/lib/smart-erp/staging /var/lib/smart-erp/reports
install -d -m 0750 -o "${BACKUP_USER}" -g "${BACKUP_USER}" /var/lib/smart-erp-drill
ok "user ${BACKUP_USER}, ${TARGET_ROOT}, ${CONFIG_DIR}, /mnt/erpbak, /var/lib/smart-erp{,-drill}"

# ---------------------------------------------------------------------------
log "5/8 deploy tree -> ${TARGET_ROOT}/deploy"
rsync -a --delete \
  --exclude '.env' --exclude '.env.*' --include '.env.*.example' \
  "${REPO_DIR}/deploy/" "${TARGET_ROOT}/deploy/"
chmod 0755 "${TARGET_ROOT}"/deploy/scripts/*.sh "${TARGET_ROOT}/deploy/nuc/bootstrap.sh"
chown -R root:root "${TARGET_ROOT}/deploy"
# Reports/staging are written by erpbackup through the compose run bind mounts.
if [[ ! -f "${CONFIG_DIR}/backup-drives.yaml" ]]; then
  install -m 0600 "${TARGET_ROOT}/deploy/nuc/backup-drives.yaml.example" "${CONFIG_DIR}/backup-drives.yaml"
  ok "installed template ${CONFIG_DIR}/backup-drives.yaml (edit UUIDs before plugging drives)"
fi
ok "synced $(find "${TARGET_ROOT}/deploy" -type f | wc -l) files"

# ---------------------------------------------------------------------------
log "6/8 systemd units and udev rule"
for unit in smart-erp.service smart-erp-backup.service smart-erp-backup.timer \
            smart-erp-anchor.service smart-erp-anchor.timer \
            smart-erp-drill.service smart-erp-drill.timer erpbak@.service; do
  install -m 0644 "${TARGET_ROOT}/deploy/systemd/${unit}" "/etc/systemd/system/${unit}"
done
install -m 0644 "${TARGET_ROOT}/deploy/systemd/99-erpbak.rules" /etc/udev/rules.d/99-erpbak.rules
systemctl daemon-reload
udevadm control --reload-rules
systemd-analyze verify /etc/systemd/system/smart-erp*.service /etc/systemd/system/erpbak@.service 2>&1 | grep -v '^$' || true
systemctl enable smart-erp.service smart-erp-backup.timer smart-erp-anchor.timer smart-erp-drill.timer >/dev/null
# Timers only fire once the stack unit exists and is running; start them now.
systemctl start smart-erp-backup.timer smart-erp-anchor.timer smart-erp-drill.timer
ok "enabled smart-erp.service (not started) and the backup/anchor/drill timers"

# ---------------------------------------------------------------------------
log "7/8 unattended-upgrades (security only, reboot window 02:30)"
if [[ ${CONFIGURE_UPDATES} -eq 1 ]]; then
  cat > /etc/apt/apt.conf.d/20auto-upgrades <<'EOF'
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Download-Upgradeable-Packages "1";
APT::Periodic::AutocleanInterval "7";
APT::Periodic::Unattended-Upgrade "1";
EOF
  cat > /etc/apt/apt.conf.d/52smart-erp-unattended <<'EOF'
// smart-erp: security updates only, automatic reboot in the 02:30 window
// (backup runs at 02:00 and sets /run/smart-erp/backup.running; see
// deploy/nuc/updates.md for the interaction and the rollback procedure).
Unattended-Upgrade::Allowed-Origins {
    "${distro_id}:${distro_codename}-security";
    "${distro_id}ESMApps:${distro_codename}-apps-security";
    "${distro_id}ESM:${distro_codename}-infra-security";
};
// Docker is upgraded by hand inside the update window, never automatically.
Unattended-Upgrade::Package-Blacklist {
    "docker-ce";
    "docker-ce-cli";
    "containerd.io";
    "docker-compose-plugin";
    "docker-buildx-plugin";
};
Unattended-Upgrade::Remove-Unused-Kernel-Packages "true";
Unattended-Upgrade::Remove-Unused-Dependencies "true";
Unattended-Upgrade::Automatic-Reboot "true";
Unattended-Upgrade::Automatic-Reboot-WithUsers "true";
Unattended-Upgrade::Automatic-Reboot-Time "02:30";
Unattended-Upgrade::SyslogEnable "true";
EOF
  systemctl enable --now unattended-upgrades >/dev/null
  # apt-daily-upgrade normally runs at 06:00; move it before the reboot window.
  mkdir -p /etc/systemd/system/apt-daily-upgrade.timer.d
  cat > /etc/systemd/system/apt-daily-upgrade.timer.d/override.conf <<'EOF'
[Timer]
OnCalendar=
OnCalendar=*-*-* 01:30
RandomizedDelaySec=10m
EOF
  systemctl daemon-reload
  systemctl restart apt-daily-upgrade.timer
  ok "security updates 01:30, reboot 02:30 if required"
else
  ok "skipped (--no-updates)"
fi

# ---------------------------------------------------------------------------
log "8/8 next steps (manual, operator)"
TS_IP="$(command -v tailscale >/dev/null 2>&1 && tailscale ip -4 2>/dev/null | head -1 || true)"
cat <<EOF

  1. Tailscale:      tailscale up --ssh            (then lock down sshd to the tailscale0 interface)
  2. Secrets:        cp ${TARGET_ROOT}/deploy/compose/.env.prod.example /root/env.plain
                     edit every CHANGE_ME, then
                     age-keygen -o ${CONFIG_DIR}/operator.key && chmod 0400 ${CONFIG_DIR}/operator.key
                     age -r \$(age-keygen -y ${CONFIG_DIR}/operator.key) -o ${CONFIG_DIR}/env.age /root/env.plain
                     shred -u /root/env.plain
                     (keep the operator key on the operator's hardware token, not only on disk)
  3. Images:         cd ${TARGET_ROOT}/deploy/compose && docker compose --profile prod pull   (pinned by digest in docker-compose.yml)
  4. Start:          systemctl start smart-erp.service && docker compose --profile prod ps
                     status page must show every service healthy, off-site anchor written,
                     WAL archiving current, backup timer armed, UPS reporting.
  5. Drives:         format ERPBAK-A / ERPBAK-B per ${TARGET_ROOT}/deploy/nuc/backup-drives.yaml.example,
                     fill ${CONFIG_DIR}/backup-drives.yaml, then: just drive-check
  6. Drills:         just drill --target drive && just drill --target offsite  -> identical chain heads (I17, I34)
                     Do NOT go live until both pass onto a clean machine.
  7. UPS:            see ${TARGET_ROOT}/deploy/nuc/ups.md (apcupsd/nut, USB signalling, shutdown at 20%)
  8. Off-site:       cloud bucket (object lock, COMPLIANCE), KMS/Vault key, OTLP sink credentials in the env file.

  Timers:  $(systemctl list-timers --no-pager 'smart-erp-*' | sed -n '2,4p' | awk '{print $NF}' | tr '\n' ' ')
  Tailscale IP: ${TS_IP:-not connected yet}
  Runbooks: ${TARGET_ROOT}/deploy/nuc/*.md
EOF
