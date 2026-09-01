#!/usr/bin/env bash
# Regression guard for polyforge-mcp.sh's daily update check (aihub#237).
#
# THE BUG: the check collapsed three different outcomes into one branch —
#
#   if [ -n "$LATEST" ] && [ -n "$CURRENT" ] && [ "$CURRENT" != "$LATEST" ]; then
#       ...update...
#   else
#       echo "$NOW" > "$LAST_CHECK_FILE"     # already up to date
#   fi
#
# so "I could not read my own version" (CURRENT="") took the branch labelled
# "already up to date" AND stamped the check file. A binary that cannot report
# a 40-char SHA therefore pinned itself to its current build forever, while
# reporting success on every launch. Self-perpetuating and silent — the worst
# combination to diagnose. It cost a full investigation via aihub#235/#225
# before anyone looked at the launcher.
#
# Unlike launcher-bash32.test.sh this suite needs NO docker and NO network: it
# sources the launcher with POLYFORGE_LAUNCHER_SOURCE_ONLY=1 and drives
# check_for_update() directly with stubbed gh/curl/binary. That matters —
# a guard that only runs where docker exists would not have run on the machine
# where this bug was found (team memory mem_I98xpPgY: aihub's local gate is
# full of tests that never execute anywhere).
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
launcher="$here/../bin/polyforge-mcp.sh"
[ -f "$launcher" ] || { echo "FAIL: launcher not found at $launcher" >&2; exit 1; }

fails=0
pass() { echo "PASS: $1"; }
fail() { echo "FAIL: $1" >&2; fails=1; }

# ── harness ──────────────────────────────────────────────────────────────────
# Each case runs in a subshell with an isolated HOME and a stub bin dir on PATH
# ahead of the real tools, then asserts on (a) whether the check file was
# stamped, (b) whether a download was attempted, (c) what was said on stderr.
#
# stub_version : text the fake polyforge binary prints for `version`
# stub_latest  : text the fake curl returns for version.txt
# stub_gh      : "ok" | "missing" | "old" | "unauth"
# dl_ok        : "yes" (default) | "no" — whether the binary download succeeds
# fresh        : "no" (default) | "yes" — leave a current timestamp in place, so
#                the 24h window is CLOSED and the check should do nothing
run_case() {
  local label="$1" stub_version="$2" stub_latest="$3" stub_gh="$4" dl_ok="${5:-yes}" fresh="${6:-no}"
  local home stubs out rc
  # Explicit template, not `-t`: GNU mktemp rejects a template with no X's
  # ("too few X's"), while bare `mktemp -d` is a usage error on BSD/macOS. An
  # explicit path template is the only form both accept.
  home=$(mktemp -d "${TMPDIR:-/tmp}/pfupd.XXXXXX")
  stubs=$(mktemp -d "${TMPDIR:-/tmp}/pfupd.XXXXXX")
  mkdir -p "$home/.polyforge"

  # A sanitised PATH, NOT a prepend onto the inherited one. With
  # PATH="$stubs:$PATH" the `missing` case still found the real gh further down
  # the path, so it silently exercised the auth-failure branch instead and the
  # "gh CLI not found" branch was never covered anywhere. Worse, on a machine
  # with a modern authenticated gh the case took the update branch and the whole
  # suite went red — on exactly the dev boxes it exists to protect, while being
  # a required CI gate. $stubs/min holds only the tools the launcher itself
  # needs, so absence of a stub really does mean absence.
  mkdir -p "$stubs/min"
  # bash/sh must be here too: the stubs below use `#!/usr/bin/env bash`, and
  # env resolves `bash` via PATH — omit it and every stub silently fails to
  # exec, which looks exactly like "gh is missing".
  for t in bash sh env date cat grep head mktemp uname sed tr find awk chmod mkdir rm ln curl; do
    tp=$(command -v "$t" 2>/dev/null) && ln -sf "$tp" "$stubs/min/$t"
  done

  # Fake managed binary: prints whatever version string the case wants.
  mkdir -p "$stubs/plugin/bin"
  printf '#!/usr/bin/env bash\n[ "${1:-}" = "version" ] && printf "%%s\\n" %q\nexit 0\n' \
    "$stub_version" > "$stubs/plugin/bin/polyforge"
  chmod +x "$stubs/plugin/bin/polyforge"

  # Fake gh.
  case "$stub_gh" in
    ok)      printf '#!/usr/bin/env bash\n[ "$1" = "auth" ] && [ "$2" = "token" ] && { echo tok; exit 0; }\nexit 0\n' > "$stubs/gh" ;;
    missing) : ;;  # no gh at all
    old)     printf '#!/usr/bin/env bash\necho '"'"'unknown command "token" for "gh auth"'"'"' >&2\nexit 1\n' > "$stubs/gh" ;;
    unauth)  printf '#!/usr/bin/env bash\nexit 1\n' > "$stubs/gh" ;;
  esac
  [ -f "$stubs/gh" ] && chmod +x "$stubs/gh"

  # Fake curl: version.txt returns stub_latest; binary download "succeeds".
  cat > "$stubs/curl" <<EOF
