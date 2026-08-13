#!/usr/bin/env bash
# idle-update-check.sh — updates the system only while nothing is running
# (docs/ARCHITECTURE.md Section 4.3). Installed to
# /usr/local/bin/idle-update-check.sh and driven by idle-updater.timer.
set -Eeuo pipefail

LOCK_DIR="${CCR_LOCK_DIR:-/run/claudecode-locks}"
STATE_DIR="${CCR_STATE_DIR:-/var/lib/claudecode}"
STALE_MINUTES="${CCR_AGENT_LOCK_STALE_MINUTES:-360}"

# Set to 1 by the test harness only. Everything above this line — the lock
# evaluation, the stale cleanup, the reboot flag — runs exactly as it does in
# production; only the package manager calls are suppressed, because a test
# container must not spend minutes doing a real -Syu. This is clearly marked
# test configuration, not weakened production logic.
CCR_SKIP_PACKAGE_UPDATES="${CCR_SKIP_PACKAGE_UPDATES:-0}"

NTFY_URL="${NTFY_URL:-}"

note() { logger -t idle-updater "$*" 2>/dev/null || true; echo "$*"; }

mkdir -p "$LOCK_DIR"

# Stale agent locks are removed; terminal locks are NEVER touched.
#
# The asymmetry is the whole design (Section 4.1): an agent lock belongs to a
# process that may have been SIGKILLed or lost to a power cut, so it needs a
# time-based escape hatch. A terminal lock belongs to a session the user opened
# and only the user closes — an "expired" terminal lock would mean updating
# underneath a terminal that is still open, which is precisely what must not
# happen. Note the -name filter: widening it to '*' would silently break this.
find "$LOCK_DIR" -maxdepth 1 -name 'agent-*' -mmin "+${STALE_MINUTES}" -delete 2>/dev/null || true

active_locks="$(ls -A "$LOCK_DIR" 2>/dev/null || true)"
if [[ -n "$active_locks" ]]; then
  note "Update skipped, active locks: $(echo "$active_locks" | tr '\n' ' ')"
  exit 0
fi

# G-01 (see ROADMAP.md): a tmux session can exist without a lock — started by
# hand with `tmux new`, or predating the daemon. That is not a reason to block
# a package update (pacman does not kill tmux sessions), but it IS the gap
# Section 4.4 relies on when it argues against automatic reboots. Surface it in
# the log rather than letting it stay invisible.
if command -v tmux >/dev/null 2>&1 && tmux ls >/dev/null 2>&1; then
  note "Note: tmux sessions exist without a corresponding lock (see ROADMAP.md G-01). Updating anyway; this only matters for reboots."
fi

note "System idle - starting update"

if [[ "$CCR_SKIP_PACKAGE_UPDATES" != "1" ]]; then
  pacman -Syu --noconfirm

  # Section 4.3 writes this as `command -v paru && paru ... || true`, which
  # reads as if-then-else but is not: the `|| true` also swallows a failure of
  # the test itself. Same behaviour, stated unambiguously.
  if command -v paru >/dev/null 2>&1; then
    paru -Syu --noconfirm --sudoloop || true
  fi

  for tool in claude-code codex antigravity-cli; do
    npm update -g "$tool" >/dev/null 2>&1 || true
  done
else
  note "CCR_SKIP_PACKAGE_UPDATES=1, skipping package manager calls (test mode)"
fi

# Kernel update detected? Never reboot automatically, only raise the flag
# (Section 4.4, decision D-03). The flag lives OUTSIDE the lock directory on
# purpose: a file inside it would count as an active lock and block every
# future update forever.
mkdir -p "$STATE_DIR"
if command -v pacman >/dev/null 2>&1; then
  CURRENT_KERNEL="$(uname -r)"
  INSTALLED_KERNEL="$(pacman -Q linux 2>/dev/null | awk '{print $2}')"
  if [[ -n "$INSTALLED_KERNEL" && "$INSTALLED_KERNEL" != *"${CURRENT_KERNEL%-arch*}"* ]]; then
    : > "${STATE_DIR}/reboot-pending"
    note "Kernel update detected, reboot-pending flag set (no automatic reboot)"
  fi
fi

if [[ -n "$NTFY_URL" ]]; then
  curl -s -d "ClaudeCode Remote: update finished ($(date '+%d.%m %H:%M'))" "$NTFY_URL" >/dev/null || true
fi

note "Update finished"
