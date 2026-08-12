# ROADMAP — ClaudeCode Remote

Status-Werte: `offen` · `in Arbeit` · `fertig`

Verbindliche Grundlage: [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md).
Nach jeder Phase: Zusammenfassung, neue Einträge in `TODO_FOR_USER.md`, Rückfrage zu
den in der Phase getroffenen offenen Entscheidungen — dann erst die nächste Phase.

## Phasenübersicht

| # | Phase | Status |
|---|---|---|
| 0 | Repo-Setup, Architekturdoku, Wiki | fertig |
| 1 | Server-Fundament | offen |
| 2 | Agent-Stack | offen |
| 3 | Idle-Update-System | offen |
| 4 | Terminal-Daemon MVP | offen |
| 5 | Dateien & Backups | offen |
| 6 | CloudGate-Anbindung | offen |
| 7 | Sicherheits-Layer | offen |
| 8 | Client-App Grundgerüst | offen |
| 9 | Android & Feinschliff | offen |

---

## Phase 1 — Server-Fundament · `offen`

**Umfang:** `server-provisioning/` — SSH-Härtung (Keys only, Port 2222, fail2ban),
User-Trennung `admin`/`agent`, Tailscale-Setup-Skript, GitHub-Key-Einrichtung dokumentiert.

**Definition of Done:** Ein frischer Arch-Server lässt sich mit einem Skript in den in
`docs/ARCHITECTURE.md` Abschnitt 2 beschriebenen Zustand bringen, inklusive Idempotenz
(mehrfaches Ausführen bricht nichts).

## Phase 2 — Agent-Stack · `offen`

**Umfang:** Installationsskripte für Claude Code / Codex / Antigravity-CLI,
`agentctl init` inkl. Git-Hooks, Verteilung der globalen `~/.claude/CLAUDE.md`.

**Definition of Done:** In einem Test-Repo erzeugt `agentctl init` korrekt die drei
Symlinks; Git-Hooks lösen es bei Checkout/Merge automatisch aus.

## Phase 3 — Idle-Update-System · `offen`

**Umfang:** `agent-run`-Wrapper, Lock-Verzeichnis-Logik (beide Lock-Typen getrennt
getestet), systemd-Timer, ntfy-Anbindung.

**Definition of Done:** Tests beweisen, dass Updates bei vorhandenem Lock übersprungen
werden, dass Agent-Locks nach 6 h Stale-Cleanup verschwinden und Terminal-Locks nicht.

## Phase 4 — Terminal-Daemon MVP · `offen`

**Umfang:** Go-Service, REST+WebSocket-API aus Abschnitt 5.2, SQLite-Schema aus 5.3,
getestet gegen einen simplen `xterm.js`-Testclient (kein Flutter nötig).

**Definition of Done:** Ein vollbildschirmfähiges TUI (Claude Code selbst, testweise auch
`htop`/`vim`) läuft sichtbar korrekt im Testclient, inklusive Alternate-Screen-Umschaltung
und Resize.

## Phase 5 — Dateien & Backups · `offen`

**Umfang:** Samba-Konfiguration (`backups`/`exchange`, Tailscale-only), restic-Timer,
File-API-Endpunkte inkl. SHA-256-Doppelprüfung und tus-basiertem Resumable Upload.

**Definition of Done:** Ein absichtlich abgebrochener Upload lässt sich fortsetzen, ein
manipulierter Chunk wird zurückgewiesen, ein Restore-Test-Timer läuft nachweislich durch.

## Phase 6 — CloudGate-Anbindung · `offen`

**Umfang:** Dokumentation/Skript für die Host-Einrichtung in CloudGate (Tunnel-Modus),
feste Subdomain-Entscheidung eintragen, Owner-Login-Portal-Grundgerüst gemäß Entscheidung
D-05.

**Definition of Done:** Der Terminal-Daemon ist über die feste Subdomain per HTTPS
erreichbar, WebSocket-Verbindung funktioniert nachweislich durch den Tunnel.

