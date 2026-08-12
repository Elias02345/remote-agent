# ClaudeCode Remote – Implementierungsdokumentation

**Version 0.1 · Konzept- und Architekturdokument**

## 1. Zielsetzung und Architekturüberblick

ClaudeCode Remote besteht aus drei unabhängigen, aber eng verzahnten Schichten:

1. **Server-Basis** – ein gehärteter Arch-Linux-Server mit fester IP, der als zentrale "Agent Base Station" dient: Coding-Agents (Claude Code, Codex, Antigravity-CLI), Code, Git-Zugang und ein autonomes Idle-Update-System.
2. **Terminal-Session-Daemon** – ein auf dem Server laufender Dienst, der persistente Terminal-Sessions verwaltet und über eine WebSocket/REST-API für Clients erreichbar macht. Dies ist das technische Herzstück, da hier "Sessions überleben SSH-Abbrüche und Geräte-Wechsel" umgesetzt wird.
3. **Client-Apps** – native Anwendungen für Android, Windows, Linux und Web, die sich mit dem Daemon verbinden und Terminals wie Tabs verwalten.

Datenfluss vereinfacht:

```
[Android/Windows/Linux/Web App]
        │  (WebSocket + REST, TLS, Geräte-Auth)
        │  ── eigene Geräte im Tailscale-Mesh: direkt ──┐
        │  ── Web-Client / fremde Netzwerke: über CloudGate ─┤
        ▼                                                    ▼
                    [Terminal-Session-Daemon auf dem Server]
        │  (roher PTY-Passthrough über tmux attach-session)
        ▼
[tmux Sessions] ── [Claude Code / Codex / Antigravity-CLI] ── [Projekt-Code + Git]
        │
[Idle-Update-System] ← prüft Locks aus Agent-Läufen und offenen Terminals
```

Die zentrale Designentscheidung: **tmux ist die Persistenzschicht, nicht dein eigener Code.** tmux löst Detach/Reattach, Scrollback und Prozess-Überleben bei SSH-Abbruch bereits seit Jahren zuverlässig. Der Daemon reicht dafür rohe Terminal-Bytes unverändert durch (siehe 5.1) und baut nur eine strukturierte API und ein Auth-/Sync-Modell darüber, statt PTY-Management oder Terminal-Interpretation neu zu erfinden.

---

## 2. Server-Basis (Arch Linux)

### 2.1 Grundinstallation und Benutzerkonzept

- Dedizierter, nicht-root Benutzer `agent` für alle Coding-Agents und Terminal-Sessions. Kein Agent läuft je als root.
- `sudo` für `agent` nur für exakt definierte Befehle (z. B. `systemctl restart claudecode-daemon`), über `/etc/sudoers.d/agent` mit `NOPASSWD` nur für diese Whitelist.
- Separater `admin`-User für SSH-Login und Serveradministration, getrennt vom `agent`-User, um eine kompromittierte Agent-Session nicht automatisch mit vollen SSH-Rechten auszustatten.

### 2.2 SSH-Härtung

`/etc/ssh/sshd_config` Kernpunkte:

```
PasswordAuthentication no
PermitRootLogin no
PubkeyAuthentication yes
AllowUsers admin agent
Port 2222
```

Zusätzlich:

- `fail2ban` mit sshd-Jail gegen Brute-Force.
- SSH-Keys ausschließlich Ed25519, pro berechtigtem Gerät ein eigenes Schlüsselpaar (kein geteilter Key), damit einzelne Geräte über `authorized_keys` gezielt widerrufen werden können.
- **Kein Port-Forwarding auf den Router.** Siehe 2.4 – Zugriff ausschließlich über ein privates Overlay-Netz.

### 2.3 GitHub-Authentifizierung

- Ein maschinengebundener Ed25519-Key wird als "Deploy-relevanter" SSH-Key im GitHub-Account hinterlegt (nicht als Deploy-Key pro Repo, da mehrere Projekte verwaltet werden).
- `~/.ssh/config` mit `IdentitiesOnly yes`, damit kein Key-Leaking zwischen Diensten passiert.
- Kein Agent-Forwarding (`ForwardAgent no`) – der Key bleibt physisch auf dem Server, was exakt zum Konzept "Server hält den ganzen Code" passt.

### 2.4 Netzwerkzugriff: Overlay-Netz statt offener Ports

Statt SSH und die App-Ports direkt über Portweiterleitung im Internet zu exponieren, empfehle ich ein privates Mesh-Netz:

- **Tailscale** (oder selbstgehostet: Headscale) auf dem Server und auf jedem Client-Gerät. Der Server bekommt eine stabile Tailnet-IP, SSH und der Terminal-Daemon binden ausschließlich an dieses Interface.
- Vorteile: Ende-zu-Ende-Verschlüsselung ist bereits durch WireGuard (Basis von Tailscale) gelöst, NAT-Traversal funktioniert automatisch, einzelne Geräte lassen sich pro ACL freigeben oder sofort entziehen, und es läuft nativ auf Android, Windows, Linux und – über einen Subnet-Router – auch im Web-Kontext.
- Da du bereits Cloudflare-Reverse-Proxy-Erfahrung hast: als Alternative funktioniert auch ein **Cloudflare Tunnel** mit Cloudflare Access für die Web-Variante der App, wenn du lieber bei deinem bestehenden Setup bleiben willst. Für die native Terminal-Kommunikation (WebSocket mit hoher Frequenz) ist Tailscale aber die einfachere und robustere Wahl, weil kein öffentlicher Endpunkt existiert, den man absichern muss.

Diese Entscheidung wirkt zunächst wie eine Nebensache, ist aber der Grund, warum die App später kein eigenes PKI-System bauen muss – das Overlay-Netz übernimmt die Transportverschlüsselung, die App-Ebene macht nur noch Geräte-Autorisierung (siehe 5.4).

---

## 3. Agentic-Coding-Stack

### 3.1 Installation

Claude Code, Codex und Antigravity-CLI werden systemweit für den `agent`-User installiert (jeweils gemäß aktueller Hersteller-Installationsanleitung, da sich Paketnamen/-manager ändern können). Wichtig ist nur die Konvention danach: alle drei werden über **einen gemeinsamen Wrapper** gestartet (siehe 4.2), nie direkt.

### 3.2 CLAUDE.md als Single Source of Truth

Deine Idee, `AGENTS.md` und `ANTIGRAVITY.md` von `CLAUDE.md` "mounten" zu lassen, löse ich am robustesten über **Symlinks**, nicht über echtes Bind-Mounting – Textdateien werden von jedem Tool transparent gelesen, ein Bind-Mount wäre unnötig komplex und würde bei Container-/Chroot-Nutzung Probleme machen.

