package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/husniadil/herdr-sched/internal/codes"
	"github.com/husniadil/herdr-sched/internal/config"
	"github.com/husniadil/herdr-sched/internal/fire"
	"github.com/husniadil/herdr-sched/internal/protocol"
	"github.com/husniadil/herdr-sched/internal/store"
	"github.com/husniadil/herdr-sched/internal/testenv"
)

// nightlyArgs is a well-formed `job add` call: 3am daily, filing a task.
func nightlyArgs() map[string]any {
	return map[string]any{
		"id":       "nightly-sweep",
		"schedule": "0 3 * * *",
		"action":   "task",
		"args":     map[string]any{"title": "sweep the board"},
	}
}

func decode[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("read the answer: %v", err)
	}
	return out
}

// A job is written, listed, and carries the next instant it fires at.
func TestAJobIsWrittenAndListedWithItsNextInstant(t *testing.T) {
	d := newDaemon(t)
	raw, err := call(t, d, "job.add", nightlyArgs())
	if err != nil {
		t.Fatalf("job.add: %v", err)
	}
	change := decode[JobChange](t, raw)
	if !change.Changed || change.State != store.KindAdded {
		t.Errorf("the add answered %+v", change)
	}
	if change.Job.Next == "" {
		t.Error("the row carries no next instant")
	}
	if change.Job.LastFiredMS != 0 {
		t.Errorf("a new job has already fired at %d", change.Job.LastFiredMS)
	}

	raw, err = call(t, d, "job.list", nil)
	if err != nil {
		t.Fatalf("job.list: %v", err)
	}
	rep := decode[JobsReport](t, raw)
	if rep.Count != 1 || rep.Jobs[0].ID != "nightly-sweep" {
		t.Fatalf("the list answered %+v", rep)
	}
	if rep.Jobs[0].Action.Args["title"] != "sweep the board" {
		t.Errorf("the action's arguments read %v", rep.Jobs[0].Action.Args)
	}
}

// Everything that could not fire is refused when the row is WRITTEN, which is
// the whole reason the checks are at create time.
func TestAJobThatCouldNotFireIsRefusedAtAdd(t *testing.T) {
	cases := map[string]func(map[string]any){
		"an expression that does not parse": func(a map[string]any) { a["schedule"] = "0 3 * *" },
		"a field out of range":              func(a map[string]any) { a["schedule"] = "0 99 * * *" },
		"an action nothing can run":         func(a map[string]any) { a["action"] = "smoke" },
		"an argument the kind never takes":  func(a map[string]any) { a["args"] = map[string]any{"titel": "typo"} },
		"a missing required argument":       func(a map[string]any) { a["args"] = map[string]any{} },
		"an id that is a principal":         func(a map[string]any) { a["id"] = "cron:nightly" },
		"an argument with a shape":          func(a map[string]any) { a["args"] = map[string]any{"title": []any{"a", "b"}} },
	}
	for what, break_ := range cases {
		d := newDaemon(t)
		args := nightlyArgs()
		break_(args)
		if _, err := call(t, d, "job.add", args); err == nil {
			t.Errorf("a job with %s was written", what)
		} else if got := codes.Of(err); got != codes.Usage {
			t.Errorf("a job with %s was refused as %s, want USAGE", what, got)
		}
		if n := len(d.Store.Jobs()); n != 0 {
			t.Errorf("a job with %s left %d rows behind", what, n)
		}
	}
}

// Two schedules answering to one name is a sibling's trail nobody can read
// back, so the second is refused.
func TestASecondJobUnderOneNameIsRefused(t *testing.T) {
	d := newDaemon(t)
	if _, err := call(t, d, "job.add", nightlyArgs()); err != nil {
		t.Fatalf("job.add: %v", err)
	}
	_, err := call(t, d, "job.add", nightlyArgs())
	if err == nil {
		t.Fatal("a second job under one name was written")
	}
	if got := codes.Of(err); got != codes.Conflict {
		t.Errorf("it was refused as %s, want CONFLICT", got)
	}
}

