# Where this plugin and the contract disagree

The shared plugin contract is **not vendored here**. It lives in herdr-tasks,
and every `§` in this repository cites it by section number. This file is the
other half of that: where this plugin knowingly does something the contract
does not say, or does not yet do something the contract does say, with the
reason. A divergence that is written down is a decision; one that is not is a
bug nobody has found yet.

Recorded 2026-08-24, against contract 0.10.0 and the repo standard as audited
that day.

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
the same write. Today that is `parked` and `parked_events`. A job and a trigger
arrive the same way, and neither has to be split out of a shared trail later.

## §13 parity — `status` and `sweep` are not here yet

The repo standard's parity list is `doctor · status · stop · dump · events ·
parked.list · parked.resolve`, with `sweep` wherever the daemon has a
reconciliation pass to run on demand. This build carries five of the seven and
neither of the other two.

`status` answers what a plugin is doing. This one is not doing anything yet:
there is no job, no trigger and nothing bound. A `status` that answers an
empty document teaches a caller a shape it will have to unlearn the moment
there is something to report, and an agent that reads "nothing" from a verb
that cannot say anything else has been told less than nothing.

`sweep` is a reconciliation pass, and the standard is explicit that a plugin
which owns no reconciliation does not grow one to match. There is nothing to
reconcile until a schedule can be missed or a trigger can be bound to a pane
that goes away.

Both land with the work that gives them something to say. The registry test
`TestTheCommonVerbsAreAllPresent` asserts their absence, so adding either is a
deliberate edit to that test rather than a silent divergence closing itself.

## §2.1 — no `[[panes]]`

§2.1 as amended owes a plugin pane only where the plugin's concern includes an
operator-facing view. This build has no schedule to show on one. `hsched
doctor` and `hsched events` are its human surface until it does, and shipping
an empty view now would be a promise of a list that is not there.

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
