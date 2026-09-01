#!/usr/bin/env bash
# Regression guard for the binary download CHANNEL and, crucially, the URL that
# channel resolves to (aihub#305).
#
# THE BUG
# -------
# polyforge-mcp.sh defaulted to CHANNEL="stable" and also fell back to "stable"
# for an unrecognised value, then fetched
#
#   https://raw.githubusercontent.com/GMISWE/ieops-aihub/bins-stable/bin/polyforge-<os>-<arch>
#
# but `bins-stable` was never published. publish-bins.yml creates it only on a
# `v*` tag push; the repo's single tag (v1.0.0, 2026-05-25) predates that
# workflow by one day, and no tag has been pushed since. So the DEFAULT
# configuration fetched a 404:
#
#   * no binary present  -> download_binary fails loudly (recoverable, visible)
#   * binary present     -> check_for_update gets an empty `latest`, prints one
#                           line on the MCP launcher's stderr and returns 0.
#                           That stderr is not surfaced in the client UI, so the
#                           machine sat frozen on its existing binary, retrying
#                           and failing every 24h, indefinitely and invisibly.
#
# WHY THE EXISTING SUITES DID NOT CATCH IT
# ----------------------------------------
# launcher-bash32.test.sh asserts on the resolved CHANNEL *string*, and its
# expectations encoded `stable` as the correct answer — it was green while the
# default was broken. launcher-update-check.test.sh stubs curl, so every URL
# "works" there by construction. Neither suite ever asked whether the URL the
# launcher builds actually resolves to something. Part 2 below does, over the
# network. That is the assertion that would have caught this, so it is the one
# that must not be allowed to quietly stop running.
#
# HOW PART 1 STAYS DISCRIMINATING
# -------------------------------
# The URL under test is read out of the argv the launcher hands to curl — a stub
# curl records it. The test does NOT rebuild the URL from $CHANNEL: a test that
# did would pass no matter what the URL template said, which is exactly the hole
# the original bug lived in. Consequence, and the point: change the template in
# the launcher to a branch that does not exist and this suite goes red.
#
# Part 1 is hermetic (no docker, no network — stubbed gh/curl/binary).
# Part 2 needs network + a token, and carries its own positive and negative
# controls so that "404" can be told apart from "my token does not work".
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
launcher="$here/../bin/polyforge-mcp.sh"
[ -f "$launcher" ] || { echo "FAIL: launcher not found at $launcher" >&2; exit 1; }

fails=0
passes=0
pass() { passes=$((passes + 1)); echo "PASS: $1"; }
fail() { fails=$((fails + 1)); echo "FAIL: $1" >&2; }

CUR_SHA=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
PUB_SHA=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb

# ── probe ────────────────────────────────────────────────────────────────────
# Drive the launcher with an isolated HOME and a sanitised PATH of stubs, then
# report what it resolved. Emits one line:
#
#   <channel>|<binary url>|<version url>|<stderr, newlines collapsed>
#
# $1 is the config.toml body (printf %b); empty means no config.toml at all.
#
# The installed fake binary reports CUR_SHA and the stub curl serves PUB_SHA for
# version.txt, so check_for_update takes its update path and calls
# download_binary — one call therefore exercises, and records the URL of, both
# fetches the launcher can make.
probe() {
  local body="$1"
  local home stubs t tp ch binurl verurl errline
  home=$(mktemp -d "${TMPDIR:-/tmp}/pfchan.XXXXXX")
  stubs=$(mktemp -d "${TMPDIR:-/tmp}/pfchan.XXXXXX")
  mkdir -p "$home/.polyforge" "$stubs/min" "$stubs/plugin/bin"
  [ -n "$body" ] && printf '%b' "$body" > "$home/.polyforge/config.toml"

  # A sanitised PATH, not a prepend: with the real PATH still on the end a
  # "missing" stub is silently satisfied by the real tool, which is how the
  # sibling update-check suite once exercised the wrong branch entirely.
  # $stubs/min holds only what the launcher itself needs.
  for t in bash sh env date cat grep head mktemp uname sed tr find awk chmod mkdir rm ln; do
    tp=$(command -v "$t" 2>/dev/null) && ln -sf "$tp" "$stubs/min/$t"
  done

  printf '#!/usr/bin/env bash\n[ "$1" = auth ] && [ "$2" = token ] && { echo tok; exit 0; }\nexit 0\n' \
    > "$stubs/gh"

  # Stub curl: record every https URL it is handed, satisfy -o, serve PUB_SHA
  # for version.txt. Recording argv is what makes this test read the launcher's
  # real URL rather than a reconstruction of it.
  cat > "$stubs/curl" <<EOF
#!/usr/bin/env bash
is_ver=no
prev=""
for a in "\$@"; do
  case "\$a" in
    https://*)
      printf '%s\n' "\$a" >> "$home/urls"
      case "\$a" in *version.txt) is_ver=yes ;; esac
      ;;
  esac
  [ "\$prev" = "-o" ] && printf 'fake\n' > "\$a"
  prev="\$a"
