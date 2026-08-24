package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/husniadil/herdr-sched/internal/codes"
	"github.com/husniadil/herdr-sched/internal/config"
	"github.com/husniadil/herdr-sched/internal/protocol"
	"github.com/husniadil/herdr-sched/internal/store"
	"github.com/husniadil/herdr-sched/internal/testenv"
)

// pinned is a clock a test can reason about, so an id and a timestamp are not
// whatever the machine happened to say.
var pinned = time.UnixMilli(1_700_000_000_000)

func newDaemon(t *testing.T) *Daemon {
	t.Helper()
	testenv.New(t)
	st, err := store.Open(config.StorePath())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	return &Daemon{
		Store:    st,
		Config:   cfg,
		Interval: time.Hour,
		Version:  "0.1.0",
		Log:      log.New(io.Discard, "", 0),
		Now:      func() time.Time { return pinned },
	}
}

func call(t *testing.T, d *Daemon, verb string, args map[string]any) (json.RawMessage, error) {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	return d.Handle(context.Background(), protocol.Request{
		Verb: verb, Args: args, Pane: "wT:p1", Door: "cli"})
}

// §10.3: doctor answers with the version and the contract, and never fails —
// it is what an operator runs when something else already refused.
func TestDoctorAnswersWithTheVersionAndTheContract(t *testing.T) {
	d := newDaemon(t)
	raw, err := call(t, d, "doctor", nil)
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	var rep DoctorReport
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Version != "0.1.0" || rep.Contract == "" || rep.Plugin != "herdr-sched" {
		t.Fatalf("report = %+v", rep)
	}
	// §10.3 makes doctor print the directories this DAEMON resolved, which is
	// the one place an operator can see an override that is not taking effect.
	if rep.StateDir != config.StateDir() || rep.ConfigDir != config.ConfigDir() {
		t.Errorf("doctor names %s / %s and the daemon resolved %s / %s",
			rep.StateDir, rep.ConfigDir, config.StateDir(), config.ConfigDir())
	}
	if rep.Store != config.StorePath() || rep.Socket != config.SocketPath() {
		t.Errorf("report = %+v", rep)
	}
	// §9.2: an unconfigured gate allows, and that is indistinguishable at the
	// call site from a configured one that allows.
	if rep.Gate.Configured || len(rep.Gate.Verbs) == 0 {
		t.Errorf("gate = %+v", rep.Gate)
	}
	if rep.Events.Max != store.MaxEvents {
		t.Errorf("events = %+v", rep.Events)
	}
}

// An argument the verb never declared is refused rather than dropped: a door
// that drops one silently is how a caller ends up believing something it asked
// for happened.
func TestAnUndeclaredArgumentIsRefused(t *testing.T) {
	d := newDaemon(t)
	_, err := call(t, d, "doctor", map[string]any{"cursor": "nope"})
	if codes.Of(err) != codes.Usage {
		t.Fatalf("code = %s, want USAGE", codes.Of(err))
	}
}

func TestAnUnknownVerbIsRefused(t *testing.T) {
	d := newDaemon(t)
	_, err := call(t, d, "schedule", nil)
	if codes.Of(err) != codes.Usage {
		t.Fatalf("code = %s, want USAGE", codes.Of(err))
	}
}

func TestARequiredArgumentIsRefusedWhenItIsMissing(t *testing.T) {
	d := newDaemon(t)
	_, err := call(t, d, "parked.resolve", nil)
	if codes.Of(err) != codes.Usage {
		t.Fatalf("code = %s, want USAGE", codes.Of(err))
	}
}

// §5.8: dump is the whole store in one document, with the file it is written
// to named, so a reader who wants it without this binary knows where to look.
func TestDumpIsTheWholeStoreWithItsPath(t *testing.T) {
	d := newDaemon(t)
	raw, err := call(t, d, "dump", nil)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	var rep DumpReport
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Path != config.StorePath() || rep.Version != store.Version {
		t.Fatalf("report = %+v", rep)
	}
	// Every entity's own trail is part of the store, so §5.8's "the whole
	// store" includes it: a reader should not have to know a list was held
	// back.
	if !strings.Contains(string(raw), "parked_events") {
		t.Fatalf("dump withholds an entity's trail: %s", raw)
	}
}

