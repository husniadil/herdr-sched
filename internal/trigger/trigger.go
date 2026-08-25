// Package trigger is the trigger half: a trigger row, and the pure decisions
// that say whether one may fire.
//
// Nothing here reads a clock, opens a socket, stats a file or touches the
// store. Allow is handed the time and the row and answers whether the firing
// happens; Changed is handed what the watcher saw and what the row last
// recorded and answers whether that is a change. Both are what make the
// cooldown, the hourly limit and the watcher's edge testable without a daemon,
// a port or a time.Sleep (§12.1).
package trigger

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/husniadil/herdr-sched/internal/action"
	"github.com/husniadil/herdr-sched/internal/codes"
)

// The two kinds of trigger, and there is no third (note 2): an inbound webhook
// on a server-issued URL, or a file watcher on a host path.
const (
	KindWebhook = "webhook"
	KindWatch   = "watch"
)

// Kinds is the whole set, in the order it is documented.
var Kinds = []string{KindWebhook, KindWatch}

// Window is the span the hourly limit counts over. It is a constant rather
// than a knob: `max_per_hour` names the hour, and a limit whose window an
// operator can move is a limit two rows can disagree about.
const Window = time.Hour

// Trigger is one inbound signal: what arrives, what it fires, and the two
// limits that hold it down.
type Trigger struct {
	// ID is the operator's own name for the trigger, the id half of the
	// `trigger:<id>` principal every call it makes declares (§3.1), and the
	// last segment of the URL a webhook is reached on. It carries no colon,
	// no space and no slash for exactly those reasons.
	ID string `json:"id"`
	// Kind is webhook or watch.
	Kind string `json:"kind"`
	// Action is what this trigger DOES, as data rather than as code.
	Action action.Action `json:"action"`
	// Path is the host path a watch polls. It is empty for a webhook, and a
	// webhook that carries one is refused rather than ignored.
	Path string `json:"path,omitempty"`
	// CooldownSeconds is how long after a firing this trigger refuses the
	// next one. Zero is no cooldown. It is what makes a replayed webhook
	// request a refusal rather than a second firing.
	CooldownSeconds int64 `json:"cooldown_seconds,omitempty"`
	// MaxPerHour is how many firings this trigger allows in any hour. Zero is
	// no limit.
	MaxPerHour int `json:"max_per_hour,omitempty"`
	// Enabled is false for a trigger an operator has turned off. It is kept
	// rather than removed: disabling is not deleting, and the row's history
	// stays readable.
	Enabled bool `json:"enabled"`
	// Project is the scope the trigger was written in (§4.2).
	Project   string `json:"project,omitempty"`
	CreatedMS int64  `json:"created_at"`
	// LastFiredMS is when this trigger last fired, and what the cooldown is
	// measured from.
	LastFiredMS int64 `json:"last_fired,omitempty"`
	// FiredMS is every firing inside the last Window, oldest first, and it is
	// what the hourly limit counts. Everything older is dropped as it is
	// written, so the row does not grow without bound and the count is a read
	// of the slice rather than a walk of the whole history.
	FiredMS []int64 `json:"fired_at,omitempty"`
	// Stamp is what the watcher last saw at Path. It is meaningless for a
	// webhook and is not written for one.
	Stamp Stamp `json:"stamp,omitempty"`
}

// Stamp is one look at a watched path. It is the whole of what the watcher
// remembers between ticks: a change is this differing from the last one.
type Stamp struct {
	// Seen says the watcher has looked at all. The first look RECORDS and
	// does not fire — a trigger written against a file that already exists
	// would otherwise fire on the tick after it was written, for a change
	// that happened before anyone asked to watch.
	Seen bool `json:"seen,omitempty"`
	// Present says the path existed at that look. A file deleted and written
	// again is a change, and without this the two zeroes of an absent file
	// read as "never looked".
	Present bool `json:"present,omitempty"`
	// ModNS is the mtime in NANOSECONDS. Milliseconds threw away resolution
	// the filesystem has: two writes microseconds apart share a millisecond,
	// and a same-size rewrite inside one was invisible. That change was lost
	// rather than delayed, because the stamp already equalled the new state.
	ModNS int64 `json:"modns,omitempty"`
	Size  int64 `json:"size,omitempty"`
}

// Validate refuses everything that could not fire, at the moment the row is
// written. A trigger that fails on the request it was written for fails in a
// log nobody reads.
func (t Trigger) Validate() error {
	if err := ValidateID(t.ID); err != nil {
		return err
	}
	switch t.Kind {
	case KindWebhook:
		if strings.TrimSpace(t.Path) != "" {
			return codes.Errorf(codes.Usage,
				"a webhook trigger watches no path, and this one names %q: it is reached on a URL", t.Path)
		}
	case KindWatch:
		if strings.TrimSpace(t.Path) == "" {
			return codes.Errorf(codes.Usage, "a watch trigger has no path, and there is nothing to watch")
		}
		// A relative path is relative to the CALLER's working directory, and
		// the daemon that stats it is somewhere else entirely — so a watcher
		// written as `inbox` would silently watch a different file from the
		// one the operator meant, or none at all, and look exactly like a
		// watcher on a file that never changes. §4.1 resolves a project in the
		// door for the same reason; a path has no such resolution, so it is
		// refused instead.
		if !filepath.IsAbs(t.Path) {
			return codes.Errorf(codes.Usage,
				"a watch path is absolute, and %q is not: the daemon that stats it does not share your working directory", t.Path)
		}
	default:
		return codes.Errorf(codes.Usage,
			"%s: no trigger does that; a trigger is one of %s", describeKind(t.Kind), strings.Join(Kinds, ", "))
	}
	if t.CooldownSeconds < 0 {
		return codes.Errorf(codes.Usage, "a cooldown is a number of seconds and never negative")
	}
	if t.MaxPerHour < 0 {
		return codes.Errorf(codes.Usage, "max_per_hour is a count of firings and never negative")
	}
	return t.Action.Validate()
}

