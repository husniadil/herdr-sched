// Package daemon is the one process that owns the store and the tick.
//
// Both doors are thin clients of it, and neither holds state: an MCP door is
// spawned once per client session, so anything kept there would be one of
// several disagreeing sets. One daemon per user is what makes one answer true
// across every caller.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/husniadil/herdr-sched/internal/codes"
	"github.com/husniadil/herdr-sched/internal/config"
	"github.com/husniadil/herdr-sched/internal/fire"
	"github.com/husniadil/herdr-sched/internal/gate"
	"github.com/husniadil/herdr-sched/internal/protocol"
	"github.com/husniadil/herdr-sched/internal/store"
	"github.com/husniadil/herdr-sched/internal/trigger"
	"github.com/husniadil/herdr-sched/internal/verbs"
	"github.com/husniadil/herdr-sched/internal/version"
)

// SocketMode is what the socket and the lock are created with. The state dir
// they sit in is private for the same reason: the boundary is the local user
// account (§3.5).
const SocketMode = 0o600

// Daemon serves the verb table over a socket and ticks behind it.
type Daemon struct {
	Store  *store.Store
	Config *config.Config
	// Secrets is every webhook's HMAC key. It is a file of its own beside the
	// store, so no door that renders the store document can render a secret.
	// A daemon without one serves every other verb and refuses to write a
	// webhook it could never verify.
	Secrets *store.Secrets
	// WebhookAddr is where the inbound trigger door listens, empty or `off`
	// for no inbound door at all.
	WebhookAddr string
	// Interval is how often the tick runs: how often the daemon asks which
	// jobs are due. It is far shorter than any schedule, because a job is
	// fired for the instant it was due at rather than for the tick that
	// noticed it.
	Interval time.Duration
	// Fire performs the action a due job carries. A daemon without one still
	// serves every verb and still keeps its cursors; what it cannot do is
	// fire, and a schedule that came round says so on the run trail rather
	// than passing quietly.
	Fire *fire.Runner
	// Version is this binary's version, for doctor.
	Version string
	Log     *log.Logger
	// LogPath is the log file this daemon opened, as it opened it, and what
	// doctor names. Empty means it could not open one and is writing to
	// stdout alone.
	LogPath string
	// Lock is the one-daemon lock this daemon holds, as Lock opened it.
	// Cleanup removes that file and no other: the path is what was opened,
	// never what the state dir names by the time cleanup runs.
	Lock *os.File
	// Now is the clock, so a test can pin one. Zero means time.Now.
	Now func() time.Time

	// halt carries an operator's stop from the verb to Serve.
	halt chan struct{}
	// answered closes when the stop that asked has its own answer on the
	// wire, which is what Serve waits for before the process goes. Waiting on
	// every open connection instead would hang on a caller that holds one
	// open and asks nothing.
	answered  chan struct{}
	stopOnce  sync.Once
	writeOnce sync.Once
	// followers is every live `events --follow`, woken by each event written.
	followers watchers
	// skipped is what the last start passed over, for doctor (note 2).
	skipped skips
	// writingTrigger serialises writing one trigger down. Every connection is
	// answered on its own goroutine, and a webhook is written as a duplicate
	// check, then a key, then a row — three steps the store's own lock covers
	// one at a time and nothing covers together. Two callers writing the same
	// id at once would otherwise interleave into a live trigger holding the
	// loser's key, which is a working webhook that silently stops verifying.
	writingTrigger sync.Mutex
	// inbound is the webhook door as doctor reports it.
	inbound inbound
}

// Lock takes the one-daemon lock, and holds it for as long as the returned
// file is open. The kernel releases it when the process ends, so a daemon that
// crashes leaves nothing behind to clean up (§2.3).
func Lock() (*os.File, error) {
	if err := config.EnsureStateDir(); err != nil {
		return nil, err
	}
	path := config.LockPath()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, SocketMode)
	if err != nil {
		return nil, codes.Errorf(codes.Unavailable, "open the daemon lock %s: %v", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, codes.Refusef(codes.AlreadyRunning,
			"another hsched daemon is running: it holds %s", path)
	}
	return f, nil
}

