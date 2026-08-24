package daemon

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/husniadil/herdr-sched/internal/action"
	"github.com/husniadil/herdr-sched/internal/codes"
	"github.com/husniadil/herdr-sched/internal/cron"
	"github.com/husniadil/herdr-sched/internal/job"
	"github.com/husniadil/herdr-sched/internal/protocol"
	"github.com/husniadil/herdr-sched/internal/store"
)

// JobRow is one schedule as a caller reads it: the row itself, plus the next
// instant it fires at. The next instant is DERIVED and never stored — it is a
// function of the expression and the clock, and a stored copy would be a
// second answer to go stale.
type JobRow struct {
	job.Job
	// Next is the next instant this job fires at, RFC3339 in UTC. It is empty
	// for a disabled job, for one whose expression no longer parses, and for
	// one no calendar can satisfy.
	Next string `json:"next,omitempty"`
	// Unreadable is why a row cannot be scheduled, for the one case an
	// operator hand-edited the store into something that no longer parses.
	Unreadable string `json:"unreadable,omitempty"`
}

// JobsReport is what job.list answers with.
type JobsReport struct {
	Jobs  []JobRow `json:"jobs"`
	Count int      `json:"count"`
}

// JobChange is what add, remove, enable and disable answer with: the row as it
// stands after the verb, and whether anything moved.
type JobChange struct {
	Job JobRow `json:"job"`
	// State is what the verb did: added, removed, enabled or disabled.
	State string `json:"state"`
	// Changed is false for a job asked for the state it was already in.
	Changed bool `json:"changed"`
}

// SkipReport is one job the daemon passed over at its last start, as doctor
// reports it (note 2). It is on the trail as well; this is the part an
// operator sees without reading the trail at all.
type SkipReport struct {
	Job    string `json:"job"`
	Missed int    `json:"missed"`
	// AtLeast says Missed is a floor: the count stopped at the cap rather
	// than walking a year of minutes.
	AtLeast bool   `json:"at_least,omitempty"`
	From    string `json:"from"`
	Through string `json:"through"`
}

// skips is what the last start passed over, held for doctor.
type skips struct {
	mu   sync.Mutex
	held []SkipReport
}

func (s *skips) add(r SkipReport) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.held = append(s.held, r)
}

func (s *skips) all() []SkipReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SkipReport{}, s.held...)
}

// addJob writes one schedule down. Everything that could not fire is refused
// here rather than at 3am: the expression, the action kind, and every argument
// that kind takes.
func (d *Daemon) addJob(req protocol.Request) (JobChange, error) {
	if req.AllProjects {
		// A job fires INTO one project's board and mailbox. "Every project"
		// is a way of reading, not a place to write one.
		return JobChange{}, codes.Refusef(codes.Invalid,
			"a job is written in ONE project; drop all_projects and name the project it fires in")
	}
	id, _ := req.Args["id"].(string)
	if err := job.ValidateID(id); err != nil {
		return JobChange{}, err
	}
	schedule, _ := req.Args["schedule"].(string)
	kind, _ := req.Args["action"].(string)
	args, err := actionArgs(req.Args["args"])
	if err != nil {
		return JobChange{}, err
	}
	catchUp, _ := req.Args["catch_up"].(bool)
	now := d.now()
	j := job.Job{
		ID:       id,
		Schedule: schedule,
		Action:   action.Action{Kind: kind, Args: args},
		CatchUp:  catchUp,
		Enabled:  true,
		Project:  req.Project,
		// The cursor starts here, so a job written at 03:05 does not fire the
		// 03:00 that had already passed.
		CreatedMS: now.UnixMilli(),
	}
	if err := j.Validate(); err != nil {
		return JobChange{}, err
	}
	ev := store.NewEvent(now, store.EntityJob, store.KindAdded, j.ID, req.Caller(),
		map[string]any{"schedule": j.Schedule, "action": j.Action.Kind, "catch_up": j.CatchUp})
	if err := d.Store.AddJob(j, ev); err != nil {
		return JobChange{}, err
	}
	d.Emitted(ev)
	d.logf("%s wrote the job %s: %s firing %s", req.Caller(), j.ID, j.Schedule, j.Action.Kind)
	return JobChange{Job: d.row(j, now), State: store.KindAdded, Changed: true}, nil
}

