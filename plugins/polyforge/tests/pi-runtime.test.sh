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

# Shared predicate: "this file is a polyforge MCP config with both IR1 bypass doors closed".
# Emits PASS|/FAIL| lines for verdicts().
#
# ONE implementation, used TWICE since aihub#689 — for the template in the checkout, and for
# the copy install.sh writes to $PI_DIR — because there are now two files that have to carry
# the same four properties, and a second hand-written copy of this predicate is a copy that
# can be relaxed on one destination without reddening anything on the other.
#   $1 path to the JSON file
#   $2 display name for the missing / unparseable messages
#   $3 prefix prepended to EVERY verdict message. It is EMPTY for the template arm, which
#      keeps those PASS lines byte-identical to the strings .github/workflows/ci.yml greps
#      for -- a prefixed line does not contain the unprefixed one as a substring, so the two
#      arms cannot satisfy each other's named checks.
mcp_config_verdicts() {
  python3 - "$1" "$2" "$3" <<'PY'
import json, os, sys
path, display, prefix = sys.argv[1:4]

# The prefix is spliced in at each CALL SITE rather than inside emit(). That is not
# style: internal/citest/dbtestcov matches a ci.yml `for want in …` entry by its NAME
# part against the literals in this file, treating `%s` as a gap that absorbs anything
# (shellsuite.go wildcardRE / templateMatches, minWantOverlap=4). A bare literal like
# `emit("PASS", "directTools is true")` carries no gap, so NO prefixed want can ever
# match it and every assertion naming one reads as a renamed/deleted check. Keep the
# leading %s on every message here.
def emit(verdict, msg):
    print("%s|%s" % (verdict, msg))

if not os.path.isfile(path):
    emit("FAIL", "%s%s is missing" % (prefix, display)); raise SystemExit
try:
    cfg = json.load(open(path))
except ValueError as e:
    emit("FAIL", "%s%s is not valid JSON (%s)" % (prefix, display, e)); raise SystemExit

s = cfg.get("settings") or {}
for key in ("scriptMode", "disableProxyTool"):
    if key not in s:
        emit("FAIL", "%ssettings.%s is absent — the bypass door it closes is open by default" % (prefix, key))
want = {"scriptMode": False, "disableProxyTool": True}
for key, val in want.items():
    if key in s:
        emit("PASS", "%ssettings.%s == %s" % (prefix, key, json.dumps(val))) if s[key] is val \
            else emit("FAIL", "%ssettings.%s must be %s, found %s"
                              % (prefix, key, json.dumps(val), json.dumps(s[key])))

srv = (cfg.get("mcpServers") or {}).get("polyforge")
if not isinstance(srv, dict):
    emit("FAIL", "%smcpServers.polyforge is absent" % prefix)
else:
    # Without directTools the 45 tools collapse into the single `mcp` proxy and the gate
    # has no individual tool name to match on — the same failure as leaving the door open.
    emit("PASS", "%sdirectTools is true" % prefix) if srv.get("directTools") is True \
        else emit("FAIL", "%smcpServers.polyforge.directTools must be true, found %s"
                          % (prefix, json.dumps(srv.get("directTools"))))
    emit("PASS", "%scommand is the polyforge CLI" % prefix) if srv.get("command") == "polyforge" \
        else emit("FAIL", "%smcpServers.polyforge.command must be \"polyforge\", found %s"
                          % (prefix, json.dumps(srv.get("command"))))
PY
}

# ---------------------------------------------------------------------------
echo "== .mcp.json template closes both IR1 bypass doors =="
# mcpScript ("MCP-only plain-JavaScript tool") and the mcp proxy both take the REAL tool
# name in an argument. A gate keyed on tool name sees `mcpScript`/`mcp` and never the
# pf_commit underneath, so with either door open IR1 is decorative on pi.
verdicts <<< "$(mcp_config_verdicts "$root/pi/mcp.json" "pi/mcp.json" "")"

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
# aihub#642: the per-role agent files are no longer a static tree committed to
# this repo -- they are GENERATED per machine by `polyforge roles generate pi`
# (internal/cli/roles_generate.go) from internal/roles/definitions/*.yaml plus
# this machine's ~/.polyforge/config.toml [roles.tiers] candidates. So this
# check builds the CLI (or reuses one already on PATH / at $root/bin/polyforge)
# and generates into a throwaway directory under a throwaway HOME, exercising
# the real generator instead of reading a tree nothing writes to any more.
# $generated_agents is read by the "read-only roles" section below too.
repo_root="$(cd "$root/../.." && pwd)"
gen_root=""
generated_agents=""
polyforge_bin=""
# Deliberately NOT `command -v polyforge` / $root/bin/polyforge first: this
# check exists to verify the generator code IN THIS WORKTREE, and both of
# those may resolve to an unrelated, independently-updated system install
# (this box's own /usr/local/bin/polyforge auto-updates daily, per
# bin/polyforge-mcp.sh) that predates whatever change is under test here --
# measured hitting exactly that path (`unknown command: roles`) against a
# same-repo, differently-versioned binary while writing this check. Building
# from source is the only way this assertion is actually about this checkout.
# Falling back to a PATH/bin binary only when go itself is unavailable is a
# last resort, matching the "opportunistic, degrades to SKIP" pattern used
# throughout this suite -- not a preference over building fresh.
if command -v go >/dev/null 2>&1; then
  gen_root="$(mktemp -d 2>/dev/null || true)"
  if [ -n "$gen_root" ] && [ -d "$gen_root" ]; then
    polyforge_bin="$gen_root/polyforge-test-bin"
    if ! (cd "$repo_root" && GOWORK=off go build -o "$polyforge_bin" ./cmd/polyforge) \
        > "$gen_root/build.log" 2>&1; then
      bad "could not build the polyforge CLI to generate pi agent files:"
      sed 's/^/      /' "$gen_root/build.log" >&2
      polyforge_bin=""
    fi
  else
    bad "could not create a temp dir to build the polyforge CLI into"
  fi
else
  polyforge_bin="$(command -v polyforge || true)"
  if [ -z "$polyforge_bin" ] && [ -x "$root/bin/polyforge" ]; then
    polyforge_bin="$root/bin/polyforge"
  fi
  if [ -z "$polyforge_bin" ]; then
    skip "go is not on PATH and no polyforge binary was found -- cannot generate pi agent files"
  fi
fi
if [ -n "$polyforge_bin" ]; then
  [ -n "$gen_root" ] || gen_root="$(mktemp -d 2>/dev/null || true)"
  if [ -n "$gen_root" ] && [ -d "$gen_root" ]; then
    mkdir -p "$gen_root/agents" "$gen_root/home"
    if HOME="$gen_root/home" "$polyforge_bin" roles generate pi --out "$gen_root/agents" \
        > "$gen_root/gen.log" 2>&1; then
      generated_agents="$gen_root/agents"
    else
      bad "polyforge roles generate pi failed:"
      sed 's/^/      /' "$gen_root/gen.log" >&2
    fi
  else
    bad "could not create a temp dir to generate pi agent files into"
  fi
fi

if [ -n "$generated_agents" ]; then
  missing_roles=""
  for role in executor operator explorer reviewer designer; do
    [ -f "$generated_agents/step-$role.md" ] || missing_roles="$missing_roles step-$role.md"
  done
  if [ -z "$missing_roles" ]; then
    ok "generator wrote all 5 role agent files (executor/operator/explorer/reviewer/designer)"
  else
    bad "generator did not write:$missing_roles"
  fi

  agent_out="$(python3 - "$generated_agents" <<'PY'
import os, re, sys
d = sys.argv[1]
files = sorted(f for f in os.listdir(d) if f.endswith(".md"))
if not files:
    print("FAIL|generated agents directory holds no .md files"); raise SystemExit
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
else
  skip "no polyforge binary could be built or found -- generated pi agent files not checked"
fi

[ -x "$root/pi/install.sh" ] && ok "pi/install.sh is executable" || bad "pi/install.sh is not executable"

repo_root="$(cd "$root/../.." && pwd)"
package_out="$(python3 - "$repo_root/package.json" <<'PY'
import json, sys
try:
    doc = json.load(open(sys.argv[1]))
except Exception as e:
    print("FAIL|root package.json is unreadable: %s" % e); raise SystemExit
pi = doc.get("pi") or {}
exts = pi.get("extensions") or []
want = "./plugins/polyforge/pi/extensions/polyforge/index.js"
print(("PASS|direct Pi Git install discovers the bootstrap sentinel" if exts == [want]
       else "FAIL|root package.json must expose only %s, got %r" % (want, exts)))
