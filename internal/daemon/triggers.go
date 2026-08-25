package daemon

import (
	"context"
	"os"
	"time"

	"github.com/husniadil/herdr-sched/internal/action"
	"github.com/husniadil/herdr-sched/internal/codes"
	"github.com/husniadil/herdr-sched/internal/protocol"
	"github.com/husniadil/herdr-sched/internal/store"
	"github.com/husniadil/herdr-sched/internal/trigger"
)

// TriggerRow is one trigger as a caller reads it. It carries NO secret, and it
// carries none because there is none to carry: the key lives in a file the
// store document does not hold, so this row could not print one if it tried.
type TriggerRow struct {
	trigger.Trigger
	// URL is where a webhook is reached, empty for a watcher and empty when
	// no inbound door is listening.
	URL string `json:"url,omitempty"`
	// FiredThisHour is how many firings the hourly limit currently counts
	// against this row, which is the number that decides the next refusal.
	FiredThisHour int `json:"fired_this_hour"`
}

// TriggersReport is what trigger.list answers with.
type TriggersReport struct {
	Triggers []TriggerRow `json:"triggers"`
	Count    int          `json:"count"`
}

// TriggerChange is what add, remove, enable and disable answer with.
type TriggerChange struct {
	Trigger TriggerRow `json:"trigger"`
	State   string     `json:"state"`
	Changed bool       `json:"changed"`
	// Secret is the webhook's HMAC key, and this is the ONE place it is ever
	// rendered (note 2). It is present only in the answer to add, for the
	// trigger just created, and never again — a caller that does not copy it
	// here cannot get it back, and removing the trigger and writing a new one
	// is the only way to a key that is known.
	Secret string `json:"secret,omitempty"`
}

// addTrigger writes one trigger down, and for a webhook draws the secret that
// is shown here and nowhere else.
//
// The secret is written BEFORE the row: a crash between the two writes leaves
// a key with no trigger, which is inert, rather than a webhook nobody holds a
// key for.
//
// The check, the key and the row are one act, so they are taken under one
// lock: the store refuses the duplicate under its own, and that is one step of
// three.
func (d *Daemon) addTrigger(req protocol.Request) (TriggerChange, error) {
	d.writingTrigger.Lock()
	defer d.writingTrigger.Unlock()
	if req.AllProjects {
		// A trigger fires INTO one project's board and mailbox. "Every
		// project" is a way of reading, not a place to write one.
		return TriggerChange{}, codes.Refusef(codes.Invalid,
			"a trigger is written in ONE project; drop all_projects and name the project it fires in")
	}
	id, _ := req.Args["id"].(string)
	if err := trigger.ValidateID(id); err != nil {
		return TriggerChange{}, err
	}
	kind, _ := req.Args["kind"].(string)
	path, _ := req.Args["path"].(string)
	actionKind, _ := req.Args["action"].(string)
	args, err := actionArgs(req.Args["args"])
	if err != nil {
		return TriggerChange{}, err
	}
	now := d.now()
	t := trigger.Trigger{
		ID:              id,
		Kind:            kind,
		Action:          action.Action{Kind: actionKind, Args: args},
		Path:            path,
		CooldownSeconds: int64(whole(req.Args["cooldown"])),
		MaxPerHour:      whole(req.Args["max_per_hour"]),
		Enabled:         true,
		Project:         req.Project,
		CreatedMS:       now.UnixMilli(),
	}
	if err := t.Validate(); err != nil {
		return TriggerChange{}, err
	}
	secret := ""
	if t.Kind == trigger.KindWebhook {
		if d.Secrets == nil {
			return TriggerChange{}, codes.Errorf(codes.Unavailable,
				"this daemon keeps no webhook secrets, so a webhook it wrote down could never be verified")
		}
		// The duplicate is refused HERE, before a key is drawn, and the store
		// refuses it again under its own lock. Writing the key first and
		// finding out afterwards would have replaced the LIVE trigger's
		// secret with one for a row that was never written — the caller reads
		// a refusal and the working webhook has silently stopped verifying.
		if _, held := d.Store.Trigger(t.ID); held {
			return TriggerChange{}, codes.Refusef(codes.AlreadyExists,
				"there is already a trigger called %s", t.ID)
		}
		if secret, err = trigger.NewSecret(); err != nil {
			return TriggerChange{}, err
		}
		if err := d.Secrets.Set(t.ID, secret); err != nil {
			return TriggerChange{}, err
		}
	}
	ev := store.NewEvent(now, store.EntityTrigger, store.KindAdded, t.ID, req.Caller(), req.Project,
		map[string]any{
			"trigger_kind": t.Kind, "action": t.Action.Kind,
			"cooldown_seconds": t.CooldownSeconds, "max_per_hour": t.MaxPerHour,
		})
	if err := d.Store.AddTrigger(t, ev); err != nil {
		// The row was refused, so the key it was drawn for has nothing left to
		// verify. It is only ever dropped when no row answers to the name: the
		// check above means a duplicate never reaches here having overwritten
		// a live key, and this is the belt for a store that refused for some
		// other reason.
		if secret != "" {
			if _, held := d.Store.Trigger(t.ID); !held {
				d.dropSecret(t.ID)
			}
		}
		return TriggerChange{}, err
	}
	d.Emitted(ev)
	d.logf("%s wrote the %s trigger %s firing %s", req.Caller(), t.Kind, t.ID, t.Action.Kind)
	return TriggerChange{
		Trigger: d.triggerRow(t, now), State: store.KindAdded, Changed: true, Secret: secret,
	}, nil
}

