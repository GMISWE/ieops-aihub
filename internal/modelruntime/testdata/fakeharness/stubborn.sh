#!/bin/sh
# Fake harness that ignores SIGTERM and sleeps, to prove the bounded
# SIGKILL backstop: it must be killed within the bounded wait, and it must
# never reach its own exit line.
REC="$RECORD"
echo "stubborn-started:$$" >> "$REC"
trap '' TERM
sleep 300
echo "stubborn-exit:$$" >> "$REC"
exit 0
