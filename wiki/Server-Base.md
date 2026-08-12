# Server Base

> **Status:** not implemented yet — planned for Phase 1. `server-provisioning/`
> is currently an empty directory; there is no target server. This page
> describes the intended design, not a working system.

The server base is the hardened Arch Linux host that everything else in
ClaudeCode Remote sits on: the agent stack, the idle update system, the
terminal daemon, the file shares. Phase 1 is only about getting this
foundation right — locking down SSH, separating privileges, and putting the
box on a private network — before any agent or daemon code touches it.

Binding detail lives in
[`docs/ARCHITECTURE.md`](https://github.com/Elias02345/remote-agent/blob/main/docs/ARCHITECTURE.md)
Section 2. This page explains the *why* behind the choices.

---

> [!WARNING]
> **Before the SSH hardening step runs, you need your own public Ed25519 key
> ready.** The provisioning script is planned to disable password login and
> restrict `AllowUsers` to `admin` and `agent` — if your key isn't already in
> `admin`'s `authorized_keys` at that point, you lock yourself out of the
> server. This is tracked as an open item in
> [`TODO_FOR_USER.md`](https://github.com/Elias02345/remote-agent/blob/main/TODO_FOR_USER.md)
> on GitHub — check it before running provisioning on real hardware.

---

## Two users, not one

Provisioning will create two separate accounts instead of doing everything as
a single user:

- **`admin`** — the human's SSH login, for server administration.
- **`agent`** — runs every coding agent and every terminal session. No agent
  is ever planned to run as root, and it never shares an identity with the
  admin login.

The reason for the split is containment, not convenience: a coding agent
executes commands you didn't type, sometimes based on instructions embedded
in files or output it processed. If that session were the same account used
for SSH administration, a bad outcome there would come with full
administrative reach. Keeping `agent` separate means a compromised or
misbehaving agent session is confined to what `agent` can do — which, by
design, is very little outside its own sandbox.

What `agent` *can* do outside that sandbox is limited to a fixed, named
allowlist: `/etc/sudoers.d/agent` is planned to grant `NOPASSWD` only for
specific commands (e.g. restarting the terminal daemon service), never a
blanket `sudo` grant. There is no path from "agent runs a command" to
"agent runs arbitrary commands as root."

## SSH hardening

The intended `/etc/ssh/sshd_config` key points, reproduced from the
architecture doc:

```
PasswordAuthentication no
PermitRootLogin no
PubkeyAuthentication yes
AllowUsers admin agent
Port 2222
```

Plus:

- **`fail2ban`** with an sshd jail, to blunt brute-force attempts against the
  (non-default) port.
- **One Ed25519 key pair per authorized device**, never a shared key. This
  matters operationally, not just cryptographically: if a laptop is lost or
  a device is retired, its specific key is removed from `authorized_keys`
  without touching anyone else's access. A shared key would mean rotating
  everyone's access to revoke one device.

## Why there is no port forwarding on the router

Password auth is off and keys are hardened, but the design goes a step
further and doesn't expose SSH (or anything else) to the internet via router
port forwarding at all. Partly this is defense in depth — no forwarded port
means no internet-facing attack surface to harden in the first place. Partly
it's a practical constraint: many residential connections in Germany sit
behind Carrier-Grade NAT, where port forwarding on your own router
accomplishes nothing because inbound traffic never reaches it (see
[Networking and CloudGate](Networking-and-CloudGate) for how the app reaches
the server despite this).

## Tailscale overlay network (D-01)

Instead of open ports, the server and every client device join a
[Tailscale](Glossary) mesh — decision D-01 in
[Roadmap and Decisions](Roadmap-and-Decisions). SSH and the terminal daemon
are planned to bind exclusively to the Tailscale interface.

What this buys, concretely:

- **Transport encryption is already solved.** Tailscale runs on WireGuard, so
  every connection between mesh members is encrypted end-to-end without the
  app doing anything.
- **NAT traversal happens automatically**, including through CGNAT, without
  manual router configuration.
- **Per-device ACLs** let you grant or immediately revoke a device's mesh
  access independent of anything at the application layer.

This is why the app itself never needs to build its own PKI: the overlay
network already provides transport-level identity and encryption between
trusted devices. The application layer only has to handle device
*authorization* on top (see [Security Model](Security-Model)), not the
transport security underneath it.

## GitHub authentication for the server

The server needs its own GitHub identity to pull and push project code,
separate from any human's GitHub credentials:

- A **machine-bound Ed25519 key**, registered as an account-level SSH key on
  GitHub (not a per-repo deploy key, since the server manages multiple
  projects).
- `~/.ssh/config` sets `IdentitiesOnly yes`, so this key can't leak into
  other SSH connections the server makes.
- `ForwardAgent no` — the key is never forwarded to anything the server
  connects out to. It **stays physically on the server**, which matches the
  project's core premise that the server holds all the code and does all the
  work; nothing needs to borrow the server's GitHub identity remotely.

## D-02: bare metal or Proxmox VM

Whether the server ends up as a dedicated machine or a VM on an existing
Proxmox host is left open by design — the provisioning scripts are meant to
stay agnostic to which. The only difference it makes is operational: a VM
lets you take a snapshot before a risky update, which is a nice safety net
for the [idle update system](Idle-Update-System) but not something the
provisioning logic depends on either way.

## What Phase 1 will deliver

- Provisioning scripts under `server-provisioning/` that take a fresh Arch
  install to the state described above: user separation, sudo whitelist, SSH
  hardening, Tailscale join, GitHub key setup.
- **Idempotency**: running the scripts again on an already-provisioned server
  must not break it.
- A **container-based Arch test harness**, because no real target server
  exists yet. The harness runs the provisioning scripts against a fresh Arch
  container twice and requires both runs to succeed — proving the scripts
  are idempotent without needing real hardware.

See [Roadmap and Decisions](Roadmap-and-Decisions) for the full Phase 1
definition of done.
