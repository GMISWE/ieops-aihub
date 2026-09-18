#!/bin/sh
# Fake harness: records its full inherited environment so the runner test can
# prove Start adds NOTHING to the child's env — no credentials, no exported
# config, no polyforge key material. The only file it touches is $RECORD.
env > "$RECORD"
exit 0
