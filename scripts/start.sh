#!/bin/sh
# Start the sched daemon and exit, which is what Herdr expects of a startup
# command. `hsched daemon` refuses when one is already listening, and that
# refusal is success here: the daemon we wanted running is running.
set -eu
cd "$(dirname "$0")/.."
if [ ! -x bin/hsched ]; then
  echo "sched: bin/hsched is missing; the [[build]] step did not run" >&2
  exit 1
fi
# Absolute path deliberately, so the daemon's argv says which binary it is
# when an operator looks.
nohup "$(pwd)/bin/hsched" daemon >/dev/null 2>&1 &
exit 0