// Listen opens the daemon's socket (§2.2). A socket file with no daemon behind
// it is replaced: the lock, not the file, is what says whether one is running.
func Listen() (net.Listener, error) {
	if err := config.EnsureStateDir(); err != nil {
		return nil, err
	}
	path := config.SocketPath()
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, codes.Errorf(codes.Unavailable, "clear the socket at %s: %v", path, err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, codes.Errorf(codes.Unavailable, "listen on %s: %v", path, err)
	}
	if err := os.Chmod(path, SocketMode); err != nil {
		ln.Close()
		return nil, codes.Errorf(codes.Unavailable, "close the socket to other users: %v", err)
	}
	return ln, nil
}

// OpenLog opens the daemon's log file, falling back to the given writer. A log
// that cannot be opened is said out loud and is never a reason not to start:
// the daemon still works, and the operator reads it on stdout instead.
func OpenLog(path string, fallback *os.File) (*os.File, error) {
	if path == "" {
		return fallback, nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fallback, codes.Errorf(codes.Unavailable, "open the log %s: %v", path, err)
	}
	return f, nil
}

// Serve answers the socket and ticks until ctx ends or stop is asked for.
// Either way it leaves nothing of itself behind.
func (d *Daemon) Serve(ctx context.Context, ln net.Listener) error {
	d.halt = make(chan struct{})
	d.answered = make(chan struct{})
	ctx, done := context.WithCancel(ctx)
	defer done()
	// The catch-up pass, before the first connection is answered: every job
	// whose instant came round while this daemon was down is decided once,
	// here, rather than by whichever tick happens first (note 2).
	d.runDue(ctx, true)
	// The inbound door, beside the socket both CLI doors dial. A door that
	// cannot bind is said out loud and is never a reason not to start: every
	// schedule and every file watcher works without it.
	webhooks := d.listenWebhooks()
	serving := make(chan struct{})
	go func() {
		defer close(serving)
		d.serveWebhooks(ctx, webhooks)
	}()
	ticking := make(chan struct{})
	go func() {
		defer close(ticking)
		d.tick(ctx)
	}()
	go func() {
		select {
		case <-ctx.Done():
		case <-d.halt:
			done()
		}
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				select {
				case <-d.halt:
					// The stop that closed the listener is still writing its
					// own answer; leave after it, not during it.
					<-d.answered
				default:
				}
				<-ticking
				<-serving
				d.Cleanup(ln)
				return nil
			}
			return codes.Errorf(codes.Unavailable, "accept: %v", err)
		}
		go d.answer(ctx, conn)
	}
}

// Cleanup removes the socket this daemon is listening on and the lock it
// holds. The lock is released by the kernel when the process ends either way;
// removing the file as well keeps a stopped daemon from leaving a path behind
// that says one is still here.
//
// Both paths come from what was opened — the listener's own address, the lock
// file's own name — and never from the state dir read again here. A daemon
// that resolves them at teardown deletes whatever that dir holds by then,
// which is another daemon's socket the moment the dir has moved underneath it.
func (d *Daemon) Cleanup(ln net.Listener) {
	paths := []string{}
	if ln != nil {
		if addr, ok := ln.Addr().(*net.UnixAddr); ok && addr.Name != "" {
			paths = append(paths, addr.Name)
		}
	}
	if d.Lock != nil {
		paths = append(paths, d.Lock.Name())
	}
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			d.logf("remove %s: %v", path, err)
		}
	}
}

// tick is the §11.5 bounded timer: every interval it asks the pure core which
// jobs are due and performs the answer. Nothing is computed from the tick
// itself — a job fires for the SCHEDULED instant it was due at, so a tick that
// ran late fires the same thing a tick that ran on time would have.
func (d *Daemon) tick(ctx context.Context) {
	t := time.NewTicker(d.interval())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.runDue(ctx, false)
			// The file watchers poll on the same tick: fsnotify would be a
			// third dependency, and the daemon already has this rhythm.
			d.runWatchers(ctx)
		}
	}
}

func (d *Daemon) interval() time.Duration {
	if d.Interval > 0 {
		return d.Interval
	}
	return time.Duration(config.DefaultTickSeconds) * time.Second
}

