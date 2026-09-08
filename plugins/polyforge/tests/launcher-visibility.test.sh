#!/usr/bin/env bash
# Regression guard for the VISIBILITY half of aihub#305.
#
# THE BUG
# -------
# Every path in bin/polyforge-mcp.sh that failed to get the binary this plugin
# release expects ended at one line on stderr, and the main flow additionally
# ran its first attempt as `download_binary 2>/dev/null`, discarding even that.
# It then symlinked whatever `polyforge` it found on PATH and carried on. An MCP
# server's stderr is written to a debug log and never to the transcript, so the
# outcome was: the session starts, every tool works, and the machine is running
# an older binary than the plugin around it — with nothing anywhere saying so.
# Since every fix under internal/mcp/** and internal/cli/** ships as that
# binary, "older" means those fixes reached nobody, and the operator believed
# they had updated.
#
# TWO INDEPENDENT CAUSES, and this is why the channel fix (PR#279) did not close
# the wi: (1) the default channel pointed at a branch that was never published,
# now fixed; (2) `download_binary` needs `gh auth token` for private-repo raw
# access, so anyone without gh, or not logged in, still fails. Cause 2 is
# untouched by the channel fix. Both are covered below, separately, because a
# suite that only covered cause 1 would have gone green on the half-fix.
#
# WHAT IS ASSERTED
# ----------------
# The fix is a durable marker (~/.polyforge/binary-status.txt) written by the
# launcher on every "did not get it" path and DELETED on success, relayed to the
# session by hooks/pf-session-start. So each arm drives the REAL launcher end to
# end under stubs and then drives the REAL hook against the HOME it left behind.
# Reading the launcher's source, or the hook's, would prove nothing: the defect
# was that two components agreed to say nothing to each other.
#
# WHY THE MUTATION CONTROLS EXIST
# -------------------------------
# Sections 5-7 re-run the same arms against a scratch COPY of the plugin with
# the visibility mechanism deliberately broken, and pass only when the arm goes
# dark. Each one first asserts the mutated file actually differs from the
# original: a sed/replace that silently matches nothing produces a green run
# that means nothing, and the mutations here reproduce precisely what main did
# before this change (`2>/dev/null` restored, no marker written, no banner
# assembled) — so "the suite fails on main and passes here" is exactly what
# sections 5-7 measure, on every run, without needing a git checkout.
#
# Hermetic: no network, no docker. Stubs gh and curl, and pins
# POLYFORGE_SYSTEM_BIN_DIR so driving the real launcher cannot repoint the
# developer's own /usr/local/bin/polyforge (which is a live symlink into the
# installed plugin).
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
PLUGIN_SRC=$(cd "$here/.." && pwd)

fails=0
passes=0
pass() { passes=$((passes + 1)); echo "PASS: $1"; }
fail() { fails=$((fails + 1)); echo "FAIL: $1" >&2; }

# A missing python3 must not become a quiet pass: the hook no-ops without it, so
# every hook assertion below would vacuously "not find the banner". Fail loudly.
if ! command -v python3 > /dev/null 2>&1; then
  echo "FAIL: python3 is required — hooks/pf-session-start no-ops without it, which would make every relay assertion below vacuous" >&2
  exit 1
fi

MARKER_HEAD='THIS MACHINE IS NOT RUNNING THE BINARY IT SHOULD BE'
BANNER_MARK='POLYFORGE IS RUNNING A BINARY IT DID NOT DOWNLOAD'
PATHBIN_SHA=cccccccccccccccccccccccccccccccccccccccc
STALE_SHA=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
PUB_SHA=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb

TMPROOT=$(mktemp -d "${TMPDIR:-/tmp}/pfvis.XXXXXX")
cleanup() { rm -rf "$TMPROOT"; }
trap cleanup EXIT

