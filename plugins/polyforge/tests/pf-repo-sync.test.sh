#!/usr/bin/env bash
# Test suite for hooks/pf-repo-sync (aihub#254).
#
# The bug this guards: pf-repo-sync was registered as a SYNCHRONOUS SessionStart hook that
# serially `git fetch`ed every .repo/* clone with no per-fetch timeout and no throttle, so every
# new session paid the sum of all repos' network latency before the first token (cc TTFT
# regression). The fix is async registration + parallel fetch + per-repo timeout + TTL stamp.
#
# Assert-based, no framework — mirrors tests/pf-commit-guard.test.sh.
#
# Hermetic: NO network. Origins are local bare repos, and a `git` shim on PATH intercepts
# `fetch` to log calls and to simulate slow/hanging remotes. Everything runs under mktemp -d.
set -uo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
hook="$here/../hooks/pf-repo-sync"
hooks_json="$here/../hooks/hooks.json"
[ -x "$hook" ] || { echo "FAIL: hook not executable at $hook" >&2; exit 1; }
command -v git >/dev/null 2>&1 || { echo "SKIP: git unavailable"; exit 0; }
REAL_GIT="$(command -v git)"
fails=0
tmproot="$(mktemp -d)"
cleanup() { rm -rf "$tmproot" 2>/dev/null || :; }
trap cleanup EXIT

pass() { echo "  PASS: $1"; }
bad()  { echo "  FAIL: $1" >&2; fails=$((fails + 1)); }
ck()   { if [ "$2" = "$3" ]; then pass "$1"; else bad "$1 (want '$3', got '$2')"; fi; }

# --- fixture helpers ---------------------------------------------------------------------

# new_ws <name> <nrepos> -> echoes the workspace path
new_ws() {
  _ws="$tmproot/$1"; mkdir -p "$_ws/.repo"
  printf 'version: 1\n' > "$_ws/.polyforge.yaml"
  _i=1
  while [ "$_i" -le "$2" ]; do
    _o="$tmproot/$1-origin$_i"
    "$REAL_GIT" init --quiet --bare "$_o"
    _seed="$tmproot/$1-seed$_i"
    "$REAL_GIT" init --quiet "$_seed"
    ( cd "$_seed" \
      && "$REAL_GIT" -c user.email=t@t -c user.name=t commit --quiet --allow-empty -m seed \
      && "$REAL_GIT" branch -M main \
      && "$REAL_GIT" remote add origin "$_o" \
      && "$REAL_GIT" push --quiet origin main ) >/dev/null 2>&1
    "$REAL_GIT" clone --quiet "$_o" "$_ws/.repo/r$_i" >/dev/null 2>&1
    _i=$((_i + 1))
  done
  echo "$_ws"
}

# advance_origin <wsname> <idx> — push a new commit to that repo's origin
advance_origin() {
  _seed="$tmproot/$1-seed$2"
  ( cd "$_seed" \
    && "$REAL_GIT" -c user.email=t@t -c user.name=t commit --quiet --allow-empty -m more \
    && "$REAL_GIT" push --quiet origin main ) >/dev/null 2>&1
}

# mkshim <ws> <mode: pass|slow|hang> <seconds> — install a `git` shim that logs fetches
mkshim() {
  _bin="$1/.shim"; mkdir -p "$_bin"
  cat > "$_bin/git" <<SHIM
#!/usr/bin/env bash
for _a in "\$@"; do
  if [ "\$_a" = "fetch" ]; then
    echo fetch >> "$1/.fetchlog"
    case "$2" in
      slow) sleep $3 ;;
      hang) exec sleep $3 ;;
    esac
    break
  fi
done
exec "$REAL_GIT" "\$@"
SHIM
  chmod +x "$_bin/git"
}

fetches() { [ -f "$1/.fetchlog" ] && wc -l < "$1/.fetchlog" | tr -d ' ' || echo 0; }
head_of()  { "$REAL_GIT" -C "$1" rev-parse HEAD 2>/dev/null; }
origin_head_of() { "$REAL_GIT" -C "$1" rev-parse origin/main 2>/dev/null; }

# run_hook <ws> [env assignments...] — always discards output so an orphaned shim child can
# never keep a pipe open and wedge the suite. Sets ELAPSED.
run_hook() {
  _ws="$1"; shift
  _t0="$(date +%s)"
  ( cd "$_ws" && env PATH="$_ws/.shim:$PATH" CLAUDE_PROJECT_DIR="$_ws" "$@" "$hook" ) \
    >/dev/null 2>&1
  ELAPSED=$(( $(date +%s) - _t0 ))
}

# --- static guards -----------------------------------------------------------------------

