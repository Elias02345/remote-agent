# ClaudeCode Remote

ClaudeCode Remote gives coding agents (Claude Code, Codex, Antigravity CLI) a
persistent home on your own hardened Arch Linux server, and lets you reach
their terminal sessions from Android, Windows, Linux, and Web. A terminal
session survives SSH drops, app restarts, and switching devices mid-task,
because [tmux](Glossary) is the actual persistence layer — the daemon
in this project only adds a structured API, sync, and auth on top of it. You
open a terminal once and it just keeps running until you explicitly close it.

## Current status

> **Phases 0-5, 7 and 8 are built and covered by CI.** Phase 6 (public access)
> is blocked on the WebAuthn domain, and Phase 9 (Android) is in progress.
> Nothing has run on a real server yet — no target machine exists. See
> [Roadmap and Decisions](Roadmap-and-Decisions).

## Start here

| You want to... | Go to |
|---|---|
| Understand the overall design and why it looks the way it does | [Architecture Overview](Architecture-Overview) |
| See how the server gets provisioned and hardened | [Server Base](Server-Base) |
| Understand how Claude Code / Codex / Antigravity are installed and share context | [Agent Stack](Agent-Stack) |
| Know how the server updates itself without breaking a running agent | [Idle Update System](Idle-Update-System) |
| Understand how terminal sessions actually stay alive and render correctly | [Terminal Daemon](Terminal-Daemon) |
| See how file exchange and backups work | [Files and Backups](Files-and-Backups) |
| Understand how the app reaches the server despite CGNAT | [Networking and CloudGate](Networking-and-CloudGate) |
| Understand pairing, passkeys, and step-up auth | [Security Model](Security-Model) |
| See what's planned, in what order, and what's still undecided | [Roadmap and Decisions](Roadmap-and-Decisions) |
| Look up an unfamiliar term | [Glossary](Glossary) |

## Design decisions you should know before reading anything else

- **tmux is the persistence layer, not custom code.** The daemon does not
  reimplement PTY session management; it attaches to and detaches from tmux
  sessions.
- **Raw PTY passthrough, no server-side terminal interpretation.** The
  daemon forwards bytes; a real terminal emulator on the client
  (`xterm.dart` / `xterm.js`) does all the rendering.
- **No own relay infrastructure.** Public reachability behind CGNAT goes
  through [CloudGate](Glossary) and Cloudflare Tunnel, not a
  self-run relay server.
- **SHA-256, not MD5**, for file transfer integrity checks — deliberately
  chosen over the cheaper option for a security-focused project.
- **Three-factor auth is for pairing and step-up only, never for daily
  use.** Everyday terminal access uses a lightweight Ed25519
  challenge-response; email+password+TOTP+passkey only gates new-device
  pairing and sensitive actions.

## Source of truth

The binding architecture decision lives in
[`docs/ARCHITECTURE.md`](https://github.com/Elias02345/remote-agent/blob/main/docs/ARCHITECTURE.md)
in the repo. This wiki explains and orients; it does not replace that
document. Phase status and decisions live in
[`ROADMAP.md`](https://github.com/Elias02345/remote-agent/blob/main/ROADMAP.md).
