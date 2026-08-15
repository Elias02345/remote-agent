#!/usr/bin/env bash
# provision.sh — brings a fresh Arch Linux machine to the state described in
# docs/ARCHITECTURE.md Section 2: hardened SSH, admin/agent user separation,
# fail2ban, Tailscale, and a GitHub-ready SSH key for the agent user.
#
# Safe to re-run: every step checks existing state before mutating it.

set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# source-path=SCRIPTDIR makes shellcheck resolve the source below relative to this
# file rather than the caller's working directory, so linting works from anywhere.
# shellcheck source-path=SCRIPTDIR
# shellcheck source=lib/common.sh
source "$SCRIPT_DIR/lib/common.sh"

# require_root must run before any side effect — nothing above this line
# writes anything or installs anything.
require_root

# --- configuration -----------------------------------------------------------

# Load SCRIPT_DIR/.env if present, but never let it override a variable the
# caller already exported — the file is a convenience for operators who'd
# rather edit a file than export vars, not a way to silently shadow an
# explicit `CCR_FOO=bar ./provision.sh`.
if [[ -f "$SCRIPT_DIR/.env" ]]; then
  while IFS='=' read -r ccr_key ccr_value; do
    [[ "$ccr_key" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || continue
    # Strip one layer of surrounding quotes. Shell-style quoting is the habit
    # everyone brings to a .env file, but this parser is not a shell: without
    # this, CCR_ADMIN_PUBKEY="ssh-ed25519 AAAA..." would arrive with a literal
    # leading quote and fail the Ed25519 check with a baffling message.
    if [[ "$ccr_value" == '"'*'"' || "$ccr_value" == "'"*"'" ]]; then
      ccr_value="${ccr_value:1:${#ccr_value}-2}"
    fi
    if [[ -z "${!ccr_key+x}" ]]; then
      export "${ccr_key}=${ccr_value}"
    fi
  done < <(grep -vE '^[[:space:]]*(#|$)' "$SCRIPT_DIR/.env")
fi

CCR_ADMIN_USER="${CCR_ADMIN_USER:-admin}"
CCR_AGENT_USER="${CCR_AGENT_USER:-agent}"

# Validated before anything interpolates them into sudoers or sshd_config.
require_valid_name CCR_ADMIN_USER "$CCR_ADMIN_USER"
require_valid_name CCR_AGENT_USER "$CCR_AGENT_USER"
CCR_SSH_PORT="${CCR_SSH_PORT:-2222}"
CCR_TAILSCALE_AUTHKEY="${CCR_TAILSCALE_AUTHKEY:-}"
CCR_SKIP_TAILSCALE="${CCR_SKIP_TAILSCALE:-0}"
CCR_ADMIN_PUBKEY="${CCR_ADMIN_PUBKEY:-}"
CCR_ADMIN_PASSWORD_HASH="${CCR_ADMIN_PASSWORD_HASH:-}"

# Refusing to proceed without an authorized key is the whole point of this
# guard: PasswordAuthentication gets disabled later in step_ssh, so a run
# without a valid pubkey on file would permanently lock the operator out of
# the machine over SSH.
if [[ -z "$CCR_ADMIN_PUBKEY" ]]; then
  fail "CCR_ADMIN_PUBKEY is not set. Refusing to harden SSH without an authorized key on file — that would lock the operator out permanently. Set it and re-run."
fi
if [[ "$CCR_ADMIN_PUBKEY" != "ssh-ed25519 "* ]]; then
  fail "CCR_ADMIN_PUBKEY must be an Ed25519 key (start with 'ssh-ed25519 ') per docs/ARCHITECTURE.md Section 2.2. Got a different key type."
fi

# Must be a crypt hash (openssl passwd -6 / mkpasswd), never a plaintext
# password — usermod -p writes this field verbatim into /etc/shadow, so a
# plaintext value here would end up stored, and used, as the literal hash.
if [[ -n "$CCR_ADMIN_PASSWORD_HASH" && "$CCR_ADMIN_PASSWORD_HASH" != '$'* ]]; then
  fail "CCR_ADMIN_PASSWORD_HASH must be a crypt hash starting with '\$' (e.g. from 'openssl passwd -6'), not a plaintext password."
fi

SSHD_CONFIG="/etc/ssh/sshd_config"

# --- steps ---------------------------------------------------------------

step_packages() {
  banner "Installing base packages"
  pkg_install openssh sudo fail2ban
}

step_users() {
  banner "Creating admin and agent users"

  local user
  for user in "$CCR_ADMIN_USER" "$CCR_AGENT_USER"; do
    if id -u "$user" >/dev/null 2>&1; then
      ok "User already exists: $user"
    else
      useradd -m -s /bin/bash "$user"
      ok "Created user: $user"
    fi
  done

  # Agent is always key-only, no exceptions: lock its password slot on
  # every run (usermod -L, equivalent to passwd -l; it only blocks password
  # auth, PAM can still accept an SSH pubkey). Re-asserting this on every
  # re-run is safe because agent never gets a real password hash anywhere.
  #
  # admin is deliberately NOT locked/unlocked here. useradd already leaves a
  # freshly created account with no usable password by default, so nothing
  # to do on first run — and on a re-run, forcing a lock here would stomp a
  # password step_admin_sudo (or a manual `passwd admin`) may have set since
  # the last run, breaking sudo. step_admin_sudo is the only place admin's
  # password field gets touched.
  usermod -L "$CCR_AGENT_USER"

  # Admin's authorized_keys is the operator's way in once PasswordAuthentication
  # is turned off in step_ssh. Never truncate this file — append-if-absent
  # only, so re-running never drops a key a previous run already added.
  local admin_home="/home/${CCR_ADMIN_USER}"
  local ssh_dir="${admin_home}/.ssh"
  local auth_keys="${ssh_dir}/authorized_keys"

  mkdir -p "$ssh_dir"
  ensure_line "$auth_keys" "$CCR_ADMIN_PUBKEY"

  chown -R "${CCR_ADMIN_USER}:${CCR_ADMIN_USER}" "$ssh_dir"
  chmod 700 "$ssh_dir"
  chmod 600 "$auth_keys"
  ok "Admin authorized_keys in place"
}

step_admin_sudo() {
  banner "Granting admin sudo access"

  # wheel membership: idempotent, only touch it if not already a member.
  if id -nG "$CCR_ADMIN_USER" | tr ' ' '\n' | grep -qx wheel; then
    ok "$CCR_ADMIN_USER already in wheel"
  else
    usermod -aG wheel "$CCR_ADMIN_USER"
    ok "Added $CCR_ADMIN_USER to wheel"
  fi

  # Own drop-in instead of uncommenting Arch's stock %wheel line in
  # /etc/sudoers — same idea as the agent drop-in below: scoped, idempotent,
  # and trivially reversible by deleting one file, instead of editing the
  # shared system file.
  #
  # Deliberately password-gated, NOT NOPASSWD: SSH access here is key-only,
  # so a stolen admin private key would otherwise be an instant, silent
  # path to root. Requiring the account password on top of the key is the
  # second factor that closes that gap. Do not "simplify" this to NOPASSWD.
  local dropin="/etc/sudoers.d/admin"
  local tmpfile
  tmpfile="$(mktemp)"

  printf '%s ALL=(ALL:ALL) ALL\n' "$CCR_ADMIN_USER" > "$tmpfile"

  if ! visudo -cf "$tmpfile"; then
    rm -f "$tmpfile"
    fail "Generated admin sudoers drop-in failed visudo validation; not installing it."
  fi

  if [[ -f "$dropin" ]] && cmp -s "$tmpfile" "$dropin"; then
    rm -f "$tmpfile"
    ok "Sudoers drop-in already up to date at $dropin"
  else
    install -m 0440 -o root -g root "$tmpfile" "$dropin"
    rm -f "$tmpfile"
    ok "Sudoers drop-in installed at $dropin"
  fi

  # Only place admin's password field gets touched, and only when a hash
  # was actually provided — an unset CCR_ADMIN_PASSWORD_HASH must never
  # reset a password set by a previous run or by a manual `passwd admin`.
  if [[ -n "$CCR_ADMIN_PASSWORD_HASH" ]]; then
    usermod -p "$CCR_ADMIN_PASSWORD_HASH" "$CCR_ADMIN_USER"
    ok "Admin password hash applied"
  fi
}

step_sudoers() {
  banner "Installing agent sudoers drop-in"

  local dropin="/etc/sudoers.d/agent"
  local tmpfile
  tmpfile="$(mktemp)"

  # Whitelist is exactly the daemon service management the architecture
  # names — nothing wider than that, no ALL=(ALL) ALL.
  #
  # Section 2.1 writes the example as `claudecode-daemon`, but the unit this
  # repo actually installs is `claudecode-remoted.service`, matching the binary
  # name. A whitelist naming a unit that does not exist grants nothing and
  # fails silently — the agent user would simply be prompted for a password it
  # does not have, the first time it tried to restart the daemon.
  {
    printf '%s ALL=(root) NOPASSWD: /usr/bin/systemctl restart claudecode-remoted\n' "$CCR_AGENT_USER"
    printf '%s ALL=(root) NOPASSWD: /usr/bin/systemctl status claudecode-remoted\n' "$CCR_AGENT_USER"
  } > "$tmpfile"

  # A broken sudoers file makes the machine unadministrable, so we validate
  # in a temp location first and only move it into place on success.
  if ! visudo -cf "$tmpfile"; then
    rm -f "$tmpfile"
    fail "Generated sudoers drop-in failed visudo validation; not installing it."
  fi

  install -m 0440 -o root -g root "$tmpfile" "$dropin"
  rm -f "$tmpfile"
  ok "Sudoers drop-in installed at $dropin"
}

step_ssh() {
  banner "Hardening sshd"

  # Never restart sshd on an untested config: back up once, apply options,
  # validate with `sshd -t`, and only reload/restart on success. On failure,
  # restore the pristine backup before failing so the running (already
  # validated, previously working) config stays in effect.
  backup_once "$SSHD_CONFIG"

  # Host keys must exist before `sshd -t` will validate anything at all — it
  # exits with "no hostkeys available" otherwise, which would make the check
  # below fail for a reason that has nothing to do with our config. Arch
  # generates them from sshdgenkeys.service, which never runs on a machine
  # without systemd as init. ssh-keygen -A only creates missing key types, so
  # this is idempotent and never replaces an existing host key (replacing one
  # would trip every client's known_hosts warning).
  ssh-keygen -A

  set_sshd_option "PasswordAuthentication" "no"
  set_sshd_option "PermitRootLogin" "no"
  set_sshd_option "PubkeyAuthentication" "yes"
  set_sshd_option "AllowUsers" "${CCR_ADMIN_USER} ${CCR_AGENT_USER}"
  set_sshd_option "Port" "${CCR_SSH_PORT}"

  if ! sshd -t; then
    err "sshd -t failed on the new config; restoring backup."
    cp -p "${SSHD_CONFIG}.ccr-orig" "$SSHD_CONFIG"
    fail "sshd config validation failed; restored previous config, nothing was restarted."
  fi

  if has_systemd; then
    systemctl enable sshd
    systemctl reload-or-restart sshd
    ok "sshd reloaded with new config"
  else
    warn "systemd not available as init, skipping sshd reload (config is valid and in place)"
  fi
}

step_fail2ban() {
  banner "Configuring fail2ban"

  # Own drop-in under jail.d/ instead of writing /etc/fail2ban/jail.local.
  # jail.local is the operator's file: overwriting it would silently destroy
  # any other jail they configured, which is exactly the "user-authored config
  # override" category CLAUDE.md forbids touching. A drop-in is deterministic
  # for our content, leaves theirs alone, and is reversible by deleting one file.
  mkdir -p /etc/fail2ban/jail.d
  cat > /etc/fail2ban/jail.d/claudecode-sshd.local <<EOF
[sshd]
enabled = true
port = ${CCR_SSH_PORT}
EOF

  svc_enable_now fail2ban
  ok "fail2ban sshd jail configured for port ${CCR_SSH_PORT}"
}

step_tailscale() {
  banner "Setting up Tailscale"

  if [[ "$CCR_SKIP_TAILSCALE" == "1" ]]; then
    warn "CCR_SKIP_TAILSCALE=1, skipping Tailscale entirely"
    return 0
  fi

  pkg_install tailscale
  svc_enable_now tailscaled

  if [[ -n "$CCR_TAILSCALE_AUTHKEY" ]]; then
    # Tailscale coming up is never allowed to fail the whole provisioning
    # run — network state on first boot is out of our control.
    if tailscale up --authkey="$CCR_TAILSCALE_AUTHKEY"; then
      ok "Tailscale authenticated"
    else
      warn "tailscale up failed; run it manually: tailscale up"
    fi
  else
    warn "CCR_TAILSCALE_AUTHKEY not set; Tailscale is installed but not authenticated. Run manually: tailscale up"
  fi
}

step_github_key() {
  banner "Setting up agent's GitHub SSH key"

  local agent_home="/home/${CCR_AGENT_USER}"
  local ssh_dir="${agent_home}/.ssh"
  local key="${ssh_dir}/id_ed25519"
  local ssh_config="${ssh_dir}/config"

  mkdir -p "$ssh_dir"

  # Never overwrite an existing key — it may already be registered on
  # GitHub, and regenerating it would silently orphan that registration.
  if [[ -f "$key" ]]; then
    ok "Agent GitHub key already exists, not touching it"
  else
    ssh-keygen -t ed25519 -N "" -C "${CCR_AGENT_USER}@$(hostname)" -f "$key" >/dev/null
    ok "Generated new Ed25519 key for $CCR_AGENT_USER"
  fi

  if ! grep -qxF "Host github.com" "$ssh_config" 2>/dev/null; then
    cat >> "$ssh_config" <<EOF
Host github.com
  IdentitiesOnly yes
  ForwardAgent no
  IdentityFile ~/.ssh/id_ed25519
EOF
  fi

  chown -R "${CCR_AGENT_USER}:${CCR_AGENT_USER}" "$ssh_dir"
  chmod 700 "$ssh_dir"
  chmod 600 "$key" "$ssh_config"
  chmod 644 "${key}.pub"
}

print_summary() {
  banner "Provisioning summary"

  local pubkey
  pubkey="$(cat "/home/${CCR_AGENT_USER}/.ssh/id_ed25519.pub" 2>/dev/null || echo "<not found>")"

  local tailscale_status="not authenticated (CCR_TAILSCALE_AUTHKEY not set)"
  if [[ "$CCR_SKIP_TAILSCALE" == "1" ]]; then
    tailscale_status="skipped (CCR_SKIP_TAILSCALE=1)"
  elif [[ -n "$CCR_TAILSCALE_AUTHKEY" ]]; then
    tailscale_status="authenticated"
  fi

  log "SSH port:        $CCR_SSH_PORT"
  log "Admin user:       $CCR_ADMIN_USER"
  log "Agent user:       $CCR_AGENT_USER"
  log "Tailscale:        $tailscale_status"
  log "Agent GitHub public key (register this on GitHub):"
  echo "  $pubkey"
  warn "Verify a NEW SSH session (port $CCR_SSH_PORT, key auth) works before closing this one."

  if [[ -z "$CCR_ADMIN_PASSWORD_HASH" ]]; then
    warn "=================================================================="
    warn "ADMIN HAS NO PASSWORD — sudo will NOT work for $CCR_ADMIN_USER yet."
    warn "sudo is password-gated by design; a locked/empty password can never"
    warn "authenticate. Run 'passwd $CCR_ADMIN_USER' now, BEFORE closing this"
    warn "root session — otherwise $CCR_ADMIN_USER can log in over SSH but"
    warn "cannot administer the machine."
    warn "=================================================================="
  fi

  if [[ -z "${CCR_PUBLIC_DOMAIN:-}" ]]; then
    warn "=================================================================="
    warn "CCR_PUBLIC_DOMAIN is not set — passkey pairing stays DISABLED."
    warn "This is fail-closed by design, not a bug: WebAuthn binds every"
    warn "passkey to this domain, so guessing one is worse than refusing."
    warn "Set CCR_PUBLIC_DOMAIN in .env (see .env.example) once you have"
    warn "chosen your domain, before pairing the first device."
    warn "=================================================================="
  fi
}

# --- run -------------------------------------------------------------------

step_packages
step_users
step_admin_sudo
step_sudoers
step_ssh
step_fail2ban
step_tailscale
step_github_key
print_summary

exit 0
