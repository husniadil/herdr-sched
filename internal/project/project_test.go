package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// §4.1: the project key is the repository, so every worktree of it answers
// with one project rather than one each.
func TestEveryWorktreeOfARepositoryIsOneProject(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git here")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init", "-q", "-b", "main")
	git(t, repo, "config", "user.email", "test@example.com")
	git(t, repo, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(repo, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "f")
	git(t, repo, "commit", "-qm", "root")

	want, err := Resolve(Options{Explicit: repo})
	if err != nil {
		t.Fatalf("Resolve the repo: %v", err)
	}

	// A subdirectory of the checkout answers with the repository.
	sub := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := Resolve(Options{Explicit: sub}); err != nil || got != want {
		t.Errorf("a subdirectory resolved to %q / %v, want %q", got, err, want)
	}

	// And so does a linked worktree.
	tree := filepath.Join(root, "tree")
	git(t, repo, "worktree", "add", "-q", "-b", "side", tree)
	if got, err := Resolve(Options{Explicit: tree}); err != nil || got != want {
		t.Errorf("a worktree resolved to %q / %v, want the repository %q", got, err, want)
	}
}

// A directory that is not a repository is its own project by canonical path.
// That is the documented fallback, not a failure.
func TestADirectoryThatIsNotARepositoryIsItsOwnProject(t *testing.T) {
	dir := t.TempDir()
	got, err := Resolve(Options{Explicit: dir})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("project = %q, which is not an absolute path", got)
	}
	// §4.1 resolves symlinks, so the same directory is one key however it was
	// reached. On macOS /var is a symlink to /private/var, which is exactly
	// the case that made one directory two projects.
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != resolved {
		t.Fatalf("project = %q, want the symlink-resolved %q", got, resolved)
	}
}

// A path that does not exist yet still has one key, taken from its nearest
// existing ancestor: the alternative made the SAME directory two projects
// depending on whether it had been created.
func TestAPathThatDoesNotExistYetStillHasOneKey(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "not", "there")
	got, err := Resolve(Options{Explicit: missing})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !strings.HasSuffix(got, filepath.Join("not", "there")) {
		t.Fatalf("project = %q, want the missing tail kept", got)
	}
	if err := os.MkdirAll(missing, 0o755); err != nil {
		t.Fatal(err)
	}
	after, err := Resolve(Options{Explicit: missing})
	if err != nil {
		t.Fatalf("Resolve after creating it: %v", err)
	}
	if after != got {
		t.Fatalf("the same directory is %q before it exists and %q after", got, after)
	}
}

// §4.2: Herdr's context document names the project when there is one, and an
// absent variable is ordinary rather than a failure.
func TestHerdrsContextDocumentNamesTheProject(t *testing.T) {
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvContext, `{"focused_pane_cwd":"`+dir+`","workspace_cwd":"/somewhere/else"}`)
	got, err := Resolve(Options{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != resolved {
		t.Fatalf("project = %q, want the focused pane's %q", got, resolved)
	}
	// An explicit project wins over it.
	other := t.TempDir()
	otherResolved, _ := filepath.EvalSymlinks(other)
	if got, err := Resolve(Options{Explicit: other}); err != nil || got != otherResolved {
		t.Fatalf("an explicit project resolved to %q / %v, want %q", got, err, otherResolved)
	}
}

// The display name is for a human and is never a key (§4.1).
func TestTheDisplayNameIsNeverTheKey(t *testing.T) {
	if got := DisplayName("/src/husniadil/herdr-sched"); got != "herdr-sched" {
		t.Fatalf("display name = %q", got)
	}
}
