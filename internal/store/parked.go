package store

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// Parked is a verb the policy gate deferred: recorded, not performed, waiting
// for the operator (§9.3).
//
// It is the one thing this daemon holds that is derivable from nothing else: a
// call refused before it did anything left no trace anywhere.
type Parked struct {
	ID      string `json:"id"`
	Subject string `json:"subject"`
	Verb    string `json:"verb"`
	Target  string `json:"target,omitempty"`
	// Payload is the call's arguments as the door sent them, so resolving
	// re-runs the verb the caller actually asked for rather than one
	// reconstructed from its target.
	Payload map[string]any `json:"payload,omitempty"`
	// Project and AllProjects are the scope the call was made with. They are
	// not arguments, so the payload cannot carry them, and a re-run that lost
	// the scope would act somewhere the caller never named.
	Project     string `json:"project,omitempty"`
	AllProjects bool   `json:"all_projects,omitempty"`
	// State is "parked", "resolved", "refused" or "failed".
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
	// Error is why the verb failed when the operator resolved it. A parked
	// action that was decided and did not happen is not a resolved one, and
	// the operator needs to see which.
	Error string `json:"error,omitempty"`
	// ResolvedBy is who ran or rejected this action. §9.3 re-runs the verb
	// under the ORIGINAL subject, so without this the record names only the
	// caller the gate stopped and no one who decided it could proceed.
	ResolvedBy string `json:"resolved_by,omitempty"`
	AtMS       int64  `json:"at_ms"`
	ResolvedMS int64  `json:"resolved_ms,omitempty"`
}

// The four states a parked action can be in.
const (
	// ParkedWaiting is the gate's deferral, undecided.
	ParkedWaiting = "parked"
	// ParkedResolved is the operator letting the verb through, and it ran.
	ParkedResolved = "resolved"
	// ParkedRefused is the operator closing it without running the verb.
	ParkedRefused = "refused"
	// ParkedFailed is the operator letting it through and the verb erroring.
	// It stays decided rather than going back to waiting: an action that
	// errored is not proof it had no effect, so the operator reads why and
	// decides again, deliberately.
	ParkedFailed = "failed"
)

// NewParkedID is a sortable id for a parked action: the millisecond it was
// parked, then eight random hex digits so two in the same millisecond are
// still two. It is a name for one row in one operator's own file, which is all
// §9.3 asks a parked_id to be.
func NewParkedID(now time.Time) string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// A machine with no entropy still has a clock, and a duplicate id is
		// a worse answer than a less random one but a far better answer than
		// refusing to record the deferral at all.
		return fmt.Sprintf("pk-%013d-00000000", now.UnixMilli())
	}
	return fmt.Sprintf("pk-%013d-%s", now.UnixMilli(), hex.EncodeToString(b[:]))
}

// Waiting reports whether this action still wants the operator's attention. A
// failed action does: hiding it would make a verb that did not happen look
// like one that did.
func (p Parked) Waiting() bool { return p.State == ParkedWaiting || p.State == ParkedFailed }
