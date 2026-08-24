# herdr-sched

herdr-sched is the scheduler and trigger plugin for a Herdr fleet. A cron
schedule or an inbound trigger fires an action into the sibling plugins —
create a task on the htask board, send or ask over hmail, dispatch a worker
through hdis, run a shell command — as the principal the shared plugin
contract already names for it: `cron:<job id>` or `trigger:<id>` (§3.1), so
the actor is on every event trail the action touches. One binary, `hsched`, is
the daemon and both doors.

**Nothing schedules or listens yet.** There is no job, no trigger and no
action verb; what is here is the skeleton every sibling shares, plus the
action vocabulary those two will fire — one verb registry both doors are generated from, the four contract
globals, the socket protocol, the daemon with its lock and its tick, the JSON
store with an events trail per entity, the config document, the policy gate,
and the test harness. The verbs are `doctor`, `dump`, `events`,
`parked list`, `parked resolve` and `stop`.

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
`doctor`, `dump`, `events`, `parked_list`, `parked_resolve`, `stop` — so a
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
`parked_events`, and the runs a signal's actions fired, in `run_events`. A job
and a trigger arrive the same way, each with its own sibling trail.

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
(§9.1). This build gates one verb:

```
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
```

Every key takes an environment override, spelled `SCHED_<KEY>`:
`SCHED_TICK_SECONDS`, `SCHED_GATE_COMMAND`, `SCHED_ON_EVENT`. An override beats
the file.

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