// Disabling keeps the row and stops the schedule; asking twice says nothing
// moved rather than refusing.
func TestDisablingKeepsTheRowAndIsIdempotent(t *testing.T) {
	d := newDaemon(t)
	call(t, d, "job.add", nightlyArgs())

	raw, err := call(t, d, "job.disable", map[string]any{"id": "nightly-sweep"})
	if err != nil {
		t.Fatalf("job.disable: %v", err)
	}
	change := decode[JobChange](t, raw)
	if !change.Changed || change.Job.Enabled {
		t.Fatalf("the disable answered %+v", change)
	}
	if change.Job.Next != "" {
		t.Error("a disabled job carries a next instant")
	}

	raw, _ = call(t, d, "job.disable", map[string]any{"id": "nightly-sweep"})
	if again := decode[JobChange](t, raw); again.Changed {
		t.Error("disabling a disabled job reports a change")
	}
	if n := len(d.Store.Jobs()); n != 1 {
		t.Errorf("%d rows, want the disabled job kept", n)
	}

	raw, _ = call(t, d, "job.enable", map[string]any{"id": "nightly-sweep"})
	if change := decode[JobChange](t, raw); !change.Changed || !change.Job.Enabled {
		t.Errorf("the enable answered %+v", change)
	}
}

// Removing takes the row and its cursor, and a job that is not there is
// NOT_FOUND rather than a quiet success.
func TestRemovingTakesTheRowAndAnAbsentOneIsNotFound(t *testing.T) {
	d := newDaemon(t)
	call(t, d, "job.add", nightlyArgs())
	if _, err := call(t, d, "job.remove", map[string]any{"id": "nightly-sweep"}); err != nil {
		t.Fatalf("job.remove: %v", err)
	}
	if n := len(d.Store.Jobs()); n != 0 {
		t.Errorf("%d rows left", n)
	}
	_, err := call(t, d, "job.remove", map[string]any{"id": "nightly-sweep"})
	if got := codes.Of(err); got != codes.NotFound {
		t.Errorf("removing an absent job answered %s, want NOT_FOUND", got)
	}
}

// Every mutation lands on the job's own trail, in the same save as the row.
func TestEveryJobMutationIsOnTheJobsOwnTrail(t *testing.T) {
	d := newDaemon(t)
	call(t, d, "job.add", nightlyArgs())
	call(t, d, "job.disable", map[string]any{"id": "nightly-sweep"})
	call(t, d, "job.enable", map[string]any{"id": "nightly-sweep"})
	call(t, d, "job.remove", map[string]any{"id": "nightly-sweep"})

	var got []string
	for _, ev := range d.Store.Snapshot().JobEvents {
		got = append(got, ev.Name)
	}
	want := "sched.job.added,sched.job.disabled,sched.job.enabled,sched.job.removed"
	if strings.Join(got, ",") != want {
		t.Errorf("the job trail reads %v", got)
	}
}

// §4.2: a job is written in one project and listed in that project, and
// all_projects reads every one.
func TestAJobIsListedInTheProjectItWasWrittenIn(t *testing.T) {
	d := newDaemon(t)
	mine := protocolRequest("job.add", nightlyArgs(), "/repo/mine")
	if _, err := d.Handle(context.Background(), mine); err != nil {
		t.Fatalf("job.add: %v", err)
	}
	theirs := protocolRequest("job.add", withID(nightlyArgs(), "theirs"), "/repo/theirs")
	if _, err := d.Handle(context.Background(), theirs); err != nil {
		t.Fatalf("job.add: %v", err)
	}

	raw, _ := d.Handle(context.Background(), protocolRequest("job.list", nil, "/repo/mine"))
	if rep := decode[JobsReport](t, raw); rep.Count != 1 || rep.Jobs[0].ID != "nightly-sweep" {
		t.Errorf("this project's list answered %+v", rep)
	}
	every := protocolRequest("job.list", nil, "")
	every.AllProjects = true
	raw, _ = d.Handle(context.Background(), every)
	if rep := decode[JobsReport](t, raw); rep.Count != 2 {
		t.Errorf("every project's list answered %d rows, want 2", rep.Count)
	}
}

// A job fires INTO one project's board, so writing one across every project
// is refused rather than given a scope it cannot use.
func TestAJobCannotBeWrittenAcrossEveryProject(t *testing.T) {
	d := newDaemon(t)
	req := protocolRequest("job.add", nightlyArgs(), "")
	req.AllProjects = true
	if _, err := d.Handle(context.Background(), req); err == nil {
		t.Fatal("a job was written across every project")
	}
}

