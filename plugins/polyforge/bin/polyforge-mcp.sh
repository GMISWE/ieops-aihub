#!/usr/bin/env bash
# polyforge-mcp.sh — MCP server entrypoint
# Auto-downloads the polyforge binary on first use.
# Checks for updates once per day and auto-updates if a newer version exists.
# ~/.polyforge/config.toml [binary] channel selects the bins-<channel> branch to
# download from. `dev` is the only channel that is published, and the default —
# see the case statement below for why there is no longer a `stable`.
set -euo pipefail

PLUGIN_DIR="${CLAUDE_PLUGIN_ROOT:-$(cd "$(dirname "$0")/.." && pwd)}"
INSTALL_PATH="$PLUGIN_DIR/bin/polyforge"
LAST_CHECK_FILE="$HOME/.polyforge/.last_binary_check"
CONFIG="$HOME/.polyforge/config.toml"

# Read [binary] channel from config.toml; fall back to "dev".
# Pure awk: no python3 dependency and no heredoc-in-$() (both break macOS bash 3.2).
CHANNEL="dev"
if [ -f "$CONFIG" ]; then
  _ch=$(awk -F= '
    /^[[:space:]]*\[/ { in_sec = ($0 ~ /\[binary\]/) }
    in_sec && $1 ~ /^[[:space:]]*channel[[:space:]]*$/ {
      if (match($2, /[a-zA-Z]+/)) { print substr($2, RSTART, RLENGTH); exit }
    }
  ' "$CONFIG" 2>/dev/null)
  [ -n "$_ch" ] && CHANNEL="$_ch"
fi

# `dev` is the ONLY channel, and `stable` is not a channel name any more
# (aihub#305). It used to be the default here and the path docs taught, but
# bins-stable was never published: publish-bins.yml creates it only on a `v*`
# tag push, the repo's single tag predates that workflow by a day, and no tag
# has been pushed since. A default-configured machine therefore fetched a 404 —
# and if it already had a binary, check_for_update just got an empty `latest`,
# printed one line on the MCP launcher's stderr (which the client UI does not
# surface) and returned 0, leaving that machine frozen on its old binary,
# retrying and failing every 24h, invisibly. Since every fix to internal/mcp/**
# and internal/cli/** ships as this binary, "frozen" meant reaching nobody.
#
# There is no tagged release process today, so a channel named "stable" was a
# name for something that does not exist; naming it back into existence is
# tracked separately. `stable` is kept ONLY as a legacy config value, mapped
# onto dev with a loud message, so that every already-broken machine recovers by
# updating the plugin and does not have to be told to edit its config.toml.
case "$CHANNEL" in
  dev) ;;
  stable)
    echo "polyforge: channel 'stable' no longer exists — bins-stable was never published (aihub#305). Using 'dev' instead. Delete the [binary] section from ~/.polyforge/config.toml, or set channel = \"dev\", to silence this." >&2
    CHANNEL="dev"
    ;;
  *)
    echo "polyforge: unknown channel '${CHANNEL}' in config.toml; defaulting to 'dev'" >&2
    CHANNEL="dev"
    ;;
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

  # Bounded like the version check at check_for_update: this also runs before
  # exec, so an unreachable or black-holed host would otherwise hang MCP
  # startup forever. --max-time is generous because this transfers a ~13MB
  # binary, not a 41-byte version file.
  if curl -fsSL --connect-timeout 10 --max-time 120 \
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

