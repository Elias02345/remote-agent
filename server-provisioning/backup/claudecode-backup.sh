#!/usr/bin/env bash
# claudecode-backup.sh — restic backup (docs/ARCHITECTURE.md Section 7.2).
#
# Deliberately NOT coupled to the idle locks from Section 4. An update can
# restart services and destabilise the system while it runs; a backup only
# reads data and writes it elsewhere, which is harmless during an agent run or
# with terminals open. Waiting for idle would mean a busy machine — exactly the
# one with the most unsaved work — gets backed up least often.
set -Eeuo pipefail

RESTIC_REPOSITORY="${RESTIC_REPOSITORY:-/srv/backups/restic}"
RESTIC_PASSWORD_FILE="${RESTIC_PASSWORD_FILE:-/etc/claudecode-remote/restic-password}"
# Architecture Section 7.2 asks for the agent's configuration and the untracked
# working data too, not just the daemon's own state. The previous default
# covered neither: a restore would come back with paired devices and the file
# exchange intact, and without the owner's .env, the agent's global CLAUDE.md,
# its GitHub key, or any project checkout's untracked .env — i.e. a successful
# backup that still loses the things that are actually hard to recreate.
#
# /home/agent is the largest of these; the excludes below keep the caches out.
BACKUP_PATHS="${CCR_BACKUP_PATHS:-/var/lib/claudecode-remote /srv/exchange /etc/claudecode-remote /home/agent}"
RETENTION_DAILY="${CCR_RETENTION_DAILY:-7}"
RETENTION_WEEKLY="${CCR_RETENTION_WEEKLY:-4}"
RETENTION_MONTHLY="${CCR_RETENTION_MONTHLY:-6}"
NTFY_URL="${NTFY_URL:-}"

# restic needs somewhere to put its cache, and it finds that from $XDG_CACHE_HOME
# or $HOME. systemd sets neither for a service without `User=`, so under the
# timer restic dies with "unable to locate cache directory" — while the same
# script run by hand in a shell works perfectly, because a shell sets HOME.
# Every hourly backup would have failed on the real server.
#
# Setting it explicitly removes the dependency on the environment entirely,
# rather than papering over it with Environment=HOME=/root in the unit.
RESTIC_CACHE_DIR="${RESTIC_CACHE_DIR:-/var/cache/claudecode-remote/restic}"
mkdir -p "$RESTIC_CACHE_DIR"

export RESTIC_REPOSITORY RESTIC_PASSWORD_FILE RESTIC_CACHE_DIR

note() { logger -t claudecode-backup "$*" 2>/dev/null || true; echo "$*"; }
die()  { note "ERROR: $*"; exit 1; }

command -v restic >/dev/null 2>&1 || die "restic is not installed"
[[ -r "$RESTIC_PASSWORD_FILE" ]] || die "password file not readable: ${RESTIC_PASSWORD_FILE}"

# Initialise on first run. `restic cat config` is the cheap way to ask "is this
# already a repository" without creating one as a side effect.
if ! restic cat config >/dev/null 2>&1; then
  note "Initialising restic repository at ${RESTIC_REPOSITORY}"
  restic init || die "restic init failed"
fi

# Only back up paths that exist. A missing path aborts the whole run otherwise,
# so one not-yet-created directory would silently mean no backups at all.
existing=()
for p in $BACKUP_PATHS; do
  if [[ -e "$p" ]]; then
    existing+=("$p")
  else
    note "Skipping ${p} (does not exist)"
  fi
done
[[ ${#existing[@]} -gt 0 ]] || die "none of the configured backup paths exist"

# The daemon's SQLite database runs in WAL mode, so the bytes on disk at any
# instant are not a valid database: a snapshot taken mid-checkpoint gets a main
# file and a -wal that disagree. restic copies files, it does not know that.
#
# `.backup` is SQLite's online backup API — it takes the right locks and hands
# back a file that is internally consistent no matter what the daemon is doing.
# Backing that copy up alongside the live files means the restore has something
# guaranteed to open, instead of something that usually opens.
LIVE_DB="/var/lib/claudecode-remote/daemon.db"
CONSISTENT_DB="/var/lib/claudecode-remote/daemon-consistent.db"
if [[ -f "$LIVE_DB" ]]; then
  if command -v sqlite3 >/dev/null 2>&1; then
    if sqlite3 "$LIVE_DB" ".backup '${CONSISTENT_DB}'"; then
      note "Wrote a consistent database snapshot to ${CONSISTENT_DB}"
    else
      note "WARNING: sqlite3 .backup failed; the snapshot contains only the live database files"
    fi
  else
    note "WARNING: sqlite3 is not installed, cannot take a consistent database snapshot"
  fi
fi

note "Backing up: ${existing[*]}"
restic backup \
  --exclude-caches \
  --exclude 'node_modules' \
  --exclude '*.tmp' \
  --exclude '/var/lib/claudecode-remote/gocache' \
  --exclude '/var/lib/claudecode-remote/uploads' \
  --exclude '/home/agent/.cache' \
  --exclude '/home/agent/.npm' \
  --tag claudecode \
  "${existing[@]}" || die "restic backup failed"

note "Applying retention policy"
restic forget \
  --keep-daily "$RETENTION_DAILY" \
  --keep-weekly "$RETENTION_WEEKLY" \
  --keep-monthly "$RETENTION_MONTHLY" \
  --prune || note "WARNING: retention pass failed, snapshots were still written"

if [[ -n "$NTFY_URL" ]]; then
  curl -s -d "ClaudeCode Remote: backup finished ($(date '+%d.%m %H:%M'))" "$NTFY_URL" >/dev/null || true
fi

note "Backup finished"