// The fire path, end to end against the fake board: the tick decides, the
// action reaches the sibling, and the run is on the trail as the schedule
// that caused it.
func TestADueJobFiresItsActionAtTheSibling(t *testing.T) {
	testenv.SkipUnlessFull(t)
	d, f := firingDaemon(t, "2026-08-25T03:00:20Z")
	f.Bin(t, "htask", `printf '{"task":{"id":"01T","seq":7,"title":"sweep the board"}}\n'`)
	addFiredYesterday(t, d, "2026-08-24T03:00:00Z")

	d.runDue(context.Background(), false)
	d.Fire.Wait()

	calls := f.Calls(t)
	if len(calls) != 1 {
		t.Fatalf("%d sibling calls, want one: %v", len(calls), calls)
	}
	if !strings.Contains(calls[0], "--as cron:nightly-sweep") {
		t.Errorf("the call does not declare the schedule that fired it: %s", calls[0])
	}
	if !strings.Contains(calls[0], "--json") {
		t.Errorf("the call does not ask for a document: %s", calls[0])
	}
	runs := runEvents(d)
	if len(runs) != 1 || runs[0].Name != "sched.run.fired" {
		t.Fatalf("the run trail reads %v", names(runs))
	}
	if runs[0].Actor != "cron:nightly-sweep" {
		t.Errorf("the run is attributed to %q", runs[0].Actor)
	}
	held, _ := d.Store.Job("nightly-sweep")
	if got := time.UnixMilli(held.LastFiredMS).UTC().Format(time.RFC3339); got != "2026-08-25T03:00:00Z" {
		t.Errorf("the cursor is at %s, want the SCHEDULED instant rather than the tick's clock", got)
	}
}

// The tick runs far more often than any schedule, and firing is once per
// instant however many ticks pass over it.
func TestASecondTickInsideOneInstantFiresNothingMore(t *testing.T) {
	testenv.SkipUnlessFull(t)
	d, f := firingDaemon(t, "2026-08-25T03:00:20Z")
	f.Bin(t, "htask", `printf '{"task":{"id":"01T","seq":7,"title":"sweep the board"}}\n'`)
	addFiredYesterday(t, d, "2026-08-24T03:00:00Z")

	d.runDue(context.Background(), false)
	d.runDue(context.Background(), false)
	d.Fire.Wait()

	if calls := f.Calls(t); len(calls) != 1 {
		t.Fatalf("%d sibling calls across two ticks, want one: %v", len(calls), calls)
	}
}

// note 2, and what doctor is asked for: a schedule missed while the daemon was
// down is skipped, said out loud on the trail, and named by doctor.
func TestAMissedScheduleIsSkippedAtStartAndDoctorSaysSo(t *testing.T) {
	testenv.SkipUnlessFull(t)
	d, f := firingDaemon(t, "2026-08-25T09:00:00Z")
	f.Bin(t, "htask", `printf '{"task":{"id":"01T","seq":7,"title":"sweep the board"}}\n'`)
	addFiredYesterday(t, d, "2026-08-22T03:00:00Z")

	d.runDue(context.Background(), true)
	d.Fire.Wait()

	if calls := f.Calls(t); len(calls) != 0 {
		t.Fatalf("a missed schedule fired anyway: %v", calls)
	}
	trail := d.Store.Snapshot().JobEvents
	last := trail[len(trail)-1]
	if last.Name != "sched.job.skipped" {
		t.Fatalf("the job trail ends with %q", last.Name)
	}
	if last.Detail["missed"] != float64(3) && last.Detail["missed"] != 3 {
		t.Errorf("the skip records %v missed instants, want 3", last.Detail["missed"])
	}
	rep := d.doctor()
	if len(rep.Jobs.SkippedAtStart) != 1 {
		t.Fatalf("doctor names %d skipped jobs, want 1", len(rep.Jobs.SkippedAtStart))
	}
	skip := rep.Jobs.SkippedAtStart[0]
	if skip.Job != "nightly-sweep" || skip.Missed != 3 {
		t.Errorf("doctor says %+v", skip)
	}
	if skip.From != "2026-08-23T03:00:00Z" || skip.Through != "2026-08-25T03:00:00Z" {
		t.Errorf("the skip runs %s to %s", skip.From, skip.Through)
	}
	// The cursor still moved, so the next tick does not fire the backlog.
	held, _ := d.Store.Job("nightly-sweep")
	if time.UnixMilli(held.LastFiredMS).UTC().Format(time.RFC3339) != "2026-08-25T03:00:00Z" {
		t.Errorf("the cursor is at %d after a skip", held.LastFiredMS)
	}
}