// §9.2: a gate that denies stops the verb, and the caller is told which verb
// and why.
func TestAGateThatDeniesStopsTheVerb(t *testing.T) {
	testenv.SkipUnlessFull(t)
	f := testenv.New(t)
	f.Gate(t, "deny", "not this one")
	d := daemonOver(t)

	_, err := call(t, d, "stop", nil)
	if codes.Of(err) != codes.Denied {
		t.Fatalf("code = %s, want DENIED", codes.Of(err))
	}
	if !strings.Contains(codes.Message(err), "sched.stop") || !strings.Contains(codes.Message(err), "not this one") {
		t.Fatalf("the refusal does not say what was refused or why: %v", err)
	}
	if len(d.Store.Parked()) != 0 {
		t.Fatal("a denial parked a row; only a deferral does that")
	}
}

// §9.3: a gate that defers PARKS the call — recorded, not performed — and the
// caller is told DENIED with the row to name.
func TestAGateThatDefersParksTheCallAndNamesTheRow(t *testing.T) {
	testenv.SkipUnlessFull(t)
	f := testenv.New(t)
	f.Gate(t, "defer", "an operator decides this one")
	d := daemonOver(t)

	_, err := call(t, d, "stop", nil)
	if codes.Of(err) != codes.Denied {
		t.Fatalf("code = %s, want DENIED", codes.Of(err))
	}
	id := codes.ParkedOf(err)
	if id == "" {
		t.Fatal("a deferral left the caller nothing to resolve")
	}
	rows := d.Store.Parked()
	if len(rows) != 1 || rows[0].ID != id || rows[0].State != store.ParkedWaiting {
		t.Fatalf("parked = %+v", rows)
	}
	if rows[0].Verb != "sched.stop" || rows[0].Subject != "agent:wT:p1" {
		t.Fatalf("the row does not record who asked for what: %+v", rows[0])
	}
	// The deferral is on the trail from day one, in the entity's own list.
	trail := d.Store.Snapshot().ParkedEvents
	if len(trail) != 1 || trail[0].Name != "sched.parked.parked" {
		t.Fatalf("parked_events = %+v", trail)
	}
}

// §9.3: resolving re-runs the verb under the subject the gate stopped, and
// does NOT ask the gate again — the resolution IS the decision it deferred,
// and a second ask would park it forever.
func TestResolvingReRunsTheVerbWithoutAskingTheGateAgain(t *testing.T) {
	testenv.SkipUnlessFull(t)
	f := testenv.New(t)
	f.Gate(t, "defer", "an operator decides this one")
	d := daemonOver(t)
	d.halt = make(chan struct{})

	_, err := call(t, d, "stop", nil)
	id := codes.ParkedOf(err)
	if id == "" {
		t.Fatalf("nothing was parked: %v", err)
	}
	raw, err := call(t, d, "parked.resolve", map[string]any{"id": id})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	var res ParkedResolution
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	if res.State != store.ParkedResolved || len(res.Result) == 0 {
		t.Fatalf("resolution = %+v", res)
	}
	// The verb really ran: stop closed the halt channel.
	select {
	case <-d.halt:
	default:
		t.Fatal("the resolved verb did not run")
	}
	rows := d.Store.Parked()
	if rows[0].ResolvedBy != "agent:wT:p1" {
		t.Fatalf("the row does not record who decided: %+v", rows[0])
	}
}

// --reject closes the row and the verb never runs.
func TestRejectingAParkedActionNeverRunsTheVerb(t *testing.T) {
	testenv.SkipUnlessFull(t)
	f := testenv.New(t)
	f.Gate(t, "defer", "an operator decides this one")
	d := daemonOver(t)
	d.halt = make(chan struct{})

	_, err := call(t, d, "stop", nil)
	id := codes.ParkedOf(err)
	if _, err := call(t, d, "parked.resolve", map[string]any{"id": id, "reject": true}); err != nil {
		t.Fatalf("reject: %v", err)
	}
	select {
	case <-d.halt:
		t.Fatal("a rejected action ran its verb")
	default:
	}
	if d.Store.Parked()[0].State != store.ParkedRefused {
		t.Fatalf("row = %+v", d.Store.Parked()[0])
	}
}

