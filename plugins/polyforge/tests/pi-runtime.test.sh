#!/usr/bin/env bash
# Test suite for the pi runtime adapter (aihub#503). Assert-based, no framework — mirrors
# tests/pf-commit-guard.test.sh.
#
# What this gate is FOR, in one line: the two settings that close pi's IR1 bypass doors are
# one JSON edit away from being "optimised" back on, and nothing else in the repo would
# notice. This suite is the thing that notices.
set -uo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
root="$here/.."
command -v python3 >/dev/null 2>&1 || { echo "SKIP: python3 unavailable"; exit 0; }
fails=0
ok()   { echo "  PASS: $1"; }
bad()  { echo "  FAIL: $1" >&2; fails=$((fails+1)); }
# A check that cannot run here says so out loud. It must never be reported as a PASS: the
# defects this suite was extended for (aihub#606) both shipped because something that was
# not actually verified read as verified.
skip() { echo "  SKIP: $1"; }
# Reader for the PASS|/FAIL|/SKIP|<msg> lines the python blocks emit.
# 🔴 Feed it with a here-string — `verdicts <<< "$out"` — never a pipe. A pipeline runs its
# right-hand side in a SUBSHELL, so `fails` is incremented in a process that then exits: the
# FAIL lines still print and the suite still exits 0. Measured while building this file.
verdicts() {
  while IFS='|' read -r verdict msg; do
    [ -n "${verdict:-}" ] || continue
    case "$verdict" in
      PASS) ok "$msg" ;;
      SKIP) skip "$msg" ;;
      *)    bad "$msg" ;;
    esac
  done
}

# ---------------------------------------------------------------------------
echo "== .mcp.json template closes both IR1 bypass doors =="
# mcpScript ("MCP-only plain-JavaScript tool") and the mcp proxy both take the REAL tool
# name in an argument. A gate keyed on tool name sees `mcpScript`/`mcp` and never the
# pf_commit underneath, so with either door open IR1 is decorative on pi.
mcp_out="$(python3 - "$root" <<'PY'
import json, os, sys
root = sys.argv[1]
path = os.path.join(root, "pi", "mcp.json")
if not os.path.isfile(path):
    print("FAIL|pi/mcp.json is missing"); raise SystemExit
try:
    cfg = json.load(open(path))
except ValueError as e:
    print("FAIL|pi/mcp.json is not valid JSON (%s)" % e); raise SystemExit

s = cfg.get("settings") or {}
for key in ("scriptMode", "disableProxyTool"):
    if key not in s:
        print("FAIL|settings.%s is absent — the bypass door it closes is open by default" % key)
want = {"scriptMode": False, "disableProxyTool": True}
for key, val in want.items():
    if key in s:
        print(("PASS|settings.%s == %s" % (key, json.dumps(val))) if s[key] is val
              else ("FAIL|settings.%s must be %s, found %s" % (key, json.dumps(val), json.dumps(s[key]))))

srv = (cfg.get("mcpServers") or {}).get("polyforge")
if not isinstance(srv, dict):
    print("FAIL|mcpServers.polyforge is absent")
else:
    # Without directTools the 45 tools collapse into the single `mcp` proxy and the gate
    # has no individual tool name to match on — the same failure as leaving the door open.
    print("PASS|directTools is true" if srv.get("directTools") is True
          else "FAIL|mcpServers.polyforge.directTools must be true, found %s" % json.dumps(srv.get("directTools")))
    print("PASS|command is the polyforge CLI" if srv.get("command") == "polyforge"
          else "FAIL|mcpServers.polyforge.command must be \"polyforge\", found %s" % json.dumps(srv.get("command")))
PY
)"
while IFS='|' read -r verdict msg; do
  [ -n "${verdict:-}" ] || continue
  [ "$verdict" = "PASS" ] && ok "$msg" || bad "$msg"
done <<< "$mcp_out"

# ---------------------------------------------------------------------------
echo ""
echo "== the bridge denies the bypass doors even when the config fails to hide them =="
# Necessary because the settings above are NOT sufficient. Measured on pi-mcp-adapter
# 2.32.1 (index.ts:1192): the proxy tool is registered when ANY of
#     disableProxyTool !== true || directSpecs.length === 0 || missingConfigured... > 0
# holds, so on a cold ~/.pi/agent/mcp-cache.json the `mcp` tool exists despite the key.
# Two arms, identical config, cache the only variable: cold => 55 tools including `mcp`,
# warm => 54 without it. A first-ever session therefore runs with the door open unless
# something blocks the CALL rather than the registration.
deny_out="$(python3 - "$root" <<'PY'
import json, os, re, sys
root = sys.argv[1]
path = os.path.join(root, "pi-hooks.json")
if not os.path.isfile(path):
    print("FAIL|pi-hooks.json is missing"); raise SystemExit
