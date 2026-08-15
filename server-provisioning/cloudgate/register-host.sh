#!/usr/bin/env bash
# register-host.sh — registers this daemon as a CloudGate host in tunnel
# mode (server-provisioning/cloudgate/SETUP.md), so the operator does not
# have to click through the CloudGate web UI by hand.
#
# CloudGate's host API (verified against the real source at
# github.com/Elias02345/CloudGate, packages/backend/src/routes/{hosts,
# tunnels,cloudflare}.ts and packages/shared/src/types/host.ts) needs a
# numeric tunnel_id and cf_zone_id, not just a hostname — this script
# resolves both from what CloudGate already has configured (the Cloudflare
# side is the owner's responsibility, see SETUP.md), so the only input a
# human supplies is the domain and the internal target.
#
# Usage:
#   CLOUDGATE_API_KEY=cgk_... bash register-host.sh <cloudgate-base-url> <public-domain> [internal-target]
#
# internal-target is the address CloudGate itself will connect to, and this is
# the part that is easy to get wrong. It is resolved from CloudGate's point of
# view, not from this machine's:
#
#   * CloudGate on THIS machine  -> 127.0.0.1:8080 is correct.
#   * CloudGate on ANOTHER machine (the normal case) -> 127.0.0.1 is CloudGate's
#     own loopback and can never reach this daemon. It must be an address of
#     this machine that CloudGate can route to: the Tailscale IP, or a LAN IP.
#
# When omitted, this script uses the Tailscale IP if there is one, because that
# is the address the architecture expects a remote CloudGate to use. It falls
# back to loopback only when no Tailscale interface exists, and says so.
#
# The daemon speaks plain HTTP — TLS terminates at Cloudflare's edge — so the
# CloudGate entry is http://, never https://.
#
# Environment:
#   CLOUDGATE_API_KEY      required. A CloudGate API key (web UI: Settings >
#                           API Keys), starts with "cgk_". Never pass this as
#                           a flag — flags are visible to every other process
#                           on the box via ps.
#   CLOUDGATE_TUNNEL_NAME   optional. Disambiguates which cloudflared tunnel
#                           to register against when more than one exists.
#
# Idempotent: if a host for <public-domain> already exists, it is updated
# in place (tunnel, zone, forward target) rather than duplicated.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source-path=SCRIPTDIR
# shellcheck source=../lib/common.sh
source "$SCRIPT_DIR/../lib/common.sh"

CLOUDGATE_URL="${1:-}"
DOMAIN="${2:-}"
API_KEY="${CLOUDGATE_API_KEY:-}"
CCR_PORT="${CCR_PORT:-8080}"

# default_target — the address CloudGate should forward to, chosen from
# CloudGate's perspective rather than this machine's. See the header comment.
default_target() {
  local ts_ip=""
  if command_exists tailscale; then
    ts_ip="$(tailscale ip -4 2>/dev/null | head -n1 || true)"
  fi
  if [[ -z "$ts_ip" ]] && command_exists ip; then
    ts_ip="$(ip -4 -o addr show dev tailscale0 2>/dev/null | awk '{print $4}' | cut -d/ -f1 | head -n1 || true)"
  fi

  if [[ -n "$ts_ip" ]]; then
    printf '%s:%s' "$ts_ip" "$CCR_PORT"
  else
    printf '127.0.0.1:%s' "$CCR_PORT"
  fi
}

TARGET="${3:-$(default_target)}"

if [[ -z "$CLOUDGATE_URL" || -z "$DOMAIN" ]]; then
  fail "Usage: $0 <cloudgate-base-url> <public-domain> [internal-target]  (internal-target defaults to this machine's Tailscale IP, or loopback if there is none). CLOUDGATE_API_KEY must be set in the environment."
fi
if [[ -z "$API_KEY" ]]; then
  fail "CLOUDGATE_API_KEY is not set. Create a key in the CloudGate web UI (Settings > API Keys) and export it — never pass it as a flag."
