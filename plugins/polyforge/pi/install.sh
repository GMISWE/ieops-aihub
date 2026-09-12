#!/usr/bin/env bash
# Install the polyforge plugin into the pi coding agent (@earendil-works/pi-coding-agent).
#
# Usage:  plugins/polyforge/pi/install.sh [<project-dir>]      (default: current directory)
#
# Env:    PI_AGENT_DIR               where the user-scope install goes (default ~/.pi/agent)
#         POLYFORGE_PI_SKILL_SCOPE   both (default) | user. "user" installs only the
#                                    user-scope skills copy, and retires an existing
#                                    project-scope one by moving it aside (never deleting
#                                    it). Any other value is a hard error.
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
# Skills are installed TWICE by default — see the two "skills ->" steps below. The
# user-scope copy is the one pi actually loads with no trust required; the project copy is
# kept because it is what starts working the moment a user trusts the project. This header
# used to give a second reason — that .agents/skills is a cross-tool convention this script
# is not the only writer of — and no such second reader was ever found when it was finally
# looked for. The measurement is at the second step, and it is why
# POLYFORGE_PI_SKILL_SCOPE=user now exists to turn that copy off.
#
# Everything this writes is listed under "what this touches" in the summary at the end.
# Re-running is safe: nothing is overwritten without a backup alongside it. Three cases are
# not plain file copies and are handled explicitly —
#   .mcp.json           is MERGED (other MCP servers in it are preserved), not replaced;
#   .agents/skills/     is a tree, so the whole directory is backed up before it is refreshed
#                       — and under POLYFORGE_PI_SKILL_SCOPE=user it is MOVED ASIDE to the
#                       same .bak-<stamp> name rather than refreshed or deleted;
#   $PI_DIR/skills/     likewise.

set -euo pipefail

PLUGIN_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PI_DIR="${PI_AGENT_DIR:-$HOME/.pi/agent}"
PROJECT_DIR="$(cd "${1:-$PWD}" && pwd)"
# "both" (default, today's behaviour) writes the skills to user AND project scope; "user"
# writes only the user-scope copy. Read here beside PI_AGENT_DIR, but VALIDATED below, after
# die() exists — an unrecognised value must exit with a message naming the variable, not be
# silently rounded to one of the two. Rounding it would make a typo look like a working
# opt-out (or a working default), which is the whole failure this knob is supposed to avoid.
PI_SKILL_SCOPE="${POLYFORGE_PI_SKILL_SCOPE:-both}"
# $$ as well as the timestamp: date has one-second granularity, and two runs inside the
# same second would otherwise have the second overwrite the first run's backup.
STAMP="$(date +%Y%m%d%H%M%S)-$$"

say()  { printf '  %s\n' "$*"; }
step() { printf '\n== %s\n' "$*"; }
warn() { printf '  ⚠️  %s\n' "$*" >&2; }
die()  { printf '  ❌ %s\n' "$*" >&2; exit 1; }

# Validate POLYFORGE_PI_SKILL_SCOPE before anything is written, so a typo costs nothing.
case "$PI_SKILL_SCOPE" in
  both|user) ;;
  *) die "POLYFORGE_PI_SKILL_SCOPE must be \"both\" (default) or \"user\", got: $PI_SKILL_SCOPE" ;;
esac

# Back up an existing file before replacing it, so a re-run never destroys local edits.
place() {
  local src="$1" dst="$2"
  if [ -e "$dst" ] && ! cmp -s "$src" "$dst"; then
    cp -p "$dst" "$dst.bak-$STAMP"
    say "backed up existing $(basename "$dst") -> $(basename "$dst").bak-$STAMP"
  fi
  cp -p "$src" "$dst"
}

