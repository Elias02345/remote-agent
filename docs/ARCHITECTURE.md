# ClaudeCode Remote – Implementation Documentation

**Version 0.1 · Concept and architecture document**

## 1. Goal and Architecture Overview

ClaudeCode Remote consists of three independent but tightly interlocking layers:

1. **Server base** – a hardened Arch Linux server with a fixed IP that serves as the central "agent base station": coding agents (Claude Code, Codex, Antigravity CLI), code, Git access, and an autonomous idle update system.
2. **Terminal session daemon** – a service running on the server that manages persistent terminal sessions and exposes them to clients via a WebSocket/REST API. This is the technical heart of the system, since this is where "sessions survive SSH drops and device switches" gets implemented.
3. **Client apps** – native applications for Android, Windows, Linux, and Web that connect to the daemon and manage terminals like tabs.

Simplified data flow:

```
[Android/Windows/Linux/Web App]
        │  (WebSocket + REST, TLS, device auth)
        │  ── own devices in the Tailscale mesh: direct ──┐
        │  ── web client / foreign networks: via CloudGate ─┤
        ▼                                                    ▼
                    [Terminal session daemon on the server]
        │  (raw PTY passthrough via tmux attach-session)
        ▼
[tmux sessions] ── [Claude Code / Codex / Antigravity CLI] ── [Project code + Git]
        │
[Idle update system] ← checks locks from agent runs and open terminals
```

The central design decision: **tmux is the persistence layer, not your own code.** tmux has reliably solved detach/reattach, scrollback, and process survival across SSH drops for years. To achieve this, the daemon passes raw terminal bytes through unchanged (see 5.1) and only builds a structured API and an auth/sync model on top of it, instead of reinventing PTY management or terminal interpretation.

---

## 2. Server Base (Arch Linux)

### 2.1 Base installation and user concept

- A dedicated, non-root user `agent` for all coding agents and terminal sessions. No agent ever runs as root.
- `sudo` for `agent` only for exactly defined commands (e.g. `systemctl restart claudecode-daemon`), via `/etc/sudoers.d/agent` with `NOPASSWD` limited to this whitelist.
- A separate `admin` user for SSH login and server administration, kept apart from the `agent` user, so that a compromised agent session doesn't automatically come with full SSH privileges.

### 2.2 SSH hardening

`/etc/ssh/sshd_config` key points:

```
PasswordAuthentication no
PermitRootLogin no
PubkeyAuthentication yes
AllowUsers admin agent
Port 2222
```

Additionally:

- `fail2ban` with an sshd jail against brute-force attacks.
- SSH keys exclusively Ed25519, one dedicated key pair per authorized device (no shared key), so individual devices can be revoked selectively via `authorized_keys`.
- **No port forwarding on the router.** See 2.4 – access exclusively via a private overlay network.

### 2.3 GitHub authentication

- A machine-bound Ed25519 key is registered as an SSH key on the GitHub account (not as a per-repo deploy key, since multiple projects are managed).
- `~/.ssh/config` with `IdentitiesOnly yes`, so no key leaking happens between services.
- No agent forwarding (`ForwardAgent no`) – the key stays physically on the server, which fits exactly with the concept "the server holds all the code."

### 2.4 Network access: overlay network instead of open ports

Instead of exposing SSH and the app ports directly to the internet via port forwarding, I recommend a private mesh network:

- **Tailscale** (or self-hosted: Headscale) on the server and on every client device. The server gets a stable tailnet IP; SSH and the terminal daemon bind exclusively to this interface.
- Advantages: end-to-end encryption is already solved via WireGuard (the basis of Tailscale), NAT traversal works automatically, individual devices can be granted or immediately revoked per ACL, and it runs natively on Android, Windows, Linux and – via a subnet router – also in the web context.
- Since you already have Cloudflare reverse-proxy experience: as an alternative, a **Cloudflare Tunnel** with Cloudflare Access also works for the web variant of the app, if you'd rather stick with your existing setup. For the native terminal communication (high-frequency WebSocket), Tailscale is nonetheless the simpler and more robust choice, because there's no public endpoint that needs to be secured.

This decision seems like a side note at first, but it's the reason the app later won't need to build its own PKI system – the overlay network handles transport encryption, and the app layer only has to do device authorization (see 5.4).

---

## 3. Agentic Coding Stack

### 3.1 Installation