fi
if [[ "$DOMAIN" == *"://"* || "$DOMAIN" == *"/"* ]]; then
  fail "public-domain must be a bare hostname (e.g. remote.your-domain.example), not a URL: '${DOMAIN}'"
fi

command_exists curl || fail "curl is required"
command_exists jq || fail "jq is required (pacman -S jq / apt-get install jq)"

CLOUDGATE_URL="${CLOUDGATE_URL%/}"
DOMAIN="${DOMAIN,,}"
TARGET_HOST="${TARGET%:*}"
TARGET_PORT="${TARGET##*:}"
if [[ "$TARGET_HOST" == "$TARGET" || ! "$TARGET_PORT" =~ ^[1-9][0-9]{0,4}$ ]]; then
  fail "internal-target must be host:port with a numeric port, got '${TARGET}'"
fi

# Loopback is right only when CloudGate runs on this same machine. It is worth
# a loud warning rather than a silent success, because the failure it causes —
# CloudGate reaching its own loopback and getting nothing — looks like a broken
# tunnel or a dead daemon, and sends people debugging in the wrong place.
if [[ "$TARGET_HOST" == "127.0.0.1" || "$TARGET_HOST" == "localhost" || "$TARGET_HOST" == "::1" ]]; then
  warn "internal-target is loopback (${TARGET})."
  warn "That only works if CloudGate runs on THIS machine. If it runs elsewhere,"
  warn "pass this machine's Tailscale or LAN IP instead — from CloudGate's side,"
  warn "127.0.0.1 is CloudGate's own host and will never reach this daemon."
  warn "The daemon must also bind there: set CCR_BIND_ADDR in"
  warn "/etc/claudecode-remote/.env and restart claudecode-remoted."
fi

# --- API helper --------------------------------------------------------------

# cg_api <method> <path> [json-body] — calls the CloudGate API and prints the
# response body to stdout; fails (returns 1, does not exit) on anything
# outside the 2xx range so callers can decide whether to fall back or abort.
#
# Auth: "Authorization: Bearer cgk_..." — verified against
# middleware/auth.ts + middleware/api-key.ts, the same header shape as a
# browser session's JWT, no separate scheme to learn.
cg_api() {
  local method="$1" path="$2" body="${3:-}"
  local tmp status
  tmp="$(mktemp)"
  if [[ -n "$body" ]]; then
    status="$(curl -sS -o "$tmp" -w '%{http_code}' -X "$method" \
      -H "Authorization: Bearer ${API_KEY}" \
      -H "Content-Type: application/json" \
      -d "$body" \
      "${CLOUDGATE_URL}${path}")"
  else
    status="$(curl -sS -o "$tmp" -w '%{http_code}' -X "$method" \
      -H "Authorization: Bearer ${API_KEY}" \
      "${CLOUDGATE_URL}${path}")"
  fi
  if [[ "$status" -lt 200 || "$status" -ge 300 ]]; then
    err "CloudGate API ${method} ${path} -> HTTP ${status}"
    cat "$tmp" >&2
    rm -f "$tmp"
    return 1
  fi
  cat "$tmp"
  rm -f "$tmp"
}

# --- resolve the tunnel -------------------------------------------------------
# mode=cloudflare_tunnel (never local_nginx: that mode needs a reachable
# public IP, which CGNAT denies — SETUP.md) requires an existing tunnel's
# numeric row id, not the Cloudflare tunnel UUID.

log "Looking up the cloudflared tunnel to register against..."
tunnels_json="$(cg_api GET /api/tunnels)" || fail "could not list tunnels"

if [[ -n "${CLOUDGATE_TUNNEL_NAME:-}" ]]; then
  tunnel_id="$(jq -r --arg name "$CLOUDGATE_TUNNEL_NAME" \
    '[.tunnels[] | select(.provider=="cloudflared" and .name==$name)] | .[0].id // empty' <<<"$tunnels_json")"
  [[ -n "$tunnel_id" ]] || fail "No cloudflared tunnel named '${CLOUDGATE_TUNNEL_NAME}' found in CloudGate."
