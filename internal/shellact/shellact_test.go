package shellact

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/husniadil/herdr-sched/internal/testenv"
)

// A shell action runs detached from the tick (note 2): Start hands back a
// handle at once, and the daemon keeps ticking while the command runs.
func TestStartDoesNotWaitForTheCommand(t *testing.T) {
	testenv.SkipUnlessFull(t)
	testenv.New(t)
	run, err := Start(context.Background(), Command{Line: "sleep 0.4; echo late"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	select {
	case <-run.Done:
		t.Fatal("Start waited for the command; a slow command would hold up every other schedule")
	case <-time.After(50 * time.Millisecond):
	}
	res := <-run.Done
	if res.Exit != 0 || !strings.Contains(res.Output, "late") {
		t.Fatalf("got %+v", res)
	}
}

// The command's output is captured onto the run, both streams, because the
// operator reading the trail is the only reader it has.
func TestBothStreamsAreCapturedOntoTheRun(t *testing.T) {
	testenv.SkipUnlessFull(t)
	testenv.New(t)
	run, err := Start(context.Background(), Command{Line: "echo out; echo err >&2"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	res := <-run.Done
	if !strings.Contains(res.Output, "out") || !strings.Contains(res.Output, "err") {
		t.Fatalf("output is %q", res.Output)
	}
}

// A command that fails says so loudly, with its status and its output. It is
// never a run that quietly succeeded.
func TestANonZeroExitIsAFailedRun(t *testing.T) {
	testenv.SkipUnlessFull(t)
	testenv.New(t)
	run, err := Start(context.Background(), Command{Line: "echo nope >&2; exit 3"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	res := <-run.Done
	if res.Err == nil {
		t.Fatal("want a failure")
	}
	if res.Exit != 3 || !strings.Contains(res.Err.Error(), "3") {
		t.Fatalf("got exit %d, err %v", res.Exit, res.Err)
	}
	if !strings.Contains(res.Output, "nope") {
		t.Fatalf("the failure dropped the output: %q", res.Output)
	}
}

// A directory that does not exist refuses at Start rather than reporting a
// shell error the operator has to decode.
func TestAMissingDirectoryRefusesAtStart(t *testing.T) {
	testenv.SkipUnlessFull(t)
	testenv.New(t)
	_, err := Start(context.Background(), Command{Line: "pwd", Dir: "/no/such/place"})
	if err == nil || !strings.Contains(err.Error(), "/no/such/place") {
		t.Fatalf("want a refusal naming the directory, got %v", err)
	}
}

// The command runs where the action said to run it.
func TestTheCommandRunsInTheDirectoryItNames(t *testing.T) {
	testenv.SkipUnlessFull(t)
	testenv.New(t)
	dir := t.TempDir()
	run, err := Start(context.Background(), Command{Line: "pwd", Dir: dir})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	res := <-run.Done
	// macOS resolves the temp root through a symlink, so the tail is what
	// can be compared without shelling out to resolve it.
	if !strings.HasSuffix(strings.TrimSpace(res.Output), strings.TrimPrefix(dir, "/private")) {
		t.Fatalf("ran in %q, want %q", strings.TrimSpace(res.Output), dir)
	}
}

// Output is bounded: it lands in an event detail in a document written whole
// on every save, so a command that prints a megabyte does not become a
// megabyte written on every subsequent change.
func TestOutputIsBoundedAndSaysSo(t *testing.T) {
	testenv.SkipUnlessFull(t)
	testenv.New(t)
	run, err := Start(context.Background(), Command{Line: "yes abcdefghij | head -c 100000"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	res := <-run.Done
	if len(res.Output) > MaxOutput+200 {
		t.Fatalf("the run kept %d bytes of output", len(res.Output))
	}
	if !strings.Contains(res.Output, "truncated") {
		t.Fatal("the output was cut without saying so, which reads as a command that printed less than it did")
	}
}

// The command does not inherit the daemon's pane: a shell action that shells
// out to a sibling would otherwise arrive as this daemon rather than as the
// signal that fired it.
func TestTheCommandDoesNotInheritTheDaemonsPane(t *testing.T) {
	testenv.SkipUnlessFull(t)
	testenv.New(t) // sets HERDR_PANE_ID
	run, err := Start(context.Background(), Command{Line: `echo "pane=[$HERDR_PANE_ID]"`})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	res := <-run.Done
	if !strings.Contains(res.Output, "pane=[]") {
		t.Fatalf("the command saw a pane: %q", res.Output)
	}
}
