// Package htask is the board adapter: the only place in this plugin that
// reaches htask. A signal that files a task goes through here, and the board
// row it gets back is read once and not kept — the board is the writer of its
// own store.
package htask

import (
	"context"
	"fmt"
	"strconv"

	"github.com/husniadil/herdr-sched/internal/sibling"
)

// Client files tasks on the board as one firing signal.
type Client struct {
	// Bin overrides the binary resolved off PATH.
	Bin string
	// Principal is the firing signal, `cron:<job>` or `trigger:<id>`.
	Principal string
}

// Draft is the task a signal files, as the action's arguments name it.
type Draft struct {
	Title       string
	Description string
	Project     string
	Priority    int
}

// Task is the slice of the board's answer this plugin records on the run.
type Task struct {
	ID      string `json:"id"`
	Seq     int    `json:"seq"`
	Project string `json:"project"`
	Title   string `json:"title"`
	Status  string `json:"status"`
}

// Create files one task and answers with the row the board made.
func (c *Client) Create(ctx context.Context, d Draft) (Task, error) {
	args := []string{"task", "create", d.Title}
	if d.Description != "" {
		args = append(args, "--description", d.Description)
	}
	if d.Project != "" {
		args = append(args, "--project", d.Project)
	}
	if d.Priority != 0 {
		args = append(args, "--priority", strconv.Itoa(d.Priority))
	}
	var res struct {
		Task Task `json:"task"`
	}
	if err := c.client().JSON(ctx, &res, args...); err != nil {
		return Task{}, err
	}
	if res.Task.ID == "" {
		return Task{}, fmt.Errorf("htask task create %s: no task in the response", d.Title)
	}
	return res.Task, nil
}

func (c *Client) client() *sibling.Client {
	return &sibling.Client{Name: "htask", Bin: c.Bin, Principal: c.Principal}
}
