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
# CI STATUS: WIRED (aihub#293). .github/workflows/ci.yml runs this as the step
# "aihub#293 using-polyforge payload budget gate". A non-zero exit fails that step directly;
# on top of that it asserts named PASS markers, that no SKIP fired, and a floor on the PASS
# count, to cover the case the exit code CANNOT report — a run that exits 0 having executed
# nothing (see the `command -v python3` line below for one way that happens).
# ADDING a check needs no CI change: the floor is `-ge`, a one-way ratchet. Only DELETING or
# RENAMING one of the markers that step greps for does.
#
# WHAT IS ASSERTED
#   1. Size:     assembled payload <= PF_PAYLOAD_MAX_CHARS, measured in characters, and
#                measured for the WORST CASE (all conditional `when:` fragments enabled).
#                Measured for the FULL assembly even when the hook degrades it — see the
#                note next to `degraded` below, or this check becomes a tautology.
#   2. Order:    IR1-IR3 land inside the preview window, so that if the budget is ever
#                busted again the Iron Rules survive truncation rather than boilerplate.
#   3. Tiering:  the on-demand fragments are absent from the payload but each is still named
#                by fragments/on-demand-index.md, so none of them becomes orphaned.
#   4. Control:  a deliberately mis-ordered build (Iron Rules last) must FAIL assertion 2.
#                Without this the window check could pass vacuously and prove nothing.
#   5. Degrade:  an over-budget manifest makes the hook emit a fitting, banner-led payload
#                and warn on stderr instead of failing silently (aihub#293), with the real
#                tree as the negative control that the degrade path is normally dormant.

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
assemble_err() { # plugin_root, stderr_path -> stdout = raw additionalContext (may be empty)
  env -u CURSOR_PLUGIN_ROOT -u PLUGIN_ROOT \
      HOME="$home_max" CLAUDE_PROJECT_DIR="$ws" "$1/hooks/pf-session-start" 2>"$2" \
  | python3 -c 'import json,sys
try: sys.stdout.write(json.load(sys.stdin)["hookSpecificOutput"]["additionalContext"])
except Exception: pass'
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

main_err="$tmp/main.err"
ctx="$(assemble_err "$plugin_root" "$main_err")"
[ -n "$ctx" ] || { echo "FAIL: hook produced no additionalContext" >&2; exit 1; }
delivered_n="$(printf '%s' "$ctx" | charlen)"  # characters, not bytes (bash ${#} is bytes under LC_ALL=C)
vis="$(printf '%s' "$ctx" | tle)"
visn="$(printf '%s' "$vis" | charlen)"

# aihub#293: the hook now DEGRADES an over-budget payload (drops trailing fragments and
# prepends a banner) instead of letting the harness truncate it silently. That is right for
# a session, and poison for this test: what comes back is then the post-degradation size,
# which is under the limit BY CONSTRUCTION. Measuring it would turn the size gate below into
# a tautology — the exact defect this suite exists to prevent, reintroduced through the fix.
# So take the hook's own pre-degradation figure off stderr when it degraded, and treat the
# degradation itself as a failure. `n` is therefore always the size of the FULL assembly.
degraded=0
n="$delivered_n"
full_n="$(sed -n 's/.*payload is \([0-9][0-9]*\) chars.*/\1/p' "$main_err" | head -1)"
if [ -n "$full_n" ]; then degraded=1; n="$full_n"; fi

echo "using-polyforge SessionStart payload (worst case: $KNOWN_CONDITIONS enabled)"
echo "  assembled : $n chars (gate $PF_PAYLOAD_MAX_CHARS, harness hard limit $HARNESS_HARD_LIMIT)"
if [ "$degraded" -eq 1 ]; then
echo "  delivered : $delivered_n chars  <-- DEGRADED by the hook; see check 1"
fi
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
if [ "$degraded" -eq 0 ]; then
  ok "the hook did not have to degrade this payload"
else
  bad "the hook DEGRADED this payload ($n chars assembled -> $delivered_n delivered): the manifest is over the $HARNESS_HARD_LIMIT-char hard limit and fragments were dropped from the session. The degrade path is a safety net for sessions running an older plugin copy, NOT an acceptable steady state — fix the budget."
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
check_deferred "repo-detail.md"         "The pointer is written by the same code that writes the map file"

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
echo "5. runtime self-check — an over-budget manifest degrades LOUDLY, not silently (aihub#293)"
# Assertions 1-4 above all measure THIS tree. They cannot see what the hook does when a
# manifest is over budget anyway — an older plugin copy, a hand-edited fragment, a branch
# that never ran this suite. That case is the aihub#285 bug itself, so the hook now checks
# its own assembled length at runtime. Build a tree that really busts the budget and drive it.
BANNER_MARK="POLYFORGE SESSION-START PAYLOAD OVER BUDGET"

# Negative control FIRST, reusing the real tree's own run from the top of this file: the
# degrade path must be DORMANT here. Without this, "the banner is present" below would pass
# just as well for a hook that degrades unconditionally.
case "$ctx" in
  *"$BANNER_MARK"*) bad "the over-budget banner appears on the real tree ($n chars) — either the manifest is over budget (see check 1) or the check fires unconditionally and proves nothing";;
  *)                ok "real tree ($n chars): no over-budget banner";;
