package cli

import (
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/husniadil/herdr-sched/internal/codes"
	"github.com/husniadil/herdr-sched/internal/project"
	"github.com/husniadil/herdr-sched/internal/protocol"
	"github.com/husniadil/herdr-sched/internal/verbs"
)

// Call is one parsed invocation: the verb, the request both doors build for
// it, and whether the caller asked for the answer as it came. Parsing an argv
// and performing the call are separate so that a test can read what a command
// line MEANS without a daemon to send it to.
type Call struct {
	Verb   verbs.Verb
	Req    protocol.Request
	AsJSON bool
}

// Runner performs a parsed call. Root takes one so the command tree is built
// once and driven by whoever owns the socket.
type Runner func(Call) error

// globals are the four flags §3.2 and §4.2 fix for every plugin's CLI. They
// are per-root rather than package-level, so building a second root in the
// same process cannot inherit the first one's answers.
type globals struct {
	jsonOut     bool
	project     string
	allProjects bool
	as          string
}

// Root builds the whole command tree. Every verb comes from the one registry
// the MCP door is generated from, so a verb cannot exist on one door and not
// the other, and none of them writes its own flags out by hand.
func Root(run Runner) *cobra.Command {
	g := &globals{}
	root := &cobra.Command{
		Use:   "hsched",
		Short: "The scheduler and trigger plugin for a Herdr fleet",
		Long: "herdr-sched: it fires a schedule or an inbound trigger into the sibling\n" +
			"plugins, as a principal the contract already names — cron:<job> and\n" +
			"trigger:<id>. This binary is the daemon and both doors.\n" +
			"Conforms to the shared plugin contract.",
		// §6.2: a failure is one report on one stream. Cobra's own habit of
		// printing the usage block after an error would make it two, and the
		// error itself is printed once, by main, in the shape the contract
		// fixes.
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().BoolVar(&g.jsonOut, "json", false, "Print one JSON document on stdout (§6.2)")
	root.PersistentFlags().StringVar(&g.project, "project", "", "The project to act in; defaults to the working directory (§4.2)")
	root.PersistentFlags().BoolVar(&g.allProjects, "all-projects", false, "Act across every project rather than this one")
	root.PersistentFlags().StringVar(&g.as, "as", "", "Act as a cron, trigger or plugin principal (§3.2)")

	groups := map[string]*cobra.Command{}
	for _, v := range verbs.All {
		cmd := buildVerb(v, g, run)
		if len(v.CLI) == 1 {
			root.AddCommand(cmd)
			continue
		}
		parent, ok := groups[v.CLI[0]]
		if !ok {
			parent = newGroup(v.CLI[0])
			groups[v.CLI[0]] = parent
			root.AddCommand(parent)
		}
		parent.AddCommand(cmd)
	}
	// Cobra adds the completion command on its way through Execute, which
	// leaves it invisible to anything that reads the tree instead of running
	// it. Added here so the command exists the moment the root does, and so a
	// test can ask for it without executing a call.
	root.InitDefaultCompletionCmd()
	return root
}

// newGroup is a parent command like `hsched parked`, which carries subcommands
// and does nothing itself.
//
// It has to be Runnable AND refuse arguments. Cobra returns help for a command
// that is not Runnable BEFORE it ever validates arguments, so NoArgs alone is
// unreachable on a bare parent: `hsched parked nonsense` would read as "no
// subcommand given", print help on stdout and exit 0, where §6.2 and §6.3
// promise one document and a failure status. RunE makes the parent Runnable so
// NoArgs is reached, and NoArgs turns the stray argument into the parse error
// main already renders as a USAGE envelope. Both siblings hit this first; the
// fix travels with the shape.
func newGroup(name string) *cobra.Command {
	return &cobra.Command{
		Use:   name,
		Short: groupShort(name),
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
		// Being Runnable makes cobra add `hsched parked [flags]` to the usage
		// line, which is an artifact of that mechanism rather than a fact
		// about the command: a group takes no flags of its own.
		DisableFlagsInUseLine: true,
	}
}

func groupShort(name string) string {
	if name == "parked" {
		return "Work with the actions the policy gate deferred"
	}
	return name
}

// buildVerb turns one registry entry into a cobra command, so the CLI and the
// MCP door cannot drift (§6.1).
func buildVerb(v verbs.Verb, g *globals, run Runner) *cobra.Command {
	positional := []verbs.Arg{}
	for _, a := range v.Args {
		if a.Positional {
			positional = append(positional, a)
		}
	}
	use := v.CLI[len(v.CLI)-1]
	for _, a := range positional {
		if a.Required {
			use += " <" + a.Name + ">"
		} else {
			use += " [" + a.Name + "]"
		}
	}
	cmd := &cobra.Command{
		Use:   use,
		Short: v.Short,
		Long:  v.Help(),
		Args:  cobra.MaximumNArgs(len(positional)),
	}

	// A switch is a flag on this door and a boolean field on the other,
	// registered from the same table the MCP schema is rendered from.
	strs := map[string]*string{}
	bools := map[string]*bool{}
	ints := map[string]*int{}
	for _, a := range v.Args {
		if a.Positional {
			continue
		}
		switch a.Type {
		case verbs.String:
			strs[a.Name] = cmd.Flags().String(a.Name, "", a.Desc)
		case verbs.Bool:
			bools[a.Name] = cmd.Flags().Bool(a.Name, false, a.Desc)
		case verbs.Int:
			ints[a.Name] = cmd.Flags().Int(a.Name, 0, a.Desc)
		}
	}
	// --follow is the CLI's own flag, not the registry's: it is a property of
	// the CONNECTION rather than an argument of the verb (§8.2 with §7.1), and
	// there is no tool call that could set it.
	var follow *bool
	if v.Name == "events" {
		follow = cmd.Flags().Bool("follow", false,
			"Keep the connection and print each event as it is written")
	}

	cmd.RunE = func(cmd *cobra.Command, argv []string) error {
		args := map[string]any{}
		for i, a := range positional {
			if i < len(argv) {
				args[a.Name] = argv[i]
				continue
			}
			if a.Required {
				return codes.Refusef(codes.Invalid, "%s needs <%s>", strings.Join(v.CLI, " "), a.Name)
			}
		}
		// Only a flag the caller actually wrote is sent. A false the caller
		// never typed is an argument the daemon would then have to tell apart
		// from one they did.
		for name, p := range strs {
			if cmd.Flags().Changed(name) {
				args[name] = *p
			}
		}
		for name, p := range bools {
			if cmd.Flags().Changed(name) {
				args[name] = *p
			}
		}
		for name, p := range ints {
			if cmd.Flags().Changed(name) {
				args[name] = *p
			}
		}
		req, err := g.request(v, args)
		if err != nil {
			return err
		}
		if follow != nil {
			req.Follow = *follow
		}
		if run == nil {
			return nil
		}
		return run(Call{Verb: v, Req: req, AsJSON: g.jsonOut})
	}
	return cmd
}

// request fills in everything the door derives rather than the caller
// declaring it: the scope (§4.2) and the principal (§3.2).
func (g *globals) request(v verbs.Verb, args map[string]any) (protocol.Request, error) {
	scope, all, err := Scope(g.project, g.allProjects, os.Stderr)
	if err != nil {
		return protocol.Request{}, err
	}
	as, err := principal(g.as)
	if err != nil {
		return protocol.Request{}, err
	}
	return protocol.Request{
		Verb: v.Name,
		Args: args,
		// The pane this door runs in, recorded by the daemon and granting
		// nothing. A caller on another harness has none, and needs none.
		Pane:        os.Getenv("HERDR_PANE_ID"),
		Door:        Door,
		Project:     scope,
		AllProjects: all,
		As:          as,
	}, nil
}

// Scope turns a named project and an every-project switch into the pair the
// daemon reads. It is exported because the MCP door injects the same two as
// tool arguments (§4.2), and a second copy of this policy is how the two doors
// would come to disagree about what "no scope" means.
//
// §4.2 resolves the project HERE, in the door, because a relative path is
// relative to the CALLER's working directory and the daemon's is somewhere
// else entirely. §4.1 is what canonical means: the git common dir's parent, so
// every worktree of a repository answers with one project.
func Scope(named string, allProjects bool, warn *os.File) (string, bool, error) {
	if named != "" && allProjects {
		// Named for both doors, because both reach this: the CLI spells the
		// pair --project / --all-projects and the MCP door spells it project /
		// all_projects.
		return "", false, codes.Refusef(codes.Invalid,
			"project names one project and all_projects names every one; pass one "+
				"(--project / --all-projects on the CLI)")
	}
	if allProjects {
		return "", true, nil
	}
	opts := project.Options{Explicit: named}
	if warn != nil {
		opts.Warn = warn
	}
	proj, err := project.Resolve(opts)
	if err != nil {
		return "", false, codes.Refusef(codes.Invalid, "cannot resolve the project %q: %v", named, err)
	}
	return proj, false, nil
}

// declarable are the §3.2 principal kinds a call may declare. `agent` and
// `human` are missing on purpose: both are DERIVED from the calling process,
// and a caller who could write `--as agent:<someone else's pane>` would be
// declaring the one fact the rule exists to keep underived.
var declarable = []string{"cron:", "trigger:", "plugin:"}

// principal reads --as, refusing anything §3.2 does not let a call declare.
func principal(as string) (string, error) {
	if as == "" {
		return "", nil
	}
	for _, kind := range declarable {
		if id, found := strings.CutPrefix(as, kind); found && id != "" {
			return as, nil
		}
	}
	return "", codes.Refusef(codes.Invalid,
		"--as %q is not a principal a call may declare; write %s<id> (§3.2)",
		as, strings.Join(declarable, "<id>, "))
}

// Request builds the daemon request one subcommand's argv asks for, without
// sending it. It drives the real command tree rather than a second parser, so
// what this reports is what the binary does.
func Request(v verbs.Verb, argv []string) (protocol.Request, bool, error) {
	var got Call
	root := Root(func(c Call) error {
		got = c
		return nil
	})
	root.SetOut(discard{})
	root.SetErr(discard{})
	root.SetArgs(append(append([]string{}, v.CLI...), argv...))
	if err := root.Execute(); err != nil {
		return protocol.Request{}, false, AsRefusal(err)
	}
	return got.Req, got.AsJSON, nil
}

// AsRefusal gives cobra's own failures — an unknown flag, an unknown
// subcommand, a stray argument — the code §6.3 fixes for caller input. They
// are the one kind of failure that reaches this binary without a name of its
// own, and codes.Of would call them UNAVAILABLE, which reads as "something was
// unreachable" for what is a typo. A refusal already named keeps its own,
// including every code the daemon sent back.
func AsRefusal(err error) error {
	if err == nil || codes.Named(err) {
		return err
	}
	return codes.Refusef(codes.Invalid, "%v", err)
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
