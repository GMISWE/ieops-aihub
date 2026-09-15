#!/usr/bin/env bash
# Install the polyforge plugin into the opencode coding agent (sst/opencode).
#
# Usage:  plugins/polyforge/opencode/install.sh [<project-dir>]      (default: current directory)
#
# Env:    OPENCODE_CONFIG_DIR   opencode's own config dir override (same var opencode itself
#                               honours -- confirmed via strings on the compiled binary:
#                               `G===k.OPENCODE_CONFIG_DIR`). Default:
#                               ${XDG_CONFIG_HOME:-$HOME/.config}/opencode
#         POLYFORGE_OPENCODE_AGENT_DIR   override where generated agent files go.
#                               Default: $OC_CONFIG_DIR/agent
#
# WHY A SCRIPT AND NOT "the plugin ships it" (mirrors pi/install.sh's own reasoning, adapted):
# opencode has neither a hooks.json mechanism (Claude Code / Copilot) nor a pi.on(event, ...)
# subscription API (pi) -- see opencode/plugin/polyforge-hooks.js's own header for the shape
# this forces on the bridge. Three things still need a machine-local, install-time step
# regardless of that shape:
#   - agent definitions (aihub#642: generated per machine from internal/roles/definitions/*.yaml
#     against THIS machine's concrete model catalog via `polyforge roles generate opencode`,
#     same reasoning as pi and codex -- there is no static file to ship for these);
#   - the MCP server entry, because opencode.json's string interpolation is `{env:VAR}` /
#     `{file:path}` -- NOT shell-style `${VAR}` (confirmed via opencode's own embedded docs) --
#     so a `${CLAUDE_PLUGIN_ROOT}`-style token the way Claude Code's .mcp.json uses would never
#     expand; this script must bake in the real absolute path itself;
#   - the hook bridge plugin file, because opencode's global auto-load directory
#     ($OC_CONFIG_DIR/plugin/) is OUTSIDE this checkout, so __dirname inside the copied file
#     cannot find the checkout on its own -- see polyforge-hooks.js's resolvePluginRoot() and
#     the sidecar JSON this script writes next to the copy.
#
# Everything this writes is listed under "what this touches" in the summary at the end.
# Re-running is safe: nothing is overwritten without a backup alongside it. One case is not a
# plain file copy and is handled explicitly -- opencode.json is MERGED (other top-level keys,
# and other MCP servers in it, are preserved), not replaced.

set -euo pipefail

PLUGIN_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OC_CONFIG_DIR="${OPENCODE_CONFIG_DIR:-${XDG_CONFIG_HOME:-$HOME/.config}/opencode}"
AGENT_DIR="${POLYFORGE_OPENCODE_AGENT_DIR:-$OC_CONFIG_DIR/agent}"
PROJECT_DIR="$(cd "${1:-$PWD}" && pwd)"
# $$ as well as the timestamp: date has one-second granularity, and two runs inside the same
# second would otherwise have the second overwrite the first run's backup (same reasoning as
# pi/install.sh's STAMP).
STAMP="$(date +%Y%m%d%H%M%S)-$$"

say()  { printf '  %s\n' "$*"; }
step() { printf '\n== %s\n' "$*"; }
warn() { printf '  \xe2\x9a\xa0\xef\xb8\x8f  %s\n' "$*" >&2; }
die()  { printf '  \xe2\x9d\x8c %s\n' "$*" >&2; exit 1; }

# Back up an existing file before replacing it, so a re-run never destroys local edits.
place() {
  local src="$1" dst="$2"
  if [ -e "$dst" ] && ! cmp -s "$src" "$dst"; then
    cp -p "$dst" "$dst.bak-$STAMP"
    say "backed up existing $(basename "$dst") -> $(basename "$dst").bak-$STAMP"
  fi
  cp -p "$src" "$dst"
}

# opencode-hooks.json is nested one level under $PLUGIN_ROOT (see its own _comment for why:
# aihub#653's file_scope lock covers exactly plugins/polyforge/opencode, not a new plugin-root
# sibling file), so the guard below checks the nested path, not "$PLUGIN_ROOT/opencode-hooks.json".
[ -f "$PLUGIN_ROOT/opencode/opencode-hooks.json" ] || die "not a polyforge plugin checkout: $PLUGIN_ROOT"

step "opencode runtime"
OC_BIN="$(command -v opencode || true)"
if [ -n "$OC_BIN" ]; then
  say "opencode found at $OC_BIN"
else
  warn "opencode is not on PATH. The plugin/MCP config below is still installed, but nothing"
  warn "will load it until opencode itself is installed -- see https://opencode.ai for how."
fi

