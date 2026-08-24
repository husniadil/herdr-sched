package action

import (
	"strings"

	"github.com/husniadil/herdr-sched/internal/codes"
)

// The two signal kinds, and there is no third (note 2).
const (
	SourceCron    = "cron"
	SourceTrigger = "trigger"
)

// Source is the signal an action fired from. It is what every sibling call
// declares itself as (§3.2), so the actor on the sibling's own event trail is
// the schedule that caused the call rather than "some plugin": an operator
// reading the board sees `cron:nightly-sweep` filed the task.
type Source struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// Principal is the §3.2 principal string a sibling call passes to --as.
func (s Source) Principal() string { return s.Kind + ":" + s.ID }

// Validate refuses a source that could not attribute a call. A bare `cron:`
// on a sibling's trail is worse than no call at all: it is a fact the
// operator cannot trace back to anything.
func (s Source) Validate() error {
	if s.Kind != SourceCron && s.Kind != SourceTrigger {
		return codes.Errorf(codes.Usage,
			"a signal is a %s or a %s, and this one is %q", SourceCron, SourceTrigger, s.Kind)
	}
	if strings.TrimSpace(s.ID) == "" {
		return codes.Errorf(codes.Usage,
			"a %s has no id, so nothing on a sibling's trail could name what fired the call", s.Kind)
	}
	return nil
}
