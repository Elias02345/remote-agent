# Architecture Overview

> **Status:** largely implemented. The server provisioning, the agent stack, the
> idle updater, the terminal daemon, the file API and the security layer all ship
> and are covered by CI. Outstanding: the CloudGate connection and the passkey
> factor, both blocked on the WebAuthn domain (D-04), and the Android client.
> See [Roadmap and Decisions](Roadmap-and-Decisions) for the per-phase state.

ClaudeCode Remote is three layers that only work because each one deliberately
does less than it could:

1. **Server base** — a hardened Arch Linux server that hosts the coding
   agents, the code, and an autonomous idle update system.
2. **Terminal session daemon** — the technical heart of the project. It
   manages persistent terminal sessions and exposes them over a
   WebSocket/REST API. This is where "sessions survive drops and device
   switches" actually gets implemented.
3. **Client apps** — Android, Windows, Linux, and Web apps that talk to the
   daemon and present terminals like browser tabs.

## Data flow

```
[Android/Windows/Linux/Web App]
        │  (WebSocket + REST, TLS, device auth)
        │  ── own devices in the Tailscale mesh: direct ──┐
        │  ── web client / foreign networks: via CloudGate ─┤
        ▼                                                    ▼
                    [Terminal session daemon on the server]
        │  (raw PTY passthrough via tmux attach-session)
        ▼
[tmux sessions] ── [Claude Code / Codex / Antigravity CLI] ── [Project code + Git]
        │
[Idle update system] ← checks locks from agent runs and open terminals
```

(Reproduced from [`docs/ARCHITECTURE.md`](https://github.com/Elias02345/remote-agent/blob/main/docs/ARCHITECTURE.md) §1.)

## The load-bearing decisions, and why

**tmux instead of custom PTY session management.** tmux has spent years
solving detach/reattach, scrollback, and process survival across dropped
connections. Reimplementing that in the daemon would mean rebuilding a
mature, battle-tested piece of software badly. The daemon's job shrinks to:
start/attach/list/close tmux sessions, and move bytes.

**Raw byte passthrough instead of tmux control mode (`-CC`).** An earlier
draft of the architecture assumed control mode. It doesn't work here:
`-CC` is a proprietary protocol built for iTerm2's pane-layout
synchronization, not a raw terminal stream. A full-screen TUI like Claude
Code — an Ink-based UI with its own alternate-screen buffer and redraw
cycle — would not render correctly through it. Instead, the daemon runs
`tmux attach-session -t <id>` inside its own PTY (`creack/pty` in Go) and
forwards the bytes unchanged, bidirectionally, over the WebSocket. No
parsing, no interpretation on the server.

**Terminal emulation lives client-side.** Because the daemon doesn't
interpret bytes, something has to. That's `xterm.dart` in the app and
`xterm.js` for a web test client — the same class of emulator that already
renders vim, htop, and tmux correctly in tools like ttyd, gotty, Upterm, and
Termius. This isn't a novel approach; it's the standard one for this
problem.

**Go for the daemon.** A mature PTY ecosystem (`creack/pty`), solid
WebSocket libraries (`gorilla/websocket`), a concurrency model that's a
natural fit for many parallel session streams, and a single static binary
with no runtime dependencies — which makes `systemd` deployment trivial and
matches this project's "no managed PaaS, everything self-hosted" stance.

**`set -g window-size latest` matters for multi-device use.** tmux's
default behavior shrinks a session to the smallest attached window — if the
same session is open on a phone and a desktop at once, the desktop's layout
would get squeezed to phone width. `window-size latest` instead makes the
session track whichever device most recently sent input; other attached
devices see a scaled snapshot rather than a distorted live layout. Without
this one option, multi-device access to the same session would actively
work against the point of the project.

## Component → directory → technology

| Component | Directory | Technology |
|---|---|---|
| Server hardening, users, Tailscale, systemd units, idle updater, backups, Samba | [`server-provisioning/`](https://github.com/Elias02345/remote-agent/tree/main/server-provisioning) | Bash + systemd |
| Terminal session daemon, file API, device identity | [`daemon/`](https://github.com/Elias02345/remote-agent/tree/main/daemon) | Go |
| Client app (Android / Windows / Linux / Web) | [`app/`](https://github.com/Elias02345/remote-agent/tree/main/app) | Flutter + `xterm.dart` |
| Public reachability behind CGNAT | external project | [CloudGate](https://github.com/Elias02345/CloudGate) + Cloudflare Tunnel |

## Where to go next

- Server setup and hardening: [Server Base](Server-Base)
- How the three coding agents are installed and share `CLAUDE.md`: [Agent Stack](Agent-Stack)
- The lock logic that keeps updates from killing a running agent: [Idle Update System](Idle-Update-System)
- The daemon's session model and API in detail: [Terminal Daemon](Terminal-Daemon)
- File exchange and backup design: [Files and Backups](Files-and-Backups)
- Reaching the server behind CGNAT: [Networking and CloudGate](Networking-and-CloudGate)
- Pairing, passkeys, and step-up auth: [Security Model](Security-Model)

For the complete binding detail behind every decision above, see
[`docs/ARCHITECTURE.md`](https://github.com/Elias02345/remote-agent/blob/main/docs/ARCHITECTURE.md).
