#!/usr/bin/env bash
# server-provisioning/test/e2e-cases.sh
#
# The actual e2e assertions. Runs INSIDE the systemd container, started by
# run-e2e-tests.sh, as the container's real PID-1-supervised root shell (via
# `exec`, not a nested container) -- so `systemctl`, `journalctl` and `tmux`
# below are talking to the one real systemd this whole harness exists to
# reach, not a stub.
#
# /repo is server-provisioning/, bind-mounted read-only (installers only
# write outside it: /etc, /usr/local/bin, /var/lib, /srv, systemd unit
# files). /daemon-src is daemon/, also read-only -- `go build` writes its
# output to /usr/local/bin and its build cache under root's $HOME, never
# back into the mount.
set -Eeuo pipefail

PASS=0
FAIL=0

pass()  { echo "[PASS] $1"; PASS=$((PASS + 1)); }
fails() { echo "[FAIL] $1"; FAIL=$((FAIL + 1)); }

assert_ok()       { if [[ "$2" -eq 0 ]]; then pass "$1"; else fails "$1 (exit ${2})"; fi; }
assert_fails()    { if [[ "$2" -ne 0 ]]; then pass "$1"; else fails "$1 (expected non-zero exit, got 0)"; fi; }
assert_eq()       { if [[ "$2" == "$3" ]]; then pass "$1"; else fails "$1 (expected [${2}], got [${3}])"; fi; }
assert_contains() { if [[ "$2" == *"$3"* ]]; then pass "$1"; else fails "$1 (expected to find '${3}')"; fi; }
assert_exists()   { if [[ -e "$2" ]]; then pass "$1"; else fails "$1 (missing: ${2})"; fi; }
assert_missing()  { if [[ ! -e "$2" ]]; then pass "$1"; else fails "$1 (still exists: ${2})"; fi; }

# rc_of <command...> — runs a command, prints its exit code, never trips
# set -e. Same helper as backup-cases.sh / agentctl-cases.sh.
rc_of() {
  local rc=0
  "$@" >/dev/null 2>&1 || rc=$?
  echo "$rc"
}

# dump_journal_on_failure <unit> <rc> <journal>
#
# A red assertion that does not say why costs a full CI round trip to diagnose,
# and this harness is the slowest job in the workflow. When a unit fails, print
# its own journal and `systemctl status` immediately, so the next person reads
# the cause instead of guessing at it.
dump_journal_on_failure() {
  local unit="$1" rc="$2" journal="$3"
  [[ "$rc" -eq 0 ]] && return 0

  echo "--- ${unit} failed (exit ${rc}); its journal follows ---"
  echo "$journal"
  echo "--- systemctl status ${unit} ---"
  systemctl status "$unit" --no-pager --full 2>&1 || true
  echo "--- end of ${unit} diagnostics ---"
}

# --- configuration -----------------------------------------------------------

# Same dummy Ed25519 key run-tests.sh uses: provision.sh only checks the
# "ssh-ed25519 " prefix per the Phase 1 contract, never real key material.
export CCR_ADMIN_PUBKEY="ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIatestatestatestatestatestatest ccr-e2e-test"
export CCR_SKIP_TAILSCALE=1

# CCR_SKIP_PACKAGE_UPDATES is read by idle-update-check.sh's own process
# environment, but that script only ever runs as a systemd unit here — and a
# systemd unit's environment comes from `EnvironmentFile=`, never from this
# shell's exports. Written before anything else runs so it is already in
# place for both the explicit `systemctl start` below and the timer's own
# OnBootSec=5min, in case that fires first.
mkdir -p /etc/claudecode-remote
echo "CCR_SKIP_PACKAGE_UPDATES=1" > /etc/claudecode-remote/.env

echo
echo "== Install chain, first run (provision -> agent-stack -> idle-updater -> files-backups) =="

rc=$(rc_of bash /repo/provision.sh)
assert_ok "provision.sh exits 0 under real systemd" "$rc"

rc=$(rc_of bash /repo/install-agent-stack.sh)
assert_ok "install-agent-stack.sh exits 0" "$rc"

rc=$(rc_of bash /repo/install-idle-updater.sh)
assert_ok "install-idle-updater.sh exits 0" "$rc"

rc=$(rc_of bash /repo/install-files-backups.sh)
assert_ok "install-files-backups.sh exits 0" "$rc"