Claude Code, Codex, and Antigravity CLI are installed system-wide for the `agent` user (each following the current vendor installation instructions, since package names/managers can change). What matters is only the convention afterward: all three are started via **one shared wrapper** (see 4.2), never directly.

### 3.2 CLAUDE.md as single source of truth

Your idea to have `AGENTS.md` and `ANTIGRAVITY.md` "mount" from `CLAUDE.md` is most robustly solved via **symlinks**, not via real bind-mounting – text files are read transparently by every tool, a bind mount would be unnecessarily complex and would cause problems with container/chroot usage.

Per project root:

```
CLAUDE.md            # canonical source, gets edited
AGENTS.md -> CLAUDE.md
ANTIGRAVITY.md -> CLAUDE.md
```

A small init script automates this:

```bash
#!/usr/bin/env bash
# agentctl init – run in the project root
set -e
[ -f CLAUDE.md ] || { echo "CLAUDE.md missing"; exit 1; }
ln -sf CLAUDE.md AGENTS.md
ln -sf CLAUDE.md ANTIGRAVITY.md
echo "Agent context files linked."
```

You additionally hook this script in as a `post-checkout` and `post-merge` Git hook, so freshly cloned or updated repos automatically get the symlinks without you having to intervene manually.

For machine-wide conventions (coding style, security rules that apply to all projects) you additionally set up `~/.claude/CLAUDE.md` as a global layer – Claude Code already reads project-specific and global layers hierarchically anyway; Codex and Antigravity get this global file mirrored into their respective home directories via the same symlink mechanism.

---

## 4. Autonomous Idle Update System

This is the part where the reliability of the lock logic is decisive – a stuck lock must never block updates forever, and a forgotten agent run must never be killed mid-update.

### 4.1 Two lock types, different lifetimes

- **Agent lock** (`/run/claudecode-locks/agent-<pid>`): created automatically whenever a coding agent is running. Must **never get stuck permanently** – if an agent crashes, the lock still has to disappear eventually.
- **Terminal lock** (`/run/claudecode-locks/terminal-<session-id>`): created as soon as a session exists in the terminal daemon. Deliberately persists **indefinitely** until the user manually closes the terminal – this is explicitly desired behavior from your specification, not a bug.

### 4.2 Automatic wrapper instead of "the agent has to remember"

Your original idea was that the agent itself, by instruction, would know to start and stop the lock process. That works, but it's fragile: if the agent crashes or forgets the instruction (e.g. on a context reset), the lock stays forever and permanently blocks updates. More robust is a **transparent shell wrapper** that sets the lock automatically and reliably removes it again even on errors via `trap ... EXIT`:

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

As shell aliases for the `agent` user:

```bash
alias claude='agent-run claude'
alias codex='agent-run codex'
alias antigravity='agent-run antigravity-cli'
```

This means no agent instance has to "know" that it's managing a lock – it happens transparently on every invocation. If you still want the agent to understand it explicitly (e.g. for very long-running manual shell sessions outside the wrapper), add a note in `CLAUDE.md` such as: *"Long-running background processes outside of an agent invocation must manually set `touch /run/claudecode-locks/agent-manual-<name>` and remove it again on exit."* That remains useful as a fallback, but does not replace the automatic wrapper as the primary mechanism.

Against locks left behind by hard crashes (`SIGKILL`, power outage), the update check additionally cleans up **only agent locks** that are older than, say, 6 hours – terminal locks are deliberately excluded from this, since they're meant to stay valid indefinitely.

### 4.3 systemd timer

```ini
# /etc/systemd/system/idle-updater.timer
[Unit]
Description=Idle-based system update timer

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
Description=Checks idle status and updates the system

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

# Automatically remove stale agent locks (>6h), do NOT touch terminal locks
find "$LOCK_DIR" -name 'agent-*' -mmin +360 -delete 2>/dev/null || true

if [ -n "$(ls -A "$LOCK_DIR" 2>/dev/null)" ]; then
    logger -t idle-updater "Update skipped, active locks: $(ls "$LOCK_DIR" | tr '\n' ' ')"
    exit 0
fi

logger -t idle-updater "System idle – starting update"
pacman -Syu --noconfirm
command -v paru >/dev/null 2>&1 && paru -Syu --noconfirm --sudoloop || true

for tool in claude-code codex antigravity-cli; do
    npm update -g "$tool" >/dev/null 2>&1 || true
done

# Kernel update detected? Do not reboot automatically, just set a flag.
CURRENT_KERNEL="$(uname -r)"
INSTALLED_KERNEL="$(pacman -Q linux | awk '{print $2}')"
mkdir -p /var/lib/claudecode
if [[ "$INSTALLED_KERNEL" != *"${CURRENT_KERNEL%-arch*}"* ]]; then
    touch /var/lib/claudecode/reboot-pending
fi

[ -n "${NTFY_URL:-}" ] && curl -s -d "ClaudeCode Remote: update completed ($(date '+%d.%m %H:%M'))" "$NTFY_URL" >/dev/null || true
logger -t idle-updater "Update completed"
```

