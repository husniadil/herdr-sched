// Package cron reads the standard five-field cron expression and answers when
// it fires. It is a pure decision core: no clock of its own, no goroutine, no
// state — a Schedule is asked what the instant before or after a given time
// is, and everything that schedules anything is built on those two answers.
//
// The arithmetic is UTC and only UTC. A local zone would put a schedule inside
// the two hours a DST transition either repeats or never has, where "3am
// daily" means twice one night and never another, and there is no answer to
// that an operator would not have to be told about. Refusing the question is
// the honest one: a job's expression is read against UTC, the README says so,
// and an operator who wants a local hour writes the UTC hour it falls on.
//
// A parser rather than a dependency, because it is this: 200 lines of
// bit-setting over five bounded fields, against a syntax that has not moved in
// forty years (note 2, and herdr-dispatch's README records how a dependency
// earns its way in).
package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/husniadil/herdr-sched/internal/codes"
)

// searchYears bounds both walks. A schedule that names an instant no calendar
// has — 30 february — must answer "never" rather than walk forever, and five
// years is longer than any expression that fires at all will ever need.
const searchYears = 5

// Schedule is one parsed expression: a bitmask per field, and whether the two
// day fields were restricted, which is what decides how they combine.
type Schedule struct {
	minute, hour, dom, month, dow uint64
	domStar, dowStar              bool
	expr                          string
}

// String is the expression as it was written, which is what a job row carries
// and what an operator reads back.
func (s Schedule) String() string { return s.expr }

type field struct {
	name     string
	min, max int
	names    map[string]int
}

var months = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}

var days = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}

// fields is the five, in the order a cron line writes them.
var fields = []field{
	{name: "minute", min: 0, max: 59},
	{name: "hour", min: 0, max: 23},
	{name: "day of month", min: 1, max: 31},
	{name: "month", min: 1, max: 12, names: months},
	// 7 is accepted and folded onto 0: every cron spells Sunday both ways.
	{name: "day of week", min: 0, max: 7, names: days},
}

// Parse reads one five-field expression. Everything it accepts can be asked
// for a next and a previous instant; everything it refuses names the field it
// refused, because "invalid" on a five-field line leaves an operator counting
// columns.
func Parse(expr string) (Schedule, error) {
	parts := strings.Fields(expr)
	if len(parts) != len(fields) {
		return Schedule{}, codes.Errorf(codes.Usage,
			"a schedule is five fields — minute hour day-of-month month day-of-week — and %q is %d",
			expr, len(parts))
	}
	s := Schedule{expr: strings.Join(parts, " ")}
	masks := make([]uint64, len(fields))
	for i, f := range fields {
		mask, err := parseField(f, parts[i])
		if err != nil {
			return Schedule{}, err
		}
		masks[i] = mask
	}
	s.minute, s.hour, s.dom, s.month = masks[0], masks[1], masks[2], masks[3]
	// 7 means Sunday, and folding it here is what lets every later read use
	// Go's own 0-6 weekday without asking which spelling was written.
	s.dow = masks[4]
	if s.dow&(1<<7) != 0 {
		s.dow = s.dow&^(1<<7) | 1
	}
	s.domStar, s.dowStar = parts[2] == "*", parts[4] == "*"
	return s, nil
}

// parseField turns one comma-separated field into its bitmask.
func parseField(f field, text string) (uint64, error) {
	var mask uint64
	for _, term := range strings.Split(text, ",") {
		bits, err := parseTerm(f, term)
		if err != nil {
			return 0, err
		}
		mask |= bits
	}
	if mask == 0 {
		return 0, refuse(f, text, "it names no value")
	}
	return mask, nil
}

