package codes

import (
	"errors"
	"fmt"
	"testing"
)

// §6.3 fixes both the vocabulary and the exit status of each word. A caller
// scripting three sibling plugins reads the same number from each.
func TestEveryContractCodeHasItsFixedExitStatus(t *testing.T) {
	want := map[Code]int{
		Usage: 2, NotFound: 3, Unavailable: 4, Timeout: 5, Conflict: 6,
		Unsupported: 7, Forbidden: 8, Denied: 9, Unexpected: 1,
	}
	if len(exits) != len(want) {
		t.Fatalf("the table holds %d codes and §6.3 names %d", len(exits), len(want))
	}
	for code, status := range want {
		if got := Exit(code); got != status {
			t.Errorf("%s exits %d, §6.3 fixes %d", code, got, status)
		}
	}
	if got := Exit(Code("SOMETHING_ELSE")); got != 1 {
		t.Errorf("an unknown code exits %d, want UNEXPECTED's 1", got)
	}
}

// A sub-reason is never a code: it travels as the first word of the message,
// under the contract code it is a reason for.
func TestASubReasonTravelsInsideTheMessageUnderAContractCode(t *testing.T) {
	err := Refusef(NotRunning, "no hsched daemon is listening on %s", "/tmp/sched.sock")
	if Of(err) != Conflict {
		t.Fatalf("code = %s, want CONFLICT", Of(err))
	}
	if ReasonOf(err) != NotRunning {
		t.Fatalf("reason = %s, want NOT_RUNNING", ReasonOf(err))
	}
	if got := Message(err); got != "NOT_RUNNING: no hsched daemon is listening on /tmp/sched.sock" {
		t.Fatalf("message = %q", got)
	}
}

// Every sub-reason maps onto one of the nine. A reason with no code is a
// refusal that would reach a caller as UNEXPECTED.
func TestEverySubReasonNamesAContractCode(t *testing.T) {
	for reason, code := range carries {
		if _, ok := exits[code]; !ok {
			t.Errorf("%s carries %q, which is not one of §6.3's nine", reason, code)
		}
	}
	for _, reason := range []Reason{Invalid, AlreadyRunning, NotRunning, AlreadyResolved} {
		if _, ok := carries[reason]; !ok {
			t.Errorf("%s is declared and carries no contract code", reason)
		}
	}
}

// §9.3: a DENIED the gate deferred names the row the operator resolves, at any
// depth, because a caller told only that it was denied has nothing to point at.
func TestADeferredDenialCarriesTheParkedRow(t *testing.T) {
	err := Parked("pk-1", "the policy gate parked %s for the operator", "sched.stop")
	if Of(err) != Denied {
		t.Fatalf("code = %s, want DENIED", Of(err))
	}
	if ParkedOf(err) != "pk-1" {
		t.Fatalf("parked id = %q", ParkedOf(err))
	}
	wrapped := fmt.Errorf("resolving: %w", err)
	if ParkedOf(wrapped) != "pk-1" {
		t.Fatalf("a wrapped denial lost its parked id")
	}
	if ParkedOf(errors.New("plain")) != "" {
		t.Fatal("an unnamed error claimed a parked row")
	}
}

// An error from outside this binary failed reaching something else, and is
// never mistaken for a caller's own typo.
func TestAnUnnamedErrorIsUnavailableAndNotNamed(t *testing.T) {
	err := errors.New("dial: connection refused")
	if Of(err) != Unavailable {
		t.Errorf("code = %s, want UNAVAILABLE", Of(err))
	}
	if Named(err) {
		t.Error("an unnamed error reported itself as named")
	}
	if !Named(Errorf(Usage, "no")) {
		t.Error("a coded error reported itself as unnamed")
	}
}