Important: the reboot flag is deliberately placed **outside** of `LOCK_DIR` – if it were inside, it would count itself as an active lock and block all future updates.

### 4.4 Reboot handling

An automatic reboot on idle is risky, since it would also kill old, "forgotten" tmux sessions even when no lock is currently active. Recommendation: **no automatic reboot**; instead a separate, much rarer timer (e.g. weekly, at a fixed time window such as 4 a.m.) that only reboots when `reboot-pending` is set **and** zero terminal sessions exist (not just zero locks, but an actual query to the daemon whether any sessions are open at all).

### 4.5 Notification

`ntfy` (self-hosted or ntfy.sh) as a simple push channel into the app/phone: update summary, notice of pending reboots, error messages for failed updates.

---

## 5. Terminal Session Daemon (Core Piece)

### 5.1 Architecture decision

- Language: **Go** – a mature PTY ecosystem (`creack/pty`), good WebSocket libraries (`gorilla/websocket`), a simple concurrency model for many parallel streams, compiles to a single binary with no runtime dependencies, which makes running it as a systemd service trivial.
- **Correction versus an earlier version of this doc:** tmux control mode (`-CC`) is a proprietary protocol tailored to iTerm2 for pane-layout synchronization – not a raw byte stream. Full-screen TUIs like Claude Code (an Ink-based UI with an alternate screen buffer, its own redraw cycle, box-drawing frames) would not arrive correctly through it. Instead, the daemon does **raw PTY passthrough**: for every client connection it starts `tmux attach-session -t <id>` inside its own PTY (`creack/pty`) and forwards the raw bytes unchanged, bidirectionally, over the WebSocket – no server-side interpretation, no parsing.
- The client takes over interpretation entirely via a real terminal emulator library (`xterm.dart` in the app, `xterm.js` for a possible web test client) – exactly the same emulation layer that also correctly renders vim, htop, or tmux itself. This is not a special case, but the same mechanism already used in production by ttyd, gotty, Upterm, and Termius.
- For correct rendering, the following points must be right in the client emulator and in the `TERM` environment variable: `TERM=tmux-256color` (or `xterm-256color`), alternate-screen-buffer switching (`?1049h`/`?1049l`) for clean restoration when leaving full-screen mode, truecolor escape codes, cursor visibility (`?25l`/`?25h`), and correct width calculation for Unicode/box-drawing characters. `xterm.dart`/`xterm.js` already support all of this natively.
- When a client connects, the daemon first delivers the current screen content via `tmux capture-pane -p -e` (the `-e` preserves the color/escape codes), then streams live afterward – this way the app immediately sees the current state without having to replay the entire scrollback.
- **Resize with several simultaneously connected devices:** by default, tmux resizes a session to match the smallest attached window size – if you had the same session open simultaneously on your phone and on the desktop, the Claude Code layout would get squeezed to phone width. The solution is the native tmux option `set -g window-size latest`: the session always adapts to the device that most recently sent active input, while all other devices see a scaled snapshot instead of the layout actively being distorted.

### 5.2 API surface

REST (session management):

```
GET    /sessions              → list all open terminals
POST   /sessions               → new terminal (shell, cwd, name)
DELETE /sessions/{id}          → close terminal manually (the only close method)
PATCH  /sessions/{id}          → rename
```

WebSocket (live interaction):

```
/sessions/{id}/stream
  → Client sends: {"type":"input","data":"..."} | {"type":"resize","cols":n,"rows":n}
  ← Server sends: {"type":"output","data":"..."} | {"type":"exit"}
```

### 5.3 Data model (SQLite)

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

`status='closed'` is set exclusively by a user's explicit `DELETE` action – your principle of "manually open and close" is thereby anchored directly in the data model, not just in the UI.

### 5.4 Device authentication (pairing instead of password)

Instead of passwords or static tokens, a challenge-response procedure analogous to SSH:

