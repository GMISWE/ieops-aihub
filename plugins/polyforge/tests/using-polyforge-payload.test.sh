#!/usr/bin/env bash
# Regression gate for the using-polyforge SessionStart payload (aihub#285).
#
# WHY THIS EXISTS
# ---------------
# hooks/pf-session-start assembles skills/using-polyforge/SKILL.md's @include manifest into
# ONE `additionalContext` string. Claude Code replaces any single hook output longer than
# 10,000 CHARACTERS with a `<persisted-output>` wrapper: the full text is written to a file
# and only a ~2,000-character preview reaches the model. The hook still exits 0, so the
# failure is completely silent.
#
# Both constants are MEASURED, not inferred. Real headless sessions on Claude Code 2.1.246:
#   10,000-char additionalContext -> delivered intact
#   10,001-char additionalContext -> truncated
#   10,000-char / 29,658-byte CJK -> delivered intact  => the limit counts CHARACTERS, not bytes
# The preview rule is the shipped `tle(e,t)`: take the first 2,000 chars, find the last
# newline in that slice, and cut there if its index > 1,000 (otherwise use the raw 2,000).
# Ported below as tle(); validated against 178 real persisted previews, exact match on all.
#
# Before the fix the manifest assembled to 18,286 characters, so ~89% of the skill —
# including all of Iron Rules, the three-segment output format and NL Routing — reached no
# model at all, for every session since 2026-08-05.
#
# !! CI STATUS: THIS SUITE IS NOT WIRED INTO CI YET. !!
# .github/workflows/ci.yml runs only launcher-update-check.test.sh; every other suite in this
# directory (including this one) is advisory and runs only when invoked by hand — see the
# aihub#254 note at ci.yml:171-183 for the documented cost of that gap. Wiring this in is
# tracked separately (ci.yml is owned by a concurrent work item). Until then this file is a
# manual check, NOT an automatic guarantee. Do not describe it as one.
#
# WHAT IS ASSERTED
#   1. Size:     assembled payload <= PF_PAYLOAD_MAX_CHARS, measured in characters, and
#                measured for the WORST CASE (all conditional `when:` fragments enabled).
#   2. Order:    IR1-IR3 land inside the preview window, so that if the budget is ever
#                busted again the Iron Rules survive truncation rather than boilerplate.
#   3. Tiering:  the on-demand fragments are absent from the payload but each is still named
#                by fragments/on-demand-index.md, so none of them becomes orphaned.
#   4. Control:  a deliberately mis-ordered build (Iron Rules last) must FAIL assertion 2.
#                Without this the window check could pass vacuously and prove nothing.

set -uo pipefail

# Gate, in CHARACTERS. The harness's hard limit is 10000; we sit below it on purpose so the
# band between PF_PAYLOAD_MAX_CHARS and 10000 is a "test red but users still fine" warning
# zone. If this fails, do NOT raise this number — move a fragment to the on-demand tier
# (see the SIZE BUDGET comment in skills/using-polyforge/SKILL.md).
PF_PAYLOAD_MAX_CHARS=9800
HARNESS_HARD_LIMIT=10000
PREVIEW_CHARS=2000

# Every condition hooks/pf-session-start's cond_met() knows about. The size gate measures
# with ALL of them enabled, because a CI runner's own $HOME has none of them and would
# otherwise measure a payload no real user receives.
KNOWN_CONDITIONS="superpowers"

here="$(cd "$(dirname "$0")" && pwd)"
plugin_root="$(cd "$here/.." && pwd)"
hook="$plugin_root/hooks/pf-session-start"
[ -x "$hook" ] || { echo "FAIL: hook not executable at $hook" >&2; exit 1; }
command -v python3 >/dev/null 2>&1 || { echo "SKIP: python3 unavailable"; exit 0; }

tmp="$(mktemp -d 2>/dev/null || mktemp -d -t pfpayload)"
trap 'rm -rf "$tmp"' EXIT

ws="$tmp/ws"; mkdir -p "$ws"
echo 'version: 1' > "$ws/.polyforge.yaml"

# Fixture HOME with every known condition forced ON -> worst-case assembly.
home_max="$tmp/home_max"; mkdir -p "$home_max/.claude"
python3 - "$home_max/.claude/settings.json" $KNOWN_CONDITIONS <<'PY'
import json,sys
json.dump({"enabledPlugins":{c+"@fixture":True for c in sys.argv[2:]}}, open(sys.argv[1],"w"))
PY

fails=0
ok()  { echo "  PASS: $1"; }
bad() { echo "  FAIL: $1" >&2; fails=$((fails+1)); }

# Assemble the additionalContext for a given plugin root, hermetically:
#  - HOME pinned to the all-conditions-on fixture (worst case)
#  - CURSOR_PLUGIN_ROOT/PLUGIN_ROOT scrubbed, or the hook emits a different JSON shape
assemble() { # plugin_root -> stdout = raw additionalContext
  env -u CURSOR_PLUGIN_ROOT -u PLUGIN_ROOT \
      HOME="$home_max" CLAUDE_PROJECT_DIR="$ws" "$1/hooks/pf-session-start" 2>/dev/null \
  | python3 -c 'import json,sys; sys.stdout.write(json.load(sys.stdin)["hookSpecificOutput"]["additionalContext"])'
}