func (d *Daemon) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *Daemon) answer(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	var req protocol.Request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		d.write(conn, protocol.Response{Error: &protocol.Failure{
			Code: string(codes.Usage), Message: "unreadable request: " + err.Error()}})
		return
	}
	// `events --follow` is the one call that does not answer once: it holds
	// the connection and writes one Response per event (§8.2).
	if req.Verb == "events" && req.Follow {
		d.stream(ctx, req, conn)
		return
	}
	result, err := d.Handle(ctx, req)
	if err != nil {
		d.write(conn, protocol.Response{Error: &protocol.Failure{
			Code: string(codes.Of(err)), Message: codes.Message(err), ParkedID: codes.ParkedOf(err)}})
		return
	}
	d.write(conn, protocol.Response{Result: result})
	if d.answered != nil && d.halted() && (req.Verb == "stop" || req.Verb == "parked.resolve") {
		// A stop reaches here directly, or wrapped in the parked.resolve
		// that let it through: either way it is this answer Serve waits on.
		d.writeOnce.Do(func() { close(d.answered) })
	}
}

// halted reports whether a stop has closed the halt channel.
func (d *Daemon) halted() bool {
	if d.halt == nil {
		return false
	}
	select {
	case <-d.halt:
		return true
	default:
		return false
	}
}

func (d *Daemon) write(conn net.Conn, resp protocol.Response) {
	if err := json.NewEncoder(conn).Encode(resp); err != nil {
		d.logf("answer: %v", err)
	}
}

// Handle runs one verb. It is the whole of what a door may ask for: both doors
// reach the daemon through here and nowhere else.
func (d *Daemon) Handle(ctx context.Context, req protocol.Request) (json.RawMessage, error) {
	v, ok := verbs.ByName(req.Verb)
	if !ok {
		return nil, codes.Refusef(codes.Invalid, "no verb named %q", req.Verb)
	}
	if err := check(v, req.Args); err != nil {
		return nil, err
	}
	if err := d.pass(v, req); err != nil {
		return nil, err
	}
	return d.serve(ctx, v, req)
}

// serve runs one verb that has already been checked and passed the gate. It is
// separate from Handle because §9.3 re-runs a resolved verb here and MUST NOT
// put it through the gate again: the resolution is the decision the gate
// deferred, and a second ask would park it forever.
func (d *Daemon) serve(ctx context.Context, v verbs.Verb, req protocol.Request) (json.RawMessage, error) {
	switch v.Name {
	case "doctor":
		return encode(d.doctor(req), nil)
	case "dump":
		// One read of the whole document, not one per list: two reads would
		// let a save land between them and print a document no process ever
		// held.
		doc := d.Store.Snapshot()
		return encode(DumpReport{Version: doc.Version, Path: d.Store.Path, Document: doc}, nil)
	case "events":
		return encode(d.events(req))
	case "job.add":
		return encode(d.addJob(req))
	case "job.list":
		return encode(d.listJobs(req))
	case "job.remove":
		return encode(d.removeJob(req))
	case "job.enable":
		return encode(d.setJobEnabled(req, true))
	case "job.disable":
		return encode(d.setJobEnabled(req, false))
	case "trigger.add":
		return encode(d.addTrigger(req))
	case "trigger.list":
		return encode(d.listTriggers(req))
	case "trigger.remove":
		return encode(d.removeTrigger(req))
	case "trigger.enable":
		return encode(d.setTriggerEnabled(req, true))
	case "trigger.disable":
		return encode(d.setTriggerEnabled(req, false))
	case "parked.list":
		held := d.Store.Parked()
		return encode(ParkedReport{Parked: held, Count: len(held)}, nil)
	case "parked.resolve":
		return encode(d.resolveParked(ctx, req))
	case "stop":
		if d.halt == nil {
			return nil, codes.Refusef(codes.NotRunning, "this daemon is not serving")
		}
		d.logf("stopping: %s asked over %s", req.Caller(), door(req))
		d.stopOnce.Do(func() { close(d.halt) })
		return encode(StopReport{Stopping: true, Socket: config.SocketPath(), PID: os.Getpid()}, nil)
	}
	return nil, codes.Refusef(codes.Invalid, "verb %q is declared and not served", v.Name)
}

// StopReport is what stop answers with, before the daemon goes.
type StopReport struct {
	Stopping bool   `json:"stopping"`
	Socket   string `json:"socket"`
	PID      int    `json:"pid"`
}

// DumpReport is §5.8: the whole store in one document, with the file it is
// written to named, so a reader who wants it without this binary knows where
// to look.
type DumpReport struct {
	Version int    `json:"version"`
	Path    string `json:"path"`
	// Document is every entity and every entity's own trail, inline.
	Document store.Document `json:"store"`
}