Pro Projekt-Root:

```
CLAUDE.md            # kanonische Quelle, wird editiert
AGENTS.md -> CLAUDE.md
ANTIGRAVITY.md -> CLAUDE.md
```

Ein kleines Init-Skript automatisiert das:

```bash
#!/usr/bin/env bash
# agentctl init – im Projekt-Root ausführen
set -e
[ -f CLAUDE.md ] || { echo "CLAUDE.md fehlt"; exit 1; }
ln -sf CLAUDE.md AGENTS.md
ln -sf CLAUDE.md ANTIGRAVITY.md
echo "Agent-Kontextdateien verlinkt."
```

Dieses Skript hängst du zusätzlich als `post-checkout`- und `post-merge`-Git-Hook ein, damit frisch geklonte oder aktualisierte Repos die Symlinks automatisch bekommen, ohne dass du manuell eingreifen musst.

Für maschinenweite Konventionen (Coding-Style, Sicherheitsregeln, die für alle Projekte gelten) legst du zusätzlich `~/.claude/CLAUDE.md` als globale Ebene an – Claude Code liest projektspezifische und globale Ebenen ohnehin hierarchisch, Codex und Antigravity bekommen diese globale Datei über denselben Symlink-Mechanismus in ihr jeweiliges Home-Verzeichnis gespiegelt.

---

## 4. Autonomes Idle-Update-System

Das ist der Teil, bei dem die Zuverlässigkeit der Lock-Logik entscheidend ist – ein hängengebliebener Lock darf niemals Updates für immer blockieren, und ein vergessener Agent-Lauf darf niemals mitten in einem Update abgewürgt werden.

### 4.1 Zwei Lock-Typen, unterschiedliche Lebensdauer

- **Agent-Lock** (`/run/claudecode-locks/agent-<pid>`): entsteht automatisch, wenn ein Coding-Agent läuft. Soll **nie dauerhaft hängen bleiben** – wenn ein Agent abstürzt, muss der Lock trotzdem irgendwann verschwinden.
- **Terminal-Lock** (`/run/claudecode-locks/terminal-<session-id>`): entsteht, sobald im Terminal-Daemon eine Session existiert. Bleibt **absichtlich unbegrenzt** bestehen, bis der Nutzer das Terminal manuell schließt – das ist explizit gewünschtes Verhalten aus deiner Spezifikation, kein Bug.

### 4.2 Automatischer Wrapper statt "der Agent muss es sich merken"

Deine ursprüngliche Idee war, dass der Agent selbst per Instruktion weiß, den Lock-Prozess zu starten und zu stoppen. Das funktioniert, ist aber fragil: Stürzt der Agent ab oder vergisst die Instruktion (z. B. bei einem Kontext-Reset), bleibt der Lock für immer stehen und blockiert Updates dauerhaft. Robuster ist ein **transparenter Shell-Wrapper**, der den Lock automatisch setzt und per `trap ... EXIT` auch bei Fehlern zuverlässig wieder entfernt:

```bash
#!/usr/bin/env bash
# /usr/local/bin/agent-run
LOCK_DIR="/run/claudecode-locks"
mkdir -p "$LOCK_DIR"
LOCK_FILE="$LOCK_DIR/agent-$$"
trap 'rm -f "$LOCK_FILE"' EXIT
touch "$LOCK_FILE"
exec "$@"
```

Als Shell-Aliase für den `agent`-User:

```bash
alias claude='agent-run claude'
alias codex='agent-run codex'
alias antigravity='agent-run antigravity-cli'
```

Damit muss keine Agent-Instanz "wissen", dass sie einen Lock verwaltet – es passiert transparent bei jedem Aufruf. Falls du trotzdem willst, dass der Agent es explizit versteht (z. B. für sehr lange manuelle Shell-Sessions außerhalb des Wrappers), ergänzt du in der `CLAUDE.md` einen Hinweis wie: *"Lang laufende Hintergrundprozesse außerhalb eines Agent-Aufrufs müssen manuell `touch /run/claudecode-locks/agent-manual-<name>` setzen und beim Beenden wieder entfernen."* Das bleibt als Fallback sinnvoll, ersetzt aber nicht den automatischen Wrapper als primären Mechanismus.

Gegen liegen gebliebene Locks durch harte Abstürze (`SIGKILL`, Stromausfall) räumt der Update-Check zusätzlich **nur Agent-Locks** auf, die älter als z. B. 6 Stunden sind – Terminal-Locks werden davon bewusst ausgenommen, da sie beliebig lange gültig bleiben sollen.

### 4.3 systemd-Timer

```ini
# /etc/systemd/system/idle-updater.timer
[Unit]
Description=Idle-basiertes System-Update Timer

[Timer]
OnBootSec=5min
OnUnitActiveSec=30min
Unit=idle-updater.service

[Install]
WantedBy=timers.target
```

```ini
# /etc/systemd/system/idle-updater.service
[Unit]
Description=Prüft Idle-Status und aktualisiert das System

[Service]
Type=oneshot
ExecStart=/usr/local/bin/idle-update-check.sh
```

```bash
#!/usr/bin/env bash
# /usr/local/bin/idle-update-check.sh
set -euo pipefail
LOCK_DIR="/run/claudecode-locks"
mkdir -p "$LOCK_DIR"

# Stale Agent-Locks (>6h) automatisch entfernen, Terminal-Locks NICHT anfassen
find "$LOCK_DIR" -name 'agent-*' -mmin +360 -delete 2>/dev/null || true

if [ -n "$(ls -A "$LOCK_DIR" 2>/dev/null)" ]; then
    logger -t idle-updater "Update übersprungen, aktive Locks: $(ls "$LOCK_DIR" | tr '\n' ' ')"
    exit 0
fi

logger -t idle-updater "System idle – starte Update"
pacman -Syu --noconfirm
command -v paru >/dev/null 2>&1 && paru -Syu --noconfirm --sudoloop || true

for tool in claude-code codex antigravity-cli; do
    npm update -g "$tool" >/dev/null 2>&1 || true
done

# Kernel-Update erkannt? Nicht automatisch neu starten, nur Flag setzen.
CURRENT_KERNEL="$(uname -r)"
INSTALLED_KERNEL="$(pacman -Q linux | awk '{print $2}')"
mkdir -p /var/lib/claudecode
if [[ "$INSTALLED_KERNEL" != *"${CURRENT_KERNEL%-arch*}"* ]]; then
    touch /var/lib/claudecode/reboot-pending
fi

[ -n "${NTFY_URL:-}" ] && curl -s -d "ClaudeCode Remote: Update abgeschlossen ($(date '+%d.%m %H:%M'))" "$NTFY_URL" >/dev/null || true
logger -t idle-updater "Update abgeschlossen"
```