# Daily update check. Split into a function so it can be unit-tested without
# docker or network (tests/launcher-update-check.test.sh) — the previous inline
# form could only be exercised by running the real launcher, which is why the
# aihub#237 bug survived unnoticed.
#
# Every branch below either updates or SAYS WHY IT DID NOT. The bug this
# replaces collapsed "versions match", "cannot read the published version" and
# "cannot read my own version" into one `else` commented "already up to date",
# which then stamped LAST_CHECK_FILE. A binary that could not report its own
# SHA was therefore pinned to its current build permanently, reporting success
# every time.
#
# LAST_CHECK_FILE is still stamped on the paths we cannot act on, so the daily
# cadence (and the once-per-day warning) is preserved rather than warning on
# every MCP session. It is NOT stamped when a download was attempted and
# failed, so that case retries on the next launch.
check_for_update() {
  local now last
  now=$(date +%s)
  last=$(cat "$LAST_CHECK_FILE" 2>/dev/null || echo 0)
  # Treat any non-numeric content as "never checked". Without this, a file
  # containing e.g. `abc` makes `set -u` reject the arithmetic below as an
  # unbound variable and the launcher aborts rc=1 — the MCP server never
  # starts, from nothing worse than a corrupt cache file.
  case "$last" in ''|*[!0-9]*) last=0 ;; esac
  [ $((now - last)) -gt 86400 ] || return 0

  # gh gates access to the private bins branch. Its three failure modes were
  # previously one silent skip; the dev box that surfaced aihub#237 hit the
  # third (gh 2.4.0 predates `gh auth token`, so the guard failed with
  # "unknown command" rather than any auth problem, and printed nothing).
  local gh_token
  if ! command -v gh >/dev/null 2>&1; then
    echo "polyforge: skipping update check — gh CLI not found (https://cli.github.com). Retrying in 24h." >&2
    echo "$now" > "$LAST_CHECK_FILE"
    return 0
  fi
  if ! gh_token=$(gh auth token 2>/dev/null) || [ -z "$gh_token" ]; then
    echo "polyforge: skipping update check — 'gh auth token' failed. Run 'gh auth login'; if that subcommand is unknown, upgrade gh (it needs >= 2.7). Retrying in 24h." >&2
    echo "$now" > "$LAST_CHECK_FILE"
    return 0
  fi

  local latest current
  # --max-time bounds the check: this runs before exec, so a black-holed
  # network would otherwise hang MCP startup indefinitely.
  latest=$(curl -fsSL --max-time 10 \
    -H "Authorization: Bearer ${gh_token}" \
    "https://raw.githubusercontent.com/GMISWE/ieops-aihub/bins-${CHANNEL}/bin/version.txt" \
    2>/dev/null | grep -oE '[a-f0-9]{40}' | head -1 || echo "")
  current=$("$INSTALL_PATH" version 2>/dev/null | grep -oE '[a-f0-9]{40}' | head -1 || echo "")

  if [ -z "$latest" ]; then
    echo "polyforge: skipping update check — could not read the published version from bins-${CHANNEL}. Retrying in 24h." >&2
    echo "$now" > "$LAST_CHECK_FILE"
    return 0
  fi

  if [ -z "$current" ]; then
    # THE aihub#237 BUG. Not knowing our own version is not evidence of being
    # current — it is evidence of a binary built outside publish-bins.yml (a
    # plain `make build` stamped a 7-char SHA, which never matches [a-f0-9]{40}).
    # Update rather than pin; a published binary reports a full SHA, so the
    # next run compares normally and this self-heals.
    echo "polyforge: installed binary reports no commit SHA (likely a local build); updating to ${latest:0:8} to recover update capability." >&2
    if download_binary; then
      echo "$now" > "$LAST_CHECK_FILE"
    fi
    return 0
  fi

  if [ "$current" = "$latest" ]; then
    echo "$now" > "$LAST_CHECK_FILE"   # genuinely up to date
    return 0
  fi

  echo "polyforge: updating ${current:0:8} → ${latest:0:8}..." >&2
  if download_binary; then
    echo "$now" > "$LAST_CHECK_FILE"   # only stamp after a successful download
  fi
  return 0
}

# Tests source this file to drive the functions above in isolation. Everything
# below would download a binary and exec it, which a unit test must not do.
#
# The BASH_SOURCE check is load-bearing, not defensive style. Without it, a user
# who happens to have POLYFORGE_LAUNCHER_SOURCE_ONLY exported gets a launcher
# that exits 0 with no output and never execs the server — `return` outside a
# function fails, `|| exit 0` swallows it, and the MCP server silently does not
# start. That is precisely the silent-success failure mode this file is being
# changed to eliminate. BASH_SOURCE is bash 3.0+, so macOS bash 3.2 is fine.
if [ -n "${POLYFORGE_LAUNCHER_SOURCE_ONLY:-}" ] && [ "${BASH_SOURCE[0]}" != "$0" ]; then
  return 0
fi

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
  check_for_update
fi

unset _path_bin 2>/dev/null || true

# Keep /usr/local/bin/polyforge in sync with the managed binary so that bash
# invocations of `polyforge` (e.g. `polyforge init`) always use the same version
# as the MCP server. Silent no-op when /usr/local/bin is not writable.
if [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then
  ln -sf "$INSTALL_PATH" /usr/local/bin/polyforge 2>/dev/null || true
fi

exec "$INSTALL_PATH" "$@"
