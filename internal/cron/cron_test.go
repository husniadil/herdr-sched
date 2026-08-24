package cron

import (
	"strings"
	"testing"
	"time"
)

func at(t *testing.T, s string) time.Time {
	t.Helper()
	when, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("read %q: %v", s, err)
	}
	return when.UTC()
}

// The five fields are minute, hour, day of month, month and day of week, and
// nothing else is a schedule this plugin accepts.
func TestFiveFieldsAndNoOther(t *testing.T) {
	for _, expr := range []string{"", "*", "* * * *", "* * * * * *", "0 0 0 0 0 0"} {
		if _, err := Parse(expr); err == nil {
			t.Errorf("%q parsed as a schedule", expr)
		}
	}
	if _, err := Parse("*/5 0-6 1,15 jan-mar mon"); err != nil {
		t.Fatalf("a five-field expression was refused: %v", err)
	}
}

// A field out of its own range is refused when the job is written, not at
// three in the morning.
func TestAFieldOutOfRangeIsRefused(t *testing.T) {
	for _, expr := range []string{"60 * * * *", "* 24 * * *", "* * 32 * *", "* * * 13 *", "* * * * 8", "* * 0 * *"} {
		if _, err := Parse(expr); err == nil {
			t.Errorf("%q parsed and cannot ever fire", expr)
		}
	}
}

// Every term shape the standard five-field expression carries.
func TestEveryTermShapeIsRead(t *testing.T) {
	cases := []struct {
		expr  string
		when  string
		match bool
	}{
		{"* * * * *", "2026-08-25T13:47:00Z", true},
		{"0 * * * *", "2026-08-25T13:00:00Z", true},
		{"0 * * * *", "2026-08-25T13:01:00Z", false},
		{"*/15 * * * *", "2026-08-25T13:30:00Z", true},
		{"*/15 * * * *", "2026-08-25T13:31:00Z", false},
		{"10-20 * * * *", "2026-08-25T13:15:00Z", true},
		{"10-20/5 * * * *", "2026-08-25T13:16:00Z", false},
		{"10-20/5 * * * *", "2026-08-25T13:20:00Z", true},
		{"1,2,59 * * * *", "2026-08-25T13:59:00Z", true},
		{"0 3 * * *", "2026-08-25T03:00:00Z", true},
		{"0 0 1 * *", "2026-09-01T00:00:00Z", true},
		{"0 0 * aug *", "2026-08-25T00:00:00Z", true},
		{"0 0 * * tue", "2026-08-25T00:00:00Z", true},
		{"0 0 * * 2", "2026-08-25T00:00:00Z", true},
		// 0 and 7 are both Sunday, the way every cron spells it.
		{"0 0 * * 7", "2026-08-30T00:00:00Z", true},
		{"0 0 * * 0", "2026-08-30T00:00:00Z", true},
	}
	for _, c := range cases {
		s, err := Parse(c.expr)
		if err != nil {
			t.Errorf("%q: %v", c.expr, err)
			continue
		}
		if got := s.Matches(at(t, c.when)); got != c.match {
			t.Errorf("%q at %s matched %v, want %v", c.expr, c.when, got, c.match)
		}
	}
}

// Day of month and day of week both restricted is an OR, which is what every
// cron does and what an operator writing `0 0 1 * mon` expects.
func TestARestrictedDayPairIsAnOr(t *testing.T) {
	s, err := Parse("0 0 1 * mon")
	if err != nil {
		t.Fatal(err)
	}
	for _, when := range []string{"2026-09-01T00:00:00Z", "2026-08-31T00:00:00Z"} {
		if !s.Matches(at(t, when)) {
			t.Errorf("%s did not match `0 0 1 * mon`", when)
		}
	}
	if s.Matches(at(t, "2026-09-02T00:00:00Z")) {
		t.Error("2026-09-02 is neither the first nor a Monday and matched anyway")
	}
}

