// Package fire is what happens when a signal goes off: it turns one validated
// action into one call, and records the run.
//
// The run history is the §8 trail and nothing else (note 2): every firing
// lands as a `sched.run.fired` or a `sched.run.failed` event on the run
// trail, whether it reached what it was aimed at or not. A sibling that is
// unreachable is a loud failed run — never a silent skip, because a schedule
// that quietly stopped working is indistinguishable from one that had nothing
// to do.
//
// Nothing here schedules anything. A job and a trigger arrive later and both
// call Fire.
package fire

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/husniadil/herdr-sched/internal/action"
	"github.com/husniadil/herdr-sched/internal/hdis"
	"github.com/husniadil/herdr-sched/internal/hmail"
	"github.com/husniadil/herdr-sched/internal/htask"
	"github.com/husniadil/herdr-sched/internal/shellact"
	"github.com/husniadil/herdr-sched/internal/sibling"
	"github.com/husniadil/herdr-sched/internal/store"
)

// Runner fires actions and writes what happened to the store.
type Runner struct {
	// Store is where a run is recorded. The daemon is the only writer of it.
	Store *store.Store
	// Now is the clock, so a test can hold it still.
	Now func() time.Time
	// HTaskBin, HMailBin and HDisBin override the binaries resolved off
	// PATH. They are empty in production and a test's stand-in otherwise.
	HTaskBin, HMailBin, HDisBin string
	// Emit hands each run to whatever else wants it: the live `events
	// --follow` subscriptions and the §8.3 hook. Without it a run would reach
	// the store and no follower, and an event written in one place and not
	// the other is the one thing that split cannot be allowed to be.
	Emit func(store.Event)

	// detached counts the shell actions still running, so a caller that
	// needs to see their runs on the trail can wait for them.
	detached sync.WaitGroup
}

// Fire performs one action on behalf of one signal.
//
// It answers with the failure of anything that ran INSIDE the call, so a tick
// can log it. A shell action is detached from the tick (note 2) and its
// outcome therefore arrives on the trail rather than here: Fire answers nil
// once the command has started, and the run lands when it ends.
//
// project is the scope the ROW was written in, and it is where the action
// lands unless the action names one of its own (§4.2). Without it a sibling
// resolves the project from this daemon's own working directory, which is one
// board for every schedule this user has and never the one the operator wrote
// the row in.
func (r *Runner) Fire(ctx context.Context, src action.Source, a action.Action, project string) error {
	// Both are create-time checks, re-run here rather than trusted: the row
	// they were checked on lives in a JSON document an operator can edit.
	if err := src.Validate(); err != nil {
		return err
	}
	if err := a.Validate(); err != nil {
		return err
	}
	if a.Kind == action.KindShell {
		return r.shell(ctx, src, a, project)
	}
	detail, err := r.call(ctx, src, a, project)
	if err != nil {
		return r.record(src, a, project, store.KindFailed, failure(detail, err))
	}
	return r.record(src, a, project, store.KindFired, detail)
}

// Wait blocks until every detached shell action has finished and its run is
// on the trail. It is what a shutdown and a test both need; nothing on the
// tick path calls it.
func (r *Runner) Wait() { r.detached.Wait() }

// call performs the one action that reaches a sibling, and answers with what
// the run should say about it.
func (r *Runner) call(ctx context.Context, src action.Source, a action.Action, project string) (map[string]any, error) {
	as := src.Principal()
	// The action's own project wins where it names one: an operator who wrote
	// a schedule to file into another project meant it.
	if p := a.Arg("project"); p != "" {
		project = p
	}
	switch a.Kind {
	case action.KindTask:
		task, err := (&htask.Client{Bin: r.HTaskBin, Principal: as}).Create(ctx, htask.Draft{
			Title:       a.Arg("title"),
			Description: a.Arg("description"),
			Project:     project,
			Priority:    atoi(a.Arg("priority")),
		})
		if err != nil {
			return map[string]any{"title": a.Arg("title")}, err
		}
		return map[string]any{"task": task.ID, "seq": task.Seq, "title": task.Title}, nil
	case action.KindMail:
		client := &hmail.Client{Bin: r.HMailBin, Principal: as}
		draft := hmail.Draft{To: a.Arg("to"), Body: a.Arg("body"), Project: project}
		post := client.Send
		if a.Ask() {
			post = client.Ask
		}
		m, err := post(ctx, draft)
		if err != nil {
			return map[string]any{"to": draft.To}, err
		}
		return map[string]any{"message": m.ID, "to": m.To, "mail_kind": m.Kind}, nil
	case action.KindDispatch:
		res, err := (&hdis.Client{Bin: r.HDisBin, Principal: as}).Dispatch(ctx, a.Arg("task"), project)
		if err != nil {
			return map[string]any{"task": a.Arg("task")}, err
		}
		return map[string]any{"task": res.TaskID, "seq": res.Seq, "title": res.Title}, nil
	}
	// Validate has already refused every kind but the four, so this is a
	// kind added to the vocabulary and not to the fire path.
	return nil, errors.New("no adapter fires a " + a.Kind + " action")
}

// shell starts the command and hands the tick back. The run is recorded by
// the goroutine that waits for it, which is the whole point of detaching:
// a slow command must not hold up every other schedule.
func (r *Runner) shell(ctx context.Context, src action.Source, a action.Action, project string) error {
	run, err := shellact.Start(ctx, shellact.Command{Line: a.Arg("command"), Dir: a.Arg("dir")})
	if err != nil {
		return r.record(src, a, project, store.KindFailed, map[string]any{
			"command": a.Arg("command"), "error": err.Error(),
		})
	}
	r.detached.Add(1)
	go func() {
		defer r.detached.Done()
		res := <-run.Done
		detail := map[string]any{"command": a.Arg("command"), "exit": res.Exit, "output": res.Output}
		kind := store.KindFired
		if res.Err != nil {
			kind, detail["error"] = store.KindFailed, res.Err.Error()
		}
		// Nothing above this can report a failure to a caller that has
		// already been handed back its tick, so the store is the only
		// place left to be loud in.
		_ = r.record(src, a, project, kind, detail)
	}()
	return nil
}

// record writes one run to the trail, and answers with the failure the run
// carries so a caller sees the same thing the operator will.
func (r *Runner) record(src action.Source, a action.Action, project, kind string, detail map[string]any) error {
	if detail == nil {
		detail = map[string]any{}
	}
	detail["action"] = a.Kind
	ev := store.NewEvent(r.now(), store.EntityRun, kind, src.ID, src.Principal(), project, detail)
	if err := r.Store.RecordRun(ev); err != nil {
		return err
	}
	if r.Emit != nil {
		r.Emit(ev)
	}
	if kind == store.KindFailed {
		if reason, ok := detail["error"].(string); ok {
			return errors.New(reason)
		}
	}
	return nil
}

// failure is the run's detail with the sibling's own words on it, and its
// §6.3 code when the sibling answered with one: the operator reads why
// without a second lookup, and a call that never reached a sibling carries no
// code, which is the difference between a refusal and a door that could not
// answer.
func failure(detail map[string]any, err error) map[string]any {
	if detail == nil {
		detail = map[string]any{}
	}
	detail["error"] = err.Error()
	var refusal *sibling.Refusal
	if errors.As(err, &refusal) {
		detail["code"] = refusal.Code
		detail["sibling"] = refusal.Sibling
	}
	return detail
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// atoi reads a priority that Validate has already accepted as a whole
// number; an absent one is zero, which is what the board's own default is.
func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
