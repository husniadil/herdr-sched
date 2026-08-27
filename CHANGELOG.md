# Changelog

All notable changes to herdr-sched are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
uses [semantic versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.2.1] - 2026-08-27

The contract, read again: a deferred-action list scoped where 0.10.1 says it
is scoped, and a resolution that was not the operator's own saying so on the
trail.

### Changed

- `parked list` answers the resolved project's deferred actions alone and
  refuses `--all-projects` with `USAGE`, as contract 0.10.1 §4.4 spells out.
  It used to return every project's rows, which handed one project's operator
  the deferred actions of every other.
- A `parked resolve` decided by anyone but the operator now carries
  `on_behalf_of_operator` on its event (§3.7), the same mark the sibling
  plugins write. The operator's own decision carries nothing extra, so the
  trail distinguishes the human's call from an agent making it for them.
- The declared contract revision is now 0.10.1, up from 0.10.0.

## [0.2.0] - 2026-08-27

The operator, made visible: a popup dashboard on a key, and a principal that
is the human who wrote the row rather than nobody.

### Added

- A read-only popup dashboard for jobs, triggers and watches
  (`scripts/popup-dashboard.sh`): every schedule with when it fires next,
  every trigger with what is holding it down, and the tail of the run trail.
  It is read-only on purpose — a popup carries no `HERDR_PANE_ID`, so its
  principal is the human and the CRUD stays on the gated verbs. It needs `jq`
  and says so rather than drawing an empty dashboard.
- A keyboard route to it. Herdr binds keys to plugin actions and not to panes,
  so a popup with no action has no keyboard route at all: the `dashboard`
  action in `herdr-plugin.toml` is that route, and `keybindings.toml` carries
  the block to copy into `~/.config/herdr/config.toml`. Herdr does not load a
  plugin's keybindings — a plugin does not get to claim keys in the operator's
  config. Opening a dashboard that is already open reports success rather than
  an error, so the key can be leaned on.
- `--operator` on `hsched mcp` (§7.5). A door registered in a desktop MCP
  client stands in no Herdr pane, so every job, trigger and parked resolution
  an operator wrote through it was attributed to nobody. The declaration is a
  **start-time flag only**: a pane still wins, a declared door inside a pane
  refuses to start with `FORBIDDEN`, and a call that carries `operator` as a
  tool argument is refused with `USAGE`.
- `hsched doctor` prints the calling principal on a `principal` line (§10.3
  with §7.5), so an operator sees which of their registrations speak for them
  without reading the trail: a declared door answers `human`, an undeclared
  one `none`. Same JSON key and text label as `htask`.
- `hsched mcp --project <dir>`: the project a door acts in, read once from the
  server command, for a door spawned in a client's working directory rather
  than in a project (§4.2). It is a **default and not an override** — a tool
  call that names `project` still acts in the one it named, and `all_projects`
  is untouched.

### Fixed

- A stop the gate deferred and an operator then resolved closed halt from
  inside `parked resolve`, which never closed the answer `Serve` waits on: the
  daemon hung holding its lock and its socket. A stop let through the gate
  stops.
- `job enable` flipped the flag and moved the cursor in two writes, and a tick
  landing between them fired the whole backlog the move exists to skip. One
  write now.
- A door whose started daemon lost the lock race gave up instead of dialing
  the winner, and a detached shell run whose record the store refused vanished
  without a word — it says so in the log now.
- The dashboard drew a refusing daemon as an empty one. §6.2 puts a failure on
  stdout as one JSON document, `jq` read it happily, and every section drew
  "no schedules" / "no triggers" / "nothing has fired yet" — a healthy
  scheduler with nothing in it, during an outage. Each renderer captures the
  answer and its status and asks whether it is usable first, naming which of
  the four ways it was not: nothing on stdout, an error document, something
  that is not JSON, or a non-zero status behind a readable one. An empty but
  healthy daemon still reads as empty.
- A `q` piped without a trailing newline took the whole redraw interval to
  act on input that had already arrived: the popup's non-tty fallback slept
  on a partial line. A partial line is handed over at once; true EOF still
  sleeps, which is what keeps a closed stdin a redraw loop rather than a spin.
- The sample keybinding moved off `prefix+c`, which Herdr itself holds.
- A paneless caller nobody declared is `none`, which is what the contract
  spells, rather than `unknown`; and a paneless CLI invocation is the operator,
  because its argv IS the deliberate human act §3.6 names (§3.7). A parked
  re-run reproduces `human` through that act rather than through `--as`.
