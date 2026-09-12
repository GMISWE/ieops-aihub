#!/usr/bin/env bash
# Test suite for hooks/pf-skill-router (PreToolUse(Skill) engine-router).
#
# Feeds synthetic PreToolUse payloads on stdin and asserts on the emitted JSON. The router
# json.dumps with ensure_ascii=True, so all assertions match ASCII substrings only (Chinese
# prose is \uXXXX-escaped and never asserted on).
#
# Covers: pf-execute identification, composition (common + engine), superpowers branch vs
# native fallback, common fragments injected in BOTH branches, pf-spec/pf-plan routed
# header-only (aihub#478), non-target inert, empty/bad payload safety.

set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
router="$here/../hooks/pf-skill-router"
plugin_root="$(cd "$here/.." && pwd)"
[ -x "$router" ] || { echo "FAIL: router not executable at $router" >&2; exit 1; }
command -v python3 >/dev/null 2>&1 || { echo "SKIP: python3 unavailable"; exit 0; }

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

home_empty="$tmp/home"; mkdir -p "$home_empty/.claude"   # isolate from real ~/.claude

ws_on="$tmp/ws_on"; mkdir -p "$ws_on/.claude"
echo 'version: 1' > "$ws_on/.polyforge.yaml"
printf '{"enabledPlugins":{"superpowers@gmi-marketplace":true}}\n' > "$ws_on/.claude/settings.json"

ws_off="$tmp/ws_off"; mkdir -p "$ws_off/.claude"
echo 'version: 1' > "$ws_off/.polyforge.yaml"
printf '{"enabledPlugins":{"superpowers@gmi-marketplace":false}}\n' > "$ws_off/.claude/settings.json"

fails=0
run() { # skill, ws [, root]  -> stdout = router output, against the tree at root
  # root defaults to the shipped tree. Fixture sections pass their own copy instead of
  # keeping a second, hand-maintained copy of this pipeline (run_fx was exactly that: a
  # payload/env change applied to one copy would silently not be applied to the other).
  local skill="$1" ws="$2" root="${3:-$plugin_root}"
  printf '{"tool_name":"Skill","tool_input":{"skill":"%s"},"cwd":"%s"}' "$skill" "$ws" \
    | HOME="$home_empty" CLAUDE_PLUGIN_ROOT="$root" bash "$root/hooks/pf-skill-router" 2>/dev/null
}
run_raw() { # raw_payload, ws -> stdout
  printf '%s' "$1" | HOME="$home_empty" CLAUDE_PLUGIN_ROOT="$plugin_root" "$router" 2>/dev/null
}
has()  { case "$1" in *"$2"*) return 0;; *) return 1;; esac; }
ck()      { if has "$1" "$2"; then echo "  PASS: $3"; else echo "  FAIL: $3 (missing: $2)" >&2; fails=$((fails+1)); fi; }
# ck_not refuses the vacuous pass (aihub#537): a negative check against EMPTY output proves
# nothing — with the router emitting nothing at all, every ck_not below printed PASS while
# asserting nothing, and only the positive checks had any discriminating power. Every ck_not
# call site in this file runs against output that must be non-empty (the intentionally-empty
# renders are asserted with ck_empty), so an empty haystack here is a broken fixture or a
# silent router, never a pass.
ck_not()  { if [ -z "$1" ]; then echo "  FAIL: $3 (vacuous: no output to assert against)" >&2; fails=$((fails+1)); elif has "$1" "$2"; then echo "  FAIL: $3 (unexpected: $2)" >&2; fails=$((fails+1)); else echo "  PASS: $3"; fi; }
ck_empty(){ if [ -z "$1" ]; then echo "  PASS: $2"; else echo "  FAIL: $2 (expected empty, got ${#1} chars)" >&2; fails=$((fails+1)); fi; }

