#!/usr/bin/env bash
# Regression gate for the plugin VERSION DRIFT self-check in hooks/pf-session-start
# (aihub#365).
#
# WHY THIS EXISTS
# ---------------
# "Published" is not "running". The plugin install cache keys on
# <cache>/<marketplace>/<plugin>/<version>, so versions COEXIST on disk, and a session binds
# to one at the instant it starts. `claude plugin update` rewrites
# ~/.claude/plugins/installed_plugins.json immediately but cannot move a session that is
# already bound. Measured 2026-09-05 on this machine: installed_plugins.json recorded 1.1.23
# while the live session was executing 1.1.22's skills, and BOTH version directories had live
# PIDs under .in_use. Nothing anywhere said so.
#
# aihub#302 gated "plugin contents changed => version must be bumped"; polyforge-scenario#9
# added the release step that was missing entirely. Of the three hops — change merged,
# released, IN EFFECT — the first two are gated and the third had nothing watching it. This
# suite watches the third.
#
# WHAT MAKES IT CHECKABLE AT ALL
# ------------------------------
# The running version is programmatically readable: the hook derives PF_PLUGIN_ROOT from its
# own $0, which names the copy the harness really executed, and the cache path carries the
# version. That is a fact about the process, not a string a model paraphrased.
#
# WHAT IS ASSERTED
#   1. Detect:   the real aihub#365 shape (running X, recorded Y, same cache family) puts a
#                banner in front of the model AND the full paths in `systemMessage`.
#   2. Silence:  four ways to have no drift — running the recorded version, running from a
#                source checkout, no installed_plugins.json, corrupt installed_plugins.json —
#                all say nothing. A checkout must not cry wolf at every contributor.
#   3. Scope:    a plugin recorded at two scopes does not self-report drift just because the
#                first entry scanned was the other one.
#   4. Manifest: the running version is read from .claude-plugin/plugin.json, NEVER from the
#                plugin.json at the tree root — that one is the Copilot/Codex manifest and has
#                shipped a stale version for real (cached 1.1.7 says "1.1.6" to this day).
#   5. Budget:   the ctx banner stays under its declared ceiling; a session firing BOTH this
#                banner and aihub#305's stale-binary banner is still under the 10,000-char
#                harness limit; and — because the ceiling is applied by SLICING — the banner
#                is still intact at the widest version strings the clamp allows. A ceiling
#                set to the typical size does not fail loudly, it quietly cuts the tail off,
#                and the tail is the sentence naming the remedy.
#   6. Degrade:  the warning survives aihub#293's over-budget rebuild — a session pinned to an
#                old plugin is a prime way to be over budget, so those two correlate.
#   7. Safety:   every arm exits 0 with parseable JSON. A SessionStart hook must never block
#                startup.
#   8. Controls: mutation controls for the banner, the criterion and the manifest choice. Each
#                proves the mutation actually applied first — a replacement that matches
#                nothing yields a green run that means nothing.
#
# Hermetic: no network, no docker. HOME and CLAUDE_PROJECT_DIR are redirected into a temp
# tree, so the developer's real plugin cache is never read and never written.
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
PLUGIN_SRC=$(cd "$here/.." && pwd)

fails=0
passes=0
pass() {
  passes=$((passes + 1))
  echo "PASS: $1"
}
fail() {
  fails=$((fails + 1))
  echo "FAIL: $1" >&2
}

# A missing python3 must not become a quiet pass: the hook no-ops without it, so every
# assertion below would vacuously "not find the banner". Fail loudly instead.
if ! command -v python3 > /dev/null 2>&1; then
  echo "FAIL: python3 is required — hooks/pf-session-start no-ops without it, which would make every assertion below vacuous" >&2
  exit 1
fi

BANNER_MARK='THIS SESSION IS RUNNING AN OLDER POLYFORGE THAN THE ONE INSTALLED'
SYSMSG_MARK='polyforge version drift: this session is running'
# Kept in sync with VERSION_CTX_MAX in hooks/pf-session-start. Asserted, not assumed.
VERSION_CTX_MAX=560
HARNESS_MAX=10000

TMPROOT=$(mktemp -d "${TMPDIR:-/tmp}/pf365.XXXXXX")
cleanup() { rm -rf "$TMPROOT"; }
trap cleanup EXIT

