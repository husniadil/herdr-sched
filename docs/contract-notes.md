# Where this plugin and the contract disagree

The shared plugin contract is **not vendored here**. It lives in agamemnon
(`docs/contract.md`), with identical copies in herdr-tasks, herdr-mail and herdr-dispatch,
and every `§` in this repository cites it by section number. This file is the
other half of that: where this plugin knowingly does something the contract
does not say, or does not yet do something the contract does say, with the
reason. A divergence that is written down is a decision; one that is not is a
bug nobody has found yet.

Recorded 2026-08-24, against contract 0.10.0 and the repo standard as audited
that day. Amended 2026-08-25 with the cron half, and again the same day with
the trigger half. Amended 2026-08-27 with the §7.5 operator declaration, again
the same day when it was implemented, again when the §3.7 paneless spellings it
left owed were closed, and again with what a re-read of the whole contract
found. Amended again the same day against contract **0.10.1**, which closes
three of these entries by saying what this plugin already did — §13.2's short
name, §8.4's dot spelling and §5.4's ids — while §4.4's `parked.list` and
§3.7's operator mark were closed by fixing the code.

## §5.1 — the store is JSON, not SQLite

The contract's store is SQLite. This is a JSON document at
`<state dir>/sched.json`.

The whole set is a handful of rows held in memory anyway, rewritten whole on
every change, read by one process under the daemon's own lock. It never needs
a query, a schema or a second reader. A SQLite driver is a large dependency
for that, and the budget the repo standard sets is the standard library plus
the two libraries every sibling pins.

herdr-dispatch made the same call for the same reason, so this is the
established pattern between siblings rather than a new one. It is a decision
to revisit when this plugin grows a set that wants querying — a run history
long enough to page through, say — and note 2 settles that it will not: the
run history per job or trigger IS the §8 event stream, and there is no second
table.

What the document does carry from day one is the shape the siblings retrofitted
late: **every entity has its own trail beside it**, `<entity>_events`, saved in
the same write. Today that is `parked`/`parked_events`, `jobs`/`job_events` and
`triggers`/`trigger_events`, with `run_events` as the trail that has no rows
beside it.

One rule written for that SQLite store goes with it. There is no `meta` table
and no `created_at` (§5.2): the document carries a `version` and nothing else,
because a whole-file rewrite migrates by being read into the current shape
rather than by a numbered step. The exit condition is this note's own — a store
that wants querying.

The ids are no longer a divergence. Contract 0.10.1 binds §5.4's 26-char ULID
rule to a SQLite store and says what a JSON-document store mints instead: a
time-sortable text id with a millisecond prefix, and an operator-named entity
keeps that name as its id. That is what this store already had — `pk-` and
`ev-` plus the millisecond and eight random hex digits
(`internal/store/parked.go`, `internal/store/events.go`), and a job or trigger
named by the operator, because the name is the id half of the `cron:<job id>`
and `trigger:<id>` principals their calls declare. Closed.

## §5.1 again — the webhook secrets are a second file

One thing is deliberately outside the document: a webhook's HMAC secret. It
lives in `<state dir>/sched.secrets.json`, mode `0600`, written whole through
the same temp-file-and-rename the document is.

Note 2 settles that a webhook's secret is shown once at creation and never
listed again. Verifying an HMAC needs the key itself, so it cannot be hashed
away, which leaves **where it is kept** as the only thing that can carry the
rule. `dump` renders the store document; a redaction inside `dump` would be one
line anybody could later forget, and a file `dump` does not read cannot be
forgotten. Every door renders the document and nothing else, so no door can
print a secret. `TestNoSecretReachesListGetDumpOrTheTrail` drives every read a
caller has, and the store file on disk besides.

The cost is that writing a webhook is two writes rather than one, which is
exactly the property the "written whole" rule exists to give. The secret is
written **first**, so a crash between them leaves a key with no trigger — inert,
and counted by `doctor` as an orphan — rather than a webhook nobody holds a key
for. A duplicate id is refused before a key is drawn, so a refused write can
never replace a live trigger's secret.

## §5.7 — remove is a hard delete, the trail stays

