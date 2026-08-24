//go:build e2e

// Package e2e is layer 3: the SHIPPED `hsched` binary, over a real socket,
// through the plugin's own start and stop scripts, against a throwaway state
// dir.
//
// It is out of `make test-full` on purpose. It builds and runs the binary and
// its scripts, which is a release concern rather than a per-push one, and it
// skips loudly naming what was missing rather than passing quietly.
// SCHED_E2E_REQUIRED=1 turns the skip into a failure, which is what
// `make release-check` does before a tag.
//
// Nothing here may reach the operator's live Herdr, config or state: HOME, the
// XDG bases, this plugin's own dirs and PATH are all temporary.
package e2e

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// RequiredEnv turns this suite's skip into a failure. A release must not be
// cut on a suite that silently did not run.
const RequiredEnv = "SCHED_E2E_REQUIRED"

// world is one throwaway installation of the plugin.
type world struct {
	t     *testing.T
	root  string
	repo  string
	state string
	// webhook is the loopback address this world's inbound door gets, so the
	// suite never takes the port an operator's own daemon is on.
	webhook string
	env     []string
}

func setup(t *testing.T) *world {
	t.Helper()
	// A unix socket path has a hard length limit in the kernel, and macOS's
	// TMPDIR spends most of it before the daemon gets a name.
	root, err := os.MkdirTemp("/tmp", "hse2e")
	if err != nil {
		t.Fatalf("temp root: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })

	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}
	w := &world{t: t, root: root, repo: repo, state: filepath.Join(root, "state"), webhook: freePort(t)}

	// The binary the manifest's [[build]] step produces, at the path the
	// scripts look for it. This drives what ships, not `go run`.
	bin := filepath.Join(repo, "bin", "hsched")
	build := exec.Command("go", "build", "-o", bin, "./cmd/hsched")
	build.Dir = repo
	if out, err := build.CombinedOutput(); err != nil {
		missing(t, "the binary could not be built: %v\n%s", err, out)
	}
	if _, err := os.Stat(bin); err != nil {
		missing(t, "bin/hsched is not there after the build: %v", err)
	}

	home := filepath.Join(root, "home")
	config := filepath.Join(root, "config")
	for _, d := range []string{home, config, w.state} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	w.env = append(os.Environ(),
		"HOME="+home,
		"XDG_STATE_HOME="+filepath.Join(root, "xdg-state"),
		"XDG_CONFIG_HOME="+filepath.Join(root, "xdg-config"),
		"SCHED_STATE_DIR="+w.state,
		"SCHED_CONFIG_DIR="+config,
		// The inbound door gets a port of its OWN. The default is a fixed
		// loopback port, and a suite that took it would be fighting the
		// operator's own daemon for it — and, worse, could answer requests
		// meant for theirs.
		"SCHED_WEBHOOK_ADDR="+w.webhook,
		// A pane, so a derived principal is not `unknown` in every call.
		"HERDR_PANE_ID=wE:p1",
	)
	return w
}

// freePort is a loopback address nothing is listening on yet: it binds one,
// reads what the kernel gave, and lets it go. The daemon takes it a moment
// later. Nothing else in this suite opens a port, so the window is only a
// race against the rest of the machine.
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find a free port: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("release %s: %v", addr, err)
	}
	return addr
}

// missing skips loudly, naming what was not there — unless a release is being
// cut, in which case it fails.
func missing(t *testing.T, format string, a ...any) {
	t.Helper()
	if os.Getenv(RequiredEnv) != "" {
		t.Fatalf(RequiredEnv+" is set and "+format, a...)
	}
	t.Skipf("layer 3 needs what it could not find: "+format, a...)
}

// run drives the shipped binary and returns its stdout, stderr and exit status.
func (w *world) run(args ...string) (string, string, int) {
	w.t.Helper()
	cmd := exec.Command(filepath.Join(w.repo, "bin", "hsched"), args...)
	cmd.Env = w.env
	cmd.Dir = w.root
	var out, errOut strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()
	status := 0
	if ee, ok := err.(*exec.ExitError); ok {
		status = ee.ExitCode()
	} else if err != nil {
		w.t.Fatalf("run hsched %v: %v", args, err)
	}
	return out.String(), errOut.String(), status
}

// script drives one of the plugin's own scripts, the way Herdr runs them.
func (w *world) script(name string) (string, int) {
	w.t.Helper()
	cmd := exec.Command(filepath.Join(w.repo, "scripts", name))
	cmd.Env = w.env
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	status := 0
	if ee, ok := err.(*exec.ExitError); ok {
		status = ee.ExitCode()
	} else if err != nil {
		w.t.Fatalf("run scripts/%s: %v", name, err)
	}
	return out.String(), status
}

