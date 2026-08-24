package daemon

import (
	"context"
	"encoding/json"
	"net"
	"os/exec"
	"strconv"
	"sync"
	"syscall"

	"github.com/husniadil/herdr-sched/internal/codes"
	"github.com/husniadil/herdr-sched/internal/protocol"
	"github.com/husniadil/herdr-sched/internal/store"
	"github.com/husniadil/herdr-sched/internal/verbs"
)

// EventsReport is what a single `events` call answers with.
type EventsReport struct {
	Events []store.Event `json:"events"`
	Count  int           `json:"count"`
}

// events is one read of the merged trail (§8.2). A `since` that is all digits
// is a Unix-millisecond timestamp, and anything else is an event id — an id
// opens with `ev-`, so the two can never be confused for each other.
func (d *Daemon) events(req protocol.Request) (EventsReport, error) {
	f, err := filterFrom(req)
	if err != nil {
		return EventsReport{}, err
	}
	trail, err := d.Store.Events(f)
	if err != nil {
		return EventsReport{}, err
	}
	return EventsReport{Events: trail, Count: len(trail)}, nil
}

func filterFrom(req protocol.Request) (store.EventFilter, error) {
	var f store.EventFilter
	if raw, ok := req.Args["since"].(string); ok && raw != "" {
		if ms, err := strconv.ParseInt(raw, 10, 64); err == nil {
			if ms < 0 {
				return f, codes.Refusef(codes.Invalid, "since must not be negative, got %q", raw)
			}
			f.SinceMS = ms
		} else {
			f.SinceID = raw
		}
	}
	if raw, ok := req.Args["limit"]; ok {
		n, err := wholeNumber(raw)
		if err != nil {
			return f, err
		}
		if n < 0 {
			return f, codes.Refusef(codes.Invalid, "limit must not be negative, got %d", n)
		}
		f.Limit = n
	}
	return f, nil
}

func wholeNumber(raw any) (int, error) {
	switch n := raw.(type) {
	case float64:
		return int(n), nil
	case int:
		return n, nil
	}
	return 0, codes.Refusef(codes.Invalid, "events wants limit as a whole number")
}

// stream is `events --follow` (§8.2): the events already held, then every one
// written after them, until the caller goes away or the daemon stops.
//
// The backlog is sent before the subscription is registered, and the two
// cannot overlap because the trail read and the register happen under one
// lock: an event written between them would otherwise be sent twice or not at
// all, and a follower has no way to tell either from the truth.
func (d *Daemon) stream(ctx context.Context, req protocol.Request, conn net.Conn) {
	enc := json.NewEncoder(conn)
	v, ok := verbs.ByName(req.Verb)
	if !ok {
		d.write(conn, protocol.Response{Error: &protocol.Failure{
			Code: string(codes.Usage), Message: "no verb named " + req.Verb}})
		return
	}
	if err := check(v, req.Args); err != nil {
		d.write(conn, protocol.Response{Error: &protocol.Failure{
			Code: string(codes.Of(err)), Message: codes.Message(err)}})
		return
	}
	f, err := filterFrom(req)
	if err != nil {
		d.write(conn, protocol.Response{Error: &protocol.Failure{
			Code: string(codes.Of(err)), Message: codes.Message(err)}})
		return
	}

	live := make(chan store.Event, 64)
	backlog, err := d.followers.attach(live, func() ([]store.Event, error) {
		return d.Store.Events(f)
	})
	if err != nil {
		d.write(conn, protocol.Response{Error: &protocol.Failure{
			Code: string(codes.Of(err)), Message: codes.Message(err)}})
		return
	}
	defer d.followers.detach(live)

	for _, ev := range backlog {
		if err := writeEvent(enc, ev); err != nil {
			return
		}
	}
	for {
		select {
		case <-ctx.Done():
			// The daemon is going. Saying so is what lets a follower tell
			// "there is no more" from "I stopped being told".
			enc.Encode(protocol.Response{Done: true})
			return
		case ev := <-live:
			if err := writeEvent(enc, ev); err != nil {
				return
			}
		}
	}
}

func writeEvent(enc *json.Encoder, ev store.Event) error {
	raw, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	return enc.Encode(protocol.Response{Result: raw})
}

// Emitted hands one event to everything that wants it: every live follower,
// and the §8.3 hook. It is the one place both live, so an event written
// anywhere in this daemon reaches both or neither.
func (d *Daemon) Emitted(ev store.Event) {
	d.followers.send(ev)
	d.hook(ev)
}

// hook runs the §8.3 command with the event on stdin, detached, with all three
// stdio closed to the daemon. A hook that hangs, prints or fails changes
// nothing here: it is the operator's own program, and this daemon is not
// waiting on it and never reads what it says.
func (d *Daemon) hook(ev store.Event) {
	if d.Config == nil || len(d.Config.OnEvent) == 0 {
		return
	}
	body, err := json.Marshal(ev)
	if err != nil {
		d.logf("render %s for the event hook: %v", ev.ID, err)
		return
	}
	cmd := exec.Command(d.Config.OnEvent[0], d.Config.OnEvent[1:]...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		d.logf("the event hook %s: %v", d.Config.OnEvent[0], err)
		return
	}
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		d.logf("start the event hook %s: %v", d.Config.OnEvent[0], err)
		stdin.Close()
		return
	}
	go func() {
		stdin.Write(append(body, '\n'))
		stdin.Close()
		cmd.Wait()
	}()
}

// watchers is every live `events --follow`.
type watchers struct {
	mu sync.Mutex
	to map[chan store.Event]struct{}
}

// attach reads the backlog and registers the follower under one lock, so an
// event written between the two is neither sent twice nor missed.
func (w *watchers) attach(ch chan store.Event, backlog func() ([]store.Event, error)) ([]store.Event, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	held, err := backlog()
	if err != nil {
		return nil, err
	}
	if w.to == nil {
		w.to = map[chan store.Event]struct{}{}
	}
	w.to[ch] = struct{}{}
	return held, nil
}

func (w *watchers) detach(ch chan store.Event) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.to, ch)
}

// send hands the event to every follower. A follower whose buffer is full is
// SKIPPED rather than waited for: one slow reader must not stop the daemon,
// and a follower that fell behind reads the trail again from the last id it
// saw.
func (w *watchers) send(ev store.Event) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for ch := range w.to {
		select {
		case ch <- ev:
		default:
		}
	}
}

// count is how many followers are live, for a test.
func (w *watchers) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.to)
}