esac
if [ -s "$main_err" ]; then
  bad "real tree wrote to stderr: $(head -c 200 "$main_err") — the warning must fire only when over budget"
else
  ok "real tree ($n chars): hook stderr is silent"
fi

# Now a REAL violation: pad a resident fragment until the assembled payload passes 10,000.
# Sized from the measured $n so it stays a violation as the manifest changes.
over="$tmp/over"; cp -r "$plugin_root" "$over"
padded_rel="fragments/repo-routing.md"
padded_frag="$over/skills/using-polyforge/$padded_rel"
pad_n=$(( HARNESS_HARD_LIMIT - n + 200 ))

# What this manifest WOULD have assembled to under the pre-aihub#293 hook, predicted
# independently of the hook: the clean payload plus the pad and the "\n\n" that joins it on.
# Cross-checked against the hook's own reported figure below — two numbers derived from
# different sides, so a wrong one shows up as a disagreement rather than as a green test.
undegraded_n=$(( n + pad_n + 2 ))

# MEASURE that this fixture is really a violation; do not merely COMPUTE it. Padding that
# file busts the budget only if the manifest actually @includes it. Move it to the on-demand
# tier — which is exactly what the SIZE BUDGET note prescribes when this gate goes red — or
# delete it, and the pad never reaches the payload: nothing goes over budget, the hook
# correctly does not degrade, and every assertion below then fails POINTING AT THE HOOK when
# the broken thing is this fixture. `undegraded_n` cannot see that; it is arithmetic and
# would keep reporting a violation that is not happening. So anchor on the clean payload
# assembled at the top of this file: the fragment's own first line has to be in it.
pad_anchor="$(grep -m1 . "$padded_frag" 2>/dev/null || true)"
pad_ok=0
if [ -z "$pad_anchor" ]; then
  bad "FIXTURE, NOT HOOK: $padded_rel is missing or empty, so check 5's over-budget tree cannot be built. Point padded_rel at a fragment the manifest @includes."
elif [ "$undegraded_n" -le "$HARNESS_HARD_LIMIT" ]; then
  bad "FIXTURE, NOT HOOK: padding to $undegraded_n chars does not reach $HARNESS_HARD_LIMIT, so nothing below is tested. Raise pad_n."
else
  case "$ctx" in
    *"$pad_anchor"*)
      ok "fixture target $padded_rel is resident and pads to $undegraded_n > $HARNESS_HARD_LIMIT chars — a real violation"
      pad_ok=1;;
    *)
      bad "FIXTURE, NOT HOOK: $padded_rel is not in the resident payload (moved to the on-demand tier?), so padding it busts no budget. Point padded_rel at a fragment the manifest @includes.";;
  esac
fi

