#!/usr/bin/env bash
# server-provisioning/test/run-e2e-tests.sh
#
# End-to-end harness: boots a single Arch container with systemd as PID 1,
# runs the full install chain in it in the order a real machine would see it
# (provision.sh -> install-agent-stack.sh -> install-idle-updater.sh ->
# install-files-backups.sh), then asserts the things no other harness here
# can, because none of them have a running init system: units enabled AND
# started, a oneshot service run to completion via `systemctl start` with its
# journal read back afterward, sshd active under systemd, and a real restic
# snapshot produced by the timer's own service unit. It then builds and runs
# the daemon itself and drives a full session lifecycle over its REST API.
#
# The actual assertions live in e2e-cases.sh, run once inside the booted
# container -- same split as every other harness here, and for the same
# reason: everything after boot is local work inside one exec, not a pile of
# `docker exec` round trips from the host.
#
# Assumptions this harness cannot verify from inside a container and does not
# try to (see the final CI report for the full list): fail2ban's sshd jail
# actually depends on iptables to enforce a ban, and nothing in
# server-provisioning/ installs iptables -- if it is genuinely missing here,
# `systemctl enable --now fail2ban` inside provision.sh may or may not stay
# green depending on how fail2ban-server treats a failed jail's actionstart.
# Samba binds to `lo tailscale0` per smb.conf.template, and tailscale0 never
# exists here (CCR_SKIP_TAILSCALE=1) -- Samba is expected to just skip the
# interface it cannot find, not fail to start, but that has never been
# checked against a real smbd before this harness. Neither fail2ban nor
# Samba's own service state is asserted directly below; only that the
# scripts that call svc_enable_now on them still exit 0.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROV_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
DAEMON_DIR="$(cd "${PROV_DIR}/../daemon" && pwd)"
IMAGE_TAG="ccr-e2e-systemd-test"
CONTAINER_NAME="ccr-e2e-systemd-run"

if command -v podman >/dev/null 2>&1; then
  RUNTIME="podman"
elif command -v docker >/dev/null 2>&1; then
  RUNTIME="docker"
else
  echo "[FATAL] neither podman nor docker found in PATH. Install one to run this harness." >&2
  exit 1
fi

echo "Building systemd test image with ${RUNTIME}..."
"$RUNTIME" build -t "$IMAGE_TAG" -f "${SCRIPT_DIR}/Containerfile.systemd" "${SCRIPT_DIR}"

# shellcheck disable=SC2317
# False positive, same as the other harnesses: shellcheck only sees the
# `trap cleanup EXIT` below, not a direct call.
cleanup() {
  "$RUNTIME" rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true
}
trap cleanup EXIT

"$RUNTIME" rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true

echo
echo "Starting systemd container..."
# --privileged plus the cgroup bind mount is what makes systemd usable as
# PID 1 at all here -- these flags belong at run time, not baked into the
# image, per the container's own header. The three --tmpfs mounts are the
# standard "systemd in a container" recipe: /run and /run/lock need to be
# genuinely writable tmpfs for systemd's own runtime state, and /tmp is
# where every temporary file this harness creates below actually lives.
"$RUNTIME" run -d --name "$CONTAINER_NAME" \
  --privileged --cgroupns=host \
  -v /sys/fs/cgroup:/sys/fs/cgroup:rw \
  --tmpfs /tmp --tmpfs /run --tmpfs /run/lock \
  -v "${PROV_DIR}:/repo:ro" \
  -v "${DAEMON_DIR}:/daemon-src:ro" \
  "$IMAGE_TAG" >/dev/null

echo "Waiting for systemd to finish booting..."
tries=0
state=""
while true; do
  state="$("$RUNTIME" exec "$CONTAINER_NAME" systemctl is-system-running 2>/dev/null || true)"
  case "$state" in
    running | degraded) break ;;
  esac
  tries=$((tries + 1))
  if [[ "$tries" -ge 90 ]]; then
    echo "[FATAL] systemd inside the container never reached running/degraded state (last seen: ${state:-<unreachable>})." >&2
    echo "[FATAL] Failed units:" >&2
    "$RUNTIME" exec "$CONTAINER_NAME" systemctl list-units --failed --no-pager || true
    exit 1
  fi
  sleep 1
done
# "degraded" is an accepted outcome, not just "running": a minimal boot
# target inside a container commonly has one or two units that can never
# succeed here (e.g. anything expecting a real console), and that is not
# what this harness exists to catch.
echo "systemd reached state: ${state}"

echo
echo "Running e2e cases..."
"$RUNTIME" exec "$CONTAINER_NAME" bash /repo/test/e2e-cases.sh
