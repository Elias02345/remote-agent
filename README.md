# ClaudeCode Remote

Persistent terminal sessions for coding agents (Claude Code, Codex,
Antigravity) on your own Arch Linux server — reachable from Android, Windows,
Linux, and Web. Sessions survive connection drops and device switches because
tmux is the persistence layer and the daemon passes raw terminal bytes
through unchanged.

> **Status: Phase 0 (setup) done.** No code runs yet — Phases 1–9 build the
> system step by step. See [`ROADMAP.md`](ROADMAP.md).

## Components

| Directory | Content | Tech |
|---|---|---|
| [`server-provisioning/`](server-provisioning/) | Hardening the Arch server, user separation, Tailscale, systemd units, idle updater, backups, Samba | Bash + systemd |
| [`daemon/`](daemon/) | Terminal session daemon, file API, device identity | Go |
| [`app/`](app/) | Client for Android / Windows / Linux / Web | Flutter + `xterm.dart` |
| [`wiki/`](wiki/) | Source of the GitHub wiki | Markdown |

Public HTTPS access behind CGNAT runs through
[CloudGate](https://github.com/Elias02345/CloudGate) in tunnel mode — this
repo builds **no** own relay infrastructure.

## Documentation

- **[Wiki](https://github.com/Elias02345/remote-agent/wiki)** — operations
  and background docs, the entry point for humans.
- **[`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md)** — binding architecture
  decision. Changes only after consultation.
- **[`docs/ARCHITECTURE.de.md`](docs/ARCHITECTURE.de.md)** — German original.
- **[`ROADMAP.md`](ROADMAP.md)** — phase status and decisions made on open
  questions.
- **[`TODO_FOR_USER.md`](TODO_FOR_USER.md)** — what only the human can do.
- **[`CLAUDE.md`](CLAUDE.md)** — conventions for agent sessions in this repo.

## License

Not yet decided.