**Blocker:** D-04 (Domain) muss vorher final sein — siehe `TODO_FOR_USER.md`.

## Phase 7 — Sicherheits-Layer · `offen`

**Umfang:** Passwort (Argon2id) + TOTP + Passkey-Pairing-Flow, Ed25519-Geräte­auth,
Step-Up-Auth für sensible Aktionen, Rate-Limiting.

**Definition of Done:** Ein neues Gerät kann nur nach erfolgreichem Abschluss aller drei
Faktoren gepairt werden; ein bereits gepairtes Gerät kommt ohne erneute Passwort-Eingabe
rein; sensible Aktionen lösen nachweislich einen Step-Up-Prompt aus.

## Phase 8 — Client-App Grundgerüst · `offen`

**Umfang:** Flutter-Projekt für Linux/Windows/Web zuerst, Session-Liste, Terminal-View mit
`xterm.dart`, Reconnect-Logik, Datei-Browser.

**Definition of Done:** Alle drei Desktop-/Web-Targets bauen aus derselben Codebasis; eine
Terminal-Session lässt sich öffnen, trennen und auf einem anderen simulierten Client
fortsetzen.

## Phase 9 — Android & Feinschliff · `offen`

**Umfang:** Mobile Steuerleiste, native Copy-Paste-UX, Share-Sheet-Integration für
Datei-Uploads, FIDO2-Hardware-Key-Support als Linux-Fallback, Benachrichtigungen.

**Definition of Done:** Eine Datei aus einer beliebigen Android-App heraus lässt sich per
Share-Sheet direkt in `to-agent/` einer laufenden Session hochladen.

---

## Getroffene offene Entscheidungen

Alle Einträge folgen der in `docs/ARCHITECTURE.md` Abschnitt 12 genannten
Standardannahme. **Alle sind revidierbar**, solange die jeweils betroffene Phase noch
nicht abgeschlossen ist — Ausnahme D-04, siehe dort.

| ID | Frage | Gewählt | Betrifft | Revidierbar bis |
|---|---|---|---|---|
| D-01 | Mesh: Tailscale vs. eigenes WireGuard | **Tailscale** (Doc-Empfehlung, NAT-Traversal + ACLs ohne Eigenbau) | Phase 1 | Ende Phase 1 |
| D-02 | Bare-Metal vs. Proxmox-VM | **Provisioning bleibt agnostisch** — die Skripte laufen auf beidem, Snapshots sind ein reiner Betriebsvorteil | Phase 1 | jederzeit |
| D-03 | `reboot-pending`: informativ vs. automatisches Zeitfenster | **Rein informativ** (ntfy-Push, manueller Reboot). Kein Auto-Reboot-Timer in Phase 3 | Phase 3 | jederzeit, additiv nachrüstbar |
| D-04 | WebAuthn-Relying-Party-Domain | **offen — muss der Nutzer festlegen** | Phase 6/7 | **nur vor dem Passkey-Rollout.** Späterer Wechsel invalidiert alle Passkeys |
| D-05 | Owner-Login: WebAuthn-Modul in CloudGate vs. separates Portal | **Separates Portal** als `/owner`-Route im Daemon (Abschnitt 10.2). Saubere Trennung, CloudGate bleibt unverändert | Phase 6/7 | Ende Phase 6 |
| D-06 | CloudGate-Selbst-Update mit Lock koppeln | **Nein.** Das kurze `cloudflared`-Reload-Fenster wird akzeptiert; als Feature-Idee im CloudGate-Repo notiert, kein Blocker hier | — | jederzeit |
| D-07 | Volle Drei-Faktor-Kette bei jedem Pairing | **Ja, ausnahmslos.** Keine reduzierte Stufe für "vertraute Netze" | Phase 7 | Ende Phase 7 |
| D-08 | MD5 vs. SHA-256 für Datei-Hashes | **SHA-256** — bereits im Prompt als nicht verhandelbar fixiert | Phase 5 | — |
