#!/bin/sh
# Open one of this plugin's popups. Herdr binds keys to plugin ACTIONS, not to
# panes, so each popup has an [[actions]] entry that runs this with the pane's
# id — which is what makes a keybinding able to reach it at all.
#
# Opening a popup that is already open is not a failure. Herdr answers "popup
# already open", and to an operator leaning on a key that IS the state they
# asked for, so this reports success. A real toggle (open, then close on the
# second press) is a different decision and is not made here.
set -eu
cd "$(dirname "$0")/.."

entrypoint="${1:-}"
if [ -z "$entrypoint" ]; then
  echo "usage: open-pane.sh <pane id, as declared in herdr-plugin.toml>" >&2
  exit 2
fi

# §11.1: reach Herdr through HERDR_BIN_PATH, falling back to PATH. Herdr sets
# it for everything it spawns, so the fallback is for a hand-run script.
herdr_bin="${HERDR_BIN_PATH:-herdr}"

# The plugin id has to match the manifest's `id`, because Herdr resolves a
# pane by the pair.
plugin_id="herdr-sched"

if err=$("$herdr_bin" plugin pane open --plugin "$plugin_id" --entrypoint "$entrypoint" 2>&1 >/dev/null); then
  exit 0
fi

# Herdr prints its whole JSON response on stderr and exits 1. The one failure
# that is really a success is the popup already being up.
case "$err" in
  *"already open"*)
    exit 0
    ;;
  *)
    printf '%s\n' "$err" >&2
    exit 1
    ;;
esac