# aihub#478. pf-spec and pf-plan keep their self-sufficient SKILL.md — the router does not
# assemble a body for them — but they ARE routed now, header-only, because a dispatched
# subagent inherits no SessionStart payload and so had neither the Iron Rules nor the
# three-segment output format. Both branches are checked: the mode consults no engine, so a
# difference between them would mean the branch leaked into a payload that has no engine.
echo "== pf-spec / pf-plan are routed HEADER-ONLY =="
for sk in pf-spec pf-plan; do
  for ws_name in off on; do
    eval "ws=\$ws_$ws_name"
    o="$(run polyforge:$sk "$ws")"
    ck     "$o" "Work-item-gated writes"  "$sk (sp $ws_name) carries IR1"
    ck     "$o" "MCP unavailable"         "$sk (sp $ws_name) carries IR3"
    ck     "$o" "Three-Segment Output"    "$sk (sp $ws_name) carries the output format"
    ck     "$o" "max 5 items"             "$sk (sp $ws_name) carries the Next-steps rule"
    ck     "$o" "self-sufficient"         "$sk (sp $ws_name) says the SKILL.md still rules"
    # The whole point of header-only: no step body, so none of the assembled fragments.
    ck_not "$o" "Memory-First recall"     "$sk (sp $ws_name) injects no _common/memory.md"
    ck_not "$o" "parse_review_result"     "$sk (sp $ws_name) injects no engine fragment"
    # The real plugin root, not the @@PLUGIN_ROOT@@ token: the token is substituted before
    # emission, so a check for it could only ever catch a broken substitution.
    ck_not "$o" "$plugin_root"            "$sk (sp $ws_name) names no on-demand pointer"
  done
done

echo "== pf-execute =="
o="$(run polyforge:pf-execute "$ws_off")"
ck "$o" "parse_review_result" "execute native main loop injected"
# aihub#338 layer 3: the native loop picks a tier from the STEP KIND. Assert the two tier
# constants and the predicate separately — a check for "sonnet" alone stayed green through the
# whole aihub#358 period, when the selector existed but could never match.
ck "$o" "DEFAULT_TIER"        "execute native fragment defines the default tier"
ck "$o" "RAISED_TIER"         "execute native fragment defines the raised tier"
ck "$o" "sonnet"              "execute native default tier = sonnet"
ck "$o" "opus"                "execute native raised tier = opus"
ck "$o" "sid.endswith("     "execute native selects the raised tier by step kind"
# aihub#338 layer 2: IR1-IR3 ride this hook because a dispatched subagent never sees the
# SessionStart payload. Verbatim coverage is gated in internal/cli/skill_router_payload_test.go;
# these three are the cheap smoke check that the header carries them at all. Matched on the rule
# TITLES, not on "IR1 - ...": the em dash is \u2014-escaped by json.dumps (see the header note).
ck "$o" "Work-item-gated writes"          "execute payload carries IR1"
ck "$o" "Analyze obstacles"               "execute payload carries IR2"
ck "$o" "MCP unavailable"                 "execute payload carries IR3"
# aihub#478: the same argument that put IR1-IR3 here applies to the response format. Matched
# on the Status field list and the Next-steps rule, not on the heading alone — _common/
# lifecycle.md used to name the three headings and nothing else, which is the exact gap.
ck "$o" "Three-Segment Output"            "execute payload carries the output format"
ck "$o" "max 5 items"                     "execute payload carries the Next-steps rule"
ck "$o" "owner_display"                   "execute payload carries the multi-wi column list"
o="$(run polyforge:pf-execute "$ws_on")"
ck "$o" "subagent-driven-development"      "execute superpowers pointer"
ck "$o" "finishing-a-development-branch"   "execute D6 boundary present"
ck "$o" "Memory-First recall"              "execute memory common in superpowers branch"
ck "$o" "model: sonnet"                    "execute pointer: cheap/standard tier -> sonnet"
ck "$o" "model: opus"                      "execute pointer: review/architecture tier -> opus"
ck_not "$o" "superpowers:executing-plans"  "execute pointer fixed on SDD (executing-plans removed)"
# aihub#557: the two greps above pin TODAY'S names; these pin the DERIVATION — the pointer's
# tier names must equal what engine.native.md's constants line declares, read from the source
# here so a legitimate re-tier moves this check along with both engine branches.
tier_line="$(grep -oE 'DEFAULT_TIER, RAISED_TIER = "[a-z][a-z0-9.-]*", "[a-z][a-z0-9.-]*"' \
  "$plugin_root/skills/pf-execute/engine.native.md")"
