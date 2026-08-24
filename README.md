# herdr-sched

herdr-sched is the scheduler and trigger plugin for a Herdr fleet. A cron
schedule or an inbound trigger fires an action into the sibling plugins —
create a task on the htask board, send or ask over hmail, dispatch a worker
through hdis, run a shell command — as the principal the shared plugin
contract already names for it: `cron:<job id>` or `trigger:<id>` (§3.1), so
the actor is on every event trail the action touches. One binary, `hsched`, is
the daemon and both doors.

**Both halves are here.** A job fires on a schedule; a trigger fires on an
inbound webhook request or on a watched file changing. Beside them is the
skeleton every sibling shares — one verb registry both doors are generated
from, the four contract globals, the socket protocol, the daemon with its lock
and its tick, the JSON store with an events trail per entity, the config
document, the policy gate, and the test harness. The verbs are `job add`,
`job list`, `job remove`, `job enable`, `job disable`, `trigger add`,
`trigger list`, `trigger remove`, `trigger enable`, `trigger disable`,
`doctor`, `dump`, `events`, `parked list`, `parked resolve` and `stop`.

## Install

```sh
herdr plugin install husniadil/herdr-sched
```

Herdr runs the manifest's `[[build]]` step, which compiles `./cmd/hsched` into
`./bin/hsched` inside the installed plugin directory — the repository ships no
release artefact, and Go's build cache makes the second install fast. The
`[[startup]]` step then runs `./scripts/start.sh`, which brings the daemon up
detached and exits, which is what Herdr expects of a startup command (§2.4).

The **skill symlink is not placed for you**. Ask Herdr for the plugin root
rather than writing the hashed path out by hand, and link the skill from there:

```sh
ln -s "$(herdr plugin root herdr-sched)/skills/sched" ~/.claude/skills/sched
```

To develop against a checkout instead of an installed copy:

```sh
cd herdr-sched
herdr plugin link .
```

Herdr has no shutdown hook, so disabling or unlinking the plugin leaves the
daemon running. The `[[actions]]` entries are how it is actually turned off:
**Stop the sched daemon** and **Restart the sched daemon**, both on the
workspace context, running `./scripts/stop.sh` and `./scripts/restart.sh`.

## The two doors

Every verb is on both doors, generated from one registry
(`internal/verbs`), and a parity test enumerates both surfaces against it: a
verb on one door and not the other is a test failure rather than something an
operator discovers. §7.3 leaves nothing on the CLI alone, so a harness with no
terminal loses no verb.

On the CLI:

```sh
hsched job add nightly-sweep "0 3 * * *" task --args '{"title":"sweep the board"}'
hsched job list                   # every schedule here, and when each fires next
hsched job disable nightly-sweep  # stop it firing, without losing it
hsched doctor                     # can this plugin work at all
hsched events --follow            # the append-only trail, as it is written
hsched dump --json                # the whole store, one document
hsched parked list                # what the policy gate deferred to you
hsched parked resolve pk-… --reject
hsched stop                       # end the one daemon serving every project
hsched version                    # this binary and the contract it satisfies
```

Over MCP, wire in the same binary:

```sh
hsched mcp
```

It registers as `herdr-sched` and serves the verbs under their bare names —
`job_add`, `job_list`, `job_remove`, `job_enable`, `job_disable`, `doctor`,
`dump`, `events`, `parked_list`, `parked_resolve`, `stop` — so a
caller reads them as herdr-sched's, not as names that repeat the binary. A
dotted verb becomes an underscored tool name, and that name is a field on the
verb rather than a transformation applied at the door.

### The four globals

`--json`, `--project`, `--all-projects` and `--as` are on every verb (§3.2,
§4.2).

- `--json` prints one JSON document on stdout. A failure with it is one
  envelope carrying the contract code (§6.2); without it, a sentence goes to
  stderr and stdout stays empty.
- `--project` names one project and `--all-projects` names every one. Passing
  both is refused rather than ranked. A project is resolved in the door, not
  the daemon, because a relative path is the caller's (§4.1).