print(("PASS|Pi package has no lifecycle scripts, preserving explicit review before bootstrap"
       if not doc.get("scripts")
       else "FAIL|Pi package must not run lifecycle scripts automatically: %r" % doc.get("scripts")))
PY
)"
verdicts <<< "$package_out"

# ---------------------------------------------------------------------------
echo ""
echo "== read-only roles carry a pi tools: allowlist; write-capable roles do not =="
# aihub#642 generalizes the old single-file "pf-explore is restricted to
# read-only tools" check across all 5 generated roles. explorer and reviewer
# are the only read_only roles (internal/roles/definitions/{explorer,
# reviewer}.yaml; internal/roles/compile.go's CompileCapability compiles that
# one bool to the SAME piReadOnlyTools constant for both), so EXACTLY those
# two must carry a tools: allowlist; executor/operator/designer must carry
# NONE at all. An agent file with NO `tools:` field is not read-only, it is
# unrestricted: pi's subagent extension pushes --tools only when one is
# declared (examples/extensions/subagent/index.ts:307), so a stray allowlist
# on a write-capable role, or a MISSING one on a read-only role, both reopen
# aihub#606 (pf-explore shipped holding write, edit and subagent because its
# allowlist was silently absent).
#
# The old body-quotes-its-own-tools check ("the 'Reading - ... - is fine'
# sentence names ...") is gone: that prose lived only in the old hand-authored
# pi-specific pf-explore.md. The generated files render one prompt shared
# across CC/pi/codex (internal/roles/definitions/*.yaml's `prompt:`), which
# does not (and should not) name pi-specific tool spellings — the allowlist's
# correctness is checked directly below instead (fail-closed READ_PF set),
# not by cross-referencing prose that no longer exists.
#
# The couplings below are the reason this is a cross-file check and not a lint:
#   - the allowlist is an exact-match Set (dist/core/agent-session.js:149, :2110), no globs;
#   - it filters extension-registered tools too (:2111), and the polyforge tools are
#     extension-registered, so a builtin-only allowlist silently removes a role's own
#     documented read tools;
#   - the `polyforge_` spelling is a function of pi/mcp.json's toolPrefix.
if [ -z "${generated_agents:-}" ]; then
  skip "no generated pi agent files available -- read-only/write-capable tools: split not checked"
else
  pi_tools_dir=""
  for cand in \
    "${POLYFORGE_PI_TOOLS:-}" \
    "$(npm root -g 2>/dev/null || true)/@earendil-works/pi-coding-agent/dist/core/tools" \
    "$HOME/.pi/agent/npm/node_modules/@earendil-works/pi-coding-agent/dist/core/tools"; do
    [ -d "$cand" ] && { pi_tools_dir="$cand"; break; }
  done

  for role in executor operator explorer reviewer designer; do
    case "$role" in
      explorer|reviewer) want_tools=yes ;;
      *)                 want_tools=no ;;
    esac
    role_out="$(python3 - "$generated_agents/step-$role.md" "$role" "$want_tools" "$pi_tools_dir" "$root/pi/mcp.json" <<'PY'
import json, os, re, sys
path, role, want_tools, pi_tools_dir, mcp_path = sys.argv[1:6]

def emit(v, m):
    print("%s|step-%s.md: %s" % (v, role, m))

if not os.path.isfile(path):
    emit("FAIL", "file is missing"); raise SystemExit
text = open(path).read()
m = re.match(r"^---\n(.*?)\n---\n", text, re.S)
if not m:
    emit("FAIL", "has no frontmatter block"); raise SystemExit
fm = m.group(1)

# pi accepts both spellings: `tools: read, bash` and `tools: [read, bash]` (agents.ts).
tm = re.search(r"^tools:[ \t]*(\S.*)$", fm, re.M)

if want_tools == "no":
    if tm:
        emit("FAIL", "declares a tools: allowlist, but this is a write-capable role -- it "
                     "must inherit everything (no tools: line), matching pi's own `worker` "
                     "example and pf-execute.md's existing shape")
    else:
        emit("PASS", "declares no tools: line, so pi grants it every tool (write-capable role)")
    raise SystemExit

# want_tools == "yes": explorer or reviewer.
if not tm:
    emit("FAIL", "declares no tools: -- pi reads that as inherit-everything, so this "
                 "read-only role would hold write, edit and subagent")
    raise SystemExit
raw = tm.group(1).strip()
if raw.startswith("[") and raw.endswith("]"):
    raw = raw[1:-1]
tools = [t.strip().strip("'\"") for t in raw.split(",") if t.strip()]
if not tools:
    emit("FAIL", "tools: is empty, which pi treats as no allowlist at all"); raise SystemExit
emit("PASS", "declares a tools: allowlist (%d entries)" % len(tools))

# --- 1. nothing in it can write -------------------------------------------------------
WRITE_BUILTINS = {"write", "edit", "bash", "powershell", "subagent", "apply_patch"}
granted = sorted(t for t in tools if t in WRITE_BUILTINS)
emit("FAIL", "allowlist grants write-capable tool(s): %s" % ", ".join(granted)) if granted \
    else emit("PASS", "allowlist grants no write-capable built-in (no write/edit/bash/subagent)")

# Fail CLOSED on polyforge tools: anything that is not on the known read-only list counts as
# a write, so a pf_* tool added here later is caught even though this file never heard of it.
READ_PF = {"get_work_item", "get_step", "list_work_items", "list_projects", "list_users",
           "list_dependencies", "recall", "get_memory", "read_events", "get_ready_queue",
           "whoami", "diff", "predict_conflicts"}
pf_tools = [t for t in tools if "pf_" in t]
nonread = sorted(t for t in pf_tools if t.split("pf_", 1)[1] not in READ_PF)
emit("FAIL", "allowlist grants polyforge tool(s) that are not on the read-only list: %s"
             % ", ".join(nonread)) if nonread \
    else emit("PASS", "every polyforge tool in the allowlist is a read-only one (%d)" % len(pf_tools))

# --- 2. the pf_* spelling matches pi/mcp.json's toolPrefix ----------------------------
try:
    mcp = json.load(open(mcp_path))
except Exception as e:
    emit("FAIL", "pi/mcp.json is unreadable (%s)" % e); mcp = None
if mcp is not None:
    server = next(iter((mcp.get("mcpServers") or {})), None)
    mode = (mcp.get("settings") or {}).get("toolPrefix", "server")
    expected = {"server": "%s_" % server, "short": "%s_" % server,
                "none": "", "mcp": "mcp__%s_" % server}.get(mode)
    if server is None:
        emit("FAIL", "pi/mcp.json declares no MCP server to derive a tool prefix from")
    elif expected is None:
        emit("FAIL", "pi/mcp.json sets an unrecognised toolPrefix %r -- cannot tell what the "
                      "polyforge tools will be called" % mode)
    else:
        wrong = sorted(t for t in pf_tools if not t.startswith(expected + "pf_"))
        emit("FAIL", "toolPrefix is %r so polyforge tools are named %spf_*, but the allowlist "
                      "spells them: %s" % (mode, expected, ", ".join(wrong))) if wrong \
            else emit("PASS", "pf_* spelling matches pi/mcp.json toolPrefix=%r (%spf_*)" % (mode, expected))

# --- 3. every built-in named actually exists in pi ------------------------------------
# Opportunistic, like the event-name cross-check below. A misspelled built-in is silent —
# the allowlist is exact-match, so `list` instead of `ls` removes the tool rather than
# erroring, which is the same silent-capability-loss failure as the skills defect.
builtin_candidates = [t for t in tools if "pf_" not in t]
if not pi_tools_dir:
    emit("SKIP", "pi not installed here -- built-in tool names in the allowlist not cross-checked")
else:
    known = set()
    for fn in sorted(os.listdir(pi_tools_dir)):
        if not fn.endswith(".js"):
            continue
        for name in re.findall(r'name:\s*"([a-z_]+)"', open(os.path.join(pi_tools_dir, fn)).read()):
            known.add(name)
    if not known:
        emit("FAIL", "found pi's tools directory at %s but could not read any tool name out "
                      "of it -- the cross-check would be vacuous" % pi_tools_dir)
    else:
        unknown = sorted(t for t in builtin_candidates if t not in known)
        emit("FAIL", "allowlist names built-in(s) pi does not define: %s (pi defines: %s)"
                      % (", ".join(unknown), ", ".join(sorted(known)))) if unknown \
            else emit("PASS", "all %d built-ins in the allowlist exist in this pi" % len(builtin_candidates))
