// Package store is what this daemon remembers across restarts.
//
// The plugin contract's §5.1 store is SQLite. This is a JSON document
// instead, and the README records why: the whole set is a handful of rows held
// in memory anyway, rewritten whole on every change, read by one process under
// the daemon's own lock. A SQLite driver is a large dependency for a file that
// never needs a query, a schema or a second reader, and this repo's budget is
// the standard library plus the two libraries every sibling pins.
//
// Every entity here has a trail of its own beside it, `<entity>_events`, and
// the two are saved together because the document is written whole: a change
// and the event that records it can never land one without the other. Today
// that is the actions the policy gate parked, the cron jobs that fire them,
// the triggers that fire on an inbound signal, and the runs a signal's actions
// fired. A run has a trail and no list of rows beside it, because the run
// history IS the §8 stream and there is no second table (note 2).
//
// One thing is deliberately NOT here: a webhook's HMAC secret. It lives in its
// own file beside the document, so every door that renders this document
// renders no secret. Secrets holds the reasoning.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/husniadil/herdr-sched/internal/codes"
	"github.com/husniadil/herdr-sched/internal/job"
	"github.com/husniadil/herdr-sched/internal/trigger"
)

// Version is the document's shape. A document from a version this binary does
// not know is refused rather than guessed at.
const Version = 1

// Document is the whole store, as it is written and as it is read back.
//
// Every list is rendered even when it is empty, because a reader has to be
// able to tell "none" from "this daemon could not say", and a JSON null says
// the second while meaning the first.
type Document struct {
	Version int `json:"version"`
	// Parked is the actions the policy gate deferred (§9.3).
	Parked []Parked `json:"parked"`
	// ParkedEvents is that entity's own §8.1 trail. It is a sibling of the
	// list above rather than one trail shared by everything, so a second
	// entity arrives with its own and neither has to be split out later.
	ParkedEvents []Event `json:"parked_events"`
	// Jobs is every cron schedule (note 2), and JobEvents is that entity's
	// own §8.1 trail beside it.
	Jobs      []job.Job `json:"jobs"`
	JobEvents []Event   `json:"job_events"`
	// Triggers is every inbound signal (note 2), and TriggerEvents is that
	// entity's own §8.1 trail beside it. A webhook's HMAC secret is NOT here:
	// it is kept in a file of its own, so no door that renders this document
	// can print one.
	Triggers      []trigger.Trigger `json:"triggers"`
	TriggerEvents []Event           `json:"trigger_events"`
	// RunEvents is the run history: every time a signal's action fired, and
	// how it went. It is a trail with no list beside it on purpose (note 2):
	// the run history IS the §8 stream, and there is no second table holding
	// the same facts in another shape.
	RunEvents []Event `json:"run_events"`
}

// Store is the document at Path, held in memory and written whole.
type Store struct {
	// Path is the file the document is written to. An empty path is an
	// in-memory store, which is what a test that does not care uses.
	Path string

	mu  sync.Mutex
	doc Document
}

// Open reads the document at Path, or starts an empty one when there is no
// file yet. A document this binary does not know the shape of is refused: the
// alternative is writing a shape back over one that meant something else.
func Open(path string) (*Store, error) {
	s := &Store{Path: path, doc: Document{Version: Version}}
	if path == "" {
		return s, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, codes.Errorf(codes.Unavailable, "read the store %s: %v", path, err)
	}
	var doc Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, codes.Errorf(codes.Unavailable, "the store %s is not readable JSON: %v", path, err)
	}
	if doc.Version != Version {
		return nil, codes.Errorf(codes.Unsupported,
			"the store %s is version %d and this binary writes version %d; it is not overwritten",
			path, doc.Version, Version)
	}
	s.doc = doc
	return s, nil
}

// Snapshot is one read of the whole document, which is what `dump` answers
// with. It is one read rather than several, so a save cannot land between two
// of them and print a document no process ever held.
func (s *Store) Snapshot() Document {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.copy()
}

// copy is the held document with every list copied out, so a caller cannot
// write through the slice it was handed. The caller holds the lock.
func (s *Store) copy() Document {
	return Document{
		Version:       Version,
		Parked:        append([]Parked{}, s.doc.Parked...),
		ParkedEvents:  append([]Event{}, s.doc.ParkedEvents...),
		Jobs:          append([]job.Job{}, s.doc.Jobs...),
		JobEvents:     append([]Event{}, s.doc.JobEvents...),
		Triggers:      append([]trigger.Trigger{}, s.doc.Triggers...),
		TriggerEvents: append([]Event{}, s.doc.TriggerEvents...),
		RunEvents:     append([]Event{}, s.doc.RunEvents...),
	}
}

// Trail is every entity's events in one sequence, oldest first. An event id
// opens with the millisecond it was written, so sorting by id is sorting by
// time with a stable tiebreak.
func (s *Store) Trail() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return merge(s.doc.ParkedEvents, s.doc.JobEvents, s.doc.TriggerEvents, s.doc.RunEvents)
}