MARKETPLACE=ieops-aihub

# ── world builder ────────────────────────────────────────────────────────────
new_world() { # echoes a fresh world dir
  local W
  W=$(mktemp -d "$TMPROOT/w.XXXXXX")
  mkdir -p "$W/home/.claude/plugins" "$W/ws"
  : > "$W/ws/.polyforge.yaml"
  echo "$W"
}

cache_family() { echo "$1/home/.claude/plugins/cache/$MARKETPLACE/polyforge"; }

# Install a copy of the plugin tree as version $2 under $1's cache, stamping the version
# into .claude-plugin/plugin.json (what Claude Code reads) and, separately, into the root
# plugin.json (the Copilot/Codex manifest) so the two can be made to disagree on purpose.
add_plugin() { # $1 world, $2 version, [$3 root-plugin.json version override], [$4 src tree]
  local W="$1" v="$2" rootv="${3:-$2}" src="${4:-$PLUGIN_SRC}" dest
  dest="$(cache_family "$W")/$v"
  mkdir -p "$(dirname "$dest")"
  cp -R "$src" "$dest"
  V="$v" ROOTV="$rootv" python3 - "$dest" <<'PY'
import json, os, sys
d = sys.argv[1]
for rel, key in ((os.path.join(".claude-plugin", "plugin.json"), "V"),
                 ("plugin.json", "ROOTV")):
    p = os.path.join(d, rel)
    try:
        with open(p, encoding="utf-8") as fh:
            data = json.load(fh)
    except (OSError, ValueError):
        data = {"name": "polyforge"}
    data["version"] = os.environ[key]
    os.makedirs(os.path.dirname(p), exist_ok=True)
    with open(p, "w", encoding="utf-8") as fh:
        json.dump(data, fh)
PY
  echo "$dest"
}

write_installed() { # $1 world, $2 raw file body ("" = do not create the file)
  local W="$1" body="$2" p
  p="$W/home/.claude/plugins/installed_plugins.json"
  [ -z "$body" ] && { rm -f "$p"; return; }
  printf '%s' "$body" > "$p"
}

# A normal installed_plugins.json recording ONE install.
installed_json() { # $1 installPath, $2 version
  printf '{"version":2,"plugins":{"polyforge@%s":[{"scope":"user","installPath":"%s","version":"%s"}]}}' \
    "$MARKETPLACE" "$1" "$2"
}

run_hook() { # $1 world, $2 plugin root to execute
  local W="$1" root="$2"
  env -u CURSOR_PLUGIN_ROOT -u PLUGIN_ROOT \
    HOME="$W/home" CLAUDE_PROJECT_DIR="$W/ws" \
    "$root/hooks/pf-session-start" > "$W/hook.json" 2> "$W/hook.err"
  echo "$?" > "$W/hook.rc"
  python3 - "$W" <<'PY'
import json, os, sys
w = sys.argv[1]
raw = open(os.path.join(w, "hook.json"), encoding="utf-8").read()
ok = "yes"
try:
    d = json.loads(raw)
except Exception:
    d, ok = {}, "no"
ctx = (d.get("hookSpecificOutput") or {}).get("additionalContext", "")
for name, val in (("hook.ctx", ctx),
                  ("hook.sysmsg", d.get("systemMessage", "<absent>")),
                  ("hook.ctxlen", str(len(ctx))),
                  ("hook.json_ok", ok)):
    open(os.path.join(w, name), "w", encoding="utf-8").write(val)
PY
}

has() { grep -qF -- "$2" "$1" 2> /dev/null; }

# Every arm must leave a startable session behind, whatever it decided to say.
assert_safe() { # $1 label, $2 world
  local W="$2"
  [ "$(cat "$W/hook.rc")" = 0 ] \
    && pass "$1 -> hook exited 0 (never blocks startup)" \
    || fail "$1 -> hook exited $(cat "$W/hook.rc"); a SessionStart hook must never block startup"
  [ "$(cat "$W/hook.json_ok")" = yes ] \
    && pass "$1 -> hook emitted parseable JSON" \
    || fail "$1 -> hook emitted unparseable output: $(head -c 200 "$W/hook.json")"
}

