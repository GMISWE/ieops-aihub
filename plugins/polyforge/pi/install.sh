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
#   the two MCP configs are MERGED (other MCP servers in them are preserved), not replaced.
#                       There are TWO of them since aihub#689 — $PROJECT_DIR/.mcp.json and
#                       $PI_DIR/mcp.json — and both go through the same merge_mcp() below;
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
# Flipped to 0 if the global-MCP step below cannot write $PI_DIR/mcp.json. That step is
# DEGRADABLE (see its call site) so it cannot take the rest of the install down with it —
# but a degraded run re-creates exactly the defect aihub#689 exists to fix, so the flag is
# read again at the very end to shout about it and to exit non-zero. Declared HERE rather
# than at the step so `set -u` cannot turn "the step was never reached" into an unbound
# variable error in the summary.
GLOBAL_MCP_OK=1

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
# The skills are copied VERBATIM, and that is now correct BY CONSTRUCTION rather than by
# assumption (aihub#670). It was not before: pf-execute/engine.native.md stated the step
# dispatch as Claude Code's `Agent(subagent_type="polyforge:step-<role>", prompt=...)`, and this
# function shipped that line to pi unchanged while printing "no modification needed" -- a claim
# nothing checked. Under pi the tool is `subagent`, both argument names differ (`agent`/`task`),
# and the agent this installer generates two steps below is the BARE `step-<role>` rather than
# cc's `polyforge:`-namespaced form, so every coordinate of that line was wrong here.
#
# The fix is deliberately NOT a sed in this function. Two of the other three harnesses have no
# installer stage at all to put one in -- .codex-plugin/plugin.json points codex at "./skills/"
# in place, and opencode/install.sh never copies skills -- so a transform here would fix one
# harness of three, in a language no Go test can see, duplicating the role->agent-id mapping
# internal/roles already owns. Instead the skills now CARRY every harness's row
# (engine.native.md's compact rule + engine-native-details.md's §0f table, both pinned against
# internal/roles/dispatch.go by internal/cli/engine_native_dispatch_model_test.go). A verbatim
# copy is therefore the right thing to do, and the message below is true.
place_skills() {
  local dst="$1" label="$2" count="$3"
  if [ -d "$dst" ] && [ -n "$(ls -A "$dst" 2>/dev/null)" ]; then
    cp -a "$dst" "$dst.bak-$STAMP"
    say "backed up existing $label -> $label.bak-$STAMP"
  fi
  mkdir -p "$dst"
  cp -r "$PLUGIN_ROOT"/skills/* "$dst/"
  say "installed $count skills into $label (harness-neutral by construction; see aihub#670)"
}

# place() for an MCP config. Only the `polyforge` server entry and the settings keys the
# adapter depends on are IMPOSED; everything else already in the file is PRESERVED.
#
# ⚠️ DELIBERATELY NOT place(). place() is a whole-file copy, and either destination may
# already declare MCP servers this script knows nothing about — the project's own, or, at
# $PI_DIR, the user's GLOBAL ones, which belong to every other project on this box. Copying
# the template over either would silently unregister all of them, leaving them in a .bak the
# user has no reason to look at. That reasoning is the project file's, written when this was
# one destination (aihub#503); aihub#689 added the second, and a promise this script keeps
# for one file named mcp.json and not the other is a promise no reader can rely on — which
# is the same argument place_skills() exists for.
#
# ⚠️ EVERY failure-prone command in here is CHECKED EXPLICITLY and turned into a `return 1`,
# rather than being left to `set -e`. Two reasons, and the first is not optional: both call
# sites now test this function's return value, and a function invoked in a condition context
# has `set -e` DISABLED for its whole body — so an unchecked `cp` here would no longer abort,
# it would fall through to the next line and corrupt something quietly. The second is that
# `set -e` reported these as a bare one-line `cp:` 300 lines from the step that caused it
# (aihub#689 review), which is how this was found.
#   $1 destination path
#   $2 label used in this step's messages
# Returns 0 when the destination now carries the polyforge server, 1 when it does not.
merge_mcp() {
  local dst="$1" label="$2"
  if [ ! -e "$dst" ]; then
    if ! cp -p "$PLUGIN_ROOT/pi/mcp.json" "$dst"; then
      warn "could not create $dst (read-only mount? unwritable parent directory?)"
      return 1
    fi
    say "wrote $label (polyforge server, directTools, both bypass doors closed)"
    return 0
  fi
  # Exists but is not a regular file: a directory, a dangling symlink, a socket. There is
  # nothing to merge, and every branch below would fail on it one command at a time.
  if [ ! -f "$dst" ]; then
    warn "$dst exists but is NOT a regular file — refusing to merge into it. Move it aside"
    warn "and re-run, or the polyforge MCP server will not be registered from this path."
    return 1
  fi
  if command -v python3 >/dev/null 2>&1; then
    if ! cp -p "$dst" "$dst.bak-$STAMP"; then
      warn "could not back up $dst -> $(basename "$dst").bak-$STAMP — the original is"
      warn "therefore NOT being touched. Fix the permissions on that path and re-run."
      return 1
    fi
    if python3 - "$PLUGIN_ROOT/pi/mcp.json" "$dst" "$label" "$dst.bak-$STAMP" <<'PY'
import json, sys
tmpl = json.load(open(sys.argv[1]))
dst, label, bak = sys.argv[2], sys.argv[3], sys.argv[4]
# THREE states of an existing destination, reported apart (aihub#689 review). Rebuilding a
# file from the template and merging into it are very different things to have happened to a
# config that may declare servers belonging to every other project on this box, and until
# now the only trace of the former was `kept 0 other server(s): -` — which reads as "there
# was nothing there". Still FAIL-SAFE: an unreadable or malformed file is rebuilt, never
# aborted on, and its original bytes are always in the .bak this names out loud.
raw = open(dst, "rb").read()
salvage = None
if not raw.strip():
    salvage = "empty"
    cur = {}
else:
    try:
        cur = json.loads(raw)
        if not isinstance(cur, dict):
            raise ValueError("top level is a %s, not an object" % type(cur).__name__)
    except Exception as e:
        salvage = "%s: %s" % (type(e).__name__, e)
        cur = {}
servers = cur.setdefault("mcpServers", {})
kept = [k for k in servers if k != "polyforge"]
servers["polyforge"] = tmpl["mcpServers"]["polyforge"]
settings = cur.setdefault("settings", {})
overridden = [k for k, v in tmpl["settings"].items() if k in settings and settings[k] != v]
settings.update(tmpl["settings"])
json.dump(cur, open(dst, "w"), indent=2)
open(dst, "a").write("\n")
if salvage is None:
    print("  merged into existing %s; kept %d other server(s): %s"
          % (label, len(kept), ", ".join(kept) or "-"))
elif salvage == "empty":
    print("  %s existed but was EMPTY — wrote a fresh config into it, nothing to merge" % label)
else:
    print("  ⚠️  %s did NOT parse as a JSON object (%s)." % (label, salvage))
    print("  ⚠️  It was REBUILT from the template, NOT merged: any MCP server or setting it")
    print("  ⚠️  declared is no longer in effect. Nothing was destroyed — the original bytes")
    print("  ⚠️  are at %s — but you must merge back anything you still need by hand." % bak)
if overridden:
    print("  ⚠️  overrode conflicting settings (these are security-relevant): %s" % ", ".join(overridden))
PY
    then
      say "backed up the previous $label -> $(basename "$dst").bak-$STAMP"
      return 0
    fi
    warn "could not merge into $dst. Nothing was lost: its previous contents are intact at"
    warn "$dst.bak-$STAMP."
    return 1
  fi
  warn "$dst exists and python3 is unavailable to merge into it — NOT overwriting."
  warn "Add this by hand, or the adapter will not register the polyforge tools:"
  sed 's/^/      /' "$PLUGIN_ROOT/pi/mcp.json" >&2
  return 0
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
BRIDGE_CONFIG_TMP="$PI_DIR/extensions/polyforge/polyforge-pi.json.tmp-$STAMP"
if command -v python3 >/dev/null 2>&1; then
  python3 -c 'import json,sys; print(json.dumps({"pluginRoot": sys.argv[1]}, indent=2))' \
    "$PLUGIN_ROOT" > "$BRIDGE_CONFIG_TMP"
else
  case "$PLUGIN_ROOT" in
    *'"'*|*'\'*) die "checkout path contains a quote or backslash and python3 is unavailable to encode it safely: $PLUGIN_ROOT" ;;
  esac
  printf '{\n  "pluginRoot": "%s"\n}\n' "$PLUGIN_ROOT" > "$BRIDGE_CONFIG_TMP"
fi
# The root pointer is the one file whose stale value caused aihub#692. Preserve the old
# pointer just like the bridge itself, then replace it only after a complete JSON document
# has been written beside it.
place "$BRIDGE_CONFIG_TMP" "$PI_DIR/extensions/polyforge/polyforge-pi.json"
rm -f "$BRIDGE_CONFIG_TMP"
say "recorded pluginRoot=$PLUGIN_ROOT"

step "agent definitions -> $PI_DIR/agents/"
#
# ── DIVISION OF LABOUR WITH `polyforge roles install` (aihub#683) ─────────────
# THIS SCRIPT OWNS FIRST INSTALL. It creates $PI_DIR/agents/ and everything
# around it that agent files are useless without: the pi runtime check, the MCP
# adapter, the hook-bridge extension, the SUBAGENT TOOL that dispatches these
# definitions at all, the merged .mcp.json, the skills, and the retire loop
# below. None of that is `roles install`'s business, and none of it is re-run
# per session.
#
# `polyforge roles install` OWNS KEEPING THEM UP TO DATE, afterwards. It knows
# this same directory from a table in Go (internal/cli/roles_install.go's
# piTarget, which mirrors PI_DIR below plus the /agents suffix), and it
# regenerates ONLY for harnesses whose directory ALREADY EXISTS -- so it can
# never be the thing that first creates ~/.pi/, and on a machine where this
# script has never run it does nothing for pi at all. `polyforge serve` runs it
# on every MCP boot, and `/pf-update` is the manual entry point for "I just
# edited config.toml and want it now".
#
# ⚠️ IF YOU CHANGE $PI_DIR HERE, CHANGE piTarget TOO. The two write the SAME
# files to the SAME place; if they disagree, this installer puts them in one
# directory and every later boot refreshes a different one, with nothing
# anywhere reporting a problem. internal/cli's
# TestResolveHarnessTargets_HonoursEveryOverride pins the Go half against the
# PI_DIR line below.
#
# ⚠️ AND IF YOU ADD A NAME TO THE RETIRE LOOP BELOW, ADD ITS PREFIX TO
# internal/cli/roles_generate.go's retiredAgentNamePrefixes. The two warn
# messages further down recommend `polyforge roles generate pi --out ...` as the
# manual remedy, and that path never runs this loop -- so the Go side is the only
# thing standing between a manual upgrade and two dispatchable definitions per
# role. aihub#682's rename silently disarmed exactly that (aihub#683 rider 2).
# ─────────────────────────────────────────────────────────────────────────────
#
# aihub#642: step-<role>.md is no longer a static tree copied out of the
# checkout -- it's GENERATED per machine from internal/roles/definitions/*.yaml
# + this machine's ~/.polyforge/config.toml [roles.tiers] candidates, via the
# `polyforge roles generate pi` subcommand (internal/cli/roles_generate.go).
# That is also why this step cannot go through place(): place() diffs a single
# source file against a single destination, but here the SOURCE is a subcommand
# invocation, not a file on disk.
#
# Non-fatal by design, matching every other "needs a binary" step in this
# script (see the "pi runtime" step above): a machine bootstrapping pi for the
# first time may not have the polyforge binary on PATH or at
# $PLUGIN_ROOT/bin/polyforge yet, and that must not abort the rest of the
# install under `set -euo pipefail`.
POLYFORGE_BIN="$(command -v polyforge || true)"
if [ -z "$POLYFORGE_BIN" ] && [ -x "$PLUGIN_ROOT/bin/polyforge" ]; then
  POLYFORGE_BIN="$PLUGIN_ROOT/bin/polyforge"
fi
mkdir -p "$PI_DIR/agents"
# Leftovers under agent names this installer no longer generates. THE MECHANISM,
# which is the same one both times it has been needed: `roles generate pi` only
# ever WRITES files under its own current names, so it never touches a file
# named anything else. Renaming a generated agent therefore does not replace the
# old file on an upgrading machine, it ADDS a second one beside it -- and pi
# loads every .md in this directory, so it would then hold two definitions of
# the same role under two dispatchable names. Nothing warns; the stale one just
# keeps working, with whatever prompt and model it was generated with however
# many versions ago.
#
# Two generations of names have to be retired here:
#
#   pf-execute.md / pf-explore.md      pre-aihub#642. Back then this step copied
#                                      plugins/polyforge/pi/agents/*.md verbatim
#                                      and that tree shipped exactly those two
#                                      files. aihub#642 replaced the tree with
#                                      the generator and renamed the roles
#                                      ("executor"/"explorer", not
#                                      "execute"/"explore").
#   pf-<role>.md (five)                pre-aihub#682. pi was the only one of the
#                                      four harnesses whose agents were named
#                                      pf-<role>; cc, codex and opencode were
#                                      all already step-<role>. aihub#682 made
#                                      pi's match, so every file the aihub#642
#                                      generation wrote is now an old name too.
#
# Both lists stay forever: a machine that skipped a few releases upgrades
# straight from the oldest state, and a name dropped from here is a stale agent
# nothing will ever clean up. Retired the same way the .agents/skills step
# further down does -- moved aside rather than deleted outright, in case a user
# had layered local edits onto one.
for stale in pf-execute.md pf-explore.md \
             pf-executor.md pf-explorer.md pf-operator.md pf-reviewer.md pf-designer.md; do
  if [ -e "$PI_DIR/agents/$stale" ]; then
    mv "$PI_DIR/agents/$stale" "$PI_DIR/agents/$stale.bak-$STAMP"
    say "retired stale $stale (superseded agent naming) -> $stale.bak-$STAMP"
  fi
done
if [ -n "$POLYFORGE_BIN" ]; then
  if "$POLYFORGE_BIN" roles generate pi --out "$PI_DIR/agents"; then
    say "generated step-*.md agent definitions into $PI_DIR/agents/"
  else
    warn "polyforge roles generate pi failed -- $PI_DIR/agents/ may be missing or stale"
    warn "re-run manually once fixed: $POLYFORGE_BIN roles generate pi --out \"$PI_DIR/agents\""
  fi
else
  warn "polyforge binary not found on PATH or at $PLUGIN_ROOT/bin/polyforge -- skipped"
  warn "agent generation. Once it is available, run:"
  warn "    polyforge roles generate pi --out \"$PI_DIR/agents\""
fi

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
    warn "could not locate pi's examples/extensions/subagent — the step-* agents will not be"
    warn "dispatchable until it is copied to $PI_DIR/extensions/subagent/"
  fi
fi

step "global MCP config -> $PI_DIR/mcp.json"
# WHY A SECOND COPY OF THE SAME TEMPLATE (aihub#689).
#
# pi dispatches every step-role agent as a REAL CHILD PROCESS (its subagent extension
# spawn()s pi again), with cwd set to the task worktree, and it never passes --mcp-config.
# The adapter resolves the project config as resolve(cwd, ".mcp.json") with NO upward walk,
# and a polyforge task worktree has no .mcp.json of its own — so the child registered ZERO
# MCP servers, therefore zero polyforge_pf_* tools, and the mandatory first pf_get_step of
# every auto-executed work item was refused. That is a tool REGISTRATION failure, not a
# permission one: the read-only roles already name polyforge_pf_get_step in their `tools:`
# allowlist, and it never had a tool of that name to allow.
#
# $PI_DIR/mcp.json is the adapter's first-class "Pi global override" source
# (dist/config.js getPiGlobalConfigPath), and it is read at EVERY cwd — the one property
# the project copy below does not have. Source order, LATER WINS:
#   ~/.config/mcp/mcp.json -> ~/.agents/mcp.json -> ~/.agents/mcp/mcp.json
#   -> $PI_DIR/mcp.json -> <cwd>/.mcp.json -> <cwd>/.pi/mcp.json
# so this entry is ordered BEFORE both project sources and the project file still wins.
# Nothing about the existing main-session behaviour changes.
#
# Measured on pi 0.85.1 + pi-mcp-adapter 2.33.0, sandboxed PI_CODING_AGENT_DIR and HOME,
# with the hold-stdin-open rpc probe tests/pi-runtime.test.sh now carries:
#   this file only, cwd with no .mcp.json -> "MCP: 1 server enabled"   (the fix)
#   project .mcp.json only               -> "MCP: 1 server enabled"   (unchanged)
#   BOTH                                 -> "MCP: 1 server enabled", zero stderr, and NO
#                                           duplicate/shadow notice: loadMcpConfig merges
#                                           same-named entries silently, and the only
#                                           console.warn on that path is for Claude-plugin
#                                           servers, which this is not
#   neither                              -> no MCP status line at all
#
# ⚠️ DO NOT "simplify" this with PI_MCP_CONFIG_MODE=exclusive. Exclusive mode makes this
# file the ONLY source, which silently shadows the project .mcp.json below along with every
# other MCP server the user has configured anywhere.
#
# 🔴 DEGRADABLE, AND DELIBERATELY SO — this is the one step in the script whose failure is
# caught rather than fatal. It is NEW, and the step below it is OLD: every main pi session
# in this project already depends on $PROJECT_DIR/.mcp.json, and it has been the LAST thing
# this script wrote for as long as it has existed, so nothing could ever pre-empt it. Adding
# a new write above it under `set -euo pipefail` silently changed that: with $PI_DIR/mcp.json
# a stale DIRECTORY, the installer aborted here with a one-line `cp:` and the project copy
# was never written at all (reproduced in review). A new capability that can take down an
# existing one is a worse bug than the one this step fixes.
#
# So a failure here is caught, and paid for at the END of the script instead: a banner naming
# the exact consequence, and a NON-ZERO EXIT so no caller reads a degraded run as a complete
# install. That is the other half of the trade — "non-fatal" must not mean "quiet", because a
# quietly-skipped global copy re-creates precisely the defect above, and the next person to
# notice would be an auto-executed work item whose first pf_get_step is refused.
#
# The step below is NOT degradable and is left exactly as fatal as it was before this change.
# Making it non-fatal too would be a behaviour change beyond this fix, and in the wrong
# direction: an install that cannot write the project config has not installed anything the
# main session can use.
mkdir -p "$PI_DIR"
if ! merge_mcp "$PI_DIR/mcp.json" "$PI_DIR/mcp.json"; then
  GLOBAL_MCP_OK=0
  warn "continuing anyway — the project copy below is a PRE-EXISTING capability and this new"
  warn "step must not be able to take it down. See the banner at the end of this run."
fi

step "MCP config -> $PROJECT_DIR/.mcp.json"
# scriptMode:false and disableProxyTool:true in this file are SECURITY settings, not
# performance tuning: mcpScript and the mcp proxy both carry the real tool name in an
# argument, so a name-keyed gate cannot see a pf_commit underneath them.
# tests/pi-runtime.test.sh asserts both keys, in BOTH destinations. They are necessary but
# NOT sufficient — on a cold metadata cache the adapter registers the proxy tool anyway,
# which is why the bridge extension also denies `mcp`/`mcpScript` outright.
#
# MERGED, not replaced — see merge_mcp() for why, and note it applies to the global copy
# above just as much as to this one.
#
# FATAL, unlike the global step above: this is the file the main pi session in this project
# actually reads, and an install that could not write it has produced nothing usable. That is
# the behaviour this script has always had here (`set -e` on an unchecked `cp`); all that has
# changed is that the reason is now printed instead of a bare `cp:` line.
if ! merge_mcp "$PROJECT_DIR/.mcp.json" ".mcp.json"; then
  warn "this is the copy the main pi session in this project reads, so this is fatal."
  die "could not write $PROJECT_DIR/.mcp.json — see the message above"
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
  $PI_DIR/agents/step-*.md        polyforge agent definitions (generated per machine, aihub#642;
                                  renamed from pf-*.md by aihub#682, old names retired on upgrade)
  $PI_DIR/skills/                 the polyforge skills — the copy pi loads by default
  $PI_DIR/mcp.json                the SAME MCP config, read at every cwd (aihub#689). This
                                  is what gives a spawned step-role subagent — whose cwd is
                                  a task worktree with no .mcp.json of its own — the
                                  polyforge tools at all. Merged, not overwritten.

                                  ⚠️  IT IS MACHINE-WIDE, AND THAT IS THE POINT. Being read
                                  at every cwd means every pi session on this box — in every
                                  project, polyforge or not — now registers the polyforge MCP
                                  server and inherits all FOUR of this template's settings
                                  keys, not just the two security ones. scriptMode=false and
                                  disableProxyTool=true close pi's IR1 bypass doors and are
                                  the reason this file exists; toolPrefix="server" and
                                  warnOnLargeDirectTools=false are NOT security settings —
                                  they change how unrelated MCP servers are named and how
                                  loudly large direct-tool sets are reported, in sessions
                                  that have nothing to do with polyforge. Any value you had
                                  already set for those was reported above as "overrode
                                  conflicting settings", and your original file is in the
                                  .bak beside it.

                                  This is inherent to any cwd-independent fix rather than a
                                  defect in this one: the subagent's cwd is a task worktree,
                                  which no project-scoped file can reach. It is disclosed for
                                  the same reason the duplicated skills tree below is. To opt
                                  out, delete this file — at the cost of the defect coming
                                  back, i.e. spawned step-role subagents holding zero
                                  polyforge tools and failing their first pf_get_step.
  $PROJECT_DIR/.mcp.json          polyforge MCP server + the two security settings. Higher
                                  precedence than the global copy above, so this still wins.
$PROJECT_SKILLS_LINE

verify
  cd "$PROJECT_DIR" && pi -p "list your tools"   # note: pi -p reads stdin, so add </dev/null
  Expect 45 polyforge_pf_* tools, and NEITHER \`mcp\` nor \`mcpScript\`.
  On the very first run \`mcp\` may still be listed — the adapter needs one run to populate
  ~/.pi/agent/mcp-cache.json. Calling it is refused by the bridge either way.

  The global copy, from a cwd that has NO .mcp.json of its own — this is the subagent case
  (aihub#689), and it needs no API key either:
  cd /tmp && ( printf '{"id":1,"type":"get_commands"}\\n'; sleep 5 ) \\
    | pi --mode rpc --no-session | grep -o 'MCP: [0-9]* server[s]* enabled'
  Expect "MCP: 1 server enabled". Nothing at all means $PI_DIR/mcp.json is not being read.
  NOTE the \`sleep 5\`, and do not drop it: the MCP status line arrives ASYNCHRONOUSLY, after
  the request has been answered, so a probe that closes stdin immediately exits BEFORE it and
  prints nothing on a perfectly good install. Measured 1 marker in 6 runs without it.

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

# The other half of making the global-MCP step degradable (aihub#689 review). Everything
# above installed; the one thing that did not is the thing this work item exists to deliver,
# so it gets the LAST word on stderr and the exit status — "non-fatal" must not decay into
# "unnoticed". Placed after the summary so the reader still gets the full "what this touches"
# list, and phrased as the consequence rather than the syscall, because the consequence is
# what tells them whether they can ignore it (they cannot).
if [ "$GLOBAL_MCP_OK" != "1" ]; then
  printf '\n' >&2
  warn "───────────────────────────────────────────────────────────────────────────"
  warn "INCOMPLETE INSTALL: $PI_DIR/mcp.json was NOT written."
  warn ""
  warn "Everything else above did install, and the main pi session in this project"
  warn "will work. What will NOT work is any step-role subagent pi spawns: its cwd"
  warn "is the task worktree, which has no .mcp.json, and the adapter does not walk"
  warn "upwards — so it registers ZERO MCP servers, holds zero polyforge_pf_* tools,"
  warn "and its mandatory first pf_get_step is refused. That is the exact defect"
  warn "this file fixes (aihub#689)."
  warn ""
  warn "The reason is in the message further up. Usual causes: a stale directory or"
  warn "symlink at that path, a read-only mount, or a file owned by another user."
  warn "Clear it and re-run this installer; nothing here is destructive to re-run."
  warn "───────────────────────────────────────────────────────────────────────────"
  exit 1
fi