done
[ "\$is_ver" = yes ] && printf '%s\n' '$PUB_SHA'
exit 0
EOF

  printf '#!/usr/bin/env bash\n[ "${1:-}" = version ] && echo "polyforge v1 (%s) built now"\nexit 0\n' \
    "$CUR_SHA" > "$stubs/plugin/bin/polyforge"
  chmod +x "$stubs/gh" "$stubs/curl" "$stubs/plugin/bin/polyforge"

  ch=$(
    export HOME="$home" PATH="$stubs:$stubs/min"
    export POLYFORGE_LAUNCHER_SOURCE_ONLY=1
    export CLAUDE_PLUGIN_ROOT="$stubs/plugin"
    # shellcheck disable=SC1090
    . "$launcher" 2> "$home/err" || true
    printf '%s\n' "${CHANNEL:-<unset>}"
    rm -f "$HOME/.polyforge/.last_binary_check"
    check_for_update > /dev/null 2>&1 || true
  )

  binurl=$(grep -m1 'bin/polyforge-' "$home/urls" 2> /dev/null || true)
  verurl=$(grep -m1 'version.txt' "$home/urls" 2> /dev/null || true)
  errline=$(tr '\n' ' ' < "$home/err" 2> /dev/null || true)
  printf '%s|%s|%s|%s\n' "$ch" "$binurl" "$verurl" "$errline"
  rm -rf "$home" "$stubs"
}

f_channel() { printf '%s' "$1" | cut -d'|' -f1; }
f_binurl() { printf '%s' "$1" | cut -d'|' -f2; }
f_verurl() { printf '%s' "$1" | cut -d'|' -f3; }
f_stderr() { printf '%s' "$1" | cut -d'|' -f4-; }

# The one published channel. If a real release process ever republishes under
# another name, change it here and in the launcher together — and note that
# part 2 will refuse to go green until that branch actually exists.
WANT_CHANNEL=dev

# ── part 1: every input resolves to the one channel that exists ──────────────
# (a) no [binary] section, (b) channel = "stable", (c) channel = "garbage".
# Plus "no config.toml at all" (the state of a machine that never ran
# `polyforge init`) and an explicit channel = "dev" reverse control.
CASE_LABELS=""
RESOLVED_URLS=""

check_case() { # label  config-body  expect-stderr(yes|no)
  local label="$1" body="$2" want_err="$3" r ch bu vu er
  r=$(probe "$body")
  ch=$(f_channel "$r"); bu=$(f_binurl "$r"); vu=$(f_verurl "$r"); er=$(f_stderr "$r")

  if [ "$ch" = "$WANT_CHANNEL" ]; then
    pass "$label -> channel '$ch'"
  else
    fail "$label -> channel '$ch' (want '$WANT_CHANNEL')"
  fi

  case "$bu" in
    *"/bins-${WANT_CHANNEL}/bin/polyforge-"*)
      pass "$label -> binary url on bins-${WANT_CHANNEL}" ;;
    "") fail "$label -> launcher never fetched a binary url at all" ;;
    *)  fail "$label -> binary url is '$bu' (want a /bins-${WANT_CHANNEL}/bin/polyforge-* path)" ;;
  esac

  case "$vu" in
    *"/bins-${WANT_CHANNEL}/bin/version.txt") pass "$label -> version url on bins-${WANT_CHANNEL}" ;;
    "") fail "$label -> launcher never fetched a version url at all" ;;
    *)  fail "$label -> version url is '$vu' (want a /bins-${WANT_CHANNEL}/bin/version.txt path)" ;;
  esac

  # A legacy or bad channel must SAY it was redirected: that message is the
  # whole migration path for a machine whose config.toml still says stable.
  # Conversely the good config must be silent, or the warning is noise nobody
  # reads.
  if [ "$want_err" = yes ]; then
    case "$er" in
      *polyforge:*"$WANT_CHANNEL"*) pass "$label -> warns and names the channel it used" ;;
      *) fail "$label -> did not warn that it redirected the channel (stderr: '$er')" ;;
    esac
  else
    if [ -z "$er" ]; then
      pass "$label -> silent, as a valid config must be"
    else
      fail "$label -> warned on a valid config (stderr: '$er')"
    fi
  fi

  CASE_LABELS="$CASE_LABELS $label"
  [ -n "$bu" ] && RESOLVED_URLS="$RESOLVED_URLS$bu
