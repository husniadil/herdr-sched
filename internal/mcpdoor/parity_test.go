package mcpdoor

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/husniadil/herdr-sched/internal/cli"
	"github.com/husniadil/herdr-sched/internal/codes"
	"github.com/husniadil/herdr-sched/internal/config"
	"github.com/husniadil/herdr-sched/internal/daemon"
	"github.com/husniadil/herdr-sched/internal/project"
	"github.com/husniadil/herdr-sched/internal/protocol"
	"github.com/husniadil/herdr-sched/internal/store"
	"github.com/husniadil/herdr-sched/internal/testenv"
	"github.com/husniadil/herdr-sched/internal/verbs"
)

// pinnedTools is the tool list this door publishes. Adding, renaming or
// removing one is a deliberate change to a surface other harnesses call: it
// moves this list in the same commit, and it is a breaking change.
var pinnedTools = []string{
	"doctor",
	"dump",
	"events",
	"job_add",
	"job_disable",
	"job_enable",
	"job_list",
	"job_remove",
	"parked_list",
	"parked_resolve",
	"stop",
}

// inProcessDaemon is a real daemon over a throwaway store. No socket: both
// doors are tested against the same Handle the socket serves.
func inProcessDaemon(t *testing.T) (*daemon.Daemon, Caller) {
	t.Helper()
	testenv.New(t)
	st, err := store.Open(config.StorePath())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	d := &daemon.Daemon{
		Store:    st,
		Config:   cfg,
		Interval: time.Hour,
		Version:  "0.1.0",
		Log:      log.New(io.Discard, "", 0),
	}
	return d, func(req protocol.Request) (json.RawMessage, error) {
		return d.Handle(context.Background(), req)
	}
}

