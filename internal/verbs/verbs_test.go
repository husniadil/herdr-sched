package verbs

import (
	"strings"
	"testing"
)

// §9.4 fixes the gate name as `<short name>.<verb>`, and the short name is
// `sched` (contract-notes, §13.2). A gate name that carries the binary instead
// is a policy an operator writes once and this plugin never asks about.
func TestEveryGateNameCarriesTheShortName(t *testing.T) {
	for _, v := range All {
		if v.Gated == "" {
			continue
		}
		if !strings.HasPrefix(v.Gated, "sched.") {
			t.Errorf("verb %q gates as %q, want sched.<verb>", v.Name, v.Gated)
		}
	}
}

// §9.1 puts every world-changing verb behind the gate, and the registry makes
// an exception a decision written beside the verb rather than an omission.
func TestAWritingVerbIsGatedOrSaysWhyNot(t *testing.T) {
	for _, v := range All {
		switch {
		case !v.Mutates && v.Gated != "":
			t.Errorf("verb %q gates as %q and changes nothing", v.Name, v.Gated)
		case !v.Mutates && v.Ungated != "":
			t.Errorf("verb %q explains an exemption it does not need", v.Name)
		case v.Mutates && v.Gated == "" && v.Ungated == "":
			t.Errorf("verb %q writes, passes no gate name, and gives no reason", v.Name)
		case v.Mutates && v.Gated != "" && v.Ungated != "":
			t.Errorf("verb %q is both gated as %q and exempted", v.Name, v.Gated)
		}
	}
}

// The MCP name is the verb alone with dots as underscores. It is a field
// rather than a transformation, and this is what holds the field to the rule.
func TestTheToolNameIsTheVerbWithDotsAsUnderscores(t *testing.T) {
	for _, v := range All {
		if v.MCP == "" {
			t.Errorf("verb %q is served on no MCP door and records no reason", v.Name)
			continue
		}
		if want := strings.ReplaceAll(v.Name, ".", "_"); v.MCP != want {
			t.Errorf("verb %q is served as %q, want %q", v.Name, v.MCP, want)
		}
	}
}

// A namespaced verb is dotted on the socket and a subcommand path on the CLI,
// and the two say the same thing.
func TestTheSubcommandPathMatchesTheDottedName(t *testing.T) {
	for _, v := range All {
		if want := strings.Join(v.CLI, "."); want != v.Name {
			t.Errorf("verb %q is reached as `hsched %s`", v.Name, strings.Join(v.CLI, " "))
		}
		if _, ok := ByCLI(v.CLI); !ok {
			t.Errorf("verb %q cannot be found by its own subcommand path", v.Name)
		}
		if _, ok := ByName(v.Name); !ok {
			t.Errorf("verb %q cannot be found by its own name", v.Name)
		}
	}
}

// §13: these are the verbs every sibling spells the same way, so an operator
// learns them once. `status` and `sweep` are the two the standard's parity
// list also names, and they are held back here rather than stubbed — the
// reason is recorded beside the registry and in docs/contract-notes.md.
func TestTheCommonVerbsAreAllPresent(t *testing.T) {
	for _, name := range []string{"doctor", "stop", "dump", "events", "parked.list", "parked.resolve"} {
		if _, ok := ByName(name); !ok {
			t.Errorf("the shared verb %q is missing", name)
		}
	}
	for _, name := range []string{"status", "sweep"} {
		if _, ok := ByName(name); ok {
			t.Errorf("%q is served and this scaffold has nothing for it to answer; if that changed, this test moves with it", name)
		}
	}
}

// A required argument that is not positional is a flag a caller must write and
// the CLI never shows in its usage line.
func TestEveryRequiredArgumentIsPositional(t *testing.T) {
	for _, v := range All {
		for _, a := range v.Args {
			if a.Required && !a.Positional {
				t.Errorf("verb %q requires %q as a flag", v.Name, a.Name)
			}
			if a.Desc == "" {
				t.Errorf("verb %q takes %q with no description", v.Name, a.Name)
			}
			switch a.Type {
			case String, Bool, Int, Object:
			default:
				t.Errorf("verb %q takes %q as %q, which is no argument kind", v.Name, a.Name, a.Type)
			}
		}
	}
}

// A gate name resolves back to its verb, which is what §9.3 needs to re-run a
// parked action.
func TestAGateNameResolvesBackToItsVerb(t *testing.T) {
	for _, want := range GatedVerbs() {
		v, ok := ByGated(want)
		if !ok {
			t.Fatalf("the gated verb %q resolves to nothing", want)
		}
		if v.Gated != want {
			t.Fatalf("%q resolved to verb %q, gated as %q", want, v.Name, v.Gated)
		}
	}
}

// Both doors show the same words, because a verb explained two ways is a verb
// that drifts.
func TestEveryVerbExplainsItselfOnce(t *testing.T) {
	for _, v := range All {
		if v.Short == "" {
			t.Errorf("verb %q has no one-line help", v.Name)
		}
		if !strings.HasPrefix(v.Help(), v.Short) {
			t.Errorf("verb %q's help does not open with its summary", v.Name)
		}
	}
}
