package store

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/husniadil/herdr-sched/internal/action"
	"github.com/husniadil/herdr-sched/internal/codes"
	"github.com/husniadil/herdr-sched/internal/job"
)

func nightly(id string) job.Job {
	return job.Job{
		ID:       id,
		Schedule: "0 3 * * *",
		Action: action.Action{
			Kind: action.KindTask,
			Args: map[string]string{"title": "sweep the board"},
		},
		Enabled:   true,
		CreatedMS: 1_700_000_000_000,
	}
}

func jobEvent(ms int64, kind, id string) Event {
	return NewEvent(at(ms), EntityJob, kind, id, "agent:wA:p1", "", nil)
}

// §5.5 and the store's own rule: a job and the event recording it land in one
// save, so neither can exist without the other.
func TestAJobAndItsEventAreSavedTogether(t *testing.T) {
	s := openTemp(t)
	if err := s.AddJob(nightly("sweep"), jobEvent(1, KindAdded, "sweep")); err != nil {
		t.Fatalf("AddJob: %v", err)
	}
	raw, err := os.ReadFile(s.Path)
	if err != nil {
		t.Fatalf("read the store: %v", err)
	}
	var doc Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the store is not readable JSON: %v", err)
	}
	if len(doc.Jobs) != 1 || len(doc.JobEvents) != 1 {
		t.Fatalf("the file holds %d jobs and %d job events, want one of each", len(doc.Jobs), len(doc.JobEvents))
	}
	if doc.JobEvents[0].Name != "sched.job.added" {
		t.Errorf("the event is named %q", doc.JobEvents[0].Name)
	}
}

// The id is what `cron:<job id>` attributes every call to, so two schedules
// answering to one name is refused rather than merged.
func TestASecondJobUnderOneNameIsRefused(t *testing.T) {
	s := openTemp(t)
	if err := s.AddJob(nightly("sweep"), jobEvent(1, KindAdded, "sweep")); err != nil {
		t.Fatalf("AddJob: %v", err)
	}
	err := s.AddJob(nightly("sweep"), jobEvent(2, KindAdded, "sweep"))
	if err == nil {
		t.Fatal("a second job under one name was written")
	}
	if got := codes.Of(err); got != codes.Conflict {
		t.Errorf("the refusal is %s, want CONFLICT", got)
	}
	if len(s.Jobs()) != 1 {
		t.Errorf("%d jobs held, want the first one alone", len(s.Jobs()))
	}
}

// Removing answers with the row as it was, so the caller can say what went.
func TestRemovingAnswersWithTheRowAsItWas(t *testing.T) {
	s := openTemp(t)
	s.AddJob(nightly("sweep"), jobEvent(1, KindAdded, "sweep"))
	was, err := s.RemoveJob("sweep", jobEvent(2, KindRemoved, "sweep"))
	if err != nil {
		t.Fatalf("RemoveJob: %v", err)
	}
	if was.Schedule != "0 3 * * *" {
		t.Errorf("the removed row reads %q", was.Schedule)
	}
	if len(s.Jobs()) != 0 {
		t.Errorf("%d jobs left", len(s.Jobs()))
	}
	if _, err := s.RemoveJob("sweep", jobEvent(3, KindRemoved, "sweep")); codes.Of(err) != codes.NotFound {
		t.Errorf("removing it twice answered %v", err)
	}
}

