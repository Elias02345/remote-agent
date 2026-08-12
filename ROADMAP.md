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
| 2 | Agent stack | open |
| 3 | Idle update system | open |
| 4 | Terminal daemon MVP | open |
| 5 | Files & backups | open |
| 6 | CloudGate connection | open |
| 7 | Security layer | open |
| 8 | Client app skeleton | open |
| 9 | Android & polish | open |

---

## Phase 1 — Server foundation · `done`

**Verified:** the container harness runs `provision.sh` twice inside one Arch
container and passes 30 of 30 assertions in CI, including `sshd -t` against the
hardened config and exactly one active `Port` / `AllowUsers` /
`PasswordAuthentication` line after both runs. `shellcheck -x` and `bash -n` are
clean across the repo. **Not** covered, because no real machine exists yet: an
actual sshd restart under systemd, fail2ban banning a real client, and Tailscale
bringing up an interface.


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

## Phase 2 — Agent stack · `open`

**Scope:** Installation scripts for Claude Code / Codex / Antigravity CLI,
`agentctl init` including Git hooks, distribution of the global
`~/.claude/CLAUDE.md`.

**Definition of Done:** In a test repo, `agentctl init` correctly creates the
three symlinks; Git hooks trigger it automatically on checkout/merge.

## Phase 3 — Idle update system · `open`

**Scope:** `agent-run` wrapper, lock-directory logic (both lock types tested
separately), systemd timer, ntfy integration.

**Definition of Done:** Tests prove that updates are skipped while a lock
exists, that agent locks disappear after 6 h of stale cleanup, and that
terminal locks do not.

## Phase 4 — Terminal daemon MVP · `open`

**Scope:** Go service, REST+WebSocket API from Section 5.2, SQLite schema
from 5.3, tested against a simple `xterm.js` test client (no Flutter needed).

**Definition of Done:** A full-screen-capable TUI (Claude Code itself, also
`htop`/`vim` as a test) runs visibly correctly in the test client, including
alternate-screen switching and resize.

## Phase 5 — Files & backups · `open`

**Scope:** Samba configuration (`backups`/`exchange`, Tailscale-only),
restic timer, file API endpoints including SHA-256 double-checking and
`tus`-based resumable upload.

**Definition of Done:** A deliberately aborted upload can be resumed, a
tampered chunk is rejected, a restore test timer demonstrably runs through.

## Phase 6 — CloudGate connection · `open`

**Scope:** Documentation/script for host setup in CloudGate (tunnel mode),
record the final subdomain decision, owner login portal skeleton per
decision D-05.

**Definition of Done:** The terminal daemon is reachable via the fixed
subdomain over HTTPS; the WebSocket connection demonstrably works through
the tunnel.

**Blocker:** D-04 (domain) must be final before this — see `TODO_FOR_USER.md`.

## Phase 7 — Security layer · `open`

**Scope:** Password (`Argon2id`) + TOTP + passkey pairing flow, `Ed25519`
device auth, step-up auth for sensitive actions, rate limiting.

**Definition of Done:** A new device can only be paired after successfully
completing all three factors; an already-paired device gets in without
re-entering the password; sensitive actions demonstrably trigger a step-up
prompt.

## Phase 8 — Client app skeleton · `open`

**Scope:** Flutter project for Linux/Windows/Web first, session list,
terminal view with `xterm.dart`, reconnect logic, file browser.

**Definition of Done:** All three desktop/web targets build from the same
codebase; a terminal session can be opened, disconnected, and resumed on
another simulated client.