# Faithful port of the harness function that builds the preview.
tle() { # stdin = payload -> stdout = visible window
  PREVIEW_CHARS="$PREVIEW_CHARS" python3 -c '
import os,sys
e=sys.stdin.read(); t=int(os.environ["PREVIEW_CHARS"])
if len(e)<=t: sys.stdout.write(e)
else:
    r=e[:t].rfind("\n"); o=r if r> t*0.5 else t
    sys.stdout.write(e[:o])
'
}
charlen() { python3 -c 'import sys; sys.stdout.write(str(len(sys.stdin.read())))'; }

ctx="$(assemble "$plugin_root")"
[ -n "$ctx" ] || { echo "FAIL: hook produced no additionalContext" >&2; exit 1; }
n="$(printf '%s' "$ctx" | charlen)"     # characters, not bytes (bash ${#} is bytes under LC_ALL=C)
vis="$(printf '%s' "$ctx" | tle)"
visn="$(printf '%s' "$vis" | charlen)"

echo "using-polyforge SessionStart payload (worst case: $KNOWN_CONDITIONS enabled)"
echo "  assembled : $n chars (gate $PF_PAYLOAD_MAX_CHARS, harness hard limit $HARNESS_HARD_LIMIT)"
echo "  preview   : $visn chars"
echo

echo "0. the worst-case fixture actually covers every condition in the manifest"
unknown="$(grep -o '^when:[[:space:]]*[^[:space:]]*' "$plugin_root/skills/using-polyforge/SKILL.md" \
           | awk '{print $2}' | while read -r c; do
               case " $KNOWN_CONDITIONS " in *" $c "*) ;; *) echo "$c";; esac
             done)"
if [ -z "$unknown" ]; then
  ok "no \`when:\` condition outside KNOWN_CONDITIONS ($KNOWN_CONDITIONS)"
else
  bad "manifest uses condition(s) [$(echo $unknown)] not in KNOWN_CONDITIONS — the size gate would measure a payload real users do not get. Add them to KNOWN_CONDITIONS."
fi

echo
echo "1. size budget"
if [ "$n" -le "$PF_PAYLOAD_MAX_CHARS" ]; then
  ok "payload $n <= $PF_PAYLOAD_MAX_CHARS chars"
else
  bad "payload $n exceeds gate $PF_PAYLOAD_MAX_CHARS. Move a fragment to the on-demand tier; do not raise the gate. (Harness truncates silently above $HARNESS_HARD_LIMIT chars.)"
fi

echo
echo "2. Iron Rules reach the model even under truncation"
for ir in "IR1 —" "IR2 —" "IR3 —"; do
  case "$ctx" in *"$ir"*) ok "$ir present in payload";; *) bad "$ir missing from payload entirely";; esac
  case "$vis" in *"$ir"*) ok "$ir inside preview window";; *) bad "$ir outside preview window — @include iron-rules.md earlier in SKILL.md";; esac
done

echo
echo "3. on-demand tier is deferred, not orphaned"
index="$plugin_root/skills/using-polyforge/fragments/on-demand-index.md"
check_deferred() { # fragment_basename, marker distinctive to that fragment
  local frag="$plugin_root/skills/using-polyforge/fragments/$1"
  # the marker must really live in that fragment, or the "absent" check passes for free
  if grep -qF -- "$2" "$frag" 2>/dev/null; then :; else
    bad "$1: marker not found in the fragment itself — this check would pass vacuously"; return
  fi
  case "$ctx" in
    *"$2"*) bad "$1 is back in the session-start payload (marker: $2) — it blows the budget";;
    *)      ok "$1 not injected";;
  esac
  if grep -qF -- "$1" "$index"; then ok "$1 named by the on-demand index"
  else bad "$1 is neither injected nor listed in on-demand-index.md — it is orphaned"; fi
}
check_deferred "post-claim-routing.md"  "Mandatory output rules"
check_deferred "memory-conventions.md"  "Cross-memory links"
check_deferred "diagram-convention.md"  "degrades gracefully back to a code block"
check_deferred "platform-adaptation.md" "Copilot CLI**: installs as a native plugin"

echo
echo "4. negative control — the window check must be able to fail"
# Rebuild the plugin with iron-rules.md moved to the END of the manifest. IR3 must then fall
# outside the preview window. If this build also "passes", assertion 2 proves nothing.
ctl="$tmp/ctl"; cp -r "$plugin_root" "$ctl"
python3 - "$ctl/skills/using-polyforge/SKILL.md" <<'PY'
import sys
p=sys.argv[1]; s=open(p,encoding="utf-8").read()
line="@include: fragments/iron-rules.md"
assert line in s, "manifest no longer @includes iron-rules.md — update this control"
s=s.replace(line+"\n","",1).rstrip("\n")+"\n"+line+"\n"
open(p,"w",encoding="utf-8").write(s)
PY
ctl_ctx="$(assemble "$ctl")"
ctl_vis="$(printf '%s' "$ctl_ctx" | tle)"
case "$ctl_ctx" in
  *"IR3 —"*) ok "control build still contains IR3 (only its position changed)";;
  *)         bad "control build lost IR3 — the control is broken, not the ordering";;
esac
case "$ctl_vis" in
  *"IR3 —"*) bad "IR3 still inside the window with iron-rules.md moved LAST — the window check has no discriminating power";;
  *)         ok "IR3 falls out of the window when iron-rules.md is moved last (check discriminates)";;
esac

echo
if [ "$fails" -eq 0 ]; then
  echo "ALL PASS"
  exit 0
else
  echo "$fails CHECK(S) FAILED" >&2
  exit 1
fi