#!/usr/bin/env bash
for a in "\$@"; do case "\$a" in *version.txt) printf '%s\n' $(printf %q "$stub_latest"); exit 0;; esac; done
for a in "\$@"; do case "\$a" in -o) shift; :;; esac; done
# binary download: write something to the -o target and report success
prev=""; for a in "\$@"; do [ "\$prev" = "-o" ] && echo fake-binary > "\$a"; prev="\$a"; done
echo "DOWNLOAD_ATTEMPTED" >> "$home/.download_log"
[ "$(printf %s "$dl_ok")" = no ] && exit 22
exit 0
EOF
  chmod +x "$stubs/curl"

  out=$(
    export HOME="$home" PATH="$stubs:$stubs/min"
    export POLYFORGE_LAUNCHER_SOURCE_ONLY=1
    export CLAUDE_PLUGIN_ROOT="$stubs/plugin"
    # shellcheck disable=SC1090
    . "$launcher" 2>&1 || true
    # Open or close the daily window, per the case.
    if [ "$fresh" = yes ]; then
      date +%s > "$HOME/.polyforge/.last_binary_check"
    else
      rm -f "$HOME/.polyforge/.last_binary_check"
    fi
    check_for_update 2>&1
  )
  rc=$?

  local stamped=no attempted=no
  [ -f "$home/.polyforge/.last_binary_check" ] && stamped=yes
  [ -f "$home/.download_log" ] && attempted=yes

  printf '%s\n' "$label|$stamped|$attempted|$out"
  rm -rf "$home" "$stubs"
  return $rc
}

# The record's 4th field (captured stderr) is multi-line, and cut works
# line-by-line — so restrict the flag fields to the first line, and take
# everything after the 3rd delimiter for the message field.
field()   { printf '%s' "$1" | head -1 | cut -d'|' -f"$2"; }
message() { printf '%s' "$1" | cut -d'|' -f4-; }

# ── the reported bug ─────────────────────────────────────────────────────────
# A binary with no readable 40-char SHA (e.g. anything from `make build`, whose
# GIT_COMMIT was a 7-char short SHA) must NOT be treated as up to date and must
# NOT stamp the check file, because doing so pins it forever.
r=$(run_case unreadable 'polyforge dev (unknown) built unknown' \
      'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' ok)
out=$(message "$r")
if [ "$(field "$r" 3)" = yes ]; then
  pass "unreadable CURRENT triggers an update attempt instead of a silent no-op"
else
  fail "unreadable CURRENT did not attempt an update — binary stays pinned"
fi
# The anti-pin guarantee, and the one that actually encodes the bug: when the
# version is unreadable AND the recovery download fails, the check file must be
# left UNSTAMPED so the next launch retries. The old code stamped here, which
# is precisely what made the pin permanent and self-perpetuating.
rf=$(run_case unreadable-dlfail 'polyforge dev (unknown) built unknown' \
      'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' ok no)
if [ "$(field "$rf" 2)" = no ]; then
  pass "unreadable CURRENT + failed download leaves the check file unstamped (retries)"
else
  fail "unreadable CURRENT + failed download STAMPED the check file — binary is pinned for 24h and, since its version stays unreadable, forever"
fi
# The whole point of the fix is that this case is no longer silent: the user
# must be told the binary could not report a SHA, since that is what they have
# to act on. Assert a user-facing line exists and names the cause.
case "$out" in
  *"polyforge:"*"SHA"*) pass "unreadable CURRENT explains the cause on stderr" ;;
  "") fail "unreadable CURRENT produced no output at all (silent failure preserved)" ;;
  *) fail "unreadable CURRENT warned but did not name the cause: $out" ;;
esac

