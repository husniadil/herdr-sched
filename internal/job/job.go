// Package job is the cron half: a job row, and the pure decision that says
// which jobs fire now.
//
// Nothing here reads a clock, spawns anything or touches the store. Due is
// handed the time, the rows and the mark each row carries, and answers with
// what should happen — which is what makes every boundary case below testable
// without a time.Sleep and without a process (§12.1).
package job

import (
	"strings"
	"time"

	"github.com/husniadil/herdr-sched/internal/action"
	"github.com/husniadil/herdr-sched/internal/codes"
	"github.com/husniadil/herdr-sched/internal/cron"
)

// MaxCounted bounds how many missed instants a decision counts. A job on
// `* * * * *` and a fortnight of downtime is twenty thousand instants nobody
// will read; the count stops here and says it is a floor rather than reporting
// a smaller number as the truth.
const MaxCounted = 1000

// Job is one schedule: an expression, one action from the vocabulary, and
// whether a schedule missed while the daemon was down should fire once at the
// next start.
type Job struct {
	// ID is the operator's own name for the schedule, and the id half of the
	// `cron:<job id>` principal every call it makes declares (§3.1). It
	// carries no colon and no space for exactly that reason.
	ID string `json:"id"`
	// Schedule is the standard five-field cron expression, read in UTC.
	Schedule string `json:"schedule"`
	// Action is what this job DOES, as data rather than as code.
	Action action.Action `json:"action"`
	// CatchUp fires a schedule missed while the daemon was down ONCE at the
	// next start. Without it the miss is skipped, which is cron's own
	// semantics and the default here (note 2).
	CatchUp bool `json:"catch_up,omitempty"`
	// Enabled is false for a job an operator has turned off. It is kept
	// rather than removed: disabling is not deleting, and the row's history
	// stays readable.
	Enabled bool `json:"enabled"`
	// Project is the scope the job was written in (§4.2).
	Project   string `json:"project,omitempty"`
	CreatedMS int64  `json:"created_at"`
	// LastFiredMS is the SCHEDULED instant this job was last decided for, not
	// the wall clock it fired at. It is the cursor that makes a fire
	// once-only: it moves whether the action fired or was skipped, so a
	// backlog is never fired twice and never fired late one instant at a
	// time.
	LastFiredMS int64 `json:"last_fired,omitempty"`
}

// Validate refuses everything that could not fire, at the moment the row is
// written. A job that fails at 3am fails in a log nobody reads.
func (j Job) Validate() error {
	if err := ValidateID(j.ID); err != nil {
		return err
	}
	if _, err := cron.Parse(j.Schedule); err != nil {
		return err
	}
	return j.Action.Validate()
}

// ValidateID holds a job id to what a §3.1 principal can carry. It is
// separate from Validate because a verb that names a job — remove, enable,
// disable — checks the name without a row to check it against.
func ValidateID(id string) error {
	if strings.TrimSpace(id) == "" {
		return codes.Errorf(codes.Usage, "a job has no id, and `cron:` alone attributes nothing on a sibling's trail")
	}
	if strings.ContainsAny(id, ": \t\n") {
		return codes.Errorf(codes.Usage,
			"a job id carries no colon and no space, and %q does: it is the id half of `cron:<job id>` (§3.1)", id)
	}
	return nil
}

// Source is the §3.2 principal this job's calls declare, so the actor on a
// sibling's own trail is the schedule that caused the call.
func (j Job) Source() action.Source {
	return action.Source{Kind: action.SourceCron, ID: j.ID}
}

// Decision is what should happen to one job at one instant. It exists only
// when something should: a job whose schedule has not come round is not in the
// answer at all.
type Decision struct {
	Job Job
	// At is the scheduled instant this decision is FOR, and the mark the
	// job's cursor moves to. It moves whether the action fired or was
	// skipped: a cursor left behind fires the whole backlog on the next tick.
	At time.Time
	// Fire says the action runs.
	Fire bool
	// Skipped is how many scheduled instants passed without firing. On a
	// catch-up it is the older misses the one fire stands in for; on a skip
	// it is every miss including the latest.
	Skipped int
	// SkippedFrom and SkippedThrough are the first and last instants that
	// were passed over, both empty when none was. On a capped count Through
	// is the last one counted rather than the last one there was, which is
	// what AtLeast says.
	SkippedFrom    time.Time
	SkippedThrough time.Time
	// AtLeast says Skipped hit MaxCounted and is a floor rather than a count.
	AtLeast bool
	// Err is a row that cannot be read — an expression edited by hand into
	// something that no longer parses. It fires nothing and is loud: the
	// caller records it, and every other job in the same pass still fires.
	Err error
}

// Due is the whole decision core: given the clock, the job rows and the mark
// each one carries, which jobs fire now.
//
// atStart is the ONE thing that changes the answer: a miss discovered at
// startup is a schedule the daemon was down for, and note 2 settles that as
// skipped by default and fired once for a job with catch_up. On an ordinary
// tick a miss is fired, once, whatever catch_up says — the daemon was up, and
// the instant is this one rather than a backlog.
func Due(now time.Time, jobs []Job, atStart bool) []Decision {
	var out []Decision
	for _, j := range jobs {
		if !j.Enabled {
			// Not a skip, and deliberately not recorded as one: an operator
			// who disabled a job is not owed a skip record every night.
			continue
		}
		d, ok := decide(now, j, atStart)
		if ok {
			out = append(out, d)
		}
	}
	return out
}

func decide(now time.Time, j Job, atStart bool) (Decision, bool) {
	s, err := cron.Parse(j.Schedule)
	if err != nil {
		return Decision{Job: j, Err: err}, true
	}
	cursor := j.LastFiredMS
	if cursor == 0 {
		// A job that has never fired counts from when it was written, so
		// adding one at 03:05 does not fire the 03:00 that had already passed.
		cursor = j.CreatedMS
	}
	prev, ok := s.Prev(now)
	if !ok || prev.UnixMilli() <= cursor {
		return Decision{}, false
	}
	d := Decision{Job: j, At: prev, Fire: true}
	// Everything strictly after the cursor and at or before prev is an
	// instant this job owes an answer for.
	missed, from, before, capped := count(s, time.UnixMilli(cursor), prev)
	d.AtLeast = capped
	if atStart && !j.CatchUp {
		// Cron's own semantics: the schedule did not fire, and the operator
		// hears which ones (note 2). The cursor still moves to prev.
		d.Fire = false
		d.Skipped, d.SkippedFrom, d.SkippedThrough = missed, from, prev
		return d, true
	}
	// One fire, for the latest instant. The older ones are said out loud
	// rather than fired: firing a backlog turns one missed nightly sweep into
	// a week of them at once.
	if missed > 1 {
		d.Skipped, d.SkippedFrom, d.SkippedThrough = missed-1, from, before
	}
	return d, true
}

// count is how many instants fall in (after, through], the first of them, the
// last one BEFORE through, and whether the walk hit the cap. The last-before
// is what a catch-up reports: it fires `through` itself, and what it stood in
// for ends one instant earlier.
func count(s cron.Schedule, after, through time.Time) (int, time.Time, time.Time, bool) {
	var first, last, beforeLast time.Time
	n := 0
	for t := after; ; {
		next, ok := s.Next(t)
		if !ok || next.After(through) {
			return n, first, beforeLast, false
		}
		if n == 0 {
			first = next
		}
		beforeLast, last = last, next
		n++
		if n == MaxCounted {
			// The count is a floor, and so is the range it names.
			return n, first, last, true
		}
		t = next
	}
}