// parseTerm reads one of `*`, `*/n`, `a`, `a-b` and `a-b/n`, with a name where
// the field has names.
func parseTerm(f field, term string) (uint64, error) {
	body, stepText, hasStep := strings.Cut(term, "/")
	step := 1
	if hasStep {
		n, err := strconv.Atoi(stepText)
		if err != nil || n <= 0 {
			return 0, refuse(f, term, "a step is a positive whole number")
		}
		step = n
	}
	lo, hi := f.min, f.max
	if body != "*" {
		loText, hiText, isRange := strings.Cut(body, "-")
		n, err := value(f, loText)
		if err != nil {
			return 0, err
		}
		lo, hi = n, n
		switch {
		case isRange:
			m, err := value(f, hiText)
			if err != nil {
				return 0, err
			}
			hi = m
		case hasStep:
			// `5/15` is the same open-ended step every cron reads it as.
			hi = f.max
		}
		if lo > hi {
			return 0, refuse(f, term, fmt.Sprintf("%d is after %d", lo, hi))
		}
	}
	var mask uint64
	for v := lo; v <= hi; v += step {
		mask |= 1 << uint(v)
	}
	return mask, nil
}

// value reads one number or one three-letter name, and holds it to the field's
// own range.
func value(f field, text string) (int, error) {
	text = strings.TrimSpace(text)
	if f.names != nil {
		if n, ok := f.names[strings.ToLower(text)]; ok {
			return n, nil
		}
	}
	n, err := strconv.Atoi(text)
	if err != nil {
		return 0, refuse(f, text, "it is neither a number nor a name")
	}
	if n < f.min || n > f.max {
		return 0, refuse(f, text, fmt.Sprintf("the field runs %d to %d", f.min, f.max))
	}
	return n, nil
}

func refuse(f field, term, why string) error {
	return codes.Errorf(codes.Usage, "the %s field cannot read %q: %s", f.name, term, why)
}

// Matches reports whether the schedule fires at that minute. Seconds are not
// part of a schedule and are not read.
func (s Schedule) Matches(t time.Time) bool {
	t = t.UTC()
	return s.month&(1<<uint(t.Month())) != 0 &&
		s.hour&(1<<uint(t.Hour())) != 0 &&
		s.minute&(1<<uint(t.Minute())) != 0 &&
		s.dayMatches(t)
}

// dayMatches is cron's one irregular rule: with both day fields restricted the
// schedule fires when EITHER matches, so `0 0 1 * mon` is the first of the
// month AND every Monday. With one of them `*`, only the other decides.
func (s Schedule) dayMatches(t time.Time) bool {
	dom := s.dom&(1<<uint(t.Day())) != 0
	dow := s.dow&(1<<uint(t.Weekday())) != 0
	switch {
	case s.domStar && s.dowStar:
		return true
	case s.domStar:
		return dow
	case s.dowStar:
		return dom
	}
	return dom || dow
}

// Next is the first instant the schedule fires at STRICTLY after the given
// time. The second answer is false when there is none within the bounded
// search, which is what a schedule no calendar can satisfy answers.
func (s Schedule) Next(after time.Time) (time.Time, bool) {
	t := after.UTC().Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(searchYears, 0, 0)
	for t.Before(limit) {
		switch {
		case s.month&(1<<uint(t.Month())) == 0:
			// Skip the whole month rather than its minutes: 11 unmatched
			// months is otherwise half a million steps.
			t = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
		case !s.dayMatches(t):
			t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
		case s.hour&(1<<uint(t.Hour())) == 0:
			t = t.Truncate(time.Hour).Add(time.Hour)
		case s.minute&(1<<uint(t.Minute())) == 0:
			t = t.Add(time.Minute)
		default:
			return t, true
		}
	}
	return time.Time{}, false
}

// Prev is the last instant the schedule fires at AT OR BEFORE the given time.
// At or before rather than strictly before, because it answers "which
// scheduled minute is the one we are in or have just passed", which is the
// question a due computation asks.
func (s Schedule) Prev(before time.Time) (time.Time, bool) {
	t := before.UTC().Truncate(time.Minute)
	limit := t.AddDate(-searchYears, 0, 0)
	for t.After(limit) {
		switch {
		case s.month&(1<<uint(t.Month())) == 0:
			// The last minute of the previous month: a step back from its
			// first minute.
			t = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC).Add(-time.Minute)
		case !s.dayMatches(t):
			t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).Add(-time.Minute)
		case s.hour&(1<<uint(t.Hour())) == 0:
			t = t.Truncate(time.Hour).Add(-time.Minute)
		case s.minute&(1<<uint(t.Minute())) == 0:
			t = t.Add(-time.Minute)
		default:
			return t, true
		}
	}
	return time.Time{}, false
}