// A resolved action whose verb failed is recorded as failed, with the verb's
// own words, and stays waiting on the operator.
func TestAResolvedActionWhoseVerbFailedSaysSo(t *testing.T) {
	testenv.SkipUnlessFull(t)
	f := testenv.New(t)
	f.Gate(t, "defer", "an operator decides this one")
	d := daemonOver(t)
	// No halt channel: this daemon is not serving, so stop cannot run.

	_, err := call(t, d, "stop", nil)
	id := codes.ParkedOf(err)
	if _, err := call(t, d, "parked.resolve", map[string]any{"id": id}); err == nil {
		t.Fatal("resolving a verb that could not run reported success")
	}
	row := d.Store.Parked()[0]
	if row.State != store.ParkedFailed || row.Error == "" {
		t.Fatalf("row = %+v", row)
	}
	if !row.Waiting() {
		t.Fatal("a failed action stopped wanting the operator's attention")
	}
}

// §8.2: the trail is read oldest first, and resuming from an id the rotation
// has passed is refused rather than answered with the whole window.
func TestEventsResumesAndRefusesARotatedID(t *testing.T) {
	testenv.SkipUnlessFull(t)
	f := testenv.New(t)
	f.Gate(t, "defer", "an operator decides this one")
	d := daemonOver(t)
	for i := 0; i < 2; i++ {
		call(t, d, "stop", nil)
	}

	raw, err := call(t, d, "events", nil)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	var rep EventsReport
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Count != 2 {
		t.Fatalf("trail = %+v", rep)
	}
	raw, err = call(t, d, "events", map[string]any{"since": rep.Events[0].ID})
	if err != nil {
		t.Fatalf("events since: %v", err)
	}
	json.Unmarshal(raw, &rep)
	if rep.Count != 1 {
		t.Fatalf("resume = %+v", rep)
	}
	_, err = call(t, d, "events", map[string]any{"since": "ev-0000000000000-deadbeef"})
	if codes.Of(err) != codes.NotFound {
		t.Fatalf("code = %s, want NOT_FOUND", codes.Of(err))
	}
}

// §2.3: one daemon per store. The lock is what says whether one is running,
// and a second one meets it rather than racing.
func TestASecondDaemonMeetsTheFirstOnesLock(t *testing.T) {
	testenv.SkipUnlessFull(t)
	testenv.New(t)
	first, err := Lock()
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	defer first.Close()

	_, err = Lock()
	if err == nil {
		t.Fatal("a second daemon took the lock")
	}
	if codes.Of(err) != codes.Conflict || codes.ReasonOf(err) != codes.AlreadyRunning {
		t.Fatalf("second lock refused with %s / %s", codes.Of(err), codes.ReasonOf(err))
	}
	if !strings.Contains(codes.Message(err), config.LockPath()) {
		t.Errorf("the refusal does not name the lock: %v", err)
	}
}

// A socket file with no daemon behind it is replaced: the lock, not the file,
// is what says whether one is running.
func TestAStaleSocketFileIsReplaced(t *testing.T) {
	testenv.SkipUnlessFull(t)
	testenv.New(t)
	if err := config.EnsureStateDir(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.SocketPath(), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	ln, err := Listen()
	if err != nil {
		t.Fatalf("Listen over a stale socket: %v", err)
	}
	defer ln.Close()
	info, err := os.Stat(config.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Fatal("the stale file was not replaced by a socket")
	}
	// §3.5: the socket is this user's alone.
	if perm := info.Mode().Perm(); perm != SocketMode {
		t.Fatalf("socket is %o, want %o", perm, SocketMode)
	}
}

// Stopping leaves nothing behind that says a daemon is still here.
func TestStoppingRemovesTheSocketAndTheLock(t *testing.T) {
	testenv.SkipUnlessFull(t)
	testenv.New(t)
	lock, err := Lock()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := Listen()
	if err != nil {
		t.Fatal(err)
	}
	d := newDaemonOn(t, lock)

	done := make(chan error, 1)
	go func() { done <- d.Serve(context.Background(), ln) }()
	waitForSocket(t)

	if _, err := askOverSocket(t, protocol.Request{Verb: "stop", Args: map[string]any{}, Door: "cli"}); err != nil {
		t.Fatalf("stop: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the daemon did not stop")
	}
	for _, path := range []string{config.SocketPath(), lock.Name()} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s is still there after stopping", path)
		}
	}
}