assert_silent() { # $1 label, $2 world
  local W="$2"
  has "$W/hook.ctx" "$BANNER_MARK" \
    && fail "$1 -> a drift banner was emitted where there is no drift (this cries wolf)" \
    || pass "$1 -> no drift banner, as a session with no drift requires"
  has "$W/hook.sysmsg" "$SYSMSG_MARK" \
    && fail "$1 -> systemMessage claimed drift where there is none" \
    || pass "$1 -> systemMessage stays clean"
}

# ── mutation helper ──────────────────────────────────────────────────────────
# Copy the plugin, break ONE mechanism, prove the copy really differs. Without the
# "really differs" assertion a replacement that matched nothing produces a green run
# meaning nothing, so a no-op mutation is a FAIL and not a skip.
MUT_DIR=""
mutate() { # $1 label, $2 relpath under the plugin, $3 literal find, $4 replace
  local label="$1" rel="$2" C
  MUT_DIR=""
  C=$(mktemp -d "$TMPROOT/mut.XXXXXX")
  cp -R "$PLUGIN_SRC/." "$C/"
  FIND="$3" REPL="$4" python3 - "$C/$rel" <<'PY'
import os, sys
p = sys.argv[1]
s = open(p, encoding="utf-8").read()
open(p, "w", encoding="utf-8").write(s.replace(os.environ["FIND"], os.environ["REPL"]))
PY
  if cmp -s "$PLUGIN_SRC/$rel" "$C/$rel"; then
    fail "$label -> MUTATION DID NOT APPLY: $rel is byte-identical after the replacement, so anything this control reports is meaningless"
    return
  fi
  pass "$label -> mutation applied ($rel really changed)"
  MUT_DIR="$C"
}

# ═════════════════════════════════════════════════════════════════════════════
echo "== 1. the aihub#365 shape: bound to 1.1.22 while installed_plugins.json records 1.1.23 =="
W1=$(new_world)
OLD1=$(add_plugin "$W1" 1.1.22)
NEW1=$(add_plugin "$W1" 1.1.23)
write_installed "$W1" "$(installed_json "$NEW1" 1.1.23)"
run_hook "$W1" "$OLD1"
assert_safe "drift" "$W1"
has "$W1/hook.ctx" "$BANNER_MARK" \
  && pass "drift -> SessionStart puts the version warning in front of the model" \
  || fail "drift -> the model is told nothing; this is the aihub#365 defect, unfixed"
has "$W1/hook.ctx" 'Bound at session start: **1.1.22**' \
  && pass "drift -> banner names the RUNNING version (1.1.22)" \
  || fail "drift -> banner does not name the running version"
has "$W1/hook.ctx" '`installed_plugins.json` records: **1.1.23**' \
  && pass "drift -> banner names the RECORDED version (1.1.23)" \
  || fail "drift -> banner does not name the recorded version"
has "$W1/hook.ctx" '/reload-plugins' \
  && pass "drift -> banner tells the reader how to pick the new version up" \
  || fail "drift -> banner states a problem with no remedy"
has "$W1/hook.sysmsg" "$SYSMSG_MARK" \
  && pass "drift -> systemMessage carries the full prose (costs the ctx budget nothing)" \
  || fail "drift -> systemMessage absent; the human-facing channel is silent"
has "$W1/hook.sysmsg" "$OLD1" && has "$W1/hook.sysmsg" "$NEW1" \
  && pass "drift -> systemMessage names BOTH absolute paths, so the claim is checkable" \
  || fail "drift -> systemMessage omits a path; the reader cannot verify or locate the drift"
# aihub#291's second failure was a banner that only ever landed past the truncation point:
# it turned an intact payload into a truncated one and still delivered none of itself.
if python3 - "$W1/hook.ctx" "$BANNER_MARK" <<'PY'
import sys
ctx = open(sys.argv[1], encoding="utf-8").read()
banner, lede = ctx.find(sys.argv[2]), ctx.find("**Below is")
sys.exit(0 if 0 <= banner < lede else 1)
PY
then
  pass "drift -> banner sits in the PREAMBLE, ahead of the fragment body (aihub#291)"
