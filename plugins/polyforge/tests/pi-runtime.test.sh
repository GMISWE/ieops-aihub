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
ok()  { echo "  PASS: $1"; }
bad() { echo "  FAIL: $1" >&2; fails=$((fails+1)); }

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

echo ""
[ "$fails" -eq 0 ] && { echo "ALL PASS"; exit 0; } || { echo "$fails FAILED" >&2; exit 1; }
