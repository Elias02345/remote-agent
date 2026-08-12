#!/usr/bin/env bash
# common.sh — shared helpers for server-provisioning scripts.
# This file is sourced, never executed directly.

# --- output helpers --------------------------------------------------------

if [[ -t 1 ]]; then
  C_CYAN=$'\033[36m'; C_GREEN=$'\033[32m'; C_YELLOW=$'\033[33m'
  C_RED=$'\033[31m'; C_BOLD=$'\033[1m'; C_RESET=$'\033[0m'
else
  C_CYAN=''; C_GREEN=''; C_YELLOW=''; C_RED=''; C_BOLD=''; C_RESET=''
fi

log()    { echo "${C_CYAN}[INFO]${C_RESET} $*"; }
ok()     { echo "${C_GREEN}[OK]${C_RESET} $*"; }
warn()   { echo "${C_YELLOW}[WARN]${C_RESET} $*" >&2; }
err()    { echo "${C_RED}[ERROR]${C_RESET} $*" >&2; }
fail()   { echo "${C_RED}[FATAL]${C_RESET} $*" >&2; exit 1; }
banner() { echo "${C_BOLD}${C_CYAN}━━━ $* ━━━${C_RESET}"; }

# --- privilege / capability checks -----------------------------------------

require_root() {
  if [[ "${EUID}" -ne 0 ]]; then
    fail "This script must be run as root (current EUID=${EUID})."
  fi
}

command_exists() {
  command -v "$1" >/dev/null 2>&1
}

# has_systemd — true only when systemd is actually usable as init.
# /run/systemd/system only exists when systemd is PID 1, so this correctly
# takes the false branch inside containers that have systemd installed but
# not running as init (e.g. the Phase 1 test harness).
has_systemd() {
  [[ -d /run/systemd/system ]]
}

# --- package management -----------------------------------------------------

# pkg_install <pkg>... — installs via pacman, skipping already-installed
# packages, retrying transient failures (network/mirror hiccups) 3 times.
pkg_install() {
  local pkgs=("$@")
  local todo=()
  local pkg

  for pkg in "${pkgs[@]}"; do
    if pacman -Qi "$pkg" >/dev/null 2>&1; then
      continue
    fi
    todo+=("$pkg")
  done

  if [[ "${#todo[@]}" -eq 0 ]]; then
    ok "Packages already installed: ${pkgs[*]}"
    return 0
  fi

  local attempt
  for attempt in 1 2 3; do
    if pacman -Sy --noconfirm --needed "${todo[@]}"; then
      ok "Installed: ${todo[*]}"
      return 0
    fi
    warn "pacman install attempt ${attempt}/3 failed for: ${todo[*]}"
    [[ "$attempt" -lt 3 ]] && sleep 5
  done

  fail "Failed to install packages after 3 attempts: ${todo[*]}"
}

# --- systemd -----------------------------------------------------------------

# svc_enable_now <unit> — enables and starts a unit, but only when systemd is
# actually running as init. In containers without systemd this would hang or
# error, so we warn and return success instead of failing the whole run.
svc_enable_now() {
  local unit="$1"
  if ! has_systemd; then
    warn "systemd not available as init, skipping enable/start of ${unit}"
    return 0
  fi
  systemctl enable --now "$unit"
}

# --- idempotent file mutation helpers ---------------------------------------

# backup_once <file> — copies <file> to <file>.ccr-orig only if that backup
# does not exist yet, so re-running provisioning never overwrites the
# original with an already-modified version.
backup_once() {
  local file="$1"
  local backup="${file}.ccr-orig"
  if [[ -e "$backup" ]]; then
    return 0
  fi
  cp -p "$file" "$backup"
}

# ensure_line <file> <line> — appends the exact line only if not already
# present verbatim. Creates the file if missing.
ensure_line() {
  local file="$1"
  local line="$2"
  [[ -e "$file" ]] || : > "$file"
  if grep -qxF "$line" "$file"; then
    return 0
  fi
  printf '%s\n' "$line" >> "$file"
}

# set_sshd_option <key> <value> — makes sshd_config contain exactly one
# active "<key> <value>" line.
#
# Idempotency is checked BEFORE any mutation: if the only active line for
# this key already equals the desired line, we return immediately without
# touching the file. This matters because a naive "always comment out, then
# always append" implementation is *not* idempotent — it would comment out
# its own previous append on every re-run and grow the file by one dead
# line each time, even though the visible sshd behaviour never changes.
# Only when the current state differs do we comment out the existing active
# line(s) for the key and append the desired one.
set_sshd_option() {
  local file="/etc/ssh/sshd_config"
  local key="$1"
  local value="$2"
  local desired="${key} ${value}"

  [[ -e "$file" ]] || : > "$file"

  # Active (non-comment) lines for this key: optional leading whitespace,
  # the key, then whitespace before the value. sshd keywords are matched
  # case-insensitively by sshd itself, but this script always writes the
  # same casing it reads, so a plain match is sufficient here.
  local active
  active="$(grep -E "^[[:space:]]*${key}[[:space:]]+" "$file" || true)"

  if [[ "$active" == "$desired" ]]; then
    return 0
  fi

  if [[ -n "$active" ]]; then
    sed -i -E "/^[[:space:]]*${key}[[:space:]]+/s/^/# /" "$file"
  fi

  printf '%s\n' "$desired" >> "$file"
}