# ── world builder ────────────────────────────────────────────────────────────
# Builds an isolated HOME + plugin root + stub PATH, runs the launcher for real,
# and leaves everything on disk for the caller to assert against.
#
#   $1 plugin root to drive (the real tree, or a mutated copy)
#   $2 config.toml body, printf %b; empty means no config.toml at all
#   $3 gh          : ok | unauth | missing
#   $4 download    : ok | fail
#   $5 pathbin     : yes | no      (is there a `polyforge` on PATH to degrade to)
#   $6 installed   : none | stale  (is there already a real binary in plugin/bin)
#   $7 premark     : yes | no      (seed a stale marker before the run)
#
# Echoes the world directory. $W/rc holds the launcher's exit code.
build_world() {
  local plugin_src="$1" body="$2" ghmode="$3" dlmode="$4" pathbin="$5" installed="$6" premark="$7"
  local W t tp
  W=$(mktemp -d "$TMPROOT/w.XXXXXX")
  mkdir -p "$W/home/.polyforge" "$W/stubs/min" "$W/sysbin" "$W/plugin"

  # Copy only what the launcher needs; the hook is driven from $plugin_src
  # directly so it keeps its real skills/ tree next door.
  cp -R "$plugin_src/bin" "$W/plugin/bin"
  rm -f "$W/plugin/bin/polyforge"

  [ -n "$body" ] && printf '%b' "$body" > "$W/home/.polyforge/config.toml"
  if [ "$premark" = yes ]; then
    printf 'stale marker from an earlier failed launch\n' > "$W/home/.polyforge/binary-status.txt"
  fi

  # Sanitised PATH, not a prepend: with the real PATH on the end a "missing"
  # stub is silently satisfied by the real tool, and the gh-missing arm would
  # then be testing the developer's own gh login.
  # `mv` is load-bearing: download_binary renames its temp into place, and
  # leaving mv out makes every download "fail" for a reason that has nothing to
  # do with the code under test.
  for t in bash sh env date dirname cat grep head mktemp uname sed tr find awk chmod mkdir mv rm ln; do
    tp=$(command -v "$t" 2> /dev/null) && ln -sf "$tp" "$W/stubs/min/$t"
  done

  case "$ghmode" in
    ok)      printf '#!/usr/bin/env bash\n[ "$1" = auth ] && [ "$2" = token ] && { echo tok; exit 0; }\nexit 0\n' > "$W/stubs/gh"; chmod +x "$W/stubs/gh" ;;
    unauth)  printf '#!/usr/bin/env bash\nexit 1\n' > "$W/stubs/gh"; chmod +x "$W/stubs/gh" ;;
    missing) : ;;
  esac

  if [ "$dlmode" = ok ]; then
    cat > "$W/stubs/curl" <<EOF
#!/usr/bin/env bash
is_ver=no
prev=""
for a in "\$@"; do
  case "\$a" in
    *version.txt) is_ver=yes ;;
  esac
  [ "\$prev" = "-o" ] && printf '#!/usr/bin/env bash\nexit 0\n' > "\$a"
  prev="\$a"
done
[ "\$is_ver" = yes ] && printf '%s\n' '$PUB_SHA'
exit 0
EOF
  else
    # 22 is curl's own "HTTP error returned" code, which is what a 404 on an
    # unpublished bins branch actually produces under -f.
    printf '#!/usr/bin/env bash\nexit 22\n' > "$W/stubs/curl"
  fi
  chmod +x "$W/stubs/curl"

  if [ "$pathbin" = yes ]; then
    printf '#!/usr/bin/env bash\n[ "${1:-}" = version ] && echo "polyforge v1 (%s) built then"\nexit 0\n' \
      "$PATHBIN_SHA" > "$W/stubs/polyforge"
    chmod +x "$W/stubs/polyforge"
  fi

  if [ "$installed" = stale ]; then
    printf '#!/usr/bin/env bash\n[ "${1:-}" = version ] && echo "polyforge v1 (%s) built then"\nexit 0\n' \
      "$STALE_SHA" > "$W/plugin/bin/polyforge"
    chmod +x "$W/plugin/bin/polyforge"
  fi

  (
    export HOME="$W/home" PATH="$W/stubs:$W/stubs/min"
    export CLAUDE_PLUGIN_ROOT="$W/plugin"
    export POLYFORGE_SYSTEM_BIN_DIR="$W/sysbin"
    "$W/plugin/bin/polyforge-mcp.sh" > "$W/out" 2> "$W/err"
  )
  printf '%s\n' "$?" > "$W/rc"
  printf '%s\n' "$W"
}

