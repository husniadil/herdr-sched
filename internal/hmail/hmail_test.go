package hmail

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
	return &Client{Principal: "trigger:01TRG"}, f
}

// A notify owes nothing back, and the mailbox records the firing signal as
// the sender (§3.2).
func TestSendPostsANotifyAsTheFiringSignal(t *testing.T) {
	testenv.SkipUnlessFull(t)
	c, f := client(t)
	f.Bin(t, "hmail", `echo '{"message":{"id":"01MSG","kind":"notify","to":"wM:p1"},"delivered":true}'`)

	m, err := c.Send(context.Background(), Draft{To: "wM:p1", Body: "the build is red"})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if m.ID != "01MSG" || m.Kind != "notify" {
		t.Fatalf("got %+v", m)
	}
	want := "send wM:p1 the build is red --json --as trigger:01TRG"
	if got := f.Calls(t)[0]; got != want {
		t.Fatalf("argv:\n got %q\nwant %q", got, want)
	}
}

// An ask is the same call under the other verb: it owes a correlated reply
// back, and that obligation is the whole difference.
func TestAskPostsAnAskUnderTheAskVerb(t *testing.T) {
	testenv.SkipUnlessFull(t)
	c, f := client(t)
	f.Bin(t, "hmail", `echo '{"message":{"id":"01ASK","kind":"ask","to":"wM:p1"}}'`)

	m, err := c.Ask(context.Background(), Draft{To: "wM:p1", Body: "still on it?", Project: "/src/p"})
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if m.ID != "01ASK" || m.Kind != "ask" {
		t.Fatalf("got %+v", m)
	}
	want := "ask wM:p1 still on it? --project /src/p --json --as trigger:01TRG"
	if got := f.Calls(t)[0]; got != want {
		t.Fatalf("argv:\n got %q\nwant %q", got, want)
	}
}

// A body with a space is one argument, not two. The fake records each argv
// element whole, so this is the assertion that would catch a split.
func TestABodyWithSpacesIsOneArgument(t *testing.T) {
	testenv.SkipUnlessFull(t)
	c, f := client(t)
	f.Bin(t, "hmail", `echo '{"message":{"id":"01MSG"}}'`)

	if _, err := c.Send(context.Background(), Draft{To: "wM:p1", Body: "two words"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	argv := f.Argv(t)[0]
	if argv[2] != "two words" {
		t.Fatalf("the body arrived as %q", argv[2])
	}
}

// The mailbox's refusal is carried in its own words, with its code.
func TestTheMailboxsRefusalIsCarriedInItsOwnWords(t *testing.T) {
	testenv.SkipUnlessFull(t)
	c, f := client(t)
	f.Bin(t, "hmail", `echo '{"error":{"code":"NOT_FOUND","message":"no pane wM:p9"}}'; exit 3`)

	_, err := c.Send(context.Background(), Draft{To: "wM:p9", Body: "up"})
	var refusal *sibling.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("want a refusal, got %v", err)
	}
	if refusal.Code != "NOT_FOUND" || refusal.Sibling != "hmail" {
		t.Fatalf("got %+v", refusal)
	}
}

// A mailbox that is not installed is a loud failure, never a silent skip.
func TestAMissingMailboxIsLoud(t *testing.T) {
	testenv.SkipUnlessFull(t)
	c, _ := client(t)
	c.Bin = "hmail-that-is-not-installed"
	if _, err := c.Send(context.Background(), Draft{To: "wM:p1", Body: "up"}); err == nil {
		t.Fatal("want an error")
	} else if !strings.Contains(err.Error(), "hmail") {
		t.Fatalf("the failure does not name the sibling: %v", err)
	}
}

// A message with no id is a failure: recording a fired run whose message
// nobody can read is worse than saying the call did not land.
func TestAnAnswerWithNoMessageIsAFailure(t *testing.T) {
	testenv.SkipUnlessFull(t)
	c, f := client(t)
	f.Bin(t, "hmail", `echo '{}'`)
	if _, err := c.Send(context.Background(), Draft{To: "wM:p1", Body: "up"}); err == nil {
		t.Fatal("want an error")
	}
}