§5.7 hard-deletes only a row that never left its initial state. `job remove`
and `trigger remove` drop the row whatever it has done
(`internal/store/jobs.go`, `internal/store/triggers.go`), including one that
has fired.

A schedule row is a definition rather than a record: it says what should
happen, and removing it says it should not happen any more. What it DID is not
in the row — it is the entity's own trail, `job_events` and `trigger_events`
and the runs in `run_events`, and none of that is touched by a remove. So
nothing a reader could want is lost; what is gone is a definition the operator
asked to stop.

The exit condition is an operator asking for a removed schedule back. That
wants an archived state on the row, not a resurrection from the trail.

## §4.1 — the project key is not always the git common dir's parent

§4.1 resolves a project to the parent of the git common dir. This plugin takes
that parent only when the common dir IS a `.git`, and asks git for the working
tree directly otherwise.

The contract's rule is right for the case it was written for: an ordinary clone
and every linked worktree of it report `<repo>/.git`, whose parent is the
repository — which is what makes a worktree and its main checkout one project,
and that behaviour is unchanged here. It is wrong in two shapes the rule does
not name:

- In a **submodule** the common dir is `<super>/.git/modules/<name>`, so the
  parent is `<super>/.git/modules`. That is a path inside git's own internals,
  it is SHARED by every submodule of that superproject, and neither the
  superproject nor the submodule can reach it from where it stands. Every
  submodule of one superproject would answer to one project key, and schedules
  written in two of them would read each other's rows.
- With **`--separate-git-dir`** the common dir is wherever the operator put it,
  so its parent has nothing to do with the working tree. Kept together, which
  is the usual reason to use the flag, every clone under that directory
  collapses into one project.

So `internal/project`.`gitCommonRoot` takes the parent only for a literal
`.git`, and otherwise reads `--show-toplevel`. A bare repository has no working
tree and falls through to the documented not-a-repository answer, which is the
directory itself. Symlinks are resolved either way, as §4.1 says.

This is a divergence in the rule's SPELLING and not in its intent: the project
is still the repository a directory belongs to, one key per repository, stable
across worktrees. The exit condition is §4.1 itself being amended to say
"working tree" rather than "the common dir's parent", at which point this entry
is a note about a fix rather than a divergence.

## §4.4 — `parked.list` is project-scoped (closed)

Closed 2026-08-27. `parked.list` handed back `Store.Parked()` whole, so an
operator in one repository was shown every project's deferred actions. It now
filters on the resolved project the way `listJobs` does
(`internal/daemon/daemon.go`), and it refuses `--all-projects` with `USAGE`
rather than honouring it: contract 0.10.1 names `parked.list` the one entity
list verb that does not take the flag, because a parked action is resolved
where it was parked, by an operator acting in that project.
`TestParkedIsListedInTheProjectItWasParkedIn` and
`TestParkedIsNotListedAcrossEveryProject` hold both halves.

## §13 parity — `status` and `sweep` are not here yet

