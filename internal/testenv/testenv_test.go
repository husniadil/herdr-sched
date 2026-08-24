package testenv

import (
	"os/exec"
	"strings"
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
