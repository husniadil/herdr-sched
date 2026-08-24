# herdr-sched

The scheduler and trigger plugin for a [Herdr](https://herdr.dev) fleet,
sibling to [herdr-tasks](https://github.com/husniadil/herdr-tasks),
[herdr-dispatch](https://github.com/husniadil/herdr-dispatch) and
[herdr-mail](https://github.com/husniadil/herdr-mail). One Go binary
(`hsched`, short name `sched` in the shared plugin contract) that fires a cron
schedule or an inbound trigger into the sibling plugins.

The contract this plugin satisfies is **not vendored here**. It lives in
herdr-tasks at `docs/contract.md`, and every `§` in this repository cites it
by section number. Read it before changing any seam it names, and record a
knowing divergence in `docs/contract-notes.md` rather than leaving it to be
discovered.

**This build is the common foundation only.** No job, no trigger, no action
verb. What is here is the skeleton: one verb registry, both doors, the socket
protocol, the daemon, the JSON store, the config, the policy gate, the test
harness.

## Commands

- `make test` — the fast loop, seconds: the pure decision core and the payload
  shapes, nothing spawned. Run it on every edit.
- `make test-full` — **the gate**, and what CI runs: the above plus every case
  that starts a daemon, walks the socket or shells out to a fake, with `-race`
  and a cross-compile vet of the other supported platform. Run it before every
  commit.
- `make e2e` — layer 3: the shipped binary through its own scripts. Out of the
  gate on purpose; run it before a release tag and whenever a door, a script
  or the manifest moves.
- `make build` / `make install` — `./cmd/hsched`.

A green `make test` is not a green gate. Nothing is committed on it alone.

## Principles

- **One registry, two doors.** Every verb is generated from
  `internal/verbs` — the CLI subcommands and the MCP tools both. A verb on one
  door and not the other is a test failure. §7.3 leaves nothing on the CLI
  alone, so a harness with no terminal loses no verb, and withholding a verb
  from a principal is the §9 gate's job and never a door's.
- **An absence is not a decision until it is written down.** A verb that
  passes no gate name says why in `Ungated`. A CLI flag with no place on the
  MCP door says why in `mcpdoor.Globals`. A verb the standard's parity list
  names and this repo does not carry says why in `docs/contract-notes.md`.
  Each of the three has a test that fails when the reason is missing.
- **The daemon is the only writer.** Both doors are thin clients that hold
  nothing: an MCP door is spawned once per client session, so anything kept
  there would be one of several disagreeing sets. One daemon per user, elected
  by a lock, is what makes one answer true across every caller.
- **Shell out to the siblings, never into their stores.** This plugin calls
  `htask`, `hmail` and `hdis` through their CLIs with `--json`, the way hdis
  already calls htask. It never opens another plugin's socket and never reads
  another plugin's file: each daemon stays the only writer of its own store.
- **The gate fails closed.** Unconfigured allows; anything that is not a
  well-formed answer is a deny. A `defer` parks the call rather than
  performing it, and resolving one re-runs the verb under the subject the gate
  stopped, never the resolver's, without asking the gate again.
- **Fail loud, idle safe.** When a sibling is unreachable, say so and keep
  ticking. Never guess at state, never queue writes for later, and never
  describe a mitigation as a fix.

## The store

A JSON document, not SQLite, and the reason is in the README and in
`docs/contract-notes.md`. Two rules travel with it:

- **Every entity has its own trail beside it**, `<entity>_events`, written in
  the same save. Today that is `parked` and `parked_events`. A new entity
  brings its own; nothing is split out of a shared trail later.
- **The document is written whole**, through a temp file and a rename, so a
  change and the event recording it can never land one without the other, and
  a crash mid-write leaves the previous document intact.

## The sibling repo standard

This repo is the fourth of the Herdr plugins maintained as one discipline.
`docs/repo-standard.md` **in the herdr-dispatch checkout** is where that shape
is written down: what the short name governs on disk, the internal package
names, the one verb registry both doors are built from, the Makefile targets,
and the README shape. Read it before adding a verb, a package, or a Makefile
target, and file a delta on the owning repo's board rather than diverging
quietly.

## Conventions

- Test-first for every behavior in a decision core: the test exists and fails
  before the code that makes it pass.
- No `panic` in production code paths; errors carry what the operator needs to
  act.
- Dependency budget: the standard library plus `spf13/cobra` and the MCP
  go-sdk, at the versions herdr-dispatch pins. Anything else earns its way in
  with the reason recorded in the README.
- Lowercase conventional commits, no emojis, no co-author lines.
- Everything committed is English. Tests, docs, comments, commit messages.
