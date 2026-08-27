package mcpdoor

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/husniadil/herdr-sched/internal/cli"
	"github.com/husniadil/herdr-sched/internal/project"
	"github.com/husniadil/herdr-sched/internal/protocol"
)

// §4.2 on the door that has no working directory of its own worth speaking
// for: `hsched mcp --project <dir>` is read once from the server command, the
// way the §7.5 declaration is, and it is the DEFAULT rather than the answer.
// A call that names a project acts in that one, and `all_projects` is
// untouched — the door's default is not a second project to rank against it.
func TestTheDoorsProjectIsTheDefaultAndAnExplicitOneWins(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "")
	t.Setenv(project.EnvContext, "")
	door := t.TempDir()
	other := t.TempDir()

	for name, tc := range map[string]struct {
		opt      Options
		args     map[string]any
		wantProj string
		wantAll  bool
	}{
		"the door's project, when the call names none": {
			Options{Project: door}, nil, scopeOf(t, door), false},
		"the call's own project, which wins": {
			Options{Project: door}, map[string]any{argProject: other}, scopeOf(t, other), false},
		"the working directory, when the door was given none": {
			Options{}, nil, scopeOf(t, ""), false},
		"all_projects, which the door's project does not contradict": {
			Options{Project: door}, map[string]any{argAllProjects: true}, "", true},
	} {
		var seen []protocol.Request
		var mu sync.Mutex
		spy := func(req protocol.Request) (json.RawMessage, error) {
			mu.Lock()
			seen = append(seen, req)
			mu.Unlock()
			return json.RawMessage(`{}`), nil
		}
		sess := sessionWith(t, spy, tc.opt)
		res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "job_list", Arguments: tc.args})
		if err != nil {
			t.Fatalf("%s: CallTool: %v", name, err)
		}
		if res.IsError {
			t.Fatalf("%s: job_list: %s", name, text(res))
		}
		mu.Lock()
		if len(seen) != 1 {
			mu.Unlock()
			t.Fatalf("%s: %d requests reached the daemon, want 1", name, len(seen))
		}
		got := seen[0]
		mu.Unlock()
		if got.Project != tc.wantProj {
			t.Errorf("%s: project = %q, want %q", name, got.Project, tc.wantProj)
		}
		if got.AllProjects != tc.wantAll {
			t.Errorf("%s: all_projects = %v, want %v", name, got.AllProjects, tc.wantAll)
		}
	}
}

// scopeOf is what the door itself would resolve a named directory to, so the
// expectation is the §4.1 canonical form rather than the path this test typed.
func scopeOf(t *testing.T, dir string) string {
	t.Helper()
	proj, _, err := cli.Scope(dir, false, nil)
	if err != nil {
		t.Fatalf("scope %q: %v", dir, err)
	}
	return proj
}
