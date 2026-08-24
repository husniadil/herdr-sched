// Package verbs is the one registry both doors are built from. The CLI
// subcommands and the MCP tools are generated from this list rather than
// written twice, and a parity test enumerates both surfaces against it: a
// verb that exists on one door and not the other is a test failure, not
// something an operator discovers.
package verbs

// String, Bool and Int are the kinds of argument a verb takes. The type is in
// the registry rather than at a door, so a switch is a flag on the CLI and a
// boolean on MCP without either door deciding that for itself.
const (
	String = "string"
	Bool   = "bool"
	Int    = "int"
)

// Arg is one parameter of a verb. A positional arg is a CLI positional and an
// ordinary named field over MCP and the socket.
type Arg struct {
	Name       string
	Type       string
	Desc       string
	Required   bool
	Positional bool
}

// Verb is one operation, in every surface it appears in.
type Verb struct {
	// Name is the verb on the socket, dotted for a namespaced verb:
	// parked.list, the way all three siblings spell theirs.
	Name string
	// MCP is the tool name: the verb alone, with dots as underscores. The
	// door serves bare verbs, so a caller reads doctor as herdr-sched's
	// doctor rather than as a name that repeats the binary. It is a field
	// and not a transformation applied at the door, so an absence from the
	// agent surface is a decision written beside the verb.
	MCP string
	// CLI is the subcommand path, e.g. {"parked", "list"}.
	CLI []string
	// Short is the one-line help both doors show.
	Short string
	// Long is what the MCP tool description adds for a caller that cannot
	// ask a follow-up question.
	Long string
	// Args is the parameter list, in CLI positional order.
	Args []Arg
	// NoAutostart sends the verb to whatever daemon is already listening
	// and refuses when none is, instead of starting one.
	NoAutostart bool
	// Mutates says the verb changes the world, which is what §9.1 puts
	// behind the policy gate. A verb that only reads is neither gated nor
	// asked to explain itself.
	Mutates bool
	// Gated is the §9.4 verb name handed to the policy gate, `<short
	// name>.<verb>` with the short name §13.2 fixes. Empty means this verb
	// passes no name, which a Mutates verb must justify in Ungated.
	Gated string
	// Ungated is why a verb that writes passes no name to the policy gate.
	// Required exactly when Mutates is true and Gated is empty, so the
	// decision is written down beside the verb rather than inferred from
	// its absence.
	Ungated string
}

