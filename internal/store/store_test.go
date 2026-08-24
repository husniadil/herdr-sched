package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/husniadil/herdr-sched/internal/codes"
)

func at(ms int64) time.Time { return time.UnixMilli(ms) }

func parkedEvent(ms int64, kind, id string) Event {
	return NewEvent(at(ms), EntityParked, kind, id, "agent:wA:p1", nil)
}

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "sched.json"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

// Every entity keeps its own trail beside it, and both are saved together
// because the document is written whole: a change and the event recording it
// can never land one without the other.
func TestAParkedActionAndItsEventAreSavedTogether(t *testing.T) {
	s := openTemp(t)
	p := Parked{ID: "pk-1", Verb: "sched.stop", State: ParkedWaiting, AtMS: 1}
	if err := s.Park(p, parkedEvent(1, KindParked, "pk-1")); err != nil {
		t.Fatalf("Park: %v", err)
	}

	reopened, err := Open(s.Path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	doc := reopened.Snapshot()
	if len(doc.Parked) != 1 || doc.Parked[0].ID != "pk-1" {
		t.Fatalf("parked = %+v", doc.Parked)
	}
	if len(doc.ParkedEvents) != 1 || doc.ParkedEvents[0].EntityID != "pk-1" {
		t.Fatalf("parked_events = %+v", doc.ParkedEvents)
	}
}

// §8.1 spells the event name out of its parts, so the name and the fields can
// never disagree.
func TestTheEventNameIsSpelledFromItsParts(t *testing.T) {
	ev := NewEvent(at(7), EntityParked, KindResolved, "pk-1", "agent:wA:p1", map[string]any{"verb": "sched.stop"})
	if ev.Name != "sched.parked.resolved" {
		t.Fatalf("name = %q, want sched.parked.resolved", ev.Name)
	}
	if ev.AtMS != 7 || ev.Entity != EntityParked || ev.Kind != KindResolved {
		t.Fatalf("event = %+v", ev)
	}
	// §8.1 names the timestamp `at` on the wire.
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"id", "at", "actor", "entity", "kind", "name"} {
		if _, ok := wire[field]; !ok {
			t.Errorf("the wire form carries no %q", field)
		}
	}
}

