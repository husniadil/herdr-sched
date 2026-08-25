package daemon

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/husniadil/herdr-sched/internal/codes"
	"github.com/husniadil/herdr-sched/internal/protocol"
	"github.com/husniadil/herdr-sched/internal/store"
	"github.com/husniadil/herdr-sched/internal/testenv"
	"github.com/husniadil/herdr-sched/internal/trigger"
)

// deployArgs is a well-formed `trigger add` for a webhook that files a task.
func deployArgs() map[string]any {
	return map[string]any{
		"id":     "deploy",
		"kind":   trigger.KindWebhook,
		"action": "task",
		"args":   map[string]any{"title": "deploy asked for"},
	}
}

// triggerDaemon is a daemon with a runner, a secrets file and a pinned clock,
// which is what every case below drives.
func triggerDaemon(t *testing.T, now string) (*Daemon, *testenv.Fake) {
	t.Helper()
	d, f := firingDaemon(t, now)
	secrets, err := store.OpenSecrets(filepath.Join(t.TempDir(), "secrets.json"))
	if err != nil {
		t.Fatalf("secrets: %v", err)
	}
	d.Secrets = secrets
	return d, f
}

// A webhook is written, and its secret is answered HERE and nowhere else.
func TestAWebhookIsWrittenAndItsSecretIsAnsweredOnce(t *testing.T) {
	d, _ := triggerDaemon(t, "2026-08-25T10:00:00Z")
	raw, err := call(t, d, "trigger.add", deployArgs())
	if err != nil {
		t.Fatalf("trigger.add: %v", err)
	}
	change := decode[TriggerChange](t, raw)
	if !change.Changed || change.State != store.KindAdded {
		t.Errorf("the add answered %+v", change)
	}
	if len(change.Secret) != trigger.SecretBytes*2 {
		t.Fatalf("the add answered a secret of %d characters", len(change.Secret))
	}
	held, ok := d.Secrets.Get("deploy")
	if !ok || held != change.Secret {
		t.Error("the secret the caller was shown is not the one the daemon kept")
	}
}

// The one invariant a leaked secret would break, tested where it could leak:
// every read a caller has.
func TestNoSecretReachesListGetDumpOrTheTrail(t *testing.T) {
	d, _ := triggerDaemon(t, "2026-08-25T10:00:00Z")
	raw, err := call(t, d, "trigger.add", deployArgs())
	if err != nil {
		t.Fatalf("trigger.add: %v", err)
	}
	secret := decode[TriggerChange](t, raw).Secret
	if secret == "" {
		t.Fatal("no secret was issued, so this proves nothing")
	}

	for _, verb := range []string{"trigger.list", "dump", "events", "doctor"} {
		raw, err := call(t, d, verb, nil)
		if err != nil {
			t.Fatalf("%s: %v", verb, err)
		}
		if strings.Contains(string(raw), secret) {
			t.Errorf("%s printed the webhook secret", verb)
		}
	}

	// And the store file itself: the document is written whole, and the key is
	// not in it.
	body, err := os.ReadFile(d.Store.Path)
	if err != nil {
		t.Fatalf("read the store: %v", err)
	}
	if strings.Contains(string(body), secret) {
		t.Error("the store document on disk carries the webhook secret")
	}
}

// The trigger row a caller reads carries the URL and no key.
func TestTheListedWebhookCarriesItsURLAndNoKey(t *testing.T) {
	d, _ := triggerDaemon(t, "2026-08-25T10:00:00Z")
	d.inbound.set("127.0.0.1:9999", "")
	if _, err := call(t, d, "trigger.add", deployArgs()); err != nil {
		t.Fatalf("trigger.add: %v", err)
	}
	raw, err := call(t, d, "trigger.list", nil)
	if err != nil {
		t.Fatalf("trigger.list: %v", err)
	}
	rep := decode[TriggersReport](t, raw)
	if rep.Count != 1 {
		t.Fatalf("the list answered %+v", rep)
	}
	if want := "http://127.0.0.1:9999/trigger/deploy"; rep.Triggers[0].URL != want {
		t.Errorf("the URL is %q, not %q", rep.Triggers[0].URL, want)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("read the list: %v", err)
	}
	if strings.Contains(strings.ToLower(string(raw)), "secret") {
		t.Errorf("the listed row names a secret at all: %s", raw)
	}
}