step "hook bridge plugin -> $OC_CONFIG_DIR/plugin/polyforge-hooks.js"
# opencode's global auto-load plugin directory: dropping a .js/.ts file there loads it with
# zero config entry (aihub#653 measurement). No settings.json / plugin registry entry needed,
# unlike pi's extensions/ (which is also auto-loaded, so the parallel holds) or Claude Code's
# hooks.json (which is not -- CC needs an explicit hooks.json entry naming the script).
mkdir -p "$OC_CONFIG_DIR/plugin"
place "$PLUGIN_ROOT/opencode/plugin/polyforge-hooks.js" "$OC_CONFIG_DIR/plugin/polyforge-hooks.js"
# The plugin is copied out of the checkout, so __dirname no longer sits inside it. This
# records where the hook scripts and opencode-hooks.json actually live -- and per
# polyforge-hooks.js's own header comment, "pluginRoot" here means the TRUE root
# ($PLUGIN_ROOT, i.e. plugins/polyforge), NOT plugins/polyforge/opencode, even though the
# bridge internally appends /opencode/opencode-hooks.json when it reads the hook table back
# (hooksJsonPath()) -- CLAUDE_PLUGIN_ROOT must resolve to the TRUE root so the shared hook
# scripts (hooks/pf-commit-guard, bin/pf-chain-hook.cjs) can find their own siblings.
# Written with a real JSON encoder, not printf: a checkout path containing a quote or a
# backslash would produce invalid JSON, and the bridge swallows the parse error and goes
# INERT -- i.e. opencode would run with no IR1 gate and no message saying so (same failure
# mode pi/install.sh's identical step guards against).
if command -v python3 >/dev/null 2>&1; then
  python3 -c 'import json,sys; print(json.dumps({"pluginRoot": sys.argv[1]}, indent=2))' \
    "$PLUGIN_ROOT" > "$OC_CONFIG_DIR/plugin/polyforge-opencode.json"
else
  case "$PLUGIN_ROOT" in
    *'"'*|*'\'*) die "checkout path contains a quote or backslash and python3 is unavailable to encode it safely: $PLUGIN_ROOT" ;;
  esac
  printf '{\n  "pluginRoot": "%s"\n}\n' "$PLUGIN_ROOT" > "$OC_CONFIG_DIR/plugin/polyforge-opencode.json"
fi
say "recorded pluginRoot=$PLUGIN_ROOT"

step "agent definitions -> $AGENT_DIR/"
#
# ── DIVISION OF LABOUR WITH `polyforge roles install` (aihub#683) ────────────────────
# THIS SCRIPT OWNS FIRST INSTALL. It creates $AGENT_DIR and the things agent files are
# useless without: the opencode runtime check, the hook-bridge plugin in
# $OC_CONFIG_DIR/plugin/, its pluginRoot sidecar, and the merged project opencode.json.
# None of that is `roles install`'s business.
#
# `polyforge roles install` OWNS KEEPING THEM UP TO DATE, afterwards. It knows this same
# directory from a table in Go (internal/cli/roles_install.go's opencodeTarget, which
# mirrors the THREE-level expansion at the top of this file exactly:
# POLYFORGE_OPENCODE_AGENT_DIR, then OPENCODE_CONFIG_DIR, then XDG_CONFIG_HOME, then
# ~/.config/opencode/agent -- note the SINGULAR `agent`). It regenerates only for
# harnesses whose directory ALREADY EXISTS, so it can never be what first creates it.
# `polyforge serve` runs it on every MCP boot; `/pf-update` is the manual entry point.
#
# ⚠️ IF YOU CHANGE THE AGENT_DIR EXPANSION, CHANGE opencodeTarget TOO. The two write the
# SAME files to the SAME place; if they disagree, this installer puts them in one
# directory and every later boot refreshes a different one, silently. internal/cli's
# TestResolveHarnessTargets_HonoursEveryOverride pins the Go half, including the
# precedence order, against those lines.
# ────────────────────────────────────────────────────────────────────────────────────
#
# aihub#642 (pi, codex) / aihub#653 (opencode): step-<role>.md is GENERATED per machine from
# internal/roles/definitions/*.yaml + this machine's ~/.polyforge/config.toml [roles.tiers]
# candidates, via `polyforge roles generate opencode` (internal/cli/roles_generate.go). This
# cannot go through place(): place() diffs a single source file against a single destination,
# but here the SOURCE is a subcommand invocation, not a file on disk.
#
# Non-fatal by design, matching pi/install.sh's identical step: a machine bootstrapping
# opencode for the first time may not have the polyforge binary on PATH or at
# $PLUGIN_ROOT/bin/polyforge yet, and that must not abort the rest of the install under
# `set -euo pipefail`.
POLYFORGE_BIN="$(command -v polyforge || true)"
if [ -z "$POLYFORGE_BIN" ] && [ -x "$PLUGIN_ROOT/bin/polyforge" ]; then
  POLYFORGE_BIN="$PLUGIN_ROOT/bin/polyforge"
fi
mkdir -p "$AGENT_DIR"
if [ -n "$POLYFORGE_BIN" ]; then
  if "$POLYFORGE_BIN" roles generate opencode --out "$AGENT_DIR"; then
    say "generated step-*.md agent definitions into $AGENT_DIR/"
  else
    warn "polyforge roles generate opencode failed -- $AGENT_DIR/ may be missing or stale"
    warn "re-run manually once fixed: $POLYFORGE_BIN roles generate opencode --out \"$AGENT_DIR\""
  fi
