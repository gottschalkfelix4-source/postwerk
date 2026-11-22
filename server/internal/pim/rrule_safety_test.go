package pim

import (
	"errors"
	"testing"
	"time"
)

func ruleCal(t *testing.T, start, rule string) string {
	t.Helper()
	return `BEGIN:VCALENDAR
VERSION:2.0
PRODID:x
BEGIN:VEVENT
UID:dos
DTSTAMP:20260101T000000Z
DTSTART:` + start + `
DTEND:` + start + `
RRULE:` + rule + `
END:VEVENT
END:VCALENDAR
`
}

// withDeadline fails the test instead of hanging when f does not return.
func withDeadline(t *testing.T, d time.Duration, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { f(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatal("did not return in time")
	}
}

func TestUnexpandableRulesRejected(t *testing.T) {
	for _, rule := range []string{
		"FREQ=SECONDLY;INTERVAL=60;BYSECOND=1;COUNT=2",
		"FREQ=SECONDLY;INTERVAL=60;BYSECOND=1",
		"FREQ=MINUTELY;INTERVAL=60;BYMINUTE=1",
		"FREQ=HOURLY;INTERVAL=2;BYHOUR=1;COUNT=2", // start hour 0: only even hours reachable
		"FREQ=HOURLY;INTERVAL=24;BYHOUR=5",
	} {
		cal := mustCal(t, ruleCal(t, "20260101T000000Z", rule))
		withDeadline(t, 5*time.Second, func() {
			if _, err := Validate(cal); !errors.Is(err, ErrInvalid) {
				t.Errorf("%s: err = %v, want ErrInvalid", rule, err)
			}
			Expand(cal, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC))
		})
	}
	if _, err := validRRule("FREQ=MINUTELY;INTERVAL=5"); err == nil {
		t.Error("validRRule accepted a minutely rule")
	}
}

func TestReachableHourlyRuleAccepted(t *testing.T) {
	cal := mustCal(t, ruleCal(t, "20260101T000000Z", "FREQ=HOURLY;INTERVAL=2;BYHOUR=4,5;COUNT=3"))
	if _, err := Validate(cal); err != nil {
		t.Fatal(err)
	}
	got := starts(Expand(cal, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)))
	if len(got) != 3 || got[0] != "2026-01-01T04:00:00Z" || got[1] != "2026-01-02T04:00:00Z" {
		t.Fatalf("got %v", got)
	}
}

// A dense endless rule that started long ago must not walk (and collect)
// every occurrence up to the requested window.
func TestExpandDenseRuleBounded(t *testing.T) {
	cal := mustCal(t, ruleCal(t, "19000101T000000Z", "FREQ=HOURLY"))
	withDeadline(t, 10*time.Second, func() {
		Expand(cal, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC))
	})
	cal = mustCal(t, ruleCal(t, "20260101T000000Z", "FREQ=HOURLY"))
	withDeadline(t, 10*time.Second, func() {
		if n := len(Expand(cal, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC))); n != 1000 {
			t.Errorf("instances = %d, want cap 1000", n)
		}
	})
}

// Impossible date filters run the library up to its year limit; that has to
// stay fast enough for a request.
func TestImpossibleDateRuleFast(t *testing.T) {
	cal := mustCal(t, ruleCal(t, "20260101T000000Z", "FREQ=DAILY;BYMONTH=2;BYMONTHDAY=30;COUNT=2"))
	withDeadline(t, 5*time.Second, func() {
		Validate(cal)
		Expand(cal, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC))
	})
}