// catch_up fires ONCE for the latest missed instant, never once per miss.
func TestCatchUpFiresOnceAtStart(t *testing.T) {
	testenv.SkipUnlessFull(t)
	d, f := firingDaemon(t, "2026-08-25T09:00:00Z")
	f.Bin(t, "htask", `printf '{"task":{"id":"01T","seq":7,"title":"sweep the board"}}\n'`)
	args := nightlyArgs()
	args["catch_up"] = true
	if _, err := call(t, d, "job.add", args); err != nil {
		t.Fatalf("job.add: %v", err)
	}
	fireCursor(t, d, "2026-08-22T03:00:00Z")

	d.runDue(context.Background(), true)
	d.Fire.Wait()

	if calls := f.Calls(t); len(calls) != 1 {
		t.Fatalf("%d sibling calls for a catch-up, want exactly one: %v", len(calls), calls)
	}
	runs := runEvents(d)
	if len(runs) != 1 || runs[0].Name != "sched.run.fired" {
		t.Fatalf("the run trail reads %v", names(runs))
	}
	// The two older instants it stood in for are still said out loud.
	if len(d.doctor().Jobs.SkippedAtStart) != 1 {
		t.Error("a catch-up that stood in for two older instants recorded no skip")
	}
}

// A sibling that is unreachable is a loud failed run, and the schedule keeps
// ticking (note 2, and the fire path's own rule).
func TestAnUnreachableSiblingIsAFailedRunAndTheDaemonKeepsTicking(t *testing.T) {
	testenv.SkipUnlessFull(t)
	d, _ := firingDaemon(t, "2026-08-25T03:00:20Z")
	// No htask fake is written, so the board is not on PATH at all.
	addFiredYesterday(t, d, "2026-08-24T03:00:00Z")

	d.runDue(context.Background(), false)
	d.Fire.Wait()

	runs := runEvents(d)
	if len(runs) != 1 || runs[0].Name != "sched.run.failed" {
		t.Fatalf("the run trail reads %v, want one failed run", names(runs))
	}
	if runs[0].Detail["error"] == nil {
		t.Error("the failed run says nothing about why")
	}
	// And the cursor moved, so the next tick is not the same failure again.
	held, _ := d.Store.Job("nightly-sweep")
	if held.LastFiredMS == 0 {
		t.Error("a failed fire left the cursor where it was, so it fires again forever")
	}
}

// The store is a JSON document an operator can edit. A row that no longer
// parses is loud, and it stops nothing else from firing.
func TestAHandEditedScheduleIsLoudAndStopsNothingElse(t *testing.T) {
	testenv.SkipUnlessFull(t)
	d, f := firingDaemon(t, "2026-08-25T03:00:20Z")
	f.Bin(t, "htask", `printf '{"task":{"id":"01T","seq":7,"title":"sweep the board"}}\n'`)
	addFiredYesterday(t, d, "2026-08-24T03:00:00Z")
	broken := nightlyArgs()
	broken["id"] = "hand-edited"
	if _, err := call(t, d, "job.add", broken); err != nil {
		t.Fatalf("job.add: %v", err)
	}
	fireCursor(t, d, "2026-08-24T03:00:00Z")
	handEdit(t, d, "hand-edited", "0 3 * *")

	d.runDue(context.Background(), false)
	d.Fire.Wait()

	var failed, fired int
	for _, ev := range runEvents(d) {
		switch ev.Name {
		case "sched.run.failed":
			failed++
		case "sched.run.fired":
			fired++
		}
	}
	if failed != 1 || fired != 1 {
		t.Fatalf("%d failed and %d fired runs, want one of each", failed, fired)
	}
	if calls := f.Calls(t); len(calls) != 1 {
		t.Errorf("%d sibling calls, want the readable job's one: %v", len(calls), calls)
	}
}

// A disabled job is passed over silently: no fire, no skip record.
func TestADisabledJobFiresNothingAndRecordsNoSkip(t *testing.T) {
	testenv.SkipUnlessFull(t)
	d, f := firingDaemon(t, "2026-08-25T09:00:00Z")
	f.Bin(t, "htask", `printf '{"task":{"id":"01T"}}\n'`)
	addFiredYesterday(t, d, "2026-08-22T03:00:00Z")
	call(t, d, "job.disable", map[string]any{"id": "nightly-sweep"})

	d.runDue(context.Background(), true)
	d.Fire.Wait()

	if calls := f.Calls(t); len(calls) != 0 {
		t.Errorf("a disabled job fired: %v", calls)
	}
	if len(d.doctor().Jobs.SkippedAtStart) != 0 {
		t.Error("a disabled job was recorded as skipped")
	}
}