// ParkedReport is what parked.list answers with.
type ParkedReport struct {
	Parked []store.Parked `json:"parked"`
	Count  int            `json:"count"`
}

// ParkedResolution says what became of one parked action.
type ParkedResolution struct {
	ID    string `json:"id"`
	State string `json:"state"`
	// Result is what the re-run verb answered, absent when the action was
	// refused or the verb failed.
	Result json.RawMessage `json:"result,omitempty"`
}

// DoctorReport is what doctor answers with: enough to say why something else
// would refuse, before it is tried (§10.3).
type DoctorReport struct {
	Version string `json:"version"`
	// Contract is the plugin contract THIS binary satisfies (§13.4).
	Contract string `json:"contract"`
	Plugin   string `json:"plugin"`
	// Principal is who the daemon records this very call as (§3.2, §3.7),
	// derived by the door and never declared by the caller. §7.5 rests its
	// declaration on this line being here: a doctor call through a declared
	// door answers `human` and one through an undeclared door answers `none`,
	// which is how an operator checks which of their registrations speak for
	// them.
	Principal string `json:"principal"`
	Socket    string `json:"socket"`
	// StateDir and ConfigDir are the two directories §10.3 makes doctor
	// print. An operator reading an override that is not taking effect needs
	// to know WHICH pair this daemon resolved, and the environment that
	// decides them is the daemon's, not the caller's.
	StateDir  string `json:"state_dir"`
	ConfigDir string `json:"config_dir"`
	Store     string `json:"store"`
	// Log is the file this daemon opened its log on. It is what was opened,
	// not what the state dir names now, and it is empty when the open failed
	// and the lines are going to stdout alone.
	Log string `json:"log,omitempty"`
	// Orphans is every directory a second store could be sitting in because a
	// build resolved it from Herdr's injected dirs. The store in use is never
	// listed.
	Orphans  []string       `json:"orphan_store_dirs"`
	Tick     string         `json:"tick"`
	Jobs     JobsHealth     `json:"jobs"`
	Triggers TriggersHealth `json:"triggers"`
	Config   ConfigHealth   `json:"config"`
	Gate     GateHealth     `json:"gate"`
	Events   EventsHealth   `json:"events"`
}

// ConfigHealth is where the config was read from and whether there was one.
// §10.1 fixes one config path per plugin, so a file the operator is editing
// somewhere else is a leftover, and this is where they see which path won.
type ConfigHealth struct {
	Path    string `json:"path"`
	Present bool   `json:"present"`
}

// GateHealth is the §9 policy gate as doctor reports it. §9.2 makes an
// unconfigured gate allow, which is indistinguishable at the call site from a
// configured one that allows — so whether one is configured at all is a fact
// only doctor can give.
type GateHealth struct {
	Configured bool     `json:"configured"`
	Command    []string `json:"command,omitempty"`
	// Verbs is the §9.4 list, so an operator writing a policy reads the names
	// it will be asked about off the running daemon.
	Verbs []string `json:"verbs"`
	// Parked is how many deferrals are waiting on the operator, or were
	// resolved and then failed. Both want a human.
	Parked int `json:"parked"`
}

// JobsHealth is the cron half as doctor reports it, and the one place note
// 2's "doctor says which jobs were skipped at the last start" is answered.
// The trail carries the same skips; this is what an operator sees without
// reading it.
type JobsHealth struct {
	Count   int `json:"count"`
	Enabled int `json:"enabled"`
	// SkippedAtStart is every job whose scheduled instant came round while
	// this daemon was down and was not fired. A job with catch_up is here too
	// when it stood in for more than one instant: it fired once, and the rest
	// did not happen.
	SkippedAtStart []SkipReport `json:"skipped_at_start"`
}