else
  tunnel_count="$(jq -r '[.tunnels[] | select(.provider=="cloudflared")] | length' <<<"$tunnels_json")"
  case "$tunnel_count" in
    0)
      fail "No cloudflared tunnel exists in CloudGate yet. Create one in the CloudGate web UI first (SETUP.md: tunnel mode)."
      ;;
    1)
      tunnel_id="$(jq -r '[.tunnels[] | select(.provider=="cloudflared")][0].id' <<<"$tunnels_json")"
      ;;
    *)
      names="$(jq -r '[.tunnels[] | select(.provider=="cloudflared") | .name] | join(", ")' <<<"$tunnels_json")"
      fail "Multiple cloudflared tunnels exist (${names}). Set CLOUDGATE_TUNNEL_NAME to pick one."
      ;;
  esac
fi
ok "Using tunnel id ${tunnel_id}"

# --- resolve the zone ----------------------------------------------------------
# The zone is looked up per Cloudflare account CloudGate knows about, picking
# the longest (most specific) zone name that is a suffix of the domain, so
# "remote.example.com" prefers a matching "example.com" zone correctly even
# if a shorter or unrelated zone also happens to match.

log "Looking up the Cloudflare zone for ${DOMAIN}..."
accounts_json="$(cg_api GET /api/cloudflare/accounts)" || fail "could not list Cloudflare accounts"
zone_id=""
while IFS= read -r account_id; do
  [[ -n "$account_id" ]] || continue
  zones_json="$(cg_api GET "/api/cloudflare/accounts/${account_id}/zones")" || continue
  match="$(jq -r --arg d "$DOMAIN" \
    '[.zones[] | select($d == .name or ($d | endswith("." + .name)))] | sort_by(.name | length) | last // empty | .id' \
    <<<"$zones_json")"
  if [[ -n "$match" ]]; then
    zone_id="$match"
    break
  fi
done < <(jq -r '.accounts[].id' <<<"$accounts_json")

[[ -n "$zone_id" ]] || fail "No Cloudflare zone known to CloudGate covers '${DOMAIN}'. Add/sync the Cloudflare account in the CloudGate web UI first."
ok "Using zone id ${zone_id}"

# --- create or update the host ------------------------------------------------

log "Checking for an existing host entry for ${DOMAIN}..."
hosts_json="$(cg_api GET /api/hosts)" || fail "could not list hosts"
existing_id="$(jq -r --arg d "$DOMAIN" '[.hosts[] | select(.hostname==$d)] | .[0].id // empty' <<<"$hosts_json")"

# Fields shared by create and update. hostname/mode/protocol are immutable
# once a host exists (hosts.ts PUT deliberately omits them — changing those
# is a delete+create in CloudGate's own model), so they are added only to
# the create body below.
common_body="$(jq -n \
  --arg forward_host "$TARGET_HOST" \
  --argjson forward_port "$TARGET_PORT" \
  --argjson tunnel_id "$tunnel_id" \
  --argjson cf_zone_id "$zone_id" \
  '{forward_scheme:"http", forward_host:$forward_host, forward_port:$forward_port,
    path_prefix:"/", tunnel_id:$tunnel_id, cf_zone_id:$cf_zone_id}')"

if [[ -n "$existing_id" ]]; then
  log "Host ${DOMAIN} already registered (id ${existing_id}); updating it in place."
  cg_api PUT "/api/hosts/${existing_id}" "$common_body" >/dev/null || fail "update failed"
  ok "Updated host ${DOMAIN} (id ${existing_id}), tunnel mode, forwarding to ${TARGET}"
else
  create_body="$(jq -n --arg hostname "$DOMAIN" --argjson common "$common_body" \
    '$common + {mode:"cloudflare_tunnel", protocol:"http", hostname:$hostname}')"
  log "Registering new host ${DOMAIN}."
  cg_api POST /api/hosts "$create_body" >/dev/null || fail "create failed"
  ok "Registered host ${DOMAIN}, tunnel mode, forwarding to ${TARGET}"
fi

log "Verify with: bash ${SCRIPT_DIR}/verify-tunnel.sh ${DOMAIN}"