Wichtig: Das Reboot-Flag liegt bewusst **außerhalb** von `LOCK_DIR` – läge es dort, würde es sich selbst als aktiven Lock zählen und alle künftigen Updates blockieren.

### 4.4 Reboot-Handling

Ein automatischer Reboot bei Idle ist riskant, da er auch alte, "vergessene" tmux-Sessions killen würde, selbst wenn gerade kein Lock aktiv ist. Empfehlung: **kein automatischer Reboot**, stattdessen ein separater, viel selteneren Timer (z. B. wöchentlich, an einem festen Zeitfenster wie 4 Uhr nachts), der nur dann rebootet, wenn `reboot-pending` gesetzt ist **und** null Terminal-Sessions existieren (nicht nur null Locks, sondern eine tatsächliche Abfrage beim Daemon, ob überhaupt Sessions offen sind).

### 4.5 Benachrichtigung

`ntfy` (self-hosted oder ntfy.sh) als simpler Push-Kanal in die App/aufs Handy: Update-Zusammenfassung, Hinweis auf ausstehende Reboots, Fehlermeldungen bei fehlgeschlagenen Updates.

---

## 5. Terminal-Session-Daemon (Kernstück)

### 5.1 Architekturentscheidung

- Sprache: **Go** – reifes PTY-Ökosystem (`creack/pty`), gute WebSocket-Bibliotheken (`gorilla/websocket`), einfaches Concurrency-Modell für viele parallele Streams, kompiliert zu einer einzelnen Binary ohne Laufzeitabhängigkeiten, was den systemd-Service-Betrieb trivial macht.
- **Korrektur gegenüber einer früheren Version dieser Doku:** tmux Control Mode (`-CC`) ist ein proprietäres, auf iTerm2 zugeschnittenes Protokoll für Pane-Layout-Synchronisation – kein roher Byte-Stream. Full-Screen-TUIs wie Claude Code (Ink-basiertes UI mit Alternate-Screen-Buffer, eigenem Redraw-Zyklus, Box-Drawing-Rahmen) würden darüber nicht korrekt ankommen. Der Daemon steuert stattdessen **rohen PTY-Passthrough**: Für jede Client-Verbindung startet er `tmux attach-session -t <id>` innerhalb einer eigenen PTY (`creack/pty`) und leitet die Rohbytes unverändert bidirektional über den WebSocket weiter – keine serverseitige Interpretation, kein Parsing.
- Der Client übernimmt die Interpretation vollständig über eine echte Terminal-Emulator-Bibliothek (`xterm.dart` in der App, `xterm.js` für einen etwaigen Web-Testclient) – exakt dieselbe Emulationsschicht, die auch vim, htop oder tmux selbst korrekt darstellt. Das ist kein Sonderfall, sondern derselbe Mechanismus, den ttyd, gotty, Upterm und Termius bereits produktiv einsetzen.
- Für korrekte Darstellung müssen folgende Punkte im Client-Emulator und in der `TERM`-Umgebungsvariable stimmen: `TERM=tmux-256color` (oder `xterm-256color`), Alternate-Screen-Buffer-Umschaltung (`?1049h`/`?1049l`) für sauberes Wiederherstellen beim Verlassen des Vollbild-Modus, Truecolor-Escape-Codes, Cursor-Sichtbarkeit (`?25l`/`?25h`) und korrekte Breitenberechnung für Unicode/Box-Drawing-Zeichen. `xterm.dart`/`xterm.js` unterstützen all das bereits nativ.
- Beim Verbindungsaufbau eines Clients liefert der Daemon zunächst den aktuellen Bildschirminhalt über `tmux capture-pane -p -e` (das `-e` erhält die Farb-/Escape-Codes), danach streamt er live weiter – so sieht die App sofort den aktuellen Zustand, ohne das komplette Scrollback erneut abspielen zu müssen.
- **Resize bei mehreren gleichzeitig verbundenen Geräten:** tmux passt eine Session standardmäßig auf die kleinste angehängte Fenstergröße an – hättest du dieselbe Session gleichzeitig auf dem Handy und am Desktop offen, würde das Claude-Code-Layout auf Handy-Breite gequetscht. Die Lösung ist die native tmux-Option `set -g window-size latest`: Die Session wird immer an das Gerät angepasst, das zuletzt aktiv Eingaben gesendet hat, alle anderen sehen währenddessen eine skalierte Momentaufnahme statt aktiv das Layout zu verzerren.

### 5.2 API-Oberfläche

REST (Session-Verwaltung):

```
GET    /sessions              → Liste aller offenen Terminals
POST   /sessions               → neues Terminal (shell, cwd, name)
DELETE /sessions/{id}          → Terminal manuell schließen (einzige Schließ-Methode)
PATCH  /sessions/{id}          → umbenennen
```

WebSocket (Live-Interaktion):

```
/sessions/{id}/stream
  → Client sendet: {"type":"input","data":"..."} | {"type":"resize","cols":n,"rows":n}
  ← Server sendet: {"type":"output","data":"..."} | {"type":"exit"}
```

### 5.3 Datenmodell (SQLite)

```sql
CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    name TEXT,
    tmux_session TEXT NOT NULL,
    cwd TEXT,
    shell TEXT,
    status TEXT CHECK(status IN ('open','closed')) DEFAULT 'open',
    created_at INTEGER,
    last_active_at INTEGER
);

CREATE TABLE devices (
    id TEXT PRIMARY KEY,
    name TEXT,
    platform TEXT,
    pubkey TEXT NOT NULL,
    paired_at INTEGER,
    last_seen_at INTEGER,
    revoked INTEGER DEFAULT 0
);
```

`status='closed'` wird ausschließlich durch die explizite `DELETE`-Aktion eines Nutzers gesetzt – dein Prinzip "manuell öffnen und schließen" ist damit direkt im Datenmodell verankert statt nur in der UI.

### 5.4 Geräte-Authentifizierung (Pairing statt Passwort)

Statt Passwörtern oder statischen Tokens ein Challenge-Response-Verfahren analog zu SSH:

1. App generiert bei Erstinstallation ein Ed25519-Schlüsselpaar lokal (Android Keystore / Windows Credential Manager / Linux Secret Service / im Web nur als kurzlebiger Session-Key, da Browser-Storage weniger vertrauenswürdig ist).
2. App zeigt einen Pairing-Code an.
3. Auf dem Server: `claudecode-remote pair <code>` registriert den öffentlichen Schlüssel des Geräts in der `devices`-Tabelle.
4. Jede weitere Verbindung signiert eine vom Daemon gestellte Zufalls-Challenge – kein Geheimnis wandert je über die Leitung.
5. Geräte lassen sich jederzeit über `claudecode-remote devices` (oder eine kleine Verwaltungsansicht in der App für das Hauptgerät) einzeln widerrufen (`revoked=1`).

Da der Daemon ohnehin nur im Tailscale-Interface lauscht (siehe 2.4), ist dies eine zweite, unabhängige Sicherheitsebene – Kompromittierung des Netzwerks allein reicht nicht für Terminalzugriff.

---

## 6. Cross-Platform Client-App

### 6.1 Techstack

**Flutter** für Android, Windows, Linux und Web aus einer Codebasis – das ist der Punkt, an dem sich die Investition am meisten lohnt, da du sonst mindestens zwei Codebasen (z. B. Tauri/Web-Frontend für Desktop+Web, plus separates Kotlin/Compose für Android) pflegen müsstest. Terminal-Rendering über `xterm.dart` (VT100/256-Farben-Emulation), das die gleiche Engine-Logik wie `xterm.js` im Web nutzt.

Alternative, falls dir später mehr native Performance auf Desktop wichtiger wird als eine einzige Codebasis: Tauri + Svelte/React mit `xterm.js` für Windows/Linux/Web, kombiniert mit einer separaten nativen Android-App. Für den Start und einen einzelnen Entwickler ist Flutter aber der pragmatischere Weg zu deinem Anspruch "überall gleich funktioniert".

### 6.2 UI-Konzept

- **Terminal-Liste** als Startbildschirm, verhält sich wie Browser-Tabs: Name, Arbeitsverzeichnis, letzte Aktivität, Status-Punkt (verbunden/getrennt). Neues Terminal über einen deutlich sichtbaren Button.
- **Terminal-Ansicht**: volle Emulation, dazu auf Mobile eine zusätzliche Steuerleiste über der Tastatur mit Esc, Tab, Strg (als Toggle für Kombinationen), Pfeiltasten, Pipe, Slash, sowie einem Paste-Button, der aus der nativen Zwischenablage liest – dieses Muster kennst du wahrscheinlich aus Termux oder Blink Shell.
- **Schließen ausschließlich manuell**: über einen expliziten Button mit Bestätigungsdialog, kein automatisches Timeout, kein Schließen bei App-Wechsel oder Verbindungsabbruch.
- **Copy-Paste**: über Flutters plattformübergreifende `Clipboard`-API, auf Mobile zusätzlich Press-and-Hold mit Auswahl-Handles wie bei nativer Texteingabe, auf Desktop/Web klassisches Rechtsklick-Kontextmenü.
- **Reconnect-Logik**: WebSocket mit exponentiellem Backoff; bei Wiederverbindung liefert der Daemon sofort den aktuellen `capture-pane`-Zustand nach, sodass kein Neuladen des kompletten Verlaufs nötig ist.

### 6.3 Verhalten bei Verbindungsverlust

Ein sichtbarer Status-Banner ("Getrennt – verbinde neu…") statt eines stillen Fehlers. Wichtig: Verbindungsverlust der App ändert nichts am Zustand der Session auf dem Server – die tmux-Session läuft unabhängig weiter, exakt wie bei SSH+tmux heute schon, nur eben mit einer sauberen, geräteübergreifenden Oberfläche darüber.

---

## 7. Datei-Freigaben, Backups und Austausch mit dem Agenten

### 7.1 Zwei getrennte Freigaben mit unterschiedlichem Zweck

Statt einer einzigen Freigabe für alles, zwei sauber getrennte Bereiche unter `/srv/`:

```
/srv/backups/     # automatische Sicherungen, nur lesend für den Nutzer relevant
/srv/exchange/    # bidirektionaler Austauschordner Agent ↔ Nutzer
```

Beide werden per **Samba (SMB3)** freigegeben – nicht NFS. Begründung: SMB3 unterstützt Transportverschlüsselung nativ (`smb encrypt = required`), wird von Windows nativ und von Linux/macOS über Standard-Clients unterstützt, während NFS ohne zusätzlichen Kerberos-Aufwand unverschlüsselt und im Wesentlichen nur IP-basiert autorisiert ist – für eine Freigabe, die potenziell Backups und Projektdateien enthält, ist das Sicherheitsniveau von NFSv3 zu niedrig für dein Anspruchsniveau.

Wichtig: **Beide Freigaben werden ausschließlich über das Tailscale-Interface angeboten** (`smb.conf` → `interfaces = tailscale0` + `bind interfaces only = yes`), niemals über das öffentliche Interface oder über CloudGate (Dateifreigabeprotokolle wie SMB sind nicht fürs offene Internet gebaut, selbst mit Verschlüsselung). Für den Zugriff von unterwegs ohne Tailscale übernimmt stattdessen die App-integrierte File-API aus Abschnitt 8 – SMB bleibt der komfortable Weg für den Schreibtisch-Rechner im Mesh.

### 7.2 Automatisches Backup-System

Backup-Tool: **restic** – dedupliziert, verschlüsselt die Snapshots clientseitig (auch wenn sie lokal auf `/srv/backups` liegen, ist das sinnvoll, falls du später einen Offsite-Kopiervorgang ergänzt), und erlaubt inkrementelle, platzsparende Snapshots mit einfacher Restore-Historie.

Was gesichert wird:

- SQLite-Datenbank des Terminal-Daemons (Sessions, Geräte, Auth-Zustand)
- `~/.claude/`, `~/.codex/`, `~/.antigravity/` Konfigurationen inklusive der globalen `CLAUDE.md`
- `/srv/exchange/` Inhalte
- Alles unter dem Projekt-Root, das **nicht** von Git erfasst ist (`.env`-Dateien, lokale Build-Artefakte, die du bewusst nicht committen willst) – über eine `.backupignore`-ähnliche Include-Liste, damit nicht versehentlich das gesamte `node_modules` mitgesichert wird

Wichtige Klarstellung zur Lock-Logik aus Abschnitt 4: Backups sind **nicht** an die Idle-Locks aus dem Update-System gekoppelt. Ein Update kann Dienste neu starten oder das System instabil machen, solange es läuft – ein Backup liest nur Daten und schreibt sie woanders hin, das ist auch bei aktivem Agent-Lauf oder offenem Terminal unkritisch. Ein separater, häufigerer systemd-Timer (z. B. stündlich für die SQLite-DB und `/srv/exchange/`, täglich für den vollen Projektbestand) läuft daher unabhängig vom Idle-Zustand.

