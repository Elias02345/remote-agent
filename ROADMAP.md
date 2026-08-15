# ROADMAP — ClaudeCode Remote

Status values: `open` · `in progress` · `done`

Binding basis: [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md).
After each phase: summary, new entries in `TODO_FOR_USER.md`, a check-in on
the open decisions made during the phase — only then the next phase.

## Phase overview

| # | Phase | Status |
|---|---|---|
| 0 | Repo setup, architecture doc, wiki | done |
| 1 | Server foundation | done |
| 2 | Agent stack | done |
| 3 | Idle update system | done |
| 4 | Terminal daemon MVP | done (visual DoD outstanding) |
| 5 | Files & backups | done |
| 6 | CloudGate connection | blocked on real deployment (D-04 resolved: `CCR_PUBLIC_DOMAIN` is now first-run config) |
| 7 | Security layer | done (passkey inactive until the operator sets `CCR_PUBLIC_DOMAIN`) |
| 8 | Client app skeleton | done |
| 9 | Android & polish | done (Android glue outstanding) |

---

## Phase 1 — Server foundation · `done`

**Verified:** the plain (non-systemd) container harness runs `provision.sh`
twice inside one Arch container and passes 30 of 30 assertions in CI,
including `sshd -t` against the hardened config and exactly one active
`Port` / `AllowUsers` / `PasswordAuthentication` line after both runs.
`shellcheck -x` and `bash -n` are clean across the repo. The systemd
container harness added later (`server-provisioning/test/run-e2e-tests.sh`)
closes one more real gap: it boots systemd as PID 1 and asserts
`sshd` is **active**, not just correctly configured — the first time this
repo has actually started sshd under an init system rather than only
validating its config file. **Still not** covered, because a privileged
container is not a machine: fail2ban actually banning a real remote client
(its sshd jail depends on `iptables`, which nothing in
`server-provisioning/` installs — see `run-e2e-tests.sh`'s header for the
open question this leaves), and Tailscale bringing up a real interface
(`CCR_SKIP_TAILSCALE=1` in every harness, container or not, since Tailscale
cannot come up in a container at all).


**Scope:** `server-provisioning/` — SSH hardening (keys only, port 2222,
fail2ban), user separation `admin`/`agent`, Tailscale setup script, GitHub
key setup documented. No target server exists yet (confirmed by the owner),
so Phase 1 additionally ships a container-based test harness (Arch
container) so the provisioning scripts are provably runnable and idempotent
rather than merely written.

**Definition of Done:** A fresh Arch server can be brought into the state
described in `docs/ARCHITECTURE.md` Section 2 with a single script, including
idempotency (running it multiple times breaks nothing). The container-based
test harness runs the provisioning scripts against a fresh Arch container
twice and passes both times, proving idempotency without real hardware.

## Phase 2 — Agent stack · `done`

**Verified:** 22 of 22 assertions green in CI. The Definition of Done is proven
with a real repository — a real `git checkout` and a real `git merge` restore a
deleted symlink through the hooks, rather than a hand-invoked hook.

**Scope:** Installation scripts for Claude Code / Codex / Antigravity CLI,
`agentctl init` including Git hooks, distribution of the global
`~/.claude/CLAUDE.md`.

**Definition of Done:** In a test repo, `agentctl init` correctly creates the
three symlinks; Git hooks trigger it automatically on checkout/merge.

## Phase 3 — Idle update system · `done`

**Verified:** 21 of 21 assertions green in CI (plain container harness, no
systemd), including the asymmetry the design rests on — a stale agent lock is
cleaned after 6 h, a stale terminal lock is not, and a surviving terminal lock
still blocks the update. The systemd container harness
(`run-e2e-tests.sh`) closes the gap this phase originally shipped with
unverified: `idle-updater.timer` is proven **enabled** by a real systemd, not
merely installed, and `systemctl start idle-updater.service` proves the
oneshot unit **runs to completion** — its journal is read back afterward and
must contain both the idle decision line and confirmation that
`CCR_SKIP_PACKAGE_UPDATES` actually reached the unit's environment via
`EnvironmentFile=`, which is the mechanism, not the timer, that this whole
phase depends on working. **Still not** covered, because a privileged
container is not a machine: the timer's own `OnBootSec=5min` /
`OnUnitActiveSec=30min` schedule actually elapsing on its own (the harness
calls `systemctl start` directly instead of waiting for it), a real
`pacman -Syu` (skipped everywhere via `CCR_SKIP_PACKAGE_UPDATES=1`), and a
real reboot.