// TriggersHealth is the trigger half as doctor reports it, and the one place
// the inbound door's address is answered: a webhook URL is issued against a
// port, and an operator whose requests are refused needs to know which port
// this daemon actually got.
type TriggersHealth struct {
	Count   int `json:"count"`
	Enabled int `json:"enabled"`
	// Webhooks and Watches split the count by kind, because the two fail in
	// entirely different ways and an operator debugging one is not helped by
	// the other's total.
	Webhooks int `json:"webhooks"`
	Watches  int `json:"watches"`
	// Inbound is where the webhook door is listening, empty when none is.
	Inbound string `json:"inbound,omitempty"`
	// InboundError is why there is no inbound door, empty when one was asked
	// for and got its port, and empty when none was asked for at all. §9.2's
	// own problem in a different shape: a door that is off and a door that
	// failed to bind are indistinguishable at the call site.
	InboundError string `json:"inbound_error,omitempty"`
	// SecretsPath is the file the webhook keys are kept in, which is NOT the
	// store: no door that renders the store document can render a secret.
	SecretsPath string `json:"secrets_path,omitempty"`
	// OrphanSecrets is how many keys are held for a trigger that no longer
	// exists. It is a count and never a list: naming them would put trigger
	// ids in an answer for no reason, and the number is what says whether
	// anything needs cleaning.
	OrphanSecrets int `json:"orphan_secrets"`
}

// EventsHealth is the §8 trail as doctor reports it.
type EventsHealth struct {
	// Trail is how many events are held across every entity, of the Max each
	// entity's own trail keeps.
	Trail int `json:"trail"`
	Max   int `json:"max"`
	// Hook is the §8.3 command every event is handed to, empty when none is
	// configured. A hook that is configured and never fires is
	// indistinguishable from no hook at all at the call site.
	Hook []string `json:"hook,omitempty"`
}

// doctor never fails. It is what an operator runs when something else already
// refused, and a store that could not be read is the answer rather than an
// obstacle to giving one.
func (d *Daemon) doctor(req protocol.Request) DoctorReport {
	rep := DoctorReport{
		Version:   d.Version,
		Contract:  version.Contract,
		Plugin:    version.Plugin,
		Principal: req.Caller(),
		Socket:    config.SocketPath(),
		StateDir:  config.StateDir(),
		ConfigDir: config.ConfigDir(),
		Store:     config.StorePath(),
		Log:       d.LogPath,
		Orphans:   config.OrphanStoreDirs(),
		Tick:      d.interval().String(),
		Gate: GateHealth{
			Configured: d.Policy().Configured(),
			Command:    d.Policy().Command(),
			Verbs:      verbs.GatedVerbs(),
		},
		Events: EventsHealth{Max: store.MaxEvents},
	}
	if rep.Orphans == nil {
		rep.Orphans = []string{}
	}
	if d.Config != nil {
		rep.Config = ConfigHealth{Path: d.Config.Path, Present: d.Config.Present}
		rep.Events.Hook = d.Config.OnEvent
	}
	rep.Triggers.Inbound, rep.Triggers.InboundError = d.inbound.read()
	if d.Secrets != nil {
		rep.Triggers.SecretsPath = d.Secrets.Path
	}
	rep.Jobs.SkippedAtStart = d.skipped.all()
	if rep.Jobs.SkippedAtStart == nil {
		rep.Jobs.SkippedAtStart = []SkipReport{}
	}
	if d.Store != nil {
		for _, j := range d.Store.Jobs() {
			rep.Jobs.Count++
			if j.Enabled {
				rep.Jobs.Enabled++
			}
		}
		held := map[string]bool{}
		for _, t := range d.Store.Triggers() {
			held[t.ID] = true
			rep.Triggers.Count++
			if t.Enabled {
				rep.Triggers.Enabled++
			}
			if t.Kind == trigger.KindWebhook {
				rep.Triggers.Webhooks++
			} else {
				rep.Triggers.Watches++
			}
		}
		if d.Secrets != nil {
			for _, id := range d.Secrets.IDs() {
				if !held[id] {
					rep.Triggers.OrphanSecrets++
				}
			}
		}
		rep.Store = d.Store.Path
		rep.Gate.Parked = d.Store.WaitingParked()
		rep.Events.Trail = len(d.Store.Trail())
	}
	return rep
}

// Policy is the §9 gate as this daemon is configured for it. It is built per
// call from the config the daemon holds: the gate is a command name and a
// timeout, so there is nothing to keep alive between calls.
func (d *Daemon) Policy() *gate.Gate {
	if d.Config == nil {
		return gate.New(nil)
	}
	return gate.New(d.Config.GateCommand)
}