// listTriggers answers with this project's triggers, or every project's when
// the caller asked for that (§4.2).
func (d *Daemon) listTriggers(req protocol.Request) (TriggersReport, error) {
	now := d.now()
	rows := []TriggerRow{}
	for _, t := range d.Store.Triggers() {
		if !req.AllProjects && req.Project != "" && t.Project != req.Project {
			continue
		}
		rows = append(rows, d.triggerRow(t, now))
	}
	return TriggersReport{Triggers: rows, Count: len(rows)}, nil
}

func (d *Daemon) removeTrigger(req protocol.Request) (TriggerChange, error) {
	id, _ := req.Args["id"].(string)
	now := d.now()
	ev := store.NewEvent(now, store.EntityTrigger, store.KindRemoved, id, req.Caller(), req.Project, nil)
	was, err := d.Store.RemoveTrigger(id, ev)
	if err != nil {
		return TriggerChange{}, err
	}
	d.Emitted(ev)
	// The row is gone, so the key has nothing left to verify.
	d.dropSecret(id)
	d.logf("%s removed the trigger %s", req.Caller(), id)
	return TriggerChange{Trigger: d.triggerRow(was, now), State: store.KindRemoved, Changed: true}, nil
}

// setTriggerEnabled turns one trigger off or on.
//
// Enabling a WATCH forgets the stamp, so the next look records what is there
// rather than firing for what changed while it was off. A disabled watcher is
// passed over entirely and costs no stat per tick, which leaves its stamp
// stale by exactly the gap; firing for that gap is what an operator who turned
// it off asked not to happen.
func (d *Daemon) setTriggerEnabled(req protocol.Request, on bool) (TriggerChange, error) {
	id, _ := req.Args["id"].(string)
	kind := store.KindDisabled
	if on {
		kind = store.KindEnabled
	}
	now := d.now()
	ev := store.NewEvent(now, store.EntityTrigger, kind, id, req.Caller(), req.Project, nil)
	held, changed, err := d.Store.SetTriggerEnabled(id, on, ev)
	if err != nil {
		return TriggerChange{}, err
	}
	if changed {
		d.Emitted(ev)
		d.logf("%s %s the trigger %s", req.Caller(), kind, id)
		if on && held.Kind == trigger.KindWatch {
			if err := d.Store.StampTrigger(id, trigger.Stamp{}); err != nil {
				d.logf("clearing the stamp on the watcher %s: %v", id, err)
			} else {
				held.Stamp = trigger.Stamp{}
			}
		}
	}
	return TriggerChange{Trigger: d.triggerRow(held, now), State: kind, Changed: changed}, nil
}

func (d *Daemon) dropSecret(id string) {
	if d.Secrets == nil {
		return
	}
	if err := d.Secrets.Delete(id); err != nil {
		d.logf("dropping the webhook secret for %s: %v", id, err)
	}
}

// triggerRow renders one trigger for a caller: the row, where a webhook is
// reached, and what the hourly limit currently counts.
func (d *Daemon) triggerRow(t trigger.Trigger, now time.Time) TriggerRow {
	row := TriggerRow{Trigger: t, FiredThisHour: len(trigger.Recent(t.FiredMS, now))}
	if t.Kind == trigger.KindWebhook {
		row.URL = d.webhookURL(t.ID)
	}
	return row
}