func TestATriggerThatCouldNotFireIsRefusedAtAdd(t *testing.T) {
	cases := map[string]func(map[string]any){
		"a kind outside the two":             func(a map[string]any) { a["kind"] = "poll" },
		"a webhook naming a path":            func(a map[string]any) { a["path"] = "/tmp/x" },
		"a watch naming none":                func(a map[string]any) { a["kind"] = trigger.KindWatch },
		"an action nothing can run":          func(a map[string]any) { a["action"] = "smoke" },
		"an argument the kind never takes":   func(a map[string]any) { a["args"] = map[string]any{"titel": "typo"} },
		"an id that is a principal":          func(a map[string]any) { a["id"] = "trigger:deploy" },
		"an id that would not survive a URL": func(a map[string]any) { a["id"] = "a/b" },
		"a negative cooldown":                func(a map[string]any) { a["cooldown"] = -1 },
		"a watch on a relative path": func(a map[string]any) {
			a["kind"], a["path"] = trigger.KindWatch, "inbox"
		},
	}
	for what, break_ := range cases {
		d, _ := triggerDaemon(t, "2026-08-25T10:00:00Z")
		args := deployArgs()
		break_(args)
		if _, err := call(t, d, "trigger.add", args); err == nil {
			t.Errorf("a trigger with %s was written", what)
		} else if got := codes.Of(err); got != codes.Usage {
			t.Errorf("a trigger with %s was refused as %s, want USAGE", what, got)
		}
	}
}

// Two triggers answering to one name would be one URL nobody can say which row
// handled.
func TestASecondTriggerUnderOneNameIsRefusedAndTheLiveKeyIsKept(t *testing.T) {
	d, _ := triggerDaemon(t, "2026-08-25T10:00:00Z")
	raw, err := call(t, d, "trigger.add", deployArgs())
	if err != nil {
		t.Fatalf("trigger.add: %v", err)
	}
	first := decode[TriggerChange](t, raw).Secret

	if _, err := call(t, d, "trigger.add", deployArgs()); err == nil {
		t.Fatal("a second trigger called deploy was written")
	} else if codes.Of(err) != codes.Conflict {
		t.Errorf("the duplicate was refused as %s, want CONFLICT", codes.Of(err))
	}
	held, ok := d.Secrets.Get("deploy")
	if !ok || held != first {
		t.Error("the refused duplicate replaced the live trigger's key")
	}
}

// A key with no trigger left to use it is a secret kept for nothing.
func TestRemovingATriggerDropsItsKey(t *testing.T) {
	d, _ := triggerDaemon(t, "2026-08-25T10:00:00Z")
	if _, err := call(t, d, "trigger.add", deployArgs()); err != nil {
		t.Fatalf("trigger.add: %v", err)
	}
	if _, err := call(t, d, "trigger.remove", map[string]any{"id": "deploy"}); err != nil {
		t.Fatalf("trigger.remove: %v", err)
	}
	if _, ok := d.Secrets.Get("deploy"); ok {
		t.Error("the removed trigger's key is still held")
	}
}

// serveInbound starts the real webhook door on an ephemeral port and answers
// with the base URL. It is the whole door: the same handler a request from
// outside meets.
func serveInbound(t *testing.T, d *Daemon) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	d.inbound.set(ln.Addr().String(), "")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.serveWebhooks(ctx, ln)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return "http://" + ln.Addr().String() + WebhookPrefix
}

// post sends one body with a signature made from the given secret, and answers
// with the status and the body.
func post(t *testing.T, url, secret string, body []byte) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if secret != "" {
		req.Header.Set(trigger.SignatureHeader, trigger.Sign(secret, body))
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer res.Body.Close()
	answer, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(answer)
}

// runsOn is every run event this trigger has.
//
// It is deliberately NOT read as an ordered sequence: the clock is pinned in
// these cases, so several runs share a millisecond and the trail's tiebreak is
// the random half of the event id. What each case asserts is which runs are
// there, by kind, which is the fact that matters.
func runsOn(t *testing.T, d *Daemon, id string) []store.Event {
	t.Helper()
	var out []store.Event
	for _, ev := range d.Store.Trail() {
		if ev.Entity == store.EntityRun && ev.EntityID == id {
			out = append(out, ev)
		}
	}
	return out
}

// runKinds counts this trigger's runs by kind.
func runKinds(t *testing.T, d *Daemon, id string) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, ev := range runsOn(t, d, id) {
		out[ev.Kind]++
	}
	return out
}

