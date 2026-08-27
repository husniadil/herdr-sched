package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/husniadil/herdr-sched/internal/codes"
	"github.com/husniadil/herdr-sched/internal/testenv"
	"github.com/husniadil/herdr-sched/internal/verbs"
)

// §4.2 and §3.2 fix four globals for every plugin's CLI, and they are on the
// root so every verb carries them.
func TestTheFourContractGlobalsAreOnEveryVerb(t *testing.T) {
	root := Root(nil)
	for _, name := range []string{"json", "project", "all-projects", "as"} {
		if root.PersistentFlags().Lookup(name) == nil {
			t.Errorf("--%s is not a global", name)
		}
	}
	for _, v := range verbs.All {
		cmd, _, err := root.Find(v.CLI)
		if err != nil {
			t.Fatalf("`hsched %s`: %v", strings.Join(v.CLI, " "), err)
		}
		for _, name := range []string{"json", "project", "all-projects", "as"} {
			if cmd.InheritedFlags().Lookup(name) == nil {
				t.Errorf("`hsched %s` does not take --%s", strings.Join(v.CLI, " "), name)
			}
		}
	}
}

// §3.2: agent and human are DERIVED from the calling process. A caller who
// could declare one would be declaring the fact the rule exists to keep
// underived.
func TestAgentAndHumanAreRefusedAsDeclaredPrincipals(t *testing.T) {
	testenv.New(t)
	for _, as := range []string{"agent:wX:p9", "human", "human:husni", "cron:", ""} {
		got, err := principal(as)
		switch as {
		case "":
			if err != nil || got != "" {
				t.Errorf("no --as should derive nothing, got %q / %v", got, err)
			}
		default:
			if err == nil {
				t.Errorf("--as %q was accepted", as)
			} else if codes.Of(err) != codes.Usage {
				t.Errorf("--as %q refused with %s, want USAGE", as, codes.Of(err))
			}
		}
	}
}

// §3.6 and §3.7: a CLI invocation is one process per call, so the argv that
// ran IS the deliberate human act §3.7 asks a paneless `human` to point at.
// The CLI door therefore says so on every request, and a paneless call is the
// operator where a paneless server door nobody declared is `none`. The pane
// still wins (§3.2), and so does an explicit --as.
func TestAPanelessCLIInvocationIsTheOperator(t *testing.T) {
	v, ok := verbs.ByName("doctor")
	if !ok {
		t.Fatal("doctor is not a verb")
	}
	for name, tc := range map[string]struct {
		pane string
		argv []string
		want string
	}{
		"outside a pane":           {"", nil, "human"},
		"inside a pane":            {"wT:p1", nil, "agent:wT:p1"},
		"outside a pane with --as": {"", []string{"--as", "cron:nightly"}, "cron:nightly"},
		"inside a pane with --as":  {"wT:p1", []string{"--as", "cron:nightly"}, "cron:nightly"},
	} {
		testenv.New(t)
		t.Setenv("HERDR_PANE_ID", tc.pane)
		req, _, err := Request(v, tc.argv)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !req.Operator {
			t.Errorf("%s: the CLI door sent no human act for its own argv", name)
		}
		if got := req.Caller(); got != tc.want {
			t.Errorf("%s: caller = %q, want %q", name, got, tc.want)
		}
	}
}

// The three §3.2 kinds a call MAY declare are the ones this plugin's own
// principals are spelled with (note 1: cron:<job id> and trigger:<id>).
func TestCronAndTriggerPrincipalsMayBeDeclared(t *testing.T) {
	for _, as := range []string{"cron:nightly-sweep", "trigger:01H", "plugin:herdr-sched"} {
		got, err := principal(as)
		if err != nil || got != as {
			t.Errorf("--as %q = %q / %v, want it declared", as, got, err)
		}
	}
}

// Naming one project and every project is refused rather than ranked.
func TestNamingOneProjectAndEveryProjectIsRefused(t *testing.T) {
	testenv.New(t)
	_, _, err := Scope(".", true, nil)
	if err == nil {
		t.Fatal("both were accepted")
	}
	if codes.Of(err) != codes.Usage {
		t.Fatalf("code = %s, want USAGE", codes.Of(err))
	}
}

// --all-projects carries no project, and the default resolves one from the
// working directory (§4.2).
func TestTheScopePairIsOneOrTheOther(t *testing.T) {
	testenv.New(t)
	proj, all, err := Scope("", true, nil)
	if err != nil || proj != "" || !all {
		t.Fatalf("all-projects = %q / %t / %v", proj, all, err)
	}
	proj, all, err = Scope("", false, nil)
	if err != nil {
		t.Fatalf("default scope: %v", err)
	}
	if all || proj == "" {
		t.Fatalf("the default resolved to %q / all=%t", proj, all)
	}
}

