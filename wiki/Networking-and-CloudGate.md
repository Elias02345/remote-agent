# Networking and CloudGate

## The address CloudGate forwards to

The single most common way to get this wrong, and worth reading before
anything else on this page: **the target address is resolved from CloudGate's
point of view, not from the agent machine's.**

| CloudGate runs… | Host entry target | Daemon binds to |
|---|---|---|
| on the same machine | `http://127.0.0.1:8080` | `127.0.0.1:8080` |
| **on another machine** (the usual case) | `http://<agent machine's Tailscale IP>:8080` | that same address |

If CloudGate lives elsewhere and the entry says `127.0.0.1`, CloudGate
resolves that to *its own* loopback. Nothing answers. The symptom is a tunnel
that looks correctly configured sitting next to a daemon that looks dead —
which is how people end up debugging the wrong machine for an hour.

Both halves have to agree: `CCR_BIND_ADDR` in `/etc/claudecode-remote/.env`
must name the same address that is registered in CloudGate.
`register-host.sh` picks the Tailscale IP by default for exactly this reason,
and warns when it has to fall back to loopback.

The scheme is `http://`. TLS terminates at Cloudflare's edge; the daemon holds
no certificate and speaks plaintext on an interface that is not public.

---

> **Status:** documented and scripted, **not connected** (Phase 6). `SETUP.md` and
> `verify-tunnel.sh` exist; the latter is the Definition-of-Done check. Nothing is
> live, because the domain (decision D-04) has not been chosen — and that choice
> cannot be deferred past the passkey rollout without invalidating every passkey.

This page covers how the daemon is meant to become reachable from outside
the Tailscale mesh — a web browser, a phone on a foreign network — despite
the server having no usable public IP.

Binding detail lives in
[`docs/ARCHITECTURE.md`](https://github.com/Elias02345/remote-agent/blob/main/docs/ARCHITECTURE.md)
Section 9. This page explains the *why*.

---

## Why this is a problem at all: CGNAT

A "fixed IP" from a residential or mobile ISP contract — especially common
in Germany — is often still behind **Carrier-Grade NAT (CGNAT)**: the IP the
router shows isn't the actual publicly routable address, it's itself behind
another NAT layer operated by the ISP and shared across many customers.
Classic router port forwarding accomplishes nothing here, because inbound
traffic never reaches the router in the first place — there's no public port
to forward *to*. This is exactly the scenario CloudGate exists for.

## What CloudGate actually is

Worth stating precisely, because it's easy to get wrong: CloudGate is
**not** its own relay server that has to be operated. It is a web UI that
orchestrates **Cloudflare Tunnel** (`cloudflared`). The actual relay function
— a publicly reachable node the server holds an open outbound connection to
— is Cloudflare's own global edge network, not CloudGate itself and not any
VPS. **This project builds no relay infrastructure of its own.** The
mechanism is the same generic idea used elsewhere in this architecture
(outbound connection instead of inbound port), except the relay point
already exists, ready-made, and is operated by Cloudflare.

CloudGate itself runs as a Docker container on the server (bundled nginx on
80/443 locally, plus a Node/TypeScript backend on port 3000) and automates
the rest of the setup.

## Setup flow

1. A Cloudflare API token is stored in the CloudGate web UI once.
2. For each service to publish, only `internal-IP:port` and the desired
   subdomain are entered — e.g. `127.0.0.1:8080` → `remote.example.com`.
3. CloudGate creates the Cloudflare Tunnel, the DNS CNAME, and the ingress
   rule, and reloads `cloudflared` automatically — on the order of 30 seconds
   from entry to the service being live.

## Tunnel mode, not local nginx mode

CloudGate supports a hybrid mode: per host, either "via Cloudflare Tunnel" or
"local nginx reverse proxy." Local nginx mode needs a reachable public IP —
which is exactly what CGNAT denies. So every ClaudeCode Remote service is
planned to run in **tunnel mode**, without exception.

## Practical consequences

- **TLS terminates at Cloudflare's edge**, not on the server. The terminal
  daemon therefore never needs to manage its own certificate — it listens
  internally on plain HTTP, and `cloudflared` carries the connection through
  from the public HTTPS hostname down to it. This is a real simplification
  compared to the daemon having to run its own Let's Encrypt flow.
- Because of that, the daemon must listen on **`localhost` or the Tailscale
  interface — never `0.0.0.0`.** CloudGate reaches it via the internal
  `IP:port` configured during host setup; nothing about tunnel mode requires
  or benefits from the daemon binding to all interfaces.
- **WebSockets work over Cloudflare Tunnel** without special handling — this
  is an established, widely used pattern, including for terminal/remote
  tools comparable to this one. That said, for the owner's own devices, a
  direct Tailscale connection stays lower-latency, since it skips the
  Cloudflare hop entirely. CloudGate/tunnel mode is the path for the web
  client and for foreign networks — not necessarily the fastest path for
  everyday use from a device already in the mesh.

## Division of labour: Tailscale vs. CloudGate

The two aren't competing options — they cover different cases:

- **Tailscale** — the owner's own devices, and SSH administration. Lower
  latency, no public exposure, ACL-based revocation. See [Server Base](Server-Base).
- **CloudGate** — the web client (a browser can't join a WireGuard mesh, so
  it needs a real HTTPS endpoint regardless), access from foreign or
  restrictive networks where Tailscale isn't installed or doesn't reach, and
  the publicly reachable HTTPS origin that WebAuthn requires (next section).

## Fixed domain is mandatory for WebAuthn (Section 9.5)

Passkeys/WebAuthn bind every credential to a **relying-party ID**, which must
be a domain — never an IP address. CloudGate provides the domain mechanically
via its automatically created CNAME entries, but *which* subdomain gets used
for the owner login endpoint (e.g. `remote.example.com`) has to be settled
once and then left alone: changing the domain later invalidates every
already-registered passkey.

This is tracked as **decision D-04** in
[Roadmap and Decisions](Roadmap-and-Decisions) and is currently **open** —
the owner has explicitly deferred it, Phases 1–5 are built domain-agnostic on
purpose, and the decision is re-raised before Phase 6 starts. It is one of
only two decisions in this project (alongside D-05) that isn't freely
revisable later: D-04 can only change *before* the passkey rollout, since a
change after that point invalidates existing passkeys. It directly blocks
Phase 6.

## CloudGate's own update cycle (D-06)

CloudGate self-updates on its own schedule — polling GitHub every 6 hours,
GPG-signed, atomic rollback — entirely independent of this project's idle
lock logic (see [Idle Update System](Idle-Update-System)). That coupling was
considered and deliberately **not built** (decision D-06): a CloudGate
update potentially reloads `cloudflared` briefly, which could momentarily
affect an open tunnel connection, but the reload window is short and is
accepted as-is rather than retrofitted with a lock mechanism. It's noted as a
possible future feature in the CloudGate project itself, not a blocker here.

---

See [Security Model](Security-Model) for what the fixed domain enables (the
owner login portal and WebAuthn), and
[Roadmap and Decisions](Roadmap-and-Decisions) for the full Phase 6
definition of done and the D-04/D-06 decision records.