# Captured now, before the second pass, for the idempotency check further
# down — regenerating this on a re-run would render every existing snapshot
# permanently unreadable, so it is the single most important thing "same
# state" has to mean here.
# Guarded with `|| true`: under `set -e`, a plain top-level "var=$(cmd)"
# assignment fails the whole script if the substituted pipeline exits
# non-zero — and the exact scenario these lines exist to catch (a missing
# file, a zero count) IS a non-zero exit from grep/sha256sum. `|| true` on
# the assignment itself is exempt from errexit (it is not the final command
# in the list), so a broken case surfaces as an empty/zero value and a
# reported [FAIL] instead of silently aborting the whole run.
restic_hash_before="$(sha256sum < /etc/claudecode-remote/restic-password)" || true

echo
echo "== Units actually enabled by systemd (not just installed) =="

rc=$(rc_of systemctl is-enabled --quiet idle-updater.timer)
assert_ok "idle-updater.timer is enabled" "$rc"

rc=$(rc_of systemctl is-enabled --quiet claudecode-backup.timer)
assert_ok "claudecode-backup.timer is enabled" "$rc"

timers="$(systemctl list-timers --all --no-pager 2>&1)" || true
assert_contains "systemctl list-timers shows idle-updater.timer" "$timers" "idle-updater.timer"
assert_contains "systemctl list-timers shows claudecode-backup.timer" "$timers" "claudecode-backup.timer"

echo
echo "== idle-updater.service: the assertion that matters most =="

# `systemctl start` on a Type=oneshot unit blocks until ExecStart finishes
# and reports failure via its own exit code if the unit ends up failed — this
# is the one place this harness proves the unit is not just installed, it
# actually runs to completion under systemd.
rc=$(rc_of systemctl start idle-updater.service)
assert_ok "systemctl start idle-updater.service runs to completion" "$rc"

idle_journal="$(journalctl -u idle-updater.service --no-pager 2>&1)" || true
# An assertion that fails without saying why costs a whole CI round trip to
# diagnose. Dump the unit's own journal the moment it misbehaves.
dump_journal_on_failure "idle-updater.service" "$rc" "$idle_journal"
assert_contains "idle-updater journal shows the idle decision line" "$idle_journal" "System idle - starting update"
assert_contains "idle-updater journal shows the update finishing" "$idle_journal" "Update finished"
assert_contains "idle-updater journal proves CCR_SKIP_PACKAGE_UPDATES reached the unit via EnvironmentFile" \
  "$idle_journal" "CCR_SKIP_PACKAGE_UPDATES=1, skipping package manager calls"

echo
echo "== sshd under systemd =="

# Not opening a port to the host here — the point is only that sshd, hardened
# by provision.sh's step_ssh, actually comes up under real systemd. No other
# harness in this repo has ever run sshd, only validated its config file.
rc=$(rc_of systemctl is-active --quiet sshd)
assert_ok "sshd is active under systemd after hardening" "$rc"

echo
echo "== claudecode-backup.service: a real restic snapshot =="

rc=$(rc_of systemctl start claudecode-backup.service)
assert_ok "systemctl start claudecode-backup.service runs to completion" "$rc"

backup_journal="$(journalctl -u claudecode-backup.service --no-pager 2>&1)" || true
dump_journal_on_failure "claudecode-backup.service" "$rc" "$backup_journal"
assert_contains "backup journal shows the backup finishing" "$backup_journal" "Backup finished"

export RESTIC_REPOSITORY=/srv/backups/restic
export RESTIC_PASSWORD_FILE=/etc/claudecode-remote/restic-password
snapshot_count="$(restic snapshots --json 2>/dev/null | grep -o '"short_id"' | wc -l)" || true
if [[ "$snapshot_count" -ge 1 ]]; then
  pass "claudecode-backup.service produced a restic snapshot"
else
  fails "claudecode-backup.service produced no restic snapshot"
fi
unset RESTIC_REPOSITORY RESTIC_PASSWORD_FILE

echo
echo "== Install chain, second run (idempotency across all four installers at once) =="

rc=$(rc_of bash /repo/provision.sh)
assert_ok "provision.sh re-run exits 0" "$rc"

rc=$(rc_of bash /repo/install-agent-stack.sh)
assert_ok "install-agent-stack.sh re-run exits 0" "$rc"

rc=$(rc_of bash /repo/install-idle-updater.sh)
assert_ok "install-idle-updater.sh re-run exits 0" "$rc"

rc=$(rc_of bash /repo/install-files-backups.sh)
assert_ok "install-files-backups.sh re-run exits 0" "$rc"

restic_hash_after="$(sha256sum < /etc/claudecode-remote/restic-password)" || true
assert_eq "restic password unchanged after re-running the whole chain" "$restic_hash_before" "$restic_hash_after"

wheel_count="$(id -nG admin | tr ' ' '\n' | grep -c '^wheel$')" || true
assert_eq "admin is in wheel exactly once after two full runs (no duplicate membership)" "1" "$wheel_count"