# place() for a directory. A recursive copy cannot go through it, so back the whole tree up
# first when it already holds something — without this the header's "never silently
# overwritten" promise is false for exactly the directories a user is most likely to have
# edited. Both skill destinations go through this one function so the promise cannot hold
# for one of them and not the other.
# The count is an ARGUMENT, not a global read from here: under `set -u` a global would make
# this function usable only after the line that assigns it, which is 140 lines below.
place_skills() {
  local dst="$1" label="$2" count="$3"
  if [ -d "$dst" ] && [ -n "$(ls -A "$dst" 2>/dev/null)" ]; then
    cp -a "$dst" "$dst.bak-$STAMP"
    say "backed up existing $label -> $label.bak-$STAMP"
  fi
  mkdir -p "$dst"
  cp -r "$PLUGIN_ROOT"/skills/* "$dst/"
  say "installed $count skills into $label (no modification needed)"
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

# Count what pi will actually load — a directory holding a SKILL.md — not every directory
# under skills/. `_common/` carries shared fragments and no SKILL.md, so counting directories
# reported one skill more than any install has ever had.
SKILL_COUNT="$(find "$PLUGIN_ROOT/skills" -mindepth 2 -maxdepth 2 -name SKILL.md | wc -l | tr -d ' ')"

step "skills -> $PI_DIR/skills/  (the copy pi loads by default)"
# THIS is the destination that works out of the box. pi assembles its skill search path in
# dist/core/package-manager.js: the user directory `join(globalBaseDir, "skills")` — i.e.
# this one — is added unconditionally, while every ancestor `.agents/skills` is added only
# when `isProjectTrusted()`, and project trust is default-off (dist/core/trust-manager.js
# names `.agents/skills` a trust-requiring project resource, carving out only
# $HOME/.agents/skills as a user resource). Measured on 0.85.1, sandbox install, the same
# get_commands probe from the same project, trust the only variable: 0 skills loaded without
# --approve, 17 with it. So the project copy below is invisible under the default, which is
# how 17 skills shipped silently unavailable in aihub#503.
#
# The backup lands at $PI_DIR/skills.bak-$STAMP — a SIBLING of the search path, never inside
# it. A backup nested under $PI_DIR/skills/ would be discovered as skills in its own right
# and every skill would load twice.
place_skills "$PI_DIR/skills" "skills" "$SKILL_COUNT"

# Deliberately NOT replaced by the step above — but on ONE reason plus a bet, not the two
# reasons this comment used to give.
#
# What it used to say: ".agents/skills is a cross-tool convention (this box carries a
# .agents/.skill-lock.json written by something else entirely), so deleting it would change
# behaviour for readers this script does not know about." That sentence was written from an
# assumption and never checked. Checked since (aihub#617, 2026-09-12, on the box it was
# written on):
#   - $HOME/.agents holds .skill-lock.json and NOTHING else — no skills/ directory beside it.
#     The lock file names 38 skills from a third-party repo whose files are not in that tree.
#   - It is at HOME scope, and pi's own trust-manager (dist/core/trust-manager.js) carves
#     $HOME/.agents/skills out as a USER resource. Only <project>/.agents/skills — the path
#     THIS step writes — is the trust-requiring project resource. So the lock file is not
#     evidence about this copy's readers even if something does read it.
#   - Grepping the workspace for other readers of <project>/.agents/skills turned up only a
#     vendored third-party scratch checkout, and none of the other agent CLIs installed there
#     (claude, codex, opencode, goose) is configured to read it.
#   - <project>/.agents/ is untracked in git, so the copy is not shared with other machines.
# Removing it costs nothing measurable there either: with the project copy moved away, pi
# loaded all 17 skills from the user copy in BOTH trust states (measured on 0.85.1 with the
# get_commands probe printed under "verify" below).
#
# So the default keeps writing it on the first reason and an explicit bet, both stated:
#   1. It is what starts working the moment a user runs /trust on the project — real, and
#      unaffected by any of the above.
#   2. .agents/ is a live convention, and "no second reader HERE" is not "no second reader".
#      Another machine, another tool, or another account on a shared checkout is exactly the
#      case a default should cover, and writing a duplicate tree is cheap to undo.
# What the evidence does NOT support is making that bet unconditional, which is why
# POLYFORGE_PI_SKILL_SCOPE=user exists. What that buys is the removal of the DEFAULT's
# visible cost: one "skill collision" line per skill at every pi startup, because both copies
# carry the same names and project scope wins.
if [ "$PI_SKILL_SCOPE" = "user" ]; then
  step "skills -> $PROJECT_DIR/.agents/skills/  (RETIRED: POLYFORGE_PI_SKILL_SCOPE=user)"
  # Declining to REFRESH the tree is not enough, and getting this wrong would make the switch
  # useless exactly where it was asked for. The box that wants it is the box that ALREADY has
  # the project copy and has already trusted the project: skip the write and pi still finds
  # the old copy, project scope still wins, and every collision line the switch exists to
  # silence is still printed. It would look like a working opt-out in a clean sandbox and do
  # nothing at all in the field.
  #
  # So the copy is retired, not merely skipped — and MOVED ASIDE rather than deleted, which is
  # the same promise place_skills makes for every other tree this script manages. The backup
  # is a SIBLING of .agents/skills, never a child, so pi does not discover it as a second
  # skills root (pi looks for the exact path <ancestor>/.agents/skills).
  if [ -d "$PROJECT_DIR/.agents/skills" ]; then
    mv "$PROJECT_DIR/.agents/skills" "$PROJECT_DIR/.agents/skills.bak-$STAMP"
    say "retired the existing .agents/skills -> .agents/skills.bak-$STAMP"
    PROJECT_SKILLS_LINE="  $PROJECT_DIR/.agents/skills.bak-$STAMP  the retired project copy — nothing was deleted"
  else
    say "not written (there was no existing copy to retire)"
    PROJECT_SKILLS_LINE="  $PROJECT_DIR/.agents/skills/    NOT written (POLYFORGE_PI_SKILL_SCOPE=user)"
  fi
  say "the user-scope copy above is what pi loads, with no trust required"
  say "unset POLYFORGE_PI_SKILL_SCOPE and re-run to put it back"
else
  step "skills -> $PROJECT_DIR/.agents/skills/  (kept: used once the project is trusted)"
  place_skills "$PROJECT_DIR/.agents/skills" ".agents/skills" "$SKILL_COUNT"
  PROJECT_SKILLS_LINE="  $PROJECT_DIR/.agents/skills/    the same skills, for when the project is trusted"
fi

cat <<EOF

== done

what this touches
  $PI_DIR/extensions/polyforge/   hook bridge (pi events -> polyforge's bash hooks)
  $PI_DIR/extensions/subagent/    pi's own subagent tool
  $PI_DIR/agents/pf-*.md          polyforge agent definitions
  $PI_DIR/skills/                 the polyforge skills — the copy pi loads by default
  $PROJECT_DIR/.mcp.json          polyforge MCP server + the two security settings
$PROJECT_SKILLS_LINE

verify
  cd "$PROJECT_DIR" && pi -p "list your tools"   # note: pi -p reads stdin, so add </dev/null
  Expect 45 polyforge_pf_* tools, and NEITHER \`mcp\` nor \`mcpScript\`.
  On the very first run \`mcp\` may still be listed — the adapter needs one run to populate
  ~/.pi/agent/mcp-cache.json. Calling it is refused by the bridge either way.

  The skills, without needing an API key — this counts what pi LOADED, not what was copied:
  cd "$PROJECT_DIR" && printf '{"id":1,"type":"get_commands"}' \\
    | pi --mode rpc --no-session | grep -o '"source":"skill"' | wc -l
  Expect at least $SKILL_COUNT (this install), plus any skill from another source. A count of
  0 or 1 means discovery is not seeing this install at all.
  NOTE the missing \`</dev/null\` here, deliberately: --mode rpc takes its REQUEST on stdin,
  so redirecting it closes the channel and the count comes back 0 on a perfectly good
  install. That is the opposite of the \`pi -p\` line above, which has no pipe and does need
  the redirect. Measured both ways under bash on a correct install: 0 with it, $SKILL_COUNT
  without. (Under zsh MULTIOS merges the two and it "works", which is how the wrong advice
  reads as fine on a dev box.)
EOF