// whole reads an int argument the registry has already held to a whole number,
// whichever door sent it.
func whole(raw any) int {
	switch n := raw.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

// fireTrigger is the one path every inbound signal takes, whatever brought it:
// a verified webhook request or a watched file that changed.
//
// The pure core is asked under the store's lock, and the cursor moves there
// before anything is fired. Two requests arriving in the same millisecond
// would otherwise both read a spent cooldown as unspent — the webhook door is
// one HTTP server answering any number of connections, so the decision that
// counts is the one made against the row as it is now.
//
// A refusal is recorded and said out loud. Rate limiting that refuses silently
// is a trigger that looks broken (note 2).
func (d *Daemon) fireTrigger(ctx context.Context, t trigger.Trigger, why map[string]any) trigger.Verdict {
	now := d.now()
	verdict, err := d.Store.ClaimTriggerFire(t.ID, func(held trigger.Trigger) (trigger.Trigger, trigger.Verdict) {
		v := trigger.Allow(now, held)
		if !v.Fire {
			return held, v
		}
		return trigger.Fired(now, held), v
	})
	if err != nil {
		// The store could not answer the claim, which is this daemon unable to
		// work rather than a rule refusing. It lands on the run trail as a
		// FAILURE and carries no limit, so nothing downstream reads it as a
		// caller that should slow down.
		d.recordTriggerRun(t, store.KindFailed, detailWith(why, map[string]any{
			"action": t.Action.Kind, "error": codes.Message(err),
		}))
		d.logf("firing the trigger %s: %v", t.ID, err)
		return trigger.Verdict{Reason: codes.Message(err)}
	}
	if !verdict.Fire {
		d.recordTriggerRun(t, store.KindLimited, detailWith(why, map[string]any{
			"action": t.Action.Kind, "limit": verdict.Limit, "reason": verdict.Reason,
		}))
		d.logf("the trigger %s was held down: %s", t.ID, verdict.Reason)
		return verdict
	}
	if d.Fire == nil {
		d.recordTriggerRun(t, store.KindFailed, detailWith(why, map[string]any{
			"action": t.Action.Kind,
			"error":  "this daemon has no runner: the trigger fired and nothing could perform it",
		}))
		return verdict
	}
	if err := d.Fire.Fire(ctx, t.Source(), t.Action, t.Project); err != nil {
		// The run is already on the trail in the runner's own words. This is
		// the operator's log line for the same failure.
		d.logf("the trigger %s fired %s and it failed: %v", t.ID, t.Action.Kind, err)
	}
	return verdict
}

// recordTriggerRun puts one run on the run trail for a signal that never
// reached the runner: held down by a limit, dropped for a signature that did
// not hold, or arriving at a daemon with nothing to fire it.
func (d *Daemon) recordTriggerRun(t trigger.Trigger, kind string, detail map[string]any) {
	ev := store.NewEvent(d.now(), store.EntityRun, kind, t.ID, t.Source().Principal(), t.Project, detail)
	if err := d.Store.RecordRun(ev); err != nil {
		d.logf("recording the run of %s: %v", t.ID, err)
		return
	}
	d.Emitted(ev)
}

func detailWith(base, extra map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// runWatchers is the file-watcher half of the tick. It polls rather than
// subscribing: fsnotify would be a third dependency on a budget the README
// fixes at the standard library plus the two every sibling pins, and polling
// is the rhythm this daemon already has. The cost is that a change is noticed
// within one tick rather than at once, which is the same latency a cron job
// already accepts.
func (d *Daemon) runWatchers(ctx context.Context) {
	if d.Store == nil {
		return
	}
	for _, t := range d.Store.Triggers() {
		if t.Kind != trigger.KindWatch {
			continue
		}
		if !t.Enabled {
			// Not recorded: an operator who disabled a watcher is not owed a
			// line every tick. The stamp stays where it was, and enabling it
			// records what is there then rather than firing for the gap.
			continue
		}
		d.watch(ctx, t)
	}
}

func (d *Daemon) watch(ctx context.Context, t trigger.Trigger) {
	stamp, fire := trigger.Changed(t.Stamp, look(t.Path))
	if stamp == t.Stamp {
		return
	}
	// The stamp moves BEFORE the action fires, and it moves whether the action
	// fired or was held down. A daemon that dies mid-action leaves a change
	// that did not fire, which the trail can say, rather than one that fires
	// again on every tick from now on.
	if err := d.Store.StampTrigger(t.ID, stamp); err != nil {
		d.logf("recording what the watcher %s saw at %s: %v", t.ID, t.Path, err)
		return
	}
	if !fire {
		return
	}
	d.logf("the watcher %s saw %s change", t.ID, t.Path)
	d.fireTrigger(ctx, t, map[string]any{"trigger_kind": trigger.KindWatch, "path": t.Path})
}

// look is one stat of a watched path. A path that cannot be read at all is
// recorded as absent rather than as an error: a file inside a directory whose
// permissions changed is a change, and the next look that can read it is
// another one.
func look(path string) trigger.Stamp {
	info, err := os.Stat(path)
	if err != nil {
		return trigger.Stamp{Present: false}
	}
	return trigger.Stamp{
		Present: true,
		ModNS:   info.ModTime().UnixNano(),
		Size:    info.Size(),
	}
}