port_count="$(grep -c '^Port ' /etc/ssh/sshd_config)" || true
assert_eq "exactly one active Port line in sshd_config after two full runs" "1" "$port_count"

rc=$(rc_of systemctl is-enabled --quiet idle-updater.timer)
assert_ok "idle-updater.timer still enabled after the second run" "$rc"

rc=$(rc_of systemctl is-enabled --quiet claudecode-backup.timer)
assert_ok "claudecode-backup.timer still enabled after the second run" "$rc"

echo
echo "== Build the daemon inside this container =="

# CGO_ENABLED=0: modernc.org/sqlite is a pure-Go SQLite, so there is no C
# toolchain in this image and none is needed.
rc=$(rc_of env CGO_ENABLED=0 go -C /daemon-src build -o /usr/local/bin/claudecode-remoted ./cmd/claudecode-remoted)
assert_ok "daemon builds with go build (CGO_ENABLED=0)" "$rc"

DAEMON_A_DB=/tmp/e2e-daemon-a.db
DAEMON_A_LOCKS=/tmp/e2e-locks-a
DAEMON_B_DB=/tmp/e2e-daemon-b.db
DAEMON_B_LOCKS=/tmp/e2e-locks-b
mkdir -p "$DAEMON_A_LOCKS" "$DAEMON_B_LOCKS"

wait_for_health() {
  local url="$1" tries=0
  while true; do
    if curl -s -o /dev/null "$url"; then
      return 0
    fi
    tries=$((tries + 1))
    [[ "$tries" -ge 30 ]] && return 1
    sleep 0.5
  done
}

echo
echo "== --insecure-no-auth is the only thing that opens /sessions =="

# Instance A: --insecure-no-auth, for the session lifecycle below (Section
# 5.2/7 note that this flag exists only for the xterm.js test client).
claudecode-remoted --insecure-no-auth --addr 127.0.0.1:8080 \
  --db "$DAEMON_A_DB" --lock-dir "$DAEMON_A_LOCKS" >/tmp/daemon-a.log 2>&1 &
DAEMON_A_PID=$!

# Instance B: no flags at all, on a different port/db/lock-dir — proves the
# 401 below is because auth is on by default, not an artifact of instance A's
# state.
claudecode-remoted --addr 127.0.0.1:8081 \
  --db "$DAEMON_B_DB" --lock-dir "$DAEMON_B_LOCKS" >/tmp/daemon-b.log 2>&1 &
DAEMON_B_PID=$!

if wait_for_health "http://127.0.0.1:8080/health"; then
  pass "daemon A (--insecure-no-auth) became healthy"
else
  fails "daemon A (--insecure-no-auth) never became healthy"
fi
assert_eq "GET /health returns {\"status\":\"ok\"}" '{"status":"ok"}' "$(curl -s http://127.0.0.1:8080/health)"

if wait_for_health "http://127.0.0.1:8081/health"; then
  pass "daemon B (default auth) became healthy"
else
  fails "daemon B (default auth) never became healthy"
fi

status="$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8081/sessions)" || true
assert_eq "unauthenticated GET /sessions without --insecure-no-auth returns 401" "401" "$status"

echo
echo "== Session lifecycle end to end (REST API -> tmux -> terminal lock) =="

create_body="$(curl -s -X POST http://127.0.0.1:8080/sessions -d '{"name":"e2e-test"}')" || true
session_id="$(echo "$create_body" | grep -o '"id":"[^"]*"' | head -n1 | cut -d'"' -f4)" || true
if [[ -n "$session_id" ]]; then
  pass "POST /sessions returned a session id"
else
  fails "POST /sessions did not return a session id (body: ${create_body})"
fi

tmux_name="ccr-${session_id}"
lock_file="${DAEMON_A_LOCKS}/terminal-${session_id}"

rc=$(rc_of tmux has-session -t "$tmux_name")
assert_ok "tmux session ${tmux_name} exists after session create" "$rc"
assert_exists "terminal lock file appeared after session create" "$lock_file"

delete_status="$(curl -s -o /dev/null -w '%{http_code}' -X DELETE "http://127.0.0.1:8080/sessions/${session_id}")" || true
assert_eq "DELETE /sessions/{id} returns 204" "204" "$delete_status"

rc=$(rc_of tmux has-session -t "$tmux_name")
assert_fails "tmux session ${tmux_name} is gone after DELETE" "$rc"
assert_missing "terminal lock file is gone after DELETE" "$lock_file"

kill "$DAEMON_A_PID" "$DAEMON_B_PID" 2>/dev/null || true

echo
echo "Results: ${PASS} passed, ${FAIL} failed."
[[ "$FAIL" -eq 0 ]]
