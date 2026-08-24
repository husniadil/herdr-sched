// Package mcpdoor serves the verb table over stdio MCP. It is the second thin
// door over the same daemon calls the CLI makes, and it holds nothing: a door
// is spawned once per client session, so anything kept here would be one of
// several disagreeing sets. Every tool builds the same protocol.Request the
// CLI builds and hands back the same JSON the CLI prints with --json.
package mcpdoor

import (
	"context"
	"encoding/json"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/husniadil/herdr-sched/internal/cli"
	"github.com/husniadil/herdr-sched/internal/client"
	"github.com/husniadil/herdr-sched/internal/codes"
	"github.com/husniadil/herdr-sched/internal/daemon"
	"github.com/husniadil/herdr-sched/internal/protocol"
	"github.com/husniadil/herdr-sched/internal/verbs"
	"github.com/husniadil/herdr-sched/internal/version"
)

// ServerName is how this server registers itself: the repository an operator
// wiring it into a client sees. It is not the tool prefix — the tools are bare
// verbs, and the parity test pins that.
const ServerName = version.Plugin

// Title is the display name.
const Title = "Herdr Sched"

// Door names this surface in the daemon's log.
const Door = "mcp"

// Instructions is what a caller reads before it picks a tool.
const Instructions = "herdr-sched is the scheduler and trigger plugin for a Herdr fleet: a cron " +
	"schedule or an inbound trigger fires an action into the sibling plugins, as the principal " +
	"the contract already names for it — `cron:<job id>` or `trigger:<id>` (§3.1), so the actor " +
	"is on every event trail it writes. Everything is scoped to a project, the git root of the " +
	"directory you are working in, and your principal is derived from the pane you run in: you " +
	"never declare who you are. This build is the cron half: `job_add` writes down a schedule — a " +
	"five-field cron expression read in UTC, one action, and whether to fire once at the next " +
	"start for a schedule the daemon was down for — and `job_list`, `job_remove`, `job_enable` " +
	"and `job_disable` are the rest of it. A schedule missed while the daemon was down is " +
	"SKIPPED by default, which is cron's own semantics; `doctor` names the jobs that were. " +
	"There is no trigger verb yet. Beside the jobs are the verbs every sibling spells the same " +
	"way: `doctor` says whether the plugin can work at all and is what to run first when " +
	"something refuses, `events` reads the append-only trail of what it did, `dump` prints the " +
	"whole store, `parked_list` and `parked_resolve` are the actions the §9 policy gate deferred " +
	"to the operator, and `stop` ends the one daemon serving every project of this user. Every " +
	"verb this plugin has is here: §7.3 leaves nothing on the CLI alone, so a harness with no " +
	"terminal loses no verb."

// Caller is what the door needs to reach the daemon. The default dials the
// real socket, starting a daemon when none answers; a test swaps in something
// that answers in process.
type Caller func(protocol.Request) (json.RawMessage, error)

// New builds the MCP server with one tool per verb.
func New(v string, call Caller) *mcp.Server {
	if call == nil {
		call = func(req protocol.Request) (json.RawMessage, error) {
			verb, _ := verbs.ByName(req.Verb)
			return (&client.Client{NoStart: verb.NoAutostart}).Call(req)
		}
	}
	s := mcp.NewServer(&mcp.Implementation{
		Name:        ServerName,
		Title:       Title,
		Version:     v,
		Description: "Fire a schedule or an inbound trigger into the sibling Herdr plugins",
	}, &mcp.ServerOptions{Instructions: Instructions})
	for _, verb := range verbs.MCPTools() {
		s.AddTool(tool(verb), handlerFor(verb, call))
	}
	return s
}

// Serve runs the door on stdio until the client disconnects.
func Serve(ctx context.Context, v string, call Caller) error {
	return New(v, call).Run(ctx, &mcp.StdioTransport{})
}

// tool renders one verb as an MCP tool. The schema is built from the same Args
// the CLI builds its positionals from, which is what parity means here: same
// name, same arguments, same result.
func tool(v verbs.Verb) *mcp.Tool {
	props := map[string]any{}
	var required []string
	for _, a := range v.Args {
		props[a.Name] = map[string]any{"type": jsonType(a.Type), "description": a.Desc}
		if a.Required {
			required = append(required, a.Name)
		}
	}
	// The scope pair, mirrored from the CLI's global flags so that parity is
	// about the whole surface and not only the per-verb half (§4.2).
	for name, prop := range globalProperty {
		props[name] = prop
	}
	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	return &mcp.Tool{Name: v.MCP, Description: v.Help(), InputSchema: schema}
}

// globalProperty is how each mapped global renders in a tool's schema. The
// maps are shared across tools and never written after init.
var globalProperty = map[string]map[string]any{
	argProject: {"type": "string",
		"description": "The project to act in; defaults to the directory this server runs in (§4.2)"},
	argAllProjects: {"type": "boolean",
		"description": "Act across every project rather than this one"},
}

