package job

import (
	"strings"
	"testing"
	"time"

	"github.com/husniadil/herdr-sched/internal/action"
)

func at(t *testing.T, s string) time.Time {
	t.Helper()
	when, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("read %q: %v", s, err)
	}
	return when.UTC()
}

func ms(t *testing.T, s string) int64 { return at(t, s).UnixMilli() }

// sweep is a well-formed nightly job, which the cases below vary one field of
// at a time.
func sweep(t *testing.T) Job {
	t.Helper()
	return Job{
		ID:        "nightly-sweep",
		Schedule:  "0 3 * * *",
		Action:    action.Action{Kind: action.KindTask, Args: map[string]string{"title": "sweep the board"}},
		Enabled:   true,
		CreatedMS: ms(t, "2026-08-20T00:00:00Z"),
	}
}

// A job is refused when it is WRITTEN if anything about it could not fire: the
// id that names the principal, the expression, and the action.
func TestAJobThatCouldNotFireIsRefusedAtCreate(t *testing.T) {
	cases := map[string]func(*Job){
		"no id":             func(j *Job) { j.ID = "" },
		"a spaced id":       func(j *Job) { j.ID = "nightly sweep" },
		"a colon in the id": func(j *Job) { j.ID = "cron:nightly" },
		"no schedule":       func(j *Job) { j.Schedule = "" },
		"a bad schedule":    func(j *Job) { j.Schedule = "0 3 * *" },
		"an unknown action": func(j *Job) { j.Action = action.Action{Kind: "smoke"} },
		"a bare action":     func(j *Job) { j.Action = action.Action{Kind: action.KindTask} },
	}
	for what, break_ := range cases {
		j := sweep(t)
		break_(&j)
		if err := j.Validate(); err == nil {
			t.Errorf("a job with %s was accepted", what)
		}
	}
	if err := sweep(t).Validate(); err != nil {
		t.Fatalf("a well-formed job was refused: %v", err)
	}
}

// §3.2: the principal a job's calls declare is `cron:<job id>`, so an operator
// reading the board sees which schedule filed the task.
func TestTheJobIsTheSignalItsCallsDeclare(t *testing.T) {
	src := sweep(t).Source()
	if src.Principal() != "cron:nightly-sweep" {
		t.Errorf("a job fires as %q", src.Principal())
	}
	if err := src.Validate(); err != nil {
		t.Errorf("a job's own source does not attribute a call: %v", err)
	}
}

// The plain case: the schedule came round while the daemon was up.
func TestAScheduleThatCameRoundFires(t *testing.T) {
	j := sweep(t)
	j.LastFiredMS = ms(t, "2026-08-24T03:00:00Z")
	due := Due(at(t, "2026-08-25T03:00:20Z"), []Job{j}, false)
	if len(due) != 1 {
		t.Fatalf("%d decisions, want 1", len(due))
	}
	if !due[0].Fire {
		t.Fatal("the schedule came round and the job did not fire")
	}
	if !due[0].At.Equal(at(t, "2026-08-25T03:00:00Z")) {
		t.Errorf("it fired for %s, want the scheduled instant 2026-08-25T03:00:00Z", due[0].At)
	}
	if due[0].Skipped != 0 {
		t.Errorf("%d skipped on a schedule that was not missed", due[0].Skipped)
	}
}

// The tick runs far more often than any schedule, and a job whose instant has
// not come round is not a decision at all.
func TestATickBetweenInstantsDecidesNothing(t *testing.T) {
	j := sweep(t)
	j.LastFiredMS = ms(t, "2026-08-25T03:00:00Z")
	if due := Due(at(t, "2026-08-25T03:00:30Z"), []Job{j}, false); len(due) != 0 {
		t.Fatalf("%d decisions between two instants, want none", len(due))
	}
	if due := Due(at(t, "2026-08-25T14:00:00Z"), []Job{j}, false); len(due) != 0 {
		t.Fatalf("%d decisions eleven hours before the next instant, want none", len(due))
	}
}