try:
    doc = json.load(open(path))
except ValueError as e:
    print("FAIL|pi-hooks.json is not valid JSON (%s)" % e); raise SystemExit

entries = (doc.get("hooks") or {}).get("tool_call") or []
denies = [e for e in entries if isinstance(e, dict) and isinstance(e.get("deny"), str) and e["deny"]]
if not denies:
    print("FAIL|no tool_call entry denies the bypass doors"); raise SystemExit

# Positive: both doors must be denied. Negative: the deny must not be so broad that it
# also refuses the real tools — a matcher of ".*" would satisfy every positive assertion.
for tool in ("mcp", "mcpScript"):
    hit = [e for e in denies if re.search(e.get("matcher") or "", tool)]
    print(("PASS|%s is denied outright" % tool) if hit else ("FAIL|%s is not denied by any entry" % tool))
for tool in ("polyforge_pf_commit", "polyforge_pf_get_work_item", "bash", "mcp_something", "read"):
    hit = [e for e in denies if re.search(e.get("matcher") or "", tool)]
    print(("FAIL|deny matcher is too broad — it also refuses %s" % tool) if hit
          else ("PASS|%s is left callable" % tool))
PY
)"
while IFS='|' read -r verdict msg; do
  [ -n "${verdict:-}" ] || continue
  [ "$verdict" = "PASS" ] && ok "$msg" || bad "$msg"
done <<< "$deny_out"

# ---------------------------------------------------------------------------
echo ""
echo "== every script pi-hooks.json points at exists and is runnable =="
# A typo'd path here is silent: the bridge fails open, the hook simply never fires.
script_out="$(python3 - "$root" <<'PY'
import json, os, re, sys
root = sys.argv[1]
doc = json.load(open(os.path.join(root, "pi-hooks.json")))
seen = 0
for event, entries in (doc.get("hooks") or {}).items():
    for e in entries or []:
        cmd = e.get("bash")
        if not cmd:
            continue
        seen += 1
        m = re.search(r'\$\{CLAUDE_PLUGIN_ROOT\}/([^"\s]+)', cmd)
        if not m:
            print("FAIL|%s entry does not reference ${CLAUDE_PLUGIN_ROOT}: %s" % (event, cmd)); continue
        rel = m.group(1)
        path = os.path.join(root, rel)
        if not os.path.isfile(path):
            print("FAIL|%s -> %s does not exist" % (event, rel))
        elif rel.endswith(".cjs"):
            print("PASS|%s -> %s exists" % (event, rel))
        elif not os.access(path, os.X_OK):
            print("FAIL|%s -> %s is not executable" % (event, rel))
        else:
            print("PASS|%s -> %s exists and is executable" % (event, rel))
print("PASS|%d hook scripts wired" % seen if seen >= 4 else "FAIL|only %d hook scripts wired, expected 4" % seen)
PY
)"
while IFS='|' read -r verdict msg; do
  [ -n "${verdict:-}" ] || continue
  [ "$verdict" = "PASS" ] && ok "$msg" || bad "$msg"
done <<< "$script_out"

# ---------------------------------------------------------------------------
echo ""
echo "== bridge extension contract =="
ext="$root/pi/extensions/polyforge/index.js"
if [ ! -f "$ext" ]; then
  bad "pi/extensions/polyforge/index.js is missing"
else
  # Event names are pi's API surface. pi ships breaking changes in patch releases, so a
  # renamed event must fail here rather than silently unsubscribing the IR1 gate.
  for evt in session_start input before_agent_start tool_call tool_result; do
    grep -q "pi\.on(\"$evt\"" "$ext" && ok "subscribes to $evt" || bad "does not subscribe to $evt"
  done

  # tool_call must be the gate point: tool_execution_start fires AFTER the decision, so a
  # block returned there is too late (measured, aihub#503).
  grep -q 'pi\.on("tool_execution_start"' "$ext" \
    && bad "gates on tool_execution_start, which fires after the block point" \
    || ok "does not gate on tool_execution_start"

  # pi's block protocol.
  grep -q 'block: true' "$ext" && ok "returns { block: true } to refuse a call" \
    || bad "never returns { block: true } — nothing can be refused"

  # pf-session-start picks its OUTPUT SHAPE from env: a bare PLUGIN_ROOT means "Codex",
  # which drops the top-level additionalContext and systemMessage. pi must not export it.
  grep -q 'delete env.PLUGIN_ROOT' "$ext" \
    && ok "unsets PLUGIN_ROOT so pi is not misread as Codex" \
    || bad "does not unset PLUGIN_ROOT — pf-session-start would emit the Codex shape"
  grep -q 'delete env.CURSOR_PLUGIN_ROOT' "$ext" \
    && ok "unsets CURSOR_PLUGIN_ROOT" || bad "does not unset CURSOR_PLUGIN_ROOT"
  grep -q 'CLAUDE_PROJECT_DIR' "$ext" \
    && ok "exports CLAUDE_PROJECT_DIR (pf-session-start's only workspace input)" \
    || bad "does not export CLAUDE_PROJECT_DIR — pf-session-start would find no workspace"
