package mcpdoor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/husniadil/herdr-sched/internal/codes"
	"github.com/husniadil/herdr-sched/internal/protocol"
	"github.com/husniadil/herdr-sched/internal/store"
	"github.com/husniadil/herdr-sched/internal/verbs"
)

// §7.5, on the trail that reads it: a door started with the declaration writes
// `human` onto every event it causes, and one nobody declared writes what the
// daemon actually knows. The gate subject, the parked row and every actor in
// this store come off the same Caller, so one verb through two doors is the
// whole of it.
func TestTheDeclaredDoorIsTheOperatorAndTheUndeclaredOneIsNot(t *testing.T) {
	for name, tc := range map[string]struct {
		opt  Options
		want string
	}{
		// §3.7: a paneless door nobody declared has no principal, and the
		// trail says `none` rather than filing the row under the operator.
		"a door nobody declared":          {Options{}, "none"},
		"a door declared with --operator": {Options{Operator: true}, "human"},
	} {
		d, call := inProcessDaemon(t)
		// AFTER inProcessDaemon, which sets a pane at the one seam every test
		// goes through: what §7.5 answers is the door standing in no pane.
		t.Setenv("HERDR_PANE_ID", "")
		sess := sessionWith(t, call, tc.opt)

		res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "job_add", Arguments: map[string]any{
				"id": "nightly", "schedule": "0 3 * * *", "action": "task",
				"args": map[string]any{"title": "sweep the board"}}})
		if err != nil {
			t.Fatalf("%s: CallTool: %v", name, err)
		}
		if res.IsError {
			t.Fatalf("%s: job_add: %s", name, text(res))
		}

		trail, err := d.Store.Events(store.EventFilter{})
		if err != nil {
			t.Fatalf("%s: events: %v", name, err)
		}
		if len(trail) == 0 {
			t.Fatalf("%s: the job was written and no event records it", name)
		}
		for _, ev := range trail {
			if ev.Actor != tc.want {
				t.Errorf("%s: the %s event names %q, want %q", name, ev.Kind, ev.Actor, tc.want)
			}
		}
	}
}

// The other end of the same fact, held on the request rather than the trail:
// what the door sends is what it was STARTED with, on every call.
func TestTheDeclarationTravelsOnEveryRequestTheDeclaredDoorSends(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "")
	var seen []protocol.Request
	var mu sync.Mutex
	spy := func(req protocol.Request) (json.RawMessage, error) {
		mu.Lock()
		seen = append(seen, req)
		mu.Unlock()
		return json.RawMessage(`{}`), nil
	}
	sess := sessionWith(t, spy, Options{Operator: true})
	for _, tool := range []string{"doctor", "parked_list", "job_list"} {
		if _, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: tool}); err != nil {
			t.Fatalf("%s: %v", tool, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 3 {
		t.Fatalf("%d requests reached the daemon, want 3", len(seen))
	}
	for _, req := range seen {
		if !req.Operator {
			t.Errorf("%s was sent without the declaration the door was started with", req.Verb)
		}
		if got := req.Caller(); got != "human" {
			t.Errorf("%s: caller = %q, want human", req.Verb, got)
		}
	}
}

// §7.5's first property: the declaration is read once, from the server
// command, and MUST NOT arrive as a tool argument. Three things have to hold
// at once, so all three are here — no schema offers it, a call that tries to
// carry it is refused with USAGE, and it does not reach the daemon either.
func TestTheOperatorDeclarationNeverArrivesPerCall(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "")
	for _, v := range verbs.MCPTools() {
		props, _ := tool(v).InputSchema.(map[string]any)["properties"].(map[string]any)
		for _, name := range []string{argOperator, "as", "principal", "human"} {
			if _, ok := props[name]; ok {
				t.Errorf("tool %q offers %q as an argument; §7.5 forbids the declaration "+
					"reaching a door through a call", v.MCP, name)
			}
		}
	}

	var seen []protocol.Request
	var mu sync.Mutex
	spy := func(req protocol.Request) (json.RawMessage, error) {
		mu.Lock()
		seen = append(seen, req)
		mu.Unlock()
		return json.RawMessage(`{}`), nil
	}
	sess := session(t, spy)

	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "doctor", Arguments: map[string]any{argOperator: true}})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatalf("the door accepted a call carrying the declaration: %s", text(res))
	}
	if got := text(res); !strings.Contains(got, string(codes.Usage)) || !strings.Contains(got, argOperator) {
		t.Fatalf("refused with %s, want USAGE naming %s", got, argOperator)
	}
	mu.Lock()
	reached := len(seen)
	mu.Unlock()
	if reached != 0 {
		t.Fatalf("%d requests reached the daemon; the refusal happens at the door", reached)
	}

	// And the same call without the rejected argument goes through an
	// undeclared door declaring nothing.
	if _, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "doctor"}); err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 {
		t.Fatalf("%d requests reached the daemon, want 1", len(seen))
	}
	if seen[0].Operator {
		t.Fatal("an undeclared door told the daemon it speaks for the operator")
	}
}