PY
)"
    verdicts <<< "$role_out"
  done
fi

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
  mkdir -p "$sandbox/agent/npm/node_modules/pi-mcp-adapter" "$sandbox/proj" \
    "$sandbox/agent/extensions/polyforge"
  # Seed a stale root record. The installer must repair it without destroying the only
  # evidence of where this machine was previously bound.
  printf '{"pluginRoot":"/stale/polyforge/1.1.53"}\n' \
    > "$sandbox/agent/extensions/polyforge/polyforge-pi.json"
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

  if grep -q '"pluginRoot": ".*plugins/polyforge"' \
      "$sandbox/agent/extensions/polyforge/polyforge-pi.json" 2>/dev/null; then
    ok "$label: installer repaired the persisted bridge root"
  else
    bad "$label: installer did not record its current plugin root"
  fi
  bridge_backup="$(find "$sandbox/agent/extensions/polyforge" -maxdepth 1 \
      -name 'polyforge-pi.json.bak-*' -print -quit)"
  if [ -n "$bridge_backup" ] && grep -q '/stale/polyforge/1.1.53' "$bridge_backup"; then
    ok "$label: stale bridge root was preserved in a backup"
  else
    bad "$label: stale bridge root was replaced without a readable backup"
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
echo "== the installer retires agent files under names it no longer generates =="
# aihub#682 renamed pi's generated agents pf-<role>.md -> step-<role>.md. The
# mechanism that makes a rename dangerous here is that `polyforge roles generate
# pi` only ever WRITES files under its own CURRENT names -- it never deletes and
# never renames -- while pi loads EVERY .md in ~/.pi/agent/agents/. So on an
# upgrading machine the rename does not replace the five old files, it adds five
# new ones beside them: two dispatchable definitions per role, the stale one
# carrying whatever prompt, model and tool policy it was generated with however
# many releases ago, and nothing printing a word about it.
#
# This is the SECOND time that exact shape has occurred (aihub#642 was the
# first, renaming pf-execute.md/pf-explore.md), which is why install.sh carries
# an explicit retire list covering both generations, and why it is asserted here
# rather than assumed.
#
# Two arms, and the NEGATIVE one is the load-bearing half: an installer that
# backed up indiscriminately -- a `mv` over a glob, a place() applied to the
# whole directory -- satisfies the positive arm perfectly while moving aside
# every agent file it finds on every run, including the ones it just generated.
#
# 🔴 THE NEGATIVE ARM MUST SEED A CURRENT NAME, and this was measured, not
# reasoned. The first version of it ran the installer over an EMPTY agents dir
# and asserted no .bak appeared. The indiscriminate mutant above passes that:
# install.sh mkdir -p's the directory immediately before the retire loop, so on
# a first install there is nothing for a glob to match, and `for p in dir/*.md`
# with nullglob off yields the unexpanded pattern, which `[ -e ]` rejects. The
# control was green against a genuinely broken installer -- it was measuring
# "an empty directory has no files in it". Seeding a file under a name the
# installer is SUPPOSED to leave alone is what makes the two implementations
# distinguishable.
#
# Neither arm needs pi. Neither needs the polyforge binary for the RETIREMENT
# itself (that loop runs before the generate call), so both arms' assertions run
# on any machine; only the "and the replacement actually arrived" check needs
# one, and it SKIPs when the generator did not run earlier in this suite.
agent_retire_sandbox="$(mktemp -d 2>/dev/null || true)"
if [ -z "$agent_retire_sandbox" ] || [ ! -d "$agent_retire_sandbox" ]; then
  bad "could not create a temp dir for the agent-retire arm"
else
  trap 'rm -rf "$agent_retire_sandbox"' EXIT
  # install.sh finds the generator with `command -v polyforge`, so put THIS
  # worktree's build first on PATH rather than letting it reach whatever is
  # installed on the box (which auto-updates daily and may predate this change
  # -- the same trap the "agent definitions" section above documents).
  gen_path_dir=""
  if [ -n "${polyforge_bin:-}" ] && [ -x "${polyforge_bin:-}" ]; then
    gen_path_dir="$agent_retire_sandbox/bin"
    mkdir -p "$gen_path_dir"
    cp -p "$polyforge_bin" "$gen_path_dir/polyforge"
  fi

  # Seeds one name from EACH retired generation, so dropping either list from
  # install.sh reddens this arm.
  retire_seed_names="pf-execute.md pf-explore.md pf-executor.md pf-explorer.md pf-operator.md pf-reviewer.md pf-designer.md"
  # ...and the names the installer must NOT touch: the ones it generates itself.
  keep_seed_names="step-executor.md step-reviewer.md"
  # 🔴 The negative control's ANTI-VACUITY PROBE, and it must be a step-*.md name
  # the generator NEVER writes. Measured: seeding the marker into step-executor.md
  # instead proves nothing, because `roles generate pi` rewrites that file
  # unconditionally -- the marker is gone afterwards even on a correct installer
  # (verified: grep -c of the marker in a freshly generated step-executor.md is 0).
  # An existence-only check on a generated name is therefore satisfied by the
  # GENERATOR'S OWN OUTPUT, not by the seed surviving, and a mutant that deletes
  # every .md in the directory before generating passes it. A name outside the
  # role catalog is the only seed whose bytes a correct run leaves alone.
  # (`roles generate` prints an orphan WARNING about it and exits 0 -- expected,
  # and asserted on below by the run's exit status rather than its stderr.)
  keep_probe_name="step-local-note.md"

  agent_retire_run() {   # $1 = arm dir under the sandbox; $2.. = file names to seed
    local armdir="$agent_retire_sandbox/$1"
    shift
    mkdir -p "$armdir/agent/agents" "$armdir/agent/npm/node_modules/pi-mcp-adapter" "$armdir/proj"
    printf '{"name":"pi-mcp-adapter","version":"0.0.0-test-stub"}\n' \
      > "$armdir/agent/npm/node_modules/pi-mcp-adapter/package.json"
    local n
    for n in "$@"; do
      printf -- '---\nname: %s\n---\nLOCALLY EDITED BY THE USER\n' "${n%.md}" \
        > "$armdir/agent/agents/$n"
    done
    if [ -n "$gen_path_dir" ]; then
      env PI_AGENT_DIR="$armdir/agent" PATH="$gen_path_dir:$PATH" \
        bash "$root/pi/install.sh" "$armdir/proj" > "$armdir/install.log" 2>&1
    else
      env PI_AGENT_DIR="$armdir/agent" \
        bash "$root/pi/install.sh" "$armdir/proj" > "$armdir/install.log" 2>&1
    fi
  }

  # --- positive arm: a machine upgrading from either older naming ------------
  if agent_retire_run upgrade $retire_seed_names; then
    ok "agent retire arm: install.sh succeeded over an agents dir holding retired names"
  else
    bad "agent retire arm: install.sh failed over an agents dir holding retired names:"
    sed 's/^/      /' "$agent_retire_sandbox/upgrade/install.log" >&2
  fi
  up_agents="$agent_retire_sandbox/upgrade/agent/agents"
  n_left="$(find "$up_agents" -maxdepth 1 -name 'pf-*.md' 2>/dev/null | wc -l | tr -d ' ')"
  if [ "$n_left" -eq 0 ]; then
    ok "agent retire arm: no pf-*.md is left dispatchable beside the new step-*.md files"
  else
    bad "agent retire arm: $n_left pf-*.md survived — pi would hold two definitions per role, and the stale one still dispatches"
  fi
  n_seeded="$(printf '%s\n' $retire_seed_names | wc -l | tr -d ' ')"
  n_bakd=0
  for n in $retire_seed_names; do
    for b in "$up_agents/$n.bak-"*; do
      [ -f "$b" ] && { n_bakd=$((n_bakd + 1)); break; }
    done
  done
  if [ "$n_bakd" -eq "$n_seeded" ]; then
    ok "agent retire arm: all $n_seeded retired names have a .bak-<stamp> beside them"
  else
    bad "agent retire arm: only $n_bakd/$n_seeded retired names were moved aside — the rest were deleted outright, or a generation was dropped from install.sh's retire list"
  fi
  # Non-destructive: the retired file must still hold the user's own edits. A
  # retire step that re-generated over the backup would pass every count above.
  n_intact=0
  for b in "$up_agents/pf-executor.md.bak-"*; do
    [ -f "$b" ] && grep -q 'LOCALLY EDITED BY THE USER' "$b" && n_intact=1
  done
  if [ "$n_intact" -eq 1 ]; then
    ok "agent retire arm: the retired file's own content survived intact in the backup"
  else
    bad "agent retire arm: the .bak of pf-executor.md does not hold the content that was there — it was overwritten, not moved aside"
  fi
  if [ -n "${generated_agents:-}" ] && [ -n "$gen_path_dir" ]; then
    if [ -f "$up_agents/step-executor.md" ]; then
      ok "agent retire arm: the replacement step-executor.md was generated in the same run"
    else
      bad "agent retire arm: no step-executor.md after the retire — the roles are now retired AND absent"
    fi
  else
    skip "no polyforge binary available to this suite — 'the replacement arrived' half not checked"
  fi

  # --- negative control: a dir holding ONLY current names loses nothing -----
  if agent_retire_run clean $keep_seed_names "$keep_probe_name"; then
    ok "agent retire arm: negative control run succeeded over an already-current agents dir"
  else
    bad "agent retire arm: negative control run failed over an already-current agents dir:"
    sed 's/^/      /' "$agent_retire_sandbox/clean/install.log" >&2
  fi
  clean_agents="$agent_retire_sandbox/clean/agent/agents"
  # Anti-vacuity FIRST, and it asserts BYTES not existence: if the probe is gone
  # (or was rewritten), a zero .bak count below would mean "there was nothing
  # left to back up", which is a false green rather than a pass. Existence alone
  # is not enough here -- see keep_probe_name's comment for the measurement.
  if [ -f "$clean_agents/$keep_probe_name" ] \
     && grep -q 'LOCALLY EDITED BY THE USER' "$clean_agents/$keep_probe_name"; then
    ok "agent retire arm: negative control left $keep_probe_name byte-for-byte alone, so the .bak count below is not vacuous"
  else
    bad "agent retire arm: negative control lost or rewrote $keep_probe_name — the installer touched a step-*.md it does not own, so a zero .bak count below would mean nothing"
  fi
  n_stray="$(find "$clean_agents" -maxdepth 1 -name '*.bak-*' 2>/dev/null | wc -l | tr -d ' ')"
  if [ "$n_stray" -eq 0 ]; then
    ok "agent retire arm: negative control produced NO .bak file at all, so only the named old files are retired"
  else
    bad "agent retire arm: negative control produced $n_stray .bak file(s) over current names; the retire step is firing on names it was never given, so the positive arm above proves nothing"
  fi
  rm -rf "$agent_retire_sandbox"
  trap - EXIT
