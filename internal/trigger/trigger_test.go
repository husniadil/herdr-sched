package trigger

import (
	"strings"
	"testing"
	"time"

	"github.com/husniadil/herdr-sched/internal/action"
	"github.com/husniadil/herdr-sched/internal/codes"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func webhook() Trigger {
	return Trigger{
		ID:      "deploy",
		Kind:    KindWebhook,
		Action:  action.Action{Kind: action.KindShell, Args: map[string]string{"command": "echo hi"}},
		Enabled: true,
	}
}

// The id is the last segment of the URL and the id half of `trigger:<id>`, so
// it carries nothing that would not survive either.
func TestATriggerIDCarriesNothingThatWouldBreakItsPrincipalOrItsURL(t *testing.T) {
	for _, id := range []string{"", "  ", "one two", "cron:nightly", "a/b", "a?b", "a#b", "a%2f"} {
		if err := ValidateID(id); err == nil {
			t.Errorf("the id %q was accepted", id)
		}
	}
	for _, id := range []string{"deploy", "deploy-prod", "deploy_prod.2"} {
		if err := ValidateID(id); err != nil {
			t.Errorf("the id %q was refused: %v", id, err)
		}
	}
}

func TestAWebhookWatchesNoPathAndAWatchHasOne(t *testing.T) {
	w := webhook()
	w.Path = "/tmp/x"
	if err := w.Validate(); err == nil {
		t.Error("a webhook carrying a path was accepted")
	}
	watch := webhook()
	watch.Kind, watch.Path = KindWatch, ""
	if err := watch.Validate(); err == nil {
		t.Error("a watch with no path was accepted")
	}
	watch.Path = "/tmp/x"
	if err := watch.Validate(); err != nil {
		t.Errorf("a watch on a path was refused: %v", err)
	}
}

// A relative path would be stat'd against the DAEMON's working directory,
// which is not the caller's. A watcher on the wrong file looks exactly like a
// watcher on a file that never changes.
func TestAWatchPathIsAbsolute(t *testing.T) {
	watch := webhook()
	watch.Kind = KindWatch
	for _, path := range []string{"inbox", "./inbox", "../inbox", "a/b"} {
		watch.Path = path
		err := watch.Validate()
		if err == nil {
			t.Errorf("the relative watch path %q was accepted", path)
			continue
		}
		if !strings.Contains(codes.Message(err), "absolute") {
			t.Errorf("the refusal of %q does not say why: %v", path, err)
		}
	}
}

func TestATriggerKindOutsideTheTwoIsRefused(t *testing.T) {
	w := webhook()
	w.Kind = "poll"
	err := w.Validate()
	if err == nil {
		t.Fatal("the kind `poll` was accepted")
	}
	if !strings.Contains(codes.Message(err), KindWebhook) {
		t.Errorf("the refusal does not name the kinds there are: %v", err)
	}
}

// The whole point of the vocabulary being validated at create time is that a
// trigger cannot be written carrying an action nothing can run.
func TestATriggerIsRefusedForTheActionItCouldNotFire(t *testing.T) {
	w := webhook()
	w.Action = action.Action{Kind: action.KindTask}
	if err := w.Validate(); err == nil {
		t.Error("a task action with no title was accepted")
	}
}

func TestTheSourceIsTheTriggerPrincipal(t *testing.T) {
	if got := webhook().Source().Principal(); got != "trigger:deploy" {
		t.Errorf("the principal is %q, not trigger:deploy", got)
	}
}

// The cooldown is what turns a replayed webhook request into a refusal rather
// than a second firing.
func TestAFiringInsideTheCooldownIsRefusedAndOneAfterItIsNot(t *testing.T) {
	now := at("2026-08-25T10:00:00Z")
	w := webhook()
	w.CooldownSeconds = 60
	w.LastFiredMS = now.Add(-30 * time.Second).UnixMilli()

	v := Allow(now, w)
	if v.Fire {
		t.Fatal("a firing 30s into a 60s cooldown was allowed")
	}
	if v.Limit != LimitCooldown {
		t.Errorf("the limit is %q, not %q", v.Limit, LimitCooldown)
	}
	if !strings.Contains(v.Reason, "30s") {
		t.Errorf("the reason does not say how much cooldown is left: %q", v.Reason)
	}

	if !Allow(now.Add(31*time.Second), w).Fire {
		t.Error("a firing past the cooldown was refused")
	}
}

// A trigger that has never fired has no cooldown to be inside.
func TestTheFirstFiringMeetsNoCooldown(t *testing.T) {
	w := webhook()
	w.CooldownSeconds = 3600
	if !Allow(at("2026-08-25T10:00:00Z"), w).Fire {
		t.Error("the first firing was held down by a cooldown it had never spent")
	}
}

func TestADisabledTriggerFiresNothing(t *testing.T) {
	w := webhook()
	w.Enabled = false
	v := Allow(at("2026-08-25T10:00:00Z"), w)
	if v.Fire || v.Limit != LimitDisabled {
		t.Errorf("a disabled trigger answered %+v", v)
	}
}

// max-fires-per-hour, proven in the pure core: the limit counts firings inside
// the window and nothing outside it.
func TestTheHourlyLimitRefusesTheOneOverAndForgetsWhatFellOutOfTheWindow(t *testing.T) {
	now := at("2026-08-25T10:00:00Z")
	w := webhook()
	w.MaxPerHour = 3
	w.FiredMS = []int64{
		now.Add(-50 * time.Minute).UnixMilli(),
		now.Add(-20 * time.Minute).UnixMilli(),
		now.Add(-1 * time.Minute).UnixMilli(),
	}

	v := Allow(now, w)
	if v.Fire {
		t.Fatal("the fourth firing in an hour was allowed against max_per_hour 3")
	}
	if v.Limit != LimitRate {
		t.Errorf("the limit is %q, not %q", v.Limit, LimitRate)
	}
	if !strings.Contains(v.Reason, "3") {
		t.Errorf("the reason does not name the limit: %q", v.Reason)
	}

	// Ten minutes on, the oldest of the three has left the window.
	if !Allow(now.Add(11*time.Minute), w).Fire {
		t.Error("a firing was refused on a count that had fallen out of the window")
	}
}

func TestNoLimitConfiguredMeansNoLimitEnforced(t *testing.T) {
	now := at("2026-08-25T10:00:00Z")
	w := webhook()
	for i := 0; i < 50; i++ {
		w.FiredMS = append(w.FiredMS, now.Add(-time.Duration(i)*time.Minute).UnixMilli())
	}
	w.LastFiredMS = now.UnixMilli()
	if !Allow(now, w).Fire {
		t.Error("a trigger with neither limit set was held down")
	}
}

// The row keeps only the hour the limit reads, so it never grows without bound.
func TestFiringMovesTheCursorAndDropsWhatLeftTheWindow(t *testing.T) {
	now := at("2026-08-25T10:00:00Z")
	w := webhook()
	w.FiredMS = []int64{
		now.Add(-2 * time.Hour).UnixMilli(),
		now.Add(-10 * time.Minute).UnixMilli(),
	}

	after := Fired(now, w)
	if after.LastFiredMS != now.UnixMilli() {
		t.Errorf("the cursor is %d, not %d", after.LastFiredMS, now.UnixMilli())
	}
	if len(after.FiredMS) != 2 {
		t.Fatalf("the hour's count is %v; the two-hour-old firing was kept", after.FiredMS)
	}
	if after.FiredMS[1] != now.UnixMilli() {
		t.Error("the firing just recorded is not the newest in the count")
	}
}

// The first look records and does not fire: a trigger written against a file
// that already exists must not fire for a change that predates it.
func TestTheFirstLookAtAWatchedPathRecordsAndDoesNotFire(t *testing.T) {
	stamp, fire := Changed(Stamp{}, Stamp{Present: true, ModNS: 1000, Size: 7})
	if fire {
		t.Error("the first look fired")
	}
	if !stamp.Seen || !stamp.Present || stamp.ModNS != 1000 || stamp.Size != 7 {
		t.Errorf("the first look recorded %+v", stamp)
	}
}

func TestAWatchedPathFiresOnEveryKindOfChangeAndOnNothingElse(t *testing.T) {
	was := Stamp{Seen: true, Present: true, ModNS: 1000, Size: 7}
	for what, now := range map[string]Stamp{
		"a newer mtime":  {Seen: true, Present: true, ModNS: 2000, Size: 7},
		"a new size":     {Seen: true, Present: true, ModNS: 1000, Size: 9},
		"a deleted file": {Seen: true, Present: false},
	} {
		if _, fire := Changed(was, now); !fire {
			t.Errorf("%s did not fire", what)
		}
	}
	if _, fire := Changed(was, Stamp{Seen: true, Present: true, ModNS: 1000, Size: 7}); fire {
		t.Error("an unchanged file fired")
	}
	// A file written again after being deleted is a change, and the two zeroes
	// of the absent look must not read as "never looked".
	gone := Stamp{Seen: true, Present: false}
	if _, fire := Changed(gone, Stamp{Seen: true, Present: true, ModNS: 3000, Size: 1}); !fire {
		t.Error("a file written again after deletion did not fire")
	}
	if _, fire := Changed(gone, Stamp{Seen: true, Present: false}); fire {
		t.Error("a path that is still absent fired")
	}
}

func TestAValidSignatureHoldsAndEveryWayOfBeingWrongDoesNot(t *testing.T) {
	secret, err := NewSecret()
	if err != nil {
		t.Fatalf("secret: %v", err)
	}
	body := []byte(`{"ref":"refs/heads/main"}`)
	if err := Verify(secret, body, Sign(secret, body)); err != nil {
		t.Fatalf("a signature this package made did not hold: %v", err)
	}

	for what, header := range map[string]string{
		"no header at all":            "",
		"an unnamed algorithm":        strings.TrimPrefix(Sign(secret, body), SignaturePrefix),
		"a signature that is not hex": SignaturePrefix + "zz",
		"another body's signature":    Sign(secret, []byte("something else")),
		"another secret's signature":  Sign(secret+"x", body),
	} {
		err := Verify(secret, body, header)
		if err == nil {
			t.Errorf("%s was accepted", what)
			continue
		}
		if codes.Of(err) != codes.Forbidden {
			t.Errorf("%s answered %s, not %s", what, codes.Of(err), codes.Forbidden)
		}
	}
}

func TestASecretIsDrawnFreshEveryTime(t *testing.T) {
	a, err := NewSecret()
	if err != nil {
		t.Fatalf("secret: %v", err)
	}
	b, _ := NewSecret()
	if a == b {
		t.Fatal("two secrets came out the same")
	}
	if len(a) != SecretBytes*2 {
		t.Errorf("a secret is %d hex characters, not %d", len(a), SecretBytes*2)
	}
}
