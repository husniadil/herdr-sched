package gate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func script(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gate")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// §9.2: an unconfigured gate allows. It is the default, and it is the one
// state doctor has to report, because at the call site it looks exactly like a
// configured gate that allows.
func TestAnUnconfiguredGateAllows(t *testing.T) {
	g := New(nil)
	if g.Configured() {
		t.Fatal("a nil command reported itself as configured")
	}
	if res := g.Check(Request{Verb: "sched.stop"}); res.Decision != Allow {
		t.Fatalf("decision = %q, want allow", res.Decision)
	}
}

// The gate reads §9.2's request on stdin, so a policy can decide on the
// subject, the verb and the target rather than on the verb alone.
func TestTheGateIsHandedTheSubjectVerbAndTarget(t *testing.T) {
	seen := filepath.Join(t.TempDir(), "seen.json")
	path := script(t, "cat > "+seen+"\nprintf '{\"decision\":\"allow\"}\\n'")
	res := New([]string{path}).Check(Request{Subject: "cron:nightly", Verb: "sched.stop", Target: "pk-1"})
	if res.Decision != Allow {
		t.Fatalf("decision = %q", res.Decision)
	}
	raw, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	var got Request
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("the gate was handed %q: %v", raw, err)
	}
	if got.Subject != "cron:nightly" || got.Verb != "sched.stop" || got.Target != "pk-1" {
		t.Fatalf("the gate was handed %+v", got)
	}
}

// The gate FAILS CLOSED: anything that is not a well-formed answer is a deny,
// and the reason says which failure it was.
func TestTheGateFailsClosed(t *testing.T) {
	cases := map[string]string{
		"a non-zero exit":              "exit 3",
		"no output at all":             "exit 0",
		"output that is not the shape": "printf 'yes\\n'",
		"an unknown decision":          "printf '{\"decision\":\"maybe\"}\\n'",
	}
	for what, body := range cases {
		res := New([]string{script(t, body)}).Check(Request{Verb: "sched.stop"})
		if res.Decision != Deny {
			t.Errorf("%s: decision = %q, want deny", what, res.Decision)
		}
		if res.Reason == "" {
			t.Errorf("%s: denied with no reason", what)
		}
	}
	// A command that is not there at all is the same answer.
	res := New([]string{filepath.Join(t.TempDir(), "not-a-gate")}).Check(Request{Verb: "sched.stop"})
	if res.Decision != Deny {
		t.Fatalf("a missing gate command decided %q", res.Decision)
	}
}

// A gate that does not answer has not allowed anything, and the wait is
// bounded so a hung policy cannot hold a verb open forever.
func TestAGateThatDoesNotAnswerInTimeDenies(t *testing.T) {
	g := &Gate{command: []string{script(t, "sleep 30")}, Timeout: 200 * time.Millisecond}
	start := time.Now()
	res := g.Check(Request{Verb: "sched.stop"})
	if res.Decision != Deny {
		t.Fatalf("decision = %q, want deny", res.Decision)
	}
	if !strings.Contains(res.Reason, "did not answer") {
		t.Errorf("reason = %q", res.Reason)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("the check took %s; the timeout is not bounding it", elapsed)
	}
}

// An answer larger than the limit is not an answer, and §9.2 calls oversized a
// failure — which is a deny.
func TestAnOversizedAnswerDenies(t *testing.T) {
	path := script(t, "head -c 200000 /dev/zero | tr '\\0' 'x'")
	res := New([]string{path}).Check(Request{Verb: "sched.stop"})
	if res.Decision != Deny {
		t.Fatalf("decision = %q, want deny", res.Decision)
	}
}

// The three decisions §9.1 names all reach the caller.
func TestEveryDecisionTheContractNamesIsRead(t *testing.T) {
	for _, want := range []Decision{Allow, Deny, Defer} {
		path := script(t, "printf '{\"decision\":\""+string(want)+"\",\"reason\":\"because\"}\\n'")
		res := New([]string{path}).Check(Request{Verb: "sched.stop"})
		if res.Decision != want {
			t.Errorf("decision = %q, want %q", res.Decision, want)
		}
		if res.Reason != "because" {
			t.Errorf("reason = %q", res.Reason)
		}
	}
}