echo "== static =="
if bash -n "$hook" 2>/dev/null; then pass "hook parses"; else bad "hook has a syntax error"; fi

# The entire fix is that this hook no longer blocks session start. If someone flips it back to
# a synchronous hook, the TTFT regression returns even with every other bound in place.
if command -v python3 >/dev/null 2>&1; then
  async="$(python3 - "$hooks_json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
for grp in d["hooks"]["SessionStart"]:
    for h in grp["hooks"]:
        if "pf-repo-sync" in h["command"]:
            print(h.get("async"))
            raise SystemExit
print("MISSING")
PY
)"
  ck "pf-repo-sync is registered async (never blocks session start)" "$async" "True"
else
  echo "  SKIP: python3 unavailable; cannot assert hooks.json registration"
fi

# --- behaviour ---------------------------------------------------------------------------

echo "== no-op guards =="
ws="$(new_ws noyaml 1)"; mkshim "$ws" pass 0
rm -f "$ws/.polyforge.yaml"
run_hook "$ws"
ck "no .polyforge.yaml -> no fetch" "$(fetches "$ws")" "0"

ws="$(new_ws disabled 1)"; mkshim "$ws" pass 0
run_hook "$ws" PF_REPO_SYNC_DISABLE=1
ck "PF_REPO_SYNC_DISABLE=1 -> no fetch" "$(fetches "$ws")" "0"

echo "== sync brings the base clone up to date =="
ws="$(new_ws basic 1)"; mkshim "$ws" pass 0
advance_origin basic 1
before="$(head_of "$ws/.repo/r1")"
run_hook "$ws" PF_REPO_SYNC_TTL=0
after="$(head_of "$ws/.repo/r1")"
ck "fetched once" "$(fetches "$ws")" "1"
if [ "$before" != "$after" ] && [ "$after" = "$(origin_head_of "$ws/.repo/r1")" ]; then
  pass "clone reset to origin default branch"
else
  bad "clone not advanced to origin/main (before=$before after=$after)"
fi
if [ -f "$ws/.polyforge/cache/repo-sync/r1.stamp" ]; then pass "stamp written"; else bad "no stamp written"; fi

echo "== TTL throttle =="
ws="$(new_ws ttl 1)"; mkshim "$ws" pass 0
run_hook "$ws" PF_REPO_SYNC_TTL=900
ck "first run fetches" "$(fetches "$ws")" "1"
run_hook "$ws" PF_REPO_SYNC_TTL=900
ck "second run within TTL skips the fetch" "$(fetches "$ws")" "1"
run_hook "$ws" PF_REPO_SYNC_TTL=0
ck "TTL=0 always fetches" "$(fetches "$ws")" "2"

echo "== a failed fetch must not be stamped (else it is suppressed for a whole TTL) =="
ws="$(new_ws failnostamp 1)"; mkshim "$ws" hang 30
run_hook "$ws" PF_REPO_SYNC_TIMEOUT=2 PF_REPO_SYNC_TTL=900
if [ -f "$ws/.polyforge/cache/repo-sync/r1.stamp" ]; then
  bad "timed-out fetch wrote a stamp (would skip retries for the whole TTL)"
else
  pass "timed-out fetch left no stamp"
fi

echo "== per-repo timeout bounds an unreachable remote =="
ws="$(new_ws hang 1)"; mkshim "$ws" hang 60
run_hook "$ws" PF_REPO_SYNC_TIMEOUT=3 PF_REPO_SYNC_TTL=0
if [ "$ELAPSED" -lt 15 ]; then
  pass "hanging remote bounded (${ELAPSED}s, not the old 60s outer timeout)"
else
  bad "hanging remote not bounded (${ELAPSED}s)"
fi

echo "== timeout(1) is absent on stock macOS: the fallback watchdog must still bound it =="
ws="$(new_ws nolimit 1)"; mkshim "$ws" hang 60
minbin="$ws/.minbin"; mkdir -p "$minbin"
# `bash` MUST be here: it is both the interpreter we invoke and the shim's shebang. Omitting it
# made this case pass vacuously (hook exited in 0s having never attempted a fetch), so the
# fetch-attempted assertion below is load-bearing, not decoration.
for c in bash basename dirname cat date mkdir sleep rm wc tr env; do
  p="$(command -v "$c" 2>/dev/null)" && ln -sf "$p" "$minbin/$c"
done
ln -sf "$ws/.shim/git" "$minbin/git"
if PATH="$minbin" command -v timeout >/dev/null 2>&1 \
  || PATH="$minbin" command -v gtimeout >/dev/null 2>&1; then
  echo "  SKIP: could not hide timeout(1)/gtimeout(1) from the hook"
