# Files and Backups

> **Status:** not implemented yet — planned for Phase 5. `daemon/` is
> currently an empty directory; no Samba share, backup timer, or file-API
> endpoint exists yet. This page describes the intended design, not a
> working system.

This page covers three related things: the two file shares the server will
expose, the automated backup system, and the file API the daemon will offer
to clients that can't mount a share directly.

Binding detail lives in
[`docs/ARCHITECTURE.md`](https://github.com/Elias02345/remote-agent/blob/main/docs/ARCHITECTURE.md)
Sections 7 and 8. This page explains the *why*.

---

## Two shares, deliberately separate

```
/srv/backups/     # automatic backups, relevant to the user only for reading
/srv/exchange/    # bidirectional exchange folder agent ↔ user
```

They're split rather than merged into one share because they serve different
audiences and different write patterns: `backups/` is something the owner
occasionally reads from (never writes to directly — restic owns it),
`exchange/` is something both the owner and the coding agents actively write
into and read from during normal use. Keeping them apart means there's never
ambiguity about who's expected to touch what.

### Why SMB3, not NFS

Both shares are planned to go out over **Samba (SMB3)**. The reasoning is
about transport security, not familiarity:

- SMB3 supports native transport encryption (`smb encrypt = required`) and is
  natively supported by Windows, and by Linux/macOS via standard clients —
  no extra client-side setup.
- NFS, without the added effort of Kerberos, is unencrypted and effectively
  authorized only by source IP. For a share that potentially carries backups
  and project files, that's below this project's bar.

### Tailscale-only, never public

Both shares are planned to bind **exclusively** to the Tailscale interface:

```
interfaces = tailscale0
bind interfaces only = yes
```

Never through [CloudGate](Networking-and-CloudGate), never on the public
interface. This is a hard rule, not a default that could reasonably flip: SMB
and file-sharing protocols generally are not designed for exposure to the
open internet, encrypted transport or not — the attack surface of the
protocol itself is the concern, independent of what's encrypting the bytes on
the wire. For access away from the Tailscale mesh, the file API below is the
intended path instead; SMB stays the convenient option for a desktop machine
that's already in the mesh.

## Automatic backups

Backup tool: **restic**. It deduplicates, encrypts snapshots client-side
(worthwhile even for local-only storage, since it means an offsite copy step
can be added later without redoing the encryption story), and gives
incremental, space-efficient snapshots with a real restore history.

What gets backed up:

- The terminal daemon's SQLite database (sessions, devices, auth state)
- `~/.claude/`, `~/.codex/`, `~/.antigravity/` configuration, including the
  global `CLAUDE.md`
- `/srv/exchange/` contents
- Everything under a project root that Git doesn't already track (`.env`
  files, local build artifacts) — via an include list, so `node_modules`
  doesn't end up backed up wholesale by accident

### Backups are not coupled to the idle-lock logic

This is worth calling out explicitly because it's easy to assume otherwise:
[the idle update system](Idle-Update-System)'s locks exist to stop *updates*
from running while an agent or terminal is active, because an update can
restart services or leave the system briefly unstable. A backup does neither
— it only reads data and writes it somewhere else, which is safe even during
an active agent run or with terminals open. So the backup timer runs on its
own schedule, independent of lock state:

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

### An untested backup is a guess, not a backup

A restore test is planned as its own small timer, separate from the backup
timer itself: a monthly `restic check` plus a spot-check `restic restore`
into a scratch directory. The point is to catch a silently-broken backup
chain before it's the only copy of something that matters, rather than
discovering the problem at restore time.

## The exchange folder

Per project, a two-way structure rather than one mixed folder, so the
direction a file traveled is always encoded in its path rather than left
ambiguous:

```
/srv/exchange/<project>/from-agent/
/srv/exchange/<project>/to-agent/
```

- `to-agent/` is where a client app writes when the owner shares a file for
  the agent's context (Section 8.3, below).
