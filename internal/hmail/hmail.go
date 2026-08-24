// Package hmail is the mailbox adapter: the only place in this plugin that
// reaches hmail. A signal that notifies or asks goes through here.
//
// The two verbs are one adapter because everything but the obligation is the
// same call: a notify owes nothing back, an ask owes a correlated reply that
// never expires and blocks nothing.
package hmail

import (
	"context"
	"fmt"

	"github.com/husniadil/herdr-sched/internal/sibling"
)

// Client posts mail as one firing signal.
type Client struct {
	// Bin overrides the binary resolved off PATH.
	Bin string
	// Principal is the firing signal, `cron:<job>` or `trigger:<id>`.
	Principal string
}

// Draft is the message a signal posts.
type Draft struct {
	To      string
	Body    string
	Project string
}

// Message is the slice of the mailbox's answer this plugin records.
type Message struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	To      string `json:"to"`
	Project string `json:"project"`
}

// Send posts a notify: information to note, with nothing owed back.
func (c *Client) Send(ctx context.Context, d Draft) (Message, error) {
	return c.post(ctx, "send", d)
}

// Ask posts an ask, which owes a correlated reply from the recipient.
func (c *Client) Ask(ctx context.Context, d Draft) (Message, error) {
	return c.post(ctx, "ask", d)
}

func (c *Client) post(ctx context.Context, verb string, d Draft) (Message, error) {
	args := []string{verb, d.To, d.Body}
	if d.Project != "" {
		args = append(args, "--project", d.Project)
	}
	var res struct {
		Message Message `json:"message"`
	}
	client := &sibling.Client{Name: "hmail", Bin: c.Bin, Principal: c.Principal}
	if err := client.JSON(ctx, &res, args...); err != nil {
		return Message{}, err
	}
	if res.Message.ID == "" {
		return Message{}, fmt.Errorf("hmail %s %s: no message in the response", verb, d.To)
	}
	return res.Message, nil
}