- `hsched mcp --project` parsed and was then dropped: `--project` is a
  persistent flag, so the door accepted it and went on resolving every call
  from the tool argument or its own working directory. A flag that parses and
  is silently ignored tells the operator nothing.

## [0.1.0] - 2026-08-25

Both halves: jobs that fire on a schedule, triggers that fire on an inbound
webhook request or on a watched file changing, and the one action vocabulary
they share.

### Added

- `internal/trigger`, the trigger entity and the pure decisions beside it:
  `Allow` says whether the cooldown or the hourly limit holds a signal down,
  `Changed` says whether a watched path differs from the last look, and
  `Sign`/`Verify` are the HMAC over a raw body. No clock, no socket, no stat —
  so a replay inside a cooldown, a fourth firing in an hour and every way a
  signature can be wrong are pinned with no daemon, no port and no
  `time.Sleep`.
- `trigger add`, `trigger list`, `trigger remove`, `trigger enable` and
  `trigger disable` on both doors, gated as `sched.trigger.add`,
  `sched.trigger.remove`, `sched.trigger.enable` and `sched.trigger.disable`.
  A trigger carries one action from the vocabulary, a `--cooldown` and a
  `--max_per_hour`, and everything that could not fire is refused when the row
  is **written**.
- The inbound webhook door: one HTTP server on a loopback port by default
  (`webhook_addr`, `127.0.0.1:8797` by default, `off` for none), answering
  `POST /trigger/<id>`. The signature is verified over the **raw body before
  anything parses a byte of it**, and a request that does not verify is
  dropped onto the run trail naming the trigger and fires nothing. A door that
  cannot bind never stops the daemon: `hsched doctor` names the address it got
  and why it got none.
- A webhook's HMAC secret is answered by `trigger add` and by nothing else,
  ever. It is kept in `<state dir>/sched.secrets.json`, mode `0600`, which is
  **not** the store document — so no door that renders the document can render
  a secret, and a test drives `trigger list`, `dump`, `events`, `doctor` and
  the store file itself to prove it.
- The file watcher, polling on the daemon's own tick. It fires when a watched
  path's mtime, size or existence differs from the last look; the first look
  records and does not fire, and so does the first look after a re-enable.
  Polling rather than `fsnotify` is the dependency budget: the daemon already
  has this rhythm.
- Both limits refuse **loudly**. A cooldown or an hourly limit that holds a
  signal down lands on the run trail as `sched.run.limited` with the rule and
  the words, and the caller is answered `429` with the same. The decision is
  made under the store's lock and the cursor moves before the action fires, so
  two requests in the same millisecond cannot both read a spent cooldown as
  unspent.
- `triggers` and `trigger_events` in the store, the entity and its own trail
  written in the same save (§5.5).
- `webhook_addr` in the config, with `SCHED_WEBHOOK_ADDR` beside it.

- `internal/cron`, a five-field cron parser and the two answers everything
  else is built on: the next instant strictly after a time, and the last one
  at or before it. `*`, `*/n`, `a-b`, `a-b/n`, comma lists, three-letter month
  and day names, and cron's own or-rule when both day fields are restricted.
  Every expression is read in **UTC**, and `docs/contract-notes.md` records
  why there is no per-job timezone.
- `internal/job`, the job entity and the pure due-computation: given the
  clock, the rows and the cursor each row carries, which jobs fire now. No
  clock of its own, no goroutine, no store — so a schedule missed for three
  days, a catch-up standing in for two instants and a hand-edited expression
  are all pinned by tests with no `time.Sleep` and no process in them.
- `job add`, `job list`, `job remove`, `job enable` and `job disable` on both
  doors, gated as `sched.job.add`, `sched.job.remove`, `sched.job.enable` and
  `sched.job.disable`. A job carries a cron expression, one action from the
  vocabulary and a per-job `catch_up`, and everything that could not fire is
  refused when the row is **written**.
- `jobs` and `job_events` in the store, the entity and its own trail written
  in the same save (§5.5).
- The tick fires what is due, as `cron:<job id>` (§3.2). The cursor is the
  **scheduled instant**, and it moves before the action fires and whether the
  action fired, failed or was skipped: a schedule fires once per instant and
  never twice.