fi

# ---------------------------------------------------------------------------
echo ""
echo "== agent definitions are installable and well-formed =="
agent_out="$(python3 - "$root" <<'PY'
import os, re, sys
root = sys.argv[1]
d = os.path.join(root, "pi", "agents")
files = sorted(f for f in os.listdir(d)) if os.path.isdir(d) else []
if not files:
    print("FAIL|pi/agents/ holds no agent definitions"); raise SystemExit
for f in files:
    text = open(os.path.join(d, f)).read()
    m = re.match(r"^---\n(.*?)\n---\n", text, re.S)
    if not m:
        print("FAIL|%s has no frontmatter block" % f); continue
    fm = m.group(1)
    name = re.search(r"^name:\s*(\S+)\s*$", fm, re.M)
    desc = re.search(r"^description:\s*(\S.*)$", fm, re.M)
    if not name:
        print("FAIL|%s frontmatter has no name:" % f)
    elif name.group(1) != os.path.splitext(f)[0]:
        print("FAIL|%s declares name: %s — pi discovers agents by filename" % (f, name.group(1)))
    else:
        print("PASS|%s declares a matching name" % f)
    print(("PASS|%s has a dispatch description" % f) if desc and len(desc.group(1)) > 40
          else ("FAIL|%s needs a description long enough to route on" % f))
PY
)"
while IFS='|' read -r verdict msg; do
  [ -n "${verdict:-}" ] || continue
  [ "$verdict" = "PASS" ] && ok "$msg" || bad "$msg"
done <<< "$agent_out"

[ -x "$root/pi/install.sh" ] && ok "pi/install.sh is executable" || bad "pi/install.sh is not executable"

# ---------------------------------------------------------------------------
echo ""
echo "== pf-explore is restricted to read-only tools =="
# An agent file with NO `tools:` field is not read-only, it is unrestricted: pi's subagent
# extension pushes --tools only when one is declared (examples/extensions/subagent/
# index.ts:307), so pf-explore shipped holding write, edit and subagent (aihub#606). The IR1
# hook does not close that gap — its matcher names neither `edit` nor `write`.
#
# `bash` is excluded on purpose, following pi's own read-only example agent (`planner`:
# read, grep, find, ls) rather than `scout`/`reviewer`, which get a shell because they are
# allowed side effects. A shell is a write vector no name-keyed gate can police.
#
# pf-execute.md is EXEMPT by design — it is the write-capable agent, mirroring pi's own
# `worker`, which also declares nothing. Nothing here asserts anything about its tools.
#
# The three couplings below are the reason this is a cross-file check and not a lint:
#   - the allowlist is an exact-match Set (dist/core/agent-session.js:149, :2110), no globs;
#   - it filters extension-registered tools too (:2111), and the polyforge tools are
#     extension-registered, so a builtin-only allowlist silently removes pf-explore's own
#     documented read tools;
#   - the `polyforge_` spelling is a function of pi/mcp.json's toolPrefix.
pi_tools_dir=""
for cand in \
  "${POLYFORGE_PI_TOOLS:-}" \
  "$(npm root -g 2>/dev/null || true)/@earendil-works/pi-coding-agent/dist/core/tools" \
  "$HOME/.pi/agent/npm/node_modules/@earendil-works/pi-coding-agent/dist/core/tools"; do
  [ -d "$cand" ] && { pi_tools_dir="$cand"; break; }
done
explore_out="$(python3 - "$root" "$pi_tools_dir" <<'PY'
import json, os, re, sys
root, pi_tools_dir = sys.argv[1], sys.argv[2]

path = os.path.join(root, "pi", "agents", "pf-explore.md")
if not os.path.isfile(path):
    print("FAIL|pi/agents/pf-explore.md is missing"); raise SystemExit
text = open(path).read()
m = re.match(r"^---\n(.*?)\n---\n", text, re.S)
if not m:
    print("FAIL|pf-explore.md has no frontmatter block"); raise SystemExit
fm, body = m.group(1), text[m.end():]

tm = re.search(r"^tools:[ \t]*(\S.*)$", fm, re.M)
if not tm:
    print("FAIL|pf-explore.md declares no tools: — pi reads that as inherit-everything, so "
          "the read-only agent holds write, edit and subagent")
    raise SystemExit
# pi accepts both spellings: `tools: read, bash` and `tools: [read, bash]` (agents.ts).
raw = tm.group(1).strip()
if raw.startswith("[") and raw.endswith("]"):
    raw = raw[1:-1]
