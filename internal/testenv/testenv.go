// Package testenv builds the throwaway world a layer-2 test runs in: stand-in
// sibling binaries first on PATH, and temporary state and config directories.
// A test never touches the operator's live Herdr, their boards, their mailbox,
// their config or their state (§12.3).
package testenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/husniadil/herdr-sched/internal/config"
)

// Fake is one directory of stand-in binaries, and the only place a test can
// resolve one from. Scripts find it again as $SCHED_FAKE_DIR.
type Fake struct{ Dir string }

// FakeDirEnv is where a fake script finds the directory it lives in, for a
// counter or a canned document it needs to keep between calls.
const FakeDirEnv = "SCHED_FAKE_DIR"

// Siblings are the binaries this plugin shells out to (note 2: it calls the
// siblings' CLIs with --json, never their sockets, so each daemon stays the
// only writer of its own store). A fake is written for the ones a case needs;
// this list is what a case picks from, and what the "not found" failure below
// is measured against.
var Siblings = []string{"htask", "hmail", "hdis", "herdr"}

// New creates the directory and makes it the only place a test can resolve a
// binary from, apart from the system directories the scripts themselves need.
// Replacing PATH rather than prepending to it is the point: a call whose fake
// was never written fails as "not found" instead of quietly reaching the
// operator's real board, mailbox, dispatcher or Herdr server.
//
// It also points the plugin's own state and config at temporary directories,
// because a test that wrote to the live ones would be editing the operator's
// schedules.
func New(t *testing.T) *Fake {
	t.Helper()
	dir := t.TempDir()
	sep := string(os.PathListSeparator)
	t.Setenv("PATH", dir+sep+"/usr/bin"+sep+"/bin")
	t.Setenv(FakeDirEnv, dir)
	t.Setenv(config.EnvPrefix+"STATE_DIR", ShortDir(t))
	t.Setenv(config.EnvPrefix+"CONFIG_DIR", t.TempDir())
	// A pane a test can be someone in, so a derived principal is not `unknown`
	// by accident in every case (§3.2).
	t.Setenv("HERDR_PANE_ID", "wT:p1")
	return &Fake{Dir: dir}
}

// Bin writes an executable /bin/sh script under the given name. The script
// runs with $SCHED_FAKE_DIR set, and "$@" carries the argv it was called with.
func (f *Fake) Bin(t *testing.T, name, script string) {
	t.Helper()
	path := filepath.Join(f.Dir, name)
	body := "#!/bin/sh\n" +
		"printf '%s\\037' \"$@\" >> \"$" + FakeDirEnv + "/calls.log\"\n" +
		"printf '\\n' >> \"$" + FakeDirEnv + "/calls.log\"\n" + script + "\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", name, err)
	}
}

// Argv returns the argument vector of every call to every fake binary, in
// order and with each argument whole, spaces included.
func (f *Fake) Argv(t *testing.T) [][]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.Dir, "calls.log"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read the fake call log: %v", err)
	}
	var out [][]string
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		out = append(out, strings.Split(strings.TrimSuffix(line, "\x1f"), "\x1f"))
	}
	return out
}

// Calls returns the same calls with each argv joined by a space, which reads
// better in an assertion when no argument contains one.
func (f *Fake) Calls(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, argv := range f.Argv(t) {
		out = append(out, strings.Join(argv, " "))
	}
	return out
}

// Path names a file inside the fake's directory, for a script that needs to
// keep a counter or a canned document between calls.
func (f *Fake) Path(name string) string { return filepath.Join(f.Dir, name) }

// Write puts a file in the fake's directory for a script to read.
func (f *Fake) Write(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(f.Path(name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// Config writes this plugin's own TOML config into the throwaway config dir,
// where the daemon under test will read it.
func (f *Fake) Config(t *testing.T, body string) string {
	t.Helper()
	path := config.ConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("config dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// Gate writes a policy gate script that answers every check the same way, and
// configures this plugin to use it (§9.2).
func (f *Fake) Gate(t *testing.T, decision, reason string) {
	t.Helper()
	path := filepath.Join(f.Dir, "gate")
	body := "#!/bin/sh\ncat >/dev/null\nprintf '{\"decision\":\"" + decision + "\",\"reason\":\"" + reason + "\"}\\n'\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write the gate: %v", err)
	}
	f.Config(t, "gate_command = [\""+path+"\"]\n")
}

// SkipUnlessFull skips a layer-2 test in the fast loop. `make test` runs
// -short and deliberately leaves out every case that starts a daemon, walks
// the socket, or shells out to a fake; `make test-full` runs all of it (§12.1).
func SkipUnlessFull(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("layer 2 (§12.1): runs in make test-full")
	}
}

// ShortDir is a temp dir with a short path. A Unix socket path has a hard
// length limit in the kernel, and t.TempDir()'s name is the test's own, which
// on a long test name is enough to cross it.
func ShortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "hsched")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}