```ini
# /etc/systemd/system/claudecode-backup.timer
[Unit]
Description=Regelmäßiges restic-Backup

[Timer]
OnCalendar=hourly
Persistent=true

[Install]
WantedBy=timers.target
```

Restore-Test nicht vergessen einzuplanen: ein Backup, das nie zurückgespielt wurde, ist nur eine Vermutung – ein monatlicher `restic check` plus stichprobenartiger `restic restore` in ein Scratch-Verzeichnis gehört als eigener, kleiner Timer mit dazu.

### 7.3 Der Austauschordner (Agent ↔ Nutzer)

Statt eines einzigen gemischten Ordners, pro Projekt eine klare Zweiwege-Struktur, damit nie unklar ist, wer eine Datei dort abgelegt hat:

```
/srv/exchange/<projekt>/from-agent/
/srv/exchange/<projekt>/to-agent/
```

- `to-agent/` ist das Ziel, wenn du aus einer Client-App heraus eine Datei "für den Agenten-Kontext teilen" möchtest (siehe 8.3).
- `from-agent/` nutzt der Coding-Agent, wenn er dir aktiv etwas übergeben will (z. B. ein generiertes Bild, einen Export, einen Report) – das gehört als Konvention in die `CLAUDE.md`, damit alle drei Agenten (Claude Code, Codex, Antigravity) denselben Pfad kennen.
- Ein kleiner `inotifywait`-basierter Watcher auf `from-agent/` kann optional eine ntfy-Benachrichtigung auslösen, sobald der Agent dort etwas ablegt, damit du es nicht zufällig entdecken musst.

---

## 8. Dateizugriff und Datei-Sharing aus den Client-Apps

### 8.1 Zwei Zugriffswege für zwei Zielgruppen

- **SMB-Mount** (Abschnitt 7.1) für den bequemen Zugriff vom Windows/Linux-Schreibtisch aus dem Tailscale-Mesh – kein zusätzlicher Code nötig, funktioniert mit jedem Dateimanager.
- **App-integrierte File-API** für alle vier Plattformen gleichermaßen, weil Android und Web keine praktikable native SMB-Mount-Erfahrung bieten und weil dies der Weg ist, über den Dateien gezielt **für eine bestimmte Terminal-Session** geteilt werden.

Die File-API ist eine Erweiterung desselben Daemons aus Abschnitt 5, kein separater Dienst:

```
GET    /files?path=...              → Verzeichnisinhalt auflisten
GET    /files/download?path=...     → Datei herunterladen (gestreamt, mit Hash-Header)
POST   /files/upload                → Chunked Upload mit Hash-Verifikation (siehe 8.2)
DELETE /files?path=...               → Datei löschen
```

Zugriff ist auf `/srv/exchange/` und explizit freigegebene Projektverzeichnisse beschränkt (Allowlist, kein freier Dateisystem-Browser über den ganzen Server) – der Daemon läuft als `agent`-User und kann strukturell gar nicht außerhalb dieser Pfade lesen oder schreiben.

### 8.2 Integritätsprüfung: Hash vor dem Senden, Hash nach dem Empfang

Deine Anforderung – vollständig und redundant, mit Prüfsumme vor dem Senden und nach dem Empfang – setze ich so um:

1. Client berechnet den Hash der Datei lokal, **bevor** der Upload beginnt.
2. Upload erfolgt in Chunks (wichtig bei größeren Dateien über Mobilfunk – ein Abbruch bei 95 % soll nicht bedeuten, dass die ganze Datei neu übertragen wird). Als Protokoll-Grundlage eignet sich das offene **tus-Resumable-Upload-Protokoll** (`tus.io`), das genau dieses Chunking/Resume-Verhalten standardisiert, statt es selbst neu zu entwerfen.
3. Nach vollständigem Empfang berechnet der Server denselben Hash über die geschriebene Datei und vergleicht ihn mit dem vom Client mitgesendeten Wert. Bei Mismatch wird die Datei verworfen und der Client zum erneuten Senden aufgefordert, statt eine möglicherweise korrupte Datei stillschweigend zu behalten.
4. Der Server sendet seinen berechneten Hash in der Antwort zurück, der Client vergleicht ihn ein zweites Mal gegen seinen eigenen – das deckt zusätzlich Fehler ab, die erst bei der Antwort selbst entstehen (Man-in-the-Middle-Verfälschung wäre durch TLS/das Overlay-Netz zwar bereits ausgeschlossen, der doppelte Vergleich ist aber eine günstige zusätzliche Absicherung gegen Übertragungsfehler).

Eine Anmerkung zur Wahl des Hash-Algorithmus: Du hattest MD5 vorgeschlagen, das ist für reine **Korruptionserkennung** (kaputte Übertragung, defekter Speicher) völlig ausreichend und schnell. Für ein Projekt, das ohnehin auf höchste Sicherheit ausgelegt ist, würde ich aber **SHA-256** als Standard setzen – MD5 gilt kryptographisch als gebrochen (Kollisionen sind praktisch erzeugbar), was für reine Integritätsprüfung selbst meist irrelevant ist, aber sobald jemand mit Schreibzugriff auf einen Zwischenpunkt gezielt eine Kollision konstruieren könnte, wäre MD5 die schwächere Wahl. SHA-256 kostet auf heutiger Hardware keinen spürbaren Mehraufwand und du musst dir die Unterscheidung "wofür ist MD5 noch okay" später nicht merken.

### 8.3 Datei direkt für den Agenten-Kontext teilen

Das ist der Workflow, der deine Anforderung "vollständig funktionsfähig" konkret einlöst:

1. In der App wählst du innerhalb einer offenen Terminal-Session den Menüpunkt "Datei teilen" – entweder aus dem geräteeigenen Dateisystem (Android: nativer Share-Sheet-Eintrag "An ClaudeCode Remote senden" aus jeder beliebigen App heraus, Desktop/Web: Datei-Picker oder Drag & Drop) oder aus dem Server-Dateibrowser selbst (z. B. eine bereits vorhandene Datei aus `from-agent/` erneut in eine andere Session einspeisen).
2. Die App lädt die Datei nach dem Verfahren aus 8.2 in `to-agent/` der aktuellen Session-Zuordnung hoch (`/srv/exchange/<projekt>/to-agent/<dateiname>`).
3. Optional – konfigurierbar pro Session – schreibt der Daemon danach automatisch eine Zeile in den Terminal-Input-Stream, z. B. `# Neue Datei verfügbar: to-agent/foto.png`, sodass der Agent den Kontext sofort sieht, ohne dass du selbst tippen musst. Standardmäßig würde ich das als Vorschlag statt als automatische Eingabe umsetzen (ein Hinweis-Overlay mit "In Terminal einfügen"-Button), damit nie ungewollt Text in eine laufende Eingabe des Agenten hineingeschrieben wird, während er gerade selbst etwas tippt.