// Next is the first instant strictly after the one it is given, and Prev the
// last one at or before it. The pair is what a due computation is built from.
func TestNextAndPrevBracketAnInstant(t *testing.T) {
	s, err := Parse("0 3 * * *")
	if err != nil {
		t.Fatal(err)
	}
	next, ok := s.Next(at(t, "2026-08-25T03:00:00Z"))
	if !ok || !next.Equal(at(t, "2026-08-26T03:00:00Z")) {
		t.Errorf("Next after a matching instant is %s, %v", next, ok)
	}
	prev, ok := s.Prev(at(t, "2026-08-25T03:00:00Z"))
	if !ok || !prev.Equal(at(t, "2026-08-25T03:00:00Z")) {
		t.Errorf("Prev at a matching instant is %s, %v", prev, ok)
	}
	prev, ok = s.Prev(at(t, "2026-08-25T02:59:59Z"))
	if !ok || !prev.Equal(at(t, "2026-08-24T03:00:00Z")) {
		t.Errorf("Prev before the day's instant is %s, %v", prev, ok)
	}
}

// The seconds an instant is asked about are not part of the schedule: a
// schedule has minute resolution, and Prev of 03:00:59 is 03:00:00.
func TestSecondsAreNotPartOfTheSchedule(t *testing.T) {
	s, _ := Parse("0 3 * * *")
	prev, ok := s.Prev(at(t, "2026-08-25T03:00:59Z"))
	if !ok || !prev.Equal(at(t, "2026-08-25T03:00:00Z")) {
		t.Errorf("Prev inside the matching minute is %s, %v", prev, ok)
	}
	next, ok := s.Next(at(t, "2026-08-25T03:00:59Z"))
	if !ok || !next.Equal(at(t, "2026-08-26T03:00:00Z")) {
		t.Errorf("Next inside the matching minute is %s, %v", next, ok)
	}
}

// A schedule is read in UTC whatever the host's own zone is, which is what
// makes the arithmetic here free of a DST hour that repeats or never happens.
func TestTheArithmeticIsUTCWhateverTheZone(t *testing.T) {
	s, _ := Parse("30 2 * * *")
	// 2026-03-08 is when US DST skips 02:00-03:00 local. The instant asked
	// about is read as the moment it is, and the answer is the next 02:30
	// UTC after it — 02:30 exists exactly once, on every day of the year.
	zone := time.FixedZone("nowhere", -5*3600)
	next, ok := s.Next(time.Date(2026, 3, 7, 20, 0, 0, 0, zone))
	if !ok {
		t.Fatal("no next instant")
	}
	if !next.Equal(at(t, "2026-03-08T02:30:00Z")) {
		t.Errorf("Next is %s, want 2026-03-08T02:30:00Z", next.UTC())
	}
	if next.Location() != time.UTC {
		t.Errorf("Next answered in %s, not UTC", next.Location())
	}
}

// A schedule that names a day that month never has has no next instant, and
// says so rather than looping forever.
func TestAnImpossibleScheduleHasNoInstant(t *testing.T) {
	s, err := Parse("0 0 30 feb *")
	if err != nil {
		t.Fatalf("30 february is well formed and was refused: %v", err)
	}
	if next, ok := s.Next(at(t, "2026-01-01T00:00:00Z")); ok {
		t.Errorf("30 february has a next instant at %s", next)
	}
	if prev, ok := s.Prev(at(t, "2026-01-01T00:00:00Z")); ok {
		t.Errorf("30 february has a previous instant at %s", prev)
	}
}

// The month end is walked, not assumed: the last day of a month differs and a
// leap February exists.
func TestTheMonthEndIsWalked(t *testing.T) {
	s, _ := Parse("0 0 29 * *")
	next, ok := s.Next(at(t, "2026-01-30T00:00:00Z"))
	if !ok || !next.Equal(at(t, "2026-03-29T00:00:00Z")) {
		t.Errorf("the 29th after 2026-01-30 is %s, %v; 2026 february has no 29th", next, ok)
	}
	next, ok = s.Next(at(t, "2028-01-30T00:00:00Z"))
	if !ok || !next.Equal(at(t, "2028-02-29T00:00:00Z")) {
		t.Errorf("the 29th after 2028-01-30 is %s, %v; 2028 is a leap year", next, ok)
	}
}

// The parse failure names the field, because "invalid" on a five-field line
// leaves an operator counting columns.
func TestTheRefusalNamesTheField(t *testing.T) {
	_, err := Parse("* * * * 9")
	if err == nil {
		t.Fatal("9 is no day of the week and parsed")
	}
	if !strings.Contains(err.Error(), "day of week") {
		t.Errorf("the refusal does not name the field: %v", err)
	}
}