// oneRun is the single run of the given kind, and fails when there is not
// exactly one.
func oneRun(t *testing.T, d *Daemon, id, kind string) store.Event {
	t.Helper()
	var found []store.Event
	for _, ev := range runsOn(t, d, id) {
		if ev.Kind == kind {
			found = append(found, ev)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%s has %d runs of kind %q, want 1", id, len(found), kind)
	}
	return found[0]
}

// The three the task pins as one case, because they are one story: a valid
// signature fires, an invalid one drops with an event and fires nothing, and a
// replay inside the cooldown drops.
func TestAValidSignatureFiresAnInvalidOneDropsAndAReplayIsHeldDown(t *testing.T) {
	testenv.SkipUnlessFull(t)
	d, f := triggerDaemon(t, "2026-08-25T10:00:00Z")
	f.HTask(t, `printf '%s\n' '{"task":{"id":"01J","seq":7,"title":"deploy asked for"}}'`)

	args := deployArgs()
	args["cooldown"] = 60
	raw, err := call(t, d, "trigger.add", args)
	if err != nil {
		t.Fatalf("trigger.add: %v", err)
	}
	secret := decode[TriggerChange](t, raw).Secret
	base := serveInbound(t, d)
	body := []byte(`{"ref":"refs/heads/main"}`)

	// One: a signature that holds fires the action.
	status, answer := post(t, base+"deploy", secret, body)
	if status != http.StatusAccepted {
		t.Fatalf("a verified request answered %d: %s", status, answer)
	}
	d.Fire.Wait()
	if got := runKinds(t, d, "deploy"); got[store.KindFired] != 1 || len(got) != 1 {
		t.Fatalf("a verified request left the runs %v", got)
	}
	if actor := oneRun(t, d, "deploy", store.KindFired).Actor; actor != "trigger:deploy" {
		t.Errorf("the run is attributed to %q, not trigger:deploy", actor)
	}
	if calls := f.Calls(t); len(calls) != 1 || !strings.Contains(calls[0], "--as trigger:deploy") {
		t.Errorf("the sibling was called as %v", calls)
	}

	// Two: a signature that does not hold is DROPPED, with an event naming the
	// trigger, and nothing is fired.
	status, answer = post(t, base+"deploy", secret+"ff", body)
	if status != http.StatusForbidden {
		t.Fatalf("an unverified request answered %d: %s", status, answer)
	}
	d.Fire.Wait()
	if got := runKinds(t, d, "deploy"); got[store.KindDropped] != 1 || got[store.KindFired] != 1 {
		t.Fatalf("an unverified request left the runs %v", got)
	}
	if dropped := oneRun(t, d, "deploy", store.KindDropped); dropped.EntityID != "deploy" {
		t.Errorf("the drop names %q, not the trigger it was aimed at", dropped.EntityID)
	}
	if got := len(f.Calls(t)); got != 1 {
		t.Errorf("an unverified request fired something: the sibling saw %d calls", got)
	}

	// Three: the same signed body again, inside the cooldown, is held down.
	status, answer = post(t, base+"deploy", secret, body)
	if status != http.StatusTooManyRequests {
		t.Fatalf("a replay inside the cooldown answered %d: %s", status, answer)
	}
	if !strings.Contains(answer, trigger.LimitCooldown) {
		t.Errorf("the refusal does not name the cooldown: %s", answer)
	}
	d.Fire.Wait()
	got := runKinds(t, d, "deploy")
	if got[store.KindLimited] != 1 || got[store.KindFired] != 1 || got[store.KindDropped] != 1 {
		t.Fatalf("a replay left the runs %v", got)
	}
	if limited := oneRun(t, d, "deploy", store.KindLimited); limited.Detail["limit"] != trigger.LimitCooldown {
		t.Errorf("the refusal was recorded as %v", limited.Detail)
	}
	if got := len(f.Calls(t)); got != 1 {
		t.Errorf("a replay fired the action again: the sibling saw %d calls", got)
	}
}

// A request with no signature at all is the same story as a wrong one.
func TestAnUnsignedRequestIsDropped(t *testing.T) {
	testenv.SkipUnlessFull(t)
	d, f := triggerDaemon(t, "2026-08-25T10:00:00Z")
	f.HTask(t, `printf '%s\n' '{"task":{"id":"01J","seq":7}}'`)
	if _, err := call(t, d, "trigger.add", deployArgs()); err != nil {
		t.Fatalf("trigger.add: %v", err)
	}
	base := serveInbound(t, d)

	status, answer := post(t, base+"deploy", "", []byte(`{}`))
	if status != http.StatusForbidden {
		t.Fatalf("an unsigned request answered %d: %s", status, answer)
	}
	d.Fire.Wait()
	if got := len(f.Calls(t)); got != 0 {
		t.Errorf("an unsigned request fired something: the sibling saw %d calls", got)
	}
	runs := runsOn(t, d, "deploy")
	if len(runs) != 1 || runs[0].Kind != store.KindDropped {
		t.Errorf("an unsigned request left the runs %+v", runs)
	}
}

// An id no row answers to is not a trigger that was dropped, and it leaves no
// event: this door is not a way to enumerate the ids there are.
func TestARequestForATriggerThatDoesNotExistIsNotFoundAndLeavesNoTrail(t *testing.T) {
	testenv.SkipUnlessFull(t)
	d, _ := triggerDaemon(t, "2026-08-25T10:00:00Z")
	base := serveInbound(t, d)

	status, _ := post(t, base+"nobody", "whatever", []byte(`{}`))
	if status != http.StatusNotFound {
		t.Fatalf("a request for an unknown trigger answered %d", status)
	}
	if runs := runsOn(t, d, "nobody"); len(runs) != 0 {
		t.Errorf("an unknown id left the runs %+v", runs)
	}
}

func TestOnlyPOSTFiresAWebhook(t *testing.T) {
	testenv.SkipUnlessFull(t)
	d, _ := triggerDaemon(t, "2026-08-25T10:00:00Z")
	if _, err := call(t, d, "trigger.add", deployArgs()); err != nil {
		t.Fatalf("trigger.add: %v", err)
	}
	base := serveInbound(t, d)
	res, err := http.Get(base + "deploy")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("a GET answered %d", res.StatusCode)
	}
}

