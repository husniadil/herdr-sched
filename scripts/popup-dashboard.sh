#!/bin/sh
# The read-only dashboard: every schedule, every trigger and the tail of the
# run trail, rendered from the CLI's own `--json` and redrawn every 30
# seconds. `r` redraws now; `q` or Esc quits. Keys are read raw (one
# character, no Enter) where stty allows it.
#
# READ-ONLY, deliberately. Nothing here calls a verb that writes: the popup is
# opened by a human with no HERDR_PANE_ID, so a mutating call from it would be
# a human principal walking past the §9 gate that CRUD is held behind. Adding,
# removing, enabling and disabling stay on the gated verbs.
#
# It renders the store's own answer and never the secrets file. `trigger list`
# carries no webhook secret because the key is not in the document it reads;
# this script does not reach for the file that does hold one.
set -u

cd "$(dirname "$0")/.."

# jq is this SCRIPT's runtime requirement and not a dependency of the plugin:
# nothing Go builds or ships needs it, and a machine without it loses the
# popup rather than the scheduler. Say so loudly rather than drawing an empty
# dashboard.
command -v jq >/dev/null 2>&1 || { echo "sched dashboard: jq is required" >&2; exit 1; }
if [ -x bin/hsched ]; then
  HSCHED="$(pwd)/bin/hsched"
elif command -v hsched >/dev/null 2>&1; then
  HSCHED=hsched
else
  echo "sched dashboard: bin/hsched is missing; the [[build]] step did not run" >&2
  exit 1
fi

# Raw terminal input, so a single keypress — Esc included — acts without
# Enter. A terminal stty cannot reshape falls back to line-buffered reads,
# where Enter is still needed and Esc cannot be seen.
saved_tty=$(stty -g 2>/dev/null || true)
restore_tty() { [ -n "$saved_tty" ] && stty "$saved_tty" 2>/dev/null; }
trap 'restore_tty' EXIT INT TERM
raw=0
if [ -n "$saved_tty" ] && stty -icanon -echo min 0 time 0 2>/dev/null; then
  raw=1
fi
esc=$(printf '\033')

# Waits up to $1 seconds and prints the first key pressed, or nothing on
# timeout. Raw mode polls once a second; the fallback is the old read.
next_key() {
  if [ "$raw" = 1 ]; then
    waited=0
    while [ "$waited" -lt "$1" ]; do
      c=$(dd bs=1 count=1 2>/dev/null)
      [ -n "$c" ] && { printf '%s' "$c"; return 0; }
      sleep 1
      waited=$((waited + 1))
    done
    return 0
  fi
  started=$(date +%s)
  key=""
  if ! read -r -t "$1" key 2>/dev/null; then
    # A shell without `read -t` returns at once; sleep the interval so the
    # loop stays a redraw rather than a spin. Quitting still works — the popup
    # closes with the pane.
    [ $(($(date +%s) - started)) -lt 2 ] && sleep "$1"
  fi
  printf '%s' "$key"
}

# The shared jq prelude: the time and column helpers all three sections use.
# Every timestamp on the wire is Unix MILLISECONDS (§5.3) except the derived
# `next`, which is RFC3339, so the two are formatted by different helpers.
JQ_LIB='
  def pad($n): . + (" " * ($n - (. | length)));
  def rpad($n): (" " * ($n - (. | length))) + .;
  def clip($n): if (. | length) > $n then .[0:$n-1] + "…" else . end;
  def ago: if . == null or . == 0 then "never"
    else ($now - (. / 1000 | floor)) as $s
      | if $s < 0 then "just now"
        elif $s < 60 then "\($s)s ago"
        elif $s < 3600 then "\($s / 60 | floor)m ago"
        elif $s < 86400 then "\($s / 3600 | floor)h ago"
        else "\($s / 86400 | floor)d ago" end end;
  def act: (.kind // "?") + (if (.args // {} | length) > 0
    then "(" + ((.args | to_entries | map(.value) | join(" "))) + ")" else "" end);
'