- `--as` declares a `cron:`, `trigger:` or `plugin:` principal. `agent` and
  `human` are refused: both are derived from the calling process, and a caller
  who could declare one would be declaring the fact the rule exists to keep
  underived.

`--json` and `--as` are absent from the MCP door on purpose, and `--follow`
with them: a tool call already answers with a structured document, has no pane
to borrow a principal from, and answers once, because a stream is not a tool
call. `internal/mcpdoor`'s `Globals` table records each one, and a test walks
the whole subcommand tree against it — a flag that is neither mapped nor
excluded with a reason is a failing test.

## Jobs

A job is a five-field cron expression, one action from the vocabulary below,
and a flag saying what to do about a schedule the daemon was down for:

```sh
hsched job add nightly-sweep "0 3 * * *" task --args '{"title":"sweep the board"}'
hsched job add heartbeat "*/5 * * * *" shell --args '{"command":"./bin/health"}' --catch_up
```

The expression is the standard five fields — minute, hour, day of month,
month, day of week — with `*`, `*/n`, `a-b`, `a-b/n`, comma lists, and
three-letter month and day names. Both day fields restricted is an **or**, the
way every cron reads it. The parser is `internal/cron`, a hundred lines of
bit-setting over five bounded fields, and not a dependency: the syntax has not
moved in forty years, and herdr-dispatch's README is where the rule about what
earns a dependency is written down.

**Every expression is read in UTC**, and the arithmetic is UTC alone. A local
zone would put a schedule inside the hour a DST transition either repeats or
never has, where "3am daily" means twice one night and never another. An
operator who wants a local hour writes the UTC hour it falls on.

Everything that could not fire is refused when the job is **written**: an
expression that does not parse, an action nothing can run, an argument its
kind never takes. `--args` is one JSON object, which is what makes the same
call spell the same way on both doors.

### What a missed schedule does

The daemon holds a cursor per job: the last **scheduled instant** it was
decided for, not the clock it fired at. Everything below follows from that
one field.

- A tick that finds an instant has passed fires the action **once**, for that
  instant, and moves the cursor. Ticks run far more often than any schedule,
  so a schedule fires once however many ticks pass over it.
- The cursor moves **before** the action fires, and it moves whether the
  action fired, failed or was skipped. A daemon that dies mid-action leaves a
  schedule that did not fire — which the trail says — rather than one that
  fires again on the next start.
- A schedule missed while the daemon was **down** is **skipped**, which is
  cron's own semantics and the default here. The skip is recorded on the job's
  trail and named by `hsched doctor`, so a schedule that stopped working never
  looks like one with nothing to do.
- `--catch_up` fires such a job **once** at the next start, for the latest
  missed instant. Not once per miss: firing a backlog turns one missed nightly
  sweep into a week of them at once. The instants it stood in for are recorded
  as skipped anyway.
- A job **disabled** is passed over silently and records no skip. Re-enabled,
  it fires from the next instant rather than from the backlog.

The decision is a pure function — `internal/job`.`Due` is handed the clock,
the rows and the cursors, and answers what should happen — so every case above
is pinned by a test with no `time.Sleep` and no process in it.

## Triggers

A trigger is an inbound signal carrying one action from the vocabulary below.
There are two kinds:

```sh
hsched trigger add deploy webhook shell \
  --args '{"command":"./bin/deploy"}' --cooldown 60 --max_per_hour 10
hsched trigger add inbox watch task \
  --path /var/spool/herdr/inbox --args '{"title":"something arrived"}'
```

Everything that could not fire is refused when the trigger is **written**, the
same way a job is: an id that would not survive a URL or a `trigger:<id>`
principal, a webhook that names a path, a watch that names none, an action
nothing can run.

### The webhook secret is shown once

