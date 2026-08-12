# Agent Stack

> **Status:** not implemented yet — planned for Phase 2, and depends on
> [Server Base](Server-Base) (Phase 1) existing first. This page describes
> the intended design, not a working system.

The agent stack is the layer that installs Claude Code, Codex, and
Antigravity CLI on the server and makes sure all three agree on the same
project context. Getting the installation right matters less than getting
the *conventions* right — three different CLIs, one shared understanding of
what a project is and how it should be worked on.

Binding detail lives in
[`docs/ARCHITECTURE.md`](https://github.com/Elias02345/remote-agent/blob/main/docs/ARCHITECTURE.md)
Section 3.

## Installation and the shared wrapper

Claude Code, Codex, and Antigravity CLI will each be installed system-wide
for the `agent` user, following each vendor's current installation
instructions (package names and install methods change often enough that
pinning a method here would go stale). What's fixed by convention, and does
not change regardless of how any one tool is installed, is that **none of
the three is ever invoked directly**. All three go through one shared
wrapper, `agent-run`.

The wrapper's job is lock management for the [idle update
system](Idle-Update-System) — every agent invocation needs to tell the
update checker "something is running right now" without the agent itself
having to remember to do that. See [Idle Update System](Idle-Update-System)
for why this is a wrapper and not an instruction to the agent.

## `CLAUDE.md` as the single source of truth

Claude Code, Codex, and Antigravity each expect their own context file —
`CLAUDE.md`, `AGENTS.md`, and `ANTIGRAVITY.md` respectively. Maintaining
three separate files in sync by hand doesn't scale, so the design makes
`CLAUDE.md` the one file you actually edit, and turns the other two into
**symlinks** pointing at it:

```
CLAUDE.md            # canonical source, gets edited
AGENTS.md -> CLAUDE.md
ANTIGRAVITY.md -> CLAUDE.md
```

Symlinks rather than bind mounts, deliberately: a symlink is just a
filesystem entry that every tool already follows transparently when it opens
a file — no mount namespace, no extra privilege, nothing that behaves
differently inside a container or chroot. A bind mount would need to be set
up per-process and would add a failure mode symlinks don't have. For three
plain text files that all need to read identically, the boring option wins.

## `agentctl init` and Git hooks

A small script creates the symlinks in a project root:

```bash
#!/usr/bin/env bash
# agentctl init – run in the project root
set -e
[ -f CLAUDE.md ] || { echo "CLAUDE.md missing"; exit 1; }
ln -sf CLAUDE.md AGENTS.md
ln -sf CLAUDE.md ANTIGRAVITY.md
echo "Agent context files linked."
```

Running this by hand after every clone is exactly the kind of step that gets
forgotten, so it's planned to be wired into `post-checkout` and
`post-merge` Git hooks. That means a fresh clone or a completed merge
re-links the context files automatically, without a manual step — the
symlinks can't silently go stale after a merge changes `CLAUDE.md`.

## Two layers: per-project and machine-wide

There are two levels of context, not one:

- **Per-project `CLAUDE.md`** — what `agentctl init` links into
  `AGENTS.md`/`ANTIGRAVITY.md`, specific to that repository.
- **Machine-wide `~/.claude/CLAUDE.md`** — conventions that apply to every
  project on the server (coding style, security rules). Claude Code already
  reads project and global context hierarchically on its own.

Codex and Antigravity don't have a native concept of a global context file
the way Claude Code does, so the plan is to get them the same global layer
through the identical symlink mechanism: the global `CLAUDE.md` gets mirrored
into their respective home directories rather than maintained as a second
copy.

## The exchange folder convention

All three agents need to agree on one more thing that isn't a context file
at all: the path convention for exchanging files with the human,
`/srv/exchange/<project>/from-agent/` and `to-agent/`. This is documented as
a `CLAUDE.md` convention specifically so that whichever of the three agents
is running, it knows where to look for files shared with it and where to put
files meant for the human. See [Files and Backups](Files-and-Backups) for
how that folder structure works and how it's backed up.
