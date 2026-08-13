#!/usr/bin/env bash
# server-provisioning/test/agentctl-cases.sh
#
# The actual agentctl assertions. Runs INSIDE the test container, started by
# run-agentctl-tests.sh. Kept as its own file rather than a pile of container
# `exec` calls from outside: the Git-hook cases need a real repository with
# real branch switches and merges, and orchestrating that across a container
# boundary one command at a time buys nothing but noise.
#
# All work happens under /tmp, never in the bind-mounted /repo: that mount is
# read-only, and Git refuses to operate on a tree owned by a different uid
# ("dubious ownership").
set -Eeuo pipefail

PASS=0
FAIL=0

pass() { echo "[PASS] $1"; PASS=$((PASS + 1)); }
fails() { echo "[FAIL] $1"; FAIL=$((FAIL + 1)); }

assert_ok()      { if [[ "$2" -eq 0 ]]; then pass "$1"; else fails "$1 (exit ${2})"; fi; }
assert_fails()   { if [[ "$2" -ne 0 ]]; then pass "$1"; else fails "$1 (expected non-zero exit, got 0)"; fi; }
assert_eq()      { if [[ "$2" == "$3" ]]; then pass "$1"; else fails "$1 (expected [${2}], got [${3}])"; fi; }
assert_file()    { if [[ -f "$2" ]]; then pass "$1"; else fails "$1 (missing: ${2})"; fi; }
assert_symlink() { if [[ -L "$2" ]]; then pass "$1"; else fails "$1 (not a symlink: ${2})"; fi; }
assert_exec()    { if [[ -x "$2" ]]; then pass "$1"; else fails "$1 (not executable: ${2})"; fi; }

# rc_of <command...> — runs a command, prints its exit code, never trips set -e.
rc_of() {
  local rc=0
  "$@" >/dev/null 2>&1 || rc=$?
  echo "$rc"
}

# --- setup -------------------------------------------------------------------

install -m 0755 /repo/agentctl /usr/local/bin/agentctl
git config --global user.email "ccr-test@example.invalid"
git config --global user.name "ccr test"
git config --global init.defaultBranch main

echo
echo "== Symlink behaviour =="

WORK=/tmp/case-basic
mkdir -p "$WORK"
echo "marker-alpha" > "$WORK/CLAUDE.md"

rc=$(rc_of agentctl init "$WORK")
assert_ok "init in a directory containing CLAUDE.md exits 0" "$rc"
assert_symlink "AGENTS.md is a symlink" "$WORK/AGENTS.md"
assert_symlink "ANTIGRAVITY.md is a symlink" "$WORK/ANTIGRAVITY.md"
assert_eq "AGENTS.md reads through to CLAUDE.md content" "marker-alpha" "$(cat "$WORK/AGENTS.md")"
assert_eq "ANTIGRAVITY.md reads through to CLAUDE.md content" "marker-alpha" "$(cat "$WORK/ANTIGRAVITY.md")"

# A relative target is the whole point: an absolute one breaks the moment the
# repo is moved or cloned to a different path.
assert_eq "AGENTS.md target is relative, not absolute" "CLAUDE.md" "$(readlink "$WORK/AGENTS.md")"
assert_eq "ANTIGRAVITY.md target is relative, not absolute" "CLAUDE.md" "$(readlink "$WORK/ANTIGRAVITY.md")"

rc=$(rc_of agentctl init "$WORK")
assert_ok "second init exits 0 (idempotent)" "$rc"
assert_eq "AGENTS.md target unchanged after second init" "CLAUDE.md" "$(readlink "$WORK/AGENTS.md")"

WORK_EMPTY=/tmp/case-no-claude
mkdir -p "$WORK_EMPTY"
rc=$(rc_of agentctl init "$WORK_EMPTY")
assert_fails "init without CLAUDE.md exits non-zero" "$rc"

# The single most important assertion in this file: a regular AGENTS.md is
# someone's real content. Replacing it with a symlink would destroy work
# silently, so agentctl must refuse and leave the bytes exactly as they were.
WORK_CLASH=/tmp/case-regular-file
mkdir -p "$WORK_CLASH"
echo "canonical" > "$WORK_CLASH/CLAUDE.md"
printf 'hand written agents notes\n' > "$WORK_CLASH/AGENTS.md"
before="$(sha256sum < "$WORK_CLASH/AGENTS.md")"
rc=$(rc_of agentctl init "$WORK_CLASH")
after="$(sha256sum < "$WORK_CLASH/AGENTS.md")"
assert_fails "init exits non-zero when AGENTS.md is a regular file" "$rc"
assert_eq "pre-existing regular AGENTS.md is preserved byte-for-byte" "$before" "$after"

echo
echo "== Git hooks (Phase 2 Definition of Done) =="

REPO=/tmp/case-repo
mkdir -p "$REPO"
git -C "$REPO" init -q
echo "repo conventions" > "$REPO/CLAUDE.md"
git -C "$REPO" add CLAUDE.md
git -C "$REPO" commit -qm "add CLAUDE.md"

rc=$(rc_of agentctl init "$REPO")
assert_ok "init inside a Git work tree exits 0" "$rc"
assert_file "post-checkout hook installed" "$REPO/.git/hooks/post-checkout"
assert_file "post-merge hook installed" "$REPO/.git/hooks/post-merge"
assert_exec "post-checkout hook is executable" "$REPO/.git/hooks/post-checkout"
assert_exec "post-merge hook is executable" "$REPO/.git/hooks/post-merge"

# Real branch switch, not a hand-invoked hook: this is what the Definition of
# Done actually claims.
git -C "$REPO" checkout -q -b feature
rm -f "$REPO/AGENTS.md"
git -C "$REPO" checkout -q main
assert_symlink "post-checkout restored AGENTS.md after a real branch switch" "$REPO/AGENTS.md"

git -C "$REPO" checkout -q feature
echo "work" > "$REPO/feature-file"
git -C "$REPO" add feature-file
git -C "$REPO" commit -qm "feature work"
git -C "$REPO" checkout -q main
rm -f "$REPO/AGENTS.md"
git -C "$REPO" merge -q --no-ff -m "merge feature" feature
assert_symlink "post-merge restored AGENTS.md after a real merge" "$REPO/AGENTS.md"

# An unmarked hook belongs to the operator. Overwriting it would silently break
# whatever it did.
REPO_FOREIGN=/tmp/case-foreign-hook
mkdir -p "$REPO_FOREIGN"
git -C "$REPO_FOREIGN" init -q
echo "conventions" > "$REPO_FOREIGN/CLAUDE.md"
printf '#!/bin/sh\necho operator hook\n' > "$REPO_FOREIGN/.git/hooks/post-checkout"
chmod 0755 "$REPO_FOREIGN/.git/hooks/post-checkout"
foreign_before="$(sha256sum < "$REPO_FOREIGN/.git/hooks/post-checkout")"
rc=$(rc_of agentctl init "$REPO_FOREIGN")
foreign_after="$(sha256sum < "$REPO_FOREIGN/.git/hooks/post-checkout")"
assert_ok "init does not fail because of a foreign hook" "$rc"
assert_eq "foreign post-checkout hook is preserved byte-for-byte" "$foreign_before" "$foreign_after"

NOT_A_REPO=/tmp/case-not-a-repo
mkdir -p "$NOT_A_REPO"
rc=$(rc_of agentctl install-hooks "$NOT_A_REPO")
assert_ok "install-hooks outside a Git work tree exits 0" "$rc"

echo
echo "Results: ${PASS} passed, ${FAIL} failed."
[[ "$FAIL" -eq 0 ]]
