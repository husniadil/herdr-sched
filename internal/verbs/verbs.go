// Package verbs is the one registry both doors are built from. The CLI
// subcommands and the MCP tools are generated from this list rather than
// written twice, and a parity test enumerates both surfaces against it: a
// verb that exists on one door and not the other is a test failure, not
// something an operator discovers.
package verbs

// String, Bool, Int and Object are the kinds of argument a verb takes. The
// type is in the registry rather than at a door, so a switch is a flag on the
// CLI and a boolean on MCP without either door deciding that for itself.
const (
	String = "string"
	Bool   = "bool"
	Int    = "int"
	// Object is a set of named values — an action's own arguments, whose
	// names depend on which action it is. On the MCP door it is an object in
	// the schema, and on the CLI it is a flag carrying one JSON document,
	// because a repeatable --arg key=value is a shape the other door has no
	// way to spell.
	Object = "object"
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
// Beside the cron half's own five verbs are the seven §13 makes every sibling
// spell the same way, minus `status` and `sweep`, which are held back rather
// than stubbed. `status` answers what a plugin is DOING, and half a plugin —
// jobs without triggers — would answer it in a shape that has to change once
// the other half lands; `sweep` is a reconciliation pass, and a cron job needs
// none: the cursor on the row IS the reconciliation, re-read from the store on
// every start. A verb that exists and answers nothing teaches a caller a shape
// it will have to unlearn.
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
		Name: "job.add", MCP: "job_add", CLI: []string{"job", "add"},
		Short: "Write down a schedule and what it fires",
		Long: "A job is a five-field cron expression, one action from the vocabulary, " +
			"and whether a schedule missed while the daemon was down should fire once " +
			"at the next start. The expression is read in UTC, always: a local zone " +
			"would put a schedule inside the hour a DST transition either repeats or " +
			"never has. Everything that could not fire is refused HERE rather than at " +
			"3am — an expression that does not parse, an action nothing can run, an " +
			"argument its kind does not take. Every call the job makes declares itself " +
			"as `cron:<id>` (§3.2), so the actor on the sibling's own trail is the " +
			"schedule rather than this plugin.",
		Args: []Arg{
			{Name: "id", Type: String, Desc: "The name for this schedule; it is the id half of the `cron:<job id>` principal its calls declare", Required: true, Positional: true},
			{Name: "schedule", Type: String, Desc: "The five-field cron expression, read in UTC: minute hour day-of-month month day-of-week", Required: true, Positional: true},
			{Name: "action", Type: String, Desc: "What it fires: task, mail, dispatch or shell", Required: true, Positional: true},
			{Name: "args", Type: Object, Desc: "The action's own arguments, e.g. {\"title\": \"nightly sweep\"} for a task"},
			{Name: "catch_up", Type: Bool, Desc: "Fire ONCE at the next start when the daemon was down for a scheduled instant; without it the miss is skipped, which is cron's own semantics"},
		},
		Mutates: true,
		Gated:   "sched.job.add",
	},
	{
		Name: "job.list", MCP: "job_list", CLI: []string{"job", "list"},
		Short: "List the schedules and when each one next fires",
		Long: "Every job in this project, with its expression, its action, whether it " +
			"is enabled, when it last fired and when it fires next. A job that is " +
			"disabled is listed and says so: it is kept rather than removed, because " +
			"disabling is not deleting. Pass --all-projects to read every project's.",
	},
	{
		Name: "job.remove", MCP: "job_remove", CLI: []string{"job", "remove"},
		Short: "Take a schedule off for good",
		Long: "The row and its cursor go; the job's trail stays, because what a " +
			"schedule DID is not undone by removing it. To stop a job without losing " +
			"it, disable it instead.",
		Args: []Arg{
			{Name: "id", Type: String, Desc: "The job to remove", Required: true, Positional: true},
		},
		Mutates: true,
		Gated:   "sched.job.remove",
	},
	{
		Name: "job.enable", MCP: "job_enable", CLI: []string{"job", "enable"},
		Short: "Let a schedule fire again",
		Long: "A job enabled after a spell disabled does NOT fire what it missed: the " +
			"cursor is where it was, and the first instant after this one is what " +
			"fires. Enabling a job that is already enabled changes nothing and records " +
			"nothing.",
		Args: []Arg{
			{Name: "id", Type: String, Desc: "The job to enable", Required: true, Positional: true},
		},
		Mutates: true,
		Gated:   "sched.job.enable",
	},
	{
		Name: "job.disable", MCP: "job_disable", CLI: []string{"job", "disable"},
		Short: "Stop a schedule from firing, without losing it",
		Long: "The row stays and the tick passes over it. Nothing is recorded as " +
			"skipped while it is off — an operator who disabled a job is not owed a " +
			"skip record every night — so a job re-enabled later fires from the next " +
			"instant and not from the backlog.",
		Args: []Arg{
			{Name: "id", Type: String, Desc: "The job to disable", Required: true, Positional: true},
		},
		Mutates: true,
		Gated:   "sched.job.disable",
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
