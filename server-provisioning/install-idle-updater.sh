#!/usr/bin/env bash
# install-idle-updater.sh — installs the idle update system
# (docs/ARCHITECTURE.md Section 4). Idempotent, safe to re-run.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# shellcheck source-path=SCRIPTDIR
# shellcheck source=lib/common.sh
source "$SCRIPT_DIR/lib/common.sh"

require_root

CCR_AGENT_USER="${CCR_AGENT_USER:-agent}"
LOCK_DIR="/run/claudecode-locks"
STATE_DIR="/var/lib/claudecode"

step_binaries() {
  banner "Installing agent-run and idle-update-check.sh"
  install -m 0755 "$SCRIPT_DIR/agent-run" /usr/local/bin/agent-run
  install -m 0755 "$SCRIPT_DIR/idle-update-check.sh" /usr/local/bin/idle-update-check.sh
  ok "Installed to /usr/local/bin"
}

step_directories() {
  banner "Creating lock and state directories"

  # /run is a tmpfs and disappears on reboot, so a tmpfiles.d entry recreates
  # the lock directory early on boot. Both scripts also mkdir -p it themselves,
  # so a missing tmpfiles drop-in is never fatal.
  install -d -m 0755 "$LOCK_DIR"
  printf 'd %s 0755 root root -\n' "$LOCK_DIR" > /etc/tmpfiles.d/claudecode-locks.conf
  ok "Lock directory: ${LOCK_DIR}"

  # The reboot-pending flag lives here, deliberately OUTSIDE the lock directory:
  # a file inside it would be counted as an active lock and would block every
  # future update permanently.
  install -d -m 0755 "$STATE_DIR"
  ok "State directory: ${STATE_DIR}"
}

step_aliases() {
  banner "Installing shell aliases"

  # A profile.d drop-in rather than editing the agent user's ~/.bashrc: that
  # file belongs to the user, and rewriting it on every run would eventually
  # eat something they wrote.
  cat > /etc/profile.d/claudecode-agents.sh <<'EOF'
# Route the coding agents through the lock wrapper so the idle updater can see
# them. Installed by install-idle-updater.sh; edits here are overwritten.
alias claude='agent-run claude'
alias codex='agent-run codex'
# Architecture Section 4.2 writes this as `agent-run antigravity-cli`, but that
# command does not exist: Antigravity CLI installs as a single binary named
# `agy`. Aliasing a nonexistent command would leave every Antigravity run
# outside the wrapper, and therefore unlocked against system updates.
alias antigravity='agent-run agy'
alias agy='agent-run agy'
EOF
  chmod 0644 /etc/profile.d/claudecode-agents.sh
  ok "Aliases installed for all users via /etc/profile.d"
}

step_units() {
  banner "Installing systemd units"

  install -m 0644 "$SCRIPT_DIR/systemd/idle-updater.service" /etc/systemd/system/idle-updater.service
  install -m 0644 "$SCRIPT_DIR/systemd/idle-updater.timer" /etc/systemd/system/idle-updater.timer

  if has_systemd; then
    systemctl daemon-reload
    systemctl enable --now idle-updater.timer
    ok "idle-updater.timer enabled and started"
  else
    warn "systemd not available as init, units installed but not enabled"
  fi
}

print_summary() {
  banner "Idle update system summary"
  log "Wrapper:        /usr/local/bin/agent-run"
  log "Update check:   /usr/local/bin/idle-update-check.sh"
  log "Locks:          ${LOCK_DIR}  (agent-* stale after 6 h, terminal-* never)"
  log "Reboot flag:    ${STATE_DIR}/reboot-pending"
  log "Timer:          every 30 min, 5 min after boot"
  log "Agent user:     ${CCR_AGENT_USER}"

  if [[ -z "${NTFY_URL:-}" ]]; then
    warn "NTFY_URL is not set — update notifications are disabled."
    warn "Set it in /etc/claudecode-remote/.env to get push notifications."
  fi
  log "Reboots are never automatic: a kernel update only sets the reboot-pending flag."
}

step_binaries
step_directories
step_aliases
step_units
print_summary

exit 0
