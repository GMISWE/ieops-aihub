#!/usr/bin/env bash
# Test suite for hooks/pf-skill-router (PreToolUse(Skill) engine-router).
#
# Feeds synthetic PreToolUse payloads on stdin and asserts on the emitted JSON. The router
# json.dumps with ensure_ascii=True, so all assertions match ASCII substrings only (Chinese
# prose is \uXXXX-escaped and never asserted on).
#
# Covers: three-skill identification, composition (common + engine), superpowers branch vs
# native fallback, common fragments injected in BOTH branches, prefix stripping, non-target
# inert, empty/bad payload safety.

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
run() { # skill, ws  -> stdout = router output
  local skill="$1" ws="$2"
  printf '{"tool_name":"Skill","tool_input":{"skill":"%s"},"cwd":"%s"}' "$skill" "$ws" \
    | HOME="$home_empty" CLAUDE_PLUGIN_ROOT="$plugin_root" "$router" 2>/dev/null
}
run_raw() { # raw_payload, ws -> stdout
  printf '%s' "$1" | HOME="$home_empty" CLAUDE_PLUGIN_ROOT="$plugin_root" "$router" 2>/dev/null
}
has()  { case "$1" in *"$2"*) return 0;; *) return 1;; esac; }
ck()      { if has "$1" "$2"; then echo "  PASS: $3"; else echo "  FAIL: $3 (missing: $2)" >&2; fails=$((fails+1)); fi; }
ck_not()  { if has "$1" "$2"; then echo "  FAIL: $3 (unexpected: $2)" >&2; fails=$((fails+1)); else echo "  PASS: $3"; fi; }
ck_empty(){ if [ -z "$1" ]; then echo "  PASS: $2"; else echo "  FAIL: $2 (expected empty, got ${#1} chars)" >&2; fails=$((fails+1)); fi; }

echo "== pf-spec, superpowers OFF (native) =="
o="$(run polyforge:pf-spec "$ws_off")"
ck "$o" "Guide spec definition" "native engine fragment injected"
ck "$o" "methodology.spec"      "storage common: artifact type substituted"
ck "$o" "Memory-First recall"   "memory common injected"
ck "$o" "Bracket every step"    "lifecycle common injected"
ck "$o" "engine: native"        "router header labels engine=native"
ck_not "$o" "engine: superpowers" "native branch not labelled superpowers"

echo "== pf-spec, superpowers ON =="
o="$(run polyforge:pf-spec "$ws_on")"
ck "$o" "superpowers:brainstorming" "superpowers engine pointer injected"
ck "$o" "docs/superpowers/specs"    "pointer tells model where superpowers writes the doc"
ck "$o" "pf_save_artifact"          "pointer says record the doc into polyforge after writing"
ck "$o" "methodology.spec"          "storage common present in superpowers branch (both-branch invariant)"
ck "$o" "Memory-First recall"       "memory common present in superpowers branch (both-branch invariant)"
ck "$o" "Bracket every step"        "lifecycle common present in superpowers branch (both-branch invariant)"
ck_not "$o" "Guide spec definition" "native engine fragment NOT injected when superpowers on"
ck "$o" "MUST include"             "spec pointer mandates a diagram"
ck "$o" "d2"                       "spec pointer names d2 format"

echo "== pf-plan =="
o="$(run polyforge:pf-plan "$ws_off")"
ck "$o" "Draft plan steps" "plan native engine injected"
ck "$o" "methodology.plan" "plan storage artifact type"
o="$(run polyforge:pf-plan "$ws_on")"
ck "$o" "superpowers:writing-plans" "plan superpowers pointer"
ck "$o" "docs/superpowers/plans"    "plan pointer tells where superpowers writes + record into polyforge"
ck "$o" "Bracket every step"        "plan lifecycle common in superpowers branch"
ck "$o" "MUST include"             "plan pointer mandates a diagram"
ck "$o" "d2"                       "plan pointer names d2 format"

echo "== pf-execute =="
o="$(run polyforge:pf-execute "$ws_off")"
ck "$o" "parse_review_result" "execute native main loop injected"
ck "$o" "default_model"       "execute native fragment defines default_model"
ck "$o" "sonnet"              "execute native default_model = sonnet"
o="$(run polyforge:pf-execute "$ws_on")"
ck "$o" "subagent-driven-development"      "execute superpowers pointer"
ck "$o" "finishing-a-development-branch"   "execute D6 boundary present"
ck "$o" "Memory-First recall"              "execute memory common in superpowers branch"
ck "$o" "model: sonnet"                    "execute pointer: cheap/standard tier -> sonnet"
ck "$o" "model: opus"                      "execute pointer: review/architecture tier -> opus"
ck_not "$o" "superpowers:executing-plans"  "execute pointer fixed on SDD (executing-plans removed)"

echo "== prefix stripping (skill without 'polyforge:' prefix) =="
o="$(run pf-spec "$ws_off")"
ck "$o" "Guide spec definition" "bare 'pf-spec' recognised"

echo "== non-target skill is inert =="
o="$(run polyforge:pf-help "$ws_off")"
ck_empty "$o" "pf-help -> no injection"
o="$(run polyforge:pf-status "$ws_on")"
ck_empty "$o" "pf-status -> no injection"

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
