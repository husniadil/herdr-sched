package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/husniadil/herdr-sched/internal/mcpdoor"
	"github.com/husniadil/herdr-sched/internal/verbs"
)

// §7.1: the MCP door is a subcommand of the same binary, so an operator wires
// one command into a client and the daemon it talks to is the one already
// running (§2.1).
func TestTheCLICarriesTheMCPCommand(t *testing.T) {
	for _, want := range []string{"mcp", "daemon", "version"} {
		found := false
		for _, c := range newRootCmd().Commands() {
			if c.Name() == want {
				found = true
			}
		}
		if !found {
			t.Errorf("`hsched %s` is not a subcommand", want)
		}
	}
}

// §6.1: every flag the CLI adds beyond a verb's own arguments is accounted for
// on the MCP door — mapped to a property, or excluded with a reason. The flags
// live here, in the command tree, so this is the half of the drift check only
// this package can make: internal/mcpdoor cannot enumerate cobra's flags.
//
// An absence is not a decision until it is written down.
func TestEveryCLIGlobalIsAccountedForOnTheMCPDoor(t *testing.T) {
	root := newRootCmd()
	seen := map[string]bool{}
	note := func(f *pflag.Flag) {
		if f.Name == "help" {
			return
		}
		seen[f.Name] = true
	}
	root.PersistentFlags().VisitAll(note)

	// A verb is found by its WHOLE subcommand path, not by its last segment:
	// `job add` and `trigger add` end in the same word, and matching on that
	// word alone reads one verb's flags against the other's argument list.
	var walk func(*cobra.Command, []string)
	walk = func(c *cobra.Command, path []string) {
		for _, sub := range c.Commands() {
			walk(sub, append(append([]string{}, path...), sub.Name()))
		}
		v, ok := verbs.ByCLI(path)
		if !ok {
			// A command that is not a verb — daemon, mcp, version,
			// completion — carries flags that configure a PROCESS rather than
			// a call, and there is no tool call that could set one.
			return
		}
		c.Flags().VisitAll(func(f *pflag.Flag) {
			if f.Name == "help" {
				return
			}
			for _, a := range v.Args {
				if a.Name == f.Name {
					return
				}
			}
			seen[f.Name] = true
		})
	}
	walk(root, nil)

	if len(seen) < 5 {
		t.Fatalf("only %d flags found; the walk is not reaching the command tree", len(seen))
	}
	for name := range seen {
		g, ok := mcpdoor.Globals[name]
		if !ok {
			t.Errorf("--%s is a CLI global the MCP door says nothing about; map it or record why not", name)
			continue
		}
		if g.Property == "" && g.Excluded == "" {
			t.Errorf("--%s is excluded from the MCP door with no reason recorded", name)
		}
		if g.Property != "" && g.Excluded != "" {
			t.Errorf("--%s is both mapped to %q and excluded", name, g.Property)
		}
	}
	// And the table does not name flags that do not exist.
	for name := range mcpdoor.Globals {
		if !seen[name] {
			t.Errorf("the MCP door's globals table names --%s, which the CLI does not offer", name)
		}
	}
}

// §3.2: --as is a CLI-only surface. Both siblings exclude it and so does this
// one — a principal an MCP caller could declare is a principal it could
// borrow, and `--as human` is refused everywhere.
func TestAsStaysOffTheMCPDoor(t *testing.T) {
	g, ok := mcpdoor.Globals["as"]
	if !ok || g.Property != "" || g.Excluded == "" {
		t.Fatalf("--as is not recorded as excluded from the MCP door: %+v", g)
	}
	for _, v := range verbs.MCPTools() {
		if v.Accepts("as") {
			t.Errorf("tool %q takes an `as` argument", v.MCP)
		}
	}
}

// Every subcommand in the tree is either a verb from the registry or one of
// the three commands that are deliberately not verbs. A fourth appearing
// without a line here is a surface that grew outside the registry.
func TestTheSubcommandTreeHoldsOnlyVerbsAndTheNamedExemptions(t *testing.T) {
	// The reasoned exemption list. Each of these is a command that starts or
	// describes a PROCESS rather than asking the daemon to do something, which
	// is why none of them is a verb on either door.
	exempt := map[string]string{
		"daemon":     "§2.1: it IS the daemon; a tool call asking a daemon to become one is a call to nothing",
		"run":        "the alias `daemon` answers to, kept because both siblings spell it both ways",
		"mcp":        "§7.1: it IS the MCP door; a tool that starts the door serving it is a loop",
		"version":    "§13.4: it answers off this binary rather than the daemon, so it works with no daemon at all — doctor relays the same two facts over both doors",
		"completion": "cobra's own shell-completion command, which describes this binary to a shell",
	}
	// A group — `parked`, `job` — is a command that carries subcommands and
	// does nothing itself. It is read off the registry rather than listed
	// here, so a namespaced verb brings its own parent and no one has to
	// remember to exempt it.
	groups := map[string]bool{}
	for _, v := range verbs.All {
		if len(v.CLI) > 1 {
			groups[v.CLI[0]] = true
		}
	}
	var walk func(*cobra.Command, []string)
	walk = func(c *cobra.Command, path []string) {
		for _, sub := range c.Commands() {
			here := append(append([]string{}, path...), sub.Name())
			if _, ok := verbs.ByCLI(here); ok {
				walk(sub, here)
				continue
			}
			if groups[sub.Name()] && len(here) == 1 {
				walk(sub, here)
				continue
			}
			reason, named := exempt[sub.Name()]
			if !named || reason == "" {
				t.Errorf("`hsched %s` is a subcommand and no verb in the registry; add it to the registry or to the exemption list with a reason",
					strings.Join(here, " "))
				continue
			}
			// An exemption covers what the command carries under it: cobra's
			// `completion` has one child per shell, and naming the parent is
			// naming the whole of it.
		}
	}
	walk(newRootCmd(), nil)
}

// §7.5: `hsched mcp` MUST accept a flag spelled --operator, and MUST NOT
// accept that declaration by any other route. The flag half is here because
// only this package holds the command tree; the door half — no tool argument,
// no per-call declaration — is pinned in internal/mcpdoor.
func TestTheOperatorDeclarationIsAFlagOnTheMCPCommandAndNowhereElse(t *testing.T) {
	root := newRootCmd()
	var mcp *cobra.Command
	for _, c := range root.Commands() {
		if c.Name() == "mcp" {
			mcp = c
		}
	}
	if mcp == nil {
		t.Fatal("`hsched mcp` is not a subcommand")
	}
	f := mcp.Flags().Lookup("operator")
	if f == nil {
		t.Fatal("`hsched mcp` takes no --operator flag (§7.5)")
	}
	if f.Value.Type() != "bool" {
		t.Errorf("--operator is a %s; the declaration is made or not made", f.Value.Type())
	}
	if f.DefValue != "false" {
		t.Errorf("--operator defaults to %q; a declaration nobody wrote is not one (§3.7)", f.DefValue)
	}

	// And nowhere else. A persistent flag would put the declaration on every
	// verb, which is the per-call declaration §7.5 exists instead of, and no
	// other command may carry it: the door's identity comes from the command
	// that STARTS the door.
	if root.PersistentFlags().Lookup("operator") != nil {
		t.Error("--operator is a persistent flag; §7.5 reads it from the server command alone")
	}
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			if sub != mcp && sub.Flags().Lookup("operator") != nil {
				t.Errorf("`hsched %s` also takes --operator; there is one route and it is the door's", sub.Name())
			}
			walk(sub)
		}
	}
	walk(root)
}
