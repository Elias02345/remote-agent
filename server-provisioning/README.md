# server-provisioning

> **Phase 1 status:** this directory currently ships the provisioning script
> and a container-based test harness only. No real target server exists yet
> (see `TODO_FOR_USER.md`), so the container harness under `test/` is the
> *only* verification that has run so far. It proves the script is
> idempotent and that its safety guards work inside an Arch Linux container
> without systemd as init -- it does not prove the script works on real
> hardware. See "What the test harness cannot cover" below before treating
> Phase 1 as done.

## Purpose

`provision.sh` takes a fresh Arch Linux server from zero to the hardened
state described in `docs/ARCHITECTURE.md` Section 2: SSH hardening
(Ed25519-only, non-standard port, fail2ban), a strict `admin`/`agent` user
split, a password-gated `sudo` grant for `admin` and a narrowly scoped
NOPASSWD `sudo` grant for `agent`, and an SSH identity for the `agent` user
to authenticate to GitHub. It is idempotent -- running it again on an
already-provisioned server must not break anything.

**Why `admin`'s sudo is password-gated, not NOPASSWD:** SSH access is
key-only. If `admin`'s sudo grant needed no password on top of that, a
single stolen SSH private key would be instant, silent root. Requiring the
account password for `sudo` is the second factor that closes that gap --
this is deliberate, not an oversight, see `step_admin_sudo` in
`provision.sh`.

## Before you run this on a real machine: read this first

**This script hardens SSH and changes the port. Get this wrong and you lock
yourself out of the server.**

- Have a working Ed25519 key pair for the `admin` user ready *before*
  running the script, and pass its public half as `CCR_ADMIN_PUBKEY`.
  Password authentication is disabled and root login is disabled as part of
  this run -- if `CCR_ADMIN_PUBKEY` is wrong or missing, you will not be
  able to log in afterward.
- **Keep your current SSH session open.** Do not close it until you have
  confirmed a *new* session connects successfully on the new port as the
  `admin` user. If the new connection fails, your existing open session is
  the only way back in to fix it.
- The SSH port changes to `2222` (or whatever you set `CCR_SSH_PORT` to).
  Update your bookmarks/`~/.ssh/config` and any firewall rules accordingly
  *before* you disconnect.
- **`admin` cannot use `sudo` without a password**, and sudo is deliberately
  password-gated (see above). Either set `CCR_ADMIN_PASSWORD_HASH` before
  running (generate it with `openssl passwd -6`), or run `passwd admin`
  yourself on the server *before closing the root session* that ran
  `provision.sh`. If you skip both, `admin` can log in over SSH but cannot
  administer the machine, and you will need root (or single-user mode) to
  fix it. `provision.sh` prints a loud warning at the end of every run where
  this is still unresolved.

## Configuration

All configuration is via environment variables, optionally via a sibling
`.env` file (copy `.env.example` to `.env` and edit it; `.env` is
git-ignored and never committed).

| Variable | Default | Meaning |
|---|---|---|
| `CCR_ADMIN_PUBKEY` | none -- **required** | Operator SSH public key. Must start with `ssh-ed25519 `; the script refuses to run without it rather than risk a lockout. |
| `CCR_ADMIN_PASSWORD_HASH` | empty -- optional | Crypt hash for `admin`'s account password, needed for `admin` to actually use `sudo` (it is password-gated, see above). Must start with `$` -- a plaintext value is rejected. Generate one with `openssl passwd -6`. If left empty, provisioning still succeeds, but `admin` cannot use `sudo` until you set a password yourself (see the warning below). |
| `CCR_ADMIN_USER` | `admin` | SSH/administration user, kept separate from `agent`. |
| `CCR_AGENT_USER` | `agent` | User that runs coding agents and terminal sessions. Never root. |
| `CCR_SSH_PORT` | `2222` | Port sshd listens on after hardening. |
| `CCR_TAILSCALE_AUTHKEY` | empty | Optional Tailscale auth key for unattended `tailscale up`. |
| `CCR_SKIP_TAILSCALE` | `0` | Set to `1` to skip Tailscale setup entirely. |

