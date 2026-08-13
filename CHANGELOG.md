# Changelog

All notable changes to this project are recorded here. Dates are the day the
work landed on `main`.

## [Unreleased]

Everything below is built and covered by CI, but **nothing has run on a real
server yet** — no target machine exists. There is deliberately no release tag:
device pairing cannot complete until the WebAuthn domain is decided, so a
version number would claim more than is true. See `TODO_FOR_USER.md`.

### Server provisioning
- One-shot `provision.sh` for a fresh Arch host: `admin`/`agent` split, SSH
  hardened to keys-only on port 2222, fail2ban, Tailscale, and a machine-bound
  GitHub key. Idempotent, proven by a container harness that runs it twice.
- `install-agent-stack.sh` installs the coding agents and `agentctl`, which
  makes `CLAUDE.md` the single source of truth per project with `AGENTS.md` and
  `ANTIGRAVITY.md` as relative symlinks, kept in place by Git hooks.
- `install-idle-updater.sh` ships `agent-run` and the idle update check. Agent
  locks expire after 6 h; terminal locks never do. Kernel updates only raise a
  flag — reboots are never automatic.
- `install-files-backups.sh` configures Samba on the Tailscale interface only,
  and restic backups with a monthly restore test.

### Daemon
- Terminal sessions over raw PTY passthrough: `tmux attach-session` inside a
  PTY, bytes moved to the client unchanged. All emulation is client-side.
- File API restricted to an allowlist, with resumable `tus` uploads and
  SHA-256 verified before sending and after receiving.
- Security layer: Argon2id, TOTP with replay protection, Ed25519 device
  challenge-response, step-up grants for sensitive actions, and rate limiting
  per IP *and* per account.
- Refuses to start on a wildcard bind address.

### Client
- Flutter app for Linux, Windows and Web built against a transcribed design
  system, including a terminal palette whose sixteen ANSI colours were each
  checked for contrast and colour-vision separation.
- Mobile control bar with a latching Ctrl, resumable uploads, and share-to-
  session that offers a file rather than typing into a running agent.

### Known gaps
- No `app/android/` platform folder; `flutter create` has never been run.
- FIDO2 hardware keys are not implemented — a passkey binds to a domain, and
  that domain is undecided.
- Phase 4's visual check (does a full-screen TUI really render?) needs a human.
