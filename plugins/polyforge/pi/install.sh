#!/usr/bin/env bash
# Install the polyforge plugin into the pi coding agent (@earendil-works/pi-coding-agent).
#
# Usage:  plugins/polyforge/pi/install.sh [<project-dir>]      (default: current directory)
#
# WHY A SCRIPT AND NOT "the plugin ships it".
# pi's package mechanism does not carry agent definitions or extensions into a project.
# Agents are discovered in exactly two places: ~/.pi/agent/agents/ (always loaded) and
# <project>/.pi/agents/ (loaded ONLY when agentScope is "project"/"both", off by default —
# a project-local agent is a repo-controlled prompt that can run bash, so the default-off
# is a deliberate supply-chain gate, not an oversight). "Distributed with the plugin" would
# land in the project layer and be ignored. So user-level installation is the only route,
# and something has to copy the files there.
#
# Everything this writes is listed under "what this touches" in the summary at the end.
# Re-running is safe: nothing is overwritten without a backup alongside it. Two cases are
# not plain file copies and are handled explicitly —
#   .mcp.json         is MERGED (other MCP servers in it are preserved), not replaced;
#   .agents/skills/   is a tree, so the whole directory is backed up before it is refreshed.

set -euo pipefail

PLUGIN_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PI_DIR="${PI_AGENT_DIR:-$HOME/.pi/agent}"
PROJECT_DIR="$(cd "${1:-$PWD}" && pwd)"
# $$ as well as the timestamp: date has one-second granularity, and two runs inside the
# same second would otherwise have the second overwrite the first run's backup.
STAMP="$(date +%Y%m%d%H%M%S)-$$"

say()  { printf '  %s\n' "$*"; }
step() { printf '\n== %s\n' "$*"; }
warn() { printf '  ⚠️  %s\n' "$*" >&2; }
die()  { printf '  ❌ %s\n' "$*" >&2; exit 1; }

# Back up an existing file before replacing it, so a re-run never destroys local edits.
place() {
  local src="$1" dst="$2"
  if [ -e "$dst" ] && ! cmp -s "$src" "$dst"; then
    cp -p "$dst" "$dst.bak-$STAMP"
    say "backed up existing $(basename "$dst") -> $(basename "$dst").bak-$STAMP"
  fi
  cp -p "$src" "$dst"
}

[ -f "$PLUGIN_ROOT/pi-hooks.json" ] || die "not a polyforge plugin checkout: $PLUGIN_ROOT"

step "pi runtime"
PI_BIN="$(command -v pi || true)"
if [ -n "$PI_BIN" ]; then
  say "pi found at $PI_BIN"
else
  warn "pi is not on PATH. Install it first, pinning the exact version:"
  warn "    npm install -g --ignore-scripts @earendil-works/pi-coding-agent@0.85.1"
  warn "Pin exactly — pi ships breaking changes in PATCH releases (5 of 17 releases over"
  warn "67 days were breaking, 3 of those were patches), so a caret range is not safe."
fi

