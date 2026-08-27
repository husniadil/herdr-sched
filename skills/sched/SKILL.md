---
name: sched
description: The scheduler and trigger plugin for this Herdr fleet. Use when checking whether the scheduler is healthy, reading what it did, resolving something its policy gate deferred to the operator, or stopping its daemon. Trigger words - sched, hsched, schedule, cron, trigger, scheduler, parked, gate.
---

# Sched

`hsched` is the scheduler and trigger plugin. A cron schedule or an inbound
trigger fires an action into the sibling plugins — a task on the htask board,
a message over hmail, a worker through hdis, a shell command — as the
principal the contract already names for it: `cron:<job id>` or
`trigger:<id>`, so the actor is on every event trail the action touches.

**Read this first: both halves are here.** A schedule is `job_add`. An inbound
webhook or a file watcher is `trigger_add`. Do not reach for `crontab`, a
background `sleep` loop, a detached process, or a listener of your own, for a
trigger OR for a schedule. Scheduling is what this plugin exists to own, and
standing up a second scheduler beside it is the thing that will be hardest to
unpick later.

## Reach for the tools, not the shell

Every verb is an MCP tool on the `herdr-sched` server AND an `hsched`
subcommand. **Use the tools.** They take typed arguments and answer with a
document, where the CLI takes a shell line that quoting can mangle. Nothing
about the tools depends on `hsched` being on your PATH either, which it may
not be: a dispatched worker pane can have the door and not the binary.

| Tool | What it answers |
| --- | --- |
| `job_add` | Write down a schedule: a cron expression, one action, and whether to catch up a missed one. |
| `job_list` | Every schedule here, with when it last fired and when it fires next. |
| `job_remove` | Take one off for good. |
| `job_enable` / `job_disable` | Stop one firing, or let it fire again, without losing it. |
| `trigger_add` | Write down an inbound trigger: a webhook on a URL, or a watcher on a path. A webhook's secret is answered HERE and never again. |
| `trigger_list` | Every trigger here, with its URL, its limits and what it has fired this hour. Never a secret. |
| `trigger_remove` | Take one off for good; a webhook's key goes with it. |
| `trigger_enable` / `trigger_disable` | Stop one firing, or let it fire again, without losing it. |
| `doctor` | Whether the plugin can work at all. Run it FIRST when anything else refuses. |
| `events` | The append-only trail of what this plugin did. |
| `dump` | The whole store in one document. |
| `parked_list` | What the policy gate deferred to the operator, in this project. |
| `parked_resolve` | Let one of those through, or reject it. |
| `stop` | End the one daemon serving every project of this user. |

On the CLI they are `hsched job add <id> <schedule> <action>`, `hsched job
list`, `hsched job remove <id>`, `hsched job enable|disable <id>`, `hsched
trigger add <id> <kind> <action>`, `hsched trigger list`, `hsched trigger
remove <id>`, `hsched trigger enable|disable <id>`, `hsched
doctor`, `hsched events`, `hsched dump`, `hsched parked list`, `hsched parked
resolve <id>` and `hsched stop`. The CLI
adds `hsched events --follow`, which keeps the connection and prints each
event as it is written; a tool call answers once, because a stream is not a
tool call.

## Writing a schedule

`job_add` takes an id, a five-field cron expression and an action kind, with
the action's own arguments as an object:

```json
{"id": "nightly-sweep", "schedule": "0 3 * * *", "action": "task",
 "args": {"title": "sweep the board"}}
```

Three things to tell the operator rather than discover for them:

- **The expression is read in UTC**, always. "3am" is 3am UTC, not 3am where
  they are. Convert before you write the row, and say which you wrote.
- **A schedule missed while the daemon was down is skipped**, which is cron's
  own semantics. `catch_up: true` fires it once at the next start instead —
  once, for the latest missed instant, never once per miss. Ask which they
  want when it matters; the default is skip.
- **The id is the principal.** Every call the job makes declares itself as
  `cron:<id>`, so `nightly-sweep` is what shows up as the actor on the htask
  board. Name it for what it does.

Everything that could not fire is refused when you write the row: an
expression that does not parse, an action nothing can run, an argument its
kind never takes. A `USAGE` here is worth reading in full — it names the field.

`hsched doctor` reports which jobs were skipped at the last start, which is
the first thing to check when a schedule "did not run".

## Writing a trigger

