#!/usr/bin/env bash
# server-provisioning/test/idle-update-cases.sh
#
# Assertions for the idle update system (docs/ARCHITECTURE.md Section 4,
# ROADMAP.md Phase 3). Runs INSIDE the test container, started by
# run-idle-update-tests.sh.
#
# Everything works against CCR_LOCK_DIR / CCR_STATE_DIR under /tmp, so the
# real /run/claudecode-locks is never involved, and package manager calls are
# suppressed with CCR_SKIP_PACKAGE_UPDATES=1. The lock evaluation itself — the
# part under test — runs exactly as it does in production.
set -Eeuo pipefail

PASS=0
FAIL=0

pass()  { echo "[PASS] $1"; PASS=$((PASS + 1)); }
fails() { echo "[FAIL] $1"; FAIL=$((FAIL + 1)); }

assert_ok()       { if [[ "$2" -eq 0 ]]; then pass "$1"; else fails "$1 (exit ${2})"; fi; }
assert_fails()    { if [[ "$2" -ne 0 ]]; then pass "$1"; else fails "$1 (expected non-zero exit, got 0)"; fi; }
assert_eq()       { if [[ "$2" == "$3" ]]; then pass "$1"; else fails "$1 (expected [${2}], got [${3}])"; fi; }
assert_contains() { if [[ "$2" == *"$3"* ]]; then pass "$1"; else fails "$1 (expected to find '${3}')"; fi; }
assert_missing()  { if [[ ! -e "$2" ]]; then pass "$1"; else fails "$1 (still exists: ${2})"; fi; }
assert_exists()   { if [[ -e "$2" ]]; then pass "$1"; else fails "$1 (missing: ${2})"; fi; }

export CCR_LOCK_DIR=/tmp/locks
export CCR_STATE_DIR=/tmp/state
export CCR_SKIP_PACKAGE_UPDATES=1

install -m 0755 /repo/agent-run /usr/local/bin/agent-run
install -m 0755 /repo/idle-update-check.sh /usr/local/bin/idle-update-check.sh

reset_locks() { rm -rf "$CCR_LOCK_DIR"; mkdir -p "$CCR_LOCK_DIR"; }

# run_check — runs the update check, returning its combined output.
run_check() { idle-update-check.sh 2>&1 || true; }

IDLE_MARKER="System idle - starting update"
SKIP_MARKER="Update skipped, active locks"

echo
echo "== Update gating =="

reset_locks
out="$(run_check)"
assert_contains "empty lock directory lets the update run" "$out" "$IDLE_MARKER"

reset_locks
: > "$CCR_LOCK_DIR/agent-1234"
out="$(run_check)"
assert_contains "a fresh agent lock skips the update" "$out" "$SKIP_MARKER"

reset_locks
: > "$CCR_LOCK_DIR/terminal-abc123"
out="$(run_check)"
assert_contains "a terminal lock skips the update" "$out" "$SKIP_MARKER"

echo
echo "== Stale cleanup: the asymmetry between the two lock types =="

# An agent lock belongs to a process that may have been SIGKILLed or lost to a
# power cut, so it must eventually expire or updates are blocked forever.
reset_locks
: > "$CCR_LOCK_DIR/agent-9999"
touch -d '7 hours ago' "$CCR_LOCK_DIR/agent-9999"
out="$(run_check)"
assert_missing "agent lock older than 6 h is removed" "$CCR_LOCK_DIR/agent-9999"
assert_contains "update proceeds once the stale agent lock is gone" "$out" "$IDLE_MARKER"

# A terminal lock belongs to a session only the user closes. Expiring it would
# mean updating underneath a terminal that is still open. This assertion is the
# one that would catch someone "tidying up" the find filter to '*'.
reset_locks
: > "$CCR_LOCK_DIR/terminal-old"
touch -d '7 hours ago' "$CCR_LOCK_DIR/terminal-old"
out="$(run_check)"
assert_exists "terminal lock older than 6 h is NOT removed" "$CCR_LOCK_DIR/terminal-old"
assert_contains "an old terminal lock still skips the update" "$out" "$SKIP_MARKER"

# A very old agent lock must not drag a live terminal lock down with it.
reset_locks
: > "$CCR_LOCK_DIR/agent-1"
touch -d '7 hours ago' "$CCR_LOCK_DIR/agent-1"
: > "$CCR_LOCK_DIR/terminal-live"
out="$(run_check)"
assert_missing "stale agent lock removed while a terminal lock is present" "$CCR_LOCK_DIR/agent-1"
assert_exists "terminal lock survives alongside a cleaned agent lock" "$CCR_LOCK_DIR/terminal-live"
assert_contains "surviving terminal lock still blocks the update" "$out" "$SKIP_MARKER"

echo
echo "== Reboot flag placement =="

# If the flag lived inside the lock directory it would count as an active lock
# and block every future update permanently.
reset_locks
rm -rf "$CCR_STATE_DIR"
: > "$CCR_LOCK_DIR/../unrelated" 2>/dev/null || true
out="$(run_check)"
assert_contains "state directory is separate, update still runs" "$out" "$IDLE_MARKER"
assert_missing "no reboot flag inside the lock directory" "$CCR_LOCK_DIR/reboot-pending"

reset_locks
mkdir -p "$CCR_STATE_DIR"
: > "$CCR_STATE_DIR/reboot-pending"
out="$(run_check)"
assert_contains "an existing reboot-pending flag does not block updates" "$out" "$IDLE_MARKER"
assert_exists "reboot-pending flag is left in place" "$CCR_STATE_DIR/reboot-pending"

echo
echo "== agent-run wrapper =="

reset_locks
rc=0
# shellcheck disable=SC2016  # must expand in the child shell, not here
agent-run bash -c 'ls "$CCR_LOCK_DIR"/agent-* > /tmp/seen-lock' || rc=$?
assert_ok "agent-run exits 0 for a successful command" "$rc"
assert_eq "a lock existed while the command was running" "1" "$(wc -l < /tmp/seen-lock)"
assert_eq "no agent lock is left behind afterwards" "0" "$(find "$CCR_LOCK_DIR" -name 'agent-*' | wc -l)"

reset_locks
rc=0
agent-run bash -c 'exit 3' || rc=$?
assert_eq "agent-run propagates the command's exit code" "3" "$rc"
assert_eq "lock is removed even when the command fails" "0" "$(find "$CCR_LOCK_DIR" -name 'agent-*' | wc -l)"

# The reason this wrapper does not use `exec`: exec would replace the shell and
# discard the EXIT trap, leaking the lock on every single run.
reset_locks
agent-run sleep 30 &
wrapper=$!
sleep 1
kill -TERM "$wrapper" 2>/dev/null || true
wait "$wrapper" 2>/dev/null || true
sleep 1
assert_eq "lock is removed when the wrapper is terminated" "0" "$(find "$CCR_LOCK_DIR" -name 'agent-*' | wc -l)"

rc=0
agent-run || rc=$?
assert_fails "agent-run without a command exits non-zero" "$rc"

echo
echo "Results: ${PASS} passed, ${FAIL} failed."
[[ "$FAIL" -eq 0 ]]