// session connects an in-memory MCP client to the door.
func session(t *testing.T, call Caller) *mcp.ClientSession {
	t.Helper()
	srv := New("0.1.0", call)
	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	sess, err := mcp.NewClient(&mcp.Implementation{Name: "parity-test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

func text(res *mcp.CallToolResult) string {
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}

// The list a caller on another harness binds to. It moves only on purpose.
func TestTheServedToolListIsPinned(t *testing.T) {
	_, call := inProcessDaemon(t)
	sess := session(t, call)

	tools, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var got []string
	for _, tl := range tools.Tools {
		got = append(got, tl.Name)
	}
	sort.Strings(got)
	want := append([]string(nil), pinnedTools...)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("the served tool list moved.\n got: %v\nwant: %v\nIf this is intended, move pinnedTools in the same commit.", got, want)
	}
}

// The parity guard: every verb reaches both doors, under the same name, and
// neither door carries one the other does not.
func TestNeitherDoorCarriesAVerbTheOtherLacks(t *testing.T) {
	_, call := inProcessDaemon(t)
	sess := session(t, call)
	tools, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	served := map[string]*mcp.Tool{}
	for _, tl := range tools.Tools {
		served[tl.Name] = tl
	}
	for _, v := range verbs.All {
		if _, ok := verbs.ByCLI(v.CLI); !ok {
			t.Errorf("verb %q has no CLI subcommand", v.Name)
		}
		tl, ok := served[v.MCP]
		if !ok {
			t.Errorf("verb %q is a CLI subcommand and no MCP tool", v.Name)
			continue
		}
		if tl.Name != v.MCP {
			t.Errorf("tool %q is served for verb %q: the tool name is the one the verb declares, %q", tl.Name, v.Name, v.MCP)
		}
		delete(served, v.MCP)
	}
	for name := range served {
		t.Errorf("tool %q is served and is no verb in the table", name)
	}
}

// §7.3 leaves no verb on one door only, so a harness with no terminal loses
// nothing. This is the half of that rule the registry can state.
func TestEveryVerbReachesBothDoors(t *testing.T) {
	if len(verbs.All) != len(verbs.MCPTools()) {
		t.Fatalf("%d verbs and %d of them are served over MCP; §7.3 leaves none on the CLI alone",
			len(verbs.All), len(verbs.MCPTools()))
	}
	// Every CLI subcommand path resolves to a verb, so a command tree that
	// grew one outside the registry is caught here too.
	for _, v := range verbs.All {
		got, ok := verbs.ByCLI(v.CLI)
		if !ok || got.Name != v.Name {
			t.Errorf("`hsched %s` does not resolve to verb %q", strings.Join(v.CLI, " "), v.Name)
		}
	}
}

// Same arguments on both doors: the schema is rendered from the same Args the
// CLI reads its positionals from, and nothing else may appear in it.
func TestTheSchemaDeclaresExactlyWhatTheCLITakes(t *testing.T) {
	_, call := inProcessDaemon(t)
	sess := session(t, call)
	tools, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	byName := map[string]*mcp.Tool{}
	for _, tl := range tools.Tools {
		byName[tl.Name] = tl
	}
	for _, v := range verbs.All {
		props := properties(t, byName[v.MCP])
		// The two scope arguments are injected into every tool rather than
		// declared by a verb: they are the CLI's --project and
		// --all-projects, and TestEveryToolTakesTheScopeArguments is what
		// holds them. Everything else in the schema comes from the registry.
		declared := len(props)
		for _, name := range []string{argProject, argAllProjects} {
			if _, ok := props[name]; ok {
				declared--
			}
		}
		if declared != len(v.Args) {
			t.Errorf("tool %q declares %d arguments and the CLI takes %d", v.Name, declared, len(v.Args))
		}
		required := requiredOf(t, byName[v.MCP])
		for _, a := range v.Args {
			raw, ok := props[a.Name]
			if !ok {
				t.Errorf("tool %q is missing the %q argument the CLI takes", v.Name, a.Name)
				continue
			}
			var prop struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(raw, &prop); err != nil {
				t.Fatalf("tool %q, argument %q: %v", v.Name, a.Name, err)
			}
			if prop.Type != jsonType(a.Type) {
				t.Errorf("tool %q declares %q as %q, and the registry says %q",
					v.Name, a.Name, prop.Type, jsonType(a.Type))
			}
			if a.Required != required[a.Name] {
				t.Errorf("tool %q requires %q: %t, and the CLI requires it: %t",
					v.Name, a.Name, required[a.Name], a.Required)
			}
		}
	}
}

// Both doors show the same words, because a verb explained two ways is a verb
// that drifts.
func TestBothDoorsDescribeAVerbTheSameWay(t *testing.T) {
	_, call := inProcessDaemon(t)
	sess := session(t, call)
	tools, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	byName := map[string]*mcp.Tool{}
	for _, tl := range tools.Tools {
		byName[tl.Name] = tl
	}
	root := cli.Root(nil)
	for _, v := range verbs.All {
		if got := byName[v.MCP].Description; got != v.Help() {
			t.Errorf("tool %q is described as %q and the registry says %q", v.MCP, got, v.Help())
		}
		cmd, _, err := root.Find(v.CLI)
		if err != nil {
			t.Fatalf("`hsched %s`: %v", strings.Join(v.CLI, " "), err)
		}
		if cmd.Long != v.Help() {
			t.Errorf("`hsched %s --help` says something the tool description does not", strings.Join(v.CLI, " "))
		}
	}
}

// Both doors build the same request out of the same call. This is what makes
// one verb table a guarantee rather than a convention.
func TestBothDoorsBuildTheSameRequest(t *testing.T) {
	testenv.New(t)
	cases := []struct {
		verb string
		argv []string
		args map[string]any
	}{
		{"doctor", nil, map[string]any{}},
		{"dump", nil, map[string]any{}},
		{"events", []string{"--since", "ev-1", "--limit", "5"}, map[string]any{"since": "ev-1", "limit": float64(5)}},
		{"parked.list", nil, map[string]any{}},
		{"parked.resolve", []string{"--reject", "pk-1"}, map[string]any{"id": "pk-1", "reject": true}},
		{"stop", nil, map[string]any{}},
	}
	for _, tc := range cases {
		v, ok := verbs.ByName(tc.verb)
		if !ok {
			t.Fatalf("no verb named %q", tc.verb)
		}

		fromCLI, _, err := cli.Request(v, tc.argv)
		if err != nil {
			t.Fatalf("cli %s: %v", tc.verb, err)
		}

		var fromMCP protocol.Request
		catch := func(req protocol.Request) (json.RawMessage, error) {
			fromMCP = req
			return json.RawMessage(`{}`), nil
		}
		sess := session(t, catch)
		if _, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: v.MCP, Arguments: tc.args}); err != nil {
			t.Fatalf("mcp %s: %v", tc.verb, err)
		}

		if fromCLI.Verb != fromMCP.Verb {
			t.Errorf("%s: the cli asks for %q and the mcp door for %q", tc.verb, fromCLI.Verb, fromMCP.Verb)
		}
		cliArgs, _ := json.Marshal(fromCLI.Args)
		mcpArgs, _ := json.Marshal(fromMCP.Args)
		if string(cliArgs) != string(mcpArgs) {
			t.Errorf("%s: the cli sends %s and the mcp door %s", tc.verb, cliArgs, mcpArgs)
		}
		if fromCLI.Project != fromMCP.Project || fromCLI.AllProjects != fromMCP.AllProjects {
			t.Errorf("%s: the cli scopes to %q/all=%t and the mcp door to %q/all=%t",
				tc.verb, fromCLI.Project, fromCLI.AllProjects, fromMCP.Project, fromMCP.AllProjects)
		}
		if fromCLI.Pane != fromMCP.Pane {
			t.Errorf("%s: the doors derive different panes, %q and %q", tc.verb, fromCLI.Pane, fromMCP.Pane)
		}
		if fromCLI.Door != cli.Door || fromMCP.Door != Door {
			t.Errorf("%s: the doors do not name themselves: %q and %q", tc.verb, fromCLI.Door, fromMCP.Door)
		}
	}
}

