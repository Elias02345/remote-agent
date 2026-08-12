# CLAUDE.md — Project Conventions ClaudeCode Remote

This file is the work order for every future agent session (Claude Code,
Codex, Antigravity) in this repo. It extends the global `~/.claude/CLAUDE.md`.

## Source of truth

`docs/ARCHITECTURE.md` is the complete, already-agreed architecture decision.
Every technical decision in it is **binding** — do not renegotiate it, do not
replace it with a "better idea", unless the user explicitly asks for that.
Changes to `docs/ARCHITECTURE.md` only after consultation.

`ROADMAP.md` tracks phase status and all decisions made on open questions.
`TODO_FOR_USER.md` collects everything only the human can do.

## Role

Lead developer for a multi-component system:

| Component | Directory | Tech |
|---|---|---|
| Arch Linux server as agent base station | `server-provisioning/` | Bash, systemd |
| Terminal session and file daemon | `daemon/` | Go |
| Client app Android/Windows/Linux/Web | `app/` | Flutter, `xterm.dart` |
| Public HTTPS access behind CGNAT | — | CloudGate (separate project) |

## Non-negotiable decisions

1. **Terminal transport**: raw PTY passthrough over `tmux attach-session`,
   byte-for-byte over WebSocket. **No** tmux Control Mode (`-CC`) — that is an
   iTerm2-specific, structured protocol and unsuitable for a generic client.
2. **Terminal emulation** exclusively client-side (`xterm.dart` / `xterm.js`).
   Never interpret, parse, or simplify it server-side.
3. **The persistence layer is tmux itself**, no custom PTY session management.
   `set -g window-size latest`, so the session adapts to the most recently
   active device.
4. **Two lock types with different lifetimes**, never mixed:
   - Agent locks (`agent-<pid>`): automatic via shell wrapper with
     `trap EXIT`, stale cleanup after 6 h.
   - Terminal locks (`terminal-<session-id>`): unlimited, only manual closing
     removes them. No stale cleanup.
5. **Backups are not coupled to the idle-lock logic** — they run independently
   and more frequently, because they do nothing destructive. Only
   system/package updates wait for idle.
6. **File hashes: SHA-256** (not MD5). Verification before sending AND after
   receiving, plus a comparison against the server-side computed hash on the
   client.
7. **CloudGate connection**: no own relay server. CloudGate is used as a
   finished building block via a host entry in **tunnel mode**. TLS
   terminates at Cloudflare's edge; the daemon listens internally on
   plaintext HTTP, bound to `localhost` or the Tailscale interface.
8. **Three-factor auth** (email+password + TOTP + passkey) applies only to
   device pairing and security-critical single actions (step-up) — never to
   day-to-day operation of already-paired devices. `Ed25519` challenge-response
   is sufficient for that.
9. **SMB/NFS shares** exclusively over the Tailscale interface, never over
   CloudGate or the public internet.
10. **Linux passkey gap**: plan for a FIDO2 hardware key (`YubiKey` or similar)
    as a fallback.

## Security guardrails (always apply, phase-independent)

- No process runs as root except the systemd units explicitly designated for it.
- No secrets (API tokens, password hashes, private keys) in Git.
  `.gitignore` + `.env.example` instead of a real `.env` from the start.
- No endpoint accepts arbitrary shell commands from the app — only the
  narrowly specified actions from Section 8.4 of the architecture doc.
- Every new network listener binds to `localhost` or the Tailscale interface
  by default, **never** to `0.0.0.0`, unless explicitly designated as a
  CloudGate host.
- Rate limiting and lockout on all auth endpoints from the start, not as an
  add-on.

## What will not be done

- No custom terminal emulation/interpretation in the daemon, "just to make it
  simpler".
- No automatically triggering kernel updates or reboots, not even on idle —
  only set the `reboot-pending` flag.
- No falling back to MD5 as the sole integrity check without a comment in the
  code or commit text.
- No own relay/tunnel infrastructure in parallel to CloudGate.
- No silent shortcuts in the three-factor chain for pairing, not even "just
  for testing". Test environments get their own, clearly marked test
  configuration instead of weakened production logic.

## Way of working

- **One phase at a time** (see `ROADMAP.md`), not everything started in
  parallel. Every phase ends in an independently runnable/testable state.
- Small, described commits per logical step. No monster commits per phase.
- Every component has its own `README.md`: purpose, local setup, running tests.
- Everything only the human can do (Cloudflare API token, domain, buying a
  FIDO2 key, SSH access to the real server, registering the GitHub key) goes
  into `TODO_FOR_USER.md` instead of blocking or being simulated.
- Open questions from Section 12 of the architecture doc: pick the default
  assumption named there, document it in `ROADMAP.md`, explicitly mark it as
  revisable. **Never decide silently.**
- Tests alongside implementation, not after. For the daemon: unit tests for
  lock logic, hash verification, session state transitions. For security
  paths (auth, rate limiting) additionally negative tests (wrong password,
  expired challenge, reused pairing code).

## Model hierarchy in this project

Opus orchestrates, analyzes, and verifies. Sonnet implements. Haiku does
mechanics (renames, formatting). Subagents are advisory and deliver facts
with `file:line`; Opus spot-verifies against the real repo state before
anything builds on it.

## Repo structure

```
server-provisioning/   Bash/systemd for the Arch server
daemon/                Go daemon
  cmd/claudecode-remoted/
  internal/{terminal,files,identity,locks,db}/
app/                   Flutter client
wiki/                  source for the GitHub wiki (pushed to remote-agent.wiki)
docs/ARCHITECTURE.md   binding architecture
.github/workflows/     CI: lint, tests, build
```

## Sacred paths (updater/installer never touch these)

- `/var/lib/claudecode-remote/` — daemon's SQLite DB (`.db`, `-wal`, `-shm`)
- `/etc/claudecode-remote/.env` — secrets
- `/srv/exchange/`, `/srv/backups/` — user data
- `/run/claudecode-locks/terminal-*` — open terminal locks
