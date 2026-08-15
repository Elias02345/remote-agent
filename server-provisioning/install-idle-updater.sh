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
require_valid_name CCR_AGENT_USER "$CCR_AGENT_USER"
LOCK_DIR="/run/claudecode-locks"
STATE_DIR="/var/lib/claudecode"
# Group that may create lock files. Both writers — the agent-run wrapper and
# the daemon — run as unprivileged users, so a root-only directory means
# neither of them can lock anything.
LOCK_GROUP="ccr-locks"

id -u "$CCR_AGENT_USER" >/dev/null 2>&1 ||
  fail "User '${CCR_AGENT_USER}' does not exist. Run provision.sh first."

step_binaries() {
  banner "Installing agent-run and idle-update-check.sh"
  install -m 0755 "$SCRIPT_DIR/agent-run" /usr/local/bin/agent-run
  install -m 0755 "$SCRIPT_DIR/idle-update-check.sh" /usr/local/bin/idle-update-check.sh
  ok "Installed to /usr/local/bin"
}

step_directories() {
  banner "Creating lock and state directories"

  # The lock directory used to be created 0755 root:root, which made the whole
  # locking scheme inoperative: agent-run runs as the agent user and the daemon
  # runs unprivileged too, so neither could ever create a file in it. Wrapped
  # agents would die at startup and terminal creation would fail *after* it had
  # already made a tmux session and a database row.
  #
  # 1770 root:ccr-locks instead:
  #   - group write, so both writers can create their locks;
  #   - sticky (the leading 1), so a member of the group can delete only files
  #     it owns — nobody else's lock, and never a lock owned by root;
  #   - no world access at all, so a lock is not something any local account can
  #     drop to make the updater think the machine is idle.
  #
  # ponytail: this does not separate the daemon from the coding agents, because
  # today they are the same uid — a coding agent could still remove a terminal
  # lock the daemon created. Closing that needs the daemon to run under its own
  # user, which changes docs/ARCHITECTURE.md and is therefore the operator's
  # call, not a change to smuggle in here. Noted in TODO_FOR_USER.md.
  getent group "$LOCK_GROUP" >/dev/null 2>&1 || groupadd --system "$LOCK_GROUP"
  if id -nG "$CCR_AGENT_USER" 2>/dev/null | tr ' ' '\n' | grep -qx "$LOCK_GROUP"; then
    ok "${CCR_AGENT_USER} already in ${LOCK_GROUP}"
  else
    usermod -aG "$LOCK_GROUP" "$CCR_AGENT_USER"
    ok "Added ${CCR_AGENT_USER} to ${LOCK_GROUP}"
  fi

  # /run is a tmpfs and disappears on reboot, so a tmpfiles.d entry recreates
  # the lock directory early on boot. Both scripts also mkdir -p it themselves,
  # so a missing tmpfiles drop-in is never fatal.
  install -d -o root -g "$LOCK_GROUP" -m 1770 "$LOCK_DIR"
  printf 'd %s 1770 root %s -\n' "$LOCK_DIR" "$LOCK_GROUP" > /etc/tmpfiles.d/claudecode-locks.conf
  ok "Lock directory: ${LOCK_DIR} (1770 root:${LOCK_GROUP})"

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