else
  warn "polyforge binary not found on PATH or at $PLUGIN_ROOT/bin/polyforge -- skipped"
  warn "agent generation. Once it is available, run:"
  warn "    polyforge roles generate opencode --out \"$AGENT_DIR\""
fi

step "MCP config -> $PROJECT_DIR/opencode.json"
# opencode.json's string interpolation is {env:VAR} / {file:path} -- NOT shell-style ${VAR}
# (confirmed via opencode's own embedded docs) -- so the placeholder in mcp.json.template must
# be substituted with the real absolute path HERE, not left for opencode to expand.
#
# MERGED, not replaced. opencode.json is the project's file and may already declare other
# top-level keys and other MCP servers; writing the template over it would silently delete
# every one of them, leaving them only in a .bak the user has no reason to look at. Only the
# `mcp.polyforge` entry is imposed; everything else is preserved (same posture as pi's
# .mcp.json merge step and Claude Code's own convention).
TEMPLATE="$PLUGIN_ROOT/opencode/mcp.json.template"
MCP_DST="$PROJECT_DIR/opencode.json"
if command -v python3 >/dev/null 2>&1; then
  # Snapshot the pre-merge bytes so we can tell, AFTER merging, whether the merge actually
  # changed anything -- same "only back up on a real change" gate place() uses (cmp -s), not
  # a backup on every re-run even when the merge is a no-op (aihub#653 code_review minor #7).
  PRE_MERGE=""
  if [ -e "$MCP_DST" ]; then
    PRE_MERGE="$MCP_DST.pre-merge-$STAMP"
    cp -p "$MCP_DST" "$PRE_MERGE"
  fi
  python3 - "$TEMPLATE" "$MCP_DST" "$PLUGIN_ROOT/bin/polyforge-mcp.sh" <<'PY'
import json, sys
tmpl_path, dst_path, real_script = sys.argv[1], sys.argv[2], sys.argv[3]
tmpl = json.load(open(tmpl_path))
entry = tmpl["mcp"]["polyforge"]
entry["command"] = [c.replace("__POLYFORGE_PLUGIN_ROOT__/bin/polyforge-mcp.sh", real_script) for c in entry["command"]]
try:
    cur = json.load(open(dst_path))
    if not isinstance(cur, dict):
        raise ValueError
except Exception:
    cur = {}
mcp = cur.setdefault("mcp", {})
kept = [k for k in mcp if k != "polyforge"]
mcp["polyforge"] = entry
json.dump(cur, open(dst_path, "w"), indent=2)
open(dst_path, "a").write("\n")
print("  merged into %s; kept %d other MCP server(s): %s"
      % (dst_path, len(kept), ", ".join(kept) or "-"))
PY
  if [ -n "$PRE_MERGE" ]; then
    if cmp -s "$PRE_MERGE" "$MCP_DST"; then
      rm -f "$PRE_MERGE"
      say "opencode.json already up to date (no change, no backup kept)"
    else
      mv "$PRE_MERGE" "$MCP_DST.bak-$STAMP"
      say "backed up the previous opencode.json -> opencode.json.bak-$STAMP"
    fi
  else
    say "wrote opencode.json (polyforge MCP server)"
  fi
else
  warn "$MCP_DST: python3 is unavailable to merge into it -- NOT overwriting."
  warn "Add this by hand (with __POLYFORGE_PLUGIN_ROOT__/bin/polyforge-mcp.sh replaced by the"
  warn "absolute path $PLUGIN_ROOT/bin/polyforge-mcp.sh), or opencode will never see the"
  warn "polyforge MCP tools:"
  sed 's/^/      /' "$TEMPLATE" >&2
fi

cat <<EOF

== done

what this touches
  $OC_CONFIG_DIR/plugin/polyforge-hooks.js       hook bridge (opencode Hooks -> polyforge's bash hooks)
  $OC_CONFIG_DIR/plugin/polyforge-opencode.json  sidecar recording where the checkout lives
  $AGENT_DIR/step-*.md                           polyforge agent definitions (generated per machine)
  $PROJECT_DIR/opencode.json                     polyforge MCP server entry

verify
  cd "$PROJECT_DIR" && opencode run "list your tools"
  Expect polyforge_pf_* tools (45 as of aihub#642's role set) among them.

  A banned commit message is actually blocked (not just matched):
  cd "$PROJECT_DIR" && node --test plugins/polyforge/opencode/tests/opencode-bridge.test.cjs
  (this runs the real bridge against the real pf-commit-guard/pf-chain-hook.cjs scripts --
  see that file's own header for what it does and does not cover. aihub#659 wired this file
  into CI and added an opencode row to pf-commit-guard.test.sh's cross-harness matcher
  table, both out of aihub#653's file_scope lock at the time.)
EOF