tools = [t.strip().strip("'\"") for t in raw.split(",") if t.strip()]
if not tools:
    print("FAIL|pf-explore.md's tools: is empty, which pi treats as no allowlist at all")
    raise SystemExit
print("PASS|pf-explore declares a tools: allowlist (%d entries)" % len(tools))

# --- 1. nothing in it can write -------------------------------------------------------
WRITE_BUILTINS = {"write", "edit", "bash", "powershell", "subagent", "apply_patch"}
granted = sorted(t for t in tools if t in WRITE_BUILTINS)
print(("FAIL|allowlist grants write-capable tool(s): %s" % ", ".join(granted)) if granted
      else "PASS|allowlist grants no write-capable built-in (no write/edit/bash/subagent)")

# Fail CLOSED on polyforge tools: anything that is not on the known read-only list counts as
# a write, so a pf_* tool added here later is caught even though this file never heard of it.
READ_PF = {"get_work_item", "get_step", "list_work_items", "list_projects", "list_users",
           "list_dependencies", "recall", "get_memory", "read_events", "get_ready_queue",
           "whoami", "diff", "predict_conflicts"}
pf_tools = [t for t in tools if "pf_" in t]
nonread = sorted(t for t in pf_tools if t.split("pf_", 1)[1] not in READ_PF)
print(("FAIL|allowlist grants polyforge tool(s) that are not on the read-only list: %s"
       % ", ".join(nonread)) if nonread
      else "PASS|every polyforge tool in the allowlist is a read-only one (%d)" % len(pf_tools))

# --- 2. it still grants what the agent's own body says it uses ------------------------
# The prompt tells the agent to read polyforge state. Because the allowlist also filters
# extension-registered tools, dropping these would not error — it would silently leave the
# agent unable to read any polyforge state at all.
reading = re.search(r"Reading\s*—(.*?)—\s*is fine", body, re.S)
if not reading:
    print("FAIL|pf-explore.md's body no longer carries the 'Reading — ... — is fine' list, "
          "so the allowlist cannot be checked against what the prompt promises")
else:
    named = re.findall(r"`(\w*pf_\w+)`", reading.group(1))
    if not named:
        print("FAIL|the 'Reading — ... — is fine' sentence names no pf_* tool")
    else:
        absent = [n for n in named if not any(t.endswith(n) for t in tools)]
        print(("FAIL|the body tells the agent to use %s, which the allowlist does not grant"
               % ", ".join(absent)) if absent
              else "PASS|every read tool the prompt names is in the allowlist (%d)" % len(named))

# --- 3. the pf_* spelling matches pi/mcp.json's toolPrefix ----------------------------
mcp_path = os.path.join(root, "pi", "mcp.json")
try:
    mcp = json.load(open(mcp_path))
except Exception as e:
    print("FAIL|pi/mcp.json is unreadable (%s)" % e); mcp = None
if mcp is not None:
    server = next(iter((mcp.get("mcpServers") or {})), None)
    mode = (mcp.get("settings") or {}).get("toolPrefix", "server")
    expected = {"server": "%s_" % server, "short": "%s_" % server,
                "none": "", "mcp": "mcp__%s_" % server}.get(mode)
    if server is None:
        print("FAIL|pi/mcp.json declares no MCP server to derive a tool prefix from")
    elif expected is None:
        print("FAIL|pi/mcp.json sets an unrecognised toolPrefix %r — cannot tell what the "
              "polyforge tools will be called" % mode)
    else:
        wrong = sorted(t for t in pf_tools if not t.startswith(expected + "pf_"))
        print(("FAIL|toolPrefix is %r so polyforge tools are named %spf_*, but the allowlist "
               "spells them: %s" % (mode, expected, ", ".join(wrong))) if wrong
              else "PASS|pf_* spelling matches pi/mcp.json toolPrefix=%r (%spf_*)" % (mode, expected))

# --- 4. every built-in named actually exists in pi ------------------------------------
# Opportunistic, like the event-name cross-check below. A misspelled built-in is silent —
# the allowlist is exact-match, so `list` instead of `ls` removes the tool rather than
# erroring, which is the same silent-capability-loss failure as the skills defect.
builtin_candidates = [t for t in tools if "pf_" not in t]
if not pi_tools_dir:
    print("SKIP|pi not installed here — built-in tool names in the allowlist not cross-checked")
