#!/usr/bin/env bash
# verify-tunnel.sh — checks that the daemon is actually reachable through the
# CloudGate/Cloudflare tunnel (docs/ARCHITECTURE.md Section 9, ROADMAP.md
# Phase 6 Definition of Done).
#
# This is the DoD check for Phase 6 and can only be run once the domain is
# decided (D-04), CloudGate has the host entry, and the daemon is running.
#
# Usage: bash verify-tunnel.sh remote.your-domain.example
set -Eeuo pipefail

HOST="${1:-${CCR_PUBLIC_HOST:-}}"
if [[ -z "$HOST" ]]; then
  echo "Usage: $0 <public-hostname>" >&2
  echo "   or: CCR_PUBLIC_HOST=remote.your-domain.example $0" >&2
  exit 1
fi

PASS=0
FAIL=0
pass()  { echo "[PASS] $1"; PASS=$((PASS + 1)); }
fails() { echo "[FAIL] $1"; FAIL=$((FAIL + 1)); }

command -v curl >/dev/null 2>&1 || { echo "[FATAL] curl is required" >&2; exit 1; }

echo "Verifying https://${HOST}"
echo

# 1. HTTPS with a certificate that actually validates. Cloudflare terminates
# TLS at its edge, so a certificate error here means the tunnel or the DNS
# record is wrong, not that the daemon is broken.
if curl -fsS --max-time 15 "https://${HOST}/health" >/dev/null 2>&1; then
  pass "HTTPS health endpoint responds with a valid certificate"
else
  fails "HTTPS health endpoint did not respond (tunnel, DNS or daemon down)"
fi

# 2. It must be the daemon behind it, not CloudGate's own error page.
body="$(curl -fsS --max-time 15 "https://${HOST}/health" 2>/dev/null || true)"
if [[ "$body" == *'"status":"ok"'* ]]; then
  pass "the health endpoint is served by the daemon itself"
else
  fails "unexpected health response: ${body:-<empty>}"
fi

# 3. The WebSocket upgrade is the part that actually matters: plain HTTPS
# working proves nothing about whether the terminal stream survives the tunnel,
# and a proxy that silently drops Upgrade headers looks perfectly healthy.
#
# This probes /health/ws, not a session route. A session stream requires a
# single-use WebSocket ticket, which requires a paired device — which the
# operator does not have at the point they are verifying the tunnel. Probing
# /sessions/<id>/stream therefore returns 401 before the handshake is even
# attempted, so it proves nothing about Upgrade headers; the daemon exposes
# /health/ws for exactly this check instead.
key="$(head -c 16 /dev/urandom | base64)"
status="$(curl -s -o /dev/null -w '%{http_code}' --max-time 15 \
  -H "Connection: Upgrade" \
  -H "Upgrade: websocket" \
  -H "Sec-WebSocket-Version: 13" \
  -H "Sec-WebSocket-Key: ${key}" \
  "https://${HOST}/health/ws" 2>/dev/null || echo "000")"

case "$status" in
  101) pass "WebSocket upgrade accepted through the tunnel (101)" ;;
  000) fails "no response to the WebSocket upgrade attempt" ;;
  404) fails "/health/ws is missing — the daemon behind the tunnel is older than this script" ;;
  *)   fails "WebSocket upgrade returned ${status}; a proxy may be stripping Upgrade headers" ;;
esac

# 4. Session streams must NOT be reachable without a ticket. This is the other
# half of the check above: /health/ws is open by design, and that is only
# acceptable while the real streams stay shut.
session_status="$(curl -s -o /dev/null -w '%{http_code}' --max-time 15 \
  -H "Connection: Upgrade" \
  -H "Upgrade: websocket" \
  -H "Sec-WebSocket-Version: 13" \
  -H "Sec-WebSocket-Key: ${key}" \
  "https://${HOST}/sessions/probe/stream" 2>/dev/null || echo "000")"

if [[ "$session_status" == "401" ]]; then
  pass "session streams refuse an unticketed upgrade (401)"
else
  fails "unticketed session stream returned ${session_status}, expected 401"
fi

# 5. The daemon must not be reachable on a public port directly. If it is, the
# tunnel is not the only way in and the localhost-only guarantee is fiction.
if curl -fsS --max-time 5 "http://${HOST}:8080/health" >/dev/null 2>&1; then
  fails "the daemon answers directly on port 8080 — it must only be reachable through the tunnel"
else
  pass "the daemon is not exposed directly on port 8080"
fi

echo
echo "Results: ${PASS} passed, ${FAIL} failed."
[[ "$FAIL" -eq 0 ]]