`trigger_add` takes an id, a kind — `webhook` or `watch`, and there is no third
— and an action kind, with the action's own arguments as an object. Both kinds
also take a `cooldown` in seconds and a `max_per_hour`:

```json
{"id": "deploy", "kind": "webhook", "action": "shell",
 "args": {"command": "./bin/deploy"}, "cooldown": 60, "max_per_hour": 10}
```

```json
{"id": "inbox", "kind": "watch", "path": "/abs/path/to/file",
 "action": "task", "args": {"title": "something landed"}}
```

**A webhook's secret is answered once and never again.** `trigger_add` is the
only place it is ever rendered — no list, no dump, no doctor will print it, and
there is no verb that reissues one. Hand it to the operator in the same turn
you create the trigger, and say plainly that the only way back from a lost key
is to remove the trigger and write it again.

Signing is `X-Sched-Signature: sha256=<hmac-hex>`, an HMAC-SHA256 over the
**raw request body** with that secret, and the request is a `POST` to the URL
the add answered with. The signature is verified before anything parses a byte
of the body: what the body says decides nothing, so any body — `{}` included —
is fine. A request that does not verify is dropped onto the trail and fires
nothing.

**A watcher's path is absolute**, and a relative one is refused when the row is
written: it would be relative to your working directory, and the daemon that
stats it is somewhere else. It polls on the daemon's own tick, so a change is
noticed within one tick rather than at once. **The first look records and does
not fire** — a watcher written against a file that already exists must not fire
for a change that predates it — and a watcher re-enabled after a spell off does
the same thing rather than firing for the gap.

Both limits refuse **loudly**: a firing held down by the cooldown or the hourly
limit lands on the run trail as `sched.run.limited` naming the rule, and a
webhook caller is answered `429` with the same words. A `503` there is not a
limit at all — it is this daemon unable to work, and `doctor` is the next call.

## The action vocabulary

Four kinds, and the list is closed. A job and a trigger fire the same four:

| Action | What it does | Arguments |
| --- | --- | --- |
| `task` | Files a task on the htask board | `title` (required), `description`, `project`, `priority` |
| `mail` | Sends a notify, or an ask, over hmail | `to` and `body` (required), `ask`, `project` |
| `dispatch` | Brings a worker up for a ready task via hdis | `task` (required), `project` |
| `shell` | Runs a command on the host | `command` (required), `dir` |

An argument the kind does not name is refused when the row is written, not at
3am. The action lands in the project the row was written in unless it names a
`project` of its own.

## You never say who you are

Your principal is derived from the Herdr pane you run in. `agent` and `human`
are refused as declared principals on both doors — a principal you could
declare is one you could borrow. The `--as` flag exists for `cron:`,
`trigger:` and `plugin:` only, it is CLI-only, and it is how this plugin's own
scheduled work will name itself once there is any.

Everything is scoped to a project, the git root of the directory you are
working in. `project` and `all_projects` are on every tool; passing both is
refused rather than ranked. `parked_list` is the one list tool that takes no
`all_projects` and refuses it (§4.4): a parked action is resolved where it was
parked.

## When something refuses

Every refusal carries one of the contract's nine codes and, where this plugin
refuses for a finer reason, that reason is the first word of the message.

- **`CONFLICT` / `NOT_RUNNING`** from `stop` — no daemon is listening. Nothing
  is started just to be stopped, and that is not an error to route around.
- **`DENIED` with a `parked_id`** — the policy gate **deferred** your call. It
  was recorded, not performed. The row is in `parked_list`, and the operator
  decides.
- **`DENIED` with no `parked_id`** — the gate refused outright. Do not retry
  it; the reason is in the message.
- **`NOT_FOUND`** from `events --since` — that event id has rotated out of the
  trail. Read from the beginning or pass a millisecond. Do not treat the whole
  window as your own tail.

## Two things that need the operator

**Resolving a parked action is the operator's authority.** `parked_resolve`
re-runs a verb the gate stopped, under the original subject, without asking
the gate again. Confirm with the user before resolving one on their behalf,
and never reject one to clear a list. The trail is what carries the
accountability: your principal is the actor, and the event is marked
`on_behalf_of_operator` because you performed the operator's authority for
them (§3.7).

**`stop` is a brake on the whole plugin**, not on one schedule. Nothing this
plugin drives fires again until a daemon is started. Confirm with the operator
first, the same way you would before any act whose blast radius is everyone
else's work.
