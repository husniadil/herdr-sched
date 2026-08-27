# herdr-sched

The scheduler and trigger plugin for a [Herdr](https://herdr.dev) fleet,
sibling to [herdr-tasks](https://github.com/husniadil/herdr-tasks),
[herdr-dispatch](https://github.com/husniadil/herdr-dispatch) and
[herdr-mail](https://github.com/husniadil/herdr-mail). One Go binary
(`hsched`, short name `sched` in the shared plugin contract, §13.2) that fires
a cron schedule or an inbound trigger into the sibling plugins.

The contract this plugin satisfies is **not vendored here**. It lives in
agamemnon at `docs/contract.md`, with identical copies in herdr-tasks,
herdr-mail and herdr-dispatch, and every `§` in this repository cites it by
section number. The revision this binary claims is declared beside its own
version in `internal/version/version.go` (§13.4). Read the contract before
changing any seam it names, and record a knowing divergence in
`docs/contract-notes.md` rather than leaving it to be discovered.

**Both halves are here.** Jobs fire on a schedule; triggers fire on an inbound
webhook request or on a watched file changing. Beside them is the skeleton: one
verb registry, both doors, the socket protocol, the daemon, the JSON store, the
config, the policy gate, the test harness.

## Commands

- `make test` — the fast loop, seconds: gofmt checked rather than applied,
  then the pure decision core and the payload shapes, nothing spawned. Run it
  on every edit.
- `make test-full` — **the gate**, and what CI runs: the above plus every case
  that starts a daemon, walks the socket or shells out to a fake, with `-race`
  and a cross-compile vet of the other supported platform. Run it before every
  commit.
- `make e2e` — layer 3: the shipped binary through its own scripts, against a
  throwaway state dir. Out of the gate on purpose; run it whenever a door, a
  script or the manifest moves. A machine that cannot build the binary gets a
  loud skip, never a silent pass.
- `make release-check` — `test-full`, a build, and that same layer 3 with
  `SCHED_E2E_REQUIRED=1`, which turns the skip into a failure. This is what
  goes before a release tag, and it is the target the siblings spell the same
  way.
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
  The first two have a test that fails when the reason is missing; the third
  is `TestTheCommonVerbsAreAllPresent`, which asserts the absence, so serving
  the verb is a deliberate edit to that test rather than a divergence closing
  itself in silence.
- **The daemon is the only writer.** Both doors are thin clients that hold
  nothing: an MCP door is spawned once per client session, so anything kept
  there would be one of several disagreeing sets. One daemon per user, elected
  by a lock, is what makes one answer true across every caller.
- **The config is read once, at startup.** The contract permits a reload and
  hmail does one; this daemon holds the document it read, so an edit takes
  effect at the next start and `doctor` prints the path it resolved and
  whether a file was there. `SCHED_STATE_DIR` and `SCHED_CONFIG_DIR` are the
  only directory overrides: Herdr's injected plugin dirs are deliberately not
  read, because honouring them would give one plugin two stores (§5.1, §10.1).
- **Shell out to the siblings, never into their stores.** This plugin calls
  `htask`, `hmail` and `hdis` through their CLIs with `--json`, the way hdis
  already calls htask. It never opens another plugin's socket and never reads
  another plugin's file: each daemon stays the only writer of its own store.
- **The gate fails closed.** Unconfigured allows; anything that is not a
  well-formed answer is a deny. A `defer` parks the call rather than
  performing it, and resolving one re-runs the verb under the subject the gate
  stopped, never the resolver's, without asking the gate again. A parked row
  belongs to the project it was parked in: `parked list` answers that
  project's rows and refuses `--all-projects` with USAGE (§4.4), and a
  resolution decided by anyone but the operator is marked on its event as
  performed on their behalf (§3.7).
- **Fail loud, idle safe.** When a sibling is unreachable, say so and keep
  ticking. Never guess at state, never queue writes for later, and never
  describe a mitigation as a fix.
- **A schedule fires once per instant, never twice.** The cursor on a job row
  is the last SCHEDULED instant it was decided for, it moves before the action
  fires, and it moves whether the action fired, failed or was skipped. Cron
  arithmetic is UTC and only UTC.
- **Nothing unverified is ever parsed.** The webhook door reads the raw body,
  checks the HMAC over it, and only then does anything else happen. What the
  body SAYS decides nothing: the trigger is named in the path and the proof is
  in the signature. An unverified request is dropped onto the run trail naming
  the trigger, and fires nothing.
- **A webhook secret is shown once and lives outside the store.** `trigger add`
  answers it; nothing else ever does. It is kept in its own file, so no door
  that renders the store document can render a secret — the rule is carried by
  where the key IS rather than by a redaction someone can forget.
- **A watch path is absolute.** A relative one is relative to the CALLER's
  working directory, and the daemon that stats it is somewhere else: a watcher
  on the wrong file looks exactly like a watcher on a file that never changes.
- **A limit refuses loudly.** A cooldown or an hourly limit that holds a signal
  down lands on the run trail and answers the caller. A firing that vanished
  is indistinguishable from one that never arrived. Both decisions are made
  under the store's lock, and the cursor moves before the action fires.

## The store

A JSON document, not SQLite, and the reason is in the README and in
`docs/contract-notes.md`. Three rules travel with it:

- **Every entity has its own trail beside it**, `<entity>_events`, written in
  the same save. Today that is `parked`/`parked_events`, `jobs`/`job_events`
  and `triggers`/`trigger_events`, with `run_events` as the trail that has no
  rows beside it. A new entity brings its own; nothing is split out of a shared
  trail later.
- **The document is written whole**, through a temp file and a rename, so a
  change and the event recording it can never land one without the other, and
  a crash mid-write leaves the previous document intact.
- **The webhook secrets are the one thing outside it**, in
  `<state dir>/sched.secrets.json`, and `docs/contract-notes.md` records why.
  An HMAC cannot be verified from a hash, so where the key is kept is the only
  thing that can keep it off every door.

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