src_default="$(printf '%s' "$tier_line" | sed -E 's/.*= "([^"]*)", "([^"]*)"/\1/')"
src_raised="$(printf '%s' "$tier_line" | sed -E 's/.*= "([^"]*)", "([^"]*)"/\2/')"
if [ -n "$src_default" ] && [ -n "$src_raised" ]; then
  # Anchored on each bullet's closing words, not on "model: <name>" alone — a hook that
  # derives both names but SWAPS them still contains both substrings, so only the pairing
  # of role text to tier name can catch it.
  ck "$o" "debugging -> model: $src_default" "execute pointer default tier matches engine.native.md constants"
  ck "$o" "judgement -> model: $src_raised"  "execute pointer raised tier matches engine.native.md constants"
else
  echo "  FAIL: engine.native.md no longer declares the tier constants line — the derivation checks matched nothing" >&2
  fails=$((fails+1))
fi
ck_not "$o" "@@DEFAULT_TIER@@" "no unsubstituted default-tier placeholder leaks"
ck_not "$o" "@@RAISED_TIER@@"  "no unsubstituted raised-tier placeholder leaks"

echo "== prefix stripping (skill without 'polyforge:' prefix) =="
ck "$(run pf-spec "$ws_off")" "Three-Segment Output" "bare 'pf-spec' routes the same as the prefixed form"

echo "== non-target skill is inert =="
o="$(run polyforge:pf-help "$ws_off")"
ck_empty "$o" "pf-help -> no injection"
o="$(run polyforge:pf-status "$ws_on")"
ck_empty "$o" "pf-status -> no injection"

echo "== aihub#514: header-only empty-fragment guard + banner-first truncation =="
# Fixture tree: precise position/size assertions live in internal/cli/skill_router_payload_test.go
# (TestRoutedSkillHook_HeaderOnlyEmptyFragmentGuard / _HeaderOnlyOverBudgetBannerSurvives);
# this is the harness-free smoke check of the same two behaviours.
fx="$tmp/plugin_fx"; rm -rf "$fx"; cp -r "$plugin_root" "$fx"
run_fx() { run "$1" "$ws_off" "$fx"; } # skill -> stdout, against the fixture tree
# Control first: the copy renders before gutting, so the silence below is the guard, not the copy.
ck "$(run_fx polyforge:pf-spec)" "Three-Segment Output" "fixture copy renders pf-spec before gutting (control)"
# F5: a header-only payload with an empty resident fragment claims rules it does not carry -> inert.
# ck_empty alone would record a CRASHED hook as PASS (empty stdout either way), so the guard is
# pinned three-sided: empty stdout AND exit 0 AND the reason on stderr (reviewer-confirmed hole:
# a broken %-format in the guard passed the empty-stdout check).
: > "$fx/skills/using-polyforge/fragments/iron-rules.md"
fx_out="$(printf '{"tool_name":"Skill","tool_input":{"skill":"polyforge:pf-spec"},"cwd":"%s"}' "$ws_off" \
  | HOME="$home_empty" CLAUDE_PLUGIN_ROOT="$fx" bash "$fx/hooks/pf-skill-router" 2>"$tmp/fx_err")"
fx_rc=$?
ck_empty "$fx_out" "pf-spec with empty iron-rules.md -> no payload (guard fires)"
if [ "$fx_rc" -eq 0 ]; then
  echo "  PASS: guard exits 0 (inert, not a crash)"
else
  echo "  FAIL: guard exits $fx_rc — a crash is indistinguishable from the guard firing" >&2; fails=$((fails+1))
