#!/usr/bin/env bash
# server-provisioning/test/run-idle-update-tests.sh
#
# Container harness for the idle update system (docs/ARCHITECTURE.md Section 4,
# ROADMAP.md Phase 3). Builds the same Arch image the other harnesses use, then
# runs the assertions in idle-update-cases.sh inside it.
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
echo "Running idle update cases..."
"$RUNTIME" run --rm -v "${PROV_DIR}:/repo:ro" "$IMAGE_TAG" \
  bash /repo/test/idle-update-cases.sh
