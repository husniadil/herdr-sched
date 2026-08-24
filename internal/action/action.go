// Package action is the vocabulary a signal fires: what a cron schedule or a
// trigger DOES when it goes off, as data on the row rather than as code.
//
// There are four kinds and the list is closed (note 2): create a task on the
// htask board, send or ask via hmail, dispatch via hdis, run a shell command.
// Nothing here runs anything — this is the pure core that decides whether an
// action is well formed, and it is validated at CREATE time. A job saved with
// a kind nothing can run, or without the argument its kind cannot run
// without, is a schedule that fails at 3am in a log nobody reads; refusing it
// at create puts the failure in front of whoever wrote it.
package action

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/husniadil/herdr-sched/internal/codes"
)

// The four kinds. A fifth is a deliberate edit here and in the spec below.
const (
	// KindTask files a task on the htask board.
	KindTask = "task"
	// KindMail sends a notify, or an ask, via hmail.
	KindMail = "mail"
	// KindDispatch brings a worker up for a ready task via hdis.
	KindDispatch = "dispatch"
	// KindShell runs a command on the host.
	KindShell = "shell"
)

// Kinds is the whole vocabulary, in the order it is documented.
var Kinds = []string{KindTask, KindMail, KindDispatch, KindShell}

// Types an argument can have. A value is carried as a string because the row
// it lives on is JSON written by an operator; the type is what says how to
// read it, and reading it is a create-time check rather than a fire-time
// surprise.
const (
	typeString = "string"
	typeBool   = "bool"
	typeInt    = "int"
)

type arg struct {
	name     string
	typ      string
	required bool
}

// spec is what each kind accepts. An argument not named here is refused: a
// typo silently kept is an action that fires with a default nobody chose.
var spec = map[string][]arg{
	KindTask: {
		{name: "title", typ: typeString, required: true},
		{name: "description", typ: typeString},
		{name: "project", typ: typeString},
		{name: "priority", typ: typeInt},
	},
	KindMail: {
		{name: "to", typ: typeString, required: true},
		{name: "body", typ: typeString, required: true},
		// An ask owes a correlated reply back; a notify owes nothing. The
		// two are one kind with a switch rather than two kinds, because
		// everything else about the call is the same.
		{name: "ask", typ: typeBool},
		{name: "project", typ: typeString},
	},
	KindDispatch: {
		{name: "task", typ: typeString, required: true},
		{name: "project", typ: typeString},
	},
	KindShell: {
		{name: "command", typ: typeString, required: true},
		{name: "dir", typ: typeString},
	},
}

// Action is one thing a signal does, as it sits on the job or trigger row.
type Action struct {
	Kind string            `json:"kind"`
	Args map[string]string `json:"args,omitempty"`
}

// Validate refuses everything that could not run, at the moment it is
// written down. Everything it accepts, an adapter can turn into an argv.
func (a Action) Validate() error {
	accepted, ok := spec[a.Kind]
	if !ok {
		return codes.Errorf(codes.Usage,
			"%s: no action does that; a signal fires one of %s",
			describeKind(a.Kind), strings.Join(Kinds, ", "))
	}
	known := map[string]arg{}
	for _, want := range accepted {
		known[want.name] = want
	}
	for _, name := range sortedKeys(a.Args) {
		want, ok := known[name]
		if !ok {
			return codes.Errorf(codes.Usage,
				"a %s action takes no %q; it takes %s",
				a.Kind, name, strings.Join(names(accepted), ", "))
		}
		if err := checkType(a.Kind, want, a.Args[name]); err != nil {
			return err
		}
	}
	for _, want := range accepted {
		if want.required && strings.TrimSpace(a.Args[want.name]) == "" {
			return codes.Errorf(codes.Usage,
				"a %s action needs a %s, and this one has none", a.Kind, want.name)
		}
	}
	return nil
}

// Ask reports whether a mail action owes a reply back. It is only ever asked
// of a validated action, so the value parses.
func (a Action) Ask() bool {
	ask, _ := strconv.ParseBool(a.Args["ask"])
	return ask
}

// Arg is one argument's value, empty when the action does not carry it.
func (a Action) Arg(name string) string { return a.Args[name] }

func checkType(kind string, want arg, value string) error {
	switch want.typ {
	case typeBool:
		if value == "" {
			return nil
		}
		if _, err := strconv.ParseBool(value); err != nil {
			return codes.Errorf(codes.Usage,
				"a %s action's %s is true or false, and this one is %q", kind, want.name, value)
		}
	case typeInt:
		if value == "" {
			return nil
		}
		if _, err := strconv.Atoi(value); err != nil {
			return codes.Errorf(codes.Usage,
				"a %s action's %s is a whole number, and this one is %q", kind, want.name, value)
		}
	}
	return nil
}

func describeKind(kind string) string {
	if strings.TrimSpace(kind) == "" {
		return "an action with no kind"
	}
	return fmt.Sprintf("%q", kind)
}

func names(args []arg) []string {
	var out []string
	for _, a := range args {
		if a.required {
			out = append(out, a.name)
			continue
		}
		out = append(out, "["+a.name+"]")
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