`trigger add` answers a webhook with its URL and its HMAC secret. That answer
is the **one** time the secret exists anywhere a caller can read it. It is not
in `trigger list`, not in `dump`, and not in the event trail — the key is kept
in a file of its own, `<state dir>/sched.secrets.json`, which no door renders.
Verifying an HMAC needs the key itself, so it cannot be hashed away; where it
is kept is the only thing left that can carry the rule, and a file `dump` does
not read cannot be forgotten the way a redaction inside `dump` could.

Copy it when you see it. A secret that is lost is a trigger removed and
written again.

```sh
body='{"ref":"refs/heads/main"}'
sig=$(printf '%s' "$body" | openssl dgst -sha256 -hmac "$SECRET" -hex | awk '{print $2}')
curl -X POST http://127.0.0.1:8787/trigger/deploy \
  -H "X-Sched-Signature: sha256=$sig" -d "$body"
```

The signature is verified over the **raw body, before anything parses a byte
of it**. What the body says decides nothing: the trigger id is in the path and
the proof is in the signature, so this door never unmarshals a payload a
stranger sent. A request that does not verify is **dropped** — it fires
nothing, and it lands on the run trail naming the trigger it was aimed at,
because a URL being probed is something an operator wants to see, and a
trigger that stopped working because a caller's secret drifted otherwise looks
exactly like one nobody is calling.

The door **defaults to loopback** (`127.0.0.1:8787`, `webhook_addr` in the
config, `off` for no door at all). The trust boundary is the local user
account (§3.5), and a scheduler that fires shell commands is not a thing to
put on a network interface, so reaching it from elsewhere is meant to be a
tunnel the operator sets up deliberately.

`webhook_addr` is **not** held to loopback: an operator who writes a routable
address has said so on purpose, and refusing it would be this plugin deciding
their network for them. It is not silent either — a door that binds something
other than a loopback address says so in the log, once, naming what is now
reachable off the host. `hsched doctor` prints the address the daemon
actually got, and why it got none when a port was taken — a door that cannot
bind never stops the daemon starting, because that would take every schedule
down with one port.

### The watcher polls

The path is **absolute**, and a relative one is refused when the row is
written: it would be relative to the caller's working directory, and the
daemon that stats it is somewhere else entirely.

A `watch` trigger stats its path on the daemon's own tick and fires when the
mtime, the size or the file's existence differs from the last look. The
**first** look records and does not fire: a trigger written against a file
that already exists must not fire for a change that predates it. The same
holds for a watcher re-enabled after a spell off.

Polling rather than `fsnotify` is the dependency budget doing its job: the
daemon already has this rhythm, and the cost is that a change is noticed
within one tick rather than at once — the latency a cron job already accepts.

### Both limits refuse loudly

Every trigger carries a `--cooldown` in seconds and a `--max_per_hour`, and
both are enforced in the pure core. A cooldown is what a **replayed** webhook
request meets: the same signed body sent twice fires once and is refused the
second time. The hourly limit counts firings inside the last hour and forgets
what falls out of it.

A refusal is never silent. It lands on the run trail as `sched.run.limited`
with the rule that refused and how long is left, and the caller is answered
`429` with the same words. A firing that vanished is indistinguishable from
one that never arrived.

Both decisions are made **under the store's lock**, and the cursor moves
before the action fires. The webhook door is one HTTP server answering any
number of connections, so two requests in the same millisecond would otherwise
both read a spent cooldown as unspent.

## The action vocabulary

An action is data on a job or a trigger row — a kind and its arguments — and
there are four kinds: `task` files one on the htask board, `mail` sends a
notify or an ask through hmail, `dispatch` brings a worker up through hdis,
and `shell` runs a command on the host. An unknown kind, a missing argument or
an argument of the wrong type is refused when the row is **written**, not when
it fires: a schedule that fails at 3am fails in a log nobody reads.

Every sibling call shells out to that sibling's CLI with `--json` and never
opens its socket, so each daemon stays the only writer of its own store, and
every call declares `--as cron:<job>` or `--as trigger:<id>` (§3.2) so the
actor on the sibling's own trail is the schedule rather than "some plugin".
The pane is scrubbed out of the environment for the same reason: a call that
carried one would be attributed to this daemon instead.

