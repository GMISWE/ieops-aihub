#!/usr/bin/env bash
# Test suite for hooks/pf-superpowers-bridge. Feeds synthetic PostToolUse(Write|Edit)
# payloads on stdin; asserts on emitted JSON. Save is exercised via PF_BRIDGE_DRYRUN=1
# (prints the would-be request instead of POSTing).
set -uo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
bridge="$here/../hooks/pf-superpowers-bridge"
[ -x "$bridge" ] || { echo "FAIL: bridge not executable" >&2; exit 1; }
command -v python3 >/dev/null 2>&1 || { echo "SKIP: python3 unavailable"; exit 0; }
fails=0
tmp_state="$(mktemp -d)"; trap 'rm -rf "$tmp_state"' EXIT
emit() { printf '{"tool_name":"Write","tool_input":{"file_path":"%s"},"cwd":"%s"}' "$1" "${2:-/tmp}"; }
run() { printf '%s' "$1" | env -u POLYFORGE_API_KEY -u POLYFORGE_AIHUB_URL "$bridge" 2>/dev/null; }
has() { case "$1" in *"$2"*) return 0;; *) return 1;; esac; }
ck()     { if has "$1" "$2"; then echo "  PASS: $3"; else echo "  FAIL: $3 (missing $2)" >&2; fails=$((fails+1)); fi; }
ck_empty(){ if [ -z "$1" ]; then echo "  PASS: $2"; else echo "  FAIL: $2 (expected empty)" >&2; fails=$((fails+1)); fi; }

echo "== path gating =="
ck "$(run "$(emit /x/docs/superpowers/specs/2026-01-01-foo-design.md)")" "methodology.spec" "flat spec path matches"
ck "$(run "$(emit /x/docs/superpowers/plans/sub/dir/p.md)")"            "methodology.plan" "NESTED plan path now matches (regex widened)"
ck_empty "$(run "$(emit /x/src/main.go)")"                              "non-superpowers file inert"
ck_empty "$(run "$(emit /x/mydocs/superpowers/specs/a.md)")"            "lookalike prefix still inert"

echo ""
echo "== deterministic save (dry-run) =="
sf="$tmp_state"; mkdir -p "$sf/.polyforge/state" "$sf/docs/superpowers/specs"
printf '{"wi_id":"wi_T","project":"aihub","attempt_id":"ra_T","claim_epoch":1,"session_secret":"s","claimed":true}\n' > "$sf/.polyforge/state/wi_T.json"
printf 'version: 1\n' > "$sf/.polyforge.yaml"
printf '# x design\nsome content\n' > "$sf/docs/superpowers/specs/x-design.md"
p="$(emit "$sf/docs/superpowers/specs/x-design.md" "$sf")"
o="$(printf '%s' "$p" | PF_BRIDGE_DRYRUN=1 POLYFORGE_AIHUB_URL=http://h POLYFORGE_API_KEY=k "$bridge" 2>/dev/null)"
ck "$o" "/v1/memories"     "dry-run shows POST target"
ck "$o" "methodology.spec" "dry-run payload has type"
ck "$o" "wi_T"             "dry-run payload has wi_id"

echo ""
echo "== fallback when creds missing =="
o="$(printf '%s' "$p" | env -u POLYFORGE_API_KEY -u POLYFORGE_AIHUB_URL "$bridge" 2>/dev/null)"
ck "$o" "pf_save_artifact" "no creds -> falls back to model reminder"

echo ""
[ "$fails" -eq 0 ] && { echo "ALL PASS"; exit 0; } || { echo "$fails FAILED" >&2; exit 1; }