1. On first install, the app generates an Ed25519 key pair locally (Android Keystore / Windows Credential Manager / Linux Secret Service / on the web only as a short-lived session key, since browser storage is less trustworthy).
2. The app displays a pairing code.
3. On the server: `claudecode-remote pair <code>` registers the device's public key in the `devices` table.
4. Every further connection signs a random challenge issued by the daemon – no secret ever travels over the wire.
5. Devices can be individually revoked at any time via `claudecode-remote devices` (or a small management view in the app for the main device) (`revoked=1`).

Since the daemon only listens on the Tailscale interface anyway (see 2.4), this is a second, independent security layer – compromising the network alone is not enough for terminal access.

---

## 6. Cross-Platform Client App

### 6.1 Tech stack

**Flutter** for Android, Windows, Linux, and Web from a single codebase – this is the point where the investment pays off the most, since otherwise you'd have to maintain at least two codebases (e.g. Tauri/web frontend for desktop+web, plus a separate Kotlin/Compose app for Android). Terminal rendering via `xterm.dart` (VT100/256-color emulation), which uses the same engine logic as `xterm.js` on the web.

Alternative, if native desktop performance later becomes more important to you than a single codebase: Tauri + Svelte/React with `xterm.js` for Windows/Linux/Web, combined with a separate native Android app. For getting started and for a single developer, though, Flutter is the more pragmatic path to your goal of "works the same everywhere."

### 6.2 UI concept

- **Terminal list** as the home screen, behaving like browser tabs: name, working directory, last activity, status dot (connected/disconnected). New terminal via a clearly visible button.
- **Terminal view**: full emulation, plus on mobile an additional control bar above the keyboard with Esc, Tab, Ctrl (as a toggle for combinations), arrow keys, pipe, slash, and a paste button that reads from the native clipboard – a pattern you probably know from Termux or Blink Shell.
- **Closing is exclusively manual**: via an explicit button with a confirmation dialog, no automatic timeout, no closing on app switch or connection loss.
- **Copy-paste**: via Flutter's cross-platform `Clipboard` API, on mobile additionally press-and-hold with selection handles like native text input, on desktop/web the classic right-click context menu.
- **Reconnect logic**: WebSocket with exponential backoff; on reconnection the daemon immediately delivers the current `capture-pane` state, so no full history reload is needed.

### 6.3 Behavior on connection loss

A visible status banner ("Disconnected – reconnecting…") instead of a silent failure. Important: the app losing connection changes nothing about the state of the session on the server – the tmux session keeps running independently, exactly as with SSH+tmux today, just now with a clean, cross-device interface on top.

---

## 7. File Shares, Backups, and Exchange with the Agent

### 7.1 Two separate shares with different purposes

Instead of a single share for everything, two cleanly separated areas under `/srv/`:

```
/srv/backups/     # automatic backups, relevant to the user only for reading
/srv/exchange/    # bidirectional exchange folder agent ↔ user
```

Both are shared via **Samba (SMB3)** – not NFS. Rationale: SMB3 natively supports transport encryption (`smb encrypt = required`), is natively supported by Windows and by Linux/macOS via standard clients, while NFS without additional Kerberos effort is unencrypted and authorized essentially only by IP – for a share that potentially contains backups and project files, NFSv3's security level is too low for your standards.

Important: **both shares are offered exclusively over the Tailscale interface** (`smb.conf` → `interfaces = tailscale0` + `bind interfaces only = yes`), never over the public interface or via CloudGate (file-sharing protocols like SMB are not built for the open internet, even with encryption). For access on the go without Tailscale, the app-integrated file API from section 8 takes over instead – SMB remains the convenient path for the desktop machine in the mesh.

### 7.2 Automatic backup system

