#!/usr/bin/env bash
# install-files-backups.sh — Samba shares and restic backups
# (docs/ARCHITECTURE.md Section 7). Idempotent, safe to re-run.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# shellcheck source-path=SCRIPTDIR
# shellcheck source=lib/common.sh
source "$SCRIPT_DIR/lib/common.sh"

require_root

CCR_AGENT_USER="${CCR_AGENT_USER:-agent}"
CCR_TAILSCALE_IFACE="${CCR_TAILSCALE_IFACE:-tailscale0}"
CCR_SMB_USER="${CCR_SMB_USER:-$CCR_AGENT_USER}"
CCR_SKIP_SAMBA="${CCR_SKIP_SAMBA:-0}"
SECRETS_DIR="/etc/claudecode-remote"
RESTIC_PASSWORD_FILE="${SECRETS_DIR}/restic-password"

step_directories() {
  banner "Creating /srv directories"
  install -d -m 0755 /srv/backups
  install -d -o "$CCR_AGENT_USER" -g "$CCR_AGENT_USER" -m 0775 /srv/exchange
  ok "/srv/backups and /srv/exchange in place"
}

step_restic_password() {
  banner "Restic repository password"

  install -d -m 0700 "$SECRETS_DIR"

  # Never regenerate an existing password: it is the only key to every
  # existing snapshot, and overwriting it would render the entire backup
  # archive permanently unreadable. This is the single most destructive thing
  # a re-run of this script could do, so it is guarded first.
  if [[ -s "$RESTIC_PASSWORD_FILE" ]]; then
    ok "Restic password already present, leaving it untouched"
    return 0
  fi

  if command_exists openssl; then
    openssl rand -base64 48 > "$RESTIC_PASSWORD_FILE"
  else
    head -c 48 /dev/urandom | base64 > "$RESTIC_PASSWORD_FILE"
  fi
  chmod 0600 "$RESTIC_PASSWORD_FILE"
  ok "Generated restic password at ${RESTIC_PASSWORD_FILE}"
  warn "Back this file up OFF the server. Losing it means losing every backup."
}

step_restic() {
  banner "Installing restic and backup scripts"
  pkg_install restic
  install -m 0755 "$SCRIPT_DIR/backup/claudecode-backup.sh" /usr/local/bin/claudecode-backup.sh
  install -m 0755 "$SCRIPT_DIR/backup/claudecode-restore-test.sh" /usr/local/bin/claudecode-restore-test.sh
  ok "Backup scripts installed"
}

step_samba() {
  banner "Configuring Samba"

  if [[ "$CCR_SKIP_SAMBA" == "1" ]]; then
    warn "CCR_SKIP_SAMBA=1, skipping Samba entirely"
    return 0
  fi

  pkg_install samba

  install -d -m 0755 /etc/samba
  sed -e "s|@@TAILSCALE_IFACE@@|${CCR_TAILSCALE_IFACE}|g" \
      -e "s|@@SMB_USER@@|${CCR_SMB_USER}|g" \
      "$SCRIPT_DIR/samba/smb.conf.template" > /etc/samba/smb.conf
  chmod 0644 /etc/samba/smb.conf

  # A config that binds beyond the mesh would put backups and source code on
  # the open network, so verify rather than trust the substitution.
  grep -q '^   bind interfaces only = yes' /etc/samba/smb.conf ||
    fail "generated smb.conf is missing 'bind interfaces only' — refusing to continue"
  grep -q "interfaces = lo ${CCR_TAILSCALE_IFACE}" /etc/samba/smb.conf ||
    fail "generated smb.conf does not bind to ${CCR_TAILSCALE_IFACE} — refusing to continue"

  if command_exists testparm; then
    testparm -s /etc/samba/smb.conf >/dev/null 2>&1 || fail "testparm rejected the generated smb.conf"
    ok "smb.conf validated with testparm"
  fi

  svc_enable_now smb
  ok "Samba configured for ${CCR_TAILSCALE_IFACE} only"

  warn "Set the share password once with: smbpasswd -a ${CCR_SMB_USER}"
}

step_units() {
  banner "Installing backup timers"

  install -m 0644 "$SCRIPT_DIR/systemd/claudecode-backup.service" /etc/systemd/system/claudecode-backup.service
  install -m 0644 "$SCRIPT_DIR/systemd/claudecode-backup.timer" /etc/systemd/system/claudecode-backup.timer
  install -m 0644 "$SCRIPT_DIR/systemd/claudecode-restore-test.service" /etc/systemd/system/claudecode-restore-test.service
  install -m 0644 "$SCRIPT_DIR/systemd/claudecode-restore-test.timer" /etc/systemd/system/claudecode-restore-test.timer

  if has_systemd; then
    systemctl daemon-reload
    systemctl enable --now claudecode-backup.timer
    systemctl enable --now claudecode-restore-test.timer
    ok "Backup and restore-test timers enabled"
  else
    warn "systemd not available as init, units installed but not enabled"
  fi
}

print_summary() {
  banner "Files and backups summary"
  log "Shares:          /srv/backups (read-only), /srv/exchange (read-write)"
  log "Interface:       ${CCR_TAILSCALE_IFACE} only — never the public interface"
  log "Restic repo:     /srv/backups/restic"
  log "Password file:   ${RESTIC_PASSWORD_FILE}"
  log "Backup timer:    hourly"
  log "Restore test:    monthly"
  warn "Copy ${RESTIC_PASSWORD_FILE} somewhere off this machine. Without it the backups are unreadable."
  log "Backups run independently of the idle locks, on purpose: they only read data."
}

step_directories
step_restic_password
step_restic
step_samba
step_units
print_summary

exit 0