func jsonType(t string) string {
	switch t {
	case verbs.Int:
		return "integer"
	case verbs.Bool:
		return "boolean"
	case verbs.Object:
		return "object"
	default:
		return "string"
	}
}

// handlerFor turns a tool call into the same daemon call the CLI makes.
func handlerFor(v verbs.Verb, call Caller) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := map[string]any{}
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return failure(codes.Refusef(codes.Invalid, "unreadable arguments: %v", err)), nil
			}
		}
		if err := check(v, args); err != nil {
			return failure(err), nil
		}
		// The scope is the door's to resolve and never the verb's, so it
		// leaves Args before the daemon sees them: an argument no verb
		// declares would be refused there.
		named, _ := args[argProject].(string)
		everyProject, _ := args[argAllProjects].(bool)
		delete(args, argProject)
		delete(args, argAllProjects)
		// Resolved here, in the door: a relative path is the CALLER's, and
		// this process is the only one that knows what it was relative to
		// (§4.1). Nothing is warned about on this door — there is no stderr a
		// tool caller reads.
		project, allProjects, err := cli.Scope(named, everyProject, nil)
		if err != nil {
			return failure(err), nil
		}
		raw, err := call(protocol.Request{
			Verb:        v.Name,
			Args:        args,
			Project:     project,
			AllProjects: allProjects,
			// The pane this door was spawned in, if it was spawned in one. A
			// caller on another harness has none; the daemon records what it
			// is given and grants nothing for it.
			Pane: os.Getenv("HERDR_PANE_ID"),
			Door: Door,
		})
		if err != nil {
			return failure(err), nil
		}
		var structured any
		if err := json.Unmarshal(raw, &structured); err != nil {
			structured = nil
		}
		return &mcp.CallToolResult{
			Content:           []mcp.Content{&mcp.TextContent{Text: string(raw)}},
			StructuredContent: structured,
		}, nil
	}
}

// check holds the door to the schema it published. The go-sdk validates
// arguments only for tools registered through its generic AddTool, which wants
// a Go type per tool; this door is built from a table, so the check lives here
// and walks the same Args the schema was rendered from.
func check(v verbs.Verb, args map[string]any) error {
	// The two the door injects into every schema rather than the registry
	// declaring them, held to the types the schema published.
	for name, want := range map[string]string{argProject: verbs.String, argAllProjects: verbs.Bool} {
		raw, ok := args[name]
		if !ok || raw == nil {
			continue
		}
		if err := daemon.CheckArg(v, verbs.Arg{Name: name, Type: want}, raw); err != nil {
			return err
		}
	}
	for name := range args {
		if name == argProject || name == argAllProjects {
			continue
		}
		if !v.Accepts(name) {
			return codes.Refusef(codes.Invalid, "%s takes no argument named %q", v.Name, name)
		}
	}
	for _, a := range v.Args {
		raw, ok := args[a.Name]
		if !ok || raw == nil {
			if a.Required {
				return codes.Refusef(codes.Invalid, "%s needs %s", v.Name, a.Name)
			}
			continue
		}
		if err := daemon.CheckArg(v, a, raw); err != nil {
			return err
		}
	}
	return nil
}

// failure is a refusal as a tool error carrying the daemon's own code, never
// as a JSON-RPC protocol error: the caller asked a fair question and gets a
// named answer.
func failure(err error) *mcp.CallToolResult {
	envelope := map[string]string{"code": string(codes.Of(err)), "message": codes.Message(err)}
	// §9.3: a DENIED the gate deferred names the row the operator resolves. A
	// caller told only that it was denied has nothing to point at.
	if id := codes.ParkedOf(err); id != "" {
		envelope["parked_id"] = id
	}
	body, _ := json.Marshal(map[string]any{"error": envelope})
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: string(body)}},
	}
}

// The scope arguments every tool takes, mirroring the CLI's global flags. They
// are constants because tool() publishes them and check() enforces them, and a
// typo in one place would make the door promise what it does not keep.
const (
	argProject     = "project"
	argAllProjects = "all_projects"
)

// Global is one CLI flag's place on this door: the tool property it becomes,
// or the reason it has none. An absence is not a decision until it is written
// down, and the tree-walk parity test in cmd/ holds every flag the CLI carries
// to exactly one of these two.
type Global struct {
	// Property is the tool argument this flag becomes, empty when excluded.
	Property string
	// Excluded is why this flag has no place on the MCP door.
	Excluded string
}

// Globals maps the CLI's flag names to their place on the MCP door. It is the
// reasoned exemption list: every flag the command tree carries beyond a verb's
// own arguments appears here, mapped or excluded with a reason.
var Globals = map[string]Global{
	"project":      {Property: argProject},
	"all-projects": {Property: argAllProjects},
	"as": {Excluded: "§3.2: agent and human principals are derived, never declared — " +
		"a principal an MCP caller could declare is a principal it could borrow"},
	"json": {Excluded: "§7.1: a tool call already answers with a structured document, " +
		"so there is no prose mode to switch off"},
	"follow": {Excluded: "§8.2 with §7.1: a tool call answers once; a stream is not a tool call"},
}