// §7.5's fourth property, the loud half: a door given --operator inside a
// Herdr pane refuses to START, with FORBIDDEN. It is defence in depth rather
// than the thing that stops the escalation — the test below holds that — and
// it earns its place by failing loudly once instead of running an ambiguous
// door all day.
func TestServeRefusesADeclaredDoorInsideAPane(t *testing.T) {
	// Cancelled before it starts, so the cases that ARE allowed to serve
	// return from the transport promptly instead of reading stdin. What is
	// under test is which of the two answers comes back.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	silent := func(protocol.Request) (json.RawMessage, error) { return json.RawMessage(`{}`), nil }

	for name, tc := range map[string]struct {
		pane    string
		opt     Options
		refused bool
	}{
		"declared inside a pane":   {"wT:p1", Options{Operator: true}, true},
		"declared in no pane":      {"", Options{Operator: true}, false},
		"undeclared inside a pane": {"wT:p1", Options{}, false},
		"undeclared and paneless":  {"", Options{}, false},
	} {
		t.Setenv("HERDR_PANE_ID", tc.pane)
		done := make(chan error, 1)
		opt := tc.opt
		go func() { done <- Serve(ctx, "0.1.0", silent, opt) }()
		var err error
		select {
		case err = <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: Serve did not return", name)
		}
		var ce *codes.Error
		refused := errors.As(err, &ce) && ce.Code == codes.Forbidden
		if refused != tc.refused {
			t.Errorf("%s: refused = %v, want %v (err = %v)", name, refused, tc.refused, err)
			continue
		}
		if tc.refused && !strings.Contains(err.Error(), tc.pane) {
			t.Errorf("%s: the refusal does not name the pane: %v", name, err)
		}
	}
}

// And the property that startup check is only the loud half of: a declared
// door that somehow IS inside a pane is still that pane's agent, never the
// operator. This is what actually prevents the escalation — Caller reads the
// pane before it reads the declaration — so it is pinned here rather than left
// resting on a check a caller can avoid by starting the door another way.
func TestAnInPaneDeclaredDoorIsStillThePanesAgent(t *testing.T) {
	var seen []protocol.Request
	var mu sync.Mutex
	spy := func(req protocol.Request) (json.RawMessage, error) {
		mu.Lock()
		seen = append(seen, req)
		mu.Unlock()
		return json.RawMessage(`{}`), nil
	}
	t.Setenv("HERDR_PANE_ID", "wT:p9")
	sess := sessionWith(t, spy, Options{Operator: true})
	if _, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "doctor"}); err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 {
		t.Fatalf("%d requests reached the daemon, want 1", len(seen))
	}
	if got := seen[0].Caller(); got != "agent:wT:p9" {
		t.Fatalf("caller = %q, want agent:wT:p9: the declaration overruled the pane", got)
	}
	// And the door really did send the declaration, so what loses to the pane
	// is the ordering in Caller and not the door quietly dropping the flag.
	if !seen[0].Operator {
		t.Fatal("the door dropped the declaration; this test would then pass for the wrong reason")
	}
}