**Scope:** `agent-run` wrapper, lock-directory logic (both lock types tested
separately), systemd timer, ntfy integration.

**Definition of Done:** Tests prove that updates are skipped while a lock
exists, that agent locks disappear after 6 h of stale cleanup, and that
terminal locks do not.

## Phase 4 — Terminal daemon MVP · `done (visual DoD outstanding)`

**Verified:** `go vet`, `go test` and `go build` green in CI and locally across
all four packages. The daemon refuses to start on a wildcard bind address, a
session becomes `closed` only through an explicit DELETE, and a terminal lock is
released only on that same path.

**Not verified, and it cannot be by a test:** that a full-screen TUI actually
*renders* correctly — alternate-screen switching, colours, box drawing, reflow
on resize. That needs a human at a browser with tmux installed; the checklist is
in `TODO_FOR_USER.md`. The phase is marked done for everything a machine can
check, not for the part only an eye can.

**Scope:** Go service, REST+WebSocket API from Section 5.2, SQLite schema
from 5.3, tested against a simple `xterm.js` test client (no Flutter needed).

**Definition of Done:** A full-screen-capable TUI (Claude Code itself, also
`htop`/`vim` as a test) runs visibly correctly in the test client, including
alternate-screen switching and resize.

## Phase 5 — Files & backups · `done`

**Verified:** 20 of 20 provisioning assertions (plain container harness) plus
the Go test suite. That harness runs `testparm` against the generated
`smb.conf` and performs a real restic backup and restore, comparing the
restored bytes against the source; the upload path is covered by resume,
offset-conflict and tampered-content tests. The systemd container harness
(`run-e2e-tests.sh`) adds three things no earlier harness could: it proves
`claudecode-backup.timer` is **enabled** by a real systemd, that
`systemctl start claudecode-backup.service` **runs to completion**, and that
a real restic snapshot exists afterward — not merely that the script
exited 0. It also builds and runs the daemon itself under that same
systemd and drives one full session lifecycle over its REST API end to end:
create → a real `tmux` session appears → a `terminal-*` lock file appears →
delete → both disappear again. No earlier harness in this repo ever ran the
daemon against a real `tmux`. **Still not** covered, because a privileged
container is not a machine: Samba actually binding to a live `tailscale0`
interface and being reachable by a real SMB client — `tailscale0` never
exists in any harness (`CCR_SKIP_TAILSCALE=1`), so only `lo` has ever been
proven — and `claudecode-restore-test.timer`'s monthly schedule actually
elapsing on real hardware.

**Scope:** Samba configuration (`backups`/`exchange`, Tailscale-only),
restic timer, file API endpoints including SHA-256 double-checking and
`tus`-based resumable upload.

**Definition of Done:** A deliberately aborted upload can be resumed, a
tampered chunk is rejected, a restore test timer demonstrably runs through.

## Phase 6 — CloudGate connection · `blocked on real deployment`

**Built:** `server-provisioning/cloudgate/SETUP.md` (host entries, tunnel mode,
why the daemon needs no certificate), `register-host.sh` (registers this
service as a CloudGate host against the owner's already-running CloudGate
instance, tunnel mode, idempotent) and `verify-tunnel.sh`, which is the
Definition of Done check — it verifies a WebSocket upgrade survives the tunnel,
which plain HTTPS success says nothing about. Also the fail-closed pairing state
machine in `internal/identity`, so a half-finished Phase 7 cannot pair a device.
`CCR_PUBLIC_DOMAIN` (`.env.example`) and `--public-domain` wire the domain into
`identity.NewOwner`'s `WebAuthnRPID`, validated at startup as a bare hostname
(never an IP, scheme, port or path) — see D-04 below.