else:
    known = set()
    for fn in sorted(os.listdir(pi_tools_dir)):
        if not fn.endswith(".js"):
            continue
        for name in re.findall(r'name:\s*"([a-z_]+)"', open(os.path.join(pi_tools_dir, fn)).read()):
            known.add(name)
    if not known:
        print("FAIL|found pi's tools directory at %s but could not read any tool name out of "
              "it — the cross-check would be vacuous" % pi_tools_dir)
    else:
        unknown = sorted(t for t in builtin_candidates if t not in known)
        print(("FAIL|allowlist names built-in(s) pi does not define: %s (pi defines: %s)"
               % (", ".join(unknown), ", ".join(sorted(known)))) if unknown
              else "PASS|all %d built-ins in the allowlist exist in this pi" % len(builtin_candidates))
PY
)"
verdicts <<< "$explore_out"

# ---------------------------------------------------------------------------
echo ""
echo "== subscribed event names still exist in pi's own type definitions =="
# Opportunistic: only runs where pi is installed. This is the check that catches a pi
# upgrade renaming an event out from under the bridge — the failure mode that version
# discipline exists for, and the one that is otherwise completely silent.
types=""
for cand in \
  "${POLYFORGE_PI_TYPES:-}" \
  "$(npm root -g 2>/dev/null || true)/@earendil-works/pi-coding-agent/dist/core/extensions/types.d.ts" \
  "$HOME/.pi/agent/npm/node_modules/@earendil-works/pi-coding-agent/dist/core/extensions/types.d.ts"; do
  [ -f "$cand" ] && { types="$cand"; break; }
done
if [ -z "$types" ]; then
  echo "  SKIP: pi not installed here — event names not cross-checked"
else
  for evt in session_start input before_agent_start tool_call tool_result; do
    grep -q "on(event: \"$evt\"" "$types" && ok "pi still defines the $evt event" \
      || bad "pi no longer defines a \"$evt\" event — the bridge subscribes to nothing"
  done
fi

# ---------------------------------------------------------------------------
echo ""
echo "== pi actually LOADS the skills install.sh installs, under both skill scopes =="
# THE ASSERTION IS "pi loaded it", NOT "the directory exists" — that distinction is the
# whole defect (aihub#606). install.sh used to write the skills only to
# <project>/.agents/skills, and pi reads that path only when the project is TRUSTED:
# dist/core/package-manager.js gates projectAgentsSkillDirs on isProjectTrusted(), and
# trust-manager.js lists .agents/skills as a trust-requiring project resource. Trust is
# default-off, so all 17 skills were silently unavailable while every file sat exactly where
# the installer said it put it. A file-existence check passes on the broken tree; this one
# does not.
#
# Measured on pi 0.85.1, this sandbox, trust the only variable: 0 skills loaded by default,
# 17 with --approve. After the fix the user-scope copy loads with no --approve at all.
#
# TWO ARMS since aihub#617 added POLYFORGE_PI_SKILL_SCOPE:
#   unset / both  -> user copy AND project copy written
#   user          -> user copy only; <project>/.agents/skills must be ABSENT
# The ABSENCE assertion is the load-bearing half. An install.sh that ignored the switch
# outright would still write the user copy and still load all 17 skills, so every
# presence-only assertion here passes on it — presence cannot distinguish a working opt-out
# from a no-op.
#
# Nothing here was trusted until it was shown red. Five mutants, each reddening only the
# assertions it should (aihub#617):
#   switch ignored (project copy always written)  -> the scope=user ABSENCE check, and the
#                                                    retire arm's SURVIVED check
#   default stops writing the project copy        -> the default arm's presence check
#   validation removed                            -> all three invalid-value checks
#   skip-only, an existing copy left in place     -> the retire arm (see its own section)
#   retire by deleting instead of moving aside    -> the retire arm's intact-backup check
# Every one of those runs green against a presence-only version of this suite.
#
# The install and its filesystem assertions run EVERYWHERE — install.sh only warns when pi
# is missing, so CI gets real coverage of the switch. Only the get_commands probe is
# opportunistic, and where pi is absent it SKIPs loudly rather than passing.
#
# LLM-free: `--mode rpc` + get_commands needs no API key and makes no model call, so it
# runs anywhere pi is installed.
pi_bin="$(command -v pi || true)"
skill_count="$(find "$root/skills" -mindepth 2 -maxdepth 2 -name SKILL.md | wc -l | tr -d ' ')"
[ "$skill_count" -gt 0 ] || bad "no skills/<name>/SKILL.md found — every arm below would be vacuous"

