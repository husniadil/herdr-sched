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
the same day when it was implemented, and again when the §3.7 paneless
spellings it left owed were closed.

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

## §13 parity — `status` and `sweep` are not here yet

The repo standard's parity list is `doctor · status · stop · dump · events ·
parked.list · parked.resolve`, with `sweep` wherever the daemon has a
reconciliation pass to run on demand. This build carries five of the seven and
neither of the other two.

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
by carrying nothing, which is what it means. Nothing here diverges from §3
any more.

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
