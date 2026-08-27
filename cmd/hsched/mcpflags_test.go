package main

import (
	"testing"

	"github.com/husniadil/herdr-sched/internal/mcpdoor"
)

// §4.2: `hsched mcp --project <dir>` has to REACH the door. --project is a
// persistent flag on the root, so it parses on `hsched mcp` whether or not
// anything reads it, and a flag that parses and is then dropped is worse than
// one that is refused: the operator is told nothing.
func TestTheMCPCommandCarriesTheGlobalProjectIntoTheDoor(t *testing.T) {
	for name, tc := range map[string]struct {
		argv []string
		want string
	}{
		"the project the operator named":     {[]string{"mcp", "--project", "/x"}, "/x"},
		"none, when the operator named none": {[]string{"mcp"}, ""},
	} {
		var got mcpdoor.Options
		root := newRootCmdWith(func(opt mcpdoor.Options) error {
			got = opt
			return nil
		})
		root.SetArgs(tc.argv)
		if err := root.Execute(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.Project != tc.want {
			t.Errorf("%s: the door was started with project %q, want %q", name, got.Project, tc.want)
		}
	}
}
