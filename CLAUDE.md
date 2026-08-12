# CLAUDE.md — Projektkonventionen ClaudeCode Remote

Diese Datei ist die Arbeitsanweisung für jede künftige Agent-Session (Claude Code,
Codex, Antigravity) in diesem Repo. Sie ergänzt die globale `~/.claude/CLAUDE.md`.

## Quelle der Wahrheit

`docs/ARCHITECTURE.md` ist die vollständige, bereits abgestimmte Architektur­entscheidung.
Jede technische Entscheidung darin ist **verbindlich** — nicht neu verhandeln, nicht durch
eine "bessere Idee" ersetzen, außer der Nutzer bittet ausdrücklich darum.
Änderungen an `docs/ARCHITECTURE.md` nur nach Rücksprache.

`ROADMAP.md` hält den Phasenstatus und alle getroffenen offenen Entscheidungen fest.
`TODO_FOR_USER.md` sammelt alles, was nur der Mensch tun kann.

## Rolle

Leitender Entwickler für ein Mehrkomponenten-System:

| Komponente | Verzeichnis | Technik |
|---|---|---|
| Arch-Linux-Server als Agent-Base-Station | `server-provisioning/` | Bash, systemd |
| Terminal-Session- und Datei-Daemon | `daemon/` | Go |
| Client-App Android/Windows/Linux/Web | `app/` | Flutter, `xterm.dart` |
| Öffentlicher HTTPS-Zugang hinter CGNAT | — | CloudGate (separates Projekt) |

## Nicht verhandelbare Entscheidungen

1. **Terminal-Transport**: roher PTY-Passthrough über `tmux attach-session`, byteweise
   über WebSocket. **Kein** tmux Control Mode (`-CC`) — das ist ein iTerm2-spezifisches,
   strukturiertes Protokoll und für einen generischen Client ungeeignet.
2. **Terminal-Emulation** ausschließlich clientseitig (`xterm.dart` / `xterm.js`).
   Niemals serverseitig interpretieren, parsen oder vereinfachen.
3. **Persistenzschicht ist tmux selbst**, kein eigenes PTY-Session-Management.
   `set -g window-size latest`, damit sich die Session an das zuletzt aktive Gerät anpasst.
4. **Zwei Lock-Typen mit unterschiedlicher Lebensdauer**, niemals vermischen:
   - Agent-Locks (`agent-<pid>`): automatisch per Shell-Wrapper mit `trap EXIT`,
     Stale-Cleanup nach 6 h.
   - Terminal-Locks (`terminal-<session-id>`): unbegrenzt, nur manuelles Schließen
     entfernt sie. Kein Stale-Cleanup.
5. **Backups sind nicht an die Idle-Lock-Logik gekoppelt** — sie laufen unabhängig und
   häufiger, weil sie nichts Destruktives tun. Nur System-/Paket-Updates warten auf Idle.
6. **Datei-Hashes: SHA-256** (nicht MD5). Verifikation vor dem Senden UND nach dem
   Empfang, plus Rückvergleich des serverseitig berechneten Hash beim Client.
7. **CloudGate-Anbindung**: kein eigener Relay-Server. CloudGate wird als fertiger
   Baustein per Host-Eintrag im **Tunnel-Modus** genutzt. TLS terminiert an Cloudflares
   Edge; der Daemon lauscht intern auf Klartext-HTTP, gebunden an `localhost` bzw. das
   Tailscale-Interface.
8. **Drei-Faktor-Auth** (E-Mail+Passwort + TOTP + Passkey) gilt nur für Geräte-Pairing
   und sicherheitskritische Einzelaktionen (Step-Up) — niemals für den laufenden
   Alltagsbetrieb bereits gepaarter Geräte. Dafür reicht Ed25519-Challenge-Response.
9. **SMB/NFS-Freigaben** ausschließlich über das Tailscale-Interface, niemals über
   CloudGate oder das öffentliche Internet.
10. **Linux-Passkey-Lücke**: FIDO2-Hardware-Key (YubiKey o. ä.) als Fallback einplanen.

## Sicherheits-Leitplanken (immer gültig, phasenunabhängig)