# Drive the REAL SessionStart hook against a world's HOME and report what it
# would put in front of the session. Emits three files next to the world:
#   $W/hook.json  raw stdout    $W/hook.ctx  additionalContext
#   $W/hook.sysmsg  systemMessage ("<absent>" when the key is not emitted)
run_hook() { # $1 = plugin root to drive, $2 = world
  local plugin_src="$1" W="$2" ws
  ws="$W/ws"
  mkdir -p "$ws"
  : > "$ws/.polyforge.yaml"
  env -u CURSOR_PLUGIN_ROOT -u PLUGIN_ROOT \
      HOME="$W/home" CLAUDE_PROJECT_DIR="$ws" \
      "$plugin_src/hooks/pf-session-start" > "$W/hook.json" 2> "$W/hook.err"
  python3 - "$W" <<'PY'
import json, os, sys
w = sys.argv[1]
raw = open(os.path.join(w, "hook.json"), encoding="utf-8").read()
try:
    d = json.loads(raw)
except Exception:
    d = {}
ctx = (d.get("hookSpecificOutput") or {}).get("additionalContext", "")
open(os.path.join(w, "hook.ctx"), "w", encoding="utf-8").write(ctx)
open(os.path.join(w, "hook.sysmsg"), "w", encoding="utf-8").write(
    d["systemMessage"] if "systemMessage" in d else "<absent>")
open(os.path.join(w, "hook.ctxlen"), "w", encoding="utf-8").write(str(len(ctx)))
PY
}

has() { grep -qF -- "$2" "$1" 2> /dev/null; }

# Assert the marker exists and the session relays it. Shared by every failing
# arm, so that "visible" means the same thing for all of them.
assert_visible() { # $1 label, $2 world, $3 substring the cause must contain, $4 plugin root
  local label="$1" W="$2" want="$3" plugin_src="$4"
  local mark="$W/home/.polyforge/binary-status.txt"

  if [ -s "$mark" ]; then
    pass "$label -> launcher recorded ~/.polyforge/binary-status.txt"
  else
    fail "$label -> no ~/.polyforge/binary-status.txt was written; the failure is still invisible"
    return
  fi
  has "$mark" "$MARKER_HEAD" \
    && pass "$label -> marker states plainly that the wrong binary is running" \
    || fail "$label -> marker does not contain '$MARKER_HEAD' (got: $(tr '\n' ' ' < "$mark" | cut -c1-160))"
  has "$mark" "$want" \
    && pass "$label -> marker names the actual cause ($want)" \
    || fail "$label -> marker does not name the cause '$want' (got: $(tr '\n' ' ' < "$mark" | cut -c1-200))"

  run_hook "$plugin_src" "$W"
  has "$W/hook.ctx" "$BANNER_MARK" \
    && pass "$label -> SessionStart puts the warning in front of the model" \
    || fail "$label -> SessionStart additionalContext carries no warning; the marker reaches nobody"
  if [ "$(cat "$W/hook.sysmsg")" = "<absent>" ]; then
    fail "$label -> SessionStart emitted no systemMessage, so nothing goes straight to the human"
  else
    has "$W/hook.sysmsg" "$MARKER_HEAD" \
      && pass "$label -> SessionStart systemMessage carries the launcher's full text" \
      || fail "$label -> systemMessage is present but does not carry the marker text"
  fi
}

echo "== 1. the mutant: unknown channel + empty plugin bin (aihub#305 acceptance probe) =="
# `channel = "zzznope"` is the acceptance probe's "a value that does not exist".
# It resolves onto dev (PR#279) and then the download fails anyway, which is the
# point: the channel fix does not make a download succeed.
W1=$(build_world "$PLUGIN_SRC" '[binary]\nchannel = "zzznope"\n' ok fail yes none no)
[ "$(cat "$W1/rc")" = 0 ] \
  && pass "mutant/degraded -> launcher still starts the server (availability is preserved)" \
  || fail "mutant/degraded -> launcher exited $(cat "$W1/rc"); degrading to the PATH binary must still start"
