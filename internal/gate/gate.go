// Package gate is the policy check every world-changing verb passes through
// (§9). It is configured, not built in: unconfigured allows, and any failure
// to get a well-formed answer denies. The gate fails closed.
package gate

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"time"
)

// Decision is the gate's answer (§9.1).
type Decision string

const (
	Allow Decision = "allow"
	Deny  Decision = "deny"
	Defer Decision = "defer"
)

// Request is what the gate command reads on stdin (§9.2).
type Request struct {
	Subject string `json:"subject"`
	Verb    string `json:"verb"`
	Target  string `json:"target"`
}

// Result is the gate's answer plus why.
type Result struct {
	Decision Decision `json:"decision"`
	Reason   string   `json:"reason,omitempty"`
}

// maxAnswer bounds what the gate command may print. An answer larger than this
// is not an answer; §9.2 calls oversized a failure, and a failure is deny.
const maxAnswer = 64 * 1024

// DefaultTimeout bounds the wait. A gate that does not answer has not allowed
// anything.
const DefaultTimeout = 5 * time.Second

// Gate checks a subject against a verb and a target.
type Gate struct {
	command []string
	// Timeout bounds one gate run. Zero means DefaultTimeout.
	Timeout time.Duration
}

// New builds a gate from the configured command. A nil or empty command is the
// unconfigured gate, which allows (§9.2).
func New(command []string) *Gate { return &Gate{command: command} }

// Configured reports whether a gate command is set, for doctor (§10.3).
func (g *Gate) Configured() bool { return len(g.command) > 0 }

// Command is the configured argv, for doctor.
func (g *Gate) Command() []string { return g.command }

// Check runs the gate.
func (g *Gate) Check(req Request) Result {
	if len(g.command) == 0 {
		return Result{Decision: Allow}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return denied("cannot render the gate request: " + err.Error())
	}
	limit := g.Timeout
	if limit <= 0 {
		limit = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, g.command[0], g.command[1:]...)
	cmd.Stdin = bytes.NewReader(body)
	var out bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &out, left: maxAnswer + 1}
	cmd.Stderr = io.Discard
	// A killed gate command can leave a grandchild holding the stdout pipe,
	// which would make Run outlive the timeout it exists to enforce. WaitDelay
	// closes the pipe shortly after the kill so a hang is bounded in fact and
	// not only in intent.
	cmd.WaitDelay = time.Second
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return denied("the gate command did not answer within " + limit.String())
	}
	if runErr != nil {
		return denied("the gate command failed: " + runErr.Error())
	}
	if out.Len() > maxAnswer {
		return denied("the gate command printed more than the answer limit")
	}
	var res Result
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &res); err != nil {
		return denied("the gate command printed no readable decision")
	}
	switch res.Decision {
	case Allow, Deny, Defer:
		return res
	default:
		return denied("the gate command answered with an unknown decision: " + string(res.Decision))
	}
}

func denied(reason string) Result { return Result{Decision: Deny, Reason: reason} }

// limitedWriter stops reading a runaway gate rather than buffering it all.
type limitedWriter struct {
	w    *bytes.Buffer
	left int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.left <= 0 {
		return len(p), nil
	}
	if len(p) > l.left {
		l.w.Write(p[:l.left])
		l.left = 0
		return len(p), nil
	}
	l.w.Write(p)
	l.left -= len(p)
	return len(p), nil
}