// The boundary the whole design turns on: a job due twice while the daemon
// slept fires ONCE with catch_up, and never twice.
func TestAJobDueTwiceWhileTheDaemonSleptFiresOnce(t *testing.T) {
	j := sweep(t)
	j.CatchUp = true
	j.LastFiredMS = ms(t, "2026-08-22T03:00:00Z")
	due := Due(at(t, "2026-08-25T09:00:00Z"), []Job{j}, true)
	if len(due) != 1 {
		t.Fatalf("%d decisions, want 1", len(due))
	}
	if !due[0].Fire {
		t.Fatal("catch_up is set and the job did not fire at start")
	}
	if !due[0].At.Equal(at(t, "2026-08-25T03:00:00Z")) {
		t.Errorf("it fired for %s, want the LATEST missed instant", due[0].At)
	}
	if due[0].Skipped != 2 {
		t.Errorf("%d skipped, want the 2 older instants said out loud", due[0].Skipped)
	}
	// What the one fire stood in for ends one instant BEFORE the one it
	// fired: 23rd and 24th skipped, 25th fired.
	if !due[0].SkippedFrom.Equal(at(t, "2026-08-23T03:00:00Z")) ||
		!due[0].SkippedThrough.Equal(at(t, "2026-08-24T03:00:00Z")) {
		t.Errorf("the catch-up stood in for %s to %s", due[0].SkippedFrom, due[0].SkippedThrough)
	}
}

// The default, and cron's own semantics: a schedule missed while the daemon
// was down is SKIPPED, and the skip is recorded rather than swallowed.
func TestAMissedScheduleIsSkippedByDefault(t *testing.T) {
	j := sweep(t)
	j.LastFiredMS = ms(t, "2026-08-22T03:00:00Z")
	due := Due(at(t, "2026-08-25T09:00:00Z"), []Job{j}, true)
	if len(due) != 1 {
		t.Fatalf("%d decisions, want 1", len(due))
	}
	if due[0].Fire {
		t.Fatal("a schedule missed while the daemon was down fired anyway")
	}
	if due[0].Skipped != 3 {
		t.Errorf("%d skipped, want the 3 missed instants", due[0].Skipped)
	}
	if !due[0].SkippedFrom.Equal(at(t, "2026-08-23T03:00:00Z")) {
		t.Errorf("the skip runs from %s, want the first missed instant", due[0].SkippedFrom)
	}
	if !due[0].SkippedThrough.Equal(at(t, "2026-08-25T03:00:00Z")) {
		t.Errorf("the skip runs through %s, want the last missed instant", due[0].SkippedThrough)
	}
	// The cursor still moves to the last one: a skip that left it behind
	// would fire the whole backlog on the next tick.
	if !due[0].At.Equal(at(t, "2026-08-25T03:00:00Z")) {
		t.Errorf("the cursor moves to %s, want the last missed instant", due[0].At)
	}
}

// catch_up fires once at start and not once per missed instant, which is the
// other half of "never twice".
func TestCatchUpFiresOnceAndNotOncePerMiss(t *testing.T) {
	j := sweep(t)
	j.CatchUp = true
	j.LastFiredMS = ms(t, "2026-06-01T03:00:00Z")
	due := Due(at(t, "2026-08-25T09:00:00Z"), []Job{j}, true)
	if len(due) != 1 || !due[0].Fire {
		t.Fatalf("%d decisions, want one firing", len(due))
	}
	if !due[0].At.Equal(at(t, "2026-08-25T03:00:00Z")) {
		t.Errorf("it fired for %s, want one fire at the latest instant", due[0].At)
	}
}

// A start with nothing missed is not a catch-up: catch_up fires the schedule
// that was MISSED, never one that was not.
func TestCatchUpWithNothingMissedFiresNothing(t *testing.T) {
	j := sweep(t)
	j.CatchUp = true
	j.LastFiredMS = ms(t, "2026-08-25T03:00:00Z")
	if due := Due(at(t, "2026-08-25T03:05:00Z"), []Job{j}, true); len(due) != 0 {
		t.Fatalf("%d decisions with nothing missed, want none", len(due))
	}
}

