package fire

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/husniadil/herdr-sched/internal/action"
	"github.com/husniadil/herdr-sched/internal/store"
	"github.com/husniadil/herdr-sched/internal/testenv"
)

func runner(t *testing.T) (*Runner, *testenv.Fake) {
	t.Helper()
	f := testenv.New(t)
	s, err := store.Open(filepath.Join(t.TempDir(), "sched.json"))
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	at := time.Unix(1700000000, 0)
	return &Runner{Store: s, Now: func() time.Time { return at }}, f
}

var nightly = action.Source{Kind: action.SourceCron, ID: "nightly"}

// Each of the four kinds reaches its own sibling, and every call declares the
// firing signal on the argv (§3.2). This is the done-when of the vocabulary:
// four actions, four fakes, four principals asserted.
func TestTheFourActionsFireAtTheirSiblings(t *testing.T) {
	testenv.SkipUnlessFull(t)
	cases := []struct {
		name   string
		bin    string
		answer string
		act    action.Action
		want   string
	}{
		{
			name: "task", bin: "htask",
			answer: `{"task":{"id":"01AAA","seq":7,"title":"sweep"}}`,
			act:    action.Action{Kind: action.KindTask, Args: map[string]string{"title": "sweep"}},
			want:   "create sweep --json --as cron:nightly",
		},
		{
			name: "mail", bin: "hmail",
			answer: `{"message":{"id":"01MSG","kind":"notify"}}`,
			act:    action.Action{Kind: action.KindMail, Args: map[string]string{"to": "wM:p1", "body": "up"}},
			want:   "send wM:p1 up --json --as cron:nightly",
		},
		{
			name: "ask", bin: "hmail",
			answer: `{"message":{"id":"01ASK","kind":"ask"}}`,
			act:    action.Action{Kind: action.KindMail, Args: map[string]string{"to": "wM:p1", "body": "up", "ask": "true"}},
			want:   "ask wM:p1 up --json --as cron:nightly",
		},
		{
			name: "dispatch", bin: "hdis",
			answer: `{"task":"01AAA","seq":7,"title":"sweep"}`,
			act:    action.Action{Kind: action.KindDispatch, Args: map[string]string{"task": "01AAA"}},
			want:   "dispatch 01AAA --json --as cron:nightly",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, f := runner(t)
			f.Bin(t, c.bin, "echo '"+c.answer+"'")
			if err := r.Fire(context.Background(), nightly, c.act); err != nil {
				t.Fatalf("fire: %v", err)
			}
			r.Wait()
			if got := f.Calls(t)[0]; got != c.want {
				t.Fatalf("argv:\n got %q\nwant %q", got, c.want)
			}
			ev := onlyEvent(t, r)
			if ev.Kind != store.KindFired || ev.Actor != "cron:nightly" || ev.Entity != store.EntityRun {
				t.Fatalf("event is %+v", ev)
			}
			if ev.Detail["action"] != c.act.Kind {
				t.Fatalf("the trail does not say what fired: %+v", ev.Detail)
			}
		})
	}
}

// The fourth action reaches no sibling: it runs on the host, and its output
// is on the run the operator reads.
func TestAShellActionRunsAndItsOutputIsOnTheRun(t *testing.T) {
	testenv.SkipUnlessFull(t)
	r, _ := runner(t)
	act := action.Action{Kind: action.KindShell, Args: map[string]string{"command": "echo swept"}}
	if err := r.Fire(context.Background(), nightly, act); err != nil {
		t.Fatalf("fire: %v", err)
	}
	r.Wait()
	ev := onlyEvent(t, r)
	if ev.Kind != store.KindFired {
		t.Fatalf("event is %+v", ev)
	}
	if out, _ := ev.Detail["output"].(string); !strings.Contains(out, "swept") {
		t.Fatalf("the run kept no output: %+v", ev.Detail)
	}
}

