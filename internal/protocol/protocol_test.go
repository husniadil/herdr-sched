package protocol

import (
	"encoding/json"
	"testing"
)

// §3.2: the principal is what the door declared with --as, else the pane it
// runs in, else the operator when the process the door runs in was started by
// a deliberate human act, else nothing at all. It is never more than the
// daemon knows, and §3.7 spells "nothing at all" the literal `none` rather
// than filing it under the operator.
func TestTheCallerIsDerivedAndNeverMoreThanTheDaemonKnows(t *testing.T) {
	cases := []struct {
		req  Request
		want string
	}{
		// §3.7: a paneless door nobody declared has no principal, and says so.
		{Request{}, "none"},
		{Request{Pane: "wA:p1"}, "agent:wA:p1"},
		{Request{As: "cron:nightly"}, "cron:nightly"},
		// An explicit principal wins over the pane, which is what makes a
		// scheduled call name the schedule rather than the daemon's pane.
		{Request{As: "trigger:01H", Pane: "wA:p1"}, "trigger:01H"},
		// §3.6 and §7.5: a process started by a deliberate human act — a CLI
		// invocation, whose argv IS that act, or a door started with the
		// declaration — is the operator. The pane is read FIRST, so an agent
		// that starts a declared door gains nothing by it, because its calls
		// are still its pane's.
		{Request{Operator: true}, "human"},
		{Request{Operator: true, Pane: "wA:p1"}, "agent:wA:p1"},
		{Request{Operator: true, As: "cron:nightly"}, "cron:nightly"},
		// And --as still wins over everything, declared door or not.
		{Request{Operator: true, Pane: "wA:p1", As: "trigger:01H"}, "trigger:01H"},
	}
	for _, tc := range cases {
		if got := tc.req.Caller(); got != tc.want {
			t.Errorf("Caller(%+v) = %q, want %q", tc.req, got, tc.want)
		}
	}
}

// §6.2: exactly one of Result or Error, and an empty scope or principal is
// absent from the wire rather than sent as an empty string.
func TestTheWireCarriesOnlyWhatWasSet(t *testing.T) {
	raw, err := json.Marshal(Request{Verb: "doctor"})
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{"project", "all_projects", "pane", "door", "as", "operator", "follow", "args"} {
		if _, ok := wire[absent]; ok {
			t.Errorf("an unset %q was sent: %s", absent, raw)
		}
	}
	if wire["verb"] != "doctor" {
		t.Fatalf("request = %s", raw)
	}

	// A stream's ending is said on purpose: without it a daemon that finished
	// and one that was killed both look like a closed socket.
	raw, err = json.Marshal(Response{Done: true})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"done":true}` {
		t.Fatalf("the end of a stream is %s", raw)
	}
}

// §9.3: a DENIED the gate deferred names the row the operator resolves, and
// every other failure leaves the field off the wire.
func TestOnlyADeferredDenialCarriesAParkedRow(t *testing.T) {
	raw, err := json.Marshal(Response{Error: &Failure{Code: "CONFLICT", Message: "NOT_RUNNING: nothing is listening"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); got != `{"error":{"code":"CONFLICT","message":"NOT_RUNNING: nothing is listening"}}` {
		t.Fatalf("failure = %s", got)
	}
}
