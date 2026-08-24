package hdis

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/husniadil/herdr-sched/internal/sibling"
	"github.com/husniadil/herdr-sched/internal/testenv"
)

func client(t *testing.T) (*Client, *testenv.Fake) {
	t.Helper()
	f := testenv.New(t)
	return &Client{Principal: "cron:nightly"}, f
}

// A dispatch reserves one ready task for the next tick and answers at once;
// the dispatcher's own trail records the schedule as the caller (§3.2).
func TestDispatchReservesTheTaskAsTheFiringSignal(t *testing.T) {
	testenv.SkipUnlessFull(t)
	c, f := client(t)
	f.Bin(t, "hdis", `echo '{"task":"01AAA","seq":7,"title":"sweep","project":"/src/p"}'`)

	res, err := c.Dispatch(context.Background(), "01AAA", "/src/p")
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if res.TaskID != "01AAA" || res.Seq != 7 || res.Title != "sweep" {
		t.Fatalf("got %+v", res)
	}
	want := "dispatch 01AAA --project /src/p --json --as cron:nightly"
	if got := f.Calls(t)[0]; got != want {
		t.Fatalf("argv:\n got %q\nwant %q", got, want)
	}
}

// Without a project the dispatcher looks on every board, which is its own
// default: an empty --project would scope the call to a project named "".
func TestAnUnscopedDispatchPassesNoProject(t *testing.T) {
	testenv.SkipUnlessFull(t)
	c, f := client(t)
	f.Bin(t, "hdis", `echo '{"task":"01AAA","seq":7,"title":"sweep"}'`)

	if _, err := c.Dispatch(context.Background(), "01AAA", ""); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if got, want := f.Calls(t)[0], "dispatch 01AAA --json --as cron:nightly"; got != want {
		t.Fatalf("argv:\n got %q\nwant %q", got, want)
	}
}

// A dispatcher that refuses — the fleet is full, the board is not ready — is
// carried in its own words, with the sub-reason it opened the message with.
func TestTheDispatchersRefusalIsCarriedInItsOwnWords(t *testing.T) {
	testenv.SkipUnlessFull(t)
	c, f := client(t)
	f.Bin(t, "hdis", `echo '{"error":{"code":"CONFLICT","message":"AT_CAPACITY: the fleet is full"}}'; exit 6`)

	_, err := c.Dispatch(context.Background(), "01AAA", "")
	var refusal *sibling.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("want a refusal, got %v", err)
	}
	if refusal.Code != "CONFLICT" || !strings.Contains(refusal.Message, "AT_CAPACITY") {
		t.Fatalf("got %+v", refusal)
	}
}

// A dispatcher that is not installed is a loud failure, never a silent skip.
func TestAMissingDispatcherIsLoud(t *testing.T) {
	testenv.SkipUnlessFull(t)
	c, _ := client(t)
	c.Bin = "hdis-that-is-not-installed"
	if _, err := c.Dispatch(context.Background(), "01AAA", ""); err == nil {
		t.Fatal("want an error")
	} else if !strings.Contains(err.Error(), "hdis") {
		t.Fatalf("the failure does not name the sibling: %v", err)
	}
}

// An answer naming no task is a failure: a reservation this plugin cannot
// name is a fired run nobody can trace to a worker.
func TestAnAnswerWithNoReservationIsAFailure(t *testing.T) {
	testenv.SkipUnlessFull(t)
	c, f := client(t)
	f.Bin(t, "hdis", `echo '{}'`)
	if _, err := c.Dispatch(context.Background(), "01AAA", ""); err == nil {
		t.Fatal("want an error")
	}
}