step "MCP adapter"
# The adapter is what turns polyforge's 45 MCP tools into individually-named pi tools.
# Without it there is nothing for the IR1 gate to match on.
if [ -d "$PI_DIR/npm/node_modules/pi-mcp-adapter" ]; then
  say "pi-mcp-adapter already installed ($(sed -n 's/.*"version": *"\([^"]*\)".*/\1/p' \
      "$PI_DIR/npm/node_modules/pi-mcp-adapter/package.json" | head -1))"
elif [ -n "$PI_BIN" ]; then
  say "installing pi-mcp-adapter ..."
  "$PI_BIN" install npm:pi-mcp-adapter
else
  warn "skipped (needs pi on PATH): pi install npm:pi-mcp-adapter"
fi

step "hook bridge extension -> $PI_DIR/extensions/polyforge/"
mkdir -p "$PI_DIR/extensions/polyforge"
# index.js, not .ts: pi's loader accepts either (core/extensions/loader.js checks for both
# and runs them through jiti), and plain CommonJS is additionally requireable by
# `node --test` with no transpiler — which is what makes tests/pi-bridge.test.cjs able to
# drive the real gate handlers.
place "$PLUGIN_ROOT/pi/extensions/polyforge/index.js" "$PI_DIR/extensions/polyforge/index.js"
# A leftover index.ts from an older install would shadow this one (loader checks .ts first).
rm -f "$PI_DIR/extensions/polyforge/index.ts"
# The extension is copied out of the checkout, so __dirname no longer sits inside it.
# This records where the hook scripts and pi-hooks.json actually live.
# Written with a real JSON encoder, not printf: a checkout path containing a quote or a
# backslash would produce invalid JSON, and the extension swallows the parse error and goes
# INERT — i.e. pi would run with no IR1 gate and no message saying so.
if command -v python3 >/dev/null 2>&1; then
  python3 -c 'import json,sys; print(json.dumps({"pluginRoot": sys.argv[1]}, indent=2))' \
    "$PLUGIN_ROOT" > "$PI_DIR/extensions/polyforge/polyforge-pi.json"
else
  case "$PLUGIN_ROOT" in
    *'"'*|*'\'*) die "checkout path contains a quote or backslash and python3 is unavailable to encode it safely: $PLUGIN_ROOT" ;;
  esac
  printf '{\n  "pluginRoot": "%s"\n}\n' "$PLUGIN_ROOT" > "$PI_DIR/extensions/polyforge/polyforge-pi.json"
fi
say "recorded pluginRoot=$PLUGIN_ROOT"

step "agent definitions -> $PI_DIR/agents/"
mkdir -p "$PI_DIR/agents"
for f in "$PLUGIN_ROOT"/pi/agents/*.md; do
  [ -e "$f" ] || continue
  place "$f" "$PI_DIR/agents/$(basename "$f")"
  say "installed agent $(basename "$f" .md)"
done

step "subagent tool"
# Agent definitions are inert without the tool that dispatches them. pi ships the
# implementation as an example (zero external dependencies, no package.json of its own).
if [ -d "$PI_DIR/extensions/subagent" ]; then
  say "subagent extension already present"
else
  SUB_SRC=""
  for root in \
    "$(npm root -g 2>/dev/null || true)" \
    "$PI_DIR/npm/node_modules" \
    "$PROJECT_DIR/node_modules"; do
    [ -n "$root" ] || continue
    cand="$root/@earendil-works/pi-coding-agent/examples/extensions/subagent"
    if [ -d "$cand" ]; then SUB_SRC="$cand"; break; fi
  done
  if [ -n "$SUB_SRC" ]; then
    mkdir -p "$PI_DIR/extensions/subagent" "$PI_DIR/prompts"
    cp -p "$SUB_SRC/index.ts" "$SUB_SRC/agents.ts" "$PI_DIR/extensions/subagent/"
    [ -d "$SUB_SRC/prompts" ] && cp -p "$SUB_SRC"/prompts/*.md "$PI_DIR/prompts/" 2>/dev/null || true
    say "installed subagent extension from $SUB_SRC"
  else
    warn "could not locate pi's examples/extensions/subagent — the pf-* agents will not be"
    warn "dispatchable until it is copied to $PI_DIR/extensions/subagent/"
  fi
fi

step "MCP config -> $PROJECT_DIR/.mcp.json"
# scriptMode:false and disableProxyTool:true in this file are SECURITY settings, not
# performance tuning: mcpScript and the mcp proxy both carry the real tool name in an
# argument, so a name-keyed gate cannot see a pf_commit underneath them.
# tests/pi-runtime.test.sh asserts both keys. They are necessary but NOT sufficient — on a
# cold metadata cache the adapter registers the proxy tool anyway, which is why the bridge
# extension also denies `mcp`/`mcpScript` outright.
#
# MERGED, not replaced. .mcp.json is the project's file and may already declare other MCP
# servers; writing the template over it would silently delete every one of them, leaving
# them only in a .bak the user has no reason to look at. Only the `polyforge` server entry
# and the settings keys this adapter depends on are imposed; everything else is preserved.
MCP_DST="$PROJECT_DIR/.mcp.json"
if [ ! -e "$MCP_DST" ]; then
  cp -p "$PLUGIN_ROOT/pi/mcp.json" "$MCP_DST"
  say "wrote .mcp.json (polyforge server, directTools, both bypass doors closed)"
elif command -v python3 >/dev/null 2>&1; then
  cp -p "$MCP_DST" "$MCP_DST.bak-$STAMP"
  python3 - "$PLUGIN_ROOT/pi/mcp.json" "$MCP_DST" <<'PY'
import json, sys
tmpl = json.load(open(sys.argv[1]))
try:
    cur = json.load(open(sys.argv[2]))
    if not isinstance(cur, dict): raise ValueError
except Exception:
    cur = {}
servers = cur.setdefault("mcpServers", {})
kept = [k for k in servers if k != "polyforge"]
servers["polyforge"] = tmpl["mcpServers"]["polyforge"]
settings = cur.setdefault("settings", {})
overridden = [k for k, v in tmpl["settings"].items() if k in settings and settings[k] != v]
settings.update(tmpl["settings"])
json.dump(cur, open(sys.argv[2], "w"), indent=2)
open(sys.argv[2], "a").write("\n")
print("  merged into existing .mcp.json; kept %d other server(s): %s"
      % (len(kept), ", ".join(kept) or "-"))
if overridden:
    print("  ⚠️  overrode conflicting settings (these are security-relevant): %s" % ", ".join(overridden))
PY
  say "backed up the previous .mcp.json -> .mcp.json.bak-$STAMP"
else
  warn "$MCP_DST exists and python3 is unavailable to merge into it — NOT overwriting."
  warn "Add this by hand, or the adapter will not register the polyforge tools:"
  sed 's/^/      /' "$PLUGIN_ROOT/pi/mcp.json" >&2
fi

step "skills -> $PROJECT_DIR/.agents/skills/"
# A recursive copy cannot go through place(), so back the whole tree up first when it
# already holds something. Without this the header's "never silently overwritten" promise
# is false for exactly the directory a user is most likely to have edited.
if [ -d "$PROJECT_DIR/.agents/skills" ] && [ -n "$(ls -A "$PROJECT_DIR/.agents/skills" 2>/dev/null)" ]; then
  cp -a "$PROJECT_DIR/.agents/skills" "$PROJECT_DIR/.agents/skills.bak-$STAMP"
  say "backed up existing skills -> .agents/skills.bak-$STAMP"
fi
mkdir -p "$PROJECT_DIR/.agents/skills"
cp -r "$PLUGIN_ROOT"/skills/* "$PROJECT_DIR/.agents/skills/"
say "installed $(find "$PLUGIN_ROOT/skills" -mindepth 1 -maxdepth 1 -type d | wc -l | tr -d ' ') skills (no modification needed)"

cat <<EOF

== done

what this touches
  $PI_DIR/extensions/polyforge/   hook bridge (pi events -> polyforge's bash hooks)
  $PI_DIR/extensions/subagent/    pi's own subagent tool
  $PI_DIR/agents/pf-*.md          polyforge agent definitions
  $PROJECT_DIR/.mcp.json          polyforge MCP server + the two security settings
  $PROJECT_DIR/.agents/skills/    the polyforge skills, verbatim

verify
  cd "$PROJECT_DIR" && pi -p "list your tools"
  Expect 45 polyforge_pf_* tools, and NEITHER \`mcp\` nor \`mcpScript\`.
  On the very first run \`mcp\` may still be listed — the adapter needs one run to populate
  ~/.pi/agent/mcp-cache.json. Calling it is refused by the bridge either way.
EOF
