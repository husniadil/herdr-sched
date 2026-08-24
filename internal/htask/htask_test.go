package htask

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

// A fired task action files one task on the board, and the board's own trail
// records the schedule as the actor (§3.2).
func TestCreateFilesTheTaskAsTheFiringSignal(t *testing.T) {
	testenv.SkipUnlessFull(t)
	c, f := client(t)
	f.Bin(t, "htask", `echo '{"task":{"id":"01AAA","seq":7,"project":"/src/p","title":"sweep","status":"todo"}}'`)

	task, err := c.Create(context.Background(), Draft{Title: "sweep", Description: "the nightly one", Project: "/src/p", Priority: 3})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if task.ID != "01AAA" || task.Seq != 7 || task.Project != "/src/p" {
		t.Fatalf("got %+v", task)
	}
	want := "create sweep --description the nightly one --project /src/p --priority 3 --json --as cron:nightly"
	if got := f.Calls(t)[0]; got != want {
		t.Fatalf("argv:\n got %q\nwant %q", got, want)
	}
}

// The optional arguments are absent from the argv when the action does not
// carry them, rather than passed empty: an empty --project is a project named
// "", which is not the same as letting the board resolve its own (§4.2).
func TestAnAbsentOptionIsNotOnTheArgv(t *testing.T) {
	testenv.SkipUnlessFull(t)
	c, f := client(t)
	f.Bin(t, "htask", `echo '{"task":{"id":"01AAA","title":"sweep"}}'`)

	if _, err := c.Create(context.Background(), Draft{Title: "sweep"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got, want := f.Calls(t)[0], "create sweep --json --as cron:nightly"; got != want {
		t.Fatalf("argv:\n got %q\nwant %q", got, want)
	}
}

// A board that refuses is reported in the board's own words, with its code.
func TestTheBoardsRefusalIsCarriedInItsOwnWords(t *testing.T) {
	testenv.SkipUnlessFull(t)
	c, f := client(t)
	f.Bin(t, "htask", `echo '{"error":{"code":"USAGE","message":"a task needs a title"}}'; exit 2`)

	_, err := c.Create(context.Background(), Draft{Title: "sweep"})
	var refusal *sibling.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("want a refusal, got %v", err)
	}
	if refusal.Code != "USAGE" || refusal.Sibling != "htask" {
		t.Fatalf("got %+v", refusal)
	}
}

// A board that is not installed is a loud failure, never a silent skip.
func TestAMissingBoardIsLoud(t *testing.T) {
	testenv.SkipUnlessFull(t)
	c, _ := client(t)
	c.Bin = "htask-that-is-not-installed"
	_, err := c.Create(context.Background(), Draft{Title: "sweep"})
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "htask") {
		t.Fatalf("the failure does not name the sibling: %v", err)
	}
}

// A board that answered nothing is a failure too: reading it as an empty row
// would record a fired run whose task nobody can find.
func TestAnAnswerWithNoTaskIsAFailure(t *testing.T) {
	testenv.SkipUnlessFull(t)
	c, f := client(t)
	f.Bin(t, "htask", `echo '{}'`)
	if _, err := c.Create(context.Background(), Draft{Title: "sweep"}); err == nil {
		t.Fatal("want an error")
	}
}