// A run reaches the followers and the §8.3 hook the same way every other
// event does: written in one place and delivered in both.
func TestAFiredRunReachesTheFollowers(t *testing.T) {
	testenv.SkipUnlessFull(t)
	d, f := firingDaemon(t, "2026-08-25T03:00:20Z")
	f.Bin(t, "htask", `printf '{"task":{"id":"01T","seq":7,"title":"sweep"}}\n'`)
	addFiredYesterday(t, d, "2026-08-24T03:00:00Z")

	live := make(chan store.Event, 8)
	if _, err := d.followers.attach(live, func() ([]store.Event, error) { return nil, nil }); err != nil {
		t.Fatalf("attach: %v", err)
	}
	defer d.followers.detach(live)

	d.runDue(context.Background(), false)
	d.Fire.Wait()

	select {
	case ev := <-live:
		if ev.Name != "sched.run.fired" {
			t.Errorf("the follower was handed %q", ev.Name)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a run was written and no follower was told")
	}
}

// firingDaemon is a daemon with a runner and a pinned clock, which is what
// every case above drives.
func firingDaemon(t *testing.T, now string) (*Daemon, *testenv.Fake) {
	t.Helper()
	f := testenv.New(t)
	d := newDaemonIn(t, now)
	d.Fire = &fire.Runner{Store: d.Store, Now: d.now, Emit: d.Emitted}
	return d, f
}

// newDaemonIn is newDaemon with the clock pinned to a named instant. It does
// not call testenv.New: the caller has already built the world.
func newDaemonIn(t *testing.T, now string) *Daemon {
	t.Helper()
	when, err := time.Parse(time.RFC3339, now)
	if err != nil {
		t.Fatalf("read %q: %v", now, err)
	}
	st, err := store.Open(config.StorePath())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	return &Daemon{
		Store: st, Config: cfg, Interval: time.Hour, Version: "0.1.0",
		Log: log.New(io.Discard, "", 0), Now: func() time.Time { return when.UTC() },
	}
}

// addFiredYesterday writes the nightly job and puts its cursor at a named
// instant, which is what a job that has been running for a while looks like.
func addFiredYesterday(t *testing.T, d *Daemon, last string) {
	t.Helper()
	if _, err := call(t, d, "job.add", nightlyArgs()); err != nil {
		t.Fatalf("job.add: %v", err)
	}
	fireCursor(t, d, last)
}

// fireCursor moves every job's cursor to a named instant.
func fireCursor(t *testing.T, d *Daemon, at string) {
	t.Helper()
	when, err := time.Parse(time.RFC3339, at)
	if err != nil {
		t.Fatalf("read %q: %v", at, err)
	}
	for _, j := range d.Store.Jobs() {
		if j.LastFiredMS != 0 {
			continue
		}
		if err := d.Store.AdvanceJob(j.ID, when.UnixMilli(), nil); err != nil {
			t.Fatalf("AdvanceJob: %v", err)
		}
	}
}

// handEdit is the operator editing the store's JSON into something that no
// longer parses, which is the one way a broken row reaches the tick.
func handEdit(t *testing.T, d *Daemon, id, schedule string) {
	t.Helper()
	held, ok := d.Store.Job(id)
	if !ok {
		t.Fatalf("no job %s", id)
	}
	held.Schedule = schedule
	ev := store.NewEvent(d.now(), store.EntityJob, store.KindAdded, id, "operator", nil)
	if _, err := d.Store.RemoveJob(id, ev); err != nil {
		t.Fatalf("RemoveJob: %v", err)
	}
	if err := d.Store.AddJob(held, ev); err != nil {
		t.Fatalf("AddJob: %v", err)
	}
}

func runEvents(d *Daemon) []store.Event { return d.Store.Snapshot().RunEvents }

func names(evs []store.Event) []string {
	var out []string
	for _, ev := range evs {
		out = append(out, ev.Name)
	}
	return out
}

// protocolRequest is one call in a named project.
func protocolRequest(verb string, args map[string]any, project string) protocol.Request {
	if args == nil {
		args = map[string]any{}
	}
	return protocol.Request{Verb: verb, Args: args, Project: project, Pane: "wT:p1", Door: "cli"}
}

func withID(args map[string]any, id string) map[string]any {
	args["id"] = id
	return args
}
