# Roadmap and Decisions

> **Status:** Phases 0-5, 7 and 8 are done; Phase 6 is blocked on decision D-04
> and Phase 9 is in progress. Decisions D-01 to D-11 below are the record of what
> was chosen and why, including the ones that only surfaced during implementation.

This page is the reader-facing view of
[`ROADMAP.md`](https://github.com/Elias02345/remote-agent/blob/main/ROADMAP.md)
in the repo root. If the two ever disagree, `ROADMAP.md` is the source of
truth — this page only explains it.

## Phases

| # | Phase | Status | Definition of Done (short) |
|---|---|---|---|
| 0 | Repo setup, architecture doc, wiki | done | Architecture doc, roadmap, and wiki exist. |
| 1 | Server foundation | open | A fresh Arch server reaches the state in ARCHITECTURE.md §2 via one idempotent script; proven twice against a container test harness since no real hardware exists yet. |
| 2 | Agent stack | open | `agentctl init` creates the three `CLAUDE.md`/`AGENTS.md`/`ANTIGRAVITY.md` symlinks in a test repo; Git hooks trigger it on checkout/merge. |
| 3 | Idle update system | open | Updates are provably skipped while a lock exists; agent locks expire after 6h of staleness; terminal locks never do. |
| 4 | Terminal daemon MVP | open | A full-screen TUI (Claude Code, `htop`, `vim`) renders correctly — including alternate-screen switching and resize — in a plain `xterm.js` test client. |
| 5 | Files & backups | open | An aborted upload resumes; a tampered chunk is rejected; a restore-test timer runs successfully. |
| 6 | CloudGate connection | open | The daemon is reachable via the fixed subdomain over HTTPS; WebSocket works through the tunnel. |
| 7 | Security layer | open | Pairing requires all three factors; an already-paired device skips the password; sensitive actions trigger step-up. |
| 8 | Client app skeleton | open | Linux/Windows/Web build from one Flutter codebase; a session opens, disconnects, and resumes on a different simulated client. |
| 9 | Android & polish | open | A file from any Android app reaches `to-agent/` of a running session via the share sheet. |

Each phase closes with a summary, new entries in
[`TODO_FOR_USER.md`](https://github.com/Elias02345/remote-agent/blob/main/TODO_FOR_USER.md),
and a check-in on any open decision raised during that phase — only then
does the next phase start.

## Decisions (D-01 … D-09)

All decisions default to the recommendation in
[`docs/ARCHITECTURE.md`](https://github.com/Elias02345/remote-agent/blob/main/docs/ARCHITECTURE.md)
§12 unless noted. All are revisable while their affected phase is still
open — except D-04 and D-05, which are called out below.

| ID | Question | Chosen | Rationale | Affects | Revisable until |
|---|---|---|---|---|---|
| D-01 | Private mesh: Tailscale vs. own WireGuard | Tailscale | NAT traversal and per-device ACLs without building them ourselves | Phase 1 | End of Phase 1 |
| D-02 | Bare metal vs. Proxmox VM | Provisioning stays agnostic | Scripts run on either; VM snapshots are a bonus safety net, not a requirement | Phase 1 | any time |
| D-03 | `reboot-pending`: informational vs. automatic window | Purely informational | ntfy push, manual reboot; no auto-reboot timer built in Phase 3 | Phase 3 | any time, additively upgradable |
| D-04 | WebAuthn relying-party domain | **Open — owner must decide** | Deferred deliberately in Phase 0; Phases 1–5 are built domain-agnostic | Phase 6/7 | **only before the passkey rollout** |
| D-05 | Owner login: WebAuthn module in CloudGate vs. separate portal | Separate `/owner` portal in the daemon | Clean separation of responsibilities; CloudGate stays unchanged | Phase 6/7 | confirmed — reopen only on explicit request |
| D-06 | Couple CloudGate's self-update to the lock system | No | The brief `cloudflared` reload window is accepted as a minor cost | — | any time |
| D-07 | Full three-factor chain on every pairing | Yes, without exception | No reduced tier even for "trusted network" pairing | Phase 7 | End of Phase 7 |
| D-08 | MD5 vs. SHA-256 for file hashes | SHA-256 | Fixed as non-negotiable; MD5's collision weakness is unacceptable for a security-focused project | Phase 5 | — |
| D-09 | Documentation language | English throughout | Repo is public; German architecture original kept as `docs/ARCHITECTURE.de.md` | all phases | any time, but costly |

### D-04 blocks Phase 6 — read this before touching passkeys

D-04, the domain used as the WebAuthn relying-party ID, is still
**undecided**. Phase 6 (CloudGate connection) cannot finalize the owner
login portal setup until this is fixed, because WebAuthn binds every
registered passkey to that exact domain. Changing the relying-party domain
**after** passkeys have been rolled out invalidates every passkey already
registered — every paired device would need to re-pair from scratch. Decide
this once, before Phase 6/7, not after.

## Human-only items

Items only the repo owner can act on (domain decision, hardware
availability, account setup, and similar) are tracked separately in
[`TODO_FOR_USER.md`](https://github.com/Elias02345/remote-agent/blob/main/TODO_FOR_USER.md).
