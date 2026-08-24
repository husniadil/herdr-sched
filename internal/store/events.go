package store

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/husniadil/herdr-sched/internal/codes"
)

// Event is one state change of this plugin's own, as §8.1 shapes it:
// `{id, at, actor, project, entity, kind, detail}`, plus the §8.1 name the
// three parts spell out, `sched.<entity>.<kind>`.
//
// What is HERE is what exists nowhere else. A sibling's fact — a task created
// on the board, a message sent — is that sibling's own trail and is not
// copied into this one.
type Event struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Entity is what the event is about. Each entity keeps its events in its
	// own `<entity>_events` list, and this field says which one a merged read
	// took an event from.
	Entity   string `json:"entity"`
	EntityID string `json:"entity_id"`
	Project  string `json:"project,omitempty"`
	// AtMS is Unix milliseconds (§5.3). It is `at` on the wire, which is the
	// name §8.1 gives it.
	AtMS  int64  `json:"at"`
	Actor string `json:"actor"`
	Kind  string `json:"kind"`
	// Detail is verb-specific and small on purpose. A consumer that wants
	// more asks the entity.
	Detail map[string]any `json:"detail,omitempty"`
}

// EntityParked is an action the policy gate deferred (§9.3). It is the one
// entity this scaffold has, and its trail is Document.ParkedEvents.
const EntityParked = "parked"

// The kinds an event of this plugin's carries.
const (
	// KindParked is the gate deferring a call.
	KindParked = "parked"
	// KindResolved is the operator letting the verb through.
	KindResolved = "resolved"
	// KindRefused is the operator closing the action without running it.
	KindRefused = "refused"
	// KindFailed is a resolved action whose verb then errored.
	KindFailed = "failed"
)

// MaxEvents is how many events one entity's trail keeps. The whole document is
// written on every change, so an unbounded trail makes every save slower for
// as long as the daemon runs; the newest are kept because a consumer resumes
// from where it left off and an operator asks what happened last night.
//
// A consumer that cannot afford to miss one follows the stream (§8.2) rather
// than polling a window: `events --follow` is handed every event as it is
// written, whatever the trail then does with it.
const MaxEvents = 1000

// NewEvent builds one event with its §8.1 name spelled out from its parts, so
// the name and the fields can never disagree.
func NewEvent(now time.Time, entity, kind, entityID, actor string, detail map[string]any) Event {
	return Event{
		ID:       NewEventID(now),
		Name:     "sched." + entity + "." + kind,
		Entity:   entity,
		EntityID: entityID,
		AtMS:     now.UnixMilli(),
		Actor:    actor,
		Kind:     kind,
		Detail:   detail,
	}
}

// NewEventID is a sortable id: the millisecond the event was written, then
// eight random hex digits so two in the same millisecond are still two. It is
// the same shape a parked action's id has, and for the same reason — this is a
// name for one row in one operator's own file.
func NewEventID(now time.Time) string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("ev-%013d-00000000", now.UnixMilli())
	}
	return fmt.Sprintf("ev-%013d-%s", now.UnixMilli(), hex.EncodeToString(b[:]))
}

// Rotate is one trail bounded to MaxEvents, newest kept.
func Rotate(trail []Event) []Event {
	if len(trail) <= MaxEvents {
		return trail
	}
	return append([]Event{}, trail[len(trail)-MaxEvents:]...)
}

// EventFilter is the slice of the trail a reader asked for.
type EventFilter struct {
	// SinceID resumes strictly after that event.
	SinceID string
	// SinceMS resumes strictly after that millisecond.
	SinceMS int64
	// Limit stops after that many; zero is all of them.
	Limit int
}

// Select is the trail a reader asked for, oldest first.
//
// A since id the trail does not carry is REFUSED rather than read as no filter
// at all. A consumer resuming from an id the rotation has passed would
// otherwise be handed the whole window again and take it for the tail of its
// own stream, which is the one failure a resumable trail exists to prevent.
func Select(trail []Event, f EventFilter) ([]Event, error) {
	from := 0
	if f.SinceID != "" {
		found := false
		for i, ev := range trail {
			if ev.ID == f.SinceID {
				from, found = i+1, true
				break
			}
		}
		if !found {
			return nil, codes.Errorf(codes.NotFound,
				"the trail has no event %s to resume from: each entity keeps at most %d events and this one has rotated past, so read from the beginning or pass a millisecond",
				f.SinceID, MaxEvents)
		}
	}
	out := []Event{}
	for _, ev := range trail[from:] {
		if f.SinceMS > 0 && ev.AtMS <= f.SinceMS {
			continue
		}
		out = append(out, ev)
		if f.Limit > 0 && len(out) == f.Limit {
			break
		}
	}
	return out, nil
}