A shell action runs **detached** from the tick, with its output captured onto
the run, so a slow command holds up no other schedule. Nothing is queued for
later and nothing is retried. A sibling that is unreachable is a loud failed
run on the trail carrying that sibling's own words and its §6.3 code, never a
silent skip: a schedule that quietly stopped working looks exactly like one
with nothing to do.

## The event trail

Every entity in the store keeps its own trail beside it, `<entity>_events`,
saved in the same write: a change and the event recording it can never land
one without the other. Today that is the actions the policy gate parked, in
`parked_events`, the cron jobs in `job_events`, and the runs a signal's
actions fired, in `run_events`. A trigger arrives the same way, with its own
sibling trail.

A run is a trail with no list of rows beside it, and deliberately so: the run
history IS the §8 stream, and a second table holding the same facts in another
shape is one more thing to keep in step.

An event is §8.1's shape: `{id, at, actor, project, entity, kind, detail}`
plus the name the parts spell out, `sched.<entity>.<kind>`. `hsched events`
reads the merged trail oldest first; `--since` takes an event id or a Unix
millisecond and resumes strictly after it. Each entity's trail keeps its
newest 1000 events, because the whole document is rewritten on every change,
and an id that has rotated past is **refused** rather than answered with the
whole window — a consumer resuming from it would otherwise take the window for
its own tail.

`hsched events --follow` is the subscription (§8.2). It is handed the backlog
and then every event as it is written, and the daemon says when the stream is
over, so a follower can tell "there is no more" from "I stopped being told".

### The event hook

`on_event` in the config is handed every event on stdin, run detached with its
output going nowhere. It is the operator's own program: this daemon does not
wait on it and never reads what it says. `hsched doctor` reports whether one
is configured, which is the only way to tell a hook that never fires from no
hook at all.

## The policy gate

Every verb that changes the world passes one gate before doing anything
(§9.1). This build gates nine:

```
sched.job.add
sched.job.remove
sched.job.enable
sched.job.disable
sched.trigger.add
sched.trigger.remove
sched.trigger.enable
sched.trigger.disable
sched.stop
```

`parked resolve` writes and is deliberately **not** gated: resolving a
deferral is the answer to a gate that already spoke, and gating it would let a
gate park its own resolution and strand every deferred action. The registry
requires that reason to be written down beside the verb, so an ungated write
is never reached by omission.

The gate is a command, configured in `gate_command`. It reads
`{"subject","verb","target"}` on stdin and prints `{"decision","reason"}`,
where the decision is `allow`, `deny` or `defer`. An unconfigured gate allows
(§9.2). Anything else fails **closed**: a non-zero exit, no answer, an
unreadable answer, an oversized answer or a timeout is a deny with the reason
saying which.

A gate that answers `defer` **parks** the call (§9.3) — recorded, not
performed — and the caller is refused with `DENIED` carrying the `parked_id`.
`hsched parked list` is where those rows are read. Resolving one re-runs the
verb under the subject the gate stopped, never the resolver's, and does not
ask the gate again, because the resolution is the decision the gate deferred.
The row records who decided it. A resolved action whose verb then failed stays
decided and stays visible: an action that errored is not proof it had no
effect, so the operator reads why and decides again.

## Configuration

One config path per plugin, and no other: `~/.config/sched/sched.toml`, or
`$XDG_CONFIG_HOME/sched/sched.toml`, or `$SCHED_CONFIG_DIR/sched.toml`. A file
under a different directory or a different extension is a leftover, not an
alternative. `hsched doctor` prints the path it resolved and whether a file was
there.

It is read **once**, at daemon start. A change takes effect on the next start.

