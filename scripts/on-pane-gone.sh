#!/bin/sh
# Herdr says a pane closed or its command exited.
#
# There is nothing bound to a pane yet: this build has no job and no trigger,
# so no schedule names a pane and nothing here needs retiring. The hook is
# wired from day one because the moment a trigger can be bound to a pane, the
# change is one command in this script rather than a manifest edit every
# installed copy has to be re-installed to pick up.
#
# The hook fires for every pane in the session, not only panes this plugin
# knows (§8.4). Whatever this grows into must therefore be self-filtering and
# idempotent: a pane this plugin holds nothing for retires nothing, and a
# second firing retires nothing again.
set -eu
cd "$(dirname "$0")/.."

if [ ! -x bin/hsched ]; then
  echo "sched: bin/hsched is missing; the [[build]] step did not run" >&2
  exit 1
fi

# Herdr passes the ids in the environment and substitutes nothing into argv.
# HERDR_PANE_ID is the pane the event is about, and it is absent when the pane
# had already left Herdr's state by the time the hook ran.
if [ -z "${HERDR_PANE_ID:-}" ]; then
  echo "sched: a pane event arrived with no pane id, so there is nothing to act on" >&2
  exit 0
fi

exit 0