- A schedule missed while the daemon was down is **skipped** — cron's own
  semantics — recorded as `sched.job.skipped` and named by `hsched doctor`.
  `catch_up` fires such a job once at the next start, for the latest missed
  instant alone.
- `verbs.Object`, an argument that is a set of named values: an object in the
  MCP schema and one JSON document in a CLI flag, which is what lets an
  action's own arguments spell the same way on both doors.

- `internal/action`, the closed vocabulary of four kinds — `task`, `mail`,
  `dispatch`, `shell` — validated in the pure core at **create** time: an
  unknown kind, an unknown argument, a missing required one or an argument of
  the wrong type is refused when the row is written rather than at 3am when it
  fires. The firing signal's §3.2 principal, `cron:<job>` or `trigger:<id>`,
  is built and validated here too.
- One adapter per sibling — `internal/htask`, `internal/hmail`,
  `internal/hdis` — over `internal/sibling`, the single spawn site that
  appends `--json` and `--as` to every call and scrubs the pane out of the
  environment, so a call declares a principal INSTEAD of a pane. A sibling's
  §6.2 error envelope is carried as a refusal in that sibling's own words with
  its §6.3 code; a failure without one never reached a sibling that answered.
  A guard fails the build if any of those packages grows a second spawn.
- `internal/shellact`, the one action that reaches no sibling: it runs
  detached from the tick with both streams captured and bounded, so a slow
  command holds up no other schedule.
- `internal/fire`, the fire path: one validated action becomes one call and
  one run on the trail. A sibling that is unreachable or refuses is a loud
  `sched.run.failed` event, never a silent skip.
- `run_events` in the store, an entity trail with no list of rows beside it:
  the run history IS the §8 stream and there is no second table.

### Fixed

- The MCP-door parity walk in `cmd/hsched` matched a verb by the LAST segment
  of its subcommand path, so `job add` and `trigger add` collided and one
  verb's flags were read against the other's argument list. It matches on the
  whole path now.

## Before 0.1.0 - 2026-08-24

The untagged skeleton the 0.1.0 release was built on; kept for the record.

The first release: the common foundation every sibling plugin shares, and
nothing domain-specific yet. There is no job, trigger or action verb — this is
the skeleton they land on.

### Added

- One verb registry (`internal/verbs`) both doors are generated from, with a
  parity test enumerating each surface against it and a subcommand-tree walk
  holding every CLI flag to a mapped property or a recorded exemption.
- The verbs the shared contract makes common: `doctor`, `dump`, `events`
  (with `--follow` on the CLI), `parked list`, `parked resolve` and `stop`,
  plus the three commands that are not verbs — `daemon`, `mcp` and `version`.
- The four contract globals on every verb: `--json`, `--project`,
  `--all-projects`, and `--as`, which refuses the derived `agent` and `human`
  principals and accepts `cron:`, `trigger:` and `plugin:`.
- The socket protocol, and a daemon that takes a lock, binds
  `<state dir>/sched.sock`, runs a bounded tick and leaves neither file behind
  when it stops.
- A JSON store with an events trail per entity — `parked` and its sibling
  `parked_events` from day one — written whole through a temp file and rename,
  and refused rather than overwritten when it carries a version this binary
  does not know.
- The §9 policy gate, failing closed, with `defer` parking a call for the
  operator and `parked resolve` re-running it under the subject the gate
  stopped.
- The §8.3 event hook, handed every event on stdin and run detached.
- A TOML config at `~/.config/sched/sched.toml`, read once at startup, with
  `SCHED_<KEY>` overrides.
- `internal/testenv`: stand-in sibling binaries on a replaced `PATH`, and
  throwaway state and config directories.
- The `herdr-plugin.toml` manifest with `scripts/{start,stop,restart,on-pane-gone}.sh`,
  the Makefile target set the siblings share, and CI matching their workflow.

### Notes

- `status` and `sweep` are the two verbs on the standard's parity list that
  this build does not carry. Both are held back rather than stubbed, and the
  reason is recorded in `docs/contract-notes.md`.
- The store is JSON where §5.1 says SQLite. The reason is in the README and in
  `docs/contract-notes.md`.

[0.2.1]: https://github.com/husniadil/herdr-sched/releases/tag/v0.2.1
[0.2.0]: https://github.com/husniadil/herdr-sched/releases/tag/v0.2.0
[0.1.0]: https://github.com/husniadil/herdr-sched/releases/tag/v0.1.0
