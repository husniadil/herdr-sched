#!/bin/sh
# Herdr has no shutdown hook, so this is the way to actually turn the plugin
# off. It ASKS the daemon to end rather than signalling it: `hsched stop` is
# answered first, and the daemon then stops accepting, finishes the calls
# already in flight, and gives up the socket and the lock the way SIGTERM does
# (§2.5). A signal cuts a call in flight; this one does not. Nothing in the
# store is lost by stopping.
#
# There is one daemon per user (§2.3), and it is not always the one start.sh
# launched — a CLI call autostarts the daemon from whatever binary answered it,
# the PATH-installed copy included. So this prefers this checkout's binary and
# falls back to whatever is on PATH; either one reaches the same socket.
set -eu
cd "$(dirname "$0")/.."

if [ -x bin/hsched ]; then
  hsched=./bin/hsched
elif command -v hsched >/dev/null 2>&1; then
  hsched=hsched
else
  echo "sched: no hsched binary here or on PATH, so there is nothing to ask" >&2
  exit 1
fi

# Finding no daemon is a CONFLICT rather than a crash: `hsched stop` says so,
# and the state this script asks for already holds, so that is success here.
if "$hsched" stop; then
  exit 0
fi
status=$?
# 6 is the contract's CONFLICT (§6.3), which for `stop` means NOT_RUNNING.
if [ "$status" -eq 6 ]; then
  exit 0
fi
exit "$status"