### 8.4 Datei-Browser und Server-Steuerung in der App

Ein eigener "Server"-Tab neben der Terminal-Liste, mit:

- Verzeichnisbaum über die File-API (8.1), Download mit Fortschrittsanzeige, Upload per Drag & Drop (Desktop/Web) bzw. Datei-/Foto-Auswahl (Android).
- Kurzbefehle ("Shortcuts") für wiederkehrende Server-Aktionen, die über dieselbe REST-Schicht wie die Terminal-Verwaltung laufen und nicht über eine separate Rechteebene: Backup jetzt auslösen, Update-Status/`reboot-pending` einsehen, aktive Locks einsehen, Geräteliste verwalten (siehe 10.4).
- Diese Shortcuts sind bewusst als vordefinierte, geprüfte Aktionen umgesetzt statt als freier Kommando-Eingabe – Fernsteuerung über eine App-Schaltfläche sollte nie beliebige Shell-Befehle mit Admin-Rechten ausführen können, nur die explizit dafür vorgesehenen, engen Endpunkte.

---

## 9. CGNAT und CloudGate als Vermittler

### 9.1 Warum das überhaupt ein Problem ist

Eine "feste IP" vom Provider ist bei vielen deutschen Anschlüssen (insbesondere Mobilfunk- oder manchen Kabel-Verträgen) trotzdem hinter **Carrier-Grade NAT (CGNAT)** – die IP, die dein Router zeigt, ist nicht die öffentlich routbare Adresse, sondern selbst wieder hinter einer NAT-Ebene des Providers, die sich mehrere Kunden teilen. Klassisches Portforwarding im Router bringt in diesem Fall nichts, weil der eingehende Traffic den Router nie erreicht. CloudGate existiert laut eigener Beschreibung explizit für genau diesen Fall.

### 9.2 Was CloudGate tatsächlich tut

Wichtige Korrektur gegenüber der vorigen Version dieses Abschnitts: CloudGate ist **kein eigener Relay-Server, den du selbst betreiben musst.** Es ist eine WebUI, die **Cloudflare Tunnel** (`cloudflared`) orchestriert. Die eigentliche Relay-Funktion – ein öffentlich erreichbarer Knoten, zu dem dein Server eine ausgehende Verbindung offen hält – übernimmt **Cloudflares eigenes, globales Edge-Netzwerk**, nicht CloudGate selbst und nicht irgendein von dir gemieteter VPS. Das ist strukturell derselbe Grundmechanismus, den ich vorher generisch beschrieben hatte (ausgehende Verbindung statt Portforwarding), nur dass der Relay-Punkt bereits fertig existiert und von Cloudflare betrieben wird.

CloudGate selbst läuft als Docker-Container bei dir (mitgeliefert nginx auf Port 80/443 lokal + Node/TypeScript-Backend auf Port 3000) und automatisiert den Rest:

1. Du hinterlegst einmalig einen Cloudflare-API-Token in der WebUI.
2. Für jeden Dienst, den du veröffentlichen willst, trägst du nur `interne-IP:Port` und die gewünschte Subdomain ein (z. B. `127.0.0.1:8080` → `remote.deine-domain.de`).
3. CloudGate erstellt automatisch den Cloudflare Tunnel, das DNS-CNAME, die Ingress-Regel und lädt `cloudflared` neu – laut Repo dauert das ab Eintragung rund 30 Sekunden bis der Dienst live ist.

CloudGate unterstützt zusätzlich einen **Hybrid-Modus**: pro Host wählst du "über Cloudflare Tunnel" oder "lokaler nginx-Reverse-Proxy". Der lokale nginx-Modus setzt eine erreichbare öffentliche IP voraus – das ist bei dir wegen CGNAT gerade nicht gegeben, du wirst also für alle ClaudeCode-Remote-Dienste den **Tunnel-Modus** verwenden.

### 9.3 Praktische Folgen fürs eigene Setup

- **TLS-Terminierung übernimmt Cloudflares Edge**, nicht dein Server. Der Terminal-Daemon aus Abschnitt 5 muss also selbst kein eigenes Zertifikat verwalten – er kann intern auf reinem HTTP lauschen (gebunden an `localhost` oder die Tailscale-Schnittstelle, siehe 9.4), `cloudflared` reicht die Verbindung vom öffentlichen HTTPS-Hostnamen dorthin durch. Das vereinfacht den Daemon spürbar gegenüber meiner ursprünglichen Annahme, er müsse eigenes Let's-Encrypt-Handling einbauen.
- **WebSocket funktioniert über Cloudflare Tunnel** grundsätzlich problemlos – das ist ein etablierter, häufig genutzter Anwendungsfall (u. a. für ähnliche Terminal-/Remote-Tools). Für die niedrigste Latenz bleibt trotzdem die direkte Tailscale-Verbindung zwischen deinen eigenen Geräten und dem Server vorzuziehen (siehe 9.4) – Cloudflare Tunnel ist der Weg für Web-Client und fremde Netzwerke, nicht zwingend der schnellste Weg für den Alltag von deinem eigenen Rechner aus.
- **CloudGates Selbst-Update** (alle 6 Stunden GitHub-Polling, GPG-signiert, atomarer Rollback) läuft unabhängig von der Idle-Lock-Logik aus Abschnitt 4 – das ist ein eigenständiges System für einen anderen Container. Da ein Update von CloudGate potenziell `cloudflared` kurz neu lädt, wäre eine offene Terminal-Verbindung über den Tunnel für den Moment des Reloads theoretisch kurz betroffen. CloudGate bietet dafür aktuell keinen Lock-Mechanismus an – das wäre, falls es dich stört, ein sinnvoller Feature-Request oder eigener kleiner PR in dein eigenes Projekt, kein Blocker für den Rest der Architektur.
- Das `/data`-Verzeichnis von CloudGate ist laut Architektur-Diagramm bewusst "sacred" und wird bei Updates nie überschrieben – dasselbe Prinzip, das ich in Abschnitt 5.3 für die SQLite-Datenbank des Terminal-Daemons und in Abschnitt 4 für die Locks empfehle. Konsistent zueinander, kein Widerspruch.