func (w *world) socketLive() bool {
	_, err := os.Stat(filepath.Join(w.state, "sched.sock"))
	return err == nil
}

func (w *world) waitFor(what string, cond func() bool) {
	w.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	w.t.Fatalf("timed out waiting for %s", what)
}

// A job written through the shipped CLI is held by the daemon and read back
// by the next call. The argument that matters here is `--args`: it is one
// JSON object on a shell line, and the quoting either survives argv or it
// does not — which is a thing only layer 3 can say.
func TestAJobIsWrittenAndReadBackThroughTheShippedBinary(t *testing.T) {
	w := setup(t)
	defer w.script("stop.sh")

	out, errOut, status := w.run("job", "add", "nightly-sweep", "0 3 * * *", "task",
		"--args", `{"title":"sweep the board","priority":2}`, "--json")
	if status != 0 {
		t.Fatalf("job add exited %d: %s%s", status, out, errOut)
	}
	var added struct {
		Job struct {
			ID     string `json:"id"`
			Next   string `json:"next"`
			Action struct {
				Kind string            `json:"kind"`
				Args map[string]string `json:"args"`
			} `json:"action"`
		} `json:"job"`
		Changed bool `json:"changed"`
	}
	if err := json.Unmarshal([]byte(out), &added); err != nil {
		t.Fatalf("job add printed %q: %v", out, err)
	}
	if !added.Changed || added.Job.ID != "nightly-sweep" {
		t.Fatalf("job add = %+v", added)
	}
	if added.Job.Action.Args["title"] != "sweep the board" {
		t.Errorf("the title reached the row as %q", added.Job.Action.Args["title"])
	}
	if added.Job.Action.Args["priority"] != "2" {
		t.Errorf("a number in --args reached the row as %q", added.Job.Action.Args["priority"])
	}
	if added.Job.Next == "" {
		t.Error("the row carries no next instant")
	}

	// The daemon it autostarted is the one that answers the next call.
	out, errOut, status = w.run("job", "list")
	if status != 0 {
		t.Fatalf("job list exited %d: %s%s", status, out, errOut)
	}
	if !strings.Contains(out, "nightly-sweep") || !strings.Contains(out, "0 3 * * *") {
		t.Fatalf("job list printed %q", out)
	}

	// An expression that does not parse is refused when the row is written,
	// with the §6.3 status for a caller's own input.
	out, _, status = w.run("job", "add", "broken", "0 3 * *", "task",
		"--args", `{"title":"never"}`, "--json")
	if status != 2 {
		t.Fatalf("a bad expression exited %d, want 2 (USAGE): %s", status, out)
	}
	if !strings.Contains(out, "USAGE") {
		t.Errorf("the refusal printed %q", out)
	}
}

// The shipped binary answers with its own version and the contract it
// satisfies, with no daemon running at all (§13.4).
func TestTheShippedBinaryNamesItselfAndItsContract(t *testing.T) {
	w := setup(t)
	out, _, status := w.run("version", "--json")
	if status != 0 {
		t.Fatalf("version exited %d: %s", status, out)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("version --json printed %q: %v", out, err)
	}
	for _, key := range []string{"version", "contract", "plugin"} {
		if got[key] == "" {
			t.Errorf("version --json says nothing about %q: %v", key, got)
		}
	}
	if got["plugin"] != "herdr-sched" {
		t.Errorf("plugin = %q", got["plugin"])
	}
}

