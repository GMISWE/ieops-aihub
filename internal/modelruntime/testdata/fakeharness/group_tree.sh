#!/bin/sh
# Fake harness with a child in the same process group. Both record the
# SIGTERMs they receive, so a test can prove the GROUP was signalled (the
# child too) and count the signals (bounded cleanup: exactly one each).
REC="$RECORD"
echo "parent-started:$$" >> "$REC"
trap 'echo "parent-term:$$" >> "$REC"; exit 0' TERM
(
  echo "child-started:$$" >> "$REC"
  trap 'echo "child-term:$$" >> "$REC"; exit 0' TERM
  while :; do sleep 0.1; done
) &
child=$!
wait "$child"
exit 0