### 9.4 Einordnung: CloudGate und Tailscale ergänzen sich, statt zu konkurrieren

Tailscale bleibt als privates Mesh für SSH-Administration und die direkte Terminal-Verbindung von deinen eigenen Geräten sinnvoll (niedrigere Latenz, kein Umweg über Cloudflare, wenn beide Seiten im selben Tailnet sind). CloudGate/Cloudflare Tunnel übernimmt ergänzend, was Tailscale strukturell nicht abdeckt:

- **Web-Client**: Ein Browser kann nicht einfach Mitglied eines WireGuard-Mesh werden – dafür braucht es ohnehin einen klassischen HTTPS-Endpunkt, den CloudGate liefert.
- **Zugriff ohne installiertes Tailscale** (fremdes Gerät, restriktives Netzwerk): CloudGate als Weg über reines HTTPS/443, das praktisch überall durchgelassen wird, plus impliziten DDoS-Schutz durch Cloudflares Edge.
- **Datei-API und Owner-Login-Portal** (Abschnitt 10): brauchen eine stabile, öffentlich erreichbare HTTPS-Origin mit echtem Zertifikat – dafür trägst du sie einfach als weiteren Host in CloudGate ein.

### 9.5 Kritische Anforderung: feste Domain statt IP

Für Passkeys/WebAuthn (Abschnitt 10) ist eine **feste Domain mit gültigem TLS-Zertifikat zwingend** – WebAuthn bindet jeden Passkey an eine Relying-Party-ID, die eine Domain sein muss, keine IP-Adresse. CloudGate liefert das ohnehin über die automatisch angelegten CNAME-Einträge, du musst nur die Subdomain für den Owner-Login-Endpunkt (z. B. `remote.deine-domain.de`) einmal festlegen und danach nicht mehr ändern – ein späterer Domainwechsel würde alle registrierten Passkeys ungültig machen.

### 9.6 Mögliche Synergie: WebAuthn direkt in CloudGate statt separatem Portal

CloudGate bringt bereits Argon2id-Passwort-Hashing und TOTP-2FA für seinen eigenen Admin-Login mit, nur Passkeys/WebAuthn fehlen noch. Da es sich um dein eigenes MIT-lizenziertes Projekt im Pre-Alpha-Stadium handelt, ist eine echte Alternative zum separaten Owner-Login-Portal aus Abschnitt 10.2: **WebAuthn direkt als Modul in CloudGate ergänzen** und den Pairing-Flow für ClaudeCode Remote über CloudGates ohnehin vorhandenen Login laufen lassen, statt eine zweite, parallele Auth-Schicht zu bauen und zu pflegen. Das ist eine echte Designentscheidung mit Vor- und Nachteilen (eine Login-Oberfläche für beide Projekte vs. sauberere Trennung der Verantwortlichkeiten) – siehe die offenen Punkte in Abschnitt 12.

---

## 10. Mehrstufiges Sicherheitskonzept

### 10.1 Zwei unterschiedliche Situationen, zwei unterschiedliche Anforderungen

E-Mail+Passwort **und** TOTP **und** Passkey bei jeder einzelnen Aktion zu verlangen, wäre in der Praxis unbenutzbar – niemand will das bei jedem Tastendruck in einem Terminal durchlaufen. Sinnvoll aufgeteilt in:

- **Geräte-Pairing** (selten, sicherheitskritischste Aktion: hier wird einem neuen Gerät dauerhafter Zugriff gewährt) → volle Kette aus allen drei Faktoren.
- **Laufender Zugriff eines bereits gepaarten Geräts** (häufig, muss flüssig nutzbar sein) → das Ed25519-Challenge-Response-Verfahren aus Abschnitt 5.4, das für sich bereits ein starker Besitzfaktor ist.
- **Step-Up-Auth für sensible Einzelaktionen** (Gerät widerrufen, neues Gerät pairen, Backup löschen, Sicherheitseinstellungen ändern) → auch bei einem bereits gepaarten Gerät ein erneuter Passkey-Prompt, mittlerer Reibungsgrad für die wirklich folgenreichen Aktionen.

Eine ehrliche Einordnung dazu: Ein Passkey ist für sich genommen bereits ein Mehrfaktor-Nachweis (Besitz des Geräts plus Biometrie/PIN zur Freigabe) und gilt zusätzlich als phishingresistent, weil er an die Origin-Domain gebunden ist. Passwort, TOTP und Passkey **zusätzlich zueinander** zu stapeln, geht über gängige Best Practice hinaus – die meisten modernen Systeme ersetzen Passwort+2FA durch einen Passkey, statt beides zu kombinieren. Für ein selbst gehostetes System mit sehr wenigen, dir bekannten Nutzern und hohem Schutzbedürfnis (voller Serverzugriff, Code, Backups) ist die zusätzliche Redundanz aber vertretbar, solange sie auf das Pairing und wirklich sensible Aktionen begrenzt bleibt und nicht den Alltagsbetrieb belastet.

### 10.2 Owner-Login-Portal (Pairing-Flow im Detail)

Eine kleine, separate Web-Route auf derselben CloudGate-Domain (z. B. `remote.deine-domain.de/owner`), ausschließlich für den Pairing-Vorgang:

1. **E-Mail + Passwort**: Passwort serverseitig mit Argon2id gehasht gespeichert (nicht bcrypt – Argon2id ist der aktuelle Empfehlungsstandard für Passwort-Hashing und resistenter gegen GPU-gestützte Angriffe). Rate-Limiting: nach 5 Fehlversuchen exponentiell steigende Sperrzeit pro IP **und** pro Account, nie unbegrenzte Versuche.
2. **TOTP-Code** (RFC 6238): Secret verschlüsselt in der Datenbank abgelegt, Verifikation über eine Standardbibliothek (z. B. `pquerna/otp` in Go), funktioniert mit jeder gängigen Authenticator-App.
3. **Passkey-Ceremony** (WebAuthn): serverseitig über `go-webauthn/webauthn`, clientseitig über die Browser-WebAuthn-API bzw. in der App über die Plattform-Bridges aus 10.3.
4. Erst nach erfolgreichem Abschluss aller drei Schritte wird der Ed25519-Geräte-Schlüssel aus dem Pairing-Code (Abschnitt 5.4) tatsächlich in die `devices`-Tabelle aufgenommen – vorher bleibt das Gerät im Zustand "wartet auf Freigabe".

### 10.3 Passkeys in der Flutter-App: Plattform-Realität