// A job that has never fired counts from when it was written, so adding one at
// 3:05 does not fire the 3:00 that had already passed.
func TestANewJobCountsFromWhenItWasWritten(t *testing.T) {
	j := sweep(t)
	j.CreatedMS = ms(t, "2026-08-25T03:05:00Z")
	if due := Due(at(t, "2026-08-25T03:06:00Z"), []Job{j}, false); len(due) != 0 {
		t.Fatalf("%d decisions, want none: the 03:00 was before the job existed", len(due))
	}
	due := Due(at(t, "2026-08-26T03:00:10Z"), []Job{j}, false)
	if len(due) != 1 || !due[0].Fire {
		t.Fatalf("%d decisions on the first instant after it was written, want one firing", len(due))
	}
}

// A disabled job is not a schedule. It is skipped silently: an operator who
// disabled it is not owed a skip record every night.
func TestADisabledJobDecidesNothing(t *testing.T) {
	j := sweep(t)
	j.Enabled = false
	j.LastFiredMS = ms(t, "2026-08-20T03:00:00Z")
	if due := Due(at(t, "2026-08-25T09:00:00Z"), []Job{j}, true); len(due) != 0 {
		t.Fatalf("%d decisions for a disabled job, want none", len(due))
	}
}

// The store is a JSON document an operator can edit, so an expression that no
// longer parses reaches the tick. It is loud there, and it never stops the
// other jobs from firing.
func TestAnUnreadableScheduleIsLoudAndStopsNothingElse(t *testing.T) {
	broken := sweep(t)
	broken.ID, broken.Schedule = "hand-edited", "0 3 * *"
	broken.LastFiredMS = ms(t, "2026-08-24T03:00:00Z")
	fine := sweep(t)
	fine.LastFiredMS = ms(t, "2026-08-24T03:00:00Z")

	due := Due(at(t, "2026-08-25T03:00:20Z"), []Job{broken, fine}, false)
	if len(due) != 2 {
		t.Fatalf("%d decisions, want one per job", len(due))
	}
	if due[0].Err == nil {
		t.Error("an unreadable expression decided nothing and said nothing")
	}
	if due[0].Fire {
		t.Error("an unreadable expression fired an action")
	}
	if !due[1].Fire {
		t.Error("the job beside the broken one did not fire")
	}
}

// A schedule no calendar can satisfy never fires and never errors: it is well
// formed, and 30 february simply does not come round.
func TestAScheduleNoCalendarSatisfiesNeverFires(t *testing.T) {
	j := sweep(t)
	j.Schedule = "0 0 30 feb *"
	if err := j.Validate(); err != nil {
		t.Fatalf("30 february is well formed and was refused: %v", err)
	}
	if due := Due(at(t, "2026-08-25T09:00:00Z"), []Job{j}, true); len(due) != 0 {
		t.Fatalf("%d decisions for a schedule that never comes round", len(due))
	}
}

// The count of missed instants is bounded, and says so rather than quietly
// reporting a smaller number than the truth.
func TestALongOutageCountsUpToTheCapAndSaysSo(t *testing.T) {
	j := sweep(t)
	j.Schedule = "* * * * *"
	j.LastFiredMS = ms(t, "2026-01-01T00:00:00Z")
	due := Due(at(t, "2026-08-25T00:00:00Z"), []Job{j}, true)
	if len(due) != 1 {
		t.Fatalf("%d decisions, want 1", len(due))
	}
	if due[0].Skipped != MaxCounted {
		t.Errorf("%d skipped, want the count capped at %d", due[0].Skipped, MaxCounted)
	}
	if !due[0].AtLeast {
		t.Error("the count hit the cap and does not say it is a floor")
	}
}

// The refusal an operator reads names what is wrong with the job.
func TestTheRefusalNamesWhatIsWrong(t *testing.T) {
	j := sweep(t)
	j.Schedule = "0 3 * *"
	err := j.Validate()
	if err == nil || !strings.Contains(err.Error(), "five fields") {
		t.Fatalf("the refusal does not say the expression is short: %v", err)
	}
}