// The daemon comes up and goes down through the plugin's own scripts, which is
// how Herdr starts and stops it, on a throwaway state dir.
func TestTheDaemonStartsAndStopsThroughTheScripts(t *testing.T) {
	w := setup(t)

	if out, status := w.script("start.sh"); status != 0 {
		t.Fatalf("start.sh exited %d: %s", status, out)
	}
	w.waitFor("the daemon's socket", w.socketLive)

	out, _, status := w.run("doctor", "--json")
	if status != 0 {
		t.Fatalf("doctor exited %d: %s", status, out)
	}
	var rep struct {
		Version  string `json:"version"`
		Contract string `json:"contract"`
		Socket   string `json:"socket"`
		StateDir string `json:"state_dir"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("doctor printed %q: %v", out, err)
	}
	// The done-when of the scaffold: doctor answers with the version and the
	// contract, off the running daemon.
	if rep.Version == "" || rep.Contract == "" {
		t.Fatalf("doctor = %+v", rep)
	}
	if rep.StateDir != w.state {
		t.Fatalf("the daemon resolved state dir %q, and this world is %q", rep.StateDir, w.state)
	}

	if out, status := w.script("stop.sh"); status != 0 {
		t.Fatalf("stop.sh exited %d: %s", status, out)
	}
	w.waitFor("the socket to go", func() bool { return !w.socketLive() })
	// The lock goes with it, so nothing left behind says a daemon is here.
	if _, err := os.Stat(filepath.Join(w.state, "sched.lock")); !os.IsNotExist(err) {
		t.Error("the lock file is still there after stop.sh")
	}
	// And stopping what is already stopped is success: the state the script
	// asks for already holds.
	if out, status := w.script("stop.sh"); status != 0 {
		t.Fatalf("a second stop.sh exited %d: %s", status, out)
	}
}

// A CLI call with no daemon listening starts one and is answered, rather than
// failing (§2.1). `stop` is the one verb that refuses instead.
func TestACallAutostartsTheDaemonAndStopDoesNot(t *testing.T) {
	w := setup(t)
	if w.socketLive() {
		t.Fatal("a socket exists before anything ran")
	}
	out, errOut, status := w.run("stop")
	if status != 6 {
		t.Fatalf("stop with no daemon exited %d (want the contract's CONFLICT, 6): %s %s", status, out, errOut)
	}
	if !strings.Contains(errOut, "NOT_RUNNING") {
		t.Errorf("stop did not name its sub-reason: %q", errOut)
	}
	if w.socketLive() {
		t.Fatal("stop started a daemon just to stop it")
	}

	if out, _, status := w.run("events"); status != 0 {
		t.Fatalf("events exited %d: %s", status, out)
	}
	w.waitFor("the autostarted daemon", w.socketLive)
	w.script("stop.sh")
}

// §6.2 and §6.3 over the shipped binary: a failure is one envelope on stdout
// with --json, and the exit status is the one the contract fixes for the code.
func TestAFailureIsOneDocumentWithTheContractsExitStatus(t *testing.T) {
	w := setup(t)
	out, errOut, status := w.run("--json", "stop")
	if status != 6 {
		t.Fatalf("exit = %d, want the contract's CONFLICT, 6", status)
	}
	if strings.TrimSpace(errOut) != "" {
		t.Errorf("with --json the failure also went to stderr: %q", errOut)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &body); err != nil {
		t.Fatalf("the failure document was %q: %v", out, err)
	}
	if body.Error.Code != "CONFLICT" {
		t.Fatalf("code = %q", body.Error.Code)
	}
	if strings.Contains(body.Error.Message, "CONFLICT") {
		t.Errorf("the message repeats the code: %q", body.Error.Message)
	}

	// A caller's own typo is USAGE, exit 2, and not UNAVAILABLE.
	_, _, status = w.run("--json", "nonsense")
	if status != 2 {
		t.Fatalf("an unknown subcommand exited %d, want USAGE's 2", status)
	}
}

// §7.1: the MCP door is a subcommand of the same binary, and it serves the
// same verbs over a real stdio session.
func TestTheMCPDoorServesTheSameVerbsOverStdio(t *testing.T) {
	w := setup(t)
	cmd := exec.Command(filepath.Join(w.repo, "bin", "hsched"), "mcp")
	cmd.Env = w.env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the door: %v", err)
	}
	t.Cleanup(func() {
		stdin.Close()
		cmd.Process.Kill()
		cmd.Wait()
		w.script("stop.sh")
	})

	send := func(v any) {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := stdin.Write(append(raw, '\n')); err != nil {
			t.Fatalf("write to the door: %v", err)
		}
	}
	dec := json.NewDecoder(stdout)
	read := func() map[string]any {
		var msg map[string]any
		if err := dec.Decode(&msg); err != nil {
			t.Fatalf("read from the door: %v", err)
		}
		return msg
	}

	send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "e2e", "version": "0"},
	}})
	read()
	send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})

	raw, err := json.Marshal(read())
	if err != nil {
		t.Fatal(err)
	}
	var listed struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatalf("tools/list answered %s: %v", raw, err)
	}
	served := map[string]bool{}
	for _, tool := range listed.Result.Tools {
		served[tool.Name] = true
	}
	// The same list the parity test pins, reached through the real door.
	for _, want := range []string{
		"doctor", "dump", "events",
		"job_add", "job_list", "job_remove", "job_enable", "job_disable",
		"parked_list", "parked_resolve", "stop",
	} {
		if !served[want] {
			t.Errorf("the shipped MCP door does not serve %q; it serves %v", want, served)
		}
	}
}

// The store is written where the config says, and `dump` names that path, so a
// reader who wants the document without this binary knows where to look.
func TestTheStoreIsWrittenWhereDumpSaysItIs(t *testing.T) {
	w := setup(t)
	out, _, status := w.run("dump", "--json")
	if status != 0 {
		t.Fatalf("dump exited %d: %s", status, out)
	}
	defer w.script("stop.sh")
	var rep struct {
		Path  string `json:"path"`
		Store struct {
			Version      int   `json:"version"`
			Parked       []any `json:"parked"`
			ParkedEvents []any `json:"parked_events"`
		} `json:"store"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("dump printed %q: %v", out, err)
	}
	if want := filepath.Join(w.state, "sched.json"); rep.Path != want {
		t.Fatalf("dump names %q, want %q", rep.Path, want)
	}
	if rep.Store.Version == 0 {
		t.Fatalf("the store has no version: %s", out)
	}
	// Empty lists rather than nulls, so a reader can tell "none" from "this
	// daemon could not say". An entity's trail ships beside it from day one.
	if rep.Store.Parked == nil || rep.Store.ParkedEvents == nil {
		t.Fatalf("dump rendered a null where a list belongs: %s", out)
	}
}