**Blocked:** the DoD requires a real domain, a running CloudGate and the real
server. None exist yet in this dev environment — that is infrastructure only
the owner can stand up, not a code gap. D-04 itself is no longer the blocker:
it resolved to "every installation configures its own domain", which is now
implemented.

**Scope:** Documentation/script for host setup in CloudGate (tunnel mode),
record the final subdomain decision, owner login portal skeleton per
decision D-05.

**Definition of Done:** The terminal daemon is reachable via the fixed
subdomain over HTTPS; the WebSocket connection demonstrably works through
the tunnel.

**Blocker:** a real domain configured via `CCR_PUBLIC_DOMAIN`, a running
CloudGate instance and the real server — see `TODO_FOR_USER.md`.

## Phase 7 — Security layer · `done (passkey inactive until CCR_PUBLIC_DOMAIN is set)`

**Verified:** every package passes with `-race`. An unauthenticated request to
`/sessions` or `/files` gets 401 and provably never reaches the handler; a
device-signed one passes; revoking a device requires a fresh step-up grant;
`--insecure-no-auth` refuses to start on anything but a loopback address.

**Blocked, and correctly so, until the operator configures a domain:** the
passkey factor cannot be satisfied while `CCR_PUBLIC_DOMAIN` is unset, so a
pairing cannot complete. That is the fail-closed behaviour the chain was
built for, not a gap to work around — and it is now a one-time config step
per installation, not an open architecture question.

**Scope:** Password (`Argon2id`) + TOTP + passkey pairing flow, `Ed25519`
device auth, step-up auth for sensitive actions, rate limiting.

**Definition of Done:** A new device can only be paired after successfully
completing all three factors; an already-paired device gets in without
re-entering the password; sensitive actions demonstrably trigger a step-up
prompt.

## Phase 8 — Client app skeleton · `done`

**Verified:** `flutter analyze` clean and 45 tests green, locally and in CI,
and every platform target actually built in CI — web and Linux on the Ubuntu
runner, Windows on its own. That last part is new: this section previously
claimed all three targets built while `flutter build web` answered *"This
project is not configured for the web"*, because no platform directory existed
at all. Analyze and test never touch a platform runner, so nothing in CI
contradicted the claim. Now something does.

Of the tests, the one that matters most splits a box-drawing character across
two WebSocket frames and asserts it survives — decoding each chunk on its own
turns both halves into U+FFFD and draws mojibake exactly where TUI frame lines
are.

**Scope:** Flutter project for Linux/Windows/Web first, session list,
terminal view with `xterm.dart`, reconnect logic, file browser.

**Definition of Done:** All three desktop/web targets build from the same
codebase; a terminal session can be opened, disconnected, and resumed on
another simulated client.

**Not met:** the file browser screen. The file *API client* exists and is
tested; there is no screen in `lib/screens/` that uses it.

## Phase 9 — Android & polish · `in progress`

**Built:** the mobile control bar with a latching Ctrl (which is what makes
Ctrl+C reachable on a touch keyboard at all), the tus upload client with the
client-side half of the SHA-256 double check, the share-to-session flow, the
`app/android/` platform project, and the Kotlin share-sheet bridge — verified
by building an APK and reading its merged manifest, not by inspection.

**Not built:** device pairing in the app. This is the gap that matters most in
the whole project right now, so it is stated plainly rather than buried: the
daemon's pairing chain is complete and fail-closed, and the client has no way
to walk it. There is no Ed25519 key generation, no platform-backed key storage,
no challenge signing, no pairing screen, and no server-address setting — the
app defaults to its own loopback address. A device therefore cannot
authenticate to a daemon that has authentication switched on.

The WebAuthn side is unreachable from either end: `webauthn.go` can verify an
assertion, but nothing calls `BeginRegistration` or `BindSession`, no
credential is persisted, and there are no registration endpoints. The passkey
factor is fail-closed, so this makes pairing impossible rather than weak — but
"impossible" is not "done".

Revocation has the same shape: it sits behind a single-use step-up grant and
no production code path issues one.

