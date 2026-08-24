// Package cli is the CLI door: it turns an argv into the same request the MCP
// door builds, sends it to the daemon, and prints what comes back. It holds no
// state, and it decides nothing the daemon has not already decided.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/husniadil/herdr-sched/internal/client"
	"github.com/husniadil/herdr-sched/internal/codes"
	"github.com/husniadil/herdr-sched/internal/daemon"
	"github.com/husniadil/herdr-sched/internal/store"
)

// Door names this surface in the daemon's log.
const Door = "cli"

// WantsJSON reads --json out of a raw argv, wherever in it the caller wrote
// the flag. It has to be known BEFORE cobra parses, because cobra's own parse
// failures are among the failures §6.2 makes answer with one document, and at
// that moment the flag exists only in argv. A value that is not an explicit
// false counts as asking for a document: cobra will refuse a bad value itself,
// and a machine caller that asked for JSON should be told in JSON.
func WantsJSON(argv []string) bool {
	on := false
	for _, a := range argv {
		switch {
		case a == "--":
			return on
		case a == "--json":
			on = true
		case strings.HasPrefix(a, "--json="):
			_, v, _ := strings.Cut(a, "=")
			b, err := strconv.ParseBool(v)
			on = err != nil || b
		}
	}
	return on
}

// WriteError prints the §6.2 failure document: with --json, one envelope on
// stdout carrying the contract code and the message; otherwise nothing here,
// because a human reads the sentence on stderr instead. It is the same
// document the MCP door builds for the same failure.
func WriteError(err error, out io.Writer) error {
	envelope := map[string]string{"code": string(codes.Of(err)), "message": codes.Message(err)}
	// §9.3: a DENIED the gate deferred names the row the operator resolves.
	if id := codes.ParkedOf(err); id != "" {
		envelope["parked_id"] = id
	}
	body, merr := json.Marshal(map[string]any{"error": envelope})
	if merr != nil {
		return merr
	}
	_, werr := fmt.Fprintln(out, string(body))
	return werr
}

// Send performs one parsed call: it asks the daemon and writes the answer. It
// is the Runner the binary hands Root, and the only place in this door that
// opens the socket.
func Send(c Call) error {
	cl := &client.Client{NoStart: c.Verb.NoAutostart}
	if c.Req.Follow {
		// A stream has no single answer to print, so each event is written as
		// it arrives and the call returns when the daemon says the stream is
		// over or the caller goes away.
		return cl.Stream(c.Req, func(raw json.RawMessage) error {
			return WriteEvent(raw, c.AsJSON, os.Stdout)
		})
	}
	result, err := cl.Call(c.Req)
	if err != nil {
		return err
	}
	return Write(c.Verb.Name, result, c.AsJSON, os.Stdout)
}