fi
ck "$(cat "$tmp/fx_err")" "NOT emitted" "guard names the reason on stderr"
# ...and the guard is scoped: step-body keeps its payload (the step body is the point there).
ck "$(run_fx polyforge:pf-execute)" "parse_review_result" "pf-execute with empty iron-rules.md still emits (fail-open)"
# F4: an over-budget header-only payload is tail-cut, so the banner must LEAD to survive.
cp "$plugin_root/skills/using-polyforge/fragments/iron-rules.md" "$fx/skills/using-polyforge/fragments/iron-rules.md"
python3 - "$fx/skills/using-polyforge/fragments/iron-rules.md" <<'PAD'
import sys
p = sys.argv[1]
s = open(p, encoding="utf-8").read().rstrip("\n")
open(p, "w", encoding="utf-8").write(s + "x" * 12000 + "\n")
PAD
o="$(run_fx polyforge:pf-spec)"
ck "$o" "THIS HEADER IS TRUNCATED"                    "over-budget header-only payload carries the banner"
ck "$o" "Treat any rule below as possibly incomplete" "...including the banner's final sentence (it leads, so the tail cut cannot take it)"
# aihub#558: the SAME oversized header in STEP-BODY mode — every fragment is dropped, the
# payload is still over, and the tail cut slices the header itself. The banner must say the
# header was cut (while still naming the dropped fragments, which ARE on disk), not keep the
# fragment-blaming wording. Precise assertions live in internal/cli/skill_router_payload_test.go
# (TestRoutedSkillHook_StepBodyBlowoutBannerNamesTheHeaderCut); this is the smoke check.
o="$(run_fx polyforge:pf-execute)"
ck     "$o" "THIS HEADER IS TRUNCATED"        "over-budget step-body payload names the header as what was cut"
ck     "$o" "_common/lifecycle.md"            "...while still naming the dropped fragments for disk recovery"
ck_not "$o" "THIS STEP BODY IS INCOMPLETE"    "...and drops the fragment-blaming wording"

echo "== aihub#557: superpowers pointer derives its tiers from engine.native.md at run time =="
# The checks above prove the shipped names agree; only a fixture whose SOURCE disagrees can
# prove derivation — a hook with the names baked in passes every equality check forever.
fx2="$tmp/plugin_fx2"; rm -rf "$fx2"; cp -r "$plugin_root" "$fx2"
python3 - "$fx2/skills/pf-execute/engine.native.md" <<'RETIER'
import re, sys
p = sys.argv[1]
s = open(p, encoding="utf-8").read()
# Match the constants line by SHAPE, not by today's names, so this fixture survives a
# legitimate re-tier (the whole point of the derivation it tests).
s2, n = re.subn(r'DEFAULT_TIER, RAISED_TIER = "[a-z][a-z0-9.-]*", "[a-z][a-z0-9.-]*"',
                'DEFAULT_TIER, RAISED_TIER = "tinker", "tailor"', s, count=1)
if n != 1:
    sys.exit("RETIER MUTATION DID NOT APPLY: constants line not found")
open(p, "w", encoding="utf-8").write(s2)
RETIER
o="$(run polyforge:pf-execute "$ws_on" "$fx2")"
ck     "$o" "debugging -> model: tinker" "re-tiered source -> pointer default tier follows"
ck     "$o" "judgement -> model: tailor" "re-tiered source -> pointer raised tier follows"
ck_not "$o" "model: $src_default" "re-tiered source -> shipped default tier gone from the pointer"
ck_not "$o" "model: $src_raised"  "re-tiered source -> shipped raised tier gone from the pointer"
# Failure mode: constants line gone entirely -> the superpowers payload is NOT emitted
# (fail-silent, stub fallback), never a payload carrying raw @@…@@ placeholders.
python3 - "$fx2/skills/pf-execute/engine.native.md" <<'DETIER'
import sys, re
p = sys.argv[1]
s = open(p, encoding="utf-8").read()
s2 = re.sub(r'DEFAULT_TIER, RAISED_TIER = "[^"]*", "[^"]*"\n', "", s)
if s2 == s:
    sys.exit("DETIER MUTATION DID NOT APPLY: constants line not found")
open(p, "w", encoding="utf-8").write(s2)
DETIER
ck_empty "$(run polyforge:pf-execute "$ws_on" "$fx2")" "missing tier constants -> superpowers payload not emitted (inert)"

echo "== malformed / empty payloads are safe =="
ck_empty "$(run_raw '' "$ws_off")"            "empty stdin -> no output"
ck_empty "$(run_raw 'not json{' "$ws_off")"   "garbage stdin -> no output"
ck_empty "$(run_raw '{"tool_input":{}}' "$ws_off")" "missing skill -> no output"
ck_empty "$(run_raw '[]' "$ws_off")"          "non-object payload -> no output"

echo
if [ "$fails" -eq 0 ]; then
  echo "ALL PASS"
  exit 0
else
  echo "$fails CHECK(S) FAILED" >&2
  exit 1
fi