// A disabled trigger verifies what arrives and then refuses it onto the trail:
// an operator who forgot they disabled it has somewhere to read that.
func TestADisabledWebhookRefusesOntoTheTrailRatherThanFiring(t *testing.T) {
	testenv.SkipUnlessFull(t)
	d, f := triggerDaemon(t, "2026-08-25T10:00:00Z")
	f.HTask(t, `printf '%s\n' '{"task":{"id":"01J","seq":7}}'`)
	raw, err := call(t, d, "trigger.add", deployArgs())
	if err != nil {
		t.Fatalf("trigger.add: %v", err)
	}
	secret := decode[TriggerChange](t, raw).Secret
	if _, err := call(t, d, "trigger.disable", map[string]any{"id": "deploy"}); err != nil {
		t.Fatalf("trigger.disable: %v", err)
	}
	base := serveInbound(t, d)

	status, answer := post(t, base+"deploy", secret, []byte(`{}`))
	if status != http.StatusTooManyRequests {
		t.Fatalf("a disabled trigger answered %d: %s", status, answer)
	}
	d.Fire.Wait()
	if got := len(f.Calls(t)); got != 0 {
		t.Errorf("a disabled trigger fired: the sibling saw %d calls", got)
	}
	runs := runsOn(t, d, "deploy")
	if len(runs) != 1 || runs[0].Detail["limit"] != trigger.LimitDisabled {
		t.Errorf("a disabled trigger left the runs %+v", runs)
	}
}

// The watcher fires on a changed file across ticks, in a temp dir.
func TestTheWatcherFiresOnAChangedFileAcrossTicks(t *testing.T) {
	testenv.SkipUnlessFull(t)
	d, f := triggerDaemon(t, "2026-08-25T10:00:00Z")
	f.HTask(t, `printf '%s\n' '{"task":{"id":"01J","seq":7,"title":"something arrived"}}'`)
	watched := filepath.Join(t.TempDir(), "inbox")
	if err := os.WriteFile(watched, []byte("one"), 0o600); err != nil {
		t.Fatalf("write the watched file: %v", err)
	}
	if _, err := call(t, d, "trigger.add", map[string]any{
		"id": "inbox", "kind": trigger.KindWatch, "action": "task", "path": watched,
		"args": map[string]any{"title": "something arrived"},
	}); err != nil {
		t.Fatalf("trigger.add: %v", err)
	}

	// The first tick records what is there and fires nothing: the file existed
	// before anyone asked to watch it.
	ctx := context.Background()
	d.runWatchers(ctx)
	d.Fire.Wait()
	if got := len(f.Calls(t)); got != 0 {
		t.Fatalf("the first look fired: the sibling saw %d calls", got)
	}
	held, _ := d.Store.Trigger("inbox")
	if !held.Stamp.Seen || !held.Stamp.Present {
		t.Fatalf("the first look recorded %+v", held.Stamp)
	}

	// A tick with nothing changed fires nothing either.
	d.runWatchers(ctx)
	d.Fire.Wait()
	if got := len(f.Calls(t)); got != 0 {
		t.Fatalf("an unchanged file fired: the sibling saw %d calls", got)
	}

	// The file changes, and the next tick fires once.
	if err := os.WriteFile(watched, []byte("one and two"), 0o600); err != nil {
		t.Fatalf("change the watched file: %v", err)
	}
	d.runWatchers(ctx)
	d.Fire.Wait()
	calls := f.Calls(t)
	if len(calls) != 1 {
		t.Fatalf("a changed file left %d calls: %v", len(calls), calls)
	}
	if !strings.Contains(calls[0], "--as trigger:inbox") {
		t.Errorf("the sibling was called as %v", calls)
	}
	runs := runsOn(t, d, "inbox")
	if len(runs) != 1 || runs[0].Kind != store.KindFired {
		t.Fatalf("a changed file left the runs %+v", runs)
	}

	// And the tick after it, with nothing further changed, fires nothing: the
	// stamp moved with the firing.
	d.runWatchers(ctx)
	d.Fire.Wait()
	if got := len(f.Calls(t)); got != 1 {
		t.Errorf("the tick after the change fired again: the sibling saw %d calls", got)
	}
}