func merge(trails ...[]Event) []Event {
	out := []Event{}
	for _, t := range trails {
		out = append(out, t...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// update runs fn against the held document and writes the result whole. The
// document is only replaced when both the change and the save succeeded, so a
// write that could not reach disk is not silently live in memory.
func (s *Store) update(fn func(*Document) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.copy()
	if err := fn(&next); err != nil {
		return err
	}
	next.ParkedEvents = Rotate(next.ParkedEvents)
	next.JobEvents = Rotate(next.JobEvents)
	next.TriggerEvents = Rotate(next.TriggerEvents)
	next.RunEvents = Rotate(next.RunEvents)
	if err := s.write(next); err != nil {
		return err
	}
	s.doc = next
	return nil
}

// write puts the document on disk whole, through a temp file in the same
// directory: a crash mid-write leaves the previous document intact rather than
// half of the next one.
func (s *Store) write(doc Document) error {
	if s.Path == "" {
		return nil
	}
	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return codes.Errorf(codes.Unexpected, "render the store: %v", err)
	}
	body = append(body, '\n')
	dir := filepath.Dir(s.Path)
	tmp, err := os.CreateTemp(dir, filepath.Base(s.Path)+".*")
	if err != nil {
		return codes.Errorf(codes.Unavailable, "write the store in %s: %v", dir, err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return codes.Errorf(codes.Unavailable, "restrict %s to this user: %v", tmp.Name(), err)
	}
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return codes.Errorf(codes.Unavailable, "write %s: %v", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return codes.Errorf(codes.Unavailable, "close %s: %v", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), s.Path); err != nil {
		return codes.Errorf(codes.Unavailable, "put the store at %s: %v", s.Path, err)
	}
	return nil
}

// Events is the slice of the merged trail a reader asked for (§8.2).
func (s *Store) Events(f EventFilter) ([]Event, error) { return Select(s.Trail(), f) }

// Parked is every action the policy gate deferred, newest last.
func (s *Store) Parked() []Parked {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Parked{}, s.doc.Parked...)
}

// WaitingParked is how many deferrals still want the operator's attention,
// which is the number doctor reports.
func (s *Store) WaitingParked() int {
	n := 0
	for _, p := range s.Parked() {
		if p.Waiting() {
			n++
		}
	}
	return n
}

// RecordRun appends one run to the run trail. A fired action and a refused
// one both land here: a failure this daemon swallowed is a schedule that
// stopped working on a night nobody was watching.
func (s *Store) RecordRun(ev Event) error {
	return s.update(func(doc *Document) error {
		doc.RunEvents = append(doc.RunEvents, ev)
		return nil
	})
}

// Park records a deferral and the event that says so, in one save.
func (s *Store) Park(p Parked, ev Event) error {
	return s.update(func(doc *Document) error {
		doc.Parked = append(doc.Parked, p)
		doc.ParkedEvents = append(doc.ParkedEvents, ev)
		return nil
	})
}

// ClaimParked moves one waiting row to a decided state and hands back the row
// as it was, so the caller can re-run the verb it carries.
//
// The move happens BEFORE the verb runs, which is what makes it the one-winner
// check: two resolves that both read the row as waiting would otherwise both
// run the verb, with the side effect really happening twice and the loser told
// CONFLICT for work that had already committed.
func (s *Store) ClaimParked(id, state, by string, atMS int64, ev Event) (Parked, error) {
	var was Parked
	err := s.update(func(doc *Document) error {
		for i, p := range doc.Parked {
			if p.ID != id {
				continue
			}
			if !p.Waiting() {
				return codes.Refusef(codes.AlreadyResolved,
					"the parked action %s was already %s by %s", id, p.State, orUnknown(p.ResolvedBy))
			}
			was = p
			doc.Parked[i].State = state
			doc.Parked[i].ResolvedBy = by
			doc.Parked[i].ResolvedMS = atMS
			// A row decided again is a row whose last failure is history.
			doc.Parked[i].Error = ""
			doc.ParkedEvents = append(doc.ParkedEvents, ev)
			return nil
		}
		return codes.Errorf(codes.NotFound, "no parked action %s", id)
	})
	return was, err
}

// FailParked records that a resolved action's verb did not run. The decision
// stands: an action that errored is not proof it had no effect, so the operator
// reads why and decides again, deliberately.
func (s *Store) FailParked(id, reason string, ev Event) error {
	return s.update(func(doc *Document) error {
		for i, p := range doc.Parked {
			if p.ID == id {
				doc.Parked[i].State = ParkedFailed
				doc.Parked[i].Error = reason
				doc.ParkedEvents = append(doc.ParkedEvents, ev)
				return nil
			}
		}
		return codes.Errorf(codes.NotFound, "no parked action %s", id)
	})
}

func orUnknown(s string) string {
	if s == "" {
		return "someone this daemon did not record"
	}
	return s
}

// String is the document rendered the way `dump` prints it, for a test that
// wants to read it back.
func (d Document) String() string {
	body, err := json.Marshal(d)
	if err != nil {
		return fmt.Sprintf("unrenderable store: %v", err)
	}
	return string(body)
}