fi

# ---------------------------------------------------------------------------
echo ""
echo "== the MCP config pi reads is cwd-INDEPENDENT (aihub#689) =="
# THE DEFECT THIS GATES. pi dispatches every step-role agent as a REAL CHILD PROCESS (its
# subagent extension spawn()s pi again) with cwd set to the task worktree, and it never
# passes --mcp-config. The adapter resolves the project config as resolve(cwd, ".mcp.json")
# with NO upward walk, and a polyforge task worktree has no .mcp.json of its own — so the
# child registered ZERO MCP servers, held zero polyforge_pf_* tools, and the mandatory first
# pf_get_step of every auto-executed work item was refused. A tool REGISTRATION failure, not
# a permission one: the read-only roles already name polyforge_pf_get_step in the `tools:`
# allowlist the section above checks, and there was simply no tool of that name to allow.
# install.sh therefore ALSO writes the template to $PI_DIR/mcp.json, the adapter's
# Pi-global source, which is read at every cwd.
#
# THREE ARMS, and the THIRD is the load-bearing one:
#   file       — install.sh wrote $PI_DIR/mcp.json and it still closes both bypass doors.
#                Runs everywhere, needs no pi. Shares its predicate with the template
#                section at the top of this file, so the two destinations cannot drift.
#   behaviour  — pi, started from a cwd that has NO .mcp.json, registers the server anyway.
#   negative   — the SAME probe with $PI_DIR/mcp.json deleted registers NOTHING.
# Without the negative control the behaviour arm passes on a pi that picked the config up
# from somewhere else entirely — a real risk rather than a theoretical one, because FOUR of
# the six standard sources are cwd-independent (~/.config/mcp/mcp.json, ~/.agents/mcp.json,
# ~/.agents/mcp/mcp.json, $PI_DIR/mcp.json) — and the whole section would collapse back into
# the file arm. That is how aihub#606/#617/#682 each shipped a broken installer past a green
# suite. HOME is sandboxed alongside PI_CODING_AGENT_DIR for the same reason: the other three
# cwd-independent sources are HOME-relative, so a developer who happens to have one would
# otherwise redden the negative control for a reason that has nothing to do with this change.
#
# 🔴 THE PROBE MUST HOLD STDIN OPEN, and that was measured, not reasoned. The MCP status line
# arrives ASYNCHRONOUSLY, after the rpc request has already been answered, and `printf ... |
# pi` closes the pipe and exits BEFORE it lands. Measured on pi 0.85.1 + pi-mcp-adapter
# 2.33.0, config held constant and correct: the close-immediately probe produced the marker
# in 1 of 6 runs — a coin toss that reads as a clean failure. The loop below holds the write
# end open until the marker appears or the deadline passes, so the positive arm settles in
# ~1s (measured 3/3) and only the negative arm pays the full 20s wait.
#
# LLM-free, like the skills probe above: --mode rpc + get_commands needs no API key and makes
# no model call. It does not need the `polyforge` binary either — the marker reports what was
# REGISTERED from config, and was measured identical with polyforge hidden from PATH.
#
# $1 cwd to start pi in, $2 output file, $3 sandbox agent dir, $4 sandbox HOME.
pi_mcp_probe() {
  local cwd="$1" out="$2" agent="$3" fakehome="$4"
  rm -f "$out" "$out.err"
  : > "$out"
  ( printf '{"id":1,"type":"get_commands"}\n'
    # 🔴 Break on the SETTLED status, not on any "MCP: " line. The adapter emits an
    # intermediate "MCP: connecting to N servers..." first, and a loop that stops there
    # closes stdin while the real answer is still in flight -- measured reddening the
    # positive arm on a cold mcp-cache.json while the identical probe passed by luck on a
    # warm one. "servers? enabled" is the terminal line in both the connected and the
    # failed-to-connect case, and the assertions below read the count out of it.
    for _ in $(seq 1 80); do
      grep -qE 'servers? enabled' "$out" 2>/dev/null && break
      sleep 0.25
    done ) \
  | ( cd "$cwd" && HOME="$fakehome" PI_CODING_AGENT_DIR="$agent" \
        timeout 120 "$pi_bin" --mode rpc --no-session 2>"$out.err" ) > "$out" || true
}

# 🔴 THE STUB ADAPTER THE OTHER SECTIONS USE IS NOT ENOUGH HERE, and getting this wrong is a
# FALSE GREEN, not a false red. Those sections stub
# $PI_DIR/npm/node_modules/pi-mcp-adapter/package.json only to keep install.sh off the
# network ("already installed" branch). But this section's claim is that the ADAPTER reads
# the config, and a stub registers no extension at all: measured, a sandbox built that way
# reports no MCP server in BOTH arms, so the positive arm reddens for an unrelated reason
# and the negative control passes for the wrong one. A real adapter is therefore linked in,
# and where none exists both probe arms SKIP rather than assert anything.
#
# SYMLINKED, not copied: the tree is 88 MB, and nothing here writes to it. pi discovers npm
# packages from settings.json ("packages"), which the real install.sh flow gets from
# `pi install npm:pi-mcp-adapter` — the step this sandbox is deliberately skipping — so the
# fixture writes that entry itself.
adapter_modules=""
for cand in \
  "$HOME/.pi/agent/npm/node_modules" \
  "$(npm root -g 2>/dev/null || true)"; do
  [ -n "$cand" ] && [ -d "$cand/pi-mcp-adapter" ] && { adapter_modules="$cand"; break; }
done

mcp_cwd_sandbox="$(mktemp -d 2>/dev/null || true)"
if [ -z "$mcp_cwd_sandbox" ] || [ ! -d "$mcp_cwd_sandbox" ]; then
  bad "could not create a temp dir for the cwd-independent MCP config arm"
