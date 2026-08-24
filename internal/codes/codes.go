// Package codes is the failure vocabulary both doors answer with.
//
// The top-level names are the shared plugin contract's own (§6.3) and nothing
// else: a caller branching on a code reads the same nine words from every
// plugin, and §6.3 forbids a plugin inventing a tenth. What this binary
// refuses for is finer than nine words, so the finer name travels as a
// sub-reason INSIDE the message, which is the one place §6.3 leaves for it.
package codes

import (
	"errors"
	"fmt"
	"strings"
)

// Code is one of the contract's nine. There are no others.
type Code string

const (
	// Usage is a caller-validatable input error.
	Usage Code = "USAGE"
	// NotFound is a named entity that does not exist in scope.
	NotFound Code = "NOT_FOUND"
	// Unavailable is the daemon, a sibling binary or herdr not answering.
	Unavailable Code = "UNAVAILABLE"
	// Timeout is a bounded wait that elapsed.
	Timeout Code = "TIMEOUT"
	// Conflict is a state guard that failed: already running, already
	// resolved, no room.
	Conflict Code = "CONFLICT"
	// Unsupported is the host or Herdr lacking a capability the verb needs.
	Unsupported Code = "UNSUPPORTED"
	// Forbidden is a caller principal that may not do this to this target.
	Forbidden Code = "FORBIDDEN"
	// Denied is the policy gate saying no.
	Denied Code = "DENIED"
	// Unexpected is anything else.
	Unexpected Code = "UNEXPECTED"
)

// exits is the §6.3 exit status of each code.
var exits = map[Code]int{
	Usage:       2,
	NotFound:    3,
	Unavailable: 4,
	Timeout:     5,
	Conflict:    6,
	Unsupported: 7,
	Forbidden:   8,
	Denied:      9,
	Unexpected:  1,
}

// Exit is the process exit status the contract fixes for a code. Anything the
// table does not name is UNEXPECTED's 1, which is what the contract calls
// anything else.
func Exit(code Code) int {
	if e, ok := exits[code]; ok {
		return e
	}
	return exits[Unexpected]
}

// Reason is one of this binary's own sub-reasons. It is never a code: it is
// the first word of the message, and the code beside it is the contract's.
type Reason string

const (
	// Invalid is a request this binary cannot make sense of: an unknown
	// verb, a missing argument, an argument of the wrong kind.
	Invalid Reason = "INVALID"
	// AlreadyRunning is a second daemon meeting the first one's lock.
	AlreadyRunning Reason = "ALREADY_RUNNING"
	// NotRunning is a verb that needs a live daemon and found none. Only
	// stop answers with it: every other verb starts one rather than refuse.
	NotRunning Reason = "NOT_RUNNING"
	// AlreadyResolved is a parked action a second resolver reached after the
	// first one had already decided it.
	AlreadyResolved Reason = "ALREADY_RESOLVED"
)

// carries maps each sub-reason onto the contract code it is a reason for.
var carries = map[Reason]Code{
	Invalid: Usage,
	// Each of these is a state guard that failed, which is what §6.3 calls
	// CONFLICT.
	AlreadyRunning:  Conflict,
	NotRunning:      Conflict,
	AlreadyResolved: Conflict,
}

// Error is a failure carrying a contract code.
type Error struct {
	Code    Code
	Message string
	// ParkedID names the row a DENIED left behind when the policy gate
	// deferred the call rather than refusing it (§9.3). It is empty on
	// every other failure, and on a DENIED the gate meant.
	ParkedID string
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Message }

// Errorf builds a failure under one of the contract's own codes.
func Errorf(code Code, format string, a ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, a...)}
}

// Refusef builds a failure under the contract code a sub-reason belongs to,
// with the sub-reason kept as the first word of the message so a caller can
// tell one CONFLICT from another.
func Refusef(reason Reason, format string, a ...any) *Error {
	code, ok := carries[reason]
	if !ok {
		code = Unexpected
	}
	return &Error{Code: code, Message: string(reason) + ": " + fmt.Sprintf(format, a...)}
}

// Parked is a DENIED that names the action the policy gate deferred (§9.3).
// A caller told only that it was denied has nothing to resolve.
func Parked(id, format string, a ...any) *Error {
	return &Error{Code: Denied, Message: fmt.Sprintf(format, a...), ParkedID: id}
}

// ParkedOf reports the parked row err names, or empty when it names none.
func ParkedOf(err error) string {
	var named *Error
	if errors.As(err, &named) {
		return named.ParkedID
	}
	return ""
}

// Of reports the code err carries, at any depth. A failure that carries none
// is Unavailable: everything in this binary that fails without a name of its
// own failed reaching something else.
func Of(err error) Code {
	if err == nil {
		return ""
	}
	var named *Error
	if errors.As(err, &named) {
		return named.Code
	}
	return Unavailable
}

// ReasonOf reports the sub-reason err refuses for, or empty when it carries
// none. The code is what a caller outside this binary branches on; this is how
// a caller inside it tells one CONFLICT from another.
func ReasonOf(err error) Reason {
	var named *Error
	if !errors.As(err, &named) {
		return ""
	}
	for reason := range carries {
		if strings.HasPrefix(named.Message, string(reason)+": ") {
			return reason
		}
	}
	return ""
}

// Named reports whether err carries a code of this binary's own. Everything
// that fails inside here does; an error that does not came from a library,
// and Of would call it Unavailable, which for a caller's own typo is the
// wrong word and the wrong exit status.
func Named(err error) bool {
	var named *Error
	return errors.As(err, &named)
}

// Message is the sentence a caller reads, without the code repeated in front
// of it: the envelope already carries the code in its own field.
func Message(err error) string {
	var named *Error
	if errors.As(err, &named) {
		return named.Message
	}
	return err.Error()
}