// The same document reaches both callers: what --json prints is what the tool
// hands back, byte for byte.
func TestBothDoorsHandBackTheSameDocument(t *testing.T) {
	_, call := inProcessDaemon(t)

	fromCLI, err := call(protocol.Request{Verb: "doctor", Args: map[string]any{}})
	if err != nil {
		t.Fatalf("cli doctor: %v", err)
	}
	var printed strings.Builder
	if err := cli.Write("doctor", fromCLI, true, &printed); err != nil {
		t.Fatalf("cli render: %v", err)
	}

	sess := session(t, call)
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "doctor"})
	if err != nil {
		t.Fatalf("mcp doctor: %v", err)
	}
	if res.IsError {
		t.Fatalf("mcp doctor: %s", text(res))
	}
	if got, want := text(res), strings.TrimSpace(printed.String()); got != want {
		t.Fatalf("the doors disagree:\nmcp: %s\ncli: %s", got, want)
	}
}

// A refusal is a tool error carrying the daemon's own code, never a protocol
// error the caller cannot read.
func TestARefusalReachesTheCallerAsAToolErrorWithItsCode(t *testing.T) {
	_, call := inProcessDaemon(t)
	sess := session(t, call)

	// A daemon that is not serving cannot be stopped, and that is a state
	// guard rather than a protocol failure.
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "stop"})
	if err != nil {
		t.Fatalf("CallTool returned a protocol error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("stopping a daemon that is not serving succeeded: %s", text(res))
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(text(res)), &body); err != nil {
		t.Fatalf("error body: %v", err)
	}
	// §6.3: the code is one of the contract's nine, and the sub-reason this
	// binary refuses for is the first word of the message.
	if body.Error.Code != string(codes.Conflict) {
		t.Fatalf("error body: %+v", body.Error)
	}
	if !strings.HasPrefix(body.Error.Message, string(codes.NotRunning)+": ") {
		t.Errorf("the message does not carry the sub-reason: %q", body.Error.Message)
	}
	if strings.Contains(body.Error.Message, string(codes.Conflict)) {
		t.Errorf("the message repeats the code: %q", body.Error.Message)
	}
}

