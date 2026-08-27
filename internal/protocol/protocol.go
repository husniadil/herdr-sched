// Package protocol is the wire between the doors and the daemon: one JSON
// document per line over the Unix socket at <state dir>/sched.sock (§2.2).
package protocol

import "encoding/json"

// Request is one verb call. The principal is derived by the door from its own
// environment (§3.2) and carried here as a pane id or an explicit --as: the
// boundary is the local user account, and whoever can open the socket is
// trusted as the user (§3.5).
type Request struct {
	Verb        string `json:"verb"`
	Project     string `json:"project,omitempty"`
	AllProjects bool   `json:"all_projects,omitempty"`
	// Pane is the Herdr pane the door runs in, recorded by the daemon and
	// granting nothing (§3.4). A caller on another harness has none.
	Pane string `json:"pane,omitempty"`
	// Door names the surface the call came in on, for the daemon's log.
	Door string `json:"door,omitempty"`
	// As is the §3.2 escape hatch for cron, trigger and plugin principals. It
	// is refused for agent and human, which are derived.
	As string `json:"as,omitempty"`
	// Operator is the door saying that the PROCESS it runs in was started by
	// a deliberate human act: a CLI invocation, whose argv is that act, or a
	// server door started with `hsched mcp --operator` (§7.5). It is read once
	// from the door's own startup and never from a call, which is what keeps
	// it from being `--as human` with a different spelling. The pane is
	// resolved first, so a declared door inside a pane is still that pane's
	// agent (§3.2). Without it a paneless caller is `none` (§3.7).
	Operator bool `json:"operator,omitempty"`
	// Follow turns `events` into a subscription (§8.2).
	Follow bool           `json:"follow,omitempty"`
	Args   map[string]any `json:"args,omitempty"`
}

// Caller is the principal the daemon records for a call: what the door
// declared with --as, else the pane it runs in, else the operator when the
// PROCESS the door runs in was started by a deliberate human act, else
// nothing at all. It is never more than the daemon knows, and it grants
// nothing (§3.4).
//
// The pane is read before the declaration on purpose (§7.5): a door started
// inside a pane is that pane's agent whatever it was declared, so declaring
// one gains an agent nothing.
//
// §3.7: `human` is never the fallback for knowing nothing, and absence of
// evidence is not evidence of the highest-authority principal in the system.
// A paneless caller with neither an --as nor a human act behind its process
// has NO principal, and the literal `none` is that said out loud — written
// into the trail verbatim, so a row a door nobody declared created says so
// rather than being filed under the operator.
func (r Request) Caller() string {
	if r.As != "" {
		return r.As
	}
	if r.Pane != "" {
		return "agent:" + r.Pane
	}
	if r.Operator {
		return "human"
	}
	return "none"
}

// Response is what comes back: exactly one of Result or Error (§6.2).
type Response struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Failure        `json:"error,omitempty"`
	// Done ends a stream on purpose. Without it a daemon that finished and a
	// daemon that was killed both look like a closed socket, and a follower
	// cannot tell "there is no more" from "I stopped being told".
	Done bool `json:"done,omitempty"`
}

// Failure is the §6.2 error envelope. ParkedID is the §9.3 addition a DENIED
// answer carries when the gate deferred rather than refused.
type Failure struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	ParkedID string `json:"parked_id,omitempty"`
}