[ -L "$W1/plugin/bin/polyforge" ] \
  && pass "mutant/degraded -> it really did fall back to the PATH binary (the silent case)" \
  || fail "mutant/degraded -> no fallback symlink, so this arm is not exercising the silent path"
assert_visible "mutant/degraded" "$W1" "download from bins-dev failed" "$PLUGIN_SRC"
has "$W1/home/.polyforge/binary-status.txt" "$(printf '%.8s' "$PATHBIN_SHA")" \
  && pass "mutant/degraded -> marker names the version actually running" \
  || fail "mutant/degraded -> marker does not name the running binary's version"
# The ELOOP guard: the fallback symlink must not be advertised back into the
# system bin dir, or INSTALL_PATH and it point at each other.
[ ! -e "$W1/sysbin/polyforge" ] \
  && pass "mutant/degraded -> system bin dir not repointed at the fallback symlink (no ELOOP cycle)" \
  || fail "mutant/degraded -> $W1/sysbin/polyforge was created from a symlinked INSTALL_PATH; exec will hit ELOOP"

# Same mutant, no PATH binary at all: nothing to degrade to, so the only honest
# answer is to refuse to start. Claude Code surfaces that as a failed server.
W2=$(build_world "$PLUGIN_SRC" '[binary]\nchannel = "zzznope"\n' ok fail no none no)
[ "$(cat "$W2/rc")" != 0 ] \
  && pass "mutant/no-fallback -> launcher exits non-zero instead of pretending to start" \
  || fail "mutant/no-fallback -> launcher exited 0 with no binary; the MCP server would look healthy"
assert_visible "mutant/no-fallback" "$W2" "no polyforge on PATH" "$PLUGIN_SRC"

echo
echo "== 2. the reverse: channel=dev, download works, nothing is said =="
# Seeded with a stale marker so this also proves the marker is RETRACTED. A
# marker that only ever accumulates would train people to ignore it, which is
# the same defect wearing a different hat.
W3=$(build_world "$PLUGIN_SRC" '[binary]\nchannel = "dev"\n' ok ok no none yes)
[ "$(cat "$W3/rc")" = 0 ] \
  && pass "reverse -> launcher exits 0" \
  || fail "reverse -> launcher exited $(cat "$W3/rc") on a healthy download"
[ -f "$W3/plugin/bin/polyforge" ] && [ ! -L "$W3/plugin/bin/polyforge" ] \
  && pass "reverse -> a real binary was downloaded, not a symlink" \
  || fail "reverse -> no downloaded binary at plugin/bin/polyforge"
[ ! -e "$W3/home/.polyforge/binary-status.txt" ] \
  && pass "reverse -> the pre-seeded marker was DELETED by the successful download" \
  || fail "reverse -> marker survived a successful download (got: $(tr '\n' ' ' < "$W3/home/.polyforge/binary-status.txt" | cut -c1-120))"
run_hook "$PLUGIN_SRC" "$W3"
has "$W3/hook.ctx" "$BANNER_MARK" \
  && fail "reverse -> SessionStart warned on a healthy machine; a warning nobody can clear is noise" \
  || pass "reverse -> SessionStart says nothing, as a healthy machine must"
[ "$(cat "$W3/hook.sysmsg")" = "<absent>" ] \
  && pass "reverse -> no systemMessage key at all (JSON shape unchanged for Codex/Copilot)" \
  || fail "reverse -> systemMessage emitted on a healthy machine"

# The resident payload must be unchanged on a healthy machine, and bounded when
# it is not: aihub#291's nudge was reverted for busting a budget exactly here.
healthy_len=$(cat "$W3/hook.ctxlen")
run_hook "$PLUGIN_SRC" "$W1"
warned_len=$(cat "$W1/hook.ctxlen")
delta=$((warned_len - healthy_len))
if [ "$delta" -le 0 ]; then
  fail "budget -> the warning added $delta chars to additionalContext, i.e. it is not there"
elif [ "$delta" -gt 420 ]; then
  fail "budget -> the warning adds $delta chars to additionalContext, over the 420 ceiling the hook declares; the 8,497-char gate plus this must stay under the 10,000-char harness limit"
else
  pass "budget -> the warning costs $delta chars, and only when it fires (ceiling 420, healthy payload $healthy_len unchanged)"
