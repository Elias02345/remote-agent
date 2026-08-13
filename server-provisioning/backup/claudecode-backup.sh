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
BACKUP_PATHS="${CCR_BACKUP_PATHS:-/var/lib/claudecode-remote /srv/exchange}"
RETENTION_DAILY="${CCR_RETENTION_DAILY:-7}"
RETENTION_WEEKLY="${CCR_RETENTION_WEEKLY:-4}"
RETENTION_MONTHLY="${CCR_RETENTION_MONTHLY:-6}"
NTFY_URL="${NTFY_URL:-}"

export RESTIC_REPOSITORY RESTIC_PASSWORD_FILE

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

note "Backing up: ${existing[*]}"
restic backup \
  --exclude-caches \
  --exclude 'node_modules' \
  --exclude '*.tmp' \
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