## Running it on a real machine

```bash
# As root (or via sudo) on a fresh Arch Linux install:
export CCR_ADMIN_PUBKEY="$(cat ~/.ssh/id_ed25519.pub)"
export CCR_ADMIN_PASSWORD_HASH="$(openssl passwd -6)"   # prompts for a password; optional but recommended
bash server-provisioning/provision.sh
```

Re-running the script (e.g. after a partial failure, or to pick up an
updated `CCR_SSH_PORT`) is safe -- it only appends/rewrites what has
actually changed and never regenerates existing keys, and never resets
`admin`'s password if `CCR_ADMIN_PASSWORD_HASH` is left unset on a later run.

## Running the tests

Requires either `podman` or `docker` on the machine you run the tests from
(not on the target server -- this never touches real hardware):

```bash
cd server-provisioning/test
./run-tests.sh
```

This builds a throwaway Arch Linux container, then runs `provision.sh`
against it:

- Negative cases: missing `CCR_ADMIN_PUBKEY`, an `ssh-rsa` key instead of
  Ed25519, and a plaintext `CCR_ADMIN_PASSWORD_HASH` -- all three must fail.
- A positive case with a real `openssl passwd -6` hash, proving the optional
  password-hash path works end to end.
- A positive run of the script twice inside the *same* container, to prove
  idempotency, followed by assertions on the resulting SSH config, both
  sudoers drop-ins (`agent` and `admin`), `wheel` group membership, user
  accounts, and SSH keys -- including that `admin`'s sudoers drop-in has no
  `NOPASSWD` and that `agent`'s GitHub SSH key fingerprint is unchanged
  across both runs.

Each assertion prints `[PASS]`/`[FAIL]`; the script exits non-zero if
anything failed. This harness also runs in CI (`.github/workflows/ci.yml`)
on every push and pull request, gated behind the shellcheck job.

## The agent stack (Phase 2)

`install-agent-stack.sh` installs the coding agents for the `agent` user and
puts `agentctl` on the PATH. Run it **after** `provision.sh`, since it expects
the `agent` user to exist:

```bash
sudo bash install-agent-stack.sh
```

| Variable | Default | Meaning |
|---|---|---|
| `CCR_AGENT_USER` | `agent` | user the stack is installed for |
| `CCR_AGENT_PACKAGES` | `@anthropic-ai/claude-code @openai/codex` | npm packages to install globally |

A package that fails to install produces a warning and a `FAIL` line in the
summary, never an aborted run — vendor package names change, and one bad name
must not block the other tools.

**The Antigravity CLI is deliberately not in the default list.** Its npm
package name could not be verified, and a guessed name would fail on every run.
Add it to `CCR_AGENT_PACKAGES` once you know the correct install command.

The script also places `templates/global-CLAUDE.md` at
`~agent/.claude/CLAUDE.md` — but **only if that file does not already exist**,
because it is your machine-wide conventions file and a re-run must not discard
your edits. `~/.codex/AGENTS.md` and `~/.antigravity/ANTIGRAVITY.md` become
symlinks to it, which is what makes one file the source of truth for all three
agents.

### agentctl

Per project, `CLAUDE.md` is the single editable file and the other two are
symlinks to it (architecture Section 3.2 — symlinks, not bind mounts, because
every tool reads a text file transparently and a bind mount would break under
containers and chroots):

```
CLAUDE.md            canonical source, the one you edit
AGENTS.md -> CLAUDE.md
ANTIGRAVITY.md -> CLAUDE.md
```

| Command | Effect |
|---|---|
| `agentctl init [dir]` | requires `CLAUDE.md`, creates both symlinks, installs the Git hooks |
| `agentctl install-hooks [dir]` | hooks only; outside a Git work tree it warns and exits 0 |
| `agentctl --help` | usage |

Two rules worth knowing:

- **The links are relative.** An absolute link would break the moment the repo
  is moved or cloned to a different path.
- **A regular file is never replaced.** If `AGENTS.md` exists as a real file,
  `agentctl` refuses, leaves it untouched, and exits non-zero. Silently
  overwriting it would destroy someone's work.

The `post-checkout` and `post-merge` hooks re-run `agentctl init`, so the links
survive branch switches and merges. `.git/hooks` is **per clone and never
committed**, so `agentctl init` has to be run once in every fresh checkout —
that is a Git limitation, not an oversight. Hooks written by `agentctl` carry a
`# managed-by: agentctl` marker; a hook without it is yours and is left alone
with a warning.

Run the agentctl tests with:

```bash
cd server-provisioning/test
./run-agentctl-tests.sh
```

It builds the same Arch container and proves, among other things, that the
links are relative, that a pre-existing regular `AGENTS.md` survives byte for
byte, and — the Phase 2 Definition of Done — that a real `git checkout` and a
real `git merge` actually restore a deleted symlink through the hooks. This
harness runs in CI alongside the provisioning one.

## The idle update system (Phase 3)

`install-idle-updater.sh` installs the pieces from architecture Section 4. Run
it after `provision.sh`:

```bash
sudo bash install-idle-updater.sh
```

It places `agent-run` and `idle-update-check.sh` in `/usr/local/bin`, creates
`/run/claudecode-locks` (plus a `tmpfiles.d` entry, since `/run` is a tmpfs and
vanishes on reboot) and `/var/lib/claudecode`, drops shell aliases in
`/etc/profile.d/claudecode-agents.sh`, and enables `idle-updater.timer`
(5 min after boot, then every 30 min).

### Two lock types, and why they behave differently

| Lock | Created by | Lifetime |
|---|---|---|
| `agent-<pid>` | `agent-run`, automatically | removed on exit; stale ones cleaned after 6 h |
| `terminal-<session-id>` | the terminal daemon (Phase 4) | **unlimited** — only closing the terminal removes it |

The asymmetry is deliberate. An agent lock belongs to a process that may have
been `SIGKILL`ed or lost to a power cut, so it needs a time-based escape hatch
or a crash blocks updates forever. A terminal lock belongs to a session that
only the user closes; expiring it would mean updating underneath a terminal
that is still open. Widening the cleanup's `-name 'agent-*'` filter to `'*'`
would quietly destroy this distinction — there is a test that fails if anyone
does.

The `reboot-pending` flag lives in `/var/lib/claudecode/`, deliberately
**outside** the lock directory: a file inside it would count as an active lock
and block every future update permanently.

**Reboots are never automatic.** A kernel update only raises the flag
(decision D-03); a human reboots.

### agent-run does not use `exec`

Architecture Section 4.2 sketches the wrapper as a `trap ... EXIT` followed by
`exec "$@"`. That does not work: `exec` replaces the shell's process image and
discards every trap with it, so the lock would leak on every run and survive
until the 6 h cleanup — the exact failure the wrapper exists to prevent. The
command therefore runs as a child and the wrapper stays alive to clean up, with
`INT`/`TERM` forwarded so Ctrl-C still reaches the agent. Recorded as decision
D-11.

Run the tests with:

```bash
cd server-provisioning/test
./run-idle-update-tests.sh
```

## What the test harness cannot cover

The container has no systemd as init by design (see `test/Containerfile`),
so the following can only be verified on the real target server, not here:

- **sshd actually restarting** under systemd and continuing to accept
  connections on the new port.
- **fail2ban actually banning** an IP after repeated failed logins --
  the harness only checks that fail2ban installs and its config is valid.
- **Tailscale actually bringing up an interface** and joining the tailnet.

Treat a green `run-tests.sh` as proof the script's logic and idempotency are
sound, not as proof the server will end up reachable and correctly
hardened. That final confirmation only happens once `TODO_FOR_USER.md`'s
"Provide the target server" item is done.