// actionArgs turns the object a door sent into the string map an action row
// carries. A number and a boolean are written the way the action's own
// create-time check reads them back, and anything with a shape — a list, a
// nested object — is refused: an action argument is one value, and silently
// rendering a list as JSON inside a string is a schedule that fires with
// something nobody wrote.
func actionArgs(raw any) (map[string]string, error) {
	if raw == nil {
		return nil, nil
	}
	fields, ok := raw.(map[string]any)
	if !ok {
		return nil, codes.Refusef(codes.Invalid, "args is an object of named values")
	}
	out := make(map[string]string, len(fields))
	for name, v := range fields {
		switch value := v.(type) {
		case string:
			out[name] = value
		case bool:
			out[name] = strconv.FormatBool(value)
		case float64:
			out[name] = strconv.FormatFloat(value, 'f', -1, 64)
		case nil:
			out[name] = ""
		default:
			return nil, codes.Refusef(codes.Invalid,
				"the action's %s is %T, and an action argument is one value: a word, a number or a switch", name, v)
		}
	}
	return out, nil
}

// listJobs answers with this project's schedules, or every project's when the
// caller asked for that (§4.2).
func (d *Daemon) listJobs(req protocol.Request) (JobsReport, error) {
	now := d.now()
	rows := []JobRow{}
	for _, j := range d.Store.Jobs() {
		if !req.AllProjects && req.Project != "" && j.Project != req.Project {
			continue
		}
		rows = append(rows, d.row(j, now))
	}
	return JobsReport{Jobs: rows, Count: len(rows)}, nil
}

func (d *Daemon) removeJob(req protocol.Request) (JobChange, error) {
	id, _ := req.Args["id"].(string)
	now := d.now()
	ev := store.NewEvent(now, store.EntityJob, store.KindRemoved, id, req.Caller(), nil)
	was, err := d.Store.RemoveJob(id, ev)
	if err != nil {
		return JobChange{}, err
	}
	d.Emitted(ev)
	d.logf("%s removed the job %s", req.Caller(), id)
	return JobChange{Job: d.row(was, now), State: store.KindRemoved, Changed: true}, nil
}

// setJobEnabled turns one schedule off or on. Asking for the state it is
// already in is not a refusal: it is the state the caller wanted, and the
// answer says nothing moved.
func (d *Daemon) setJobEnabled(req protocol.Request, on bool) (JobChange, error) {
	id, _ := req.Args["id"].(string)
	kind := store.KindDisabled
	if on {
		kind = store.KindEnabled
	}
	now := d.now()
	ev := store.NewEvent(now, store.EntityJob, kind, id, req.Caller(), nil)
	held, changed, err := d.Store.SetJobEnabled(id, on, ev)
	if err != nil {
		return JobChange{}, err
	}
	if changed {
		d.Emitted(ev)
		d.logf("%s %s the job %s", req.Caller(), kind, id)
	}
	return JobChange{Job: d.row(held, now), State: kind, Changed: changed}, nil
}

// row renders one job with the next instant it fires at.
func (d *Daemon) row(j job.Job, now time.Time) JobRow {
	row := JobRow{Job: j}
	s, err := cron.Parse(j.Schedule)
	if err != nil {
		row.Unreadable = codes.Message(err)
		return row
	}
	if !j.Enabled {
		return row
	}
	if next, ok := s.Next(now); ok {
		row.Next = next.Format(time.RFC3339)
	}
	return row
}

// runDue is the whole of what the tick does: ask the pure core which jobs are
// due, then perform each answer. atStart is the one thing that changes the
// answer, and it is the daemon's own start rather than anything on the row.
func (d *Daemon) runDue(ctx context.Context, atStart bool) {
	if d.Store == nil {
		return
	}
	for _, decision := range job.Due(d.now(), d.Store.Jobs(), atStart) {
		d.perform(ctx, decision, atStart)
	}
}