else
  trap 'rm -rf "$mcp_cwd_sandbox"' EXIT
  mkdir -p "$mcp_cwd_sandbox/agent/npm" \
           "$mcp_cwd_sandbox/proj" "$mcp_cwd_sandbox/elsewhere" "$mcp_cwd_sandbox/home"
  if [ -n "$adapter_modules" ]; then
    ln -s "$adapter_modules" "$mcp_cwd_sandbox/agent/npm/node_modules"
    printf '{"packages":["npm:pi-mcp-adapter"]}\n' > "$mcp_cwd_sandbox/agent/settings.json"
  else
    mkdir -p "$mcp_cwd_sandbox/agent/npm/node_modules/pi-mcp-adapter"
    printf '{"name":"pi-mcp-adapter","version":"0.0.0-test-stub"}\n' \
      > "$mcp_cwd_sandbox/agent/npm/node_modules/pi-mcp-adapter/package.json"
  fi
  if env PI_AGENT_DIR="$mcp_cwd_sandbox/agent" \
       bash "$root/pi/install.sh" "$mcp_cwd_sandbox/proj" \
       > "$mcp_cwd_sandbox/install.log" 2>&1; then
    ok "cwd-independent MCP config: install.sh ran into a throwaway PI_AGENT_DIR"
  else
    bad "cwd-independent MCP config: install.sh failed in a throwaway dir:"
    sed 's/^/      /' "$mcp_cwd_sandbox/install.log" >&2
  fi

  # --- file arm. No pi needed, so this half runs in CI too. ------------------------------
  verdicts <<< "$(mcp_config_verdicts "$mcp_cwd_sandbox/agent/mcp.json" \
                    "\$PI_DIR/mcp.json" "global MCP config: ")"

  # 🔴 ANTI-VACUITY for both probe arms, asserted rather than assumed. The probe cwd must not
  # be the project dir and must hold no MCP config of its own, or "pi found a server" says
  # nothing about the GLOBAL copy. A later edit that pointed the probe at $sandbox/proj would
  # leave every assertion below green and meaningless.
  if [ -e "$mcp_cwd_sandbox/elsewhere/.mcp.json" ] || [ -e "$mcp_cwd_sandbox/elsewhere/.pi" ]; then
    bad "cwd-independent MCP config: the probe cwd holds its own MCP config — the behaviour arm would prove nothing"
  else
    ok "cwd-independent MCP config: the probe cwd has no .mcp.json and no .pi/ of its own"
  fi
  # ...and the installer must genuinely have written the project copy elsewhere, or "the
  # global one is what did the work" is not a distinction this sandbox can draw.
  if [ -f "$mcp_cwd_sandbox/proj/.mcp.json" ]; then
    ok "cwd-independent MCP config: the project copy still lands in the project dir, not the probe cwd"
  else
    bad "cwd-independent MCP config: no $mcp_cwd_sandbox/proj/.mcp.json — the installer stopped writing the project copy"
  fi

  # --- behaviour arm + negative control. Opportunistic. ---------------------------------
  if [ -z "$pi_bin" ]; then
    skip "cwd-independent MCP config: pi not installed here — MCP registration not probed"
  elif [ -z "$adapter_modules" ]; then
    skip "cwd-independent MCP config: no real pi-mcp-adapter to link in — MCP registration not probed"
  elif ! command -v timeout >/dev/null 2>&1; then
    skip "cwd-independent MCP config: coreutils timeout unavailable — not running pi unbounded"
  else
    pi_mcp_probe "$mcp_cwd_sandbox/elsewhere" "$mcp_cwd_sandbox/pos.out" \
                 "$mcp_cwd_sandbox/agent" "$mcp_cwd_sandbox/home"
    # The probe must have RUN. Without this, "no marker" in the negative control below is
    # satisfied by a pi that crashed on startup, and the control proves nothing either.
    if grep -q '"command":"get_commands","success":true' "$mcp_cwd_sandbox/pos.out"; then
      ok "cwd-independent MCP config: pi answered the rpc probe from a cwd with no .mcp.json"
    else
      bad "cwd-independent MCP config: pi returned no successful get_commands response — the probe did not run, so neither arm below means anything"
      sed 's/^/      /' "$mcp_cwd_sandbox/pos.out.err" >&2
    fi
    if grep -q 'MCP: 1 server enabled' "$mcp_cwd_sandbox/pos.out"; then
      ok "cwd-independent MCP config: pi registered the polyforge server from a cwd with no .mcp.json"
    else
      bad "cwd-independent MCP config: pi registered NO MCP server from a cwd with no .mcp.json — a spawned step-role subagent would hold zero polyforge_pf_* tools and its first pf_get_step would be refused"
    fi

    # 🔴 THE NEGATIVE CONTROL. Same sandbox, same probe, one variable: the file.
    rm -f "$mcp_cwd_sandbox/agent/mcp.json"
    pi_mcp_probe "$mcp_cwd_sandbox/elsewhere" "$mcp_cwd_sandbox/neg.out" \
                 "$mcp_cwd_sandbox/agent" "$mcp_cwd_sandbox/home"
    if grep -q '"command":"get_commands","success":true' "$mcp_cwd_sandbox/neg.out"; then
      ok "cwd-independent MCP config: negative control — pi still ran with the global config deleted"
    else
      bad "cwd-independent MCP config: negative control — pi returned no successful get_commands response, so its silence about MCP is not evidence"
    fi
    if grep -q 'MCP: ' "$mcp_cwd_sandbox/neg.out"; then
      bad "cwd-independent MCP config: negative control — pi STILL registered an MCP server with \$PI_DIR/mcp.json deleted, so the positive arm above is being satisfied by some other config source and proves nothing about what install.sh wrote"
    else
      ok "cwd-independent MCP config: negative control — deleting \$PI_DIR/mcp.json leaves pi with no MCP server at all, so the positive arm is about that file"
    fi
  fi
  rm -rf "$mcp_cwd_sandbox"
  trap - EXIT
fi

# ---------------------------------------------------------------------------
echo ""
echo "== a failed GLOBAL config write does not take the PROJECT one down (aihub#689) =="
# THE REGRESSION THIS GATES, which is NOT the defect the section above gates. install.sh runs
# under `set -euo pipefail`, and aihub#689 added the $PI_DIR/mcp.json write ABOVE the
# $PROJECT_DIR/.mcp.json write that every main pi session in the project already depended on
# — a step which, until then, had been the last one and so could never pre-empt anything. An
# unchecked `cp` in the NEW step therefore aborted the whole installer before the OLD step
# ran: with a stale DIRECTORY at $PI_DIR/mcp.json the run ended on a bare one-line `cp:` and
# the project copy was never written at all. A new capability that can delete an existing one
# is a worse bug than the one it was added to fix.
#
# So the new step is DEGRADABLE, and both halves of that are asserted here:
#   it does not abort  — the project copy is still written, and still a real config;
#   it is not quiet    — the path is named, the end-of-run banner names the consequence, and
#                        the script exits NON-ZERO.
# The second half is not decoration. A quietly-skipped global copy re-creates precisely the
# defect aihub#689 exists to fix, and the next person to find out would be an auto-executed
# work item whose mandatory first pf_get_step is refused — so "non-fatal" without "loud" is
# not a fix, it is the same bug with the evidence removed.
#
# A DIRECTORY is the sabotage because it is the failure that was actually reproduced in
# review, and because it needs no unusual privileges: the other two triggers named there (a
# read-only mount, a file owned by someone else) make this arm either unrunnable as a normal
# user or vacuous as root. Needs no pi, so unlike the probe arms above this one runs in CI.
#
# 🔴 AND IT HAS A NEGATIVE CONTROL, for the reason aihub#606/#617/#682 each shipped a broken
# installer past a green suite: "it printed a warning and exited 1" proves nothing unless the
# SAME installer, in the SAME sandbox, with the sabotage as the ONLY variable, exits 0 and
# prints no such thing. Without the control, a merge_mcp that failed unconditionally — or a
# banner printed on every run — satisfies every assertion above it.
degraded_sandbox="$(mktemp -d 2>/dev/null || true)"
if [ -z "$degraded_sandbox" ] || [ ! -d "$degraded_sandbox" ]; then
  bad "could not create a temp dir for the degraded-global-write arm"