// §8.2: `events --follow` is handed the backlog and then every event as it is
// written, and the daemon says when the stream is over.
func TestFollowSendsTheBacklogAndThenLiveEvents(t *testing.T) {
	testenv.SkipUnlessFull(t)
	f := testenv.New(t)
	f.Gate(t, "defer", "an operator decides this one")
	lock, err := Lock()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := Listen()
	if err != nil {
		t.Fatal(err)
	}
	d := newDaemonOn(t, lock)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Serve(ctx, ln)
	waitForSocket(t)

	// One event before the follower attaches: it is the backlog.
	if _, err := askOverSocket(t, protocol.Request{Verb: "stop", Args: map[string]any{}, Pane: "wT:p1"}); err == nil {
		t.Fatal("the deferred stop was not parked")
	}

	conn, err := net.Dial("unix", config.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(protocol.Request{
		Verb: "events", Follow: true, Args: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(conn)
	var first protocol.Response
	if err := dec.Decode(&first); err != nil {
		t.Fatalf("backlog: %v", err)
	}
	if first.Error != nil || len(first.Result) == 0 {
		t.Fatalf("backlog = %+v", first)
	}

	// And one after: it arrives live.
	if _, err := askOverSocket(t, protocol.Request{Verb: "stop", Args: map[string]any{}, Pane: "wT:p2"}); err == nil {
		t.Fatal("the second deferred stop was not parked")
	}
	var live protocol.Response
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err := dec.Decode(&live); err != nil {
		t.Fatalf("live: %v", err)
	}
	var ev store.Event
	if err := json.Unmarshal(live.Result, &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Name != "sched.parked.parked" {
		t.Fatalf("live event = %+v", ev)
	}
	if d.followers.count() != 1 {
		t.Fatalf("followers = %d, want 1", d.followers.count())
	}
}

// The §8.3 hook is handed every event, detached, with the event on stdin.
func TestTheEventHookIsHandedEveryEvent(t *testing.T) {
	testenv.SkipUnlessFull(t)
	f := testenv.New(t)
	seen := filepath.Join(f.Dir, "seen.jsonl")
	hook := filepath.Join(f.Dir, "hook")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ncat >> \""+seen+"\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	gate := filepath.Join(f.Dir, "gate")
	if err := os.WriteFile(gate, []byte("#!/bin/sh\ncat >/dev/null\nprintf '{\"decision\":\"defer\",\"reason\":\"ask\"}\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.Config(t, "gate_command = [\""+gate+"\"]\non_event = [\""+hook+"\"]\n")
	d := daemonOver(t)

	call(t, d, "stop", nil)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(seen); err == nil && strings.Contains(string(raw), "sched.parked.parked") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the event hook was never handed the event")
}

// daemonOver builds a daemon over the world testenv.New already stood up, so a
// case that wrote a config or a gate first gets a daemon that reads it.
func daemonOver(t *testing.T) *Daemon {
	t.Helper()
	st, err := store.Open(config.StorePath())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	return &Daemon{
		Store: st, Config: cfg, Interval: time.Hour, Version: "0.1.0",
		Log: log.New(io.Discard, "", 0), Now: func() time.Time { return pinned },
	}
}

func newDaemonOn(t *testing.T, lock *os.File) *Daemon {
	t.Helper()
	d := daemonOver(t)
	d.Lock = lock
	return d
}

func waitForSocket(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if conn, err := net.Dial("unix", config.SocketPath()); err == nil {
			conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the daemon never bound its socket")
}

func askOverSocket(t *testing.T, req protocol.Request) (json.RawMessage, error) {
	t.Helper()
	conn, err := net.Dial("unix", config.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		t.Fatal(err)
	}
	var resp protocol.Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		return nil, &codes.Error{Code: codes.Code(resp.Error.Code), Message: resp.Error.Message, ParkedID: resp.Error.ParkedID}
	}
	return resp.Result, nil
}