func TestADisabledWatcherIsPassedOverAndDoesNotFireForTheGap(t *testing.T) {
	testenv.SkipUnlessFull(t)
	d, f := triggerDaemon(t, "2026-08-25T10:00:00Z")
	f.HTask(t, `printf '%s\n' '{"task":{"id":"01J","seq":7}}'`)
	watched := filepath.Join(t.TempDir(), "inbox")
	if err := os.WriteFile(watched, []byte("one"), 0o600); err != nil {
		t.Fatalf("write the watched file: %v", err)
	}
	if _, err := call(t, d, "trigger.add", map[string]any{
		"id": "inbox", "kind": trigger.KindWatch, "action": "task", "path": watched,
		"args": map[string]any{"title": "something arrived"},
	}); err != nil {
		t.Fatalf("trigger.add: %v", err)
	}
	ctx := context.Background()
	d.runWatchers(ctx)

	if _, err := call(t, d, "trigger.disable", map[string]any{"id": "inbox"}); err != nil {
		t.Fatalf("trigger.disable: %v", err)
	}
	if err := os.WriteFile(watched, []byte("changed while off"), 0o600); err != nil {
		t.Fatalf("change the watched file: %v", err)
	}
	d.runWatchers(ctx)
	d.Fire.Wait()
	if got := len(f.Calls(t)); got != 0 {
		t.Fatalf("a disabled watcher fired: the sibling saw %d calls", got)
	}

	// Enabled again, it records what is there NOW rather than firing for the
	// change it was off for.
	if _, err := call(t, d, "trigger.enable", map[string]any{"id": "inbox"}); err != nil {
		t.Fatalf("trigger.enable: %v", err)
	}
	d.runWatchers(ctx)
	d.Fire.Wait()
	if got := len(f.Calls(t)); got != 0 {
		t.Errorf("a watcher re-enabled fired for the gap: the sibling saw %d calls", got)
	}
}

// doctor is where a webhook URL's port is answered, and where a door that
// could not bind says so.
func TestDoctorNamesTheInboundDoorAndTheTriggerCounts(t *testing.T) {
	d, _ := triggerDaemon(t, "2026-08-25T10:00:00Z")
	if _, err := call(t, d, "trigger.add", deployArgs()); err != nil {
		t.Fatalf("trigger.add: %v", err)
	}
	d.inbound.set("127.0.0.1:9999", "")

	rep := d.doctor()
	if rep.Triggers.Count != 1 || rep.Triggers.Webhooks != 1 || rep.Triggers.Enabled != 1 {
		t.Errorf("doctor counted %+v", rep.Triggers)
	}
	if rep.Triggers.Inbound != "127.0.0.1:9999" {
		t.Errorf("doctor names the inbound door as %q", rep.Triggers.Inbound)
	}
	if rep.Triggers.SecretsPath != d.Secrets.Path {
		t.Errorf("doctor names the secrets file as %q", rep.Triggers.SecretsPath)
	}

	// A door that could not bind is a fact only doctor can give: from the call
	// site an off door and a failed one look the same.
	d.inbound.set("", "the port is taken")
	if got := d.doctor().Triggers.InboundError; got != "the port is taken" {
		t.Errorf("doctor says %q about a door that could not bind", got)
	}
}

// A key with no trigger left is counted, never listed: naming them would put
// trigger ids in an answer for no reason.
func TestDoctorCountsAnOrphanedKeyWithoutNamingIt(t *testing.T) {
	d, _ := triggerDaemon(t, "2026-08-25T10:00:00Z")
	if err := d.Secrets.Set("left-behind", "deadbeef"); err != nil {
		t.Fatalf("secrets: %v", err)
	}
	rep := d.doctor()
	if rep.Triggers.OrphanSecrets != 1 {
		t.Errorf("doctor counted %d orphaned keys", rep.Triggers.OrphanSecrets)
	}
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("render doctor: %v", err)
	}
	if strings.Contains(string(raw), "left-behind") || strings.Contains(string(raw), "deadbeef") {
		t.Error("doctor named an orphaned key or its value")
	}
}

