#!/usr/bin/env bash
# server-provisioning/test/run-backup-tests.sh
#
# Container harness for the Samba configuration and the restic backup/restore
# cycle (docs/ARCHITECTURE.md Section 7, ROADMAP.md Phase 5).
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROV_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
IMAGE_TAG="ccr-provision-test"

if command -v podman >/dev/null 2>&1; then
  RUNTIME="podman"
elif command -v docker >/dev/null 2>&1; then
  RUNTIME="docker"
else
  echo "[FATAL] neither podman nor docker found in PATH. Install one to run this harness." >&2
  exit 1
fi

echo "Building test image with ${RUNTIME}..."
"$RUNTIME" build -t "$IMAGE_TAG" -f "${SCRIPT_DIR}/Containerfile" "${PROV_DIR}"

echo
echo "Running backup and Samba cases..."
"$RUNTIME" run --rm -v "${PROV_DIR}:/repo:ro" "$IMAGE_TAG" \
  bash /repo/test/backup-cases.sh
