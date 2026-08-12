#!/usr/bin/env bash
# server-provisioning/test/run-tests.sh
#
# Container-based test harness for provision.sh (see docs/ARCHITECTURE.md
# Section 2 and ROADMAP.md Phase 1). Builds the Arch test image, then runs a
# fixed set of negative and positive cases against it, printing [PASS]/[FAIL]
# per assertion. Every assertion runs regardless of earlier failures so a
# single invocation gives the full picture; the script exits non-zero if any
# assertion failed.
#
# Assumptions made about provision.sh / lib/common.sh that the Phase 1
# contract does not pin down (see the final report for the full list):
#   - CCR_ADMIN_USER and CCR_AGENT_USER get standard useradd home
#     directories, i.e. /home/<user>.
#   - The failure message for a missing CCR_ADMIN_PUBKEY contains the
#     substring "locked out" somewhere in stdout/stderr (case-insensitive).
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROV_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
IMAGE_TAG="ccr-provision-test"
CONTAINER_NAME="ccr-provision-test-run"

# Fixed dummy key for the positive run. The filler is arbitrary
# base64-alphabet text -- provision.sh only checks the "ssh-ed25519 " prefix
# per the Phase 1 contract, it does not validate real key material.
DUMMY_KEY="ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIatestatestatestatestatestatest ccr-test"
RSA_KEY="ssh-rsa AAAAB3..."

# --- pick a container runtime -----------------------------------------------

if command -v podman >/dev/null 2>&1; then
  RUNTIME="podman"
elif command -v docker >/dev/null 2>&1; then
  RUNTIME="docker"
else
  echo "[FATAL] neither podman nor docker found in PATH. Install one to run this harness." >&2
  exit 1
fi

# --- assertion helpers -------------------------------------------------------

PASS=0
FAIL=0

# run_case <outvar> <rcvar> -- <command...>
# Runs a command, capturing combined stdout+stderr into <outvar> and its exit
# code into <rcvar>, without tripping `set -e` on a non-zero exit -- a
# non-zero exit is an expected outcome for several cases here, not a script
# bug.
#
# Every read of an <outvar>/<rcvar> below carries a "shellcheck disable=
# SC2154" directive: shellcheck cannot trace assignment through printf -v's
# dynamic variable name, so it sees each one as read-before-assigned. That is
# a false positive, not a real bug -- run_case always assigns both before
# returning.
run_case() {
  local __outvar="$1" __rcvar="$2"
  shift 2
  local __out __rc
  if __out="$("$@" 2>&1)"; then
    __rc=0
  else
    __rc=$?
  fi
  printf -v "$__outvar" '%s' "$__out"
  printf -v "$__rcvar" '%s' "$__rc"
}

assert_ok() {
  local desc="$1" rc="$2"
  if [[ "$rc" -eq 0 ]]; then
    echo "[PASS] ${desc}"
    PASS=$((PASS + 1))
  else
    echo "[FAIL] ${desc} (exit code ${rc})"
    FAIL=$((FAIL + 1))
  fi
}

assert_fails() {
  local desc="$1" rc="$2"
  if [[ "$rc" -ne 0 ]]; then
    echo "[PASS] ${desc}"
    PASS=$((PASS + 1))
  else
    echo "[FAIL] ${desc} (expected non-zero exit, got 0)"
    FAIL=$((FAIL + 1))
  fi
}

assert_eq() {
  local desc="$1" expected="$2" actual="$3"
  if [[ "$expected" == "$actual" ]]; then
    echo "[PASS] ${desc}"
    PASS=$((PASS + 1))
  else
    echo "[FAIL] ${desc} (expected [${expected}], got [${actual}])"
    FAIL=$((FAIL + 1))
  fi
}

assert_contains() {
  local desc="$1" haystack="$2" needle="$3"
  if [[ "$haystack" == *"$needle"* ]]; then
    echo "[PASS] ${desc}"
    PASS=$((PASS + 1))
  else
    echo "[FAIL] ${desc} (expected to find '${needle}')"
    FAIL=$((FAIL + 1))
  fi
}

# --- setup / teardown ---------------------------------------------------------