// --json has to be readable out of a raw argv, because cobra's own parse
// failures are among the failures §6.2 makes answer with one document.
func TestWantsJSONReadsTheFlagOutOfARawArgv(t *testing.T) {
	cases := map[bool][][]string{
		true: {
			{"doctor", "--json"},
			{"--json", "doctor"},
			{"doctor", "--json=true"},
			{"doctor", "--json=nonsense"},
		},
		false: {
			{"doctor"},
			{"doctor", "--json=false"},
			{"doctor", "--", "--json"},
		},
	}
	for want, argvs := range cases {
		for _, argv := range argvs {
			if got := WantsJSON(argv); got != want {
				t.Errorf("WantsJSON(%v) = %t, want %t", argv, got, want)
			}
		}
	}
}

// §6.2: a failure with --json is one envelope carrying the contract code, and
// the message never repeats the code it is already filed under.
func TestTheFailureDocumentCarriesTheCodeOnce(t *testing.T) {
	var out strings.Builder
	err := codes.Parked("pk-1", "the policy gate parked sched.stop for the operator")
	if werr := WriteError(err, &out); werr != nil {
		t.Fatal(werr)
	}
	var body struct {
		Error struct {
			Code     string `json:"code"`
			Message  string `json:"message"`
			ParkedID string `json:"parked_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(out.String()), &body); err != nil {
		t.Fatalf("envelope: %v (%q)", err, out.String())
	}
	if body.Error.Code != string(codes.Denied) {
		t.Errorf("code = %q", body.Error.Code)
	}
	if strings.Contains(body.Error.Message, "DENIED") {
		t.Errorf("the message repeats the code: %q", body.Error.Message)
	}
	// §9.3: a DENIED the gate deferred names the row the operator resolves.
	if body.Error.ParkedID != "pk-1" {
		t.Errorf("parked_id = %q", body.Error.ParkedID)
	}
}

// Only a flag the caller actually wrote is sent. A false nobody typed is an
// argument the daemon would then have to tell apart from one they did.
func TestOnlyTheFlagsTheCallerWroteAreSent(t *testing.T) {
	testenv.New(t)
	v, _ := verbs.ByName("parked.resolve")
	req, _, err := Request(v, []string{"pk-1"})
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if _, ok := req.Args["reject"]; ok {
		t.Fatalf("a flag nobody wrote was sent: %v", req.Args)
	}
	req, _, err = Request(v, []string{"--reject", "pk-1"})
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if req.Args["reject"] != true {
		t.Fatalf("--reject did not travel: %v", req.Args)
	}
}

// --follow is the CLI's own flag, and it is a property of the CONNECTION
// rather than an argument of the verb (§8.2 with §7.1).
func TestFollowIsAConnectionPropertyAndNotAnArgument(t *testing.T) {
	testenv.New(t)
	v, _ := verbs.ByName("events")
	req, _, err := Request(v, []string{"--follow"})
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if !req.Follow {
		t.Fatal("--follow did not reach the request")
	}
	if _, ok := req.Args["follow"]; ok {
		t.Fatalf("--follow travelled as an argument: %v", req.Args)
	}
	// And no other verb offers it.
	root := Root(nil)
	for _, other := range verbs.All {
		if other.Name == "events" {
			continue
		}
		cmd, _, _ := root.Find(other.CLI)
		if cmd.Flags().Lookup("follow") != nil {
			t.Errorf("`hsched %s` offers --follow", strings.Join(other.CLI, " "))
		}
	}
}

// A required positional the caller left out is a USAGE refusal, not a call.
func TestAMissingPositionalIsARefusal(t *testing.T) {
	testenv.New(t)
	v, _ := verbs.ByName("parked.resolve")
	_, _, err := Request(v, nil)
	if err == nil {
		t.Fatal("parked resolve with no id was sent")
	}
	if codes.Of(err) != codes.Usage {
		t.Fatalf("code = %s, want USAGE", codes.Of(err))
	}
}

// A bare group prints its help; a group with a stray argument is a USAGE
// refusal. Cobra returns help for a command that is not Runnable BEFORE it
// validates arguments, so the parent has to be Runnable for NoArgs to be
// reached at all.
func TestAGroupWithAStrayArgumentIsARefusal(t *testing.T) {
	root := Root(nil)
	root.SetOut(discard{})
	root.SetErr(discard{})
	root.SetArgs([]string{"parked", "nonsense"})
	if err := root.Execute(); err == nil {
		t.Fatal("`hsched parked nonsense` exited without a failure")
	}

	root = Root(nil)
	root.SetOut(discard{})
	root.SetErr(discard{})
	root.SetArgs([]string{"parked"})
	if err := root.Execute(); err != nil {
		t.Fatalf("`hsched parked` should print its help: %v", err)
	}
}

// Cobra's own failures reach the caller as USAGE, which is what §6.3 fixes for
// caller input, rather than as UNAVAILABLE.
func TestCobrasOwnFailuresAreUsageErrors(t *testing.T) {
	root := Root(nil)
	root.SetOut(discard{})
	root.SetErr(discard{})
	root.SetArgs([]string{"doctor", "--nonsense"})
	err := AsRefusal(root.Execute())
	if codes.Of(err) != codes.Usage {
		t.Fatalf("code = %s, want USAGE", codes.Of(err))
	}
	// A refusal already named keeps its own code.
	named := codes.Refusef(codes.NotRunning, "nothing is listening")
	if codes.Of(AsRefusal(named)) != codes.Conflict {
		t.Fatal("AsRefusal overwrote a code the daemon sent back")
	}
}

// The renderer prints what the daemon sent, byte for byte, when the caller
// asked for a document.
func TestJSONOutputIsTheDaemonsOwnBytes(t *testing.T) {
	var out strings.Builder
	raw := json.RawMessage(`{"stopping":true,"socket":"/x/sched.sock","pid":7}`)
	if err := Write("stop", raw, true, &out); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != string(raw) {
		t.Fatalf("printed %q, want the daemon's own %q", out.String(), raw)
	}
}

// The trigger half is read on the CLI the same way the cron half is: a line
// an operator reads, not the daemon's document. A verb with no rendering falls
// through to raw JSON, which is the machine's answer handed to a person.
func TestTheTriggerVerbsRenderForAPerson(t *testing.T) {
	row := `{"id":"deploy","kind":"webhook","action":{"kind":"task","args":{"title":"deploy asked for"}},` +
		`"cooldown_seconds":60,"max_per_hour":10,"enabled":true,"created_at":1,` +
		`"url":"http://127.0.0.1:8797/trigger/deploy","fired_this_hour":2}`
	states := map[string]string{
		"trigger.add": "added", "trigger.remove": "removed",
		"trigger.enable": "enabled", "trigger.disable": "disabled",
	}
	for verb, wants := range map[string][]string{
		"trigger.list":    {"deploy", "webhook", "task", "http://127.0.0.1:8797/trigger/deploy"},
		"trigger.add":     {"deploy", "added", "s3cret"},
		"trigger.remove":  {"deploy", "removed"},
		"trigger.enable":  {"deploy", "enabled"},
		"trigger.disable": {"deploy", "disabled"},
	} {
		body := `{"triggers":[` + row + `],"count":1}`
		if verb != "trigger.list" {
			body = `{"trigger":` + row + `,"state":"` + states[verb] + `","changed":true`
			if verb == "trigger.add" {
				body += `,"secret":"s3cret"`
			}
			body += `}`
		}
		var out strings.Builder
		if err := Write(verb, json.RawMessage(body), false, &out); err != nil {
			t.Fatalf("%s: %v", verb, err)
		}
		if strings.Contains(out.String(), `"id"`) {
			t.Errorf("%s printed the document rather than a line: %q", verb, out.String())
		}
		for _, want := range wants {
			if !strings.Contains(out.String(), want) {
				t.Errorf("%s printed %q, want it to carry %q", verb, out.String(), want)
			}
		}
	}
	// The secret is the one thing shown once, and only add ever has one.
	var out strings.Builder
	if err := Write("trigger.list", json.RawMessage(`{"triggers":[`+row+`],"count":1}`), false, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "secret") {
		t.Errorf("the list printed something about a secret: %q", out.String())
	}
}

// dump says what the document holds, and every entity in it is counted. An
// entity the line leaves out is one an operator reading the summary believes
// is not there.
func TestTheDumpLineCountsEveryEntity(t *testing.T) {
	var out strings.Builder
	body := `{"version":2,"path":"/x/sched.json","store":{"version":2,"parked":[],"parked_events":[],` +
		`"jobs":[],"job_events":[],"triggers":[{"id":"deploy"}],"trigger_events":[{"id":"ev-1"}],"run_events":[]}}`
	if err := Write("dump", json.RawMessage(body), false, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "triggers") {
		t.Errorf("dump does not count the triggers: %q", out.String())
	}
	if !strings.Contains(out.String(), "1 rows, 1 events") {
		t.Errorf("dump does not count the trigger rows and their trail: %q", out.String())
	}
}

// An operator reading prose gets a sentence, and an empty answer says so
// rather than printing nothing at all.
func TestAnEmptyAnswerSaysSo(t *testing.T) {
	for verb, want := range map[string]string{
		"events":       "the trail holds nothing",
		"parked.list":  "has parked nothing",
		"trigger.list": "no triggers here yet",
	} {
		var out strings.Builder
		if err := Write(verb, json.RawMessage(`{"count":0}`), false, &out); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), want) {
			t.Errorf("%s printed %q, want it to mention %q", verb, out.String(), want)
		}
	}
}