fi

echo
echo "== 3. the second, independent cause: gh (untouched by the channel fix) =="
# No gh at all, on the download path.
W4=$(build_world "$PLUGIN_SRC" '[binary]\nchannel = "dev"\n' missing fail yes none no)
assert_visible "gh-missing/download" "$W4" "download from bins-dev failed" "$PLUGIN_SRC"
has "$W4/err" "gh CLI not found" \
  && pass "gh-missing/download -> the reason survives to the log (the 2>/dev/null is gone)" \
  || fail "gh-missing/download -> 'gh CLI not found' was swallowed; download_binary's stderr is still discarded"

# gh present but unauthenticated, with a binary ALREADY installed. This is the
# check_for_update path, not the download path: it never had a `2>/dev/null`,
# it printed its reason and returned 0, and the launcher then exec'd the stale
# binary. Same silent outcome, different function.
W5=$(build_world "$PLUGIN_SRC" '[binary]\nchannel = "dev"\n' unauth fail no stale no)
[ "$(cat "$W5/rc")" = 0 ] \
  && pass "gh-unauth/update-check -> launcher still starts on the stale binary" \
  || fail "gh-unauth/update-check -> launcher exited $(cat "$W5/rc")"
assert_visible "gh-unauth/update-check" "$W5" "gh auth token" "$PLUGIN_SRC"
has "$W5/home/.polyforge/binary-status.txt" "$(printf '%.8s' "$STALE_SHA")" \
  && pass "gh-unauth/update-check -> marker names the stale binary's version" \
  || fail "gh-unauth/update-check -> marker does not name the stale version"

echo
echo "== 4. the original aihub#305 shape: channel readable by nobody =="
# gh works, the branch does not answer. This is what bins-stable did for three
# months: check_for_update got an empty `latest`, said so on stderr, stamped its
# 24h cooldown and returned 0, forever.
W6=$(build_world "$PLUGIN_SRC" '[binary]\nchannel = "dev"\n' ok fail no stale no)
assert_visible "unreadable-channel" "$W6" "could not read bins-dev/bin/version.txt" "$PLUGIN_SRC"
[ -f "$W6/home/.polyforge/.last_binary_check" ] \
  && pass "unreadable-channel -> the 24h cooldown is still stamped (no warning storm)" \
  || fail "unreadable-channel -> cooldown not stamped"