- `from-agent/` is where a coding agent writes when it wants to hand
  something back — a generated image, an export, a report. This convention
  is meant to live in `CLAUDE.md` so all three agents (Claude Code, Codex,
  Antigravity) agree on the same path without being told per-session.
- An `inotifywait`-based watcher on `from-agent/` can optionally fire an
  ntfy notification the moment the agent places something there.

## The file API

Two access paths exist because they serve two different audiences: the SMB
share above is convenient for a Windows/Linux desktop already in the
Tailscale mesh, but Android and Web don't have a practical native SMB-mount
experience. The file API is the daemon-integrated path that works
identically on all four platforms — and it's also the path used to share a
file into a *specific* terminal session's context (Section 8.3).

It's an extension of the same daemon covered in [Terminal Daemon](Terminal-Daemon),
not a separate service:

```
GET    /files?path=...              → list directory contents
GET    /files/download?path=...     → download file (streamed, with hash header)
POST   /files/upload                → chunked upload with hash verification
DELETE /files?path=...               → delete file
```

Access is restricted by allowlist to `/srv/exchange/` and explicitly shared
project directories — there is no general filesystem browser across the
server. This is enforced structurally, not just by API logic: the daemon runs
as the `agent` user (see [Server Base](Server-Base)), which cannot read or
write outside those paths regardless of what the API layer does or doesn't
check.

## Integrity: hash before sending, hash after receiving (D-08)

Reproduced logic, per `docs/ARCHITECTURE.md` §8.2:

1. The client computes the file's hash locally, before upload starts.
2. Upload happens in chunks via the open **tus resumable-upload protocol**
   (`tus.io`) — chosen because it already standardizes chunking and resume
   behavior, so an interruption at 95% doesn't mean re-sending the whole
   file.
3. Once the server has received the complete file, it computes the same hash
   over the written bytes and compares it against the value the client sent.
   On a mismatch, the file is **discarded**, not silently kept — the client
   is asked to resend.
4. The server returns its own computed hash in the response, and the client
   compares it a second time against its local value. TLS/the overlay
   network already rules out tampering in transit; this second comparison is
   a cheap extra guard against plain transmission errors in the response
   path itself.

**Why SHA-256 and not MD5** (decision D-08 in
[Roadmap and Decisions](Roadmap-and-Decisions)): MD5 is entirely adequate for
pure corruption detection, and was the originally proposed choice. But MD5 is
cryptographically broken — collisions are practically producible — which
matters the moment anyone with write access to an intermediate point could
deliberately construct one. For a project built around a high security bar
anyway, SHA-256 costs no meaningful overhead on current hardware, so there's
no reason to carry the weaker algorithm and have to remember later exactly
which guarantees it does and doesn't give.

## Sharing a file into the agent's context (Section 8.3)

The workflow that makes "share a file with a running agent" concrete:

1. From an open terminal session in the app, the owner picks "Share file" —
   either from the device's own filesystem (Android: a native share-sheet
   entry; Desktop/Web: file picker or drag-and-drop) or from the server's own
   file browser (e.g. re-feeding a file already sitting in `from-agent/`).
2. The app uploads it via the procedure above into
   `/srv/exchange/<project>/to-agent/<filename>` for the current session.
3. The daemon can then surface a hint like `# New file available:
   to-agent/photo.png` — but by design this is planned as a **suggestion**,
   shown as an overlay with an "Insert into terminal" button, not text
   automatically written into the terminal's input stream. The deliberate
   reason: a running agent may be mid-way through typing something itself,
   and text silently injected into that input stream is exactly the kind of
   surprise this project tries to avoid. The owner decides when the hint
   becomes actual terminal input.

---

See [Networking and CloudGate](Networking-and-CloudGate) for why these shares
never appear on the CloudGate side, and
[Roadmap and Decisions](Roadmap-and-Decisions) for the Phase 5 definition of
done.
