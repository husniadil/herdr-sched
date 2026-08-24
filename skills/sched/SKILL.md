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

**Read this first: there is no job, trigger or action verb yet.** This build
is the common foundation. If you were asked to create a schedule, add a
trigger, or make something fire on a timer, `hsched` cannot do it today. Say
so plainly rather than reaching for a verb that is not there — and do not
reach for `crontab`, a background `sleep` loop, or a detached process instead.
Scheduling is what this plugin exists to own, and standing up a second
scheduler beside it is the thing that will be hardest to unpick later. Tell
the operator what is missing and let them decide.

What you *can* do is the six verbs every sibling spells the same way.

## Reach for the tools, not the shell

Every verb is an MCP tool on the `herdr-sched` server AND an `hsched`
subcommand. **Use the tools.** They take typed arguments and answer with a
document, where the CLI takes a shell line that quoting can mangle. Nothing
about the tools depends on `hsched` being on your PATH either, which it may
not be: a dispatched worker pane can have the door and not the binary.

| Tool | What it answers |
| --- | --- |
| `doctor` | Whether the plugin can work at all. Run it FIRST when anything else refuses. |
| `events` | The append-only trail of what this plugin did. |
| `dump` | The whole store in one document. |
| `parked_list` | What the policy gate deferred to the operator. |
| `parked_resolve` | Let one of those through, or reject it. |
| `stop` | End the one daemon serving every project of this user. |

On the CLI the same six are `hsched doctor`, `hsched events`, `hsched dump`,
`hsched parked list`, `hsched parked resolve <id>` and `hsched stop`. The CLI
adds `hsched events --follow`, which keeps the connection and prints each
event as it is written; a tool call answers once, because a stream is not a
tool call.

## You never say who you are

Your principal is derived from the Herdr pane you run in. `agent` and `human`
are refused as declared principals on both doors — a principal you could
declare is one you could borrow. The `--as` flag exists for `cron:`,
`trigger:` and `plugin:` only, it is CLI-only, and it is how this plugin's own
scheduled work will name itself once there is any.

Everything is scoped to a project, the git root of the directory you are
working in. `project` and `all_projects` are on every tool; passing both is
refused rather than ranked.

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
and never reject one to clear a list.

**`stop` is a brake on the whole plugin**, not on one schedule. Nothing this
plugin drives fires again until a daemon is started. Confirm with the operator
first, the same way you would before any act whose blast radius is everyone
else's work.