**Deliberately not built, with reasons:** FIDO2 hardware keys, because a
passkey binds to a domain and no installation has one configured yet (D-04).
Client-side push, because ntfy already carries it server-side.

**Scope:** Mobile control bar, native copy-paste UX, share-sheet integration
for file uploads, FIDO2 hardware key support as a Linux fallback,
notifications.

**Definition of Done:** A file from any Android app can be uploaded directly
into `to-agent/` of a running session via the share sheet.

---

## Decisions made on open questions

All entries follow the default assumption named in `docs/ARCHITECTURE.md`
Section 12. **All are revisable** as long as the affected phase isn't
finished yet — exceptions: D-04 and D-05, see there.

| ID | Question | Chosen | Affects | Revisable until |
|---|---|---|---|---|
| D-01 | Mesh: Tailscale vs. own WireGuard | **Tailscale** (doc recommendation, NAT traversal + ACLs without building it ourselves) | Phase 1 | End of Phase 1 |
| D-02 | Bare metal vs. Proxmox VM | **Provisioning stays agnostic** — the scripts run on both, snapshots are a pure operational benefit | Phase 1 | any time |
| D-03 | `reboot-pending`: informational vs. automatic time window | **Purely informational** (ntfy push, manual reboot). No auto-reboot timer in Phase 3 | Phase 3 | any time, additively upgradable later |
| D-04 | WebAuthn relying-party domain | **configured per installation, not decided once for the project.** This is a public project and every user has their own domain — the repo ships no default and never hardcodes one. Set via `CCR_PUBLIC_DOMAIN` (`server-provisioning/.env.example`), which the daemon takes as `--public-domain`, validates as a bare hostname at startup, and wires into `identity.NewOwner`'s `WebAuthnRPID`. Leaving it unset is fail-closed: the daemon still starts, the passkey factor just stays permanently unsatisfiable. Phases 1–5 were built domain-agnostic in anticipation of exactly this. | Phase 6/7 | **not project-wide revisable — it is per-installation config.** Within one installation, changeable only before that installation's first passkey rollout; a later change invalidates every passkey already registered on that install |
| D-05 | Owner login: WebAuthn module in CloudGate vs. separate portal | **Separate portal** as a `/owner` route in the daemon (Section 10.2). Clean separation, CloudGate stays unchanged. **Confirmed by the owner in Phase 0** — no longer a default assumption. | Phase 6/7 | confirmed — reopen only on explicit request |
| D-06 | Couple CloudGate self-update to the lock | **No.** The short `cloudflared` reload window is accepted; noted as a feature idea in the CloudGate repo, not a blocker here | — | any time |
| D-07 | Full three-factor chain on every pairing | **Yes, without exception.** No reduced tier for "trusted networks" | Phase 7 | End of Phase 7 |
| D-08 | MD5 vs. SHA-256 for file hashes | **SHA-256** — already fixed as non-negotiable in the prompt | Phase 5 | — |
| D-11 | `agent-run`: `exec` (as sketched in Section 4.2) vs. child process | **Child process.** The sketch in Section 4.2 sets an EXIT trap and then calls `exec "$@"`, but `exec` replaces the shell's process image and discards every trap with it — the trap never fires and the lock leaks on **every** run, lingering until the 6 h stale cleanup. That is the exact failure the wrapper exists to prevent, so the letter of the sketch was dropped to keep its stated intent ("reliably removes it again even on errors"). Cost: one extra shell process per agent invocation; INT/TERM are forwarded to the child so Ctrl-C still behaves. | Phase 3 | reopen only if someone demonstrates the trap firing after `exec` |
| D-10 | `admin` sudo: password-gated vs. `NOPASSWD` | **Password-gated** via its own `/etc/sudoers.d/admin` drop-in. Not in Section 12 — it surfaced in Phase 1, because `PermitRootLogin no` plus no sudo path for `admin` left the machine administrable only from the physical console. `NOPASSWD` was rejected: SSH is key-only, so a stolen admin key would otherwise be instant root. Cost: the operator must supply `CCR_ADMIN_PASSWORD_HASH` or run `passwd admin` before closing the root session; `provision.sh` warns loudly when neither happened. | Phase 1 | any time, but flipping to `NOPASSWD` weakens the model — reopen only deliberately |
| D-09 | Documentation language | **English throughout** (wiki, READMEs, script output, commit messages), because the repo is public. The German original of the architecture doc is preserved as `docs/ARCHITECTURE.de.md`. | all phases | any time, but costly |