## Phase 9 — Android & polish · `open`

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
| D-04 | WebAuthn relying-party domain | **open — owner must decide.** Confirmed by the owner in Phase 0 that this stays deferred; Phases 1–5 are built domain-agnostic; the question is re-raised before Phase 6. | Phase 6/7 | **only before the passkey rollout.** A later change invalidates all passkeys |
| D-05 | Owner login: WebAuthn module in CloudGate vs. separate portal | **Separate portal** as a `/owner` route in the daemon (Section 10.2). Clean separation, CloudGate stays unchanged. **Confirmed by the owner in Phase 0** — no longer a default assumption. | Phase 6/7 | confirmed — reopen only on explicit request |
| D-06 | Couple CloudGate self-update to the lock | **No.** The short `cloudflared` reload window is accepted; noted as a feature idea in the CloudGate repo, not a blocker here | — | any time |
| D-07 | Full three-factor chain on every pairing | **Yes, without exception.** No reduced tier for "trusted networks" | Phase 7 | End of Phase 7 |
| D-08 | MD5 vs. SHA-256 for file hashes | **SHA-256** — already fixed as non-negotiable in the prompt | Phase 5 | — |
| D-10 | `admin` sudo: password-gated vs. `NOPASSWD` | **Password-gated** via its own `/etc/sudoers.d/admin` drop-in. Not in Section 12 — it surfaced in Phase 1, because `PermitRootLogin no` plus no sudo path for `admin` left the machine administrable only from the physical console. `NOPASSWD` was rejected: SSH is key-only, so a stolen admin key would otherwise be instant root. Cost: the operator must supply `CCR_ADMIN_PASSWORD_HASH` or run `passwd admin` before closing the root session; `provision.sh` warns loudly when neither happened. | Phase 1 | any time, but flipping to `NOPASSWD` weakens the model — reopen only deliberately |
| D-09 | Documentation language | **English throughout** (wiki, READMEs, script output, commit messages), because the repo is public. The German original of the architecture doc is preserved as `docs/ARCHITECTURE.de.md`. | all phases | any time, but costly |

---

## Specification gaps

Places where `docs/ARCHITECTURE.md` is **silent**, found while writing the wiki in
Phase 0. These are not architecture changes and not disagreements with the doc — they
are decisions the doc defers, which the implementing phase has to make explicitly rather
than by accident. Each must be resolved *before* the listed phase is declared done, and
the resolution recorded here as a `D-xx` row.

| ID | Gap | Why it matters | Resolve in |
|---|---|---|---|
| G-01 | A running tmux session can exist **without** a terminal lock — created manually via `tmux new`, or predating the daemon. The lock directory is therefore not a complete picture of session state. | Section 4.4 already treats this as the reason not to auto-reboot on idle, but never says how the gap arises or whether the idle check should additionally consult `tmux ls` instead of trusting locks alone. Getting this wrong either blocks updates forever or kills a live session. | Phase 3 |
| G-02 | Auth mechanism for the `/files/*` endpoints is unspecified. | Section 8.1 defines the allowlist and the `agent`-user sandboxing, but never says whether file requests use the same Ed25519 challenge-response as the WebSocket, a token derived from it, or something else. An unauthenticated file API would bypass the entire device-pairing model. | Phase 5 |
| G-03 | Step-up re-auth protocol is unspecified. | Section 10 lists *which* actions require step-up but not the mechanics: a fresh WebAuthn ceremony per action, or a short-lived step-up token with a TTL? A token with too long a TTL silently degrades step-up into a single prompt per session. | Phase 7 |
| G-04 | "Short-lived session key" for the web client is undefined. | Section 5.4 rules out persistent key storage in the browser but gives no lifetime, no reissue rule, and no scope (per tab? per login?). | Phase 7/8 |
| G-05 | The `post-checkout` / `post-merge` hook bodies for `agentctl init` are described in prose only. | Low risk — the doc states the intent clearly, the implementation is straightforward. Recorded so the hooks are written deliberately rather than improvised. | Phase 2 |
| G-06 | The `inotifywait` watcher on `from-agent/` is explicitly marked "optional" in Section 7.3. | Needs a yes/no decision rather than silently shipping or silently skipping it. | Phase 5 |
