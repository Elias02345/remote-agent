# Machine-wide agent conventions

This file lives at `~/.claude/CLAUDE.md` on the ClaudeCode Remote server and is
the **global** convention layer for every coding agent on this machine.
`~/.codex/AGENTS.md` and `~/.antigravity/ANTIGRAVITY.md` are symlinks to it, so
Claude Code, Codex and Antigravity all read the same rules.

A project's own `CLAUDE.md` takes precedence over this file. Use this one only
for rules that hold for every project on the machine.

## Handing files to and from the human

Every project has a two-way exchange directory. The direction is encoded in the
path, so it is never unclear who put a file there:

```
/srv/exchange/<project>/from-agent/    you put files here for the human
/srv/exchange/<project>/to-agent/      the human puts files here for you
```

Write a generated export, report or image to `from-agent/` rather than leaving
it somewhere in the repo. Check `to-agent/` when the human says they have shared
something.

> These directories are created in Phase 5. Until then the paths are a
> convention, not an existing filesystem.

## Long-running processes and the update lock

The server updates itself only while nothing is running. Agent invocations are
wrapped by `agent-run`, which creates and removes a lock file automatically, so
a normal agent session needs no action.

If you start a long-running background process **outside** an agent invocation,
claim a lock yourself and release it when done:

```bash
touch /run/claudecode-locks/agent-manual-<name>
# ... long-running work ...
rm -f /run/claudecode-locks/agent-manual-<name>
```

Without it, an unattended system update can run underneath your process.

> `agent-run` and `/run/claudecode-locks/` arrive in Phase 3. Until then there
> is nothing to lock against and this section describes intent, not a working
> mechanism.

## Per-project context files

Every project root keeps `CLAUDE.md` as the single editable source, with
`AGENTS.md` and `ANTIGRAVITY.md` as symlinks to it. Run `agentctl init` once in
each fresh checkout — Git hooks then keep the links in place across checkouts
and merges. Edit `CLAUDE.md`, never the symlinks.