// Asking for the state a job is already in changes nothing and writes no
// event: an `enabled` on the trail for a job that was already enabled is a
// state change that never happened.
func TestEnablingAnAlreadyEnabledJobWritesNoEvent(t *testing.T) {
	s := openTemp(t)
	s.AddJob(nightly("sweep"), jobEvent(1, KindAdded, "sweep"))
	_, changed, err := s.SetJobEnabled("sweep", true, 0, jobEvent(2, KindEnabled, "sweep"))
	if err != nil {
		t.Fatalf("SetJobEnabled: %v", err)
	}
	if changed {
		t.Error("a job that was already enabled reports a change")
	}
	if n := len(s.Snapshot().JobEvents); n != 1 {
		t.Errorf("%d job events, want the add alone", n)
	}

	was, changed, err := s.SetJobEnabled("sweep", false, 0, jobEvent(3, KindDisabled, "sweep"))
	if err != nil || !changed || was.Enabled {
		t.Fatalf("disabling answered %v, changed %v, enabled %v", err, changed, was.Enabled)
	}
	if n := len(s.Snapshot().JobEvents); n != 2 {
		t.Errorf("%d job events, want the disable recorded", n)
	}
}

// The cursor moves with a skip's event beside it, and moves alone for a
// firing: the firing's record is the run trail's (note 2).
func TestTheCursorMovesWithASkipAndAloneForAFiring(t *testing.T) {
	s := openTemp(t)
	s.AddJob(nightly("sweep"), jobEvent(1, KindAdded, "sweep"))

	if err := s.AdvanceJob("sweep", 1_800_000_000_000, nil); err != nil {
		t.Fatalf("AdvanceJob: %v", err)
	}
	held, _ := s.Job("sweep")
	if held.LastFiredMS != 1_800_000_000_000 {
		t.Errorf("the cursor is %d", held.LastFiredMS)
	}
	if n := len(s.Snapshot().JobEvents); n != 1 {
		t.Errorf("%d job events for a firing, want the add alone: the run trail carries the fire", n)
	}

	skip := jobEvent(4, KindSkipped, "sweep")
	if err := s.AdvanceJob("sweep", 1_900_000_000_000, &skip); err != nil {
		t.Fatalf("AdvanceJob with a skip: %v", err)
	}
	if n := len(s.Snapshot().JobEvents); n != 2 {
		t.Errorf("%d job events, want the skip recorded", n)
	}
	if _, found := s.Job("nothing"); found {
		t.Error("a job that is not there was found")
	}
}

// The job trail is bounded like every other entity's: the whole document is
// rewritten on every change, so an unbounded trail makes every save slower
// for as long as the daemon runs.
func TestTheJobTrailIsBoundedOnSave(t *testing.T) {
	// In memory: this writes a thousand documents, and none of them has to
	// reach a disk to prove the rotation happened on the way through.
	s, err := Open("")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for i := 0; i < MaxEvents+10; i++ {
		id := fmt.Sprintf("sweep-%04d", i)
		if err := s.AddJob(nightly(id), jobEvent(int64(i+1), KindAdded, id)); err != nil {
			t.Fatalf("AddJob %s: %v", id, err)
		}
	}
	held := s.Snapshot().JobEvents
	if len(held) != MaxEvents {
		t.Fatalf("the job trail holds %d events, want it bounded at %d", len(held), MaxEvents)
	}
	if held[len(held)-1].EntityID != fmt.Sprintf("sweep-%04d", MaxEvents+9) {
		t.Errorf("rotation dropped the newest event; the trail ends at %s", held[len(held)-1].EntityID)
	}
}

// A job's trail merges into the one read `events` answers, oldest first.
func TestTheJobTrailIsInTheMergedRead(t *testing.T) {
	s := openTemp(t)
	s.AddJob(nightly("sweep"), jobEvent(2, KindAdded, "sweep"))
	s.Park(Parked{ID: "pk-1", State: ParkedWaiting}, parkedEvent(1, KindParked, "pk-1"))
	trail := s.Trail()
	if len(trail) != 2 {
		t.Fatalf("%d events in the merged trail", len(trail))
	}
	if !strings.HasPrefix(trail[0].Name, "sched.parked.") {
		t.Errorf("the merged trail opens with %q, want the older event", trail[0].Name)
	}
	if trail[1].Entity != EntityJob {
		t.Errorf("the second event is a %q", trail[1].Entity)
	}
}