# ── mutation controls ────────────────────────────────────────────────────────
# Each: copy the plugin, break ONE mechanism, prove the copy really differs,
# then prove the arm above goes dark. Without the "really differs" assertion a
# replacement that matched nothing would produce a green run meaning nothing.
MUT_DIR=""
mutate() { # $1 label, $2 relpath under the plugin, $3 literal find, $4 replace
           # sets MUT_DIR to the mutated copy, or "" if the mutation did not apply
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

echo
echo "== 5. negative control: put main's \`2>/dev/null\` back and drop the marker =="
mutate "nc/launcher-download" "bin/polyforge-mcp.sh" \
  '  if download_binary; then
    pf_status_clear
  elif' \
  '  if download_binary 2>/dev/null; then
    :
  elif'
C1="$MUT_DIR"
if [ -n "$C1" ]; then
  # Also remove the marker write from the fallback branch, which together with
  # the redirect above is exactly what main does.
  python3 - "$C1/bin/polyforge-mcp.sh" <<'PY'
import re, sys
p = sys.argv[1]
s = open(p, encoding="utf-8").read()
s = re.sub(r"\n *pf_status_record \"the download from bins-\$\{CHANNEL\} failed \(see the MCP server log for the reason curl or gh gave\)\"[^\n]*\n[^\n]*\n[^\n]*\n", "\n", s)
open(p, "w", encoding="utf-8").write(s)
PY
  M1=$(build_world "$C1" '[binary]\nchannel = "zzznope"\n' ok fail yes none no)
  [ ! -e "$M1/home/.polyforge/binary-status.txt" ] \
    && pass "nc/launcher-download -> with the mechanism removed the arm goes dark (suite discriminates)" \
    || fail "nc/launcher-download -> a marker was written even with pf_status_record removed; section 1 proves nothing"
  has "$M1/err" "gh CLI not found\|download failed from bins" \
    && fail "nc/launcher-download -> reasons still reached stderr under 2>/dev/null" \
    || pass "nc/launcher-download -> and the reason is swallowed again, as it was on main"
fi

echo
echo "== 6. negative control: hook stops assembling the banner =="
# COUPLED TO THE HOOK'S ctx ASSEMBLY LINE. This mutation deletes STATUS_BANNER and nothing
# else, so it has to quote that line as it currently reads — aihub#365 inserted
# VERSION_BANNER into it, and until this literal was updated to match, the replacement
# matched nothing. That is caught rather than silent only because `mutate` asserts the file
# really changed; anyone adding a third banner must update this literal too.
mutate "nc/hook-banner" "hooks/pf-session-start" \
  'ctx = PREAMBLE + STATUS_BANNER + VERSION_BANNER + LEDE' \
  'ctx = PREAMBLE + VERSION_BANNER + LEDE'
C2="$MUT_DIR"
if [ -n "$C2" ]; then
  W7=$(build_world "$PLUGIN_SRC" '[binary]\nchannel = "zzznope"\n' ok fail yes none no)
  run_hook "$C2" "$W7"
  has "$W7/hook.ctx" "$BANNER_MARK" \
    && fail "nc/hook-banner -> the banner survived its own removal; the relay assertions prove nothing" \
    || pass "nc/hook-banner -> with the banner removed the model is told nothing (relay assertions discriminate)"
fi

echo
echo "== 7. negative control: check_for_update stops recording =="
mutate "nc/update-check" "bin/polyforge-mcp.sh" \
  "    pf_status_record \"'gh auth token' failed" \
  "    : \"'gh auth token' failed"
C3="$MUT_DIR"
if [ -n "$C3" ]; then
  M3=$(build_world "$C3" '[binary]\nchannel = "dev"\n' unauth fail no stale no)
  [ ! -e "$M3/home/.polyforge/binary-status.txt" ] \
    && pass "nc/update-check -> the gh-unauth arm goes dark when its record is removed" \
    || fail "nc/update-check -> a marker appeared anyway; section 3's update-check arm proves nothing"
fi

echo
echo "== 8. the warning survives the hook's own over-budget degrade path =="
# aihub#293 gave pf-session-start a degrade path: over 10,000 chars it rebuilds
# ctx from scratch, dropping trailing fragments. That rebuild is a SECOND
# assembly expression, and the first draft of this change updated only the
# first — so a machine that was both over budget and running a stale binary
# would have had the stale-binary warning silently deleted, i.e. the warning
# would have gone missing from a subset of exactly the population it is for.
# Forcing MAX_CHARS down makes every payload take that path, which is cheaper
# and far more direct than padding a fragment to bust a real budget.
# 3000, not something tiny: below ~1,700 the hook's last-resort hard truncation
# fires and cuts the envelope itself, so the fixture would test the wrong thing
# (and report "no banner" for a reason that has nothing to do with the rebuild).
mutate "degrade-path" "hooks/pf-session-start" 'MAX_CHARS = 10000' 'MAX_CHARS = 3000'
C4="$MUT_DIR"
if [ -n "$C4" ]; then
  W8=$(build_world "$PLUGIN_SRC" '[binary]\nchannel = "zzznope"\n' ok fail yes none no)
  run_hook "$C4" "$W8"
  has "$W8/hook.ctx" 'POLYFORGE SESSION-START PAYLOAD OVER BUDGET' \
    && pass "degrade-path -> the forced-degrade fixture really took the degrade path" \
    || fail "degrade-path -> the fixture did not degrade, so the assertion below would be vacuous"
  has "$W8/hook.ctx" "$BANNER_MARK" \
    && pass "degrade-path -> the stale-binary warning survives being rebuilt under budget pressure" \
    || fail "degrade-path -> degrading dropped the stale-binary warning; a session that is both over budget and stale would be told nothing"
fi

echo
echo "== $passes passed, $fails failed =="
if [ "$fails" = 0 ]; then
  echo "OK: launcher visibility regression suite passed"
  exit 0
fi
echo "FAILED: launcher visibility regression suite" >&2
exit 1
