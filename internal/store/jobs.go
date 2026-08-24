package store

import (
	"github.com/husniadil/herdr-sched/internal/codes"
	"github.com/husniadil/herdr-sched/internal/job"
)

// EntityJob is a cron schedule (note 2). Unlike a run, it HAS a list of rows
// beside its trail: a job is a thing that exists between firings, and a
// history of edits is not the same as the set of jobs there are now.
const EntityJob = "job"

// The kinds a job's trail carries, beside the shared ones above.
const (
	// KindAdded is a job written down.
	KindAdded = "added"
	// KindRemoved is a job taken off the schedule for good.
	KindRemoved = "removed"
	// KindEnabled and KindDisabled are an operator turning one off and on
	// without losing it.
	KindEnabled  = "enabled"
	KindDisabled = "disabled"
	// KindSkipped is a schedule that came round while the daemon was down and
	// was not fired (note 2). It is on the trail rather than swallowed: a
	// schedule that silently stopped working looks exactly like one with
	// nothing to do.
	KindSkipped = "skipped"
)

// Jobs is every schedule this daemon holds, in the order they were written.
func (s *Store) Jobs() []job.Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]job.Job{}, s.doc.Jobs...)
}

// Job is one schedule by id.
func (s *Store) Job(id string) (job.Job, bool) {
	for _, j := range s.Jobs() {
		if j.ID == id {
			return j, true
		}
	}
	return job.Job{}, false
}

// AddJob writes one schedule and the event that says so, in one save. A
// duplicate id is refused rather than merged: the id is what `cron:<job id>`
// attributes every call to, and two schedules answering to one name is a
// sibling's trail nobody can read back.
func (s *Store) AddJob(j job.Job, ev Event) error {
	return s.update(func(doc *Document) error {
		for _, held := range doc.Jobs {
			if held.ID == j.ID {
				return codes.Refusef(codes.AlreadyExists, "there is already a job called %s", j.ID)
			}
		}
		doc.Jobs = append(doc.Jobs, j)
		doc.JobEvents = append(doc.JobEvents, ev)
		return nil
	})
}

// RemoveJob takes one schedule off for good and answers with the row as it
// was, so the caller can say what was removed.
func (s *Store) RemoveJob(id string, ev Event) (job.Job, error) {
	var was job.Job
	err := s.update(func(doc *Document) error {
		for i, held := range doc.Jobs {
			if held.ID != id {
				continue
			}
			was = held
			doc.Jobs = append(doc.Jobs[:i:i], doc.Jobs[i+1:]...)
			doc.JobEvents = append(doc.JobEvents, ev)
			return nil
		}
		return codes.Errorf(codes.NotFound, "no job called %s", id)
	})
	return was, err
}

// SetJobEnabled turns one schedule off or on. Asking for the state it is
// already in changes nothing and writes no event: an `enabled` on the trail
// for a job that was already enabled is a state change that never happened.
// The second answer says whether anything moved.
func (s *Store) SetJobEnabled(id string, on bool, ev Event) (job.Job, bool, error) {
	var was job.Job
	changed := false
	err := s.update(func(doc *Document) error {
		for i, held := range doc.Jobs {
			if held.ID != id {
				continue
			}
			was = held
			if held.Enabled == on {
				return nil
			}
			doc.Jobs[i].Enabled = on
			was = doc.Jobs[i]
			changed = true
			doc.JobEvents = append(doc.JobEvents, ev)
			return nil
		}
		return codes.Errorf(codes.NotFound, "no job called %s", id)
	})
	return was, changed, err
}

// AdvanceJob moves one job's cursor to the scheduled instant just decided,
// with the event that belongs on the job's own trail when there is one — a
// skip has one, a firing does not.
//
// A firing's event is a `sched.run.fired` on the RUN trail and nowhere else
// (note 2): the run history IS the stream, and a second job event saying the
// same thing is one more fact to keep in step. What the cursor is here is a
// mark rather than a record, and it moves BEFORE the action is fired: a
// daemon that dies mid-action leaves a schedule that did not fire, which the
// trail can say, rather than one that fires again on the next start.
func (s *Store) AdvanceJob(id string, atMS int64, ev *Event) error {
	return s.update(func(doc *Document) error {
		for i, held := range doc.Jobs {
			if held.ID != id {
				continue
			}
			doc.Jobs[i].LastFiredMS = atMS
			if ev != nil {
				doc.JobEvents = append(doc.JobEvents, *ev)
			}
			return nil
		}
		return codes.Errorf(codes.NotFound, "no job called %s", id)
	})
}
