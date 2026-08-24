// Package client is the door side of the socket: dial the daemon, start it
// when nothing is listening, send one request and read one answer. Both doors
// reach the daemon through here and hold nothing of their own.
package client

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/husniadil/herdr-sched/internal/codes"
	"github.com/husniadil/herdr-sched/internal/config"
	"github.com/husniadil/herdr-sched/internal/protocol"
)

// StartTimeout bounds the wait for a daemon this client had to start. An
// invocation that finds no live socket starts one and waits for it, bounded,
// rather than fail.
const StartTimeout = 3 * time.Second

// Client dials the daemon and starts one when none answers.
type Client struct {
	// Bin is the binary to start a daemon from; empty means this one.
	Bin string
	// Timeout bounds the wait for a daemon this client started; zero means
	// StartTimeout.
	Timeout time.Duration
	// NoStart refuses with NOT_RUNNING when nothing is listening, rather than
	// starting a daemon. `stop` is what it is for.
	NoStart bool
	// Started is the daemon this client had to bring up, if it brought one
	// up. Nothing here stops it again: it outlives the door on purpose.
	Started *os.Process
}

// Call sends one request and returns the daemon's result.
func (c *Client) Call(req protocol.Request) (json.RawMessage, error) {
	conn, err := c.dialOrStart()
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, codes.Errorf(codes.Unavailable, "send %s: %v", req.Verb, err)
	}
	var resp protocol.Response
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&resp); err != nil {
		return nil, codes.Errorf(codes.Unavailable, "read the answer to %s: %v", req.Verb, err)
	}
	if resp.Error != nil {
		return nil, &codes.Error{
			Code:     codes.Code(resp.Error.Code),
			Message:  resp.Error.Message,
			ParkedID: resp.Error.ParkedID,
		}
	}
	return resp.Result, nil
}

// Stream sends one request and hands every answer to fn until the daemon says
// the stream is over or fn returns an error. This is `events --follow` (§8.2),
// and it is the one call with no single answer to wait for.
func (c *Client) Stream(req protocol.Request, fn func(json.RawMessage) error) error {
	conn, err := c.dialOrStart()
	if err != nil {
		return err
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return codes.Errorf(codes.Unavailable, "send %s: %v", req.Verb, err)
	}
	dec := json.NewDecoder(bufio.NewReader(conn))
	for {
		var resp protocol.Response
		if err := dec.Decode(&resp); err != nil {
			// A closed socket is not an ending. The daemon says when a stream
			// is over; anything else that stops the decoder is the daemon
			// going away underneath, and reporting that as a clean finish
			// would have a follower exit having silently stopped watching.
			return codes.Errorf(codes.Unavailable,
				"the daemon stopped streaming %s without saying it had finished: %v", req.Verb, err)
		}
		if resp.Error != nil {
			return &codes.Error{Code: codes.Code(resp.Error.Code), Message: resp.Error.Message}
		}
		if resp.Done {
			return nil
		}
		if err := fn(resp.Result); err != nil {
			return err
		}
	}
}

func (c *Client) dialOrStart() (net.Conn, error) {
	path := config.SocketPath()
	if conn, err := net.Dial("unix", path); err == nil {
		return conn, nil
	}
	if c.NoStart {
		return nil, codes.Refusef(codes.NotRunning, "no hsched daemon is listening on %s", path)
	}
	if err := c.start(); err != nil {
		return nil, err
	}

	// The daemon has to open its store dir, take its lock and bind before it
	// can answer. Backing off rather than spinning keeps a slow machine from
	// being the reason this fails.
	deadline := time.Now().Add(c.timeout())
	for wait := 20 * time.Millisecond; ; wait *= 2 {
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(wait)
		if conn, err := net.Dial("unix", path); err == nil {
			return conn, nil
		}
	}
	return nil, codes.Errorf(codes.Unavailable,
		"started a daemon and none answered on %s within %s", path, c.timeout())
}

// start brings a daemon up, detached: it outlives the door that started it,
// and its log goes to a file because it has no terminal to write to.
func (c *Client) start() error {
	bin := c.Bin
	if bin == "" {
		exe, err := os.Executable()
		if err != nil {
			return codes.Errorf(codes.Unavailable,
				"no daemon is running and this binary cannot name itself: %v", err)
		}
		bin = exe
	}
	if err := config.EnsureStateDir(); err != nil {
		return err
	}
	logFile, err := os.OpenFile(config.LogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return codes.Errorf(codes.Unavailable, "open the daemon log %s: %v", config.LogPath(), err)
	}
	defer logFile.Close()

	cmd := exec.Command(bin, "daemon")
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// Its own session, so closing the pane that started it does not take it
	// with them.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return codes.Errorf(codes.Unavailable, "start a daemon from %s: %v", bin, err)
	}
	c.Started = cmd.Process
	// Nothing waits for it, so let the kernel reap it rather than leaving a
	// zombie behind this door.
	go cmd.Wait()
	return nil
}

func (c *Client) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return StartTimeout
}