else
  trap 'rm -rf "$degraded_sandbox"' EXIT
  # $1 = arm dir under the sandbox; $2 = "sabotage" to put a DIRECTORY where the global
  # config has to go. Everything else is identical between the two arms BY CONSTRUCTION —
  # one function, one call site shape — which is what makes the control a control.
  degraded_run() {
    local armdir="$degraded_sandbox/$1"
    mkdir -p "$armdir/agent/npm/node_modules/pi-mcp-adapter" "$armdir/proj"
    printf '{"name":"pi-mcp-adapter","version":"0.0.0-test-stub"}\n' \
      > "$armdir/agent/npm/node_modules/pi-mcp-adapter/package.json"
    if [ "${2:-}" = "sabotage" ]; then mkdir -p "$armdir/agent/mcp.json"; fi
    env PI_AGENT_DIR="$armdir/agent" \
      bash "$root/pi/install.sh" "$armdir/proj" > "$armdir/install.log" 2>&1
  }

  # --- positive arm: the global destination cannot be written ---------------------------
  degraded_run sabotaged sabotage && deg_rc=0 || deg_rc=$?
  deg_log="$degraded_sandbox/sabotaged/install.log"
  if [ "$deg_rc" -ne 0 ]; then
    ok "degraded global write: install.sh exited non-zero, so a degraded run is not read as a complete install (exit $deg_rc)"
  else
    bad "degraded global write: install.sh exited 0 with \$PI_DIR/mcp.json unwritten — a caller cannot tell this run apart from a working one, and the aihub#689 defect is back with nothing saying so"
  fi

  # THE REGRESSION ITSELF.
  if [ -f "$degraded_sandbox/sabotaged/proj/.mcp.json" ]; then
    ok "degraded global write: the project .mcp.json was still written"
  else
    bad "degraded global write: the project .mcp.json was NEVER written — the new global step aborted the installer before the pre-existing step it was added above"
  fi
  # ...and it has to be a real config, not merely a file at that path. A presence-only
  # assertion here is satisfied by a zero-byte touch, which is the exact shape of false green
  # aihub#606/#617/#682 shared. Same predicate as both sections above, so the three
  # destinations cannot drift apart.
  verdicts <<< "$(mcp_config_verdicts "$degraded_sandbox/sabotaged/proj/.mcp.json" \
                    "the project .mcp.json after a degraded global write" \
                    "degraded global write: project copy: ")"

  # LOUD, in two separate places, because either one alone is easy to lose in 40 lines of
  # install output. First: the path that could not be written, which is what to go and fix.
  if grep -q "$degraded_sandbox/sabotaged/agent/mcp.json" "$deg_log"; then
    ok "degraded global write: the run names the global path it could not write"
  else
    bad "degraded global write: nothing in the output names \$PI_DIR/mcp.json, so the user cannot tell which path to fix"
  fi
  # Second: the CONSEQUENCE, which is what tells a reader whether the warning can be ignored.
  # It cannot — that consequence is the entire content of aihub#689.
  if grep -q 'INCOMPLETE INSTALL' "$deg_log" && grep -q 'pf_get_step' "$deg_log"; then
    ok "degraded global write: the end-of-run banner names both the failure and what it breaks"
  else
    bad "degraded global write: no end-of-run banner naming the subagent consequence — a warning that only names a syscall is one a user skips, and this one means every auto-executed work item fails its first pf_get_step"
  fi
  # A refusal has to BE a refusal: nothing may have been written into the path it declined.
  if [ -d "$degraded_sandbox/sabotaged/agent/mcp.json" ] \
     && [ -z "$(ls -A "$degraded_sandbox/sabotaged/agent/mcp.json" 2>/dev/null)" ]; then
    ok "degraded global write: the unmergeable path was left untouched, not half-written"
  else
    bad "degraded global write: the installer wrote into the path it said it was refusing to touch"
  fi

  # --- 🔴 negative control: same sandbox, same function, sabotage the only variable -------
  degraded_run control && ctl_rc=0 || ctl_rc=$?
  ctl_log="$degraded_sandbox/control/install.log"
  if [ "$ctl_rc" -eq 0 ]; then
    ok "degraded global write: negative control — the same install with nothing sabotaged exits 0"
  else
    bad "degraded global write: negative control — the UNSABOTAGED install also failed (exit $ctl_rc), so the non-zero exit above is not evidence about the sabotage:"
    sed 's/^/      /' "$ctl_log" >&2
  fi
  if grep -q 'INCOMPLETE INSTALL' "$ctl_log"; then
    bad "degraded global write: negative control — the UNSABOTAGED install also printed the banner, so the banner is unconditional and the assertion above proves nothing"
  else
    ok "degraded global write: negative control — the unsabotaged install prints no such banner"
  fi
  if [ -f "$degraded_sandbox/control/agent/mcp.json" ]; then
    ok "degraded global write: negative control — and it DID write the global mcp.json, so the sabotage is what suppressed it"
  else
    bad "degraded global write: negative control — the global mcp.json is absent with nothing sabotaged, so this arm is measuring something other than the sabotage"
  fi
  rm -rf "$degraded_sandbox"
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

# ---------------------------------------------------------------------------
echo ""
echo "== the model-facing tool surface a GLM session sends is the advertised one (aihub#694) =="
# aihub#694 was filed as "GLM 5.3 rejects the Pi polyforge MCP tool surface". Re-measured
# with a capture server, the surface turned out to be ACCEPTED byte-for-byte: the full
# 50-tool request (~140KB, 45 of them polyforge_pf_*), the bash,read-restricted request,
# and even the cold-cache request that still carries the mcp proxy tool all replay to the
# real sub2api-glm gateway with HTTP 200, and a live GLM 5.3 session completed a full
# polyforge_pf_* tool-call round trip from a task worktree. The 400s that motivated the wi
# were a TRANSIENT GATEWAY WINDOW, and the recorded evidence is decisive: in the same pi
# session (2026-09-16T07:30, sessions/--root-code-aicoding-gmi-ws--) the IDENTICAL request
# "你是谁" failed with the wi's exact 400 at 09:25:33 and SUCCEEDED at 09:25:53 and 09:26:38,
# while the same error signature hit gpt-6-astra, gpt-5.6-sol and grok-4.6 through the same
# gateway in the same hours, and aihub#694's own executor drew 503s from it minutes later.
# The one DETERMINISTIC GLM 400 the sweep found is reasoning_effort:"off" (GLM 5.3 is
# reasoning-only) — which pi never sends: with --thinking off pi OMITS the parameter
# entirely (captured, and the live run against GLM answered), and every other level passes.
#
# So the configuration pi/mcp.json ships — directTools:true, disableProxyTool:true,
# scriptMode:false — IS the minimal compatible surface: all 45 lifecycle tools, no proxy,
# no script tool, and schemas containing no anyOf/oneOf/not/$ref/patternProperties and no
# additionalProperties:true anywhere (the jsonb-object params stay open by OMITTING the
# keyword, which is both semantically required — they accept arbitrary JSON objects — and
# the only shape permissive and strict backends agree on). aihub#694 therefore changes
# nothing in the template; what was missing is a gate holding that shape in place. This
# section is that gate, in two halves:
#
#   serve-advertised arm (runs on CI, needs only go+python3): drive `polyforge serve`
#       over stdio with a throwaway HOME and assert the advertised surface itself — count,
#       naming, and strict-backend schema hygiene. serve boots with a DUMMY api key and an
#       unreachable server URL (tool registration is static; the health check only warns —
#       measured), so this arm needs no aihub, no credentials, no network.
#
#   pi-capture arms (opportunistic, like the MCP probe arms above: need pi, a real
#       pi-mcp-adapter to symlink, node, and the built CLI — SKIP where absent): compose the
#       ACTUAL request pi sends, by pointing a fake openai-completions provider at a local
#       capture server, from two cwds — the project dir (main session) and a bare dir with
#       no .mcp.json of its own (the spawned step-role child, aihub#689's case) — and assert
#       the wire surface equals the advertised one, with neither bypass door visible.
#       LLM-free like every probe in this suite: the "model" is a local node server that
#       records the request and answers "OK", so no API key, no tokens, no network beyond
#       loopback.
#
# The pi arms are not redundant with the "cwd-independent MCP config" section above: that
# one pins "1 server enabled" — REGISTRATION — while this one pins the TOOLS the model is
# actually offered. aihub#689's defect was a registration failure invisible to a tools-list
# assertion, but the reverse gap is just as real: a config change that registers the server
# yet drops, proxies or rewrites the direct tools would keep every line of that section
# green.
glm_sandbox="$(mktemp -d 2>/dev/null || true)"
glm_node_pid=""
if [ -z "$glm_sandbox" ] || [ ! -d "$glm_sandbox" ]; then
  bad "GLM-compat surface: could not create a temp dir for the surface arms"