- Kein Prozess läuft als root außer den explizit dafür vorgesehenen systemd-Units.
- Keine Geheimnisse (API-Tokens, Passwort-Hashes, private Keys) in Git.
  `.gitignore` + `.env.example` statt echter `.env` von Anfang an.
- Kein Endpunkt akzeptiert beliebige Shell-Kommandos aus der App — nur die eng
  spezifizierten Aktionen aus Abschnitt 8.4 der Architekturdoku.
- Jeder neue Netzwerk-Listener bindet standardmäßig an `localhost` oder das
  Tailscale-Interface, **niemals** an `0.0.0.0`, außer explizit als CloudGate-Host vorgesehen.
- Rate-Limiting und Lockout bei allen Auth-Endpunkten von Anfang an, nicht als Add-on.

## Was nicht getan wird

- Keine eigene Terminal-Emulation/-Interpretation im Daemon, "nur um es einfacher zu machen".
- Keine Kernel-Updates oder Reboots automatisch auslösen, auch nicht bei Idle —
  nur das `reboot-pending`-Flag setzen.
- Kein Zurückfallen auf MD5 als alleinige Integritätsprüfung ohne Kommentar im Code
  oder Commit-Text.
- Keine eigene Relay-/Tunnel-Infrastruktur parallel zu CloudGate.
- Keine stillschweigenden Abkürzungen bei der Drei-Faktor-Kette fürs Pairing, auch nicht
  "nur für Testzwecke". Testumgebungen bekommen eigene, klar gekennzeichnete
  Test-Konfiguration statt geschwächter Produktionslogik.

## Arbeitsweise

- **Eine Phase nach der anderen** (siehe `ROADMAP.md`), nicht alles parallel anfangen.
  Jede Phase endet in einem eigenständig lauffähigen/testbaren Zustand.
- Kleine, beschriebene Commits pro logischem Schritt. Keine Monster-Commits pro Phase.
- Jede Komponente hat ein eigenes `README.md`: Zweck, lokales Setup, Tests ausführen.
- Alles, was nur der Mensch tun kann (Cloudflare-API-Token, Domain, FIDO2-Key kaufen,
  SSH-Zugang zum echten Server, GitHub-Key hinterlegen) kommt in `TODO_FOR_USER.md`
  statt zu blockieren oder simuliert zu werden.
- Offene Entscheidungen aus Abschnitt 12 der Architekturdoku: die dort genannte
  Standardannahme wählen, in `ROADMAP.md` dokumentieren, explizit als revidierbar
  kennzeichnen. **Nie schweigend entscheiden.**
- Tests parallel zur Implementierung, nicht danach. Für den Daemon: Unit-Tests für
  Lock-Logik, Hash-Verifikation, Session-Statusübergänge. Für Sicherheitspfade (Auth,
  Rate-Limiting) zusätzlich Negativ-Tests (falsches Passwort, abgelaufene Challenge,
  wiederverwendeter Pairing-Code).

## Modell-Hierarchie in diesem Projekt

Opus orchestriert, analysiert und verifiziert. Sonnet implementiert. Haiku macht Mechanik
(Umbenennungen, Formatierung). Subagenten sind advisory und liefern Fakten mit `file:line`;
Opus verifiziert stichprobenartig gegen den echten Repo-Zustand, bevor etwas darauf aufbaut.

## Repo-Struktur

```
server-provisioning/   Bash/systemd für den Arch-Server
daemon/                Go-Daemon
  cmd/claudecode-remoted/
  internal/{terminal,files,identity,locks,db}/
app/                   Flutter-Client
wiki/                  Quelle für das GitHub-Wiki (wird nach remote-agent.wiki gepusht)
docs/ARCHITECTURE.md   verbindliche Architektur
.github/workflows/     CI: Lint, Tests, Build
```

## Sacred Paths (Updater/Installer fassen sie nie an)

- `/var/lib/claudecode-remote/` — SQLite-DB des Daemons (`.db`, `-wal`, `-shm`)
- `/etc/claudecode-remote/.env` — Secrets
- `/srv/exchange/`, `/srv/backups/` — Nutzdaten
- `/run/claudecode-locks/terminal-*` — offene Terminal-Locks
