package store

import (
	"github.com/husniadil/herdr-sched/internal/codes"
	"github.com/husniadil/herdr-sched/internal/trigger"
)

// EntityTrigger is an inbound signal (note 2): a webhook on a server-issued
// URL, or a watcher on a host path. Like a job and unlike a run, it HAS a list
// of rows beside its trail: a trigger is a thing that exists between firings.
const EntityTrigger = "trigger"

// The kinds a trigger's trail carries beyond the shared added, removed,
// enabled and disabled.
const (
	// KindDropped is an inbound request whose HMAC did not hold. It names the
	// trigger it was aimed at and nothing was fired. It is on the trail rather
	// than swallowed: a webhook URL being probed is something an operator
	// wants to see, and a trigger that stopped working because a caller's
	// secret drifted looks exactly like one nobody is calling.
	KindDropped = "dropped"
	// KindLimited is a genuine signal the cooldown or the hourly limit held
	// down. Rate limiting refuses LOUDLY (note 2): a firing that vanished is
	// indistinguishable from one that never arrived.
	KindLimited = "limited"
)

// Triggers is every trigger this daemon holds, in the order they were written.
func (s *Store) Triggers() []trigger.Trigger {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]trigger.Trigger{}, s.doc.Triggers...)
}

// Trigger is one trigger by id.
func (s *Store) Trigger(id string) (trigger.Trigger, bool) {
	for _, t := range s.Triggers() {
		if t.ID == id {
			return t, true
		}
	}
	return trigger.Trigger{}, false
}

// AddTrigger writes one trigger and the event that says so, in one save. A
// duplicate id is refused rather than merged: the id is both what
// `trigger:<id>` attributes every call to and the URL segment a webhook is
// reached on, and two triggers answering to one name is a request nobody can
// say which row handled.
func (s *Store) AddTrigger(t trigger.Trigger, ev Event) error {
	return s.update(func(doc *Document) error {
		for _, held := range doc.Triggers {
			if held.ID == t.ID {
				return codes.Refusef(codes.AlreadyExists, "there is already a trigger called %s", t.ID)
			}
		}
		doc.Triggers = append(doc.Triggers, t)
		doc.TriggerEvents = append(doc.TriggerEvents, ev)
		return nil
	})
}

// RemoveTrigger takes one trigger off for good and answers with the row as it
// was, so the caller can say what was removed and drop its secret.
func (s *Store) RemoveTrigger(id string, ev Event) (trigger.Trigger, error) {
	var was trigger.Trigger
	err := s.update(func(doc *Document) error {
		for i, held := range doc.Triggers {
			if held.ID != id {
				continue
			}
			was = held
			doc.Triggers = append(doc.Triggers[:i:i], doc.Triggers[i+1:]...)
			doc.TriggerEvents = append(doc.TriggerEvents, ev)
			return nil
		}
		return codes.Errorf(codes.NotFound, "no trigger called %s", id)
	})
	return was, err
}

// SetTriggerEnabled turns one trigger off or on. Asking for the state it is
// already in changes nothing and writes no event. The second answer says
// whether anything moved.
func (s *Store) SetTriggerEnabled(id string, on bool, ev Event) (trigger.Trigger, bool, error) {
	var was trigger.Trigger
	changed := false
	err := s.update(func(doc *Document) error {
		for i, held := range doc.Triggers {
			if held.ID != id {
				continue
			}
			was = held
			if held.Enabled == on {
				return nil
			}
			doc.Triggers[i].Enabled = on
			was = doc.Triggers[i]
			changed = true
			doc.TriggerEvents = append(doc.TriggerEvents, ev)
			return nil
		}
		return codes.Errorf(codes.NotFound, "no trigger called %s", id)
	})
	return was, changed, err
}

// ClaimTriggerFire is the one-winner check for one inbound signal: it re-reads
// the row under the store's lock, asks the pure core again, and moves the
// cursor BEFORE the action fires.
//
// Asking outside the lock and firing after it is what would let two requests
// arriving in the same millisecond both read a spent cooldown as unspent. The
// webhook door is concurrent by nature — one HTTP server, any number of
// connections — so the decision that matters is the one made here, against the
// row as it is now.
//
// The verdict it answers with is the one it acted on. A refusal moves nothing.
func (s *Store) ClaimTriggerFire(id string, decide func(trigger.Trigger) (trigger.Trigger, trigger.Verdict)) (trigger.Verdict, error) {
	var verdict trigger.Verdict
	err := s.update(func(doc *Document) error {
		for i, held := range doc.Triggers {
			if held.ID != id {
				continue
			}
			next, v := decide(held)
			verdict = v
			if v.Fire {
				doc.Triggers[i] = next
			}
			return nil
		}
		return codes.Errorf(codes.NotFound, "no trigger called %s", id)
	})
	return verdict, err
}

// StampTrigger records what the watcher saw at a trigger's path. It is a mark
// rather than a record and carries no event: the firing it may cause is on the
// run trail, and a second event saying the file changed is one more fact to
// keep in step.
func (s *Store) StampTrigger(id string, stamp trigger.Stamp) error {
	return s.update(func(doc *Document) error {
		for i, held := range doc.Triggers {
			if held.ID == id {
				doc.Triggers[i].Stamp = stamp
				return nil
			}
		}
		return codes.Errorf(codes.NotFound, "no trigger called %s", id)
	})
}
