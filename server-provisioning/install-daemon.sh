#!/usr/bin/env bash
# install-daemon.sh — builds and installs the terminal session daemon as a
# systemd service (docs/ARCHITECTURE.md Section 5). Idempotent, safe to re-run.
#
# Nothing before this deployed the daemon: the earlier installers cover the
# server, the agent stack, the idle updater and the shares, but the binary that
# the whole product is about was never installed anywhere. This closes that.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# shellcheck source-path=SCRIPTDIR
# shellcheck source=lib/common.sh
source "$SCRIPT_DIR/lib/common.sh"

require_root

CCR_AGENT_USER="${CCR_AGENT_USER:-agent}"
STATE_DIR="/var/lib/claudecode-remote"
SECRETS_DIR="/etc/claudecode-remote"
DAEMON_SRC="${CCR_DAEMON_SRC:-$REPO_ROOT/daemon}"

id -u "$CCR_AGENT_USER" >/dev/null 2>&1 ||
  fail "User '${CCR_AGENT_USER}' does not exist. Run provision.sh first."

step_build() {
  banner "Building the daemon"

  pkg_install go tmux

  [[ -d "$DAEMON_SRC" ]] || fail "Daemon source not found at ${DAEMON_SRC}"

  # Build as a normal user rather than root: the Go module cache ends up owned
  # by whoever ran the build, and a root-owned cache in the agent's home is a
  # confusing failure the first time the agent tries to build anything itself.
  local gocache="${STATE_DIR}/gocache"
  install -d -o "$CCR_AGENT_USER" -g "$CCR_AGENT_USER" -m 0755 "$gocache"

  # The build output must NOT land on a predictable path in a world-writable
  # directory. `go build -o /tmp/claudecode-remoted` followed by root copying
  # that file is a straightforward local privilege escalation: the agent user
  # (or anyone else able to write /tmp first) replaces the file, or swaps it
  # for a symlink to something root-readable, between the build finishing and
  # root reading it. root then installs whatever it finds into
  # /usr/local/bin, world-executable, and systemd runs it.
  #
  # So: root creates the staging directory, hands write access to the build
  # user for exactly the duration of the build, then takes it back *before*
  # inspecting the result. Once the chown returns, no non-root process can
  # alter the directory, and the checks below cannot be raced.
  local staging
  staging="$(mktemp -d)"
  chmod 0755 "$staging"
  chown "$CCR_AGENT_USER":"$CCR_AGENT_USER" "$staging"

  local built="${staging}/claudecode-remoted"
  local build_rc=0
  ( cd "$DAEMON_SRC" && \
    sudo -u "$CCR_AGENT_USER" env GOCACHE="$gocache" GOPATH="${gocache}/gopath" \
      go build -o "$built" ./cmd/claudecode-remoted ) || build_rc=$?

  # The directory only, never -R: a recursive chown/chmod follows symlinks the
  # build user may have planted and would happily rewrite the mode of whatever
  # they point at. Taking write access to the *directory* is what actually
  # closes the race — without it nobody can create, rename or replace an entry
  # here, so the path we validate is the path we install.
  chown root:root "$staging"
  chmod 0755 "$staging"

  if [[ "$build_rc" -ne 0 ]]; then
    rm -rf "$staging"
    fail "go build failed"
  fi

  # -f dereferences symlinks, so test for the link separately: a symlink to
  # /etc/shadow would satisfy -f and get copied to a world-readable path.
  if [[ -L "$built" || ! -f "$built" ]]; then
    rm -rf "$staging"
    fail "build output at ${built} is not a regular file; refusing to install it."
  fi

  install -m 0755 "$built" /usr/local/bin/claudecode-remoted
  rm -rf "$staging"
  ok "Installed /usr/local/bin/claudecode-remoted"
}

step_directories() {
  banner "Creating daemon directories"

  # Sacred path (CLAUDE.md): the SQLite database and its -wal/-shm siblings live
  # here and no installer or updater may ever remove them.
  install -d -o "$CCR_AGENT_USER" -g "$CCR_AGENT_USER" -m 0750 "$STATE_DIR"
  install -d -o "$CCR_AGENT_USER" -g "$CCR_AGENT_USER" -m 0750 "${STATE_DIR}/uploads"
  install -d -m 0700 "$SECRETS_DIR"

  if [[ ! -e "${SECRETS_DIR}/.env" ]]; then
    # Created empty rather than populated: every value in it is either a secret
    # (owner password hash, TOTP secret) or per-installation (the domain), and
    # inventing defaults for those is exactly how a fail-closed system quietly
    # becomes fail-open.
    install -m 0600 /dev/null "${SECRETS_DIR}/.env"
    ok "Created empty ${SECRETS_DIR}/.env"
  else
    ok "${SECRETS_DIR}/.env already exists, leaving it untouched"
  fi
}

step_unit() {
  banner "Installing the systemd unit"

  install -m 0644 "$SCRIPT_DIR/systemd/claudecode-remoted.service" \
    /etc/systemd/system/claudecode-remoted.service

  if has_systemd; then
    systemctl daemon-reload
    systemctl enable --now claudecode-remoted.service
    ok "claudecode-remoted.service enabled and started"
  else
    warn "systemd not available as init, unit installed but not enabled"
  fi
}

step_sudoers_check() {
  # provision.sh grants the agent user NOPASSWD for exactly
  # `systemctl restart claudecode-daemon`. The unit is actually called
  # claudecode-remoted, so that whitelist would never match — catch the
  # mismatch here rather than the first time a restart silently asks for a
  # password nobody can type.
  if [[ -f /etc/sudoers.d/agent ]] && ! grep -q 'claudecode-remoted' /etc/sudoers.d/agent; then
    warn "/etc/sudoers.d/agent does not mention claudecode-remoted."
    warn "Re-run provision.sh so the agent user can restart this service."
  fi
}

print_summary() {
  banner "Daemon summary"
  log "Binary:      /usr/local/bin/claudecode-remoted"
  log "Unit:        claudecode-remoted.service"
  log "Database:    ${STATE_DIR}/daemon.db  (sacred path — never deleted by an update)"
  log "Config:      ${SECRETS_DIR}/.env"
  log "Listening:   127.0.0.1:8080  (public access goes through CloudGate)"

  if ! grep -q '^CCR_PUBLIC_DOMAIN=' "${SECRETS_DIR}/.env" 2>/dev/null; then
    warn "CCR_PUBLIC_DOMAIN is not set — passkey pairing stays disabled until it is."
  fi
  if ! grep -q '^CCR_OWNER_PASSWORD_HASH=' "${SECRETS_DIR}/.env" 2>/dev/null; then
    warn "Owner credentials are not set. Generate them with:"
    warn "  claudecode-remoted --setup-owner --owner-email you@example.com"
  fi
}

step_build
step_directories
step_unit
step_sudoers_check
print_summary

exit 0