else
  trap 'rm -rf "$glm_sandbox"; if [ -n "$glm_node_pid" ]; then kill "$glm_node_pid" 2>/dev/null || true; fi; trap - EXIT' EXIT
  mkdir -p "$glm_sandbox/home/.polyforge" "$glm_sandbox/proj" "$glm_sandbox/elsewhere"
  # serve refuses to boot without an api key (measured: "API key not set", exit) — any
  # value works for tool LISTING, which is static; the unreachable [server] url costs one
  # WARNING on stderr and nothing else (measured). CI has no aihub and no credentials, so
  # the dummy key is what makes this arm CI-runnable at all.
  cat > "$glm_sandbox/home/.polyforge/config.toml" <<'TOML'
machine_id = "pi-runtime-surface-arm"

[auth]
api_key = "dummy-key-listing-only"

[server]
url = "http://127.0.0.1:1"
TOML

  # --- serve-advertised arm: the surface itself. Needs the CLI this suite builds earlier
  # and nothing else (no pi, no adapter, no node) — so this half runs on CI.
  if [ -z "${polyforge_bin:-}" ] || [ ! -x "${polyforge_bin:-}" ]; then
    skip "GLM-compat surface: no polyforge CLI built earlier in this suite — advertised surface not probed"
  else
    verdicts <<< "$(python3 - "$polyforge_bin" "$glm_sandbox" <<'PY'
import json, os, subprocess, sys, time

bin_path, sandbox = sys.argv[1], sys.argv[2]
env = dict(os.environ)
env["HOME"] = os.path.join(sandbox, "home")

proc = subprocess.Popen([bin_path, "serve"], stdin=subprocess.PIPE,
                        stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, env=env,
                        text=True, bufsize=1)

def send(obj):
    proc.stdin.write(json.dumps(obj) + "\n")
    proc.stdin.flush()

def fail(msg):
    print("FAIL|GLM-compat surface: " + msg)

try:
    send({"jsonrpc": "2.0", "id": 1, "method": "initialize",
          "params": {"protocolVersion": "2024-11-05", "capabilities": {},
                     "clientInfo": {"name": "pi-runtime-surface-arm", "version": "0"}}})
    send({"jsonrpc": "2.0", "method": "notifications/initialized"})
    send({"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}})
    deadline = time.time() + 45
    tools = None
    while time.time() < deadline:
        line = proc.stdout.readline()
        if not line:
            break
        line = line.strip()
        if not line:
            continue
        try:
            obj = json.loads(line)
        except ValueError:
            continue
        if obj.get("id") == 2 and "result" in obj:
            tools = obj["result"].get("tools") or []
            break
    if tools is None:
        fail("serve never answered tools/list over stdio — the advertised-surface arm "
             "did not run, so nothing below is verified")
        raise SystemExit

    names = [t.get("name") or "" for t in tools]
    advertised = {(t.get("name") or ""): (t.get("inputSchema") or {}) for t in tools}
    json.dump({"names": names, "schemas": advertised},
              open(os.path.join(sandbox, "advertised.json"), "w"))

    if len(tools) >= 45:
        print("PASS|GLM-compat surface: serve advertised %d pf_* tools (floor 45)" % len(tools))
    else:
        fail("serve advertised only %d tools (floor 45) — lifecycle tools went missing "
             "before any adapter is involved" % len(tools))
    bad_names = [n for n in names if not n.startswith("pf_")]
    if bad_names:
        fail("tools not named pf_*: %s — the pi config prefixes them with the server name, "
             "so an unprefixed name here lands as an unprefixed pi tool" % ", ".join(bad_names[:5]))
    else:
        print("PASS|GLM-compat surface: every advertised tool name starts with pf_")

    # Strict-backend schema hygiene, the aihub#694 acceptance surface. anyOf/oneOf/not and
    # $ref/patternProperties are the constructs strict OpenAI-compatible tool-schema
    # validators reject, and additionalProperties:true is the shape strict mode refuses on
    # every object; the jsonb-object params must instead stay open by OMITTING the keyword
    # (setting it false would reject the arbitrary objects they exist to carry, aihub#486).
    FORBIDDEN = ("anyOf", "oneOf", "not", "$ref", "patternProperties")

    def walk(o, hits):
        if isinstance(o, dict):
            for k, v in o.items():
                if k in FORBIDDEN:
                    hits.add(k)
                if k == "additionalProperties" and v is True:
                    hits.add("additionalProperties:true")
                walk(v, hits)
        elif isinstance(o, list):
            for v in o:
                walk(v, hits)

    union_alias = set()
    open_objects = set()
    for n, sch in advertised.items():
        hits = set()
        walk(sch, hits)
        for h in hits:
            if h == "additionalProperties:true":
                open_objects.add(n)
            else:
                union_alias.add("%s (%s)" % (n, h))
    if union_alias:
        fail("advertised schemas use union/aliasing keywords a strict OpenAI-compat tool "
             "validator rejects: %s" % ", ".join(sorted(union_alias)[:5]))
    else:
        print("PASS|GLM-compat surface: every advertised tool schema avoids anyOf, oneOf, "
              "not, ref and patternProperties")
    if open_objects:
        fail("advertised schemas set additionalProperties to true: %s" % ", ".join(sorted(open_objects)[:5]))
    else:
        print("PASS|GLM-compat surface: no advertised tool schema sets additionalProperties "
              "to true (jsonb-object params stay open by omission)")
finally:
    try:
        proc.kill()
    except Exception:
        pass
PY
)"
  fi

  # --- pi-capture arms: the wire surface a model backend actually receives. -------------
  if [ -z "$pi_bin" ]; then
    skip "GLM-compat surface: pi not installed here — wire surface not probed"
  elif [ -z "${polyforge_bin:-}" ] || [ ! -x "${polyforge_bin:-}" ]; then
    skip "GLM-compat surface: no polyforge CLI built earlier in this suite — wire surface not probed"
  elif [ -z "$adapter_modules" ]; then
    skip "GLM-compat surface: no real pi-mcp-adapter to symlink — wire surface not probed"
  elif ! command -v node >/dev/null 2>&1; then
    skip "GLM-compat surface: node unavailable — no capture server, wire surface not probed"
  elif ! command -v timeout >/dev/null 2>&1; then
    skip "GLM-compat surface: coreutils timeout unavailable — not running pi unbounded"
  elif [ ! -f "$glm_sandbox/advertised.json" ]; then
    skip "GLM-compat surface: the serve-advertised arm did not produce the advertised list — wire surface not probed"
  else
    mkdir -p "$glm_sandbox/agent/npm" "$glm_sandbox/bin"
    ln -s "$adapter_modules" "$glm_sandbox/agent/npm/node_modules"
    printf '{"packages":["npm:pi-mcp-adapter"]}\n' > "$glm_sandbox/agent/settings.json"
    # The config's command is a bare "polyforge" (resolved from PATH), so put THIS
    # checkout's build where the spawned serve will find it — the same discipline the
    # agent-retire arm uses for the generator.
    cp -p "$polyforge_bin" "$glm_sandbox/bin/polyforge"
    if env PI_AGENT_DIR="$glm_sandbox/agent" PATH="$glm_sandbox/bin:$PATH" \
         bash "$root/pi/install.sh" "$glm_sandbox/proj" \
         > "$glm_sandbox/install.log" 2>&1; then
      ok "GLM-compat surface: install.sh ran into a throwaway PI_AGENT_DIR"
    else
      bad "GLM-compat surface: install.sh failed in a throwaway dir:"
      sed 's/^/      /' "$glm_sandbox/install.log" >&2
    fi

    # A local openai-completions "backend" that records every request body and answers OK.
    # This is the whole trick that makes the wire surface assertable WITHOUT an API key or
    # a real model: pi composes the exact request it would send (same provider api, same
    # shaping) and hands it to something that simply writes it down. ONE SERVER PER ARM,
    # not one for the section: the file name comes from the SERVER's CAP_NAME env, so a
    # shared server would have all three arms write one counter-named file while the
    # assertions read per-arm names — measured as exactly that false red on the first run
    # of this section.
    cat > "$glm_sandbox/capture.cjs" <<'CAP'
"use strict";
const http = require("node:http");
const fs = require("node:fs");
const srv = http.createServer((req, res) => {
  let body = "";
  req.on("data", (c) => (body += c));
  req.on("end", () => {
    fs.writeFileSync(process.env.CAP_OUT + "/" + process.env.CAP_NAME + ".json", body);
    res.writeHead(200, { "content-type": "application/json" });
    res.end(JSON.stringify({
      id: "chatcmpl-surface-arm", object: "chat.completion", created: 0, model: "glm-5.3",
      choices: [{ index: 0, message: { role: "assistant", content: "OK" }, finish_reason: "stop" }],
      usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 },
    }));
  });
});
srv.listen(0, "127.0.0.1", () => console.log("PORT " + srv.address().port));
CAP
    # The fake provider mirrors the real sub2api-glm entry (same api, same compat flag,
    # same model shape) so pi composes the identical request it would send to GLM 5.3 —
    # only the baseUrl differs, and it is rewritten per arm below. pi refuses to run a
    # provider with no auth entry (measured), so a dummy key stands in; nothing local ever
    # validates it.
    cat > "$glm_sandbox/agent/models.json" <<'MODELS'
{"providers":{"capglm":{"baseUrl":"http://127.0.0.1:0/v1","api":"openai-completions","compat":{"supportsFinishReason":false},"models":[{"id":"glm-5.3","name":"Glm 5.3","reasoning":true,"contextWindow":1000000,"input":["text"]}]}}}
MODELS
    printf '{"capglm":{"type":"api_key","key":"dummy"}}\n' > "$glm_sandbox/agent/auth.json"

    # $1 cwd, $2 capture name. -nc/-na keep the arms clear of this machine's CLAUDE.md
    # and trust state: the only variables left are the cwd and the configs.
    glm_capture_run() {
      local cwd="$1" capname="$2" port="" pid="" _
      rm -f "$glm_sandbox/$capname.json"
      CAP_OUT="$glm_sandbox" CAP_NAME="$capname" node "$glm_sandbox/capture.cjs" \
        > "$glm_sandbox/$capname.port.out" 2>&1 &
      pid=$!
      glm_node_pid=$pid
      for _ in $(seq 1 40); do
        port="$(sed -n 's/^PORT //p' "$glm_sandbox/$capname.port.out" 2>/dev/null | head -1)"
        [ -n "$port" ] && break
        sleep 0.25
      done
      if [ -z "$port" ]; then
        bad "GLM-compat surface: the capture server never reported a port ($capname)"
        sed 's/^/      /' "$glm_sandbox/$capname.port.out" >&2
        kill "$pid" 2>/dev/null || true
        return
      fi
      sed -i.bak "s|http://127.0.0.1:[0-9]*/v1|http://127.0.0.1:$port/v1|" \
        "$glm_sandbox/agent/models.json"
      ( cd "$cwd" \
          && HOME="$glm_sandbox/home" PI_CODING_AGENT_DIR="$glm_sandbox/agent" \
             PATH="$glm_sandbox/bin:$PATH" \
             timeout 180 "$pi_bin" -p --no-session --model capglm/glm-5.3 -nc -na \
             "Reply with the single word OK" </dev/null ) \
        > "$glm_sandbox/$capname.run.out" 2> "$glm_sandbox/$capname.run.err" || true
      kill "$pid" 2>/dev/null || true
      [ "$glm_node_pid" = "$pid" ] && glm_node_pid=""
    }

    glm_capture_run "$glm_sandbox/proj"      capA
    glm_capture_run "$glm_sandbox/elsewhere" capB
    # 🔴 NEGATIVE CONTROL: same sandbox, same probe, one variable — both config copies
    # deleted. Without it, "the capture carries the pf tools" is satisfiable by tools
    # arriving from any other source (a stale metadata cache, a global config outside the
    # sandbox), and the positive arms prove nothing about what install.sh wrote.
    rm -f "$glm_sandbox/agent/mcp.json" "$glm_sandbox/proj/.mcp.json"
    glm_capture_run "$glm_sandbox/elsewhere" capN

    verdicts <<< "$(python3 - "$glm_sandbox" <<'PY'
