# Security Model

> **Status:** implemented (Phase 7), and deliberately unable to finish a pairing.
> Argon2id, TOTP, Ed25519 device auth, step-up and rate limiting all ship and pass
> under `-race`. The **passkey factor cannot be satisfied** while the WebAuthn
> relying-party domain is undecided, so the three-factor chain refuses to complete.
> That is the fail-closed behaviour it was built for, not a gap.

This page covers how a device gets trusted in the first place, how it's
trusted day to day afterward, and what happens when an already-trusted
device wants to do something with real consequences.

Binding detail lives in
[`docs/ARCHITECTURE.md`](https://github.com/Elias02345/remote-agent/blob/main/docs/ARCHITECTURE.md)
Section 10. This page explains the *why*.

---

## Three situations, three different friction levels

Requiring email+password *and* TOTP *and* passkey on every single action
would be unusable — nobody wants to clear a three-factor gate before typing a
command in a terminal. The design instead splits into three situations that
deliberately carry different friction:

| Situation | Frequency | Requirement |
|---|---|---|
| **Device pairing** | Rare — this is where a new device gets permanent access | Full three-factor chain: password + TOTP + passkey |
| **Ongoing access** from an already-paired device | Constant — every day, every reconnect | Ed25519 challenge-response only, no password |
| **Step-up auth** for sensitive single actions | Occasional | A fresh passkey prompt |

The important line to hold onto: **the three-factor chain is never used for
day-to-day operation.** It exists only at the two rare, high-consequence
moments — granting a device permanent access, and specific sensitive
actions — not on every terminal open or every reconnect.

### Why stack three factors when a passkey alone is already strong

The architecture doc is explicit about this, and it's worth reproducing the
reasoning rather than just the conclusion: a passkey by itself is already a
multi-factor proof — possession of the device plus a biometric or PIN to
release it — and is phishing-resistant because it's bound to the origin
domain. Stacking password, TOTP, and passkey on top of each other goes
*beyond* common current best practice; most modern systems replace
password+2FA with a passkey rather than combining both.

The choice to combine them here anyway is deliberate, not an oversight: for
a self-hosted system with very few, known users, where the thing being
protected is full server access, code, and backups, the redundancy is judged
worth the friction — but **only** because it stays confined to pairing and
to genuinely sensitive step-up actions, and never leaks into everyday
terminal use.

## Owner login portal (pairing flow, D-05 — confirmed)

Decision **D-05** in [Roadmap and Decisions](Roadmap-and-Decisions) settles
where this flow lives: a separate `/owner` route in the daemon itself, not a
module bolted onto CloudGate. This keeps CloudGate unchanged and the two
projects cleanly separated. Unlike most entries in the decisions table, D-05
is marked confirmed by the owner rather than a default assumption, and is
meant to be reopened only on explicit request.

The pairing sequence, in order:

1. **Email + password.** The password is stored server-side hashed with
   **Argon2id** — chosen over bcrypt as the current recommended standard,
   more resistant to GPU-assisted cracking attempts. Rate limiting applies
   from the start: after 5 failed attempts, lockout time increases
   exponentially, tracked **per IP and per account** simultaneously. There is
   no unlimited-attempts fallback anywhere in this chain.
2. **TOTP code** (RFC 6238). The secret is stored encrypted at rest;
   verification goes through a standard library (`pquerna/otp` in Go), so it
   works with any common authenticator app.
3. **Passkey ceremony** (WebAuthn), server-side via `go-webauthn/webauthn`,
   client-side via the browser's native WebAuthn API or the app's platform
   bridges (below).
4. Only **after all three steps succeed** does the device's Ed25519 key —
   generated during pairing-code exchange — actually get written into the
   `devices` table described in [Terminal Daemon](Terminal-Daemon). Before
   that point, the device sits in a "waiting for approval" state and has no
   access at all.

## Device pairing mechanics (Section 5.4)

The underlying possession factor for day-to-day access, separate from the
three-factor chain above:

1. On first install, the app generates an Ed25519 key pair **locally** —
   Android Keystore, Windows Credential Manager, Linux Secret Service, or (on
   web only) a short-lived session key, since browser storage is treated as
   less trustworthy than a platform keystore.
2. The app displays a pairing code.
3. The pairing code, run through the owner-login flow above, registers the
   device's public key server-side.
4. From then on, **every connection** signs a random challenge issued by the
   daemon — the Ed25519 private key never leaves the device, and no secret
   ever crosses the wire.
5. Any device can be individually revoked at any time (`revoked=1` in the
   `devices` table), immediately cutting its access without touching any
   other paired device.

Since the daemon only listens on the Tailscale interface to begin with (see
[Server Base](Server-Base) and [Networking and CloudGate](Networking-and-CloudGate)),
this device-level check is a genuinely independent second layer: being on the
network alone is not sufficient for terminal access.

## Passkeys in Flutter: platform reality

Flutter has no first-party passkey API; support goes through community
plugins that pass through to each platform's native mechanism:

| Platform | Integration | Maturity |
|---|---|---|
| Android | Credential Manager API (stable since Android 14) | good |
| Windows | Windows Hello / WebAuthn platform authenticator | good |
| Web | native browser WebAuthn API | good, standard case |
| Linux Desktop | no mature native platform passkey API | weakest link |

For Linux specifically, the recommendation is to **not** rely on a purely
platform-integrated passkey experience, and instead support a **FIDO2
hardware key** (e.g. a YubiKey over USB/NFC) as a roaming authenticator. It
works identically via WebAuthn across all four platforms, which closes the
Linux gap without waiting on a still-immature software backend — and it
doubles as a hardware second factor on any platform, not just Linux.

## Security overview

Reproduced from `docs/ARCHITECTURE.md` §10.4:

| Layer | Measure |
|---|---|
| Transport (mesh) | Tailscale/WireGuard for own devices, no open port on the home network |
| Transport (internet) | CloudGate as an outbound-initiated relay tunnel, fixed domain, Let's Encrypt TLS |
| SSH | Keys only, no password, dedicated port, fail2ban, separate users for admin/agent |
| Device pairing | Email+password (Argon2id) + TOTP + Passkey/WebAuthn, then Ed25519 device key |
| Ongoing access | Ed25519 challenge-response per connection, no repeated password needed |
| Sensitive individual actions | Step-up passkey prompt (revoke device, new pairing, delete backup) |
| File shares | SMB3 encrypted, exclusively on the Tailscale interface, never public |
| File uploads | SHA-256 hash before sending and after receiving, chunked/resumable (tus) |
| Agents | Never run as root, wrapper script instead of manually managed locks |
| Updates | Only when demonstrably idle, kernel reboots never automatic |
| GitHub | Machine-bound key, no agent forwarding |

## D-07: no reduced tier for "trusted networks"

**Decision D-07** in [Roadmap and Decisions](Roadmap-and-Decisions): the full
three-factor chain applies to **every** device pairing, without exception —
including a new device joining from the owner's own home network. The
architecture doc raised this as an open question (whether a "trusted
network" should get a lighter pairing tier); the answer chosen is no reduced
tier at all. Pairing stays equally strict everywhere, because the value of
the redundancy from Section 10.1 depends on it not having quiet exceptions.

## Standing guardrails (from `CLAUDE.md`)

These aren't specific to Phase 7 — they're project-wide rules that the
security model above has to fit inside, restated here because they bound
every design choice on this page:

- Nothing runs as root except designated systemd units.
- No secrets in Git — ever, not even placeholders that look real.
- No endpoint accepts arbitrary shell commands. The file-browser "server
  actions" described in Section 8.4 (trigger a backup, view update status,
  manage devices) are implemented as a fixed set of narrow, predefined
  actions over the same REST layer as terminal management — never free-form
  command execution from the app.
- Every listener binds to `localhost` or the Tailscale interface by default
  — never `0.0.0.0` (see [Networking and CloudGate](Networking-and-CloudGate)
  for how CloudGate still reaches it without that).
- Rate limiting is built in from the start (see the login flow above), not
  retrofitted after an incident.

---

See [Roadmap and Decisions](Roadmap-and-Decisions) for the Phase 7 definition
of done and the full D-05/D-07 decision records.