Backup tool: **restic** – deduplicates, encrypts the snapshots client-side (even though they're stored locally on `/srv/backups`, this is worthwhile in case you later add an offsite copy step), and allows incremental, space-efficient snapshots with a simple restore history.

What gets backed up:

- The terminal daemon's SQLite database (sessions, devices, auth state)
- `~/.claude/`, `~/.codex/`, `~/.antigravity/` configurations including the global `CLAUDE.md`
- `/srv/exchange/` contents
- Everything under the project root that is **not** tracked by Git (`.env` files, local build artifacts you deliberately don't want to commit) – via a `.backupignore`-like include list, so that `node_modules` isn't accidentally backed up in its entirety

Important clarification regarding the lock logic from section 4: backups are **not** coupled to the idle locks from the update system. An update can restart services or make the system unstable while it's running – a backup only reads data and writes it elsewhere, which is uncritical even during an active agent run or an open terminal. A separate, more frequent systemd timer (e.g. hourly for the SQLite DB and `/srv/exchange/`, daily for the full project set) therefore runs independently of idle state.

```ini
# /etc/systemd/system/claudecode-backup.timer
[Unit]
Description=Regular restic backup

[Timer]
OnCalendar=hourly
Persistent=true

[Install]
WantedBy=timers.target
```

Don't forget to schedule a restore test: a backup that has never been restored is only a guess – a monthly `restic check` plus a spot-check `restic restore` into a scratch directory belongs in place as its own small timer.

### 7.3 The exchange folder (agent ↔ user)

Instead of a single mixed folder, a clear two-way structure per project, so it's never unclear who put a file there:

```
/srv/exchange/<project>/from-agent/
/srv/exchange/<project>/to-agent/
```

- `to-agent/` is the destination when you want to "share a file for the agent's context" from a client app (see 8.3).
- `from-agent/` is used by the coding agent when it wants to actively hand you something (e.g. a generated image, an export, a report) – this belongs in `CLAUDE.md` as a convention, so all three agents (Claude Code, Codex, Antigravity) know the same path.
- A small `inotifywait`-based watcher on `from-agent/` can optionally trigger an ntfy notification as soon as the agent places something there, so you don't have to discover it by chance.

---

## 8. File Access and File Sharing from the Client Apps

### 8.1 Two access paths for two audiences

- **SMB mount** (section 7.1) for convenient access from the Windows/Linux desktop within the Tailscale mesh – no extra code needed, works with any file manager.
- **App-integrated file API** for all four platforms equally, because Android and Web don't offer a practical native SMB mount experience, and because this is the path through which files get shared specifically **for a particular terminal session**.

The file API is an extension of the same daemon from section 5, not a separate service:

```
GET    /files?path=...              → list directory contents
GET    /files/download?path=...     → download file (streamed, with hash header)
POST   /files/upload                → chunked upload with hash verification (see 8.2)
DELETE /files?path=...               → delete file
```

Access is restricted to `/srv/exchange/` and explicitly shared project directories (allowlist, no free filesystem browser across the whole server) – the daemon runs as the `agent` user and structurally cannot read or write outside these paths.

### 8.2 Integrity checking: hash before sending, hash after receiving

Your requirement – complete and redundant, with a checksum before sending and after receiving – is implemented as follows:

1. The client computes the file's hash locally **before** the upload begins.
2. The upload happens in chunks (important for larger files over mobile data – an interruption at 95% shouldn't mean the entire file has to be transferred again). The open **tus resumable upload protocol** (`tus.io`) is well suited as a protocol foundation, since it standardizes exactly this chunking/resume behavior instead of reinventing it.
3. After the file is fully received, the server computes the same hash over the written file and compares it against the value sent by the client. On a mismatch, the file is discarded and the client is asked to resend, instead of silently keeping a potentially corrupt file.
4. The server sends its computed hash back in the response, and the client compares it a second time against its own value – this additionally covers errors that only arise in the response itself (a man-in-the-middle tampering would already be ruled out by TLS/the overlay network, but the double comparison is a cheap additional safeguard against transmission errors).

A note on the choice of hash algorithm: you had suggested MD5, which is entirely sufficient and fast for pure **corruption detection** (broken transmission, faulty storage). But for a project that's designed for maximum security anyway, I would set **SHA-256** as the standard – MD5 is considered cryptographically broken (collisions are practically producible), which is usually irrelevant for pure integrity checking by itself, but as soon as someone with write access to an intermediate point could deliberately construct a collision, MD5 would be the weaker choice. SHA-256 costs no noticeable extra overhead on today's hardware, and you won't have to remember later the distinction of "what is MD5 still okay for."

### 8.3 Sharing a file directly into the agent's context

This is the workflow that concretely fulfills your "fully functional" requirement:

1. In the app, within an open terminal session, you select the "Share file" menu item – either from the device's own filesystem (Android: native share-sheet entry "Send to ClaudeCode Remote" from any app, Desktop/Web: file picker or drag & drop) or from the server file browser itself (e.g. feeding an already existing file from `from-agent/` into another session again).
2. The app uploads the file following the procedure from 8.2 into `to-agent/` of the current session's assignment (`/srv/exchange/<project>/to-agent/<filename>`).
3. Optionally – configurable per session – the daemon then automatically writes a line into the terminal input stream, e.g. `# New file available: to-agent/photo.png`, so the agent sees the context immediately without you having to type it yourself. By default I would implement this as a suggestion rather than automatic input (a hint overlay with an "Insert into terminal" button), so that text is never written unintentionally into a running input of the agent while it's typing something itself.

### 8.4 File browser and server control in the app

A dedicated "Server" tab next to the terminal list, with:

- Directory tree via the file API (8.1), download with progress indicator, upload via drag & drop (Desktop/Web) or file/photo selection (Android).
- Shortcuts for recurring server actions that run over the same REST layer as terminal management and not over a separate privilege tier: trigger a backup now, view update status/`reboot-pending`, view active locks, manage the device list (see 10.4).
- These shortcuts are deliberately implemented as predefined, vetted actions instead of free-form command entry – remote control via an app button should never be able to execute arbitrary shell commands with admin rights, only the explicitly designated, narrow endpoints.

---

## 9. CGNAT and CloudGate as Intermediary

### 9.1 Why this is even a problem

A "fixed IP" from the provider is, for many German connections (especially mobile or some cable contracts), nonetheless behind **Carrier-Grade NAT (CGNAT)** – the IP your router shows is not the publicly routable address, but is itself behind another NAT layer of the provider, shared among several customers. Classic port forwarding on the router accomplishes nothing in this case, because the incoming traffic never reaches the router. CloudGate, by its own description, exists explicitly for exactly this case.

### 9.2 What CloudGate actually does

Important correction versus the previous version of this section: CloudGate is **not its own relay server that you have to operate yourself.** It is a WebUI that orchestrates **Cloudflare Tunnel** (`cloudflared`). The actual relay function – a publicly reachable node to which your server holds an open outgoing connection – is handled by **Cloudflare's own, global edge network**, not by CloudGate itself and not by any VPS you rent. This is structurally the same basic mechanism I described generically before (outgoing connection instead of port forwarding), except that the relay point already exists ready-made and is operated by Cloudflare.

CloudGate itself runs as a Docker container on your machine (bundled nginx on port 80/443 locally + a Node/TypeScript backend on port 3000) and automates the rest:

1. You store a Cloudflare API token in the WebUI once.
2. For every service you want to publish, you only enter `internal-IP:port` and the desired subdomain (e.g. `127.0.0.1:8080` → `remote.your-domain.com`).
3. CloudGate automatically creates the Cloudflare Tunnel, the DNS CNAME, the ingress rule, and reloads `cloudflared` – according to the repo this takes about 30 seconds from entry until the service is live.

CloudGate additionally supports a **hybrid mode**: per host, you choose "via Cloudflare Tunnel" or "local nginx reverse proxy." The local nginx mode requires a reachable public IP – which, due to CGNAT, you currently don't have, so you'll use **tunnel mode** for all ClaudeCode Remote services.

### 9.3 Practical consequences for your own setup

- **TLS termination is handled by Cloudflare's edge**, not your server. The terminal daemon from section 5 therefore doesn't need to manage its own certificate at all – it can listen internally on plain HTTP (bound to `localhost` or the Tailscale interface, see 9.4), and `cloudflared` passes the connection through from the public HTTPS hostname to it. This noticeably simplifies the daemon compared to my original assumption that it would need to build its own Let's Encrypt handling.
- **WebSocket works over Cloudflare Tunnel** basically without issues – this is an established, frequently used use case (among others for similar terminal/remote tools). For the lowest latency, the direct Tailscale connection between your own devices and the server is nonetheless still preferable (see 9.4) – Cloudflare Tunnel is the path for the web client and foreign networks, not necessarily the fastest path for your everyday use from your own machine.
- **CloudGate's self-update** (GitHub polling every 6 hours, GPG-signed, atomic rollback) runs independently of the idle lock logic from section 4 – that's a standalone system for a different container. Since a CloudGate update potentially reloads `cloudflared` briefly, an open terminal connection over the tunnel would theoretically be briefly affected for the moment of the reload. CloudGate currently offers no lock mechanism for this – if that bothers you, it would be a sensible feature request or a small PR of your own in your own project, not a blocker for the rest of the architecture.
- CloudGate's `/data` directory is, according to the architecture diagram, deliberately "sacred" and is never overwritten during updates – the same principle I recommend in section 5.3 for the terminal daemon's SQLite database and in section 4 for the locks. Consistent with each other, no contradiction.

### 9.4 Positioning: CloudGate and Tailscale complement rather than compete

Tailscale remains useful as a private mesh for SSH administration and the direct terminal connection from your own devices (lower latency, no detour via Cloudflare when both sides are in the same tailnet). CloudGate/Cloudflare Tunnel additionally covers what Tailscale structurally doesn't:

- **Web client**: a browser can't simply become a member of a WireGuard mesh – that requires a classic HTTPS endpoint anyway, which CloudGate provides.
- **Access without Tailscale installed** (foreign device, restrictive network): CloudGate as a path over plain HTTPS/443, which is let through almost everywhere, plus implicit DDoS protection via Cloudflare's edge.
- **File API and owner login portal** (section 10): need a stable, publicly reachable HTTPS origin with a real certificate – for that you simply enter them as another host in CloudGate.

### 9.5 Critical requirement: fixed domain instead of IP

For Passkeys/WebAuthn (section 10), a **fixed domain with a valid TLS certificate is mandatory** – WebAuthn binds every passkey to a relying-party ID, which must be a domain, not an IP address. CloudGate provides this anyway via the automatically created CNAME entries; you just need to settle on the subdomain for the owner login endpoint once (e.g. `remote.your-domain.com`) and not change it afterward – a later domain change would invalidate all registered passkeys.

### 9.6 Possible synergy: WebAuthn directly in CloudGate instead of a separate portal

CloudGate already brings Argon2id password hashing and TOTP 2FA for its own admin login; only passkeys/WebAuthn are still missing. Since this is your own MIT-licensed project in pre-alpha stage, a real alternative to the separate owner login portal from section 10.2 is: **add WebAuthn directly as a module in CloudGate** and route the pairing flow for ClaudeCode Remote through CloudGate's already-existing login, instead of building and maintaining a second, parallel auth layer. This is a genuine design decision with pros and cons (one login interface for both projects vs. cleaner separation of responsibilities) – see the open items in section 12.

---

## 10. Multi-Layered Security Concept

### 10.1 Two different situations, two different requirements

Requiring email+password **and** TOTP **and** passkey on every single action would be unusable in practice – nobody wants to go through that on every keystroke in a terminal. Sensibly split into:

- **Device pairing** (rare, the most security-critical action: this is where a new device is granted permanent access) → the full chain of all three factors.
- **Ongoing access from an already paired device** (frequent, must be smooth to use) → the Ed25519 challenge-response procedure from section 5.4, which by itself is already a strong possession factor.
- **Step-up auth for sensitive individual actions** (revoke a device, pair a new device, delete a backup, change security settings) → even for an already paired device, a renewed passkey prompt, a medium friction level for the actions with real consequences.

An honest assessment of this: a passkey by itself is already a multi-factor proof (possession of the device plus biometrics/PIN for release) and is additionally considered phishing-resistant because it's bound to the origin domain. Stacking password, TOTP, and passkey **on top of each other** goes beyond common best practice – most modern systems replace password+2FA with a passkey instead of combining both. For a self-hosted system with very few, known users and a high protection requirement (full server access, code, backups), the additional redundancy is nonetheless justifiable, as long as it stays limited to pairing and truly sensitive actions and doesn't burden everyday operation.

### 10.2 Owner login portal (pairing flow in detail)

A small, separate web route on the same CloudGate domain (e.g. `remote.your-domain.com/owner`), used exclusively for the pairing process:

1. **Email + password**: password stored server-side hashed with Argon2id (not bcrypt – Argon2id is the current recommended standard for password hashing and more resistant against GPU-assisted attacks). Rate limiting: after 5 failed attempts, exponentially increasing lockout time per IP **and** per account, never unlimited attempts.
2. **TOTP code** (RFC 6238): secret stored encrypted in the database, verification via a standard library (e.g. `pquerna/otp` in Go), works with any common authenticator app.
3. **Passkey ceremony** (WebAuthn): server-side via `go-webauthn/webauthn`, client-side via the browser WebAuthn API or, in the app, via the platform bridges from 10.3.
4. Only after all three steps have completed successfully is the Ed25519 device key from the pairing code (section 5.4) actually added to the `devices` table – before that, the device stays in a "waiting for approval" state.

### 10.3 Passkeys in the Flutter app: platform reality

Flutter has (as of mid-2026) no native first-party passkey API – the integration goes through community plugins that pass through to the respective platform API:

| Platform | Integration | Maturity |
|---|---|---|
| Android | Credential Manager API (stable since Android 14) | good |
| Windows | Windows Hello / WebAuthn platform authenticator | good |
| Web | native browser WebAuthn API | good, standard case |
| Linux Desktop | no mature native platform passkey API | weakest link |

For Linux, I therefore explicitly recommend **not** relying on a purely platform-integrated passkey experience, but additionally supporting a **FIDO2 hardware key** (e.g. a YubiKey over USB/NFC) as a roaming authenticator – it works identically via WebAuthn on all four platforms and thereby closes the Linux gap, without you having to wait for a still-immature software backend.

### 10.4 Security overview (updated)

| Layer | Measure |
|---|---|
| Transport (mesh) | Tailscale/WireGuard for own devices, no open port on the home network |
| Transport (internet) | CloudGate as an outbound-initiated relay tunnel, fixed domain, Let's Encrypt TLS |
| SSH | Keys only, no password, dedicated port, fail2ban, separate users for admin/agent |
| Device pairing | Email+password (Argon2id) + TOTP + Passkey/WebAuthn, then Ed25519 device key |
| Ongoing access | Ed25519 challenge-response per connection, no repeated password needed |
| Sensitive individual actions | Step-up passkey prompt (revoke device, new pairing, delete backup) |
| File shares | SMB3 encrypted, exclusively on the Tailscale interface, never public |
| File uploads | SHA-256 hash before sending and after receiving, chunked/resumable (tus) |
| Agents | Never run as root, wrapper script instead of manually managed locks |
| Updates | Only when demonstrably idle, kernel reboots never automatic |
| GitHub | Machine-bound key, no agent forwarding |

---

## 11. Implementation Roadmap

1. **Server foundation**: Arch installation, user separation, SSH hardening, Tailscale, GitHub key.
2. **Agent stack**: install Claude Code/Codex/Antigravity CLI, symlink mechanism + `agentctl init` + Git hooks.
3. **Idle update system**: lock directory, `agent-run` wrapper, systemd timer, logging/ntfy.
4. **Terminal daemon MVP**: Go service with raw PTY passthrough via `tmux attach-session`, REST+WebSocket, first test with just a simple web client (`xterm.js`) to validate the protocol before the full app is built.
5. **Files & backups**: Samba shares (`backups`/`exchange`), restic timer, file API endpoints in the daemon including hash verification.
6. **CloudGate integration**: install CloudGate via one-liner or `docker run`, store the Cloudflare API token, register the terminal daemon and owner login portal as hosts in **tunnel mode** (not local nginx mode, because of CGNAT), settle on a fixed subdomain for WebAuthn.
7. **Security layer**: email/password + TOTP + passkey pairing flow, Ed25519 device authentication, step-up auth for sensitive actions.
8. **Client app skeleton**: Flutter project for Linux/Windows/Web first (easier to debug than mobile), session list + terminal view + reconnect logic + file browser.
9. **Android + polish**: mobile control bar, native copy-paste UX, share-sheet integration for file uploads, notifications, FIDO2 hardware key support for Linux.

---

## 12. Open Decisions for You

- Tailscale (simpler, recommended) vs. your own WireGuard setup (more control, more maintenance effort) for the private mesh part – CloudGate handles the public part anyway, independent of this choice.
- Whether the server runs as its own bare-metal machine or as a VM on your existing Proxmox host – the latter would let you take snapshots before major updates, which would be a sensible safety net especially for the automatic update system.
- Whether `reboot-pending` stays purely informational (push notification, you reboot manually) or gets a weekly time window for automatic reboots.
- Which domain should permanently serve as the WebAuthn relying party, since a later change would invalidate all passkeys (section 9.5) – it gets set up as a CloudGate host with a CNAME, so decide this finally before the passkey rollout.
- Whether WebAuthn/passkeys get added directly as a module in CloudGate (one login interface for CloudGate and ClaudeCode Remote) or arise as a separate owner login portal alongside it (section 9.6) – a pure architecture question, both are technically clean options.
- Whether the missing lock coupling of CloudGate's 6-hourly self-update (section 9.3) matters enough to you to retrofit it as a feature in your own CloudGate project, or whether the brief reload window is negligible for you.
- Whether the full three-factor chain should really apply on every device pairing, or whether you want to allow a reduced tier for trusted situations (e.g. a new device on your own home network).
- MD5 vs. SHA-256 for the file hashes (section 8.2) – I recommend SHA-256, but respect MD5 as a deliberate choice if the somewhat lower compute load matters more to you than the theoretical collision resistance.
