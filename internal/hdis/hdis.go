// Package hdis is the dispatcher adapter: the only place in this plugin that
// reaches hdis. A signal that brings a worker up for a ready task goes
// through here.
package hdis

import (
	"context"
	"fmt"

	"github.com/husniadil/herdr-sched/internal/sibling"
)

// Client dispatches as one firing signal.
type Client struct {
	// Bin overrides the binary resolved off PATH.
	Bin string
	// Principal is the firing signal, `cron:<job>` or `trigger:<id>`.
	Principal string
}

// Reservation is what an accepted dispatch answers with. It is a promise that
// the dispatcher's next tick brings a worker up, never a report that one is
// already running: a spawn runs to minutes and the call does not wait for it.
type Reservation struct {
	TaskID  string `json:"task"`
	Seq     int    `json:"seq"`
	Title   string `json:"title"`
	Project string `json:"project"`
}

// Dispatch reserves one ready task. An empty project leaves the dispatcher on
// every board, which is its own default (§4.2).
func (c *Client) Dispatch(ctx context.Context, task, project string) (Reservation, error) {
	args := []string{"dispatch", task}
	if project != "" {
		args = append(args, "--project", project)
	}
	var res Reservation
	client := &sibling.Client{Name: "hdis", Bin: c.Bin, Principal: c.Principal}
	if err := client.JSON(ctx, &res, args...); err != nil {
		return Reservation{}, err
	}
	if res.TaskID == "" {
		return Reservation{}, fmt.Errorf("hdis dispatch %s: no reservation in the response", task)
	}
	return res, nil
}
