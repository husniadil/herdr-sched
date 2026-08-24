#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
./scripts/stop.sh
# A moment for the socket to go before the next daemon claims it.
sleep 1
exec ./scripts/start.sh