else
  fail "drift -> banner is not ahead of the fragment body; under truncation it would deliver none of itself"
fi

echo
echo "== 2. reverse: bound to the version installed_plugins.json records =="
W2=$(new_world)
add_plugin "$W2" 1.1.22 > /dev/null
CUR2=$(add_plugin "$W2" 1.1.23)
write_installed "$W2" "$(installed_json "$CUR2" 1.1.23)"
run_hook "$W2" "$CUR2"
assert_safe "reverse" "$W2"
assert_silent "reverse" "$W2"

echo
echo "== 3. a source checkout is not drift (same json as arm 1, plugin root moved) =="
# The ONLY difference from arm 1 is where the plugin being executed lives. If this warned,
# every contributor running the tree from git would be told their session is stale.
W3=$(new_world)
NEW3=$(add_plugin "$W3" 1.1.23)
write_installed "$W3" "$(installed_json "$NEW3" 1.1.23)"
run_hook "$W3" "$PLUGIN_SRC"
assert_safe "checkout" "$W3"
assert_silent "checkout" "$W3"

echo
echo "== 4. no installed_plugins.json at all (Codex, Copilot, a fresh box) =="
W4=$(new_world)
OLD4=$(add_plugin "$W4" 1.1.22)
add_plugin "$W4" 1.1.23 > /dev/null
write_installed "$W4" ""
run_hook "$W4" "$OLD4"
assert_safe "no-manifest" "$W4"
assert_silent "no-manifest" "$W4"

echo
echo "== 5. corrupt installed_plugins.json =="
W5=$(new_world)
OLD5=$(add_plugin "$W5" 1.1.22)
add_plugin "$W5" 1.1.23 > /dev/null
write_installed "$W5" '{"version":2,"plugins":{ this is not json'
run_hook "$W5" "$OLD5"
assert_safe "corrupt-manifest" "$W5"
assert_silent "corrupt-manifest" "$W5"

echo
echo "== 6. installed at two scopes, running one of them =="
# The running root matches the SECOND entry. A loop that returned on the first sibling it
# saw would report drift against the machine's own current install.
W6=$(new_world)
A6=$(add_plugin "$W6" 1.1.22)
B6=$(add_plugin "$W6" 1.1.23)
write_installed "$W6" "$(printf '{"version":2,"plugins":{"polyforge@%s":[{"scope":"project","installPath":"%s","version":"1.1.22"},{"scope":"user","installPath":"%s","version":"1.1.23"}]}}' "$MARKETPLACE" "$A6" "$B6")"
run_hook "$W6" "$B6"
assert_safe "two-scopes" "$W6"
assert_silent "two-scopes" "$W6"

echo
echo "== 7. the stale root plugin.json trap =="
# Cached polyforge 1.1.7 ships a root plugin.json saying "1.1.6" while
# .claude-plugin/plugin.json says "1.1.7" — Claude Code reads the latter. Reading the root
# manifest would make the hook report a version this session is NOT running.
W7=$(new_world)
OLD7=$(add_plugin "$W7" 1.1.22 1.1.6)
NEW7=$(add_plugin "$W7" 1.1.23)
write_installed "$W7" "$(installed_json "$NEW7" 1.1.23)"
run_hook "$W7" "$OLD7"
assert_safe "stale-root-manifest" "$W7"
has "$W7/hook.ctx" 'Bound at session start: **1.1.22**' \
  && pass "stale-root-manifest -> running version read from .claude-plugin/plugin.json" \
  || fail "stale-root-manifest -> banner did not report 1.1.22; the root plugin.json leaked in"
has "$W7/hook.ctx" '**1.1.6**' \
  && fail "stale-root-manifest -> banner reported 1.1.6, the Copilot/Codex manifest's stale value" \
  || pass "stale-root-manifest -> the stale root manifest value never reaches the model"

echo
echo "== 8. budget =="
BASE=$(cat "$W2/hook.ctxlen")
DRIFTED=$(cat "$W1/hook.ctxlen")
COST=$((DRIFTED - BASE))
[ "$COST" -le "$VERSION_CTX_MAX" ] \
  && pass "budget -> the warning costs $COST chars, within its declared $VERSION_CTX_MAX ceiling" \
  || fail "budget -> the warning costs $COST chars, over its declared $VERSION_CTX_MAX ceiling"
