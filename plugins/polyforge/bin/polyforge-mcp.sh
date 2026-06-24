#!/usr/bin/env bash
# polyforge-mcp.sh — MCP server entrypoint
# Auto-downloads the polyforge binary on first use.
# Checks for updates once per day and auto-updates if a newer version exists.
# ~/.polyforge/config.toml [binary] channel controls stable/dev.
set -euo pipefail

PLUGIN_DIR="${CLAUDE_PLUGIN_ROOT:-$(cd "$(dirname "$0")/.." && pwd)}"
INSTALL_PATH="$PLUGIN_DIR/bin/polyforge"
LAST_CHECK_FILE="$HOME/.polyforge/.last_binary_check"
CONFIG="$HOME/.polyforge/config.toml"

# Read [binary] channel from config.toml; fall back to "stable".
# Pure awk: no python3 dependency and no heredoc-in-$() (both break macOS bash 3.2).
CHANNEL="stable"
if [ -f "$CONFIG" ]; then
  _ch=$(awk -F= '
    /^[[:space:]]*\[/ { in_sec = ($0 ~ /\[binary\]/) }
    in_sec && $1 ~ /^[[:space:]]*channel[[:space:]]*$/ {
      if (match($2, /[a-zA-Z]+/)) { print substr($2, RSTART, RLENGTH); exit }
    }
  ' "$CONFIG" 2>/dev/null)
  [ -n "$_ch" ] && CHANNEL="$_ch"
fi

case "$CHANNEL" in
  stable|dev) ;;
  *) echo "polyforge: unknown channel '${CHANNEL}' in config.toml; defaulting to 'stable'" >&2; CHANNEL="stable" ;;
esac

download_binary() {
  echo "polyforge: downloading binary ($CHANNEL channel)..." >&2

  # Require gh auth (needed for private repo raw.githubusercontent.com access)
  local _gh_token
  if ! command -v gh &>/dev/null; then
    echo "polyforge: gh CLI not found — install from https://cli.github.com" >&2
    return 1
  fi
  if ! _gh_token=$(gh auth token 2>/dev/null); then
    echo "polyforge: gh CLI not authenticated — run 'gh auth login'" >&2
    return 1
  fi

  local os arch
  os=$(uname -s | tr '[:upper:]' '[:lower:]')
  arch=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
  local url="https://raw.githubusercontent.com/GMISWE/ieops-aihub/bins-${CHANNEL}/bin/polyforge-${os}-${arch}"

  mkdir -p "$PLUGIN_DIR/bin"

  # Download to a PER-PROCESS unique temp, then atomically rename into place.
  # A fixed-name temp (the old bin/polyforge.tmp) let concurrent MCP sessions
  # clobber each other, and a process killed between curl and mv left a stale
  # temp behind that the failure branch never cleaned up. mktemp gives each
  # process its own name. The EXIT trap removes the temp on any exit; the
  # separate INT/TERM trap calls `exit 143` so a signal mid-download actually
  # terminates the launcher (which in turn fires the EXIT trap to clean up).
  # Without the explicit exit, a bash signal handler returns and the script
  # would keep running: the launcher became unkillable mid-download and fell
  # through to exec a stale fallback binary. mktemp with an X-template is POSIX
  # and works on macOS bash 3.2; the $$-suffix is a last-resort fallback.
  local tmp
  tmp=$(mktemp "${INSTALL_PATH}.XXXXXX" 2>/dev/null) || tmp="${INSTALL_PATH}.$$.tmp"
  trap 'rm -f "$tmp"' EXIT
  trap 'exit 143' INT TERM

  if curl -fsSL \
      -H "Authorization: Bearer ${_gh_token}" \
      "$url" \
      -o "$tmp" \
    && mv "$tmp" "$INSTALL_PATH" \
    && chmod +x "$INSTALL_PATH"; then
    trap - EXIT INT TERM
    echo "polyforge: download complete" >&2
  else
    rm -f "$tmp"
    trap - EXIT INT TERM
    echo "polyforge: download failed from bins-${CHANNEL}" >&2
    return 1
  fi
}

# Sweep stale download temps left by a previous run that was hard-killed
# (e.g. kill -9) before its own EXIT trap could fire. Only remove temps not
# modified in the last minute so an actively-downloading sibling process is
# left untouched. find -mmin is portable across macOS (BSD) and GNU find.
if [ -d "$PLUGIN_DIR/bin" ]; then
  find "$PLUGIN_DIR/bin" -maxdepth 1 -name 'polyforge.*' -type f -mmin +1 \
    -exec rm -f {} + 2>/dev/null || true
fi

if [ ! -x "$INSTALL_PATH" ] || [ -L "$INSTALL_PATH" ]; then
  # Binary is missing or is a PATH fallback symlink.
  # Always attempt a real download first so we get the version that matches
  # this plugin release. The symlink case retries on every invocation by
  # design: we keep trying until a proper download succeeds.
  if download_binary 2>/dev/null; then
    : # download succeeded
  elif _path_bin=$(command -v polyforge 2>/dev/null) && [ "$_path_bin" != "$INSTALL_PATH" ]; then
    echo "polyforge: download failed, using system binary at $_path_bin" >&2
    mkdir -p "$PLUGIN_DIR/bin"
    ln -sf "$_path_bin" "$INSTALL_PATH"
  else
    download_binary  # retry with visible errors (will fail loudly)
  fi
else
  # Binary exists and is a real file — daily update check
  NOW=$(date +%s)
  LAST=$(cat "$LAST_CHECK_FILE" 2>/dev/null || echo 0)
  if [ $((NOW - LAST)) -gt 86400 ] && gh auth token &>/dev/null 2>&1; then
    _check_token=$(gh auth token 2>/dev/null)
    LATEST=$(curl -fsSL \
      -H "Authorization: Bearer ${_check_token}" \
      "https://raw.githubusercontent.com/GMISWE/ieops-aihub/bins-${CHANNEL}/bin/version.txt" \
      2>/dev/null | grep -oE '[a-f0-9]{40}' | head -1 || echo "")
    CURRENT=$("$INSTALL_PATH" version 2>/dev/null | grep -oE '[a-f0-9]{40}' | head -1 || echo "")
    if [ -n "$LATEST" ] && [ -n "$CURRENT" ] && [ "$CURRENT" != "$LATEST" ]; then
      echo "polyforge: updating ${CURRENT:0:8} → ${LATEST:0:8}..." >&2
      if download_binary; then
        echo "$NOW" > "$LAST_CHECK_FILE"   # only stamp after successful download
      fi
    else
      echo "$NOW" > "$LAST_CHECK_FILE"     # already up to date
    fi
  fi
fi

unset _path_bin 2>/dev/null || true

# Keep /usr/local/bin/polyforge in sync with the managed binary so that bash
# invocations of `polyforge` (e.g. `polyforge init`) always use the same version
# as the MCP server. Silent no-op when /usr/local/bin is not writable.
if [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then
  ln -sf "$INSTALL_PATH" /usr/local/bin/polyforge 2>/dev/null || true
fi

exec "$INSTALL_PATH" "$@"