else
  t0="$(date +%s)"
  ( cd "$ws" && env -i PATH="$minbin" CLAUDE_PROJECT_DIR="$ws" \
      PF_REPO_SYNC_TIMEOUT=3 PF_REPO_SYNC_TTL=0 "$minbin/bash" "$hook" ) >/dev/null 2>&1
  el=$(( $(date +%s) - t0 ))
  if [ "$(fetches "$ws")" -eq 0 ]; then
    bad "watchdog case never reached the fetch — the bound below would be vacuous"
  elif [ "$el" -lt 15 ]; then
    pass "bounded without timeout(1) via the watchdog fallback (${el}s)"
  else
    bad "watchdog fallback did not bound the fetch (${el}s)"
  fi
fi

# The bound above only proves the watchdog can KILL. Stock macOS is the only platform that runs
# this branch, and its normal case is the opposite one: a fast fetch that SUCCEEDS. That path must
# report success and actually advance the clone. It regressed once by construction: detecting
# completion with `kill -0` treats an exited-but-unreaped child (a zombie) as still alive, so a
# successful fetch burns the full budget and returns 124 -> no reset, no stamp, error swallowed,
# i.e. a hook that silently never does anything on macOS. Hence this case.
echo "== the fallback watchdog must also report SUCCESS, not just kill (macOS normal case) =="
ws="$(new_ws nolimitok 1)"; mkshim "$ws" pass 0
advance_origin nolimitok 1
minbin="$ws/.minbin"; mkdir -p "$minbin"
for c in bash basename dirname cat date mkdir sleep rm wc tr env; do
  p="$(command -v "$c" 2>/dev/null)" && ln -sf "$p" "$minbin/$c"
done
ln -sf "$ws/.shim/git" "$minbin/git"
if PATH="$minbin" command -v timeout >/dev/null 2>&1 \
  || PATH="$minbin" command -v gtimeout >/dev/null 2>&1; then
  echo "  SKIP: could not hide timeout(1)/gtimeout(1) from the hook"
else
  before="$(head_of "$ws/.repo/r1")"
  ( cd "$ws" && env -i PATH="$minbin" CLAUDE_PROJECT_DIR="$ws" \
      PF_REPO_SYNC_TIMEOUT=8 PF_REPO_SYNC_TTL=0 "$minbin/bash" "$hook" ) >/dev/null 2>&1
  after="$(head_of "$ws/.repo/r1")"
  if [ "$(fetches "$ws")" -eq 0 ]; then
    bad "watchdog success case never reached the fetch — the assertions below would be vacuous"
  else
    if [ "$before" != "$after" ] && [ "$after" = "$(origin_head_of "$ws/.repo/r1")" ]; then
      pass "watchdog fallback advanced the clone on a successful fetch"
    else
      bad "watchdog fallback did not advance the clone (before=$before after=$after)"
    fi
    if [ -f "$ws/.polyforge/cache/repo-sync/r1.stamp" ]; then
      pass "watchdog fallback stamped a successful fetch"
    else
      bad "watchdog fallback left no stamp on success (success mis-read as timeout?)"
    fi
  fi
fi

echo "== repos are fetched in parallel, not serially =="
ws="$(new_ws par 3)"; mkshim "$ws" slow 2
run_hook "$ws" PF_REPO_SYNC_TTL=0 PF_REPO_SYNC_TIMEOUT=20
ck "all 3 repos fetched" "$(fetches "$ws")" "3"
if [ "$ELAPSED" -le 4 ]; then
  pass "3x2s fetches took ${ELAPSED}s (parallel; serial would be ~6s)"
else
  bad "fetches look serial: ${ELAPSED}s for 3x2s (want <=4s)"
fi

# Counterpart to the case above: the fan-out is capped, so a large workspace cannot open one
# connection per repo at once. JOBS=1 is the observable extreme — it must serialize the same three
# fetches the default settings ran concurrently. If the cap were ignored, this would finish in ~2s.
echo "== the fan-out honours PF_REPO_SYNC_JOBS =="
ws="$(new_ws jobcap 3)"; mkshim "$ws" slow 2
run_hook "$ws" PF_REPO_SYNC_TTL=0 PF_REPO_SYNC_TIMEOUT=20 PF_REPO_SYNC_JOBS=1
ck "all 3 repos still fetched with JOBS=1" "$(fetches "$ws")" "3"
if [ "$ELAPSED" -ge 5 ]; then
  pass "JOBS=1 serialized 3x2s fetches (${ELAPSED}s), so the cap is honoured"
else
  bad "JOBS=1 did not serialize: ${ELAPSED}s for 3x2s (want >=5s; cap ignored?)"
fi

echo
if [ "$fails" -eq 0 ]; then echo "ALL PASS"; else echo "$fails FAILURE(S)" >&2; fi
exit "$fails"