import json, os, sys

sandbox = sys.argv[1]
adv = json.load(open(os.path.join(sandbox, "advertised.json")))
adv_names = set(adv["names"] or [])
wire_expected = {"polyforge_" + n for n in adv_names}

def emit(v, msg):
    print("%s|GLM-compat surface: %s" % (v, msg))

def load_cap(name):
    path = os.path.join(sandbox, name + ".json")
    if not os.path.exists(path):
        return None, ("no capture at %s — pi never reached the capture server; stderr is in "
                      "%s.run.err" % (path, os.path.join(sandbox, name)))
    try:
        return json.load(open(path)), None
    except Exception as e:
        return None, "capture at %s is not JSON: %s" % (path, e)

for cap, label in (("capA", "main-session capture"), ("capB", "child-cwd capture")):
    d, err = load_cap(cap)
    if d is None:
        emit("FAIL", "%s: %s" % (label, err))
        continue
    if not isinstance(d.get("messages"), list) or not isinstance(d.get("tools"), list):
        emit("FAIL", "%s: not a real request (messages/tools absent) — the arm was vacuous" % label)
        continue
    wire_pf = {t["function"]["name"] for t in d["tools"]
               if (t.get("function") or {}).get("name", "").startswith("polyforge_pf_")}
    missing = wire_expected - wire_pf
    extra = wire_pf - wire_expected
    if missing:
        emit("FAIL", "%s: carries %d/%d polyforge_pf_* tools — the adapter or config "
             "dropped: %s" % (label, len(wire_pf), len(wire_expected),
                              ", ".join(sorted(missing)[:5])))
    elif label == "main-session capture":
        emit("PASS", "%s: carries all %d polyforge_pf_* tools the server advertises"
             % (label, len(wire_pf)))
    else:
        emit("PASS", "%s: carries all %d polyforge_pf_* tools from the global config alone"
             % (label, len(wire_pf)))

# Neither bypass door may be VISIBLE to the model, in either capture — the bridge denies
# calling them (IR1), but the request surface is what a strict backend sees first.
door_hits = []
for cap in ("capA", "capB"):
    d, err = load_cap(cap)
    if d is None:
        # A missing capture must FAIL this check too, not fall through to a green line:
        # the first run of this section shipped exactly that false green.
        door_hits.append("%s has no capture (%s)" % (cap, err))
        continue
    names = {(t.get("function") or {}).get("name") for t in d.get("tools", [])}
    for door in ("mcp", "mcpScript"):
        if door in names:
            door_hits.append("%s lists %s" % (cap, door))
if door_hits:
    emit("FAIL", "neither capture lists mcp or mcpScript — violated: " + ", ".join(door_hits))
else:
    emit("PASS", "neither capture lists mcp or mcpScript")

# The schemas pi sends must be the advertised ones, unmodified — a rewrites-them mutant
# (an adapter "normalization" that, say, union-ifies a param) would keep every presence
# check above green while quietly reintroducing the constructs the hygiene arm pins out.
schema_drift = []
for cap in ("capA", "capB"):
    d, err = load_cap(cap)
    if d is None:
        schema_drift.append("%s has no capture (%s)" % (cap, err))
        continue
    for t in d.get("tools", []):
        fn = t.get("function") or {}
        n = fn.get("name", "")
        if not n.startswith("polyforge_pf_"):
            continue
        want = adv["schemas"].get(n[len("polyforge_"):])
        if json.dumps(fn.get("parameters"), sort_keys=True) != json.dumps(want, sort_keys=True):
            schema_drift.append("%s:%s" % (cap, n))
if schema_drift:
    emit("FAIL", "pi sends the advertised schemas unmodified — drifted: %s"
         % ", ".join(schema_drift[:5]))
else:
    emit("PASS", "pi sends the advertised schemas unmodified")

# The negative control: with BOTH config copies deleted the same probe must come back with
# ZERO pf tools — otherwise the positive arms were being fed from somewhere else.
d, err = load_cap("capN")
if d is None:
    emit("FAIL", "negative control: %s" % err)
else:
    wire_pf = [t for t in d.get("tools", [])
               if (t.get("function") or {}).get("name", "").startswith("polyforge_pf_")]
    if wire_pf:
        emit("FAIL", "negative control — both config copies deleted yet the capture still "
             "carries %d polyforge_pf_* tool(s), so the positive arms were not fed by the "
             "configs this section installs" % len(wire_pf))
    elif not isinstance(d.get("messages"), list):
        emit("FAIL", "negative control — the run produced no real request, so its silence "
             "about pf tools is not evidence")
    else:
        emit("PASS", "negative control — both config copies deleted leaves the capture "
             "with zero polyforge_pf_* tools")
PY
)"
    if [ -n "$glm_node_pid" ]; then
      kill "$glm_node_pid" 2>/dev/null || true
      glm_node_pid=""
    fi
  fi
  rm -rf "$glm_sandbox"
  trap - EXIT
fi

echo ""
[ "$fails" -eq 0 ] && { echo "ALL PASS"; exit 0; } || { echo "$fails FAILED" >&2; exit 1; }