"
  [ -n "$vu" ] && RESOLVED_URLS="$RESOLVED_URLS$vu
"
}

echo "== part 1: channel + url resolution (hermetic) =="
check_case no-config          ''                                             no
check_case no-binary-section  'machine_id = "x"\n[auth]\napi_key = "k"\n'    no
check_case channel-stable     '[binary]\nchannel = "stable"\n'               yes
check_case channel-garbage    '[binary]\nchannel = "garbage"\n'              yes
check_case channel-dev        '[binary]\nchannel = "dev"\n'                  no

# ── part 2: the resolved URLs actually exist ─────────────────────────────────
# A string check cannot tell a published branch from a typo; only a fetch can.
#
# raw.githubusercontent.com answers 404 — not 401 — for a private repo when the
# token is missing or wrong, so "404" is ambiguous on its own. Hence two
# controls: a POSITIVE one (a path that certainly exists) proves the token
# works, and only then is a 404 on a channel URL evidence of a real defect; a
# NEGATIVE one (a branch that certainly does not exist) proves this section can
# still go red, i.e. that it discriminates.
echo "== part 2: the resolved URLs are actually fetchable (network) =="

RAW=https://raw.githubusercontent.com/GMISWE/ieops-aihub
CONTROL_OK="$RAW/main/go.mod"
CONTROL_BAD="$RAW/bins-zzznope-does-not-exist/bin/version.txt"

live_token=""
for v in POLYFORGE_BINS_TOKEN GH_TOKEN GITHUB_TOKEN; do
  eval "cand=\${$v:-}"
  [ -n "$cand" ] && { live_token="$cand"; token_src="\$$v"; break; }
done
if [ -z "$live_token" ] && command -v gh > /dev/null 2>&1; then
  live_token=$(gh auth token 2> /dev/null || true)
  [ -n "$live_token" ] && token_src="gh auth token"
fi

http_code() { # url -> http status, or 000 on a transport failure
  curl -s -o /dev/null -w '%{http_code}' --connect-timeout 10 --max-time 30 \
    -H "Authorization: Bearer ${live_token}" "$1" 2> /dev/null || echo 000
}

if [ -z "$live_token" ]; then
  echo "SKIP: no token (POLYFORGE_BINS_TOKEN / GH_TOKEN / GITHUB_TOKEN / 'gh auth token'); cannot tell a 404 from an auth failure, so the live fetch is not attempted."
  echo "      This section is the one that would have caught aihub#305. Do not treat its absence as a pass."
else
  ctl=$(http_code "$CONTROL_OK")
  echo "      positive control $CONTROL_OK -> $ctl (token from $token_src)"
  if [ "$ctl" != 200 ]; then
    echo "SKIP: positive control did not return 200 (got $ctl) — the token or the network is the problem, not the channel. Live fetch not attempted."
    echo "      This section is the one that would have caught aihub#305. Do not treat its absence as a pass."
  else
    bad=$(http_code "$CONTROL_BAD")
    echo "      negative control $CONTROL_BAD -> $bad"
    if [ "$bad" = 200 ]; then
      fail "negative control returned 200 for a branch that does not exist — this section cannot discriminate, so its passes mean nothing"
    else
      pass "negative control: a non-existent channel does not return 200 (got $bad)"
    fi

    printf '%s' "$RESOLVED_URLS" | sort -u | while IFS= read -r u; do
      [ -z "$u" ] && continue
      code=$(http_code "$u")
      if [ "$code" = 200 ]; then
        echo "PASS: live fetch 200 $u"
      else
        echo "FAIL: live fetch $code $u — the launcher's resolved url is not published" >&2
        echo x >> "${TMPDIR:-/tmp}/pfchan.livefail.$$"
      fi
    done
    # The loop above runs in a subshell (pipeline), so its failures come back
    # through a file rather than through $fails.
    if [ -f "${TMPDIR:-/tmp}/pfchan.livefail.$$" ]; then
      n=$(wc -l < "${TMPDIR:-/tmp}/pfchan.livefail.$$" | tr -d ' ')
      rm -f "${TMPDIR:-/tmp}/pfchan.livefail.$$"
      fail "$n resolved url(s) were not fetchable (see above)"
    else
      pass "every url the launcher resolves is published and fetchable"
    fi
  fi
fi

echo "== $passes passed, $fails failed =="
if [ "$fails" = 0 ]; then
  echo "OK: launcher channel/url regression suite passed"
  exit 0
fi
echo "FAILED: launcher channel/url regression suite" >&2
exit 1