// The shell action does not block the tick: Fire returns while the command
// is still running, and the run lands on the trail when it ends.
func TestAShellActionDoesNotBlockTheTick(t *testing.T) {
	testenv.SkipUnlessFull(t)
	r, _ := runner(t)
	act := action.Action{Kind: action.KindShell, Args: map[string]string{"command": "sleep 0.4; echo late"}}
	start := time.Now()
	if err := r.Fire(context.Background(), nightly, act); err != nil {
		t.Fatalf("fire: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("Fire held the tick for %s", elapsed)
	}
	if got := len(r.Store.Trail()); got != 0 {
		t.Fatalf("the run was recorded before it ended: %d events", got)
	}
	r.Wait()
	if ev := onlyEvent(t, r); !strings.Contains(ev.Detail["output"].(string), "late") {
		t.Fatalf("event is %+v", ev)
	}
}

// A sibling that is not there is a LOUD failed run on the trail, never a
// silent skip: this is the difference between a schedule that stopped working
// in January and a schedule nobody noticed had stopped working in January.
func TestAnUnreachableSiblingIsAFailedRunAndNotASilentSkip(t *testing.T) {
	testenv.SkipUnlessFull(t)
	r, _ := runner(t) // testenv replaces PATH, so no fake means not installed
	act := action.Action{Kind: action.KindTask, Args: map[string]string{"title": "sweep"}}
	err := r.Fire(context.Background(), nightly, act)
	if err == nil {
		t.Fatal("want the failure reported to the caller as well as to the trail")
	}
	r.Wait()
	ev := onlyEvent(t, r)
	if ev.Kind != store.KindFailed {
		t.Fatalf("event is %+v", ev)
	}
	reason, _ := ev.Detail["error"].(string)
	if !strings.Contains(reason, "htask") {
		t.Fatalf("the failed run does not name the sibling: %+v", ev.Detail)
	}
}

// A sibling that refuses is a failed run carrying the sibling's own words and
// its contract code, so the operator reads why without a second lookup.
func TestASiblingsRefusalIsOnTheFailedRunInItsOwnWords(t *testing.T) {
	testenv.SkipUnlessFull(t)
	r, f := runner(t)
	f.Bin(t, "hdis", `echo '{"error":{"code":"CONFLICT","message":"AT_CAPACITY: the fleet is full"}}'; exit 6`)
	act := action.Action{Kind: action.KindDispatch, Args: map[string]string{"task": "01AAA"}}
	if err := r.Fire(context.Background(), nightly, act); err == nil {
		t.Fatal("want an error")
	}
	r.Wait()
	ev := onlyEvent(t, r)
	if ev.Kind != store.KindFailed {
		t.Fatalf("event is %+v", ev)
	}
	if code, _ := ev.Detail["code"].(string); code != "CONFLICT" {
		t.Fatalf("the failed run does not carry the sibling's code: %+v", ev.Detail)
	}
	if reason, _ := ev.Detail["error"].(string); !strings.Contains(reason, "AT_CAPACITY") {
		t.Fatalf("the failed run does not carry the sibling's words: %+v", ev.Detail)
	}
}

// A shell command that exits non-zero is a failed run too, with its output.
func TestAFailingCommandIsAFailedRunWithItsOutput(t *testing.T) {
	testenv.SkipUnlessFull(t)
	r, _ := runner(t)
	act := action.Action{Kind: action.KindShell, Args: map[string]string{"command": "echo nope >&2; exit 3"}}
	if err := r.Fire(context.Background(), nightly, act); err != nil {
		t.Fatalf("a detached command's failure arrives on the trail, not at the caller: %v", err)
	}
	r.Wait()
	ev := onlyEvent(t, r)
	if ev.Kind != store.KindFailed {
		t.Fatalf("event is %+v", ev)
	}
	if out, _ := ev.Detail["output"].(string); !strings.Contains(out, "nope") {
		t.Fatalf("the failed run dropped the output: %+v", ev.Detail)
	}
}

// An action that could not be built is refused before anything is spawned:
// validation is a create-time check and firing re-runs it rather than
// trusting a row that may have been edited by hand.
func TestAnInvalidActionFiresNothing(t *testing.T) {
	testenv.SkipUnlessFull(t)
	r, f := runner(t)
	f.Bin(t, "htask", `echo '{"task":{"id":"01AAA"}}'`)
	err := r.Fire(context.Background(), nightly, action.Action{Kind: action.KindTask})
	if err == nil || !strings.Contains(err.Error(), "title") {
		t.Fatalf("want a refusal naming title, got %v", err)
	}
	if len(f.Calls(t)) != 0 {
		t.Fatalf("something was spawned: %v", f.Calls(t))
	}
}

// A source that cannot say what fired the call is refused for the same
// reason: a sibling trail carrying a bare `cron:` is a fact nobody can trace.
func TestAnUnattributableSourceFiresNothing(t *testing.T) {
	testenv.SkipUnlessFull(t)
	r, f := runner(t)
	f.Bin(t, "htask", `echo '{"task":{"id":"01AAA"}}'`)
	act := action.Action{Kind: action.KindTask, Args: map[string]string{"title": "sweep"}}
	if err := r.Fire(context.Background(), action.Source{Kind: action.SourceCron}, act); err == nil {
		t.Fatal("want a refusal")
	}
	if len(f.Calls(t)) != 0 {
		t.Fatalf("something was spawned: %v", f.Calls(t))
	}
}

// Note 2: the run history IS the §8 stream. A run is on the trail and there
// is no second table for it — and it is in the entity's OWN trail beside the
// document, the way every entity here keeps one.
func TestARunIsOnTheTrailAndNowhereElse(t *testing.T) {
	testenv.SkipUnlessFull(t)
	r, _ := runner(t)
	act := action.Action{Kind: action.KindShell, Args: map[string]string{"command": "true"}}
	if err := r.Fire(context.Background(), nightly, act); err != nil {
		t.Fatalf("fire: %v", err)
	}
	r.Wait()
	doc := r.Store.Snapshot()
	if len(doc.RunEvents) != 1 {
		t.Fatalf("run_events holds %d", len(doc.RunEvents))
	}
	if len(doc.ParkedEvents) != 0 {
		t.Fatal("the run landed in another entity's trail")
	}
	if got := doc.RunEvents[0].Name; got != "sched.run.fired" {
		t.Fatalf("the §8.1 name is %q", got)
	}
}

func onlyEvent(t *testing.T, r *Runner) store.Event {
	t.Helper()
	trail := r.Store.Trail()
	if len(trail) != 1 {
		t.Fatalf("the trail holds %d events, want exactly one run", len(trail))
	}
	return trail[0]
}