```toml
# How often the daemon's bounded timer runs (§11.5). Positive seconds.
tick_seconds = 30

# The §9.2 policy gate. An array, always: a bare string would assign nothing
# and read as unconfigured, which is every verb allowed by a typo, so it is
# refused at load.
gate_command = ["/usr/local/bin/herdr-policy"]

# The §8.3 hook, handed every event on stdin, run detached.
on_event = ["/usr/local/bin/notify-me"]

# Where the inbound webhook door listens. Loopback, and only loopback: the
# trust boundary is the local user account. `off` is no inbound door at all,
# which is what a fleet with only file watchers wants.
webhook_addr = "127.0.0.1:8787"
```

Every key takes an environment override, spelled `SCHED_<KEY>`:
`SCHED_TICK_SECONDS`, `SCHED_GATE_COMMAND`, `SCHED_ON_EVENT`,
`SCHED_WEBHOOK_ADDR`. An override beats the file.

### Where everything lives

The short name is `sched`, and everything nameable is derived from it:

| What | Where |
| --- | --- |
| Config | `${XDG_CONFIG_HOME:-~/.config}/sched/sched.toml` |
| State dir | `${XDG_STATE_HOME:-~/.local/state}/sched`, mode `0700` |
| Socket | `<state dir>/sched.sock` |
| Lock | `<state dir>/sched.lock` |
| Log | `<state dir>/sched.log` |
| Store | `<state dir>/sched.json` |
| Webhook secrets | `<state dir>/sched.secrets.json`, mode `0600` |

`SCHED_STATE_DIR` and `SCHED_CONFIG_DIR` override the two directories and take
precedence over the XDG variables. They are how the tests isolate, and how an
operator asks for a second store on purpose rather than by accident.

Herdr injects `HERDR_PLUGIN_STATE_DIR` and `HERDR_PLUGIN_CONFIG_DIR` into what
*it* spawns and into no managed pane, so this plugin reads neither: honouring
them would give one plugin two stores that never see each other's rows. What
`doctor` does instead is **name** the directories a store could have been left
in by a build that did read them. It never names the store in use, and it
never deletes anything.

## Reading the store

The contract's §5.1 store is SQLite. This is a JSON document instead: the
whole set is a handful of rows held in memory anyway, rewritten whole on every
change, read by one process under the daemon's own lock. A SQLite driver is a
large dependency for a file that never needs a query, a schema or a second
reader, and the budget below is what makes that a decision rather than a
default. The document is written through a temp file and renamed, so a crash
mid-write leaves the previous one intact, and a document from a version this
binary does not know is refused rather than overwritten.

`hsched dump --json` is §5.8: the whole store in one document, with the file it
is written to named, so a reader who wants it without this binary knows where
to look.

## Building and testing

```sh
make build          # bin/hsched
make test           # the loop, in seconds: -short, nothing spawned
make test-full      # the gate: the above plus go vet, a cross-compile vet, -race
make e2e            # layer 3: the shipped binary through its own scripts
make release-check  # test-full, build, and e2e with the skip turned into a failure
make install        # go install ./cmd/hsched
```

`make test-full` is the gate: nothing is committed on a green `make test`
alone. `make e2e` is out of the gate on purpose — it builds and runs the
shipped binary and its scripts, and it skips loudly naming what was missing
rather than passing quietly. `SCHED_E2E_REQUIRED=1` turns that skip into a
failure, which is what `make release-check` does before a release tag.

Tests never reach the operator's live Herdr, boards, mailbox, config or state
(§12.3). `internal/testenv` replaces `PATH` with a directory of stand-in
sibling binaries — replacing it rather than prepending to it, so a call whose
fake was never written fails as "not found" instead of quietly reaching the
real thing — and points the state and config directories at temporary ones.

## Dependencies

The standard library, plus the two libraries every sibling pins, at the
versions herdr-dispatch pins them:

- `github.com/spf13/cobra` for the CLI door
- `github.com/modelcontextprotocol/go-sdk` for the MCP door

There is no TOML library — `internal/config/toml.go` is a hand-written parser
for the subset this document needs — no config library, no logging library,
and no SQLite driver. The contract is **not vendored**: it lives in
herdr-tasks, and every `§` in this repository cites it by section number.

## Licence

MIT. See [LICENSE](LICENSE).