[ "$BASE" -eq "$(cat "$W3/hook.ctxlen")" ] \
  && pass "budget -> a session with no drift pays exactly zero" \
  || fail "budget -> a no-drift session's payload changed size ($BASE vs $(cat "$W3/hook.ctxlen"))"
# Worst case: this banner AND aihub#305's stale-binary banner in the same session.
W8=$(new_world)
OLD8=$(add_plugin "$W8" 1.1.22)
NEW8=$(add_plugin "$W8" 1.1.23)
write_installed "$W8" "$(installed_json "$NEW8" 1.1.23)"
mkdir -p "$W8/home/.polyforge"
printf 'THIS MACHINE IS NOT RUNNING THE BINARY IT SHOULD BE: fixture\n' \
  > "$W8/home/.polyforge/binary-status.txt"
run_hook "$W8" "$OLD8"
assert_safe "both-banners" "$W8"
has "$W8/hook.ctx" "$BANNER_MARK" && has "$W8/hook.ctx" 'POLYFORGE IS RUNNING A BINARY IT DID NOT DOWNLOAD' \
  && pass "both-banners -> the version warning and aihub#305's stale-binary warning coexist" \
  || fail "both-banners -> one warning displaced the other"
has "$W8/hook.sysmsg" "$SYSMSG_MARK" && has "$W8/hook.sysmsg" 'THIS MACHINE IS NOT RUNNING THE BINARY' \
  && pass "both-banners -> systemMessage carries BOTH messages (neither assignment clobbers the other)" \
  || fail "both-banners -> systemMessage dropped one of the two warnings"
BOTH=$(cat "$W8/hook.ctxlen")
[ "$BOTH" -le "$HARNESS_MAX" ] \
  && pass "both-banners -> worst-case payload is $BOTH chars, under the $HARNESS_MAX harness limit" \
  || fail "both-banners -> worst-case payload is $BOTH chars, over the $HARNESS_MAX harness limit"

# The ceiling is applied by slicing, so setting it to the TYPICAL banner size rather than the
# worst case does not fail loudly — it quietly cuts the tail off, and the tail is the sentence
# that names the remedy. Version strings are clamped at 32 chars each, so the worst case is
# ~64 chars wider than the "1.1.22"/"1.1.23" pair every other arm here uses. Drive it.
LONGV=1.1.24-rc.1+build.20260905key
W8b=$(new_world)
OLD8b=$(add_plugin "$W8b" "$LONGV")
NEW8b=$(add_plugin "$W8b" "1.1.25-rc.9+build.20260906aaa")
write_installed "$W8b" "$(installed_json "$NEW8b" "1.1.25-rc.9+build.20260906aaa")"
run_hook "$W8b" "$OLD8b"
assert_safe "long-versions" "$W8b"
has "$W8b/hook.ctx" 'is what picks the new one up.' \
  && pass "long-versions -> the remedy sentence survives the ceiling (it is not sliced off)" \
  || fail "long-versions -> the ceiling truncated the banner and ate the remedy sentence"
has "$W8b/hook.ctx" "$LONGV" \
  && pass "long-versions -> the running version is reported in full" \
  || fail "long-versions -> the running version was mangled"
LONGCOST=$(( $(cat "$W8b/hook.ctxlen") - BASE ))
[ "$LONGCOST" -le "$VERSION_CTX_MAX" ] \
  && pass "long-versions -> worst-case banner is $LONGCOST chars, still within the $VERSION_CTX_MAX ceiling" \
  || fail "long-versions -> worst-case banner is $LONGCOST chars, over the $VERSION_CTX_MAX ceiling"

