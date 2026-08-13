# daemon

The terminal session daemon (`claudecode-remoted`), architecture Section 5.

> **Status: Phase 4 (MVP) complete in code and CI.** The protocol, session
> lifecycle and lock handling are covered by tests. The visual half of the
> Definition of Done — a full-screen TUI rendering correctly — needs a human at
> a browser; see `TODO_FOR_USER.md`.

## What it does

For each client connection it runs `tmux attach-session -t <id>` inside a PTY
and moves the raw bytes between that PTY and a WebSocket, unchanged. It never
parses, interprets or rewrites terminal output. All emulation happens
client-side (`xterm.dart` in the app, `xterm.js` in the test client).

tmux control mode (`-CC`) is deliberately not used: it is an iTerm2-specific
structured protocol for pane layout, not a byte stream, and full-screen TUIs
like Claude Code would not survive it.

## API

```
GET    /sessions              list open terminals
POST   /sessions              new terminal  {name, cwd, shell}
GET    /sessions/{id}         one terminal
PATCH  /sessions/{id}         rename        {name}
DELETE /sessions/{id}         close it (the only way a session ends)
GET    /sessions/{id}/stream  WebSocket
GET    /health                {"status":"ok"}
```

WebSocket messages:

```
client -> {"type":"input","data":"<base64>"} | {"type":"resize","cols":n,"rows":n}
server -> {"type":"output","data":"<base64>"} | {"type":"exit"}
```

Payloads are base64 because PTY output is arbitrary binary, not valid UTF-8.
Putting raw bytes through `encoding/json` would replace invalid sequences with
U+FFFD, and a corrupted escape sequence is a garbled screen.

## Design points that are easy to get wrong

- **`capture-pane -p -e` on connect.** A reconnecting client gets the current
  screen before the live stream starts, so it sees the present state instead of
  an empty terminal until something redraws. The `-e` is what preserves colour
  and attribute codes.
- **`window-size latest`**, set globally *and* per session. The global option
  only affects sessions created after it was set, and the tmux server may have
  been started by something else. Without it, tmux shrinks a session to the
  smallest attached client — a phone attached next to a desktop would squash
  the layout to phone width.
- **Closing an attachment detaches that client only.** The tmux session keeps
  running; a dropped connection changing session state would defeat the entire
  reason tmux is the persistence layer.
- **`status='closed'` only ever comes from an explicit `DELETE`.** Not from a
  disconnect, not from a timeout, not from a daemon restart. The `CHECK`
  constraint and a test enforce it.
- **Terminal locks are released only on that same explicit close.** They carry
  a `terminal-` prefix so the idle updater's stale cleanup, which matches only
  `agent-*`, can never expire them.
- **The daemon refuses to start on a wildcard bind address.** There is no
  authentication until Phase 7, so binding to localhost or one specific
  interface is the whole access control story — which means it must not be
  possible to widen it with a flag. Public reachability is CloudGate's job.

## Running it

```bash
cd daemon
go run ./cmd/claudecode-remoted --db /tmp/ccr.db --lock-dir /tmp/ccr-locks
```

| Flag | Default | Meaning |
|---|---|---|
| `--addr` | `127.0.0.1:8080` | listen address; wildcards are rejected |
| `--db` | `/var/lib/claudecode-remote/daemon.db` | SQLite database |
| `--lock-dir` | `/run/claudecode-locks` | terminal lock directory |

Then open <http://127.0.0.1:8080> for the xterm.js test client. It exists to
validate the protocol before the Flutter app is built and is not a shipping UI;
it loads xterm.js from a CDN and has no authentication.

## Tests

```bash
cd daemon
go test ./... -count=1
```

`modernc.org/sqlite` (pure Go) is used rather than `mattn/go-sqlite3` so the
binary stays cgo-free — Section 5.1 asks for a single binary with no runtime
dependencies, and cgo would drag in a libc dependency and complicate
cross-compilation for no benefit.
