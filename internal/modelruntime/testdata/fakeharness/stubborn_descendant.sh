#!/bin/sh
# Fake harness for the aihub#708 B5 repair: the LEADER dies promptly on
# SIGTERM (no trap — default action), while its descendant IGNORES the
# signal and stays behind holding the process group id. This is the exact
# shape that made the old Proc.Stop lie: it waited on the leader's reaping,
# so it reported success the moment the leader exited, with the stubborn
# descendant still running. No network, no model, no writes except $RECORD.
#
# aihub#708 B2 (concurrent Stop callers): the descendant RECORDS every
# TERM/INT it survives — arming the trap BEFORE announcing child-started,
# so a test synchronizing on "child-started:" is guaranteed the trap is
# already armed — which lets a test prove that two racing Stop calls still
# produce exactly ONE group SIGTERM.
REC="$RECORD"
echo "parent-started:$$" >> "$REC"
(
  trap 'echo "child-term:$$" >> "$REC"' TERM INT
  echo "child-started:$$" >> "$REC"
  while :; do sleep 0.1; done
) &
wait $!
