# CloudGate setup

How ClaudeCode Remote becomes reachable from the internet, per architecture
Section 9.

> **Status: not completed.** The steps below are written and the verification
> script exists, but the domain (decision D-04) has not been chosen yet, so
> nothing has actually been wired up. See `TODO_FOR_USER.md`.

## What this does and does not build

This repo builds **no relay infrastructure**. CloudGate is an existing separate
project that orchestrates Cloudflare Tunnel (`cloudflared`); the publicly
reachable node is Cloudflare's edge network, not a server you run and not a
rented VPS. Everything here is configuration and verification around that.

Behind CGNAT, port forwarding on the router does nothing — inbound traffic
never reaches the router in the first place. The tunnel works because the
server holds an *outbound* connection open.

## Before you start

- [ ] **Decide the domain, once.** WebAuthn binds every passkey to a
      relying-party ID, which must be a domain and never an IP. Changing it
      later invalidates every registered passkey. This is decision D-04 and it
      blocks the rest of this page.
- [ ] Domain active in Cloudflare (nameservers pointing at Cloudflare).
- [ ] Cloudflare API token with `Zone:DNS:Edit` and
      `Account:Cloudflare Tunnel:Edit`, stored in the CloudGate web UI. **Not
      in this repo.**
- [ ] CloudGate running on the same machine as the daemon.

## Host entries

Add these in the CloudGate web UI. **Tunnel mode, not the local nginx mode** —
the nginx mode assumes a reachable public IP, which CGNAT denies.

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
