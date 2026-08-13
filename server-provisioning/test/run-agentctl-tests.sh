#!/usr/bin/env bash
# server-provisioning/test/run-agentctl-tests.sh
#
# Container harness for agentctl (docs/ARCHITECTURE.md Section 3.2, ROADMAP.md
# Phase 2). Builds the same Arch image run-tests.sh uses, then executes the
# assertions in agentctl-cases.sh inside it.
#
# The assertions live in their own script rather than in this file because the
# Git-hook cases need real repositories, real branch switches and real merges;
# driving that through one container `exec` per step adds latency and noise
# without adding coverage.
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
echo "Running agentctl cases..."
"$RUNTIME" run --rm -v "${PROV_DIR}:/repo:ro" "$IMAGE_TAG" \
  bash /repo/test/agentctl-cases.sh