# ── genuinely up to date ─────────────────────────────────────────────────────
same=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
r=$(run_case uptodate "polyforge v1 ($same) built now" "$same" ok)
if [ "$(field "$r" 2)" = yes ] && [ "$(field "$r" 3)" = no ]; then
  pass "matching SHAs stamp the check file and skip the download"
else
  fail "matching SHAs: stamped=$(field "$r" 2) attempted=$(field "$r" 3) (want yes/no)"
fi

# ── a real update ────────────────────────────────────────────────────────────
r=$(run_case outdated 'polyforge v1 (cccccccccccccccccccccccccccccccccccccccc) built now' \
      'dddddddddddddddddddddddddddddddddddddddd' ok)
if [ "$(field "$r" 3)" = yes ]; then
  pass "differing SHAs trigger a download"
else
  fail "differing SHAs did not trigger a download"
fi

# ── the 24h window itself (mutant M1) ────────────────────────────────────────
# Every other case clears the stamp, so nothing exercised the "checked recently,
# do nothing" direction. If that guard broke, every MCP session would hit the
# network and re-download — the noise the stamp policy exists to prevent.
r=$(run_case fresh-window 'polyforge v1 (1111111111111111111111111111111111111111) built now' \
      '2222222222222222222222222222222222222222' ok yes yes)
if [ "$(field "$r" 3)" = no ] && [ -z "$(message "$r")" ]; then
  pass "inside the 24h window the check does nothing and says nothing"
else
  fail "inside the 24h window: attempted=$(field "$r" 3) msg='$(message "$r")' (want no/empty) — the daily guard is gone, so every session would hit the network"
fi

# ── LATEST unreadable (mutant M5) ────────────────────────────────────────────
# One of the three outcomes this change split apart, and previously untested
# because the stub curl always served version.txt successfully.
r=$(run_case latest-empty 'polyforge v1 (3333333333333333333333333333333333333333) built now' \
      'not-a-sha' ok)
if [ "$(field "$r" 3)" = no ] && [ "$(field "$r" 2)" = yes ]; then
  pass "unreadable LATEST stamps and does not download (cannot compare, so do not act)"
else
  fail "unreadable LATEST: attempted=$(field "$r" 3) stamped=$(field "$r" 2) (want no/yes)"
fi
case "$(message "$r")" in
  *"published version"*) pass "unreadable LATEST names its cause" ;;
  *) fail "unreadable LATEST did not explain itself: $(message "$r")" ;;
esac

# ── anti-pin on the ORDINARY update path (mutant M6) ─────────────────────────
# The unreadable-CURRENT case already guards this, but the plain outdated path
# had no such assertion — a stamp-on-failure there would pin a binary for 24h
# at a time, silently, which is the same defect class in a different branch.
r=$(run_case outdated-dlfail 'polyforge v1 (4444444444444444444444444444444444444444) built now' \
      '5555555555555555555555555555555555555555' ok no)
if [ "$(field "$r" 2)" = no ]; then
  pass "outdated + failed download leaves the check file unstamped (retries next launch)"
else
  fail "outdated + failed download STAMPED the check file — the update is deferred 24h despite never happening"
fi

# ── gh unavailable: must be visible, not silent ──────────────────────────────
# Three distinct causes all previously collapsed into one silent skip. The
# third (gh too old for `gh auth token`) is what the dev box actually hit —
# gh 2.4.0 predates that subcommand, so the guard failed with "unknown command"
# rather than any auth problem, and nothing was ever printed.
for g in missing old unauth; do
  r=$(run_case "gh-$g" 'polyforge v1 (eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee) built now' \
        'ffffffffffffffffffffffffffffffffffffffff' "$g")
  out=$(message "$r")
  # Each cause must be distinguishable, not merely non-silent: "gh not
  # installed" and "gh auth failed" need different remedies. Asserting only
  # that *something* was printed is what let the miswired PATH hide for a
  # whole round of review.
  case "$g" in
    missing) want="not found" ;;
    *)       want="gh auth token" ;;
  esac
  case "$out" in
    *"polyforge:"*"update check"*"$want"*) pass "gh $g names its specific cause ($want)" ;;
    "") fail "gh $g is silent — user cannot tell updates are not happening" ;;
    *) fail "gh $g did not name '$want': $out" ;;
  esac
done

if [ "$fails" = 0 ]; then
  echo "OK: launcher update-check regression suite passed"
  exit 0
fi
echo "FAILED: launcher update-check regression suite" >&2
exit 1