// A trigger fires INTO one project's board and mailbox, so "every project" is
// a way of reading and not a place to write one.
func TestATriggerIsNotWrittenAcrossEveryProject(t *testing.T) {
	d, _ := triggerDaemon(t, "2026-08-25T10:00:00Z")
	_, err := d.Handle(context.Background(), protocol.Request{
		Verb: "trigger.add", Args: deployArgs(), AllProjects: true, Pane: "wT:p1", Door: "cli"})
	if err == nil {
		t.Fatal("a trigger was written across every project")
	}
	if codes.Of(err) != codes.Usage {
		t.Errorf("it was refused as %s, want USAGE", codes.Of(err))
	}
}

// The hourly limit, through the whole door rather than only in the core.
func TestTheHourlyLimitHoldsDownTheOneOverAtTheDoor(t *testing.T) {
	testenv.SkipUnlessFull(t)
	d, f := triggerDaemon(t, "2026-08-25T10:00:00Z")
	f.HTask(t, `printf '%s\n' '{"task":{"id":"01J","seq":7}}'`)
	args := deployArgs()
	args["max_per_hour"] = 2
	raw, err := call(t, d, "trigger.add", args)
	if err != nil {
		t.Fatalf("trigger.add: %v", err)
	}
	secret := decode[TriggerChange](t, raw).Secret
	base := serveInbound(t, d)

	for i := 0; i < 2; i++ {
		if status, answer := post(t, base+"deploy", secret, []byte(`{}`)); status != http.StatusAccepted {
			t.Fatalf("firing %d answered %d: %s", i+1, status, answer)
		}
	}
	status, answer := post(t, base+"deploy", secret, []byte(`{}`))
	if status != http.StatusTooManyRequests {
		t.Fatalf("the third firing in an hour answered %d: %s", status, answer)
	}
	if !strings.Contains(answer, trigger.LimitRate) {
		t.Errorf("the refusal does not name the hourly limit: %s", answer)
	}
	d.Fire.Wait()
	if got := len(f.Calls(t)); got != 2 {
		t.Errorf("the limit let %d firings through, not 2", got)
	}
}

// The daemon starts without an inbound door when one cannot be had, and every
// schedule and watcher is unaffected.
func TestADoorThatCannotBindNeverStopsTheDaemon(t *testing.T) {
	testenv.SkipUnlessFull(t)
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer taken.Close()

	d, _ := triggerDaemon(t, "2026-08-25T10:00:00Z")
	d.WebhookAddr = taken.Addr().String()
	if ln := d.listenWebhooks(); ln != nil {
		ln.Close()
		t.Fatal("the door bound a port another listener holds")
	}
	addr, reason := d.inbound.read()
	if addr != "" || reason == "" {
		t.Errorf("a door that could not bind reported %q / %q", addr, reason)
	}
}