# $1 label, $2 POLYFORGE_PI_SKILL_SCOPE value ("" leaves it unset), $3 present|absent for
# the project-scope tree.
skill_scope_arm() {
  local label="$1" scope="$2" want_project="$3" sandbox rc n_user n_proj
  sandbox="$(mktemp -d 2>/dev/null || true)"
  if [ -z "$sandbox" ] || [ ! -d "$sandbox" ]; then
    bad "$label: could not create a temp dir for the install arm"
    return
  fi
  trap 'rm -rf "$sandbox"' EXIT
  mkdir -p "$sandbox/agent/npm/node_modules/pi-mcp-adapter" "$sandbox/proj"
  # Stub the adapter so install.sh takes its "already installed" branch. Without this the
  # test would run `pi install npm:pi-mcp-adapter` — a network fetch, in a test.
  printf '{"name":"pi-mcp-adapter","version":"0.0.0-test-stub"}\n' \
    > "$sandbox/agent/npm/node_modules/pi-mcp-adapter/package.json"
  # Everything the installer writes is redirected into the sandbox: PI_AGENT_DIR is its own
  # knob for the user scope, and the project dir is its argument. It must not touch the
  # developer's real ~/.pi/agent.
  if [ -z "$scope" ]; then
    env PI_AGENT_DIR="$sandbox/agent" \
      bash "$root/pi/install.sh" "$sandbox/proj" > "$sandbox/install.log" 2>&1 && rc=0 || rc=$?
  else
    env PI_AGENT_DIR="$sandbox/agent" POLYFORGE_PI_SKILL_SCOPE="$scope" \
      bash "$root/pi/install.sh" "$sandbox/proj" > "$sandbox/install.log" 2>&1 && rc=0 || rc=$?
  fi
  if [ "$rc" -eq 0 ]; then
    ok "$label: install.sh ran into a throwaway PI_AGENT_DIR"
  else
    bad "$label: install.sh exited $rc in a throwaway dir:"
    sed 's/^/      /' "$sandbox/install.log" >&2
    rm -rf "$sandbox"; trap - EXIT; return
  fi

  # --- what landed on disk. No pi needed, so this half runs in CI too. -------------------
  n_user="$(find "$sandbox/agent/skills" -mindepth 2 -maxdepth 2 -name SKILL.md 2>/dev/null \
            | wc -l | tr -d ' ')"
  if [ "$n_user" -eq "$skill_count" ]; then
    ok "$label: user-scope tree holds all $skill_count skills"
  else
    bad "$label: user-scope tree holds $n_user/$skill_count skills — the user copy is the one pi loads by default"
  fi
  if [ "$want_project" = "present" ]; then
    n_proj="$(find "$sandbox/proj/.agents/skills" -mindepth 2 -maxdepth 2 -name SKILL.md 2>/dev/null \
              | wc -l | tr -d ' ')"
    if [ "$n_proj" -eq "$skill_count" ]; then
      ok "$label: project-scope tree holds all $skill_count skills"
    else
      bad "$label: project-scope tree holds $n_proj/$skill_count skills — the DEFAULT must keep writing both copies"
    fi
  else
    if [ -e "$sandbox/proj/.agents/skills" ]; then
      bad "$label: <project>/.agents/skills exists — the opt-out was ignored, and every presence-only check here still passed"
    else
      ok "$label: <project>/.agents/skills is absent, which is what the opt-out asks for"
    fi
  fi

  # --- what pi actually LOADED. Opportunistic. -------------------------------------------
  if [ -z "$pi_bin" ]; then
    skip "$label: pi not installed here — skill loading not probed"
  elif ! command -v timeout >/dev/null 2>&1; then
    skip "$label: coreutils timeout unavailable — not running pi unbounded"
  else
    # The pipe IS pi's input channel here — do NOT add </dev/null, which would close stdin
    # before the request arrives and hang the probe out to its timeout.
    printf '{"id":1,"type":"get_commands"}\n' \
      | (cd "$sandbox/proj" && PI_CODING_AGENT_DIR="$sandbox/agent" \
           timeout 120 "$pi_bin" --mode rpc --no-session 2>/dev/null) \
      > "$sandbox/probe.out" || true
    local probe_out
    probe_out="$(python3 - "$root/skills" "$sandbox/agent/skills" "$sandbox/probe.out" "$label" <<'PY'
import json, os, sys
skills_src, user_skills_dir, probe_path, label = sys.argv[1:5]

def emit(verdict, msg):
    print("%s|%s: %s" % (verdict, label, msg))

expected = sorted(d for d in os.listdir(skills_src)
                  if os.path.isfile(os.path.join(skills_src, d, "SKILL.md")))
if not expected:
    emit("FAIL", "no skills/<name>/SKILL.md found to expect — the probe would be vacuous")
    raise SystemExit

resp = None
for line in open(probe_path, errors="replace").read().splitlines():
    line = line.strip()
    if not line:
        continue
    try:
        # strict=False: skill descriptions carry raw newlines/tabs through the RPC payload.
        obj = json.loads(line, strict=False)
    except ValueError:
        continue
    if obj.get("type") == "response" and obj.get("command") == "get_commands":
        resp = obj
if resp is None:
    emit("FAIL", "pi returned no get_commands response — the probe did not run, so nothing "
                 "about skill loading was verified")
    raise SystemExit
if not resp.get("success"):
    emit("FAIL", "pi answered get_commands with success=false"); raise SystemExit

loaded = [c for c in (resp.get("data") or {}).get("commands", []) if c.get("source") == "skill"]
prefix = os.path.join(user_skills_dir, "")
from_user = {}
for c in loaded:
    p = ((c.get("sourceInfo") or {}).get("path") or "")
    if p.startswith(prefix):
        from_user[p[len(prefix):].split(os.sep)[0]] = c.get("name")

missing = [n for n in expected if n not in from_user]
if missing:
    shown = ", ".join(missing[:5]) + (" …" if len(missing) > 5 else "")
    emit("FAIL", "pi loaded %d/%d installed skills from %s — missing: %s (it loaded %d skill(s) "
                 "in total, from: %s)"
                 % (len(from_user), len(expected), user_skills_dir, shown, len(loaded),
                    ", ".join(sorted({((c.get('sourceInfo') or {}).get('path') or '?').rsplit('/', 2)[0]
                                      for c in loaded})) or "nowhere"))
else:
    emit("PASS", "pi loaded all %d installed skills, from the copy install.sh made at %s"
                 % (len(expected), user_skills_dir))

# In the default arm the project copy is written but must not be what is doing the work:
# this sandbox was never trusted. If that ever flips, the user-scope install has stopped
# being load-bearing and the assertion above has quietly changed meaning. In the opt-out arm
# there is no project copy at all, so this is a free negative control.
project_scoped = [c for c in loaded if ((c.get("sourceInfo") or {}).get("scope")) == "project"]
emit("FAIL", "%d skill(s) loaded at project scope in an untrusted sandbox — the probe is no "
             "longer proving the user-scope copy works" % len(project_scoped)) if project_scoped \
    else emit("PASS", "nothing loaded from the untrusted project copy, so the user-scope copy "
                      "is what pi used")
PY
)"
    verdicts <<< "$probe_out"
  fi
  rm -rf "$sandbox"
  trap - EXIT
}