// perform carries out one decision: a skip recorded, a fire fired, or a row
// that cannot be read said out loud.
func (d *Daemon) perform(ctx context.Context, decision job.Decision, atStart bool) {
	j := decision.Job
	if decision.Err != nil {
		// The store is a JSON document an operator can edit, so a schedule
		// that no longer parses reaches here. It is a failed run rather than
		// a silence, and the cursor does not move: there is no instant to
		// move it to, and the row is broken until someone fixes it.
		d.recordRun(j, store.KindFailed, map[string]any{
			"schedule": j.Schedule, "error": codes.Message(decision.Err),
		})
		d.logf("the job %s cannot be scheduled: %s", j.ID, codes.Message(decision.Err))
		return
	}
	var skipped *store.Event
	if decision.Skipped > 0 {
		// A catch-up fires ONCE, and the older instants it stands in for are
		// still said out loud: an operator reading the trail sees what did
		// not happen, not only what did.
		report := SkipReport{
			Job: j.ID, Missed: decision.Skipped, AtLeast: decision.AtLeast,
			From:    decision.SkippedFrom.Format(time.RFC3339),
			Through: decision.SkippedThrough.Format(time.RFC3339),
		}
		ev := store.NewEvent(d.now(), store.EntityJob, store.KindSkipped, j.ID, j.Source().Principal(),
			map[string]any{
				"missed": report.Missed, "at_least": report.AtLeast,
				"from": report.From, "through": report.Through,
				"caught_up": decision.Fire,
			})
		skipped = &ev
		if atStart {
			// doctor answers "which jobs were skipped at the LAST START"
			// (note 2). A miss the daemon was up for is on the trail and is
			// not that question's answer.
			d.skipped.add(report)
		}
		d.logf("the job %s missed %s scheduled instant(s) from %s: %s",
			j.ID, atLeast(report), report.From, caughtUp(decision.Fire))
	}
	// The cursor moves BEFORE the action fires, and it moves whether the
	// action fires or is skipped. A daemon that dies mid-action leaves a
	// schedule that did not fire, which the trail can say, rather than one
	// that fires again on the next start (note 2: never twice).
	if err := d.Store.AdvanceJob(j.ID, decision.At.UnixMilli(), skipped); err != nil {
		d.logf("moving the job %s past %s: %v", j.ID, decision.At.Format(time.RFC3339), err)
		return
	}
	if skipped != nil {
		d.Emitted(*skipped)
	}
	if !decision.Fire {
		return
	}
	if d.Fire == nil {
		d.recordRun(j, store.KindFailed, map[string]any{
			"action": j.Action.Kind,
			"error":  "this daemon has no runner: the schedule fired and nothing could perform it",
		})
		return
	}
	if err := d.Fire.Fire(ctx, j.Source(), j.Action); err != nil {
		// The run is already on the trail in the runner's own words. This is
		// the operator's log line for the same failure.
		d.logf("the job %s fired %s and it failed: %v", j.ID, j.Action.Kind, err)
	}
}

// recordRun puts one run on the trail for a firing that never reached the
// runner. The runner writes its own; this is for the two failures that happen
// before it is ever called.
func (d *Daemon) recordRun(j job.Job, kind string, detail map[string]any) {
	ev := store.NewEvent(d.now(), store.EntityRun, kind, j.ID, j.Source().Principal(), detail)
	if err := d.Store.RecordRun(ev); err != nil {
		d.logf("recording the run of %s: %v", j.ID, err)
		return
	}
	d.Emitted(ev)
}

func atLeast(r SkipReport) string {
	if r.AtLeast {
		return fmt.Sprintf("at least %d", r.Missed)
	}
	return strconv.Itoa(r.Missed)
}

func caughtUp(fired bool) string {
	if fired {
		return "firing once for the latest (catch_up)"
	}
	return "skipped, which is what a job without catch_up does"
}
