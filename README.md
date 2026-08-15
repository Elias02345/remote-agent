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
| 4 Terminal daemon MVP | done · rendering verified against a live session |
| 5 Files & backups | done · 20/20 plus a real restic restore |
| 6 CloudGate connection | scripted; needs a real deployment to prove |
| 7 Security layer | done · passkeys activate once a domain is configured |
| 8 Client app skeleton | done · 36 tests |
| 9 Android & polish | done · Kotlin share bridge outstanding |

Everything a machine can check runs in CI: shell lint, five container
harnesses, the Go suite with `-race`, and `flutter analyze` + `flutter test`.
One of those harnesses boots systemd as PID 1 and actually starts the units,
which is what found three bugs that only appear as a service — see the
changelog.

## Setting it up

Each installation configures its own domain — the project ships no default,
because a WebAuthn passkey is bound to the domain it was registered against.
Set `CCR_PUBLIC_DOMAIN`, generate the owner credentials with
`claudecode-remoted --setup-owner`, and `register-host.sh` adds the host to a
running CloudGate. Until those exist the passkey factor refuses, so a
half-configured server cannot pair a device. Details in
[`TODO_FOR_USER.md`](TODO_FOR_USER.md).

What still needs real hardware, and is honest about it: fail2ban actually
banning a remote client, Tailscale bringing up an interface, and a reboot. A
privileged container is not a machine.

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

[MIT](LICENSE) — permissive, no copyleft, compatible with the Go, Flutter and
Dart ecosystems this depends on, and the same licence as CloudGate.
