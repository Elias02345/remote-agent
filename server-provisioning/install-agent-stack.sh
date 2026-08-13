#!/usr/bin/env bash
# install-agent-stack.sh — installs the coding agents and agentctl for the
# agent user (docs/ARCHITECTURE.md Section 3). Idempotent, safe to re-run.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# source-path=SCRIPTDIR makes shellcheck resolve the source below relative to
# this file rather than the caller's working directory.
# shellcheck source-path=SCRIPTDIR
# shellcheck source=lib/common.sh
source "$SCRIPT_DIR/lib/common.sh"

require_root

# --- configuration -----------------------------------------------------------

# Same loader as provision.sh: a .env file is a convenience, never a way to
# silently shadow an explicit CCR_FOO=bar ./install-agent-stack.sh.
if [[ -f "$SCRIPT_DIR/.env" ]]; then
  while IFS='=' read -r ccr_key ccr_value; do
    [[ "$ccr_key" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || continue
    if [[ "$ccr_value" == '"'*'"' || "$ccr_value" == "'"*"'" ]]; then
      ccr_value="${ccr_value:1:${#ccr_value}-2}"
    fi
    if [[ -z "${!ccr_key+x}" ]]; then
      export "${ccr_key}=${ccr_value}"
    fi
  done < <(grep -vE '^[[:space:]]*(#|$)' "$SCRIPT_DIR/.env")
fi

CCR_AGENT_USER="${CCR_AGENT_USER:-agent}"

# The Antigravity CLI is deliberately absent from this default. Its npm package
# name could not be verified when this was written, and guessing one would make
# every run print a confusing failure for a package that may not exist under
# that name. Add it to CCR_AGENT_PACKAGES once the install command is known —
# see TODO_FOR_USER.md.
CCR_AGENT_PACKAGES="${CCR_AGENT_PACKAGES:-@anthropic-ai/claude-code @openai/codex}"

id -u "$CCR_AGENT_USER" >/dev/null 2>&1 ||
  fail "User '${CCR_AGENT_USER}' does not exist. Run provision.sh first."

AGENT_HOME="$(getent passwd "$CCR_AGENT_USER" | cut -d: -f6)"
[[ -n "$AGENT_HOME" ]] || fail "Could not determine home directory for ${CCR_AGENT_USER}."

PKG_RESULTS=()
GLOBAL_CONVENTIONS_STATUS="unchanged"

# --- helpers -----------------------------------------------------------------

# link_as_agent <target> <linkpath> — relative-safe symlink owned by the agent
# user. Applies the same rule as agentctl: never replace a regular file, that
# would silently destroy the operator's own content.
link_as_agent() {
  local target="$1" linkpath="$2"

  if [[ -L "$linkpath" ]]; then
    if [[ "$(readlink -f "$linkpath")" == "$(readlink -f "$target")" ]]; then
      ok "Already linked: ${linkpath}"
      return 0
    fi
    ln -sfn "$target" "$linkpath"
    ok "Repointed ${linkpath} -> ${target}"
  elif [[ -e "$linkpath" ]]; then
    warn "${linkpath} exists as a regular file — refusing to replace it."
    return 0
  else
    ln -s "$target" "$linkpath"
    ok "Linked ${linkpath} -> ${target}"
  fi

  chown -h "${CCR_AGENT_USER}:${CCR_AGENT_USER}" "$linkpath"
}

# --- steps -------------------------------------------------------------------

step_node() {
  banner "Installing Node.js and npm"
  pkg_install nodejs npm
}

step_agent_clis() {
  banner "Installing coding agent CLIs"

  local pkg
  for pkg in $CCR_AGENT_PACKAGES; do
    # A failing package must never abort the run: vendor package names and
    # registries change, and one bad name must not block the other tools or
    # the rest of the install. Results are reported in the summary instead.
    if npm install -g "$pkg" >/dev/null 2>&1; then
      ok "Installed ${pkg}"
      PKG_RESULTS+=("OK   ${pkg}")
    else
      warn "Failed to install ${pkg} — continuing."
      PKG_RESULTS+=("FAIL ${pkg}")
    fi
  done
}

step_agentctl() {
  banner "Installing agentctl"
  install -m 0755 "$SCRIPT_DIR/agentctl" /usr/local/bin/agentctl
  ok "agentctl installed to /usr/local/bin/agentctl"
}

step_global_conventions() {
  banner "Distributing global agent conventions"

  local claude_dir="${AGENT_HOME}/.claude"
  local global_md="${claude_dir}/CLAUDE.md"

  install -d -o "$CCR_AGENT_USER" -g "$CCR_AGENT_USER" -m 0755 "$claude_dir"

  # Never overwrite an existing global conventions file: it is the operator's
  # machine-wide rules, and a re-run silently discarding their edits would be
  # the worst kind of bug — invisible until an agent misbehaves.
  if [[ -f "$global_md" ]]; then
    ok "Global CLAUDE.md already exists, leaving it untouched"
  else
    install -o "$CCR_AGENT_USER" -g "$CCR_AGENT_USER" -m 0644 \
      "$SCRIPT_DIR/templates/global-CLAUDE.md" "$global_md"
    GLOBAL_CONVENTIONS_STATUS="created from template"
    ok "Global CLAUDE.md created at ${global_md}"
  fi

  # Codex and Antigravity read their own filenames; mirroring via symlink is
  # what makes CLAUDE.md the single source of truth (Section 3.2).
  local codex_dir="${AGENT_HOME}/.codex"
  local antigravity_dir="${AGENT_HOME}/.antigravity"
  install -d -o "$CCR_AGENT_USER" -g "$CCR_AGENT_USER" -m 0755 "$codex_dir" "$antigravity_dir"

  link_as_agent "$global_md" "${codex_dir}/AGENTS.md"
  link_as_agent "$global_md" "${antigravity_dir}/ANTIGRAVITY.md"
}

print_summary() {
  banner "Agent stack summary"

  log "Agent user:       ${CCR_AGENT_USER}"
  log "Global CLAUDE.md: ${AGENT_HOME}/.claude/CLAUDE.md (${GLOBAL_CONVENTIONS_STATUS})"
  log "agentctl:         /usr/local/bin/agentctl"
  log "Package results:"
  local line
  for line in "${PKG_RESULTS[@]}"; do
    echo "  ${line}"
  done

  warn "Antigravity CLI is not in the default package list — its install command"
  warn "could not be verified. Add it to CCR_AGENT_PACKAGES once you know it."
  log "Run 'agentctl init' once in every project checkout to link the context files."
}

# --- run ---------------------------------------------------------------------

step_node
step_agent_clis
step_agentctl
step_global_conventions
print_summary

exit 0
