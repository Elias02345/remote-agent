#!/usr/bin/env bash
# server-provisioning/test/backup-cases.sh
#
# Assertions for the Samba configuration and the restic backup/restore cycle
# (docs/ARCHITECTURE.md Section 7, ROADMAP.md Phase 5). Runs INSIDE the test
# container, started by run-backup-tests.sh.
set -Eeuo pipefail

PASS=0
FAIL=0

pass()  { echo "[PASS] $1"; PASS=$((PASS + 1)); }
fails() { echo "[FAIL] $1"; FAIL=$((FAIL + 1)); }

assert_ok()       { if [[ "$2" -eq 0 ]]; then pass "$1"; else fails "$1 (exit ${2})"; fi; }
assert_fails()    { if [[ "$2" -ne 0 ]]; then pass "$1"; else fails "$1 (expected non-zero exit, got 0)"; fi; }
assert_eq()       { if [[ "$2" == "$3" ]]; then pass "$1"; else fails "$1 (expected [${2}], got [${3}])"; fi; }
assert_grep()     { if grep -qE "$3" "$2"; then pass "$1"; else fails "$1 (pattern '${3}' not found in ${2})"; fi; }
assert_not_grep() { if grep -qE "$3" "$2"; then fails "$1 (pattern '${3}' unexpectedly present in ${2})"; else pass "$1"; fi; }

rc_of() { local rc=0; "$@" >/dev/null 2>&1 || rc=$?; echo "$rc"; }

# The installer chowns /srv/exchange to the agent user, which provision.sh
# normally creates.
id -u agent >/dev/null 2>&1 || useradd -m -s /bin/bash agent

echo
echo "== Installer =="

rc=$(rc_of bash /repo/install-files-backups.sh)
assert_ok "install-files-backups.sh exits 0" "$rc"

SMB=/etc/samba/smb.conf
PWFILE=/etc/claudecode-remote/restic-password

echo
echo "== Samba: the shares must never leave the mesh =="

# These two lines are the entire reason it is safe to run Samba here at all.
assert_grep "smb.conf sets 'bind interfaces only = yes'" "$SMB" '^\s*bind interfaces only = yes'
assert_grep "smb.conf binds only to lo and the Tailscale interface" "$SMB" '^\s*interfaces = lo tailscale0'
assert_grep "smb.conf requires transport encryption" "$SMB" '^\s*smb encrypt = required'
assert_grep "smb.conf requires SMB3 as a minimum" "$SMB" '^\s*server min protocol = SMB3'
assert_grep "guest access is disabled" "$SMB" '^\s*map to guest = never'
# A public interface here would put the backup archive on the open network.
assert_not_grep "smb.conf does not bind to a wildcard address" "$SMB" 'interfaces = .*0\.0\.0\.0'
assert_not_grep "smb.conf has no 'hosts allow = ALL'" "$SMB" 'hosts allow\s*=\s*ALL'

# The backups share holds the snapshots; a writable client could delete the
# very backups it is meant to protect.
if awk '/^\[backups\]/{f=1;next}/^\[/{f=0}f' "$SMB" | grep -qE '^\s*read only = yes'; then
  pass "the backups share is read-only"
else
  fails "the backups share is not marked read only"
fi

if command -v testparm >/dev/null 2>&1; then
  rc=$(rc_of testparm -s "$SMB")
  assert_ok "testparm accepts the generated smb.conf" "$rc"
fi

echo
echo "== Restic password: the one thing a re-run must never regenerate =="

assert_eq "restic password file has mode 0600" "600" "$(stat -c '%a' "$PWFILE")"
first_hash="$(sha256sum < "$PWFILE")"

rc=$(rc_of bash /repo/install-files-backups.sh)
assert_ok "installer is re-runnable" "$rc"
second_hash="$(sha256sum < "$PWFILE")"
# Regenerating it would render every existing snapshot permanently unreadable.
assert_eq "re-running the installer does not regenerate the restic password" "$first_hash" "$second_hash"

echo
echo "== Backup and restore round trip =="

export RESTIC_REPOSITORY=/tmp/restic-repo
export RESTIC_PASSWORD_FILE="$PWFILE"
export CCR_BACKUP_PATHS=/tmp/src
export CCR_RESTORE_SCRATCH=/tmp/restore-scratch

mkdir -p /tmp/src/nested
echo "important project data" > /tmp/src/file-a.txt
echo "nested content" > /tmp/src/nested/file-b.txt
src_hash="$(sha256sum /tmp/src/file-a.txt | cut -d' ' -f1)"

rc=$(rc_of claudecode-backup.sh)
assert_ok "backup script exits 0 and initialises the repository" "$rc"

snapshot_count="$(restic snapshots --json 2>/dev/null | grep -o '"short_id"' | wc -l)"
if [[ "$snapshot_count" -ge 1 ]]; then
  pass "a snapshot exists after the backup"
else
  fails "no snapshot was created"
fi

# The Definition of Done: the restore-test timer must demonstrably run through.
rc=$(rc_of claudecode-restore-test.sh)
assert_ok "restore test passes (restic check plus a real restore)" "$rc"

# And the restored bytes must actually match. A restore that exits 0 but
# produces different content is the failure mode nobody notices until it is
# too late.
rm -rf /tmp/verify && mkdir -p /tmp/verify
restic restore latest --target /tmp/verify >/dev/null 2>&1 || true
restored_file="$(find /tmp/verify -name 'file-a.txt' -type f | head -n1)"
if [[ -n "$restored_file" ]]; then
  pass "the restored tree contains the backed-up file"
  assert_eq "restored content is byte-identical to the source" \
    "$src_hash" "$(sha256sum "$restored_file" | cut -d' ' -f1)"
else
  fails "the restored tree does not contain the backed-up file"
  fails "restored content could not be compared"
fi

echo
echo "== Failure handling =="

# Each case runs in a subshell. A `VAR=x func` prefix would stay set after the
# function returns in bash, so the second case would then fail for the first
# case's reason and still look like it passed.
#
# A backup that silently does nothing because its key is gone is worse than one
# that fails loudly.
rc=0
( export RESTIC_PASSWORD_FILE=/tmp/definitely-not-here; claudecode-backup.sh ) >/dev/null 2>&1 || rc=$?
assert_fails "backup fails loudly when the password file is missing" "$rc"

rc=0
( export CCR_BACKUP_PATHS=/tmp/does-not-exist; claudecode-backup.sh ) >/dev/null 2>&1 || rc=$?
assert_fails "backup fails when none of the configured paths exist" "$rc"

echo
echo "Results: ${PASS} passed, ${FAIL} failed."
[[ "$FAIL" -eq 0 ]]
