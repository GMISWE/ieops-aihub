#!/bin/sh
# Fake harness: records the exact argv it received, proves stdin is at EOF
# (not an open inherited stdin), echoes one stdout line, exits 0.
# No network, no model, no filesystem writes except $RECORD.
{
  echo "argv-count:$#"
  i=0
  for a in "$@"; do
    echo "arg$i:$a"
    i=$((i+1))
  done
  echo "stdin:$(cat)"
} > "$RECORD"
echo "fake-stdout-line"
exit 0
