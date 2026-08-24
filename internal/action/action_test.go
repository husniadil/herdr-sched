package action

import (
	"strings"
	"testing"

	"github.com/husniadil/herdr-sched/internal/codes"
)

// The vocabulary is closed. A job or a trigger carries one of these four and
// nothing else, and the list is here rather than spread across the adapters
// so a fifth kind is one deliberate edit.
func TestTheVocabularyIsTheFourKinds(t *testing.T) {
	want := []string{"task", "mail", "dispatch", "shell"}
	if len(Kinds) != len(want) {
		t.Fatalf("the vocabulary is %v, and the kinds this plugin has are %v", Kinds, want)
	}
	for i, k := range want {
		if Kinds[i] != k {
			t.Fatalf("kind %d is %q, want %q", i, Kinds[i], k)
		}
	}
}

// An unknown kind refuses at create time, not at fire time: a job saved with
// a kind nothing can run is a schedule that fails at 3am in a log nobody
// reads.
func TestAnUnknownKindRefusesAtCreate(t *testing.T) {
	err := Action{Kind: "webhook", Args: map[string]string{}}.Validate()
	if err == nil {
		t.Fatal("want a refusal")
	}
	var cerr *codes.Error
	if !asCode(err, &cerr) || cerr.Code != codes.Usage {
		t.Fatalf("want USAGE, got %v", err)
	}
	if !strings.Contains(err.Error(), "webhook") || !strings.Contains(err.Error(), "task") {
		t.Fatalf("the refusal names neither the kind nor the vocabulary: %v", err)
	}
}

// Each kind names the arguments it cannot run without, and a missing one is
// refused with the name of what is missing.
func TestAMissingRequiredArgumentRefusesAtCreate(t *testing.T) {
	cases := []struct {
		kind, missing string
		args          map[string]string
	}{
		{"task", "title", map[string]string{}},
		{"mail", "to", map[string]string{"body": "up"}},
		{"mail", "body", map[string]string{"to": "wM:p1"}},
		{"dispatch", "task", map[string]string{}},
		{"shell", "command", map[string]string{}},
	}
	for _, c := range cases {
		err := Action{Kind: c.kind, Args: c.args}.Validate()
		if err == nil {
			t.Fatalf("%s without %s: want a refusal", c.kind, c.missing)
		}
		if !strings.Contains(err.Error(), c.missing) {
			t.Fatalf("%s: the refusal does not name %q: %v", c.kind, c.missing, err)
		}
	}
}

// An argument no kind declares is refused too. A typo silently kept is an
// action that fires with a default nobody chose.
func TestAnUnknownArgumentRefusesAtCreate(t *testing.T) {
	err := Action{Kind: "task", Args: map[string]string{"title": "sweep", "titel": "sweep"}}.Validate()
	if err == nil || !strings.Contains(err.Error(), "titel") {
		t.Fatalf("want a refusal naming titel, got %v", err)
	}
}

// The well-formed shapes pass, optional arguments and all.
func TestTheWellFormedShapesPass(t *testing.T) {
	for _, a := range []Action{
		{Kind: "task", Args: map[string]string{"title": "sweep", "description": "d", "project": "/src/p", "priority": "3"}},
		{Kind: "mail", Args: map[string]string{"to": "wM:p1", "body": "up", "ask": "true"}},
		{Kind: "dispatch", Args: map[string]string{"task": "01AAA", "project": "/src/p"}},
		{Kind: "shell", Args: map[string]string{"command": "echo hi", "dir": "/src/p"}},
	} {
		if err := a.Validate(); err != nil {
			t.Fatalf("%s: %v", a.Kind, err)
		}
	}
}

// A boolean argument takes a boolean. "yes" is not one, and reading it as
// false would send a notify where the operator asked for an ask.
func TestABooleanArgumentTakesABoolean(t *testing.T) {
	err := Action{Kind: "mail", Args: map[string]string{"to": "wM:p1", "body": "up", "ask": "yes"}}.Validate()
	if err == nil || !strings.Contains(err.Error(), "ask") {
		t.Fatalf("want a refusal naming ask, got %v", err)
	}
	if err := (Action{Kind: "mail", Args: map[string]string{"to": "wM:p1", "body": "up", "ask": "false"}}).Validate(); err != nil {
		t.Fatalf("false is a boolean: %v", err)
	}
}

// An integer argument takes an integer, for the same reason.
func TestAnIntegerArgumentTakesAnInteger(t *testing.T) {
	err := Action{Kind: "task", Args: map[string]string{"title": "sweep", "priority": "high"}}.Validate()
	if err == nil || !strings.Contains(err.Error(), "priority") {
		t.Fatalf("want a refusal naming priority, got %v", err)
	}
}

// An empty required argument is a missing one. A title of "" is not a title.
func TestAnEmptyRequiredArgumentIsAMissingOne(t *testing.T) {
	err := Action{Kind: "task", Args: map[string]string{"title": "  "}}.Validate()
	if err == nil || !strings.Contains(err.Error(), "title") {
		t.Fatalf("want a refusal naming title, got %v", err)
	}
}

// §3.2: every sibling call this plugin makes declares the signal it fired
// from, so the actor on the sibling's own trail is the job and not "some
// plugin". The two spellings are the two signal kinds and there is no third.
func TestTheSourcePrincipalNamesTheSignal(t *testing.T) {
	if got := (Source{Kind: SourceCron, ID: "nightly"}).Principal(); got != "cron:nightly" {
		t.Fatalf("cron principal is %q", got)
	}
	if got := (Source{Kind: SourceTrigger, ID: "01TRG"}).Principal(); got != "trigger:01TRG" {
		t.Fatalf("trigger principal is %q", got)
	}
}

// A source with no id has nothing to attribute the call to, and a sibling
// call that cannot say who fired it is refused rather than sent as a bare
// `cron:`.
func TestASourceWithoutAnIDIsRefused(t *testing.T) {
	for _, s := range []Source{{Kind: SourceCron}, {Kind: "", ID: "x"}, {Kind: "webhook", ID: "x"}} {
		if err := s.Validate(); err == nil {
			t.Fatalf("%+v: want a refusal", s)
		}
	}
	if err := (Source{Kind: SourceTrigger, ID: "01TRG"}).Validate(); err != nil {
		t.Fatalf("a whole source: %v", err)
	}
}

func asCode(err error, into **codes.Error) bool {
	e, ok := err.(*codes.Error)
	if ok {
		*into = e
	}
	return ok
}