// Write prints one answer: as it came when the caller asked for that, and as a
// line an operator reads otherwise. The JSON is the daemon's own bytes, which
// is the same document the MCP door hands its caller.
func Write(verb string, result json.RawMessage, asJSON bool, out io.Writer) error {
	if asJSON {
		_, err := fmt.Fprintln(out, string(result))
		return err
	}
	switch verb {
	case "doctor":
		var rep daemon.DoctorReport
		if err := json.Unmarshal(result, &rep); err != nil {
			return err
		}
		fmt.Fprintf(out, "hsched %s on %s\n", rep.Version, rep.Socket)
		fmt.Fprintf(out, "  contract    %s satisfied by this plugin\n", rep.Contract)
		fmt.Fprintf(out, "  state dir   %s\n", rep.StateDir)
		fmt.Fprintf(out, "  config dir  %s\n", rep.ConfigDir)
		fmt.Fprintf(out, "  config      %s\n", configLine(rep.Config))
		fmt.Fprintf(out, "  store       %s\n", rep.Store)
		fmt.Fprintf(out, "  log         %s\n", or(rep.Log, "stdout only: no file could be opened"))
		fmt.Fprintf(out, "  tick        every %s\n", rep.Tick)
		fmt.Fprintf(out, "  events      %d held, %d kept per entity, hook %s\n",
			rep.Events.Trail, rep.Events.Max, or(strings.Join(rep.Events.Hook, " "), "not configured"))
		fmt.Fprintf(out, "  gate        %s\n", gateLine(rep.Gate))
		for _, dir := range rep.Orphans {
			// Named, never removed: a store this daemon is not using may still
			// be an operator's own data.
			fmt.Fprintf(out, "  orphan?     a store left by another build could be under %s\n", dir)
		}
	case "stop":
		var rep daemon.StopReport
		if err := json.Unmarshal(result, &rep); err != nil {
			return err
		}
		fmt.Fprintf(out, "the daemon on %s (pid %d) is stopping\n", rep.Socket, rep.PID)
	case "dump":
		var rep daemon.DumpReport
		if err := json.Unmarshal(result, &rep); err != nil {
			return err
		}
		fmt.Fprintf(out, "store version %d at %s\n", rep.Version, or(rep.Path, "memory only"))
		fmt.Fprintf(out, "  parked        %d rows, %d events\n",
			len(rep.Document.Parked), len(rep.Document.ParkedEvents))
	case "events":
		var rep daemon.EventsReport
		if err := json.Unmarshal(result, &rep); err != nil {
			return err
		}
		if rep.Count == 0 {
			fmt.Fprintln(out, "the trail holds nothing for that read")
			return nil
		}
		for _, ev := range rep.Events {
			if err := WriteEvent(mustJSON(ev), false, out); err != nil {
				return err
			}
		}
	case "parked.list":
		var rep daemon.ParkedReport
		if err := json.Unmarshal(result, &rep); err != nil {
			return err
		}
		if rep.Count == 0 {
			fmt.Fprintln(out, "the policy gate has parked nothing")
			return nil
		}
		for _, p := range rep.Parked {
			fmt.Fprintf(out, "%s  %-18s %-10s %-8s %s\n",
				p.ID, p.Verb, or(p.Target, "-"), p.State, or(p.Reason, "no reason given"))
			if p.Error != "" {
				// A resolved action whose verb failed is not a finished one,
				// and the operator has to see which it was.
				fmt.Fprintf(out, "%s  and the verb did not run: %s\n", strings.Repeat(" ", len(p.ID)), p.Error)
			}
		}
	case "parked.resolve":
		var res daemon.ParkedResolution
		if err := json.Unmarshal(result, &res); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s %s\n", res.ID, res.State)
		if len(res.Result) > 0 {
			fmt.Fprintln(out, string(res.Result))
		}
	default:
		_, err := fmt.Fprintln(out, string(result))
		return err
	}
	return nil
}

// configLine says which config path won and whether there was a file there.
// §10.1 fixes one config path per plugin, so a file the operator is editing
// anywhere else is a leftover, and this is the line that makes it recognisable.
func configLine(c daemon.ConfigHealth) string {
	if c.Path == "" {
		return "none resolved"
	}
	if !c.Present {
		return c.Path + " (no file there: every default applies)"
	}
	return c.Path
}

// gateLine is the §9 policy gate in one line: whether one is configured, what
// it gates, and what an operator has waiting because of it. An unconfigured
// gate allows every verb, which is what the line has to say rather than leave
// blank.
func gateLine(g daemon.GateHealth) string {
	line := "not configured: every verb is allowed (§9.2)"
	if g.Configured {
		line = strings.Join(g.Command, " ")
	}
	line += ", gating " + strings.Join(g.Verbs, " ")
	if g.Parked > 0 {
		line += fmt.Sprintf(", %d parked for you (hsched parked list)", g.Parked)
	}
	return line
}

func or(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// WriteEvent prints one event: as the daemon's own bytes when the caller asked
// for JSON, and as a line an operator reads otherwise.
func WriteEvent(raw json.RawMessage, asJSON bool, out io.Writer) error {
	if asJSON {
		_, err := fmt.Fprintln(out, string(raw))
		return err
	}
	var ev store.Event
	if err := json.Unmarshal(raw, &ev); err != nil {
		return err
	}
	line := fmt.Sprintf("%s  %-30s %-26s %s",
		time.UnixMilli(ev.AtMS).UTC().Format(time.RFC3339), ev.Name, ev.EntityID, ev.ID)
	if detail := detailLine(ev.Detail); detail != "" {
		line += "  " + detail
	}
	_, err := fmt.Fprintln(out, line)
	return err
}

// detailLine is the event's own fields, in a stable order, so two events of
// the same kind print the same way.
func detailLine(detail map[string]any) string {
	if len(detail) == 0 {
		return ""
	}
	keys := make([]string, 0, len(detail))
	for k := range detail {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, detail[k]))
	}
	return strings.Join(parts, " ")
}

// mustJSON re-renders one event for the shared renderer. It cannot fail on a
// document the daemon already encoded once, and an empty one renders as an
// event with no fields rather than taking the whole read down.
func mustJSON(ev store.Event) json.RawMessage {
	raw, err := json.Marshal(ev)
	if err != nil {
		return json.RawMessage("{}")
	}
	return raw
}
