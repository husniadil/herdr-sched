package client

import (
	"strings"
	"testing"
	"time"

	"github.com/husniadil/herdr-sched/internal/codes"
	"github.com/husniadil/herdr-sched/internal/config"
	"github.com/husniadil/herdr-sched/internal/protocol"
	"github.com/husniadil/herdr-sched/internal/testenv"
)

// `stop` is the one verb that refuses rather than starting a daemon: nothing
// is started just to be stopped.
func TestNoStartRefusesRatherThanStartingADaemon(t *testing.T) {
	testenv.New(t)
	_, err := (&Client{NoStart: true}).Call(protocol.Request{Verb: "stop"})
	if err == nil {
		t.Fatal("a call with NoStart started a daemon")
	}
	if codes.Of(err) != codes.Conflict || codes.ReasonOf(err) != codes.NotRunning {
		t.Fatalf("refused with %s / %s", codes.Of(err), codes.ReasonOf(err))
	}
	// The refusal names the socket it looked on, which is what an operator
	// with two state dirs needs to see.
	if !strings.Contains(codes.Message(err), config.SocketPath()) {
		t.Errorf("the refusal does not name the socket: %v", err)
	}
}

// A daemon that starts and then refuses to run — a store document from a
// version it will not read, a state dir it cannot open — is reported with the
// exit it died on. Discarding the child's exit leaves every one of those
// failures wearing the same generic timeout, and doctor is exactly the verb an
// operator reaches for then.
func TestADaemonThatExitsIsReportedWithTheExitItDiedOn(t *testing.T) {
	testenv.SkipUnlessFull(t)
	f := testenv.New(t)
	f.Bin(t, "refuses", "exit 3")
	start := time.Now()
	_, err := (&Client{Bin: f.Path("refuses"), Timeout: 10 * time.Second}).
		Call(protocol.Request{Verb: "doctor"})
	if err == nil {
		t.Fatal("a call reached a daemon that exited")
	}
	if codes.Of(err) != codes.Unavailable {
		t.Fatalf("code = %s, want UNAVAILABLE", codes.Of(err))
	}
	if !strings.Contains(codes.Message(err), "exit status 3") {
		t.Errorf("the failure does not carry the daemon's own exit: %v", err)
	}
	// And it is answered when the daemon dies, not when the wait runs out.
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("the caller waited %s for a daemon that had already exited", elapsed)
	}
}

// A daemon that cannot be started at all is UNAVAILABLE — the environment, not
// the caller's command line.
func TestADaemonThatCannotBeStartedIsUnavailable(t *testing.T) {
	testenv.SkipUnlessFull(t)
	f := testenv.New(t)
	_, err := (&Client{Bin: f.Path("not-a-binary")}).Call(protocol.Request{Verb: "doctor"})
	if err == nil {
		t.Fatal("a call reached a daemon that could not be started")
	}
	if codes.Of(err) != codes.Unavailable {
		t.Fatalf("code = %s, want UNAVAILABLE", codes.Of(err))
	}
}
