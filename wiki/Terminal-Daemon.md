# Terminal Daemon

> **Status:** implemented (Phase 4), with one part outstanding. The protocol,
> session lifecycle and lock handling are covered by the Go suite under `-race`.
> What no test can assert is that a full-screen TUI actually *renders* — colours,
> box drawing, alternate-screen switching, reflow on resize. That needs a human at
> a browser; the checklist is in `TODO_FOR_USER.md`.

The terminal daemon is the technical heart of ClaudeCode Remote — the piece
that makes "a terminal session survives an SSH drop and reappears on a
different device" actually true. Everything else in the project (auth, file
sharing, CloudGate) exists to get bytes to and from this daemon safely.

Binding detail lives in
[`docs/ARCHITECTURE.md`](https://github.com/Elias02345/remote-agent/blob/main/docs/ARCHITECTURE.md)
Sections 5 and 6. This page explains the *why* behind the transport decision
and reproduces the API surface and data model that follow from it.

---

## Why Go

The daemon is planned as a single Go binary, for reasons that are all about
keeping a persistent, always-on service boring to run:

- **`creack/pty`** is a mature PTY library — the daemon's entire job hinges on
  correctly opening and managing pseudo-terminals, so this needs to be solid,
  not reinvented.
- **`gorilla/websocket`** is a well-worn WebSocket library, and the live
  interaction path (Section 5.2) is WebSocket end to end.
- Go's concurrency model (goroutines, channels) fits naturally with "many
  parallel session streams, each shuttling bytes independently."
- Go compiles to a **single static binary with no runtime dependencies**,
  which is what makes "install a systemd service" trivial instead of a
  dependency-management exercise — consistent with this project's
  self-hosted, no-PaaS stance.

## The transport decision: raw PTY passthrough, not tmux control mode

The core design choice, and the one most worth understanding before anything
else in this page:

For every client connection, the daemon runs `tmux attach-session -t <id>`
inside its own PTY (via `creack/pty`) and pipes the raw bytes bidirectionally
over the WebSocket. **No server-side interpretation, no parsing.** The daemon
does not know what's on screen — it just moves bytes in both directions.

An earlier draft of the architecture assumed tmux's **control mode** (`-CC`)
instead. That was wrong, and it's worth stating plainly why it was rejected:
`-CC` is a proprietary protocol built specifically for iTerm2's pane-layout
synchronization. It is a structured message protocol, not a raw terminal byte
stream — the client would receive parsed tmux notifications about panes and
windows, not the terminal output itself. A full-screen TUI like Claude Code —
an Ink-based UI with its own alternate-screen buffer, its own redraw cycle,
box-drawing borders — would not survive translation through that protocol.
Raw byte passthrough sidesteps the problem entirely: whatever tmux would show
a real terminal, the daemon forwards unchanged.

This means **terminal emulation lives entirely on the client**, via a real
emulator library (`xterm.dart` in the app, `xterm.js` for a web test client)
— the same class of engine that already renders vim, htop, and tmux
correctly in tools like ttyd, gotty, Upterm, and Termius. This is not a novel
approach; it's the standard one for this exact problem, and it's why the
daemon can stay simple: it never has to understand what it's forwarding.

### What has to line up for correct rendering

Because the daemon doesn't interpret bytes, correctness depends entirely on
the client emulator and the `TERM` environment agreeing with what tmux
actually emits:

- `TERM=tmux-256color` (or `xterm-256color`)
- Alternate-screen-buffer switching (`?1049h` / `?1049l`), so leaving a
  full-screen app like Claude Code or vim restores the shell cleanly
- Truecolor escape codes
- Cursor visibility (`?25l` / `?25h`)
- Correct width computation for Unicode and box-drawing characters — get this
  wrong and Ink-style UIs with box-drawn borders visibly misalign

`xterm.dart`/`xterm.js` are expected to handle all of this natively; the
daemon's only responsibility is not to get in the way.

### Reconnect: current screen first, then live stream

When a client connects (fresh or reconnecting), the daemon is planned to
first send the current screen via `tmux capture-pane -p -e` — the `-e` flag
preserves color and escape codes — and only then start streaming live output.
This means a reconnecting client sees the correct current state immediately,
without needing to replay the entire scrollback history.

### Multi-device resize

tmux's default behavior is to shrink a session to match its *smallest*
attached client. If the same session were open on a phone and a desktop
simultaneously, the desktop's Claude Code layout would get squeezed down to
phone width — actively working against the point of a multi-device tool.

The planned fix is the native tmux option `set -g window-size latest`: the
session tracks whichever device most recently sent active input, while every
other attached device sees a scaled snapshot instead of the live layout being
distorted underneath them.

## API surface

Reproduced from `docs/ARCHITECTURE.md` §5.2 — this is the planned shape, not
something that responds today.

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

## Data model (SQLite)

Reproduced from `docs/ARCHITECTURE.md` §5.3:

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

The important detail is `status='closed'`: it is set **exclusively** by an
explicit user `DELETE`. "Sessions are opened and closed manually, never by
timeout or disconnect" isn't just a UI convention here — it's anchored
directly in the data model. Losing a WebSocket connection has no code path
that touches this column at all.

Device authentication (the `devices` table above, pairing, and the
Ed25519 challenge-response used on every connection) is covered in
[Security Model](Security-Model), since it's a cross-cutting concern shared
with the file API and the owner login portal.

## The planned Flutter client (Section 6)

Client work is Phase 8 (desktop/web skeleton) and Phase 9 (Android). The
intended shape:

- **Session list as tabs** — the home screen behaves like browser tabs: name,
  working directory, last activity, a connected/disconnected status dot.
- **Mobile control bar** above the on-screen keyboard: Esc, Tab, Ctrl (as a
  toggle for combinations), arrow keys, pipe, slash, and a clipboard-aware
  paste button — the pattern used by Termux and Blink Shell.
- **Manual close only**: an explicit button with a confirmation dialog. No
  automatic timeout, no closing on app switch, no closing on connection loss
  — consistent with the data-model guarantee above.
- **Reconnect with exponential backoff**, with a visible "Disconnected —
  reconnecting…" banner instead of a silent failure.

The point underlying all of it: **losing the connection changes nothing
about the session on the server.** The tmux session keeps running
independently, exactly as it would under plain SSH+tmux today — the app just
adds a clean, cross-device interface on top of that already-reliable
behavior.

---

See [Architecture Overview](Architecture-Overview) for how the daemon fits
into the rest of the system, and [Roadmap and Decisions](Roadmap-and-Decisions)
for the Phase 4 and Phase 8 definitions of done.
