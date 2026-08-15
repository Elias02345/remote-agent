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
NTFY_URL="${NTFY_URL:-}"

# Where the throwaway restore goes. This used to be a configurable full path
# that the script then `rm -rf`'d, twice, as root — so a typo'd or hostile
# CCR_RESTORE_SCRATCH in the environment file was a recursive delete of any
# path on the machine, sacred paths included. It was also a fixed name under a
# world-writable /var/tmp, which the agent user could re-create between the
# delete and the restore and so redirect the restored data.
#
# Now only the *parent* is configurable, the directory itself is created by
# mktemp with a random name and 0700, and the only thing ever removed is the
# directory this run created.
SCRATCH_BASE="${CCR_RESTORE_SCRATCH_BASE:-/var/tmp}"
[[ "$SCRATCH_BASE" == /* ]] || { echo "CCR_RESTORE_SCRATCH_BASE must be an absolute path" >&2; exit 1; }
[[ -d "$SCRATCH_BASE" ]] || { echo "CCR_RESTORE_SCRATCH_BASE is not a directory: ${SCRATCH_BASE}" >&2; exit 1; }

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

SCRATCH="$(mktemp -d "${SCRATCH_BASE}/claudecode-restore-test.XXXXXXXX")"
chmod 0700 "$SCRATCH"
# The trap is the only remover, and it can only ever remove the directory
# mktemp just handed us.
trap 'rm -rf -- "$SCRATCH"' EXIT

note "Restoring the latest snapshot into ${SCRATCH}"
restic restore latest --target "$SCRATCH" || fail_out "restore of the latest snapshot failed"

# The point of the whole exercise: a restore that produces nothing is a failed
# restore, even when restic exits 0.
restored_count="$(find "$SCRATCH" -type f | wc -l)"
if [[ "$restored_count" -eq 0 ]]; then
  fail_out "restore produced no files"
fi

# Files existing is not the same as data being usable. The daemon's SQLite
# database is the one file whose loss actually costs the owner something, and
# restic snapshots it live, in WAL mode — so the real question is whether the
# restored copy opens and answers a query, not whether a file with that name
# came back. `integrity_check` is what turns this from a file-count assertion
# into a restore test.
restored_db="$(find "$SCRATCH" -type f -name 'daemon-consistent.db' -print -quit)"
if [[ -n "$restored_db" ]]; then
  if command -v sqlite3 >/dev/null 2>&1; then
    db_status="$(sqlite3 "file:${restored_db}?immutable=1" 'PRAGMA integrity_check;' 2>&1 || echo 'query failed')"
    [[ "$db_status" == "ok" ]] || fail_out "restored daemon.db failed integrity_check: ${db_status}"
    note "Restored daemon.db passes integrity_check"
  else
    note "WARNING: sqlite3 not installed, could not verify the restored database"
  fi
else
  note "WARNING: no daemon-consistent.db in the snapshot — nothing to verify beyond the file count"
fi

note "Restore test passed: ${restored_count} files restored"

if [[ -n "$NTFY_URL" ]]; then
  curl -s -d "ClaudeCode Remote: restore test passed (${restored_count} files)" "$NTFY_URL" >/dev/null || true
fi
