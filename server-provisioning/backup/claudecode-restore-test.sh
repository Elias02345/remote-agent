#!/usr/bin/env bash
# claudecode-restore-test.sh — proves the backups can actually come back
# (docs/ARCHITECTURE.md Section 7.2).
#
# A backup that has never been restored is a guess, not a backup. This runs
# `restic check` for repository integrity and then restores the latest snapshot
# into a scratch directory and confirms files really landed there — a check
# that passes on an empty snapshot would be worthless.
set -Eeuo pipefail

RESTIC_REPOSITORY="${RESTIC_REPOSITORY:-/srv/backups/restic}"
RESTIC_PASSWORD_FILE="${RESTIC_PASSWORD_FILE:-/etc/claudecode-remote/restic-password}"
SCRATCH="${CCR_RESTORE_SCRATCH:-/var/tmp/claudecode-restore-test}"
NTFY_URL="${NTFY_URL:-}"

# Same reason as claudecode-backup.sh: systemd sets neither $HOME nor
# $XDG_CACHE_HOME for a service without `User=`, and restic refuses to run
# without a cache directory it can locate.
RESTIC_CACHE_DIR="${RESTIC_CACHE_DIR:-/var/cache/claudecode-remote/restic}"
mkdir -p "$RESTIC_CACHE_DIR"

export RESTIC_REPOSITORY RESTIC_PASSWORD_FILE RESTIC_CACHE_DIR

note() { logger -t claudecode-restore-test "$*" 2>/dev/null || true; echo "$*"; }

fail_out() {
  note "RESTORE TEST FAILED: $*"
  if [[ -n "$NTFY_URL" ]]; then
    curl -s -H "Priority: high" -d "ClaudeCode Remote: RESTORE TEST FAILED - $*" "$NTFY_URL" >/dev/null || true
  fi
  exit 1
}

command -v restic >/dev/null 2>&1 || fail_out "restic is not installed"
[[ -r "$RESTIC_PASSWORD_FILE" ]] || fail_out "password file not readable: ${RESTIC_PASSWORD_FILE}"

note "Checking repository integrity"
restic check || fail_out "restic check reported problems"

note "Restoring the latest snapshot into ${SCRATCH}"
rm -rf "$SCRATCH"
mkdir -p "$SCRATCH"
restic restore latest --target "$SCRATCH" || fail_out "restore of the latest snapshot failed"

# The point of the whole exercise: a restore that produces nothing is a failed
# restore, even when restic exits 0.
restored_count="$(find "$SCRATCH" -type f | wc -l)"
if [[ "$restored_count" -eq 0 ]]; then
  fail_out "restore produced no files"
fi

note "Restore test passed: ${restored_count} files restored"
rm -rf "$SCRATCH"

if [[ -n "$NTFY_URL" ]]; then
  curl -s -d "ClaudeCode Remote: restore test passed (${restored_count} files)" "$NTFY_URL" >/dev/null || true
fi
