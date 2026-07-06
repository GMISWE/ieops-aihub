#!/usr/bin/env bash
# Test suite for bin/pf-seam-check (superpowers drift probe).
#
# Builds a fake plugin cache dir + fake HOME settings and drives the probe via
# PF_SEAM_CACHE_DIR + HOME, asserting on stdout. Modeled on the fake-HOME + assert-helper
# pattern in tests/pf-skill-router.test.sh.

set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
probe="$here/../bin/pf-seam-check"
[ -x "$probe" ] || { echo "FAIL: probe not executable at $probe" >&2; exit 1; }

fails=0
has()     { case "$1" in *"$2"*) return 0;; *) return 1;; esac; }
ck()      { if has "$1" "$2"; then echo "  PASS: $3"; else echo "  FAIL: $3 (missing: $2)" >&2; fails=$((fails+1)); fi; }
ck_not()  { if has "$1" "$2"; then echo "  FAIL: $3 (unexpected: $2)" >&2; fails=$((fails+1)); else echo "  PASS: $3"; fi; }

names=(brainstorming writing-plans subagent-driven-development executing-plans finishing-a-development-branch)

# make_fake_cache <dir> builds a green (all-correct) fake superpowers 6.1.1 cache under <dir>/mkt/superpowers/6.1.1/skills/
make_fake_cache() {
  local root="$1" skdir
  skdir="$root/mkt/superpowers/6.1.1/skills"
  mkdir -p "$root/home/.claude"
  for n in "${names[@]}"; do
    mkdir -p "$skdir/$n"
    printf -- "---\nname: %s\n---\n\nBody text.\n" "$n" > "$skdir/$n/SKILL.md"
  done
  {
    echo "See docs/superpowers/specs/<date>-<topic>-design.md for output."
  } >> "$skdir/brainstorming/SKILL.md"
  {
    echo "See docs/superpowers/plans/<date>-<feature>.md for output."
  } >> "$skdir/writing-plans/SKILL.md"
  printf '{"enabledPlugins":{"superpowers@mkt":true}}\n' > "$root/home/.claude/settings.json"
}

run_probe() { # cache_root, home -> stdout
  # PF_SEAM_WS="" + cwd under the throwaway cache dir so Probe C's ws auto-detect can't
  # walk up into a real .polyforge.yaml workspace and scan its settings.
  ( cd "$1" && PF_SEAM_CACHE_DIR="$1" PF_SEAM_WS="" HOME="$2" "$probe" 2>&1 )
}

echo "== green case: all seams match pinned 6.1.1 =="
tmp="$(mktemp -d)"
make_fake_cache "$tmp"
o="$(run_probe "$tmp" "$tmp/home")"
ck_not "$o" "[WARN]" "no warnings on a fully-matching fake cache"
ck "$o" "all seams verified against superpowers 6.1.1" "summary line reports all-clear"
rm -rf "$tmp"

echo "== drift (i): rename a skill dir =="
tmp="$(mktemp -d)"
make_fake_cache "$tmp"
mv "$tmp/mkt/superpowers/6.1.1/skills/executing-plans" "$tmp/mkt/superpowers/6.1.1/skills/executing-plans-renamed"
o="$(run_probe "$tmp" "$tmp/home")"
ck "$o" "[WARN]" "renamed skill dir produces a warning"
rm -rf "$tmp"

echo "== drift (ii): change a name: frontmatter value =="
tmp="$(mktemp -d)"
make_fake_cache "$tmp"
printf -- "---\nname: brainstorming-wrong\n---\n\nSee docs/superpowers/specs/ for output.\n" \
  > "$tmp/mkt/superpowers/6.1.1/skills/brainstorming/SKILL.md"
o="$(run_probe "$tmp" "$tmp/home")"
ck "$o" "[WARN]" "mismatched frontmatter name produces a warning"
rm -rf "$tmp"

echo "== drift (iii): add a higher version dir =="
tmp="$(mktemp -d)"
make_fake_cache "$tmp"
cp -r "$tmp/mkt/superpowers/6.1.1" "$tmp/mkt/superpowers/7.0.0"
o="$(run_probe "$tmp" "$tmp/home")"
ck "$o" "[WARN]" "newer version present produces a warning"
ck "$o" "7.0.0" "warning names the found version"
rm -rf "$tmp"

echo "== drift (iv): remove docs/superpowers/specs/ from brainstorming =="
tmp="$(mktemp -d)"
make_fake_cache "$tmp"
printf -- "---\nname: brainstorming\n---\n\nNo output path mentioned here.\n" \
  > "$tmp/mkt/superpowers/6.1.1/skills/brainstorming/SKILL.md"
o="$(run_probe "$tmp" "$tmp/home")"
ck "$o" "[WARN]" "missing output path string produces a warning"
rm -rf "$tmp"

echo "== drift (v): settings with no superpowers@ key =="
tmp="$(mktemp -d)"
make_fake_cache "$tmp"
printf '{"enabledPlugins":{"other@mkt":true}}\n' > "$tmp/home/.claude/settings.json"
o="$(run_probe "$tmp" "$tmp/home")"
ck "$o" "[WARN]" "absent superpowers@ prefix produces a warning"
rm -rf "$tmp"

echo "== superpowers@ key ONLY in the ws layer (empty HOME) =="
tmp="$(mktemp -d)"
make_fake_cache "$tmp"
: > "$tmp/home/.claude/settings.json"   # empty HOME layer (no enabledPlugins)
mkdir -p "$tmp/ws/.claude"
printf '{"enabledPlugins":{"superpowers@claude-plugins-official":true}}\n' > "$tmp/ws/.claude/settings.json"
o="$(PF_SEAM_CACHE_DIR="$tmp" PF_SEAM_WS="$tmp/ws" HOME="$tmp/home" "$probe" 2>&1)"
ck "$o" "[ok] settings prefix" "ws-layer superpowers@ key is detected (router parity)"
ck_not "$o" "[WARN] settings prefix" "no false-warn when key lives only in the ws layer"
rm -rf "$tmp"

echo "== no superpowers cache at all =="
tmp="$(mktemp -d)"
mkdir -p "$tmp/empty" "$tmp/home/.claude"
o="$(run_probe "$tmp/empty" "$tmp/home")"
ck "$o" "[WARN]" "empty cache produces a warning"
ck "$o" "superpowers plugin not found in cache" "empty cache names the reason"
rm -rf "$tmp"

echo "== probe always exits 0 =="
tmp="$(mktemp -d)"
make_fake_cache "$tmp"
mv "$tmp/mkt/superpowers/6.1.1/skills/executing-plans" "$tmp/mkt/superpowers/6.1.1/skills/executing-plans-renamed"
PF_SEAM_CACHE_DIR="$tmp" HOME="$tmp/home" "$probe" >/dev/null 2>&1
rc=$?
if [ "$rc" -eq 0 ]; then echo "  PASS: exit 0 even with warnings"; else echo "  FAIL: exit 0 even with warnings (got $rc)" >&2; fails=$((fails+1)); fi
rm -rf "$tmp"

echo
if [ "$fails" -eq 0 ]; then
  echo "ALL PASS"
  exit 0
else
  echo "$fails CHECK(S) FAILED" >&2
  exit 1
fi
