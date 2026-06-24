#!/usr/bin/env bash
# Regression guard for macOS stock-bash compatibility of polyforge-mcp.sh.
#
# macOS ships GNU bash 3.2.57 as /bin/bash. bash < 4.0 has no recursive parser
# for $(...) and does not understand here-documents nested inside it, so a
# heredoc-in-$() (e.g. a python config parser) makes the whole launcher fail to
# PARSE -- the MCP server never starts. See IEBE-1727.
#
# This suite runs the launcher's parse + channel-detection inside the official
# `bash:3.2` image (same GNU bash source as macOS /bin/bash). Skips (exit 0)
# when Docker is unavailable; CI always provides it.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
launcher="$here/../bin/polyforge-mcp.sh"
[ -f "$launcher" ] || { echo "FAIL: launcher not found at $launcher" >&2; exit 1; }

if ! command -v docker >/dev/null 2>&1; then
  echo "SKIP: docker unavailable; cannot exercise bash 3.2 (CI provides it)."
  exit 0
fi

img=bash:3.2
fails=0

# 1) Parse check -- the whole script must parse under bash 3.2 (no unexpected EOF).
if docker run --rm -v "$launcher":/s.sh:ro "$img" bash -n /s.sh; then
  echo "PASS: parses under bash 3.2"
else
  echo "FAIL: polyforge-mcp.sh does not parse under bash 3.2 (heredoc-in-\$()?)" >&2
  fails=1
fi

# 2) Functional check -- channel detection must resolve correctly under bash 3.2.
#    Trim the launcher to header..esac and print the resolved channel; this drops
#    the network/exec tail so the probe is hermetic.
probe=$(mktemp)
awk '{print} /^esac/{exit}' "$launcher" > "$probe"
printf 'echo "$CHANNEL"\n' >> "$probe"

check() { # label  config-body(printf %b)  expected
  local home; home=$(mktemp -d)
  mkdir -p "$home/.polyforge"
  printf '%b' "$2" > "$home/.polyforge/config.toml"
  local got
  got=$(docker run --rm -v "$probe":/p.sh:ro -v "$home":/h:ro -e HOME=/h "$img" bash /p.sh 2>/dev/null | tail -1)
  if [ "$got" = "$3" ]; then
    echo "PASS: $1 -> $got"
  else
    echo "FAIL: $1 -> '$got' (want '$3')" >&2
    fails=1
  fi
  rm -rf "$home"
}

check 'channel="dev"'    '[binary]\nchannel = "dev"\n'   dev
check "channel='dev'"    "[binary]\nchannel = 'dev'\n"   dev
check 'channel=stable'   '[binary]\nchannel = stable\n'   stable
check 'unknown->stable'  '[binary]\nchannel = "wat"\n'    stable
rm -f "$probe"

if [ "$fails" = 0 ]; then
  echo "OK: macOS bash 3.2 launcher regression suite passed"
  exit 0
fi
echo "FAILED: macOS bash 3.2 launcher regression suite" >&2
exit 1