echo
echo "== 9. the warning survives the hook's own over-budget degrade path =="
# aihub#293 rebuilds ctx from scratch when it exceeds MAX_CHARS. That rebuild is a SECOND
# assembly expression; aihub#305 shipped a first draft that updated only the first one.
# A session pinned to an OLD plugin is a prime way to be over budget, so "degraded" and
# "stale version" correlate — dropping the banner here would blind exactly the population
# it is for. Forcing MAX_CHARS down is cheaper and more direct than busting a real budget.
mutate "degrade-path" "hooks/pf-session-start" 'MAX_CHARS = 10000' 'MAX_CHARS = 3000'
C9="$MUT_DIR"
if [ -n "$C9" ]; then
  W9=$(new_world)
  OLD9=$(add_plugin "$W9" 1.1.22 1.1.22 "$C9")
  NEW9=$(add_plugin "$W9" 1.1.23)
  write_installed "$W9" "$(installed_json "$NEW9" 1.1.23)"
  run_hook "$W9" "$OLD9"
  has "$W9/hook.ctx" 'POLYFORGE SESSION-START PAYLOAD OVER BUDGET' \
    && pass "degrade-path -> the forced-degrade fixture really took the degrade path" \
    || fail "degrade-path -> the fixture did not degrade, so the assertion below would be vacuous"
  has "$W9/hook.ctx" "$BANNER_MARK" \
    && pass "degrade-path -> the version warning survives being rebuilt under budget pressure" \
    || fail "degrade-path -> degrading dropped the version warning; a session that is both over budget and stale would be told nothing"
fi

echo
echo "== 10. negative control: hook stops assembling the version banner =="
mutate "nc/banner" "hooks/pf-session-start" \
  'ctx = PREAMBLE + STATUS_BANNER + VERSION_BANNER + LEDE' \
  'ctx = PREAMBLE + STATUS_BANNER + LEDE'
C10="$MUT_DIR"
if [ -n "$C10" ]; then
  W10=$(new_world)
  OLD10=$(add_plugin "$W10" 1.1.22 1.1.22 "$C10")
  NEW10=$(add_plugin "$W10" 1.1.23)
  write_installed "$W10" "$(installed_json "$NEW10" 1.1.23)"
  run_hook "$W10" "$OLD10"
  has "$W10/hook.ctx" "$BANNER_MARK" \
    && fail "nc/banner -> the banner survived its own removal; section 1 proves nothing" \
    || pass "nc/banner -> with the banner removed the model is told nothing (section 1 discriminates)"
fi

echo
echo "== 11. negative control: the running version is read from the root plugin.json =="
# Reproduces exactly the inference the 1.1.7 cache would have invited. Section 7 must go
# dark when the hook reads the wrong manifest.
mutate "nc/root-manifest" "hooks/pf-session-start" \
  'raw = read(os.path.join(root, ".claude-plugin", "plugin.json"))' \
  'raw = read(os.path.join(root, "plugin.json"))'
C11="$MUT_DIR"
if [ -n "$C11" ]; then
  W11=$(new_world)
  OLD11=$(add_plugin "$W11" 1.1.22 1.1.6 "$C11")
  NEW11=$(add_plugin "$W11" 1.1.23)
  write_installed "$W11" "$(installed_json "$NEW11" 1.1.23)"
  run_hook "$W11" "$OLD11"
  has "$W11/hook.ctx" '**1.1.6**' \
    && pass "nc/root-manifest -> reading the root manifest really does report the stale 1.1.6 (section 7 discriminates)" \
    || fail "nc/root-manifest -> the mutant reported the right version anyway; section 7 proves nothing"
fi

echo
echo "== 12. negative control: the criterion stops scoping to the cache family =="
# Drop the sibling test so ANY recorded install that is not the running root counts as
# drift. Section 3 (source checkout) must go dark.
mutate "nc/family-scope" "hooks/pf-session-start" \
  'if sibling is None and os.path.dirname(p) == family:' \
  'if sibling is None:'
C12="$MUT_DIR"
if [ -n "$C12" ]; then
  W12=$(new_world)
  NEW12=$(add_plugin "$W12" 1.1.23)
  write_installed "$W12" "$(installed_json "$NEW12" 1.1.23)"
  run_hook "$W12" "$C12"
  has "$W12/hook.ctx" "$BANNER_MARK" \
    && pass "nc/family-scope -> without the family scope a source checkout DOES cry wolf (section 3 discriminates)" \
    || fail "nc/family-scope -> the mutant stayed silent; section 3 proves nothing"
fi

echo
echo "──────────────────────────────────────────────"
echo "passes=$passes fails=$fails"
[ "$fails" -eq 0 ] || exit 1
exit 0