# Everything below drives that fixture. Skipped when the fixture is invalid — running it
# anyway would emit a run of hook-shaped failures for a hook that is behaving correctly.
if [ "$pad_ok" -eq 1 ]; then
python3 - "$padded_frag" "$pad_n" <<'PY'
import sys
p, k = sys.argv[1], int(sys.argv[2])
s = open(p, encoding="utf-8").read()
open(p, "w", encoding="utf-8").write(s.rstrip("\n") + "\n\n" + ("PAD" * k)[:k] + "\n")
PY
over_err="$tmp/over.err"
env -u CURSOR_PLUGIN_ROOT -u PLUGIN_ROOT \
    HOME="$home_max" CLAUDE_PROJECT_DIR="$ws" "$over/hooks/pf-session-start" \
    >"$tmp/over.json" 2>"$over_err"
over_rc=$?
over_ctx="$(python3 -c 'import json,sys
try: sys.stdout.write(json.load(sys.stdin)["hookSpecificOutput"]["additionalContext"])
except Exception: pass' < "$tmp/over.json")"
over_n="$(printf '%s' "$over_ctx" | charlen)"

[ "$over_rc" -eq 0 ] && ok "over-budget tree: hook still exits 0 (never blocks startup)" \
                     || bad "over-budget tree: hook exited $over_rc — it must never block startup"
[ -n "$over_ctx" ] && ok "over-budget tree: hook still emits an additionalContext" \
                   || bad "over-budget tree: hook emitted no additionalContext — degrading must not mean going silent"
if grep -q "payload is $undegraded_n chars" "$over_err"; then
  ok "the hook measured the same $undegraded_n chars this test predicted independently"
else
  bad "the hook reports a different pre-degradation size than the $undegraded_n predicted here: [$(head -c 200 "$over_err")]"
fi
if [ "$over_n" -le "$HARNESS_HARD_LIMIT" ] && [ "$over_n" -gt 0 ]; then
  ok "degraded payload $over_n <= $HARNESS_HARD_LIMIT chars (delivered whole, not replaced by a preview)"
else
  bad "degraded payload is $over_n chars, still over the $HARNESS_HARD_LIMIT hard limit — it would be silently truncated exactly as before"
fi
case "$over_ctx" in
  *"$BANNER_MARK"*) ok "degraded payload carries the over-budget banner";;
  *)                bad "degraded payload has no banner — the omission is silent, which is the aihub#285 failure mode";;
esac
for ir in "IR1 —" "IR2 —" "IR3 —"; do
  case "$over_ctx" in *"$ir"*) ok "$ir survives degradation";; *) bad "$ir dropped by degradation — Iron Rules must be the last thing to go";; esac
done
if grep -q "over the $HARNESS_HARD_LIMIT-char harness limit" "$over_err"; then
  ok "hook wrote the over-budget finding to stderr (visible outside the model)"
else
  bad "nothing on stderr for an over-budget payload: [$(head -c 200 "$over_err")]"
fi

# The banner must not lie: everything it names as dropped has to be genuinely absent.
dropped_list="$(sed -n 's/.*; dropped \(.*\)\. Run .*/\1/p' "$over_err" | tr -d ' ' | tr ',' ' ')"
if [ -z "$dropped_list" ]; then
  bad "stderr names no dropped fragment, so the claim 'dropped X' cannot be checked: [$(head -c 200 "$over_err")]"
else
  named_ok=1
  for f in $dropped_list; do
    # first non-empty line of the fragment: present in the full assembly, absent once dropped
    marker="$(grep -m1 . "$over/skills/using-polyforge/$f" 2>/dev/null)"
    [ -n "$marker" ] || { bad "cannot read dropped fragment $f to verify it"; named_ok=0; continue; }
    case "$over_ctx" in *"$marker"*) bad "banner claims $f was dropped, but its content is still in the payload"; named_ok=0;; esac
  done
  [ "$named_ok" -eq 1 ] && ok "every fragment the banner names as dropped ($dropped_list) is really absent"
fi
fi  # pad_ok

echo
if [ "$fails" -eq 0 ]; then
  echo "ALL PASS"
  exit 0
else
  echo "$fails CHECK(S) FAILED" >&2
  exit 1
fi
