package sibling

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/husniadil/herdr-sched/internal/testenv"
)

// Every call answers in JSON and declares who fired it, and neither is a
// caller's job to remember: they are appended here, so a verb added later
// cannot arrive in prose or unattributed (§3.2, §6.2).
func TestEveryCallCarriesJSONAndThePrincipal(t *testing.T) {
	testenv.SkipUnlessFull(t)
	f := testenv.New(t)
	f.HTask(t, `echo '{}'`)

	c := &Client{Name: "htask", Principal: "cron:nightly"}
	if _, err := c.Run(context.Background(), "create", "sweep"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got, want := f.Calls(t)[0], "create sweep --json --as cron:nightly"; got != want {
		t.Fatalf("argv:\n got %q\nwant %q", got, want)
	}
}

// A sibling call carries a principal INSTEAD of a pane. The pane travels in
// the environment, so a child that inherited it would be attributed to this
// daemon rather than to the schedule — hmail in particular stamps the sender
// from the pane the call came out of.
func TestThePaneIsNotHandedToASibling(t *testing.T) {
	testenv.SkipUnlessFull(t)
	f := testenv.New(t) // sets HERDR_PANE_ID
	t.Setenv("HERDR_TAB_ID", "wT")
	t.Setenv("HERDR_WORKSPACE_ID", "w1")
	t.Setenv("HERDR_PLUGIN_CONTEXT_JSON", `{"focused":{"cwd":"/somewhere/else"}}`)
	f.HTask(t, `echo "pane=[$HERDR_PANE_ID] tab=[$HERDR_TAB_ID] ws=[$HERDR_WORKSPACE_ID] ctx=[$HERDR_PLUGIN_CONTEXT_JSON]"; echo '{}' >/dev/null`)

	c := &Client{Name: "htask", Principal: "cron:nightly"}
	out, err := c.Run(context.Background(), "doctor")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "pane=[] tab=[] ws=[] ctx=[]" {
		t.Fatalf("the sibling saw %q", got)
	}
}

// A sibling's §6.2 envelope is carried as a Refusal, naming which sibling
// spoke; a failure with no envelope never reached one that answered.
func TestAnEnvelopeIsARefusalAndSilenceIsNot(t *testing.T) {
	testenv.SkipUnlessFull(t)
	f := testenv.New(t)
	f.Bin(t, "hmail", `echo '{"error":{"code":"NOT_FOUND","message":"no pane wM:p9"}}'; exit 3`)
	c := &Client{Name: "hmail", Principal: "trigger:01TRG"}
	_, err := c.Run(context.Background(), "send", "wM:p9", "up")
	var refusal *Refusal
	if !asRefusal(err, &refusal) {
		t.Fatalf("want a refusal, got %v", err)
	}
	if refusal.Sibling != "hmail" || refusal.Code != "NOT_FOUND" {
		t.Fatalf("got %+v", refusal)
	}

	f.Bin(t, "hmail", `echo "hmail: could not parse flags" >&2; exit 2`)
	_, err = c.Run(context.Background(), "send", "wM:p9", "up")
	if asRefusal(err, &refusal) {
		t.Fatalf("a door that could not answer is not a refusal: %v", err)
	}
	if !strings.Contains(err.Error(), "could not parse flags") {
		t.Fatalf("the sibling's own words were dropped: %v", err)
	}
}

// One spawn site for every sibling and every verb. The two guarantees above
// are appended at that one site, so a second exec added the obvious way in an
// adapter would carry neither — and a verb nobody has written yet is covered
// only while there is exactly one.
//
// The guard is over the packages that reach a sibling: this one and the three
// adapters, plus the fire path that drives them. Everything else in the repo
// spawns for its own reasons — the daemon's own autostart, the §10 on_event
// hook, the §9 gate, git — and none of those is a sibling call.
func TestEverySiblingCallGoesThroughTheOneSpawn(t *testing.T) {
	root := filepath.Join("..", "..", "internal")
	spawnsIn := map[string]int{}
	read := 0
	for _, pkg := range []string{"sibling", "htask", "hmail", "hdis", "fire"} {
		entries, err := os.ReadDir(filepath.Join(root, pkg))
		if err != nil {
			t.Fatalf("read %s: %v", pkg, err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(root, pkg, name)
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			read++
			if n := strings.Count(string(body), "exec.Command"); n > 0 {
				spawnsIn[path] = n
			}
		}
	}
	// A guard that read nothing passes for the wrong reason.
	if read < 5 {
		t.Fatalf("the walk read %d source files, so it guarded nothing", read)
	}
	here := filepath.Join(root, "sibling", "sibling.go")
	for path, n := range spawnsIn {
		if path != here {
			t.Errorf("%s spawns a process %d times; every sibling call goes through sibling.Client, so --json, --as and the pane scrub cover verbs nobody has written yet", path, n)
		}
	}
	if spawnsIn[here] != 1 {
		t.Errorf("sibling.go spawns %d times, and the guarantees above are appended at one site", spawnsIn[here])
	}
}

func asRefusal(err error, into **Refusal) bool {
	for e := err; e != nil; {
		if r, ok := e.(*Refusal); ok {
			*into = r
			return true
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		e = u.Unwrap()
	}
	return false
}
