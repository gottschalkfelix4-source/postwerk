package export

import (
	"fmt"
	"strings"
	"time"

	"postwerk/internal/store"
)

// ICalendar renders events (VEVENT) and tasks (VTODO) as an iCalendar
// document (RFC 5545).
func ICalendar(owner string, events []store.CalendarEvent, tasks []store.Task, now time.Time) string {
	var b strings.Builder
	line := func(s string) { b.WriteString(fold(s) + "\r\n") }
	stamp := now.UTC().Format("20060102T150405Z")
	line("BEGIN:VCALENDAR")
	line("VERSION:2.0")
	line("PRODID:-//Postwerk//Datenexport//DE")
	line("CALSCALE:GREGORIAN")
	for _, e := range events {
		line("BEGIN:VEVENT")
		line(fmt.Sprintf("UID:event-%d-%s", e.ID, owner))
		line("DTSTAMP:" + stamp)
		start, allDay := icalTime(e.Start)
		if allDay {
			line("DTSTART;VALUE=DATE:" + start)
		} else {
			line("DTSTART:" + start)
		}
		if e.End != nil && *e.End != "" {
			if end, endAllDay := icalTime(*e.End); endAllDay {
				line("DTEND;VALUE=DATE:" + end)
			} else {
				line("DTEND:" + end)
			}
		}
		line("SUMMARY:" + escapeText(e.Title))
		if e.Location != nil && *e.Location != "" {
			line("LOCATION:" + escapeText(*e.Location))
		}
		if e.Description != nil && *e.Description != "" {
			line("DESCRIPTION:" + escapeText(*e.Description))
		}
		line("END:VEVENT")
	}
	for _, t := range tasks {
		line("BEGIN:VTODO")
		line(fmt.Sprintf("UID:task-%d-%s", t.ID, owner))
		line("DTSTAMP:" + stamp)
		line("CREATED:" + t.CreatedAt.UTC().Format("20060102T150405Z"))
		line("SUMMARY:" + escapeText(t.Title))
		if t.Due != nil && *t.Due != "" {
			if due, allDay := icalTime(*t.Due); allDay {
				line("DUE;VALUE=DATE:" + due)
			} else {
				line("DUE:" + due)
			}
		}
		if t.Done {
			line("STATUS:COMPLETED")
		} else {
			line("STATUS:NEEDS-ACTION")
		}
		line("END:VTODO")
	}
	line("END:VCALENDAR")
	return b.String()
}

// icalTime converts the stored date strings (RFC 3339, local date-time or
// date only) to iCalendar values. Times with a zone become UTC, times
// without one stay floating.
func icalTime(s string) (string, bool) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04Z07:00"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Format("20060102T150405Z"), false
		}
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02 15:04"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format("20060102T150405"), false
		}
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.Format("20060102"), true
	}
	// Unknown format: keep the digits so nothing is lost.
	return strings.NewReplacer("-", "", ":", "", " ", "T").Replace(s), false
}

func escapeText(s string) string {
	return strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`, "\r\n", `\n`, "\n", `\n`).Replace(s)
}

// fold splits content lines longer than 75 octets (RFC 5545 3.1) without
// breaking UTF-8 sequences.
func fold(s string) string {
	if len(s) <= 75 {
		return s
	}
	var b strings.Builder
	limit := 75
	for len(s) > limit {
		cut := limit
		for cut > 0 && (s[cut]&0xC0) == 0x80 {
			cut--
		}
		b.WriteString(s[:cut] + "\r\n ")
		s = s[cut:]
		limit = 74 // continuation lines start with a space
	}
	b.WriteString(s)
	return b.String()
}
