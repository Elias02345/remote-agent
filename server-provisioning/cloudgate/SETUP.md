# CloudGate setup

How ClaudeCode Remote becomes reachable from the internet, per architecture
Section 9.

> **Status: automatable, per-installation.** The domain (decision D-04) is no
> longer something the project waits on — every installation configures its
> own via `CCR_PUBLIC_DOMAIN` (see `.env.example`), and `register-host.sh`
> automates the CloudGate side described below instead of clicking through
> the web UI by hand. See `TODO_FOR_USER.md` for the one-time setup steps.

## What this does and does not build

This repo builds **no relay infrastructure**. CloudGate is an existing separate
project that orchestrates Cloudflare Tunnel (`cloudflared`); the publicly
reachable node is Cloudflare's edge network, not a server you run and not a
rented VPS. Everything here is configuration and verification around that.

Behind CGNAT, port forwarding on the router does nothing — inbound traffic
never reaches the router in the first place. The tunnel works because the
server holds an *outbound* connection open.

## Before you start

- [ ] **Set `CCR_PUBLIC_DOMAIN`, once, in `server-provisioning/.env`.**
      WebAuthn binds every passkey to this value as its relying-party ID,
      which must be a real domain and never an IP. Changing it later
      invalidates every registered passkey. This project ships no default —
      it is decision D-04, and it is per-installation, not a value anyone
      else can pick for you.
- [ ] Domain active in Cloudflare (nameservers pointing at Cloudflare).
- [ ] Cloudflare API token with `Zone:DNS:Edit` and
      `Account:Cloudflare Tunnel:Edit`, stored in the CloudGate web UI. **Not
      in this repo.**
- [ ] CloudGate running (the owner's existing instance) with a cloudflared
      tunnel and the zone for your domain already added — `register-host.sh`
      registers *this* service as a host against them, it does not create the
      tunnel or the Cloudflare account.

## Where CloudGate runs decides the target address

This is the one thing that is easy to get wrong, because the address is
resolved **from CloudGate's point of view**, not from the agent machine's.

| CloudGate runs… | Target in the host entry | Daemon must bind to |
|---|---|---|
| on the same machine | `http://127.0.0.1:8080` | `127.0.0.1:8080` (default) |
| **on another machine** (the usual case) | `http://<this machine's Tailscale or LAN IP>:8080` | that same address |

If CloudGate is elsewhere and the entry says `127.0.0.1`, CloudGate resolves
that to **its own** loopback and never reaches the daemon. The symptom is a
tunnel that looks configured and a service that looks dead, which sends people
debugging the wrong box.

Both halves have to agree: set `CCR_BIND_ADDR` in
`/etc/claudecode-remote/.env` to the same address you register, and restart
`claudecode-remoted`. The daemon refuses a wildcard bind, so `0.0.0.0` is not
an option — it has no authentication in front of it beyond device pairing, and
binding it to everything would expose it on every interface the machine has.

The scheme is **`http://`**, not `https://`. TLS terminates at Cloudflare's
edge; the daemon speaks plaintext and holds no certificate.

## Host entries

`register-host.sh` automates this against CloudGate's real API (`/api/hosts`,
`/api/tunnels`, `/api/cloudflare/accounts`) — see its header comment for the
exact endpoints. It is idempotent: re-running it updates the existing host
instead of creating a duplicate.

```bash
CLOUDGATE_API_KEY=cgk_... bash server-provisioning/cloudgate/register-host.sh \
  https://your-cloudgate-instance remote.your-domain.example 127.0.0.1:8080
```

`CLOUDGATE_API_KEY` comes from the CloudGate web UI (Settings > API Keys) and
must stay out of shell history / process listings — that is why the script
only accepts it as an environment variable, never a flag.

If you would rather do it by hand, add the same host in the CloudGate web UI.
**Tunnel mode, not the local nginx mode** — the nginx mode assumes a reachable
public IP, which CGNAT denies.

| Subdomain | Internal target | Purpose |
|---|---|---|
| `remote.<your-domain>` | `127.0.0.1:8080` | terminal daemon, file API, owner login |

One subdomain is enough: the daemon serves the terminal API, the file API and
the `/owner` route from the same listener, and every additional public hostname
is another name to keep valid for WebAuthn.

## Why the daemon needs no certificate

TLS terminates at Cloudflare's edge. `cloudflared` forwards to plaintext HTTP
on `127.0.0.1`, so the daemon does no certificate handling at all — which is
why it is started without any TLS flags and refuses to bind to a wildcard
address.

That refusal is load-bearing here: if the daemon bound to `0.0.0.0`, it would
be reachable both through the tunnel *and* directly on the LAN, and the second
path has no authentication in front of it until Phase 7.

## Verify it

```bash
bash server-provisioning/cloudgate/verify-tunnel.sh remote.your-domain.example
```

It checks that HTTPS resolves with a valid certificate, that the response comes
from the daemon rather than a CloudGate error page, that a **WebSocket upgrade
survives the tunnel** — the part that plain HTTPS success says nothing about,
since a proxy stripping `Upgrade` headers looks perfectly healthy — and that
the daemon is *not* also reachable directly on port 8080.

## Known gap: CloudGate's own updates

CloudGate polls for its own updates every 6 hours and may reload `cloudflared`,
briefly interrupting tunnelled connections. It has no lock mechanism to
coordinate with this project's idle locks. Decision D-06: this is accepted
rather than worked around. Sessions survive it — tmux does not care that a
client's transport blinked, and the app reconnects with backoff — so the cost
is a momentary disconnect banner, not lost work.

## Tailscale is still the better path for your own devices

Cloudflare Tunnel is for the web client, for foreign networks, and for the
public HTTPS origin WebAuthn requires. For your own devices already in the
tailnet, a direct Tailscale connection is lower-latency and does not leave your
network. Both exist on purpose; neither replaces the other.