// The trigger half through the shipped binary: a webhook is written, its
// secret is answered ONCE, the door it names verifies a real request, and an
// unsigned one is refused. This is the case the layer-2 tests cannot make —
// the port comes from the daemon the binary autostarted, and the request is a
// real HTTP call from outside the process.
func TestAWebhookTriggerIsWrittenAndFiredThroughTheShippedBinary(t *testing.T) {
	w := setup(t)
	defer w.script("stop.sh")

	out, errOut, status := w.run("trigger", "add", "deploy", "webhook", "shell",
		"--args", `{"command":"true"}`, "--cooldown", "60", "--json")
	if status != 0 {
		t.Fatalf("trigger add exited %d: %s%s", status, out, errOut)
	}
	var added struct {
		Trigger struct {
			ID  string `json:"id"`
			URL string `json:"url"`
		} `json:"trigger"`
		Secret  string `json:"secret"`
		Changed bool   `json:"changed"`
	}
	if err := json.Unmarshal([]byte(out), &added); err != nil {
		t.Fatalf("trigger add printed %q: %v", out, err)
	}
	if !added.Changed || added.Trigger.ID != "deploy" {
		t.Fatalf("trigger add = %+v", added)
	}
	if added.Secret == "" {
		t.Fatal("trigger add answered no secret, and there is no second chance to read one")
	}
	if added.Trigger.URL == "" {
		t.Fatal("trigger add answered no URL, so nothing could ever call it")
	}

	// The secret was shown there and is shown nowhere else, ever.
	for _, read := range [][]string{{"trigger", "list", "--json"}, {"dump", "--json"}, {"events", "--json"}} {
		out, errOut, status := w.run(read...)
		if status != 0 {
			t.Fatalf("%v exited %d: %s%s", read, status, out, errOut)
		}
		if strings.Contains(out, added.Secret) {
			t.Errorf("`hsched %s` printed the webhook secret", strings.Join(read, " "))
		}
	}

	// A real request, signed the way the README tells an operator to sign one.
	body := []byte(`{"ref":"refs/heads/main"}`)
	mac := hmac.New(sha256.New, []byte(added.Secret))
	mac.Write(body)
	status, answer := w.post(added.Trigger.URL, "sha256="+hex.EncodeToString(mac.Sum(nil)), body)
	if status != http.StatusAccepted {
		t.Fatalf("a verified request answered %d: %s", status, answer)
	}

	// The same body again, inside the cooldown, is refused loudly.
	status, answer = w.post(added.Trigger.URL, "sha256="+hex.EncodeToString(mac.Sum(nil)), body)
	if status != http.StatusTooManyRequests {
		t.Fatalf("a replay inside the cooldown answered %d: %s", status, answer)
	}

	// And an unsigned one never gets that far.
	status, answer = w.post(added.Trigger.URL, "", body)
	if status != http.StatusForbidden {
		t.Fatalf("an unsigned request answered %d: %s", status, answer)
	}

	// Every one of the three is on the trail, in this plugin's own words.
	out, errOut, status = w.run("events", "--json")
	if status != 0 {
		t.Fatalf("events exited %d: %s%s", status, out, errOut)
	}
	for _, name := range []string{"sched.run.fired", "sched.run.limited", "sched.run.dropped"} {
		if !strings.Contains(out, name) {
			t.Errorf("the trail carries no %s", name)
		}
	}
}

// post sends one request to the door the daemon opened, with the signature
// header when there is one.
func (w *world) post(url, signature string, body []byte) (int, string) {
	w.t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		w.t.Fatalf("request: %v", err)
	}
	if signature != "" {
		req.Header.Set("X-Sched-Signature", signature)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		w.t.Fatalf("post %s: %v", url, err)
	}
	defer res.Body.Close()
	answer, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(answer)
}
