// Package project resolves the unit of data scoping (§4): the git common
// dir's parent, so every worktree of a repository shares one project, and a
// plain directory is its own project by canonical path.
package project

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/husniadil/herdr-sched/internal/codes"
)

// Options is the §4.2 resolution order, as data.
type Options struct {
	// Explicit is --project, which wins.
	Explicit string
	// Cwd is the caller's working directory, the last resort.
	Cwd string
	// Warn receives the one line Resolve has to say for itself: that Herdr set
	// a context document this build cannot read. A door passes os.Stderr; a
	// caller that does not want to hear it passes nil.
	Warn io.Writer
}

// EnvContext is the variable Herdr fills in for a plugin command (§4.2). It is
// named here so a warning about it and a search for it agree.
const EnvContext = "HERDR_PLUGIN_CONTEXT_JSON"

// contextWarned keeps the warning to once per process, the same way the
// door/daemon skew warning is kept: it is worth saying, and worth saying once.
var contextWarned sync.Once

// Resolve answers §4.2: explicit --project, then HERDR_PLUGIN_CONTEXT_JSON's
// focused pane cwd or workspace cwd, then the caller's working directory.
//
// A context document that cannot be READ is the one case here that is not
// benign. In a plugin pane that variable is the only thing that says which
// project the operator is looking at — a plugin pane's working directory is
// the SERVER's, not the focused pane's — so falling back silently scopes the
// call somewhere else and looks exactly like an empty answer. An absent
// variable and a well-formed document with no cwd in it are both documented
// and stay silent; only a parse failure says anything.
func Resolve(o Options) (string, error) {
	var unreadable error
	dir := o.Explicit
	if dir == "" {
		dir, unreadable = fromHerdrContext()
	}
	if dir == "" {
		dir = o.Cwd
	}
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		dir = wd
	}
	proj, err := canonical(dir)
	if err != nil {
		return "", err
	}
	if unreadable != nil && o.Warn != nil {
		contextWarned.Do(func() {
			// The value itself is not printed: it can be a long document, it
			// is not what the operator needs, and the parse error already says
			// where it went wrong.
			fmt.Fprintf(o.Warn,
				"hsched: %s is set but could not be read (%v); this call is scoped to %s, taken from the working directory instead\n",
				EnvContext, unreadable, proj)
		})
	}
	return proj, nil
}

// herdrContext is the part of HERDR_PLUGIN_CONTEXT_JSON this plugin reads.
// Herdr's document is ONE FLAT OBJECT of snake_case keys (its
// PluginInvocationContext: workspace_cwd, focused_pane_cwd, tab_id, …), not a
// nested one; Herdr owns the full shape and omits what it has no answer for,
// so the plugin reads the two cwds §4.2 names and ignores the rest.
type herdrContext struct {
	FocusedPaneCwd string `json:"focused_pane_cwd"`
	WorkspaceCwd   string `json:"workspace_cwd"`
}

// fromHerdrContext returns the cwd Herdr named, and separately the reason the
// document could not be read — which is not the same as it having nothing to
// say. Herdr omits what it has no answer for, so an empty result with a nil
// error is ordinary.
func fromHerdrContext() (string, error) {
	raw := os.Getenv(EnvContext)
	if raw == "" {
		return "", nil
	}
	var c herdrContext
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return "", err
	}
	if c.FocusedPaneCwd != "" {
		return c.FocusedPaneCwd, nil
	}
	return c.WorkspaceCwd, nil
}

// canonical turns a directory into the project key: the parent of the git
// common dir when there is one, else the directory itself. Symlinks are
// resolved either way (§4.1).
func canonical(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	abs, err = resolveLinks(abs)
	if err != nil {
		return "", err
	}
	if root, ok := gitCommonRoot(abs); ok {
		return root, nil
	}
	return abs, nil
}

// gitCommonRoot answers §4.1: the repository a directory belongs to. A
// directory that is not a repository, or a host with no git, simply is not a
// repository here — that is the documented fallback, not a failure.
//
// §4.1 says "the git common dir's parent", and that is right for the case it
// was written for: an ordinary clone and every linked worktree of it report
// <repo>/.git, whose parent is the repository, which is why worktrees share
// one project. It is wrong everywhere else, and the divergence is recorded
// under §4.1 in docs/contract-notes.md:
//
//   - in a submodule the common dir is <super>/.git/modules/<name>, so the
//     parent is <super>/.git/modules — a path inside git's internals that
//     EVERY submodule of that superproject shares, and that neither the
//     superproject nor the submodule can reach from where it stands.
//   - with --separate-git-dir the common dir is wherever the operator put it,
//     so the parent has nothing to do with the working tree; if git dirs are
//     kept together, which is the usual reason to use the flag, every such
//     clone collapses into one project.
//
// So the parent is taken only when the common dir IS a .git, and otherwise the
// working tree is asked for directly. A bare repository has no working tree and
// falls through to the not-a-repository answer.
func gitCommonRoot(dir string) (string, bool) {
	common, ok := gitPath(dir, "--git-common-dir")
	if !ok {
		return "", false
	}
	if filepath.Base(common) == ".git" {
		root, err := resolveLinks(filepath.Dir(common))
		return root, err == nil
	}
	top, ok := gitPath(dir, "--show-toplevel")
	if !ok {
		return "", false
	}
	root, err := resolveLinks(top)
	return root, err == nil
}

// gitPath asks git for one path, absolute, or reports that it could not.
func gitPath(dir, what string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--path-format=absolute", what)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", false
	}
	path := strings.TrimSpace(out.String())
	return path, path != ""
}

// resolveLinks is §4.1's "symlinks are resolved", including for a path that
// does not exist yet. EvalSymlinks cannot resolve what is not there, and
// taking the path unresolved made the SAME directory two different projects
// depending on whether it had been created — on macOS, /var/tmp/x before and
// /private/var/tmp/x after, with everything filed under the first stranded the
// moment it was. So a missing path takes its nearest existing ancestor's
// canonical form with the missing tail put back.
//
// Anything that is not "it is not there" fails: a permission error or a
// symlink loop means this path has no key we can be sure of, and quietly
// inventing a second one is the failure being fixed here.
func resolveLinks(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", codes.Errorf(codes.Usage, "cannot resolve %s: %v", path, err)
	}
	parent, last := filepath.Dir(path), filepath.Base(path)
	if parent == path || last == "." || last == string(filepath.Separator) {
		// Nothing left to climb: an absolute path whose root does not resolve
		// is as canonical as it is going to get.
		return path, nil
	}
	up, err := resolveLinks(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(up, last), nil
}

// DisplayName is the basename a human reads. It is never a key (§4.1).
func DisplayName(project string) string { return filepath.Base(project) }
