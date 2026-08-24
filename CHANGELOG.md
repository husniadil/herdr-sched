# Changelog

All notable changes to herdr-sched are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
uses [semantic versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0] - 2026-08-24

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

[0.1.0]: https://github.com/husniadil/herdr-sched/releases/tag/v0.1.0