Flutter hat (Stand Mitte 2026) kein natives First-Party-Passkey-API – die Anbindung läuft über Community-Plugins, die auf die jeweilige Plattform-API durchreichen:

| Plattform | Anbindung | Reifegrad |
|---|---|---|
| Android | Credential Manager API (ab Android 14 stabil) | gut |
| Windows | Windows Hello / WebAuthn-Plattform-Authenticator | gut |
| Web | native Browser-WebAuthn-API | gut, Standardfall |
| Linux Desktop | kein ausgereiftes natives Plattform-Passkey-API | schwächstes Glied |

Für Linux empfehle ich deshalb explizit **nicht** auf ein rein plattformintegriertes Passkey-Erlebnis zu setzen, sondern zusätzlich einen **FIDO2-Hardware-Key** (z. B. YubiKey über USB/NFC) als roaming Authenticator zu unterstützen – der funktioniert über WebAuthn identisch auf allen vier Plattformen und schließt damit die Linux-Lücke, ohne dass du auf ein noch unreifes Software-Backend warten musst.

### 10.4 Sicherheitsübersicht (aktualisiert)

| Ebene | Maßnahme |
|---|---|
| Transport (Mesh) | Tailscale/WireGuard für eigene Geräte, kein offener Port im Heimnetz |
| Transport (Internet) | CloudGate als ausgehend initiierter Relay-Tunnel, feste Domain, Let's-Encrypt-TLS |
| SSH | Nur Keys, kein Passwort, dedizierter Port, fail2ban, getrennte User für Admin/Agent |
| Geräte-Pairing | E-Mail+Passwort (Argon2id) + TOTP + Passkey/WebAuthn, danach Ed25519-Geräteschlüssel |
| Laufender Zugriff | Ed25519-Challenge-Response pro Verbindung, kein erneutes Passwort nötig |
| Sensible Einzelaktionen | Step-Up-Passkey-Prompt (Gerät widerrufen, neues Pairing, Backup löschen) |
| Dateifreigaben | SMB3 verschlüsselt, ausschließlich im Tailscale-Interface, nie öffentlich |
| Datei-Uploads | SHA-256-Hash vor dem Senden und nach dem Empfang, Chunked/Resumable (tus) |
| Agenten | Laufen nie als root, Wrapper-Skript statt manuell verwalteter Locks |
| Updates | Nur bei nachweislich idle, Kernel-Reboots nie automatisch |
| GitHub | Maschinengebundener Key, kein Agent-Forwarding |

---

## 11. Implementierungs-Roadmap

1. **Server-Fundament**: Arch-Installation, User-Trennung, SSH-Härtung, Tailscale, GitHub-Key.
2. **Agent-Stack**: Claude Code/Codex/Antigravity-CLI installieren, Symlink-Mechanismus + `agentctl init` + Git-Hooks.
3. **Idle-Update-System**: Lock-Verzeichnis, `agent-run`-Wrapper, systemd-Timer, Logging/ntfy.
4. **Terminal-Daemon MVP**: Go-Service mit rohem PTY-Passthrough über `tmux attach-session`, REST+WebSocket, zunächst nur mit einem simplen Web-Client (`xterm.js`) testen, um das Protokoll zu validieren, bevor die volle App gebaut wird.
5. **Dateien & Backups**: Samba-Freigaben (`backups`/`exchange`), restic-Timer, File-API-Endpunkte im Daemon inklusive Hash-Verifikation.
6. **CloudGate-Anbindung**: CloudGate per One-Liner oder `docker run` installieren, Cloudflare-API-Token hinterlegen, Terminal-Daemon und Owner-Login-Portal als Hosts im **Tunnel-Modus** eintragen (nicht lokaler nginx-Modus, wegen CGNAT), feste Subdomain für WebAuthn festlegen.
7. **Sicherheits-Layer**: E-Mail/Passwort + TOTP + Passkey-Pairing-Flow, Ed25519-Geräteauthentifizierung, Step-Up-Auth für sensible Aktionen.
8. **Client-App Grundgerüst**: Flutter-Projekt für Linux/Windows/Web zuerst (einfacher zu debuggen als Mobile), Session-Liste + Terminal-View + Reconnect-Logik + Datei-Browser.
9. **Android + Feinschliff**: mobile Steuerleiste, native Copy-Paste-UX, Share-Sheet-Integration für Datei-Uploads, Benachrichtigungen, FIDO2-Hardware-Key-Support für Linux.

---

## 12. Offene Entscheidungen für dich

- Tailscale (einfacher, empfohlen) vs. eigenes WireGuard-Setup (mehr Kontrolle, mehr Wartungsaufwand) für den privaten Mesh-Teil – CloudGate übernimmt ohnehin den öffentlichen Teil, unabhängig von dieser Wahl.
- Ob der Server als eigene Bare-Metal-Maschine läuft oder als VM auf deinem bestehenden Proxmox-Host – Letzteres würde dir Snapshots vor größeren Updates ermöglichen, was besonders für das automatische Update-System ein sinnvolles Sicherheitsnetz wäre.
- Ob `reboot-pending` rein informativ bleibt (Push-Benachrichtigung, du rebootest manuell) oder ein wöchentliches Zeitfenster für automatische Reboots bekommt.
- Welche Domain dauerhaft als WebAuthn-Relying-Party dienen soll, da ein späterer Wechsel alle Passkeys ungültig macht (Abschnitt 9.5) – wird als CloudGate-Host mit CNAME angelegt, also vor dem Passkey-Rollout final entscheiden.
- Ob WebAuthn/Passkeys direkt als Modul in CloudGate ergänzt werden (eine Login-Oberfläche für CloudGate und ClaudeCode Remote) oder als separates Owner-Login-Portal daneben entstehen (Abschnitt 9.6) – reine Architekturfrage, beides ist technisch sauber möglich.
- Ob dir die fehlende Lock-Kopplung von CloudGates 6-stündigem Selbst-Update (Abschnitt 9.3) wichtig genug ist, um sie als Feature in dein eigenes CloudGate-Projekt nachzurüsten, oder ob das kurze Reload-Fenster für dich vernachlässigbar ist.
- Ob die volle Drei-Faktor-Kette wirklich bei jedem Geräte-Pairing greifen soll, oder ob du für vertraute Situationen (z. B. ein neues Gerät im eigenen Heimnetz) eine reduzierte Stufe zulassen willst.
- MD5 vs. SHA-256 für die Datei-Hashes (Abschnitt 8.2) – ich empfehle SHA-256, respektiere aber MD5 als bewusste Wahl, falls dir die etwas geringere Rechenlast wichtiger ist als die theoretische Kollisionsresistenz.