// The door holds itself to the schema it published: an argument no verb
// declares is refused rather than dropped in silence.
func TestTheDoorRefusesAnArgumentItsSchemaForbids(t *testing.T) {
	_, call := inProcessDaemon(t)
	sess := session(t, call)

	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "events", Arguments: map[string]any{"since": "ev-1", "cursor": "nope"}})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatalf("the door took an argument it never declared: %s", text(res))
	}
	if !strings.Contains(text(res), string(codes.Invalid)) {
		t.Fatalf("refusal: %s", text(res))
	}
}

func TestARequiredArgumentIsRefusedWhenItIsMissing(t *testing.T) {
	_, call := inProcessDaemon(t)
	sess := session(t, call)

	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "parked_resolve"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError || !strings.Contains(text(res), string(codes.Invalid)) {
		t.Fatalf("parked_resolve with no id: %s", text(res))
	}
}

// An argument is held to the type the schema published, on this door as on the
// other.
func TestAnArgumentIsHeldToItsDeclaredType(t *testing.T) {
	_, call := inProcessDaemon(t)
	sess := session(t, call)
	for _, args := range []map[string]any{
		{"limit": "five"},
		{"since": 7},
	} {
		res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "events", Arguments: args})
		if err != nil {
			t.Fatalf("CallTool %v: %v", args, err)
		}
		if !res.IsError || !strings.Contains(text(res), string(codes.Invalid)) {
			t.Fatalf("%v was taken: %s", args, text(res))
		}
	}
}

// The registration name is the repository an operator wires in, and the tools
// are bare verbs under it: a caller reads them as herdr-sched's doctor, not as
// a name that repeats the binary.
func TestTheServerRegistersUnderTheRepositoryAndServesBareVerbs(t *testing.T) {
	if ServerName != "herdr-sched" {
		t.Fatalf("registered as %q", ServerName)
	}
	for _, v := range verbs.MCPTools() {
		if strings.Contains(v.MCP, "sched") || strings.Contains(v.MCP, "hsched") {
			t.Errorf("tool %q repeats the binary's name", v.MCP)
		}
	}
	for _, want := range []string{"doctor", "events", "parked_list", "stop", "cron:", "trigger:"} {
		if !strings.Contains(Instructions, want) {
			t.Errorf("the instructions do not mention %q", want)
		}
	}
}

// A door is spawned per client session, so it must take nothing from the
// process that spawned it beyond the pane it was told about.
func TestTheDoorKeepsNoStateOfItsOwn(t *testing.T) {
	_, call := inProcessDaemon(t)
	first := New("0.1.0", call)
	second := New("0.1.0", call)
	if first == second {
		t.Fatal("two sessions share one server")
	}
	os.Unsetenv("HERDR_PANE_ID")
	var got protocol.Request
	sess := session(t, func(req protocol.Request) (json.RawMessage, error) {
		got = req
		return json.RawMessage(`{}`), nil
	})
	if _, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "doctor"}); err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if got.Pane != "" {
		t.Fatalf("a door outside a pane claimed pane %q", got.Pane)
	}
}

// Stop is on the door like every other verb. §7.3 makes withholding a verb
// from a principal the §9 gate's job and never a door's; the blast radius is
// real and it belongs where a caller reads it, which is the verb's own Long —
// checked here, so a later edit cannot quietly drop the warning while keeping
// the tool.
func TestStopIsServedWithItsBlastRadiusStated(t *testing.T) {
	v, ok := verbs.ByName("stop")
	if !ok {
		t.Fatal("no stop verb")
	}
	if v.MCP == "" {
		t.Fatal("stop is not served over MCP; §7.3 leaves no verb on one door only")
	}
	for what, want := range map[string]string{
		"that it is not scoped to one schedule": "WHOLE scheduler",
		"what the caller owes first":            "Confirm with the operator",
	} {
		if !strings.Contains(v.Long, want) {
			t.Errorf("stop's description does not say %s (looked for %q)", what, want)
		}
	}
}