// All is the registry. Order is the order the CLI lists them in.
//
// This is the common foundation and nothing more: the seven verbs §13 makes
// every sibling spell the same way, minus `status` and `sweep`, which are
// held back rather than stubbed. `status` answers what a plugin is doing and
// this one is not doing anything yet; `sweep` is a reconciliation pass and
// there is nothing to reconcile until there is a job or a trigger. A verb
// that exists and answers nothing teaches a caller a shape it will have to
// unlearn.
var All = []Verb{
	{
		Name: "doctor", MCP: "doctor", CLI: []string{"doctor"},
		Short: "Report whether the scheduler can work at all",
		Long: "Answers with this binary's version and the contract revision it " +
			"satisfies, the socket, the state and config directories it resolved, " +
			"whether a config file was found and where, the §9 policy gate and what " +
			"it has parked, and the §8 trail's depth. Run it first when something " +
			"else refuses.",
	},
	{
		Name: "dump", MCP: "dump", CLI: []string{"dump"},
		Short: "Print the whole store as JSON",
		Long: "Everything this daemon remembers across restarts, in one document " +
			"(§5.8): the actions the policy gate parked and the §8 event trail. It " +
			"is the daemon's own live set rather than a re-read of the file, so it " +
			"is what the next save will write.",
	},
	{
		Name: "events", MCP: "events", CLI: []string{"events"},
		Short: "Read the append-only trail of what the scheduler did",
		Long: "The §8.1 events this plugin owns and nothing else records. Without " +
			"since this reads from the BEGINNING of what the daemon still holds, " +
			"oldest first, so a consumer that resumes passes since with the last " +
			"event id it saw, or a Unix-millisecond timestamp, or it reads " +
			"everything again. The trail is bounded: an id that has rotated out of " +
			"it is refused rather than answered with the whole window. On the CLI " +
			"--follow turns this into a subscription that keeps printing; a tool " +
			"call answers once and has no follow, because a stream is not a tool " +
			"call.",
		Args: []Arg{
			{Name: "since", Type: String, Desc: "An event id, or a Unix-millisecond timestamp, to resume after"},
			{Name: "limit", Type: Int, Desc: "Stop after this many events"},
		},
	},
	{
		Name: "parked.list", MCP: "parked_list", CLI: []string{"parked", "list"},
		Short: "List the actions the policy gate deferred to the operator",
		Long: "A gate that answers defer parks the call instead of performing it " +
			"(§9.3) and refuses with DENIED carrying the parked_id. This is where " +
			"those rows are read: who asked, which gated verb, what target, the " +
			"reason the gate gave, and whether the action is still waiting or was " +
			"resolved and then failed. A failed row is not finished business — the " +
			"operator decided and the verb did not run.",
	},
	{
		Name: "parked.resolve", MCP: "parked_resolve", CLI: []string{"parked", "resolve"},
		Short: "Let a parked action through, or reject it",
		Long: "Re-runs the parked verb under the subject the gate stopped, never " +
			"the resolver's (§9.3), and skips the gate, because the resolution IS " +
			"the decision the gate deferred. The row records who resolved it. With " +
			"--reject the verb never runs and the row is closed. This is the " +
			"operator's authority and therefore advice rather than a refusal this " +
			"door makes (§3.7): confirm with the user before resolving one on their " +
			"behalf.",
		Args: []Arg{
			{Name: "id", Type: String, Desc: "The parked action id, as DENIED reported it", Required: true, Positional: true},
			{Name: "reject", Type: Bool, Desc: "Close the action without running the verb"},
		},
		Mutates: true,
		Ungated: "resolving a deferral is the answer to a gate that already spoke; gating it would let a gate park its own resolution and strand every deferred action",
	},
	{
		Name: "stop", MCP: "stop", CLI: []string{"stop"},
		Short: "Ask the running daemon to shut down",
		Long: "The daemon stops ticking, closes its socket, drops its lock and " +
			"exits. Answers CONFLICT as NOT_RUNNING when no daemon is listening: " +
			"nothing is started just to be stopped. It is a brake on the WHOLE " +
			"scheduler and not on one schedule: nothing this plugin drives fires " +
			"again until a daemon is started. Confirm with the operator before " +
			"calling it, the same way you would before any act whose blast radius " +
			"is everyone else's work.",
		NoAutostart: true,
		Mutates:     true,
		Gated:       "sched.stop",
	},
}

// ByName finds the verb a socket request names.
func ByName(name string) (Verb, bool) {
	for _, v := range All {
		if v.Name == name {
			return v, true
		}
	}
	return Verb{}, false
}

// ByCLI finds the verb a subcommand path names.
func ByCLI(path []string) (Verb, bool) {
	for _, v := range All {
		if len(v.CLI) != len(path) {
			continue
		}
		same := true
		for i := range v.CLI {
			if v.CLI[i] != path[i] {
				same = false
				break
			}
		}
		if same {
			return v, true
		}
	}
	return Verb{}, false
}

// ByGated maps a §9.4 gate name back to the verb it belongs to, which is what
// resolving a parked action needs.
func ByGated(gated string) (Verb, bool) {
	for _, v := range All {
		if v.Gated != "" && v.Gated == gated {
			return v, true
		}
	}
	return Verb{}, false
}

// Accepts reports whether this verb declares an argument by that name.
func (v Verb) Accepts(name string) bool {
	for _, a := range v.Args {
		if a.Name == name {
			return true
		}
	}
	return false
}

// MCPTools is every verb the MCP door serves, in registry order.
func MCPTools() []Verb {
	out := make([]Verb, 0, len(All))
	for _, v := range All {
		if v.MCP != "" {
			out = append(out, v)
		}
	}
	return out
}

// GatedVerbs is the §9.4 list a policy plugin names, in registry order. The
// README carries the same list, and a test reads one against the other.
func GatedVerbs() []string {
	out := []string{}
	for _, v := range All {
		if v.Gated != "" {
			out = append(out, v.Gated)
		}
	}
	return out
}

// Help is the description a caller reads on either door: the one-line summary
// and the paragraph a caller who cannot ask a follow-up question needs. The
// MCP tool description and `hsched <verb> --help` are the same words, because
// a verb explained two ways is a verb that drifts.
func (v Verb) Help() string {
	if v.Long == "" {
		return v.Short
	}
	return v.Short + ". " + v.Long
}