skill_scope_arm "default scope" ""     present
skill_scope_arm "scope=user"    "user" absent

# ---------------------------------------------------------------------------
echo ""
echo "== the opt-out RETIRES a project copy that is already there =="
# Both arms above start from an empty project dir, where "skip the write" and "retire the
# tree" are indistinguishable. The box that actually wants this switch is the opposite case:
# it already HAS the project copy and has already trusted the project, which is why it sees
# the collision lines at all. An opt-out that only declines to refresh leaves pi finding the
# stale copy, project scope still winning, and every collision line still printed — a switch
# that passes every clean-sandbox assertion and does nothing in the field. This arm is the
# one that tells those two implementations apart, so it installs TWICE into ONE sandbox:
# default first (which writes the copy), opt-out second.
#
# It also pins the non-destructive half. Retiring the tree must not delete it — install.sh
# promises everywhere else that nothing it manages is destroyed without a copy alongside it,
# and a user may have edited that tree.
retire_sandbox="$(mktemp -d 2>/dev/null || true)"
if [ -z "$retire_sandbox" ] || [ ! -d "$retire_sandbox" ]; then
  bad "could not create a temp dir for the retire arm"
else
  trap 'rm -rf "$retire_sandbox"' EXIT
  mkdir -p "$retire_sandbox/agent/npm/node_modules/pi-mcp-adapter" "$retire_sandbox/proj"
  printf '{"name":"pi-mcp-adapter","version":"0.0.0-test-stub"}\n' \
    > "$retire_sandbox/agent/npm/node_modules/pi-mcp-adapter/package.json"
  env PI_AGENT_DIR="$retire_sandbox/agent" \
    bash "$root/pi/install.sh" "$retire_sandbox/proj" > "$retire_sandbox/first.log" 2>&1 \
    && first_rc=0 || first_rc=$?
  n_first="$(find "$retire_sandbox/proj/.agents/skills" -mindepth 2 -maxdepth 2 -name SKILL.md 2>/dev/null \
             | wc -l | tr -d ' ')"
  # Precondition, asserted rather than assumed: without a project copy in place first, the
  # retire assertion below would pass on an installer that does nothing at all.
  if [ "$first_rc" -eq 0 ] && [ "$n_first" -eq "$skill_count" ]; then
    ok "retire arm: the default run left a project copy of all $skill_count skills to retire"
  else
    bad "retire arm: setup failed — default run exited $first_rc leaving $n_first/$skill_count skills, so the retire assertion would be vacuous"
  fi
  env PI_AGENT_DIR="$retire_sandbox/agent" POLYFORGE_PI_SKILL_SCOPE=user \
    bash "$root/pi/install.sh" "$retire_sandbox/proj" > "$retire_sandbox/second.log" 2>&1 \
    && second_rc=0 || second_rc=$?
  if [ "$second_rc" -eq 0 ]; then
    ok "retire arm: the opt-out re-run succeeded over an existing project copy"
  else
    bad "retire arm: the opt-out re-run exited $second_rc over an existing project copy:"
    sed 's/^/      /' "$retire_sandbox/second.log" >&2
  fi
  if [ -e "$retire_sandbox/proj/.agents/skills" ]; then
    bad "retire arm: <project>/.agents/skills SURVIVED the opt-out — pi still loads it, project scope still wins, and the collision lines the switch exists to silence are still printed"
  else
    ok "retire arm: the pre-existing <project>/.agents/skills is gone after the opt-out"
  fi
  # Non-destructive: the retired tree must still be on disk, intact, beside where it was.
  n_bak=0
  for d in "$retire_sandbox/proj/.agents/skills.bak-"*; do
    [ -d "$d" ] || continue
    n_bak="$(find "$d" -mindepth 2 -maxdepth 2 -name SKILL.md 2>/dev/null | wc -l | tr -d ' ')"
  done
  if [ "$n_bak" -eq "$skill_count" ]; then
    ok "retire arm: the retired copy is intact at .agents/skills.bak-<stamp> ($skill_count skills), not deleted"
  else
    bad "retire arm: no intact .agents/skills.bak-<stamp> after the opt-out (found $n_bak/$skill_count skills) — the pre-existing copy was not moved aside intact"
  fi
  # The backup must be a SIBLING of the search path, never a child: pi looks for the exact
  # path <ancestor>/.agents/skills, and a backup nested inside it would be discovered as
  # skills in its own right — the same trap place_skills documents for $PI_DIR/skills.
  if [ -e "$retire_sandbox/proj/.agents/skills" ]; then
    bad "retire arm: cannot check backup placement — .agents/skills still exists"
  else
    ok "retire arm: the backup is a sibling of .agents/skills, so pi finds no second skills root"
  fi
  rm -rf "$retire_sandbox"
  trap - EXIT
