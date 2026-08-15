#!/usr/bin/env bash
# common.sh — shared helpers for server-provisioning scripts.
# This file is sourced, never executed directly.

# Every script that sources this runs as root and resolves commands by name:
# pacman, useradd, visudo, curl, npm, go. An inherited PATH decides which
# binaries those names mean, so a caller who can influence the environment can
# redirect every one of them. Pin it here, once, before any of them runs.
#
# This is the same reason `sudo` ships secure_path. Scripts that genuinely need
# something outside these directories should use an absolute path.
export PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

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

# require_valid_name <var-name> <value> — refuses anything that is not a plain
# POSIX-ish identifier.
#
# These values are not merely used, they are *interpolated into config files
# that grant privilege*: usernames land in /etc/sudoers.d entries, the interface
# name lands in smb.conf. A value containing a newline would inject a second,
# syntactically valid line — and `visudo -cf` would happily accept
#
#     agent ALL=(root) NOPASSWD: /usr/bin/systemctl restart claudecode-remoted
#     eve ALL=(ALL) NOPASSWD: ALL
#
# as a correct sudoers file, because it is one. Validation has to happen before
# the value is written, not after.
#
# These come from a root-owned .env today, so this is defence in depth rather
# than a live hole. It costs three lines.
require_valid_name() {
  local var="$1" value="$2"
  if [[ ! "$value" =~ ^[a-z_][a-z0-9_-]{0,31}$ ]]; then
    fail "${var} must be a plain name matching ^[a-z_][a-z0-9_-]{0,31}\$, got: '${value}'"
  fi
}

# --- configuration loading ---------------------------------------------------

# load_env_file <path> — sources a key=value config file into the environment,
# without letting it become a root command-execution primitive.
#
# The naive version of this loop exports every syntactically valid shell
# identifier it finds. That is not a config loader, it is an arbitrary
# environment injection: PATH, LD_PRELOAD, LD_LIBRARY_PATH, IFS, BASH_ENV,
# GIT_SSH_COMMAND and friends all pass "is a valid identifier", and the script
# that reads them then runs pacman and useradd as root. Anyone who can create a
# file next to otherwise-reviewed scripts would own the machine.
#
# So three rules, all of them cheap:
#
#   1. The file must be owned by root and not writable by group or other. A
#      config file that a non-root user can edit is a root shell with extra
#      steps.
#   2. Only CCR_* and NTFY_URL are accepted. Everything else is skipped with a
#      warning rather than silently, because a typo'd key that quietly does
#      nothing is its own kind of bug.
#   3. An explicitly exported variable always wins — the file is a convenience
#      for operators who would rather edit a file than export vars, never a way
#      to shadow `CCR_FOO=bar ./provision.sh`.
load_env_file() {
  local file="$1"
  [[ -f "$file" ]] || return 0

  local owner perms
  owner="$(stat -c '%U' "$file" 2>/dev/null || echo '?')"
  perms="$(stat -c '%a' "$file" 2>/dev/null || echo '?')"
  if [[ "$owner" != "root" ]]; then
    fail "${file} must be owned by root (owned by '${owner}'). Refusing to read configuration a non-root user can rewrite."
  fi
  # Last two octal digits are group and other. Anything but 0 in the write bit
  # means someone other than root can change what this script does.
  if [[ "$perms" =~ [2367]$ || "$perms" =~ [2367].$ ]]; then
    fail "${file} is group- or world-writable (mode ${perms}). chmod 0600 it before re-running."
  fi

  local ccr_key ccr_value
  while IFS='=' read -r ccr_key ccr_value; do
    [[ "$ccr_key" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || continue
    if [[ ! "$ccr_key" =~ ^CCR_[A-Z0-9_]+$ && "$ccr_key" != "NTFY_URL" ]]; then
      warn "${file}: ignoring '${ccr_key}' — only CCR_* and NTFY_URL are read from this file."
      continue
    fi
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
  done < <(grep -vE '^[[:space:]]*(#|$)' "$file")
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
#
# Uses -Syu, not -Sy. `pacman -Sy <pkg>` refreshes the package databases and
# then installs against them without upgrading anything else — the textbook
# partial upgrade, which Arch does not support and which breaks systems for
# real: the new package links against a libfoo.so.7 that the not-yet-upgraded
# system still provides as .so.6. There is no "install one package safely on
# Arch" mode; -Syu is the supported way to install anything at all.
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
    if pacman -Syu --noconfirm --needed "${todo[@]}"; then
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

# set_sshd_option <file> <key> <value> — makes <file> contain exactly one
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
#
# Matching is case-INSENSITIVE, because sshd is. `passwordauthentication yes`
# is a valid, active directive that sshd honours, and a case-sensitive matcher
# neither sees it nor comments it out — it just appends `PasswordAuthentication
# no` further down. sshd takes the FIRST occurrence of a keyword, so the
# lowercase line wins and password logins stay enabled on a config that reads,
# to the eye, as hardened. That is the worst possible outcome for this
# function: a security setting that looks applied and is not.
set_sshd_option() {
  local file="$1"
  local key="$2"
  local value="$3"
  local desired="${key} ${value}"

  [[ -e "$file" ]] || : > "$file"

  # Active (non-comment) lines for this key: optional leading whitespace,
  # the key, then whitespace before the value.
  local active
  active="$(grep -iE "^[[:space:]]*${key}[[:space:]]+" "$file" || true)"

  if [[ "$active" == "$desired" ]]; then
    return 0
  fi

  if [[ -n "$active" ]]; then
    # The I flag on the address makes the match case-insensitive (GNU sed).
    sed -i -E "/^[[:space:]]*${key}[[:space:]]+/Is/^/# /" "$file"
  fi

  printf '%s\n' "$desired" >> "$file"
}