The repo standard's parity list is `doctor · status · stop · dump · events ·
parked.list · parked.resolve`, with `sweep` wherever the daemon has a
reconciliation pass to run on demand. This build carries six of the seven —
`status` is the one it does not — and `sweep` is the other verb missing.

`status` answers what a plugin is DOING. The reason it was held back — half a
plugin would answer it in a shape that has to change once the other half lands —
no longer holds now that both halves are here, and what remains is a decision
about what it should say rather than a missing piece. It is deliberately not
being made inside the trigger work: the shape `status` takes is the shared one
across four siblings, and settling it as a side effect of landing triggers is
how one plugin's `status` comes to disagree with the other three. `hsched
doctor` carries the job counts, the skipped-at-start list, the trigger counts by
kind and the inbound door's address in the meantime, which is the part an
operator actually asks for. **This is the open one.**

`sweep` is a reconciliation pass on demand, and the standard is explicit that a
plugin which owns no reconciliation does not grow one to match. Neither half has
one: the cursor on a job row and the stamp on a trigger row ARE the
reconciliation, re-read from the store and acted on at every start. There is
still nothing to reconcile until something can be bound to a pane that goes
away.

Both land with the work that gives them something to say. The registry test
`TestTheCommonVerbsAreAllPresent` asserts their absence, so adding either is a
deliberate edit to that test rather than a silent divergence closing itself.

## The cron expression is UTC, and there is no timezone field

The contract says nothing about how a schedule reads its clock; this plugin
reads every expression in UTC and offers no per-job zone.

A local zone puts a schedule inside the hour a DST transition either repeats or
never has, where "3am daily" means twice on one night of the year and never on
another. There is no answer to that an operator would not have to be told
about, and the honest thing is to refuse the question rather than pick one of
the two wrong answers silently. `internal/cron` therefore converts to UTC on
the way in, and the README and the skill both say so where an operator writing
a job will read it.

The exit condition is a per-job `timezone` field, which is a real request the
moment a fleet spans two of them. It arrives with the ambiguous-hour rule
written down beside it — which of the two 02:30s fires, and what a skipped
02:30 does — rather than as a field that inherits Go's own answer by accident.

## The five-field parser is written here rather than pulled in

`robfig/cron` is the obvious dependency and is not taken. The parser is under
two hundred lines over five bounded fields against a syntax that has not moved
in forty years, it has no runtime, no goroutine and no state, and the budget
this repo keeps is the standard library plus the two libraries every sibling
pins. A library would also bring its own scheduler, which is the half this
plugin already owns and does differently: the cursor is on the row and is
re-read from the store at every start, which is what makes a missed schedule a
decision rather than a gap in a timer's memory.

## §7.5 — the operator declaration, and the §3.7 spelling that came with it

§7.5 is implemented as of 2026-08-27. `hsched mcp --operator` is the one route
and there is no other: the flag is on the `mcp` command alone, read once at
start into `mcpdoor.Options`, sent on every request as `Operator`, and
`operator` is refused BY name as a tool argument with `USAGE`.
`protocol.Request.Caller` reads `--as`, then the pane, then the declaration, so
an agent that starts a declared door gains nothing by it, and `Serve` refuses
to start a declared door carrying `HERDR_PANE_ID` with `FORBIDDEN`. Both halves
are pinned, the ordering and the startup refusal, because the half that exists
to reassure a reader is the half a reader stops checking.

The other two paneless answers §3.7 spells are closed as of 2026-08-27, and
they landed together because either alone is worse than neither: an undeclared
paneless caller is now the literal `none` of §3.1, and a paneless CLI
invocation is `human` on the argv that ran it (§3.6), which is the deliberate
human act §3.7 requires a paneless `human` to point at. The CLI door says so
on every request it builds — one process per call, so its argv IS that act —
and `Caller` reads `--as`, then the pane, then that act, so neither route
outranks a pane. `parked resolve` reproduces a `human` subject through the
same act rather than through `--as human`, and carries a `none` subject back
by carrying nothing, which is what it means.

§3.7's other half is implemented as of 2026-08-27. `parked resolve` is an
operator verb: it records the calling principal as the actor, and when that
principal is not `human` the event's `detail` carries
`on_behalf_of_operator` — `store.OperatorVerb`, the key the siblings already
spell that way, so a consumer reading four trails learns one name for one fact.
The `failed` event a resolution's re-run writes when the verb errors carries
the same mark beside its `error` detail, because that failure is the same
operator verb the resolution was, and a trail that marked the decision alone
would name the operator's authority only where the verb happened to succeed.
All four halves are pinned:
`TestResolvingByAnAgentIsMarkedAsTheOperatorsVerb` and
`TestFailingAfterAnAgentResolvesIsMarkedAsTheOperatorsVerb` fail when the mark
is dropped, and `TestResolvingByTheOperatorCarriesNoMark` and
`TestFailingAfterTheOperatorResolvesCarriesNoMark` fail when it is applied to
the operator's own resolution, which would make the mark say nothing about who
acted.

## §8.3 — the event hook reads JSON on stdin

§8.3 runs the hook detached with all three stdio closed and hands it the event
in `SCHED_EVENT`, `SCHED_ENTITY`, `SCHED_ID`, `SCHED_PROJECT` and
`SCHED_ACTOR`. This daemon sets none of those: it writes the whole event as one
JSON line on the hook's stdin and closes it (`internal/daemon/events.go`).

The env-var list is a lossy projection of the event. `detail` is verb-specific
JSON and has nowhere to go in it, and it is the half a hook actually reacts to
— which job fired, what it answered. One JSON document is the shape the event
already has, is the same shape `events --json` prints, and grows a field
without the hook contract changing. Everything else §8.3 asks for holds:
detached, `setsid`, stdout and stderr closed, and a hook that fails or hangs
changes nothing about the write that caused it.

The exit condition is §8.3 amended to hand the event over as JSON, or a sibling
hook script that needs the variables — at which point they are set BESIDE the
stdin document rather than instead of it.

## §8.4 — a manifest hook matches Herdr's dot spelling (closed)

Closed by contract 0.10.1, which splits the spelling in two: a manifest
`[[events]]` hook names the event in Herdr's dotted hook spelling, and the
schema's underscore spelling stands everywhere a plugin names the event to
itself. `herdr-plugin.toml` registers `pane.closed` and `pane.exited` because
Herdr compares `hook.on` against `event.dot_name()` (herdrdev/herdr,
`src/app/api/plugins/runtime.rs:219`), so the underscore spelling there matches
nothing and the hook never runs.

## §8.4 — the pane-gone hook is wired and does nothing

`scripts/on-pane-gone.sh` is registered for `pane.closed` and `pane.exited`,
and it exits 0 without acting. Nothing is bound to a pane yet.

It is wired from day one because the moment a trigger can be bound to a pane,
the change is one command in that script rather than a manifest edit every
installed copy has to be re-installed to pick up. §8.4 makes the hook fire for
every pane in the session rather than only the ones a plugin knows, so whatever
it grows into must be self-filtering and idempotent by construction.

## §10.1 — the config is read once, at startup

The contract permits a reload; hmail re-reads on SIGHUP. This daemon reads its
document once, at start, and holds it.

There is nothing here yet whose change an operator would want to take effect
without a restart, and a config that is read twice is a config that can be two
things at once inside one process. `hsched doctor` prints the path it resolved
and whether a file was there, so an operator editing a file that is not taking
effect can see which of the two it is. This is the entry to revisit first when
a schedule can be edited in the file: a reload that restarts a timer is a
different thing from a reload that swaps a gate command.

## §10.3 and §11 — no Herdr call, so no Herdr line in doctor

§10.3 makes `doctor` print Herdr reachability and the Herdr schema or protocol
it saw. `DoctorReport` (`internal/daemon/daemon.go`) has neither field, because
§11.1 and §11.2 are unimplemented: nothing here resolves `HERDR_BIN_PATH` and
nothing reads `herdr api schema --json` at daemon start.

Nothing this plugin does needs Herdr yet. A cron expression is arithmetic, a
webhook is a socket, and every action shells out to a SIBLING's CLI rather than
to Herdr. The one Herdr fact it uses — the pane a caller stands in — arrives in
the environment Herdr already injected, which is a read and not a call. A
reachability line that reported on a binary this daemon never runs would be a
health check for nothing, and feature detection against a schema no request is
built from would answer a question nobody asked.

The exit condition is the first verb that needs a Herdr capability — binding a
trigger to a pane is the likely one. It brings §11.1 and §11.2 with it, and the
`doctor` line lands in the same change, reporting on a call that is really
made.

## §13.2 — the short name is `sched` (closed)

Closed by contract 0.10.1, which corrects §13.2's fourth short name to `sched`
and adds `hsched` to §14's binary abbreviations. `sched` is what the code
always was — every path, the `SCHED_` env prefix, the `sched.*` gate names and
the `sched.*` event names derive from it — so nothing moved, and the citations
in `internal/verbs/verbs.go`, `internal/verbs/verbs_test.go` and
`internal/config/config_test.go` cite §13.2 itself again rather than pointing
here.

## §11.7 — a plugin sweeps a pane Herdr no longer lists

**Closed, nothing to do here.** 0.11.0 adds §11.7, a clause on plugins that
hold leases: a `plugin` principal may release the leases of a pane Herdr no
longer lists. This plugin holds no leases, so only the declared revision moves.
