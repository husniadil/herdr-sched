// Package sibling is how this plugin reaches the other Herdr plugins: it
// spawns their CLIs with --json and reads what they answer.
//
// It never opens another plugin's socket and never reads another plugin's
// file (note 2, following hdis's precedent): each daemon stays the only
// writer of its own store, and a call through a published CLI is the one
// surface a sibling promises to keep.
//
// One spawn site, here, for every sibling and every verb. That is what makes
// two things true for verbs nobody has written yet: every call declares the
// signal it fired from with --as (§3.2), and no call inherits the daemon's
// pane.
package sibling

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
)

// Client runs one sibling binary as one principal.
type Client struct {
	// Name is the binary: htask, hmail or hdis. It is also what a failure
	// is reported under, so an operator reads which sibling refused.
	Name string
	// Bin overrides the binary resolved off PATH, which is what a test
	// pointing at a stand-in uses.
	Bin string
	// Principal is what the call declares itself as, and is passed to --as
	// on every call. It is the firing signal's §3.2 principal.
	Principal string
}

// Refusal is a sibling answering that it will not do something, in its own
// words. Every sibling writes the §6.2 error envelope on stdout with a
// non-zero exit; a call that fails without one never reached a sibling that
// answered, and carries no Refusal.
type Refusal struct {
	Sibling string
	Code    string
	Message string
}

func (r *Refusal) Error() string { return r.Sibling + " refused, " + r.Code + ": " + r.Message }

func (c *Client) bin() string {
	if c.Bin != "" {
		return c.Bin
	}
	return c.Name
}

// JSON runs the call and reads its answer into into.
func (c *Client) JSON(ctx context.Context, into any, args ...string) error {
	out, err := c.Run(ctx, args...)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(out, into); err != nil {
		return fmt.Errorf("%s %s: unreadable json: %w", c.Name, strings.Join(args, " "), err)
	}
	return nil
}

// Run spawns the sibling and hands back its stdout. --json and --as are
// appended here rather than by a caller, so no verb can be added that answers
// in prose or arrives unattributed.
func (c *Client) Run(ctx context.Context, args ...string) ([]byte, error) {
	args = append(append([]string{}, args...), "--json", "--as", c.Principal)
	cmd := exec.CommandContext(ctx, c.bin(), args...)
	cmd.Env = EnvWithoutPane(os.Environ())
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		line := c.Name + " " + strings.Join(args, " ")
		if refusal, ok := refusalIn(c.Name, stdout.Bytes()); ok {
			return nil, fmt.Errorf("%s: %w", line, refusal)
		}
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%s: %s", line, msg)
		}
		return nil, fmt.Errorf("%s: %w", line, err)
	}
	return stdout.Bytes(), nil
}

// paneNames are the variables a sibling reads a caller's own position out of.
// They travel in the environment rather than in argv, so a child that
// inherits this daemon's environment arrives claiming to be this daemon's
// pane no matter what --as declares.
//
// The first three are the pane. HERDR_PLUGIN_CONTEXT_JSON is the project: a
// sibling resolves its board or mailbox from that document's focused-pane cwd
// before it falls back to the working directory (§4.2), and Herdr fills it in
// for the commands it spawns itself, this plugin's [[startup]] among them. A
// daemon started that way would scope every sibling call to whatever the
// operator happened to be looking at, and a board scoped somewhere else looks
// exactly like a board with nothing on it.
var paneNames = []string{
	"HERDR_PANE_ID", "HERDR_TAB_ID", "HERDR_WORKSPACE_ID", "HERDR_PLUGIN_CONTEXT_JSON",
}

// EnvWithoutPane is the daemon's environment minus where it happens to be
// running. A call here declares a signal principal, and that is what it
// declares INSTEAD of a pane: hmail in particular stamps the sender from the
// pane the call came out of, so a fired notify carrying this daemon's pane
// would be attributed to the daemon rather than to the schedule.
func EnvWithoutPane(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if slices.Contains(paneNames, name) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func refusalIn(name string, out []byte) (*Refusal, bool) {
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(out, &body); err != nil || body.Error.Code == "" {
		return nil, false
	}
	return &Refusal{Sibling: name, Code: body.Error.Code, Message: body.Error.Message}, true
}