// doctor reports the port the door ACTUALLY got, not the one it was asked
// for. The two differ whenever the kernel picks — `webhook_addr = 127.0.0.1:0`
// is the honest case — and an operator reading `:0` back has been told the one
// thing that cannot be curled. It is what makes an ephemeral port a usable
// option at all, so it is pinned rather than left to the reading of one line.
func TestTheDoorReportsThePortItGotRatherThanTheOneItAskedFor(t *testing.T) {
	testenv.SkipUnlessFull(t)
	d, _ := triggerDaemon(t, "2026-08-25T10:00:00Z")
	d.WebhookAddr = "127.0.0.1:0"
	ln := d.listenWebhooks()
	if ln == nil {
		t.Fatal("the door did not bind a kernel-chosen port")
	}
	defer ln.Close()
	addr, reason := d.inbound.read()
	if reason != "" {
		t.Fatalf("a door that bound reported the failure %q", reason)
	}
	if addr == d.WebhookAddr {
		t.Errorf("doctor reports %q, the address asked for: :0 is the one port nobody can call", addr)
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "0" || port == "" {
		t.Errorf("doctor reports %q, want the concrete port the listener got", addr)
	}
	if addr != ln.Addr().String() {
		t.Errorf("doctor reports %q, the listener is on %q", addr, ln.Addr())
	}
}

func TestNoInboundDoorIsOpenedWhenNoneIsAskedFor(t *testing.T) {
	for _, addr := range []string{"", WebhookOff} {
		d, _ := triggerDaemon(t, "2026-08-25T10:00:00Z")
		d.WebhookAddr = addr
		if ln := d.listenWebhooks(); ln != nil {
			ln.Close()
			t.Errorf("a door was opened for webhook_addr %q", addr)
		}
		if _, reason := d.inbound.read(); reason != "" {
			t.Errorf("a door nobody asked for reported the failure %q", reason)
		}
	}
}

// The stamp moves BEFORE the action fires, so a daemon that dies mid-action
// leaves a change that did not fire rather than one that fires every tick.
func TestTheStampMovesEvenWhenThereIsNoRunnerToFireWith(t *testing.T) {
	testenv.SkipUnlessFull(t)
	d, _ := triggerDaemon(t, "2026-08-25T10:00:00Z")
	d.Fire = nil
	watched := filepath.Join(t.TempDir(), "inbox")
	if err := os.WriteFile(watched, []byte("one"), 0o600); err != nil {
		t.Fatalf("write the watched file: %v", err)
	}
	if _, err := call(t, d, "trigger.add", map[string]any{
		"id": "inbox", "kind": trigger.KindWatch, "action": "shell", "path": watched,
		"args": map[string]any{"command": "true"},
	}); err != nil {
		t.Fatalf("trigger.add: %v", err)
	}
	ctx := context.Background()
	d.runWatchers(ctx)
	if err := os.WriteFile(watched, []byte("two"), 0o600); err != nil {
		t.Fatalf("change the watched file: %v", err)
	}
	d.runWatchers(ctx)

	held, _ := d.Store.Trigger("inbox")
	if held.Stamp.Size != 3 {
		t.Errorf("the stamp did not move past the change: %+v", held.Stamp)
	}
	runs := runsOn(t, d, "inbox")
	if len(runs) != 1 || runs[0].Kind != store.KindFailed {
		t.Fatalf("a daemon with no runner left the runs %+v", runs)
	}
	// And the next tick is quiet: the change was consumed even though nothing
	// could perform it.
	d.runWatchers(ctx)
	if got := len(runsOn(t, d, "inbox")); got != 1 {
		t.Errorf("the change fired again on the next tick: %d runs", got)
	}
}

// Secrets survive a restart, because a webhook whose key the daemon forgot is
// a URL nobody can ever call again.
func TestSecretsSurviveAReopen(t *testing.T) {
	d, _ := triggerDaemon(t, "2026-08-25T10:00:00Z")
	if err := d.Secrets.Set("deploy", "cafebabe"); err != nil {
		t.Fatalf("secrets: %v", err)
	}
	again, err := store.OpenSecrets(d.Secrets.Path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if held, ok := again.Get("deploy"); !ok || held != "cafebabe" {
		t.Errorf("the reopened keys answered %q / %v", held, ok)
	}
	info, err := os.Stat(d.Secrets.Path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != store.SecretsMode {
		t.Errorf("the secrets file is mode %v, not %v", info.Mode().Perm(), os.FileMode(store.SecretsMode))
	}
}

// A body longer than the door reads is refused before it is signed for: a
// signature is computed over the WHOLE body, so a body with no ceiling is a
// stranger choosing how much memory this daemon spends.
func TestABodyLongerThanTheDoorReadsIsRefused(t *testing.T) {
	testenv.SkipUnlessFull(t)
	d, f := triggerDaemon(t, "2026-08-25T10:00:00Z")
	f.HTask(t, `printf '%s\n' '{"task":{"id":"01J","seq":7}}'`)
	raw, err := call(t, d, "trigger.add", deployArgs())
	if err != nil {
		t.Fatalf("trigger.add: %v", err)
	}
	secret := decode[TriggerChange](t, raw).Secret
	base := serveInbound(t, d)

	body := make([]byte, MaxBody+1)
	for i := range body {
		body[i] = 'x'
	}
	status, _ := post(t, base+"deploy", secret, body)
	if status == http.StatusAccepted {
		t.Fatal("an oversized body was accepted")
	}
	d.Fire.Wait()
	if got := len(f.Calls(t)); got != 0 {
		t.Errorf("an oversized body fired something: the sibling saw %d calls", got)
	}
}

// The webhook door answers within a sane time, which is what the read timeout
// is for. This is only a guard that the constants are wired to the server.
func TestTheInboundDoorCarriesItsTimeouts(t *testing.T) {
	if webhookReadTimeout <= 0 || webhookWriteTimeout <= 0 || webhookIdleTimeout <= 0 {
		t.Fatal("a connection that never finishes saying anything is unbounded")
	}
	if webhookIdleTimeout < webhookReadTimeout {
		t.Error("the idle timeout is shorter than the read timeout")
	}
	_ = time.Second
}

// The claim the whole webhook door rests on: the decision is made against the
// row as it is NOW, under the store's lock, and the cursor moves there before
// anything fires.
//
// A sequential replay does not prove it. Each request re-reads the row at the
// top of the handler, so by the time the second one arrives the first firing is
// already visible to it, and a decision made against that stale-but-current
// read looks identical to one made under the lock. Nor does a burst of real
// HTTP requests: connection setup serialises them well enough that each one
// reads after the last one wrote, so it passes either way and proves nothing.
//
// What tells the two apart is callers that ALL hold a row read before any of
// them fired, which is exactly the state two same-millisecond requests are in.
// Every one of them is handed that row, and exactly one may get through.
func TestCallersHoldingOneStaleRowCannotAllSpendTheSameCooldown(t *testing.T) {
	testenv.SkipUnlessFull(t)
	d, f := triggerDaemon(t, "2026-08-25T10:00:00Z")
	f.HTask(t, `printf '%s\n' '{"task":{"id":"01J","seq":7}}'`)
	args := deployArgs()
	args["cooldown"] = 3600
	if _, err := call(t, d, "trigger.add", args); err != nil {
		t.Fatalf("trigger.add: %v", err)
	}
	// One read, before anything fires, shared by every caller.
	stale, ok := d.Store.Trigger("deploy")
	if !ok {
		t.Fatal("the trigger was not written")
	}

	const callers = 16
	fired := concurrentFires(t, d, stale, callers)
	if fired != 1 {
		t.Fatalf("%d of %d callers holding one unspent cooldown were allowed through", fired, callers)
	}
	d.Fire.Wait()
	if got := runKinds(t, d, "deploy"); got[store.KindFired] != 1 || got[store.KindLimited] != callers-1 {
		t.Errorf("the runs read %v", got)
	}
	if got := len(f.Calls(t)); got != 1 {
		t.Errorf("the action was performed %d times", got)
	}
}

// The same, for the hourly limit: callers holding one stale row must not spend
// more than it allows between them.
func TestCallersHoldingOneStaleRowSpendNoMoreThanTheHourlyLimit(t *testing.T) {
	testenv.SkipUnlessFull(t)
	d, f := triggerDaemon(t, "2026-08-25T10:00:00Z")
	f.HTask(t, `printf '%s\n' '{"task":{"id":"01J","seq":7}}'`)
	args := deployArgs()
	args["max_per_hour"] = 3
	if _, err := call(t, d, "trigger.add", args); err != nil {
		t.Fatalf("trigger.add: %v", err)
	}
	stale, ok := d.Store.Trigger("deploy")
	if !ok {
		t.Fatal("the trigger was not written")
	}

	const callers = 16
	fired := concurrentFires(t, d, stale, callers)
	if fired != 3 {
		t.Fatalf("%d of %d callers were allowed through against max_per_hour 3", fired, callers)
	}
	d.Fire.Wait()
	if got := len(f.Calls(t)); got != 3 {
		t.Errorf("the action was performed %d times, not 3", got)
	}
}

// concurrentFires hands the same pre-read row to n callers at once and answers
// with how many the row let through.
func concurrentFires(t *testing.T, d *Daemon, stale trigger.Trigger, n int) int {
	t.Helper()
	ctx := context.Background()
	var start, done sync.WaitGroup
	start.Add(1)
	verdicts := make([]trigger.Verdict, n)
	for i := 0; i < n; i++ {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			start.Wait()
			verdicts[i] = d.fireTrigger(ctx, stale, map[string]any{"trigger_kind": trigger.KindWebhook})
		}(i)
	}
	start.Done()
	done.Wait()
	fired := 0
	for _, v := range verdicts {
		if v.Fire {
			fired++
		}
	}
	return fired
}

// A burst through the real door, as a smoke check that the handler reaches the
// same decision path. It does not prove the lock discipline above.
func TestABurstThroughTheDoorSpendsOneCooldownOnce(t *testing.T) {
	testenv.SkipUnlessFull(t)
	d, f := triggerDaemon(t, "2026-08-25T10:00:00Z")
	f.HTask(t, `printf '%s\n' '{"task":{"id":"01J","seq":7}}'`)
	args := deployArgs()
	args["cooldown"] = 3600
	raw, err := call(t, d, "trigger.add", args)
	if err != nil {
		t.Fatalf("trigger.add: %v", err)
	}
	secret := decode[TriggerChange](t, raw).Secret
	base := serveInbound(t, d)

	const callers = 12
	var start, done sync.WaitGroup
	start.Add(1)
	statuses := make([]int, callers)
	for i := 0; i < callers; i++ {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			start.Wait()
			statuses[i], _ = post(t, base+"deploy", secret, []byte(`{}`))
		}(i)
	}
	start.Done()
	done.Wait()
	d.Fire.Wait()

	accepted := 0
	for _, status := range statuses {
		if status == http.StatusAccepted {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("%d of %d requests were accepted against one unspent cooldown", accepted, callers)
	}
	if got := len(f.Calls(t)); got != 1 {
		t.Errorf("the action was performed %d times", got)
	}
}