// ValidateID holds a trigger id to what a §3.1 principal and a URL segment can
// both carry. It is separate from Validate because a verb that names a trigger
// — remove, enable, disable — checks the name without a row to check it
// against.
func ValidateID(id string) error {
	if strings.TrimSpace(id) == "" {
		return codes.Errorf(codes.Usage,
			"a trigger has no id, and `trigger:` alone attributes nothing on a sibling's trail")
	}
	if strings.ContainsAny(id, ": \t\n") {
		return codes.Errorf(codes.Usage,
			"a trigger id carries no colon and no space, and %q does: it is the id half of `trigger:<id>` (§3.1)", id)
	}
	if strings.ContainsAny(id, "/?#%") {
		return codes.Errorf(codes.Usage,
			"a trigger id is the last segment of the URL it is reached on, and %q carries a character that would not survive one", id)
	}
	return nil
}

// Source is the §3.2 principal this trigger's calls declare, so the actor on a
// sibling's own trail is the trigger that caused the call.
func (t Trigger) Source() action.Source {
	return action.Source{Kind: action.SourceTrigger, ID: t.ID}
}

// The reasons a firing is refused. They are on the trail as the detail of a
// `sched.run.limited` event, so an operator reads WHICH limit held it down.
const (
	// LimitDisabled is a trigger an operator turned off.
	LimitDisabled = "disabled"
	// LimitCooldown is a firing inside the span since the last one, which is
	// what a replayed webhook request meets.
	LimitCooldown = "cooldown"
	// LimitRate is the hourly limit already spent.
	LimitRate = "rate"
)

// Verdict is what should happen to one inbound signal. A refusal carries the
// rule that refused it and the words the trail records, because a rate limit
// that refuses silently is a trigger that looks broken.
type Verdict struct {
	Fire bool
	// Limit is the rule that refused, empty when the answer is fire.
	Limit string
	// Reason is what the trail and the caller are told.
	Reason string
}

// Allow is the whole of the rate limit: given the clock and the row, may this
// trigger fire now. It is asked AFTER whatever proved the signal genuine — a
// verified HMAC, a file that really changed — because a refusal here is a real
// signal held down rather than a forgery dropped.
func Allow(now time.Time, t Trigger) Verdict {
	if !t.Enabled {
		return Verdict{Limit: LimitDisabled, Reason: "the trigger " + t.ID + " is disabled"}
	}
	nowMS := now.UnixMilli()
	if t.CooldownSeconds > 0 && t.LastFiredMS > 0 {
		since := nowMS - t.LastFiredMS
		if wait := t.CooldownSeconds*1000 - since; wait > 0 {
			return Verdict{Limit: LimitCooldown, Reason: fmt.Sprintf(
				"the trigger %s fired %s ago and its cooldown is %s: %s of it is left",
				t.ID, span(since), span(t.CooldownSeconds*1000), span(wait))}
		}
	}
	if t.MaxPerHour > 0 {
		spent := len(Recent(t.FiredMS, now))
		if spent >= t.MaxPerHour {
			return Verdict{Limit: LimitRate, Reason: fmt.Sprintf(
				"the trigger %s has fired %d time(s) in the last hour and allows %d",
				t.ID, spent, t.MaxPerHour)}
		}
	}
	return Verdict{Fire: true}
}

// Recent is the firings inside the last Window, oldest first. Everything older
// is what the row drops rather than keeps.
func Recent(fired []int64, now time.Time) []int64 {
	cut := now.Add(-Window).UnixMilli()
	out := []int64{}
	for _, at := range fired {
		if at > cut {
			out = append(out, at)
		}
	}
	return out
}

// Fired is the row as it stands after one firing: the cursor moved and the
// hour's count kept, with everything older than the window dropped in the same
// step so the slice never grows past what the limit reads.
func Fired(now time.Time, t Trigger) Trigger {
	nowMS := now.UnixMilli()
	t.LastFiredMS = nowMS
	t.FiredMS = append(Recent(t.FiredMS, now), nowMS)
	return t
}

// Changed says whether what the watcher sees now differs from what the row
// last recorded, and what the row should record either way.
//
// The first look is never a firing: a trigger written against a file that
// already exists would otherwise fire on the very next tick, for a change that
// happened before anyone asked to watch it.
func Changed(was, now Stamp) (Stamp, bool) {
	now.Seen = true
	if !was.Seen {
		return now, false
	}
	return now, was.Present != now.Present || was.ModNS != now.ModNS || was.Size != now.Size
}

func describeKind(kind string) string {
	if strings.TrimSpace(kind) == "" {
		return "a trigger with no kind"
	}
	return fmt.Sprintf("%q", kind)
}

// span renders a duration in milliseconds the way an operator reads it in a
// refusal, rather than as a bare number nobody can scale.
func span(ms int64) string { return (time.Duration(ms) * time.Millisecond).Round(time.Second).String() }
