# ClaudeCode Remote

Persistent terminal sessions for coding agents (Claude Code, Codex,
Antigravity) on your own Arch Linux server — reachable from Android, Windows,
Linux, and Web. Sessions survive connection drops and device switches because
tmux is the persistence layer and the daemon passes raw terminal bytes
through unchanged.

## Status

| Phase | State |
|---|---|
| 0 Repo, architecture, wiki | done |
| 1 Server foundation | done · 30/30 harness assertions in CI |
| 2 Agent stack | done · 22/22 |
| 3 Idle update system | done · 21/21 |
| 4 Terminal daemon MVP | done in code; the visual check needs a human |
| 5 Files & backups | done · 20/20 plus a real restic restore |
| 6 CloudGate connection | **blocked on the WebAuthn domain (D-04)** |
| 7 Security layer | done; the passkey factor is blocked on D-04 |
| 8 Client app skeleton | done |
| 9 Android & polish | in progress |

Everything a machine can check runs in CI: shell lint, four container harnesses,
the Go suite with `-race`, and `flutter analyze` + `flutter test`.

Three things need a human and are listed in
[`TODO_FOR_USER.md`](TODO_FOR_USER.md): a target server to verify Phase 1–3
against, the owner credentials (`--setup-owner`), and **the WebAuthn domain**,
which blocks pairing from completing at all. That last one is deliberate —
choosing it wrong later invalidates every registered passkey, so the chain
refuses rather than guessing.

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