fi

# ---------------------------------------------------------------------------
echo ""
echo "== an unrecognised POLYFORGE_PI_SKILL_SCOPE fails loudly and writes nothing =="
# A two-valued knob has exactly one interesting failure mode: rounding an unrecognised value
# to one of the two. `POLYFORGE_PI_SKILL_SCOPE=usr` silently treated as "both" is a typo that
# reads as a working opt-out; treated as "user" it is a typo that silently drops the project
# copy. Neither is detectable from the installer's output, so the value has to be rejected.
# Needs no pi, so unlike the arms above this one really does run in CI.
bogus_sandbox="$(mktemp -d 2>/dev/null || true)"
if [ -z "$bogus_sandbox" ] || [ ! -d "$bogus_sandbox" ]; then
  bad "could not create a temp dir for the invalid-scope arm"
else
  trap 'rm -rf "$bogus_sandbox"' EXIT
  mkdir -p "$bogus_sandbox/agent/npm/node_modules/pi-mcp-adapter" "$bogus_sandbox/proj"
  printf '{"name":"pi-mcp-adapter","version":"0.0.0-test-stub"}\n' \
    > "$bogus_sandbox/agent/npm/node_modules/pi-mcp-adapter/package.json"
  env PI_AGENT_DIR="$bogus_sandbox/agent" POLYFORGE_PI_SKILL_SCOPE="sideways" \
    bash "$root/pi/install.sh" "$bogus_sandbox/proj" > "$bogus_sandbox/log" 2>&1 \
    && bogus_rc=0 || bogus_rc=$?
  if [ "$bogus_rc" -ne 0 ]; then
    ok "install.sh refuses POLYFORGE_PI_SKILL_SCOPE=sideways (exit $bogus_rc)"
  else
    bad "install.sh accepted POLYFORGE_PI_SKILL_SCOPE=sideways — an unrecognised value was rounded to one of the two"
  fi
  if grep -q "POLYFORGE_PI_SKILL_SCOPE" "$bogus_sandbox/log"; then
    ok "the refusal names the variable it is complaining about"
  else
    bad "the refusal does not name POLYFORGE_PI_SKILL_SCOPE, so a caller cannot tell what to fix"
  fi
  # Fails BEFORE writing: an installer that dies halfway leaves a half-install behind, which
  # is worse than either accepted value.
  if [ -e "$bogus_sandbox/agent/skills" ] || [ -e "$bogus_sandbox/proj/.agents" ]; then
    bad "the refused run still wrote a skills tree — validation happens after the installer starts writing"
  else
    ok "the refused run wrote no skills tree at either scope"
  fi
  rm -rf "$bogus_sandbox"
  trap - EXIT
fi

echo ""
[ "$fails" -eq 0 ] && { echo "ALL PASS"; exit 0; } || { echo "$fails FAILED" >&2; exit 1; }
