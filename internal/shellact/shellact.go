// Package shellact runs the one action that reaches no sibling: a command on
// the host.
//
// It runs DETACHED from the tick (note 2). A schedule that shells out to
// something slow must not hold up every other schedule, so Start spawns the
// command and hands back a handle; the run's outcome arrives on a channel and
// is what the fire path records on the trail. Nothing here is queued for
// later and nothing is retried: a command that failed is a failed run the
// operator reads, not a promise this daemon keeps.
package shellact

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"

	"github.com/husniadil/herdr-sched/internal/codes"
	"github.com/husniadil/herdr-sched/internal/sibling"
)

// MaxOutput is how much of a command's output a run keeps. The store is one
// document written whole on every save, so an unbounded capture makes every
// later save carry it; what is kept is the head, because the first thing a
// command says is usually what went wrong.
const MaxOutput = 8192

// Command is the shell action as its arguments name it.
type Command struct {
	// Line is the command, run through /bin/sh -c the way an operator would
	// type it: a schedule is written as a line, not as an argv.
	Line string
	// Dir is where to run it; empty is this process's working directory.
	Dir string
}

// Result is how the command ended.
type Result struct {
	// Exit is the process status, -1 when it never ran as a process.
	Exit int
	// Output is stdout and stderr interleaved, bounded to MaxOutput.
	Output string
	// Err is non-nil when the command did not succeed, and carries the
	// status so the trail says what happened without a second lookup.
	Err error
}

// Run is a command in flight.
type Run struct {
	// Done carries the one result and is then closed.
	Done <-chan Result
}

// Start spawns the command and returns without waiting for it.
func Start(ctx context.Context, c Command) (*Run, error) {
	if c.Dir != "" {
		info, err := os.Stat(c.Dir)
		if err != nil || !info.IsDir() {
			return nil, codes.Errorf(codes.Usage,
				"a shell action cannot run in %s: it is not a directory this host has", c.Dir)
		}
	}
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", c.Line)
	cmd.Dir = c.Dir
	// The same scrub every sibling call gets, and for the same reason: a
	// command that shells out to htask or hmail would otherwise arrive
	// carrying this daemon's pane instead of the signal that fired it.
	cmd.Env = sibling.EnvWithoutPane(os.Environ())
	var out lockedBuffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		return nil, codes.Errorf(codes.Unavailable, "a shell action could not start %q: %v", c.Line, err)
	}
	done := make(chan Result, 1)
	go func() {
		defer close(done)
		err := cmd.Wait()
		status := cmd.ProcessState.ExitCode()
		res := Result{Exit: status, Output: bound(out.String())}
		if err != nil {
			res.Err = fmt.Errorf("the command exited %d: %q", status, c.Line)
		}
		done <- res
	}()
	return &Run{Done: done}, nil
}

// bound is the output a run keeps, with a line saying so when it was cut: an
// output silently truncated reads as a command that printed less than it did.
func bound(s string) string {
	if len(s) <= MaxOutput {
		return s
	}
	return s[:MaxOutput] + fmt.Sprintf("\n[truncated: the command printed %d bytes and a run keeps %d]\n", len(s), MaxOutput)
}

// lockedBuffer is one buffer for both streams. The two pipes are written from
// two goroutines, so the writes are serialised here rather than interleaved
// mid-line.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