// pass is §9.1: every verb that changes the world goes through one gate before
// doing anything. A verb with no §9.4 name passes nothing, and the registry
// makes that a decision written down beside the verb rather than an omission.
//
// The subject is the caller as the daemon records it — the pane a door runs
// in, `human` for a CLI invocation or a door started with the §7.5
// declaration, and `none` for a caller with neither (§3.7). This binary derives no principal and grants nothing
// for a pane (§3.4), so what the gate is told is what the daemon knows, and
// never more.
func (d *Daemon) pass(v verbs.Verb, req protocol.Request) error {
	if v.Gated == "" {
		return nil
	}
	target := targetOf(req)
	res := d.Policy().Check(gate.Request{Subject: req.Caller(), Verb: v.Gated, Target: target})
	switch res.Decision {
	case gate.Deny:
		d.logf("the policy gate refused %s for %s: %s", v.Gated, req.Caller(), res.Reason)
		return codes.Errorf(codes.Denied, "the policy gate refused %s: %s", v.Gated, res.Reason)
	case gate.Defer:
		// §9.3: park it. The call is recorded, not performed, and the caller
		// is told DENIED with the row to name.
		now := d.now()
		p := store.Parked{
			ID:      store.NewParkedID(now),
			Subject: req.Caller(),
			Verb:    v.Gated,
			Target:  target,
			Payload: req.Args,
			// The scope travels with the call: it is not an argument, and a
			// re-run without it would act somewhere the caller never named.
			Project:     req.Project,
			AllProjects: req.AllProjects,
			State:       store.ParkedWaiting,
			Reason:      res.Reason,
			AtMS:        now.UnixMilli(),
		}
		ev := store.NewEvent(now, store.EntityParked, store.KindParked, p.ID, req.Caller(), req.Project,
			map[string]any{"verb": v.Gated, "reason": res.Reason})
		if err := d.Store.Park(p, ev); err != nil {
			return err
		}
		d.Emitted(ev)
		d.logf("the policy gate parked %s for %s as %s: %s", v.Gated, req.Caller(), p.ID, res.Reason)
		return codes.Parked(p.ID, "the policy gate parked %s for the operator: %s", v.Gated, res.Reason)
	}
	return nil
}

// targetOf is what the gate is told the call is about: the first required
// positional the verb declares, and nothing when it declares none. A gate
// asked about a verb with no target reads an empty string, which is what §9.2
// gives it.
func targetOf(req protocol.Request) string {
	v, ok := verbs.ByName(req.Verb)
	if !ok {
		return ""
	}
	for _, a := range v.Args {
		if a.Positional && a.Required {
			s, _ := req.Args[a.Name].(string)
			return s
		}
	}
	return ""
}

// resolveParked is the operator overruling the gate. §3.7 makes that advice an
// agent confirms rather than a refusal this door makes, so any caller reaches
// it — and the row records WHO, because §9.3 re-runs the verb under the
// ORIGINAL subject and the trail would otherwise name only the caller the gate
// stopped and no one who decided it could proceed.
func (d *Daemon) resolveParked(ctx context.Context, req protocol.Request) (ParkedResolution, error) {
	id, _ := req.Args["id"].(string)
	reject, _ := req.Args["reject"].(bool)
	state, kind := store.ParkedResolved, store.KindResolved
	if reject {
		state, kind = store.ParkedRefused, store.KindRefused
	}
	now := d.now()
	ev := store.NewEvent(now, store.EntityParked, kind, id, req.Caller(), req.Project, nil)
	// The move is the one-winner check and it happens BEFORE the verb runs:
	// after it, two resolves could both read the row as waiting and both run
	// the verb, with the side effect really happening twice and the loser told
	// CONFLICT for work that had already committed.
	was, err := d.Store.ClaimParked(id, state, req.Caller(), now.UnixMilli(), ev)
	if err != nil {
		return ParkedResolution{}, err
	}
	d.Emitted(ev)
	if reject {
		d.logf("%s refused the parked %s (%s)", req.Caller(), was.Verb, id)
		return ParkedResolution{ID: id, State: state}, nil
	}
	v, ok := verbs.ByGated(was.Verb)
	if !ok {
		return ParkedResolution{}, codes.Refusef(codes.Invalid,
			"parked verb %q is not a verb of this plugin", was.Verb)
	}
	// §9.3: the verb re-runs under the subject the gate stopped, never the
	// resolver's. The gate is not consulted again — the resolution IS the
	// decision it deferred, and asking a second time would park it forever.
	rerun := protocol.Request{
		Verb: v.Name, Args: was.Payload, Door: req.Door,
		Project: was.Project, AllProjects: was.AllProjects,
	}
	// The subject is carried back in the form a door would have derived it: a
	// pane for an agent, the human act behind the process for the operator,
	// and an explicit --as for the principals §3.2 lets a call declare. `none`
	// is carried back by carrying nothing, which is what it means.
	if pane, found := strings.CutPrefix(was.Subject, "agent:"); found {
		rerun.Pane = pane
	} else if was.Subject == "human" {
		// §3.7: `human` with no pane is reproducible only by a process that
		// speaks for the operator, which is what parked the action.
		rerun.Operator = true
	} else if was.Subject != "none" {
		rerun.As = was.Subject
	}
	if rerun.Args == nil {
		rerun.Args = map[string]any{}
	}
	d.logf("%s resolved the parked %s (%s), re-running it as %s", req.Caller(), was.Verb, id, was.Subject)
	out, err := d.serve(ctx, v, rerun)
	if err != nil {
		// The decision stands; the verb did not run. Say why, in the verb's
		// own words, and leave the row saying so.
		failed := store.NewEvent(d.now(), store.EntityParked, store.KindFailed, id, req.Caller(), req.Project,
			map[string]any{"error": codes.Message(err)})
		if ferr := d.Store.FailParked(id, codes.Message(err), failed); ferr != nil {
			d.logf("recording that the parked %s failed: %v", id, ferr)
		} else {
			d.Emitted(failed)
		}
		return ParkedResolution{}, err
	}
	return ParkedResolution{ID: id, State: state, Result: out}, nil
}

