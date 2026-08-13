# Idle Update System

> **Status:** implemented (Phase 3). 21 CI assertions cover the lock semantics,
> including the asymmetry this page is mostly about: a stale agent lock is cleaned
> after 6 h, a stale terminal lock never is, and a surviving terminal lock still
> blocks the update. One deviation from the doc's sketch is documented below.

This is the part of the design where the reliability of a small piece of
lock logic decides whether the whole system is trustworthy. A stuck lock
must never block updates forever. A forgotten terminal must never get
silently killed by one. Get the lock lifetimes wrong and you either stop
patching the server indefinitely, or you pull a running agent's terminal out
from under it mid-task — both are bad enough to warrant being precise about
this page.

Binding detail lives in
[`docs/ARCHITECTURE.md`](https://github.com/Elias02345/remote-agent/blob/main/docs/ARCHITECTURE.md)
Section 4.

## Two lock types, deliberately different lifetimes

Everything in this system hinges on there being **two kinds of lock**, in
`/run/claudecode-locks/`, that behave nothing alike on purpose:

| Lock | File pattern | Created by | Removed by |
|---|---|---|---|
| Agent lock | `agent-<pid>` | `agent-run` wrapper, automatically, on every agent invocation | `trap ... EXIT` on normal or abnormal exit; stale locks older than 6 h are also swept by the update check |
| Terminal lock | `terminal-<session-id>` | the terminal daemon, as soon as a session exists | **only** an explicit `DELETE /sessions/{id}` — i.e. the user manually closing the terminal |

The asymmetry is the whole point, not an inconsistency to fix later:
**agent locks must never survive indefinitely**, because an agent run is
expected to finish, and if it crashed instead of finishing, an update should
still be able to proceed after a bounded wait. **Terminal locks must never
expire on their own**, because an open terminal represents work the human
chose to leave running — a long-idle `htop` or an agent mid-conversation in
a tmux pane is exactly the kind of session this project promises will
survive indefinitely until you close it yourself. Applying the same 6-hour
staleness rule to both would silently update the system out from under a
terminal you just hadn't gotten back to yet. That is treated as a bug, not
a feature, which is why the stale-cleanup step in `idle-update-check.sh`
explicitly matches only the `agent-*` pattern.

## Why an automatic wrapper, not "the agent manages its own lock"

An earlier version of this design had the agent itself, by instruction in
`CLAUDE.md`, take responsibility for creating and removing its own lock.
That works when everything goes right, and fails exactly when it matters
most: if an agent process is killed, hits a context reset, or simply forgets
the instruction, there is no code path left to remove the lock — it stays
forever and blocks every future update. A lock that depends on a language
model correctly remembering a bookkeeping step under all failure conditions
is not a reliable lock.

The fix is to not depend on the agent at all. `agent-run` is a transparent
shell wrapper that every agent invocation goes through — Claude Code, Codex,
and Antigravity CLI are all planned to be aliased through it (see
[Agent Stack](Agent-Stack)) — and it sets and clears the lock itself, with a
`trap` that fires on `EXIT` regardless of how the wrapped process exits:

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

```bash
alias claude='agent-run claude'
alias codex='agent-run codex'
alias antigravity='agent-run antigravity-cli'
```

No agent instance has to know it's participating in lock management — it
happens transparently on every invocation, whether the agent finishes
cleanly, errors out, or gets killed. The remaining gap is a hard crash that
takes the whole wrapper process down before `trap` can run (`SIGKILL`, power
loss) — that's what the 6-hour stale-lock sweep in the update check exists
to catch. `CLAUDE.md` can additionally note a manual-lock convention for
long-running background processes started *outside* the wrapper (e.g.
`touch /run/claudecode-locks/agent-manual-<name>`), but that's a documented
fallback for an edge case, not a replacement for the wrapper being the
primary mechanism.

## The update check

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

The check runs every 30 minutes. Every run: sweep stale agent locks, then
bail out immediately if *any* lock — agent or terminal — is still present.
Only an empty lock directory triggers an actual `pacman -Syu` and CLI tool
update.

### The `reboot-pending` trap

The flag that records "a kernel update happened, a reboot is owed" lives at
`/var/lib/claudecode/reboot-pending` — **outside** `LOCK_DIR`
(`/run/claudecode-locks/`) on purpose. If it lived inside the lock
directory instead, the next run of `idle-update-check.sh` would see a
non-empty directory, treat the flag itself as an active lock, and refuse to
ever update again until someone manually removed it. Keeping the flag in a
separate location means it can be freely read by the reboot logic and by
the terminal daemon's status endpoint without ever being mistaken for a
reason to skip an update.

## Reboot handling

Kernel updates only ever set `reboot-pending`; nothing in this design
reboots the machine automatically. This is decision **D-03**
(see [Roadmap and Decisions](Roadmap-and-Decisions)): purely informational,
pushed via `ntfy`, reboot is a manual action the human takes.

The reason an idle-triggered auto-reboot is treated as unsafe rather than
just inconvenient: the lock system only knows about sessions and agent runs
that are *currently tracked*. An old tmux session, opened weeks ago and never
formally closed through the daemon, could in principle exist without a
corresponding lock if it predates the daemon's bookkeeping or was reattached
outside it. "Zero locks" is not the same guarantee as "zero terminal
sessions" — an automatic reboot that trusted the lock count alone could kill
exactly the kind of long-forgotten-but-still-wanted session this project is
built to protect. A rarer, human-gated reboot avoids betting an already-open
terminal on that distinction.

## Notifications

`ntfy` (self-hosted or `ntfy.sh`) is the push channel into the app/phone for
this system: update summaries, notice of a pending reboot, and error
messages when an update fails.

## Failure modes this design defends against

| Failure mode | What happens |
|---|---|
| Agent process gets `SIGKILL`ed | `trap EXIT` can't run; the agent lock survives until the 6 h stale-cleanup removes it. Update is delayed, never blocked forever. |
| Power loss mid-run | Same as above — locks are files under `/run`, which doesn't survive a reboot anyway, so a fresh boot starts with an empty lock directory. |
| Terminal left open and forgotten for weeks | Its lock stays exactly as long as the session exists. Updates are correctly skipped the whole time — this is the intended behavior, not a bug to route around. |
| Update lands mid-agent-run | Can't happen: the agent lock is present for the whole run, so `idle-update-check.sh` exits before touching `pacman` while the agent is active. |
