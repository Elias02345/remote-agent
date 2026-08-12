# ClaudeCode Remote

Persistente Terminal-Sessions für Coding-Agents (Claude Code, Codex, Antigravity) auf
einem eigenen Arch-Linux-Server — erreichbar von Android, Windows, Linux und Web.
Sessions überleben Verbindungsabbrüche und Gerätewechsel, weil tmux die Persistenz­schicht
ist und der Daemon rohe Terminal-Bytes unverändert durchreicht.

> **Status: Phase 0 (Setup) abgeschlossen.** Es läuft noch kein Code — die Phasen 1–9
> bauen das System schrittweise auf. Siehe [`ROADMAP.md`](ROADMAP.md).

## Komponenten

| Verzeichnis | Inhalt | Technik |
|---|---|---|
| [`server-provisioning/`](server-provisioning/) | Arch-Server härten, User trennen, Tailscale, systemd-Units, Idle-Updater, Backups, Samba | Bash + systemd |
| [`daemon/`](daemon/) | Terminal-Session-Daemon, File-API, Geräte-Identität | Go |
| [`app/`](app/) | Client für Android / Windows / Linux / Web | Flutter + `xterm.dart` |
| [`wiki/`](wiki/) | Quelle des GitHub-Wikis | Markdown |

Der öffentliche HTTPS-Zugang hinter CGNAT läuft über
[CloudGate](https://github.com/Elias02345/CloudGate) im Tunnel-Modus — dieses Repo baut
**keine** eigene Relay-Infrastruktur.

## Dokumentation

- **[Wiki](https://github.com/Elias02345/remote-agent/wiki)** — Betriebs- und
  Verständnis-Doku, Einstiegspunkt für Menschen.
- **[`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md)** — verbindliche Architektur­entscheidung.
  Änderungen nur nach Rücksprache.
- **[`ROADMAP.md`](ROADMAP.md)** — Phasenstatus und getroffene offene Entscheidungen.
- **[`TODO_FOR_USER.md`](TODO_FOR_USER.md)** — was nur der Mensch erledigen kann.
- **[`CLAUDE.md`](CLAUDE.md)** — Konventionen für Agent-Sessions in diesem Repo.

## Lizenz

Noch nicht festgelegt.