// §4.2 on this door: the two scope arguments the CLI's --project and
// --all-projects become are injected into every tool's schema, so a caller on
// MCP can narrow the way a caller on the CLI can.
func TestEveryToolTakesTheScopeArguments(t *testing.T) {
	_, call := inProcessDaemon(t)
	sess := session(t, call)
	tools, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools.Tools) == 0 {
		t.Fatal("no tools served")
	}
	for _, tl := range tools.Tools {
		props := properties(t, tl)
		for name, want := range map[string]string{argProject: "string", argAllProjects: "boolean"} {
			var prop struct {
				Type string `json:"type"`
			}
			raw, ok := props[name]
			if !ok {
				t.Errorf("tool %q takes no %q argument", tl.Name, name)
				continue
			}
			if err := json.Unmarshal(raw, &prop); err != nil {
				t.Fatalf("tool %q, argument %q: %v", tl.Name, name, err)
			}
			if prop.Type != want {
				t.Errorf("tool %q declares %q as %q, want %q", tl.Name, name, prop.Type, want)
			}
		}
	}
}

// §4.2: an explicit project is resolved to §4.1's canonical path HERE, in the
// door, because a relative path is the CALLER's and the daemon's working
// directory is somewhere else entirely.
func TestAnExplicitProjectIsResolvedInTheMCPDoor(t *testing.T) {
	want, err := project.Resolve(project.Options{Explicit: "."})
	if err != nil {
		t.Skipf("no project here: %v", err)
	}
	req := scopedCall(t, map[string]any{argProject: "."})
	if req.Project != want {
		t.Fatalf("project = %q, want the canonical %q", req.Project, want)
	}
	if req.AllProjects {
		t.Fatal("a named project came through as every project")
	}
}

// Naming one project and every project is refused rather than ranked, the same
// way the CLI refuses --project with --all-projects.
func TestNamingOneProjectAndEveryProjectIsRefusedOnTheMCPDoor(t *testing.T) {
	_, call := inProcessDaemon(t)
	sess := session(t, call)
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "doctor", Arguments: map[string]any{argProject: ".", argAllProjects: true}})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError || !strings.Contains(text(res), string(codes.Invalid)) {
		t.Fatalf("naming both: %s", text(res))
	}
}

// The injected arguments are held to the types the schema publishes, the same
// way the declared ones are.
func TestTheScopeArgumentsAreHeldToTheirTypes(t *testing.T) {
	_, call := inProcessDaemon(t)
	sess := session(t, call)
	for _, args := range []map[string]any{
		{argProject: 7},
		{argAllProjects: "yes"},
	} {
		res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "doctor", Arguments: args})
		if err != nil {
			t.Fatalf("CallTool %v: %v", args, err)
		}
		if !res.IsError || !strings.Contains(text(res), string(codes.Invalid)) {
			t.Fatalf("%v was taken: %s", args, text(res))
		}
	}
}

// scopedCall makes one call through the door and hands back the request it
// built, so a test can read what the scope resolved to.
func scopedCall(t *testing.T, args map[string]any) protocol.Request {
	t.Helper()
	var got protocol.Request
	sess := session(t, func(req protocol.Request) (json.RawMessage, error) {
		got = req
		return json.RawMessage(`{}`), nil
	})
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "doctor", Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %v: %v", args, err)
	}
	if res.IsError {
		t.Fatalf("CallTool %v: %s", args, text(res))
	}
	return got
}

// properties reads one tool's published schema properties.
func properties(t *testing.T, tl *mcp.Tool) map[string]json.RawMessage {
	t.Helper()
	if tl == nil {
		t.Fatal("no tool served")
	}
	raw, err := json.Marshal(tl.InputSchema)
	if err != nil {
		t.Fatalf("schema for %q: %v", tl.Name, err)
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("schema for %q: %v", tl.Name, err)
	}
	return schema.Properties
}

// requiredOf reads which of one tool's arguments the schema marks required.
func requiredOf(t *testing.T, tl *mcp.Tool) map[string]bool {
	t.Helper()
	raw, err := json.Marshal(tl.InputSchema)
	if err != nil {
		t.Fatalf("schema for %q: %v", tl.Name, err)
	}
	var schema struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("schema for %q: %v", tl.Name, err)
	}
	out := map[string]bool{}
	for _, name := range schema.Required {
		out[name] = true
	}
	return out
}