// check refuses a request the verb table does not describe: a required
// argument missing, an argument of the wrong kind, or one the verb never
// declared. A door that sends an argument the daemon drops silently is how a
// caller ends up believing something it asked for happened.
func check(v verbs.Verb, args map[string]any) error {
	declared := make(map[string]verbs.Arg, len(v.Args))
	for _, a := range v.Args {
		declared[a.Name] = a
	}
	for name := range args {
		if _, ok := declared[name]; !ok {
			return codes.Refusef(codes.Invalid, "%s takes no argument named %q", v.Name, name)
		}
	}
	for _, a := range v.Args {
		raw, ok := args[a.Name]
		if !ok || raw == nil {
			if a.Required {
				return codes.Refusef(codes.Invalid, "%s needs %s", v.Name, a.Name)
			}
			continue
		}
		if err := CheckArg(v, a, raw); err != nil {
			return err
		}
	}
	return nil
}

// CheckArg holds one argument to the type the registry declares for it. Both
// doors walk the same table, so it is one function rather than the same switch
// written twice; it is exported because the MCP door publishes the schema this
// checks against and so must check the same way.
func CheckArg(v verbs.Verb, a verbs.Arg, raw any) error {
	switch a.Type {
	case verbs.Bool:
		if _, ok := raw.(bool); !ok {
			return codes.Refusef(codes.Invalid, "%s wants %s as true or false", v.Name, a.Name)
		}
	case verbs.Int:
		// A JSON number arrives as a float64 whichever door sent it, and a
		// count with a fraction on it is not a count.
		n, ok := raw.(float64)
		if !ok {
			if i, isInt := raw.(int); isInt {
				n, ok = float64(i), true
			}
		}
		if !ok || n != float64(int(n)) {
			return codes.Refusef(codes.Invalid, "%s wants %s as a whole number", v.Name, a.Name)
		}
	case verbs.Object:
		if _, ok := raw.(map[string]any); !ok {
			return codes.Refusef(codes.Invalid, "%s wants %s as an object of named values", v.Name, a.Name)
		}
	default:
		s, ok := raw.(string)
		if !ok {
			return codes.Refusef(codes.Invalid, "%s wants %s as a string", v.Name, a.Name)
		}
		if a.Required && s == "" {
			return codes.Refusef(codes.Invalid, "%s needs %s", v.Name, a.Name)
		}
	}
	return nil
}

func encode(v any, err error) (json.RawMessage, error) {
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, codes.Errorf(codes.Unexpected, "render the answer: %v", err)
	}
	return b, nil
}

func door(req protocol.Request) string {
	if req.Door == "" {
		return "an unnamed door"
	}
	return "the " + req.Door
}

func (d *Daemon) logf(format string, args ...any) {
	if d.Log != nil {
		d.Log.Printf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// String names this daemon in a log line, for a message that has one.
func (d *Daemon) String() string { return fmt.Sprintf("hsched %s", d.Version) }