---

## Specification gaps

Places where `docs/ARCHITECTURE.md` is **silent**, found while writing the wiki in
Phase 0. These are not architecture changes and not disagreements with the doc — they
are decisions the doc defers, which the implementing phase has to make explicitly rather
than by accident. Each must be resolved *before* the listed phase is declared done, and
the resolution recorded here as a `D-xx` row.

**Resolved so far:** G-01 and G-05. Their rows are struck through below rather
than deleted — how a gap was closed is worth as much as that it was.

| ID | Gap | Why it matters | Resolve in |
|---|---|---|---|
| ~~G-01~~ **resolved (Phase 3)** | A running tmux session can exist **without** a terminal lock — created manually via `tmux new`, or predating the daemon. The lock directory is therefore not a complete picture of session state. | Section 4.4 already treats this as the reason not to auto-reboot on idle, but never says how the gap arises or whether the idle check should additionally consult `tmux ls` instead of trusting locks alone. Getting this wrong either blocks updates forever or kills a live session. | **Resolved:** the update gate stays lock-based, as the doc specifies. Blocking on `tmux ls` was rejected — a forgotten manual session would then block updates forever, reintroducing the very failure the stale cleanup exists to prevent, and a package update does not kill tmux sessions anyway. Instead `idle-update-check.sh` logs a line when unlocked tmux sessions exist, so the discrepancy is visible. The real tmux query belongs in the reboot path, which D-03 defers. |
| ~~G-02~~ **resolved (Phase 5)** | Auth mechanism for the `/files/*` endpoints is unspecified. | Section 8.1 defines the allowlist and the `agent`-user sandboxing, but never says whether file requests use the same Ed25519 challenge-response as the WebSocket, a token derived from it, or something else. An unauthenticated file API would bypass the entire device-pairing model. | **Resolved:** the file API gets exactly the same Ed25519 device challenge-response as the terminal WebSocket, applied as HTTP middleware across both in Phase 7 — not a second, parallel scheme. Until then the whole daemon is unauthenticated and reachable only from localhost, which is why the wildcard-bind guard exists. Recorded in `daemon/README.md` so it cannot be forgotten when Phase 7 lands. |
| G-03 | Step-up re-auth protocol is unspecified. | Section 10 lists *which* actions require step-up but not the mechanics: a fresh WebAuthn ceremony per action, or a short-lived step-up token with a TTL? A token with too long a TTL silently degrades step-up into a single prompt per session. | Phase 7 |
| G-04 | "Short-lived session key" for the web client is undefined. | Section 5.4 rules out persistent key storage in the browser but gives no lifetime, no reissue rule, and no scope (per tab? per login?). | Phase 7/8 |
| ~~G-05~~ **resolved (Phase 2)** | The `post-checkout` / `post-merge` hook bodies for `agentctl init` are described in prose only. | **Resolved:** hooks are written by `agentctl install-hooks`, carry a `# managed-by: agentctl` marker so a foreign hook is never clobbered, guard with `command -v agentctl \|\| exit 0` so a clone on a machine without it does not error on every checkout, and `post-checkout` acts only when its third argument is `1` (a real branch switch, not a file checkout). | Phase 2 |
| ~~G-06~~ **resolved (Phase 5)** | The `inotifywait` watcher on `from-agent/` is explicitly marked "optional" in Section 7.3. | Needs a yes/no decision rather than silently shipping or silently skipping it. | **Resolved: no.** Not built. It would be a third long-running process to install, supervise and debug, for a notification the file API can raise directly once the app exists. Skipping it is recorded here rather than left as an unexplained absence; add it later if the exchange folder actually turns out to be used from outside the app. |