// The document is written whole and read back whole, and an unknown shape is
// refused rather than overwritten.
func TestAStoreFromAnUnknownVersionIsRefusedRatherThanOverwritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sched.json")
	if err := os.WriteFile(path, []byte(`{"version":99,"parked":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Open(path)
	if err == nil {
		t.Fatal("a store from an unknown version was opened")
	}
	if codes.Of(err) != codes.Unsupported {
		t.Fatalf("code = %s, want UNSUPPORTED", codes.Of(err))
	}
	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(raw), `"version":99`) {
		t.Fatal("the refused document was rewritten")
	}
}

// A store that has never been written is an empty one, not a failure.
func TestAMissingStoreOpensEmpty(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "nothing-here.json"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	doc := s.Snapshot()
	if doc.Version != Version || len(doc.Parked) != 0 || len(doc.ParkedEvents) != 0 {
		t.Fatalf("document = %+v", doc)
	}
	// Empty lists rather than nulls: a reader has to be able to tell "none"
	// from "this daemon could not say".
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "null") {
		t.Fatalf("the empty document renders a null: %s", body)
	}
}

// The move to a decided state is the one-winner check, and it happens before
// the verb runs so two resolvers cannot both run it.
func TestOnlyOneResolverWinsAParkedAction(t *testing.T) {
	s := openTemp(t)
	if err := s.Park(Parked{ID: "pk-1", Verb: "sched.stop", State: ParkedWaiting}, parkedEvent(1, KindParked, "pk-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimParked("pk-1", ParkedResolved, "agent:wA:p1", 2, parkedEvent(2, KindResolved, "pk-1")); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	_, err := s.ClaimParked("pk-1", ParkedResolved, "agent:wA:p2", 3, parkedEvent(3, KindResolved, "pk-1"))
	if err == nil {
		t.Fatal("a second resolver won the same row")
	}
	if codes.Of(err) != codes.Conflict || codes.ReasonOf(err) != codes.AlreadyResolved {
		t.Fatalf("second claim refused with %s / %s", codes.Of(err), codes.ReasonOf(err))
	}
	if !strings.Contains(codes.Message(err), "agent:wA:p1") {
		t.Errorf("the refusal does not name who got there first: %v", err)
	}
}

// A resolved action whose verb failed stays decided and stays visible: hiding
// it would make a verb that did not happen look like one that did.
func TestAFailedActionStaysDecidedAndStaysWaiting(t *testing.T) {
	s := openTemp(t)
	if err := s.Park(Parked{ID: "pk-1", Verb: "sched.stop", State: ParkedWaiting}, parkedEvent(1, KindParked, "pk-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimParked("pk-1", ParkedResolved, "agent:wA:p1", 2, parkedEvent(2, KindResolved, "pk-1")); err != nil {
		t.Fatal(err)
	}
	if err := s.FailParked("pk-1", "the daemon was not serving", parkedEvent(3, KindFailed, "pk-1")); err != nil {
		t.Fatalf("FailParked: %v", err)
	}
	rows := s.Parked()
	if rows[0].State != ParkedFailed || rows[0].Error == "" {
		t.Fatalf("row = %+v", rows[0])
	}
	if !rows[0].Waiting() {
		t.Fatal("a failed action stopped wanting the operator's attention")
	}
	if s.WaitingParked() != 1 {
		t.Fatalf("waiting = %d, want 1", s.WaitingParked())
	}
}

func TestResolvingAnActionThatIsNotThereIsNotFound(t *testing.T) {
	s := openTemp(t)
	_, err := s.ClaimParked("pk-missing", ParkedResolved, "agent:wA:p1", 1, parkedEvent(1, KindResolved, "pk-missing"))
	if codes.Of(err) != codes.NotFound {
		t.Fatalf("code = %s, want NOT_FOUND", codes.Of(err))
	}
}

// The trail a reader sees is every entity's events in one sequence, oldest
// first, whatever order they were written in.
func TestTheMergedTrailIsOldestFirst(t *testing.T) {
	s := openTemp(t)
	for _, ms := range []int64{3, 1, 2} {
		if err := s.Park(Parked{ID: "pk", State: ParkedWaiting}, parkedEvent(ms, KindParked, "pk")); err != nil {
			t.Fatal(err)
		}
	}
	trail := s.Trail()
	for i := 1; i < len(trail); i++ {
		if trail[i-1].AtMS > trail[i].AtMS {
			t.Fatalf("the trail is out of order: %v", trail)
		}
	}
}

// A resume from an id the rotation has passed is refused rather than answered
// with the whole window, which a consumer would take for its own tail.
func TestResumingFromARotatedEventIsRefused(t *testing.T) {
	trail := []Event{parkedEvent(1, KindParked, "pk-1")}
	if _, err := Select(trail, EventFilter{SinceID: "ev-0000000000000-deadbeef"}); err == nil {
		t.Fatal("a rotated id read as no filter at all")
	} else if codes.Of(err) != codes.NotFound {
		t.Fatalf("code = %s, want NOT_FOUND", codes.Of(err))
	}
}

func TestSelectResumesAndLimits(t *testing.T) {
	trail := []Event{
		parkedEvent(1, KindParked, "pk-1"),
		parkedEvent(2, KindResolved, "pk-1"),
		parkedEvent(3, KindParked, "pk-2"),
	}
	got, err := Select(trail, EventFilter{SinceID: trail[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != trail[1].ID {
		t.Fatalf("resume = %+v", got)
	}
	got, err = Select(trail, EventFilter{SinceMS: 1, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].AtMS != 2 {
		t.Fatalf("since+limit = %+v", got)
	}
}

// The trail is bounded, because the whole document is written on every change.
func TestTheTrailIsBoundedNewestKept(t *testing.T) {
	trail := make([]Event, 0, MaxEvents+10)
	for i := 0; i < MaxEvents+10; i++ {
		trail = append(trail, parkedEvent(int64(i+1), KindParked, "pk"))
	}
	rotated := Rotate(trail)
	if len(rotated) != MaxEvents {
		t.Fatalf("trail holds %d, want %d", len(rotated), MaxEvents)
	}
	if rotated[len(rotated)-1].ID != trail[len(trail)-1].ID {
		t.Fatal("rotation dropped the newest event")
	}
}

// The file is this user's alone, the same way the state dir is (§3.5).
func TestTheStoreFileIsClosedToOtherUsers(t *testing.T) {
	s := openTemp(t)
	if err := s.Park(Parked{ID: "pk-1", State: ParkedWaiting}, parkedEvent(1, KindParked, "pk-1")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("store file is %o, want 0600", perm)
	}
}

// A caller cannot write through the lists it was handed.
func TestASnapshotCannotBeWrittenThrough(t *testing.T) {
	s := openTemp(t)
	if err := s.Park(Parked{ID: "pk-1", State: ParkedWaiting}, parkedEvent(1, KindParked, "pk-1")); err != nil {
		t.Fatal(err)
	}
	doc := s.Snapshot()
	doc.Parked[0].State = "tampered"
	if s.Snapshot().Parked[0].State != ParkedWaiting {
		t.Fatal("a snapshot shares its rows with the store")
	}
}