# shellcheck disable=SC2317
# False positive: shellcheck only sees `trap cleanup EXIT` below, not a
# direct call, so it thinks this whole function body is unreachable. The
# directive on a function definition applies to its entire body.
cleanup() {
  "$RUNTIME" rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "Building test image with ${RUNTIME}..."
"$RUNTIME" build -t "$IMAGE_TAG" -f "${SCRIPT_DIR}/Containerfile" "${PROV_DIR}"

# --- negative tests: prove the guards work ------------------------------------

echo
echo "== Negative tests =="

run_case out1 rc1 "$RUNTIME" run --rm -v "${PROV_DIR}:/repo:ro" "$IMAGE_TAG" bash /repo/provision.sh
# shellcheck disable=SC2154
assert_fails "run without CCR_ADMIN_PUBKEY exits non-zero" "$rc1"
# shellcheck disable=SC2154
assert_contains "missing-key message mentions being locked out" "${out1,,}" "locked out"

run_case out2 rc2 "$RUNTIME" run --rm -e "CCR_ADMIN_PUBKEY=${RSA_KEY}" -v "${PROV_DIR}:/repo:ro" "$IMAGE_TAG" bash /repo/provision.sh
# shellcheck disable=SC2154
assert_fails "run with an ssh-rsa key exits non-zero (Ed25519 only)" "$rc2"

# CCR_ADMIN_PASSWORD_HASH must be a crypt hash, never a plaintext password --
# usermod -p writes the value verbatim into /etc/shadow, so accepting
# plaintext here would store and use it as the literal (broken) hash.
run_case out_hashneg rc_hashneg "$RUNTIME" run --rm -e "CCR_ADMIN_PUBKEY=${DUMMY_KEY}" -e "CCR_ADMIN_PASSWORD_HASH=hunter2" -e CCR_SKIP_TAILSCALE=1 -v "${PROV_DIR}:/repo:ro" "$IMAGE_TAG" bash /repo/provision.sh
# shellcheck disable=SC2154
assert_fails "run with a plaintext CCR_ADMIN_PASSWORD_HASH exits non-zero" "$rc_hashneg"

# --- positive test: optional CCR_ADMIN_PASSWORD_HASH with a real crypt hash --

echo
echo "== Positive test: valid admin password hash =="

run_case out_hashgen rc_hashgen "$RUNTIME" run --rm "$IMAGE_TAG" openssl passwd -6 ccr-test-password
# shellcheck disable=SC2154
assert_ok "generated a test password hash with openssl passwd -6" "$rc_hashgen"
# shellcheck disable=SC2154
HASH6="$out_hashgen"
# shellcheck disable=SC2016
# Single quotes are deliberate: '$6$' is a literal pattern to search for,
# not a variable reference -- there is no variable named "6".
assert_contains "generated hash uses the \$6\$ (sha512crypt) format" "$HASH6" '$6$'

run_case out_hashrun rc_hashrun "$RUNTIME" run --rm -e "CCR_ADMIN_PUBKEY=${DUMMY_KEY}" -e "CCR_ADMIN_PASSWORD_HASH=${HASH6}" -e CCR_SKIP_TAILSCALE=1 -v "${PROV_DIR}:/repo:ro" "$IMAGE_TAG" bash /repo/provision.sh
# shellcheck disable=SC2154
assert_ok "run with a valid \$6\$ CCR_ADMIN_PASSWORD_HASH exits 0" "$rc_hashrun"

# --- positive tests: run twice in the SAME container for the idempotency proof

echo
echo "== Positive run (idempotency) =="

"$RUNTIME" rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true
"$RUNTIME" run -d --name "$CONTAINER_NAME" -v "${PROV_DIR}:/repo:ro" "$IMAGE_TAG" sleep infinity >/dev/null

run_case out3 rc3 "$RUNTIME" exec -e "CCR_ADMIN_PUBKEY=${DUMMY_KEY}" -e CCR_SKIP_TAILSCALE=1 "$CONTAINER_NAME" bash /repo/provision.sh
# shellcheck disable=SC2154
assert_ok "first provisioning run exits 0" "$rc3"

run_case fp1 fp1_rc "$RUNTIME" exec "$CONTAINER_NAME" ssh-keygen -lf /home/agent/.ssh/id_ed25519

run_case out4 rc4 "$RUNTIME" exec -e "CCR_ADMIN_PUBKEY=${DUMMY_KEY}" -e CCR_SKIP_TAILSCALE=1 "$CONTAINER_NAME" bash /repo/provision.sh
# shellcheck disable=SC2154
assert_ok "second provisioning run (same container) exits 0" "$rc4"

run_case fp2 fp2_rc "$RUNTIME" exec "$CONTAINER_NAME" ssh-keygen -lf /home/agent/.ssh/id_ed25519

echo
echo "== Post-run state (after two runs) =="

run_case out_port rc_port "$RUNTIME" exec "$CONTAINER_NAME" bash -c "grep -c '^Port ' /etc/ssh/sshd_config"
# shellcheck disable=SC2154
assert_eq "exactly one active Port line in sshd_config" "1" "$out_port"

run_case out_allow rc_allow "$RUNTIME" exec "$CONTAINER_NAME" bash -c "grep -c '^AllowUsers ' /etc/ssh/sshd_config"
# shellcheck disable=SC2154
assert_eq "exactly one active AllowUsers line in sshd_config" "1" "$out_allow"

run_case out_pwauth rc_pwauth "$RUNTIME" exec "$CONTAINER_NAME" bash -c "grep -c '^PasswordAuthentication ' /etc/ssh/sshd_config"
# shellcheck disable=SC2154
assert_eq "exactly one active PasswordAuthentication line in sshd_config" "1" "$out_pwauth"

run_case out_sshdt rc_sshdt "$RUNTIME" exec "$CONTAINER_NAME" sshd -t
# shellcheck disable=SC2154
assert_ok "sshd -t passes against the resulting config" "$rc_sshdt"

run_case out_authkeys rc_authkeys "$RUNTIME" exec "$CONTAINER_NAME" bash -c "grep -cxF '${DUMMY_KEY}' /home/admin/.ssh/authorized_keys"
# shellcheck disable=SC2154
assert_eq "admin authorized_keys contains the dummy key exactly once" "1" "$out_authkeys"

run_case out_visudo rc_visudo "$RUNTIME" exec "$CONTAINER_NAME" visudo -cf /etc/sudoers.d/agent
# shellcheck disable=SC2154
assert_ok "visudo -cf passes for /etc/sudoers.d/agent" "$rc_visudo"

run_case out_mode rc_mode "$RUNTIME" exec "$CONTAINER_NAME" stat -c '%a' /etc/sudoers.d/agent
# shellcheck disable=SC2154
assert_eq "/etc/sudoers.d/agent has mode 0440" "440" "$out_mode"

# grep finding the string is the FAILURE condition here, so we assert on the
# grep command itself failing (i.e. not finding an unrestricted grant).
run_case out_wideall rc_wideall "$RUNTIME" exec "$CONTAINER_NAME" bash -c "grep -F 'ALL=(ALL)' /etc/sudoers.d/agent"
# shellcheck disable=SC2154
assert_fails "sudoers file for agent has no unrestricted ALL=(ALL) grant" "$rc_wideall"

run_case out_idadmin rc_idadmin "$RUNTIME" exec "$CONTAINER_NAME" id admin
# shellcheck disable=SC2154
assert_ok "admin user exists" "$rc_idadmin"

run_case out_idagent rc_idagent "$RUNTIME" exec "$CONTAINER_NAME" id agent
# shellcheck disable=SC2154
assert_ok "agent user exists" "$rc_idagent"

run_case out_agentkey rc_agentkey "$RUNTIME" exec "$CONTAINER_NAME" test -f /home/agent/.ssh/id_ed25519
# shellcheck disable=SC2154
assert_ok "agent has ~/.ssh/id_ed25519" "$rc_agentkey"

run_case out_agentcfg rc_agentcfg "$RUNTIME" exec "$CONTAINER_NAME" test -f /home/agent/.ssh/config
# shellcheck disable=SC2154
assert_ok "agent has ~/.ssh/config" "$rc_agentcfg"

# shellcheck disable=SC2154
assert_ok "captured agent key fingerprint after first run" "$fp1_rc"
# shellcheck disable=SC2154
assert_ok "captured agent key fingerprint after second run" "$fp2_rc"
# shellcheck disable=SC2154
assert_eq "agent id_ed25519 fingerprint unchanged across both runs" "$fp1" "$fp2"

run_case out_origexists rc_origexists "$RUNTIME" exec "$CONTAINER_NAME" test -f /etc/ssh/sshd_config.ccr-orig
# shellcheck disable=SC2154
assert_ok "/etc/ssh/sshd_config.ccr-orig backup exists" "$rc_origexists"

run_case out_origdiffers rc_origdiffers "$RUNTIME" exec "$CONTAINER_NAME" bash -c "! diff -q /etc/ssh/sshd_config.ccr-orig /etc/ssh/sshd_config >/dev/null"
# shellcheck disable=SC2154
assert_ok "sshd_config.ccr-orig differs from the hardened config" "$rc_origdiffers"

# --- admin sudo (step_admin_sudo): wheel membership + password-gated drop-in --

run_case out_admin_visudo rc_admin_visudo "$RUNTIME" exec "$CONTAINER_NAME" visudo -cf /etc/sudoers.d/admin
# shellcheck disable=SC2154
assert_ok "visudo -cf passes for /etc/sudoers.d/admin" "$rc_admin_visudo"

run_case out_admin_mode rc_admin_mode "$RUNTIME" exec "$CONTAINER_NAME" stat -c '%a' /etc/sudoers.d/admin
# shellcheck disable=SC2154
assert_eq "/etc/sudoers.d/admin has mode 0440" "440" "$out_admin_mode"

# This is the point of the whole step_admin_sudo drop-in: admin's sudo grant
# must be password-gated. SSH access is key-only, so a NOPASSWD grant here
# would turn a single stolen private key into instant, silent root -- the
# exact shortcut this drop-in exists to avoid. grep finding NOPASSWD is the
# FAILURE condition, so we assert on the grep command itself failing.
run_case out_admin_nopasswd rc_admin_nopasswd "$RUNTIME" exec "$CONTAINER_NAME" bash -c "grep -F NOPASSWD /etc/sudoers.d/admin"
# shellcheck disable=SC2154
assert_fails "admin sudoers drop-in has no NOPASSWD -- a stolen SSH key must not be instant root" "$rc_admin_nopasswd"

run_case out_wheel rc_wheel "$RUNTIME" exec "$CONTAINER_NAME" bash -c "id -nG admin | tr ' ' '\n' | grep -c '^wheel\$'"
# shellcheck disable=SC2154
assert_eq "admin is a member of wheel exactly once after two runs (no duplicate membership)" "1" "$out_wheel"

# --- summary -------------------------------------------------------------------

echo
echo "Results: ${PASS} passed, ${FAIL} failed."

if [[ "$FAIL" -gt 0 ]]; then
  exit 1
fi
exit 0