# usable says whether an answer can be RENDERED, and says loudly why not when
# it cannot. It exists because piping the CLI straight into jq turns every
# failure into a healthy-looking empty section: the contract puts a failure on
# stdout as one JSON document (§6.2), jq reads that document happily, and
# `(.count // 0) == 0` is then true for a daemon that refused exactly as it is
# for a daemon with nothing to show. An empty scheduler and a scheduler that
# is not answering must not look the same.
#
# Four ways an answer is not renderable, and each says which: nothing on
# stdout at all, an error document, something that is not JSON, and a
# non-zero status behind an otherwise readable document.
usable() {
  answer=$1
  status=$2
  if [ -z "$(printf '%s' "$answer" | tr -d '[:space:]')" ]; then
    printf '  the daemon answered nothing (exit %s); is it running?\n' "$status"
    return 1
  fi
  if ! printf '%s' "$answer" | jq -e . >/dev/null 2>&1; then
    printf '  the daemon answered something that is not JSON (exit %s)\n' "$status"
    return 1
  fi
  refusal=$(printf '%s' "$answer" | jq -r '
    if type == "object" and .error then
      "  the daemon refused: " + (.error.code // "?") + " " + (.error.message // "")
    else empty end' 2>/dev/null)
  if [ -n "$refusal" ]; then
    printf '%s\n' "$refusal"
    return 1
  fi
  if [ "$status" != 0 ]; then
    printf '  the call failed (exit %s) and said nothing about why\n' "$status"
    return 1
  fi
  return 0
}

section() { printf '\n\033[1m%s\033[0m\n' "$1"; }

render_jobs() {
  answer=$("$HSCHED" job list --json 2>/dev/null); status=$?
  usable "$answer" "$status" || return 0
  printf '%s' "$answer" | jq -r --argjson now "$(date +%s)" "$JQ_LIB"'
    if (.count // 0) == 0 then "  no schedules"
    else
      ("  " + ("ID" | pad(16)) + ("SCHEDULE" | pad(16)) + ("ACTION" | pad(26))
        + ("CCH" | pad(4)) + ("ON" | pad(4)) + ("LAST FIRED" | pad(12)) + "NEXT (UTC)"),
      (.jobs[]
        | "  " + ((.id // "?") | clip(15) | pad(16))
          + ((.schedule // "?") | clip(15) | pad(16))
          + ((.action | act) | clip(25) | pad(26))
          + ((if .catch_up then "yes" else "no" end) | pad(4))
          + ((if .enabled then "yes" else "NO" end) | pad(4))
          + ((.last_fired | ago) | pad(12))
          + (if (.unreadable // "") != "" then "unreadable: " + .unreadable
             elif (.next // "") == "" then "—" else .next end))
    end'
}

render_triggers() {
  answer=$("$HSCHED" trigger list --json 2>/dev/null); status=$?
  usable "$answer" "$status" || return 0
  printf '%s' "$answer" | jq -r --argjson now "$(date +%s)" "$JQ_LIB"'
    if (.count // 0) == 0 then "  no triggers"
    else
      ("  " + ("ID" | pad(16)) + ("KIND" | pad(9)) + ("ACTION" | pad(26))
        + ("COOL" | pad(7)) + ("HOUR" | pad(9)) + ("ON" | pad(4)) + "LAST FIRED"),
      (.triggers[]
        | "  " + ((.id // "?") | clip(15) | pad(16))
          + ((.kind // "?") | pad(9))
          + ((.action | act) | clip(25) | pad(26))
          + ((if (.cooldown_seconds // 0) > 0 then "\(.cooldown_seconds)s" else "—" end) | pad(7))
          + (((.fired_this_hour // 0) | tostring)
              + (if (.max_per_hour // 0) > 0 then "/\(.max_per_hour)" else "/∞" end) | pad(9))
          + ((if .enabled then "yes" else "NO" end) | pad(4))
          + (.last_fired | ago),
          # A watcher is the path it watches, and the stamp is the whole of
          # what it remembers between ticks. A first look RECORDS and does not
          # fire, so "not looked yet" is a state worth showing.
          (if .kind == "watch" then
            "      " + (.path // "?")
              + (.stamp as $s
                | if ($s.seen // false) | not then "  (not looked yet)"
                  elif ($s.present // false) then "  present, \($s.size // 0) bytes"
                  else "  absent" end)
           else empty end))
    end'
}

render_runs() {
  # The events verb has NO server-side entity filter and is not getting one
  # for a dashboard: the trail is read whole and narrowed here. It answers
  # oldest first, so the tail is reversed to put the newest at the top.
  answer=$("$HSCHED" events --json 2>/dev/null); status=$?
  usable "$answer" "$status" || return 0
  printf '%s' "$answer" | jq -r --argjson now "$(date +%s)" "$JQ_LIB"'
    [(.events // [])[] | select(.entity == "run")] | reverse | .[0:12] as $runs
    | if ($runs | length) == 0 then "  nothing has fired yet"
      else ("  " + ("WHEN" | pad(12)) + ("KIND" | pad(10)) + ("ACTOR" | pad(22)) + "DETAIL"),
        ($runs[]
          | "  " + ((.at | ago) | pad(12))
            + ((.kind // "?") | pad(10))
            + ((.actor // "?") | clip(21) | pad(22))
            + ((.detail // {} | to_entries | map("\(.key)=\(.value)") | join(" ")) | clip(48)))
      end'
}

while :; do
  printf '\033[2J\033[H'
  # The clock this frame was drawn at rather than "Ns ago": the screen is
  # static between redraws, so a relative age printed into it is a number that
  # stops being true the moment it is on the screen.
  printf '\033[1mhsched dashboard\033[0m   refreshed %s · every 30s · r refresh · q quit\n' \
    "$(date -u '+%H:%M:%SZ')"

  section "JOBS"
  render_jobs
  section "TRIGGERS"
  render_triggers
  section "RECENT RUNS"
  render_runs

  key=$(next_key 30)
  case "$key" in
    q | Q | "$esc") exit 0 ;;
    r | R) ;;
  esac
done
