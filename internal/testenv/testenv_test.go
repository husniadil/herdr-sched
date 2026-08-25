package testenv

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/husniadil/herdr-sched/internal/config"
)

// PATH is replaced rather than prepended to, so a call whose fake was never
// written fails as "not found" instead of quietly reaching the operator's own
// board, mailbox, dispatcher or Herdr server.
func TestASiblingWithNoFakeIsNotFoundRatherThanReal(t *testing.T) {
	f := New(t)
	f.Bin(t, "htask", `echo '{"tasks":[]}'`)

	if _, err := exec.LookPath("htask"); err != nil {
		t.Fatalf("the fake htask is not resolvable: %v", err)
	}
	for _, name := range Siblings {
		if name == "htask" {
			continue
		}
		if path, err := exec.LookPath(name); err == nil {
			t.Errorf("%s resolved to %s with no fake written for it", name, path)
		}
	}
}

// A test's state and config never reach the operator's own.
func TestTheThrowawayWorldOwnsItsStateAndConfig(t *testing.T) {
	New(t)
	for what, dir := range map[string]string{"state": config.StateDir(), "config": config.ConfigDir()} {
		if dir == "" || strings.HasPrefix(dir, "/Users/") && strings.Contains(dir, "/.local/state/sched") {
			t.Errorf("the %s dir is the operator's own: %s", what, dir)
		}
	}
}

// The call log records each argument whole, so an assertion about an argument
// containing a space is possible at all.
func TestTheCallLogKeepsEachArgumentWhole(t *testing.T) {
	f := New(t)
	f.Bin(t, "htask", `exit 0`)
	if err := exec.Command("htask", "note", "add", "two words").Run(); err != nil {
		t.Fatalf("run the fake: %v", err)
	}
	argv := f.Argv(t)
	if len(argv) != 1 {
		t.Fatalf("calls = %v", argv)
	}
	// "$@" is the argv the fake was called with, without the binary's own name.
	if len(argv[0]) != 3 || argv[0][2] != "two words" {
		t.Fatalf("argv = %q", argv[0])
	}
	if got := f.Calls(t)[0]; got != "note add two words" {
		t.Fatalf("call = %q", got)
	}
}

// The gate is a real command answering on stdout, which is what §9.2 makes it.
func TestTheGateFakeAnswersADecision(t *testing.T) {
	f := New(t)
	f.Gate(t, "defer", "an operator decides this one")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.GateCommand) != 1 {
		t.Fatalf("gate command = %v", cfg.GateCommand)
	}
	out, err := exec.Command(cfg.GateCommand[0]).Output()
	if err != nil {
		t.Fatalf("run the gate: %v", err)
	}
	if !strings.Contains(string(out), `"decision":"defer"`) {
		t.Fatalf("the gate answered %q", out)
	}
}

// The stand-in board refuses the grouped spelling of a task verb. htask moved
// those verbs to the top level of its CLI and kept the old forms only as
// hidden transition aliases; a fake that answered them would let this
// plugin's adapter outlive the aliases without anyone noticing.
func TestTheFakeBoardRefusesTheGroupedTaskVerbs(t *testing.T) {
	f := New(t)
	f.HTask(t, `echo '{"task":{"id":"01AAA"}}'`)

	out, err := exec.Command("htask", "task", "create", "sweep").Output()
	if err == nil {
		t.Fatalf("the grouped form was answered: %s", out)
	}
	if !strings.Contains(string(out), "USAGE") || !strings.Contains(string(out), "top level") {
		t.Fatalf("the refusal does not say why: %s", out)
	}
}

// The top-level spelling reaches the canned answer, and so does the note
// group, which stays a group spelled with a space.
func TestTheFakeBoardAnswersTheTopLevelAndNoteForms(t *testing.T) {
	f := New(t)
	f.HTask(t, `echo '{"task":{"id":"01AAA"}}'`)

	for _, argv := range [][]string{{"create", "sweep"}, {"note", "add", "an idea"}} {
		out, err := exec.Command("htask", argv...).Output()
		if err != nil {
			t.Fatalf("htask %v: %v", argv, err)
		}
		if !strings.Contains(string(out), "01AAA") {
			t.Fatalf("htask %v did not reach the script: %s", argv, out)
		}
	}
}

// The refusal is keyed on the binary's name, not on which helper wrote it: a
// case that reaches for Bin directly gets the same guarded board. Without
// this, one unguarded fake anywhere is enough for the adapter to keep a
// spelling htask no longer teaches, and nothing goes red.
func TestAnyFakeNamedHTaskRefusesTheGroupedTaskVerbs(t *testing.T) {
	f := New(t)
	f.Bin(t, "htask", `echo '{"task":{"id":"01AAA"}}'`)

	out, err := exec.Command("htask", "task", "create", "sweep").Output()
	if err == nil {
		t.Fatalf("the grouped form was answered: %s", out)
	}
	if !strings.Contains(string(out), "top level") {
		t.Fatalf("the refusal does not say why: %s", out)
	}
	if out, err := exec.Command("htask", "create", "sweep").Output(); err != nil || !strings.Contains(string(out), "01AAA") {
		t.Fatalf("the top-level form did not reach the script: %s %v", out, err)
	}
}

// Every call to a fake lands as its own line, even when the callers overlap.
//
// The log is appended to by concurrent processes, so a call written as more
// than one append can interleave with another call's: two argv runs land
// before either newline does, the reader sees ONE line, and an assertion on
// how many times a sibling was reached quietly counts low.
func TestConcurrentCallsToAFakeEachLandAsTheirOwnLine(t *testing.T) {
	SkipUnlessFull(t)
	f := New(t)
	f.Bin(t, "htask", "exit 0")
	const callers = 24
	var start, done sync.WaitGroup
	start.Add(1)
	for i := 0; i < callers; i++ {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			start.Wait()
			cmd := exec.Command("htask", "create", fmt.Sprintf("row-%02d", i), "--json")
			cmd.Env = os.Environ()
			if err := cmd.Run(); err != nil {
				t.Errorf("call %d: %v", i, err)
			}
		}(i)
	}
	start.Done()
	done.Wait()

	calls := f.Calls(t)
	if len(calls) != callers {
		t.Fatalf("%d overlapping calls were logged as %d lines", callers, len(calls))
	}
	seen := map[string]bool{}
	for _, got := range calls {
		seen[got] = true
	}
	for i := 0; i < callers; i++ {
		want := fmt.Sprintf("create row-%02d --json", i)
		if !seen[want] {
			t.Errorf("the log lost the call %q", want)
		}
	}
}
