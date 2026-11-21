package pim

import (
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-vcard"
)

func mustCal(t *testing.T, s string) *ical.Calendar {
	t.Helper()
	cal, err := ParseCalendar([]byte(strings.ReplaceAll(s, "\n", "\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	return cal
}

func starts(in []Instance) []string {
	var out []string
	for _, i := range in {
		out = append(out, i.Start.String())
	}
	return out
}

func TestExpandRecurrence(t *testing.T) {
	cal := mustCal(t, `BEGIN:VCALENDAR
VERSION:2.0
PRODID:x
BEGIN:VEVENT
UID:r1
DTSTAMP:20260101T000000Z
DTSTART;TZID=Europe/Berlin:20261026T100000
DTEND;TZID=Europe/Berlin:20261026T110000
RRULE:FREQ=WEEKLY;COUNT=4
EXDATE;TZID=Europe/Berlin:20261102T100000
SUMMARY:Jour fixe
END:VEVENT
BEGIN:VEVENT
UID:r1
DTSTAMP:20260101T000000Z
RECURRENCE-ID;TZID=Europe/Berlin:20261109T100000
DTSTART;TZID=Europe/Berlin:20261109T150000
DTEND;TZID=Europe/Berlin:20261109T160000
SUMMARY:Jour fixe (verschoben)
END:VEVENT
END:VCALENDAR
`)
	got := starts(Expand(cal, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)))
	// 26.10. (CET after the DST switch), 02.11. excluded, 09.11. moved to 15:00, 16.11.
	want := []string{"2026-10-26T09:00:00Z", "2026-11-09T14:00:00Z", "2026-11-16T09:00:00Z"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", got, want)
	}
	m, err := Validate(cal)
	if err != nil || m.Component != "VEVENT" || m.UID != "r1" || m.Start == nil || m.End == nil {
		t.Fatalf("meta = %+v, %v", m, err)
	}
	if time.Unix(*m.End, 0).UTC().Format(time.RFC3339) != "2026-11-16T10:00:00Z" {
		t.Fatalf("end of series = %v", time.Unix(*m.End, 0).UTC())
	}
}

func TestExpandAllDayAndOpenEnded(t *testing.T) {
	cal := mustCal(t, `BEGIN:VCALENDAR
VERSION:2.0
PRODID:x
BEGIN:VEVENT
UID:b1
DTSTAMP:20260101T000000Z
DTSTART;VALUE=DATE:19900517
RRULE:FREQ=YEARLY
SUMMARY:Geburtstag
END:VEVENT
END:VCALENDAR
`)
	in := Expand(cal, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC))
	if len(in) != 1 || in[0].Start.String() != "2026-05-17" || in[0].End.String() != "2026-05-18" || !in[0].Start.Date {
		t.Fatalf("instances = %+v", starts(in))
	}
	m, _ := Validate(cal)
	if m.End != nil {
		t.Fatal("an endless series must have an open end")
	}
	if m := mustCal(t, "BEGIN:VCALENDAR\nVERSION:2.0\nPRODID:x\nBEGIN:VEVENT\nUID:a\nDTSTAMP:20260101T000000Z\nDTSTART:20260101T100000Z\nEND:VEVENT\nBEGIN:VTODO\nUID:a\nEND:VTODO\nEND:VCALENDAR\n"); m != nil {
		if _, err := Validate(m); err == nil {
			t.Fatal("mixed component types must be rejected")
		}
	}
}

func TestEventInputAndInstanceShift(t *testing.T) {
	cal, err := NewEvent("e1", EventInput{Title: "Sport", Start: "2026-11-02T18:00", End: strPtr("2026-11-02T19:30"), RRule: strPtr("FREQ=WEEKLY;COUNT=3")})
	if err != nil {
		t.Fatal(err)
	}
	data, err := EncodeCalendar(cal)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"DTSTART:20261102T180000", "DTEND:20261102T193000", "RRULE:FREQ=WEEKLY;COUNT=3", "SUMMARY:Sport"} {
		if !strings.Contains(string(data), want+"\r\n") {
			t.Fatalf("missing %q in\n%s", want, data)
		}
	}
	// The browser edits the second occurrence (floating 18:00 shown as local
	// time) and moves it by 30 minutes; both values arrive as UTC instants.
	in := EventInput{Title: "Sport", Start: "2026-11-09T17:30:00Z", End: strPtr("2026-11-09T19:00:00Z"), Instance: strPtr("2026-11-09T17:00:00Z")}
	if err := ApplyEvent(Master(cal), in, false); err != nil {
		t.Fatal(err)
	}
	got := starts(Expand(cal, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)))
	want := []string{"2026-11-02T18:30:00", "2026-11-09T18:30:00", "2026-11-16T18:30:00"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", got, want)
	}
	// Removing the recurrence.
	in = EventInput{Title: "Sport", Start: "2026-11-02T18:30", RRule: strPtr("")}
	if err := ApplyEvent(Master(cal), in, false); err != nil {
		t.Fatal(err)
	}
	if Master(cal).Props.Get(ical.PropRecurrenceRule) != nil {
		t.Fatal("RRULE must be removed")
	}
	// Events from phones keep their time zone when edited in the browser.
	tz := mustCal(t, "BEGIN:VCALENDAR\nVERSION:2.0\nPRODID:x\nBEGIN:VEVENT\nUID:z\nDTSTAMP:20260101T000000Z\nDTSTART;TZID=Europe/Berlin:20261201T100000\nSUMMARY:x\nEND:VEVENT\nEND:VCALENDAR\n")
	if err := ApplyEvent(Master(tz), EventInput{Title: "x", Start: "2026-12-01T10:00:00Z"}, false); err != nil {
		t.Fatal(err)
	}
	p := Master(tz).Props.Get(ical.PropDateTimeStart)
	if p.Params.Get(ical.PropTimezoneID) != "Europe/Berlin" || p.Value != "20261201T110000" {
		t.Fatalf("DTSTART = %+v", p)
	}
}

func TestRRuleKeptUnchanged(t *testing.T) {
	const rule = "FREQ=MONTHLY;BYDAY=2TU;UNTIL=20271231T000000Z"
	cal, err := NewEvent("e2", EventInput{Title: "Stammtisch", Start: "2026-11-10T19:00:00Z", RRule: strPtr(rule)})
	if err != nil {
		t.Fatal(err)
	}
	ev := EventFromInstance(Master(cal), mustSingle(t, cal))
	if ev.RRule == nil || *ev.RRule != rule {
		t.Fatalf("rrule = %v", ev.RRule)
	}
	if err := ApplyEvent(Master(cal), EventInput{Title: "Stammtisch!", Start: "2026-11-10T19:00:00Z"}, false); err != nil {
		t.Fatal(err)
	}
	if v := Master(cal).Props.Get(ical.PropRecurrenceRule).Value; v != rule {
		t.Fatalf("rrule changed to %q", v)
	}
}

func mustSingle(t *testing.T, cal *ical.Calendar) Instance {
	t.Helper()
	in, ok := Single(cal)
	if !ok {
		t.Fatal("no instance")
	}
	return in
}

func TestTasks(t *testing.T) {
	title := "Steuer"
	cal, err := NewTask("t1", TaskInput{Title: &title, Due: strPtr("2026-12-31")})
	if err != nil {
		t.Fatal(err)
	}
	task := TaskFrom(cal)
	if task.Title != "Steuer" || task.Due == nil || *task.Due != "2026-12-31" || task.Done {
		t.Fatalf("task = %+v", task)
	}
	done := true
	if err := ApplyTask(Master(cal), TaskInput{Done: &done}); err != nil {
		t.Fatal(err)
	}
	if !TaskFrom(cal).Done || Master(cal).Props.Get(ical.PropCompleted) == nil {
		t.Fatal("task must be completed")
	}
}

func TestContactRoundTrip(t *testing.T) {
	bday := "1985-03-14"
	card, err := NewCard("u1", ContactInput{FirstName: "Erika", LastName: "Mustermann", Organization: "Muster GmbH", Title: "CTO",
		Emails: []Entry{{Type: "work", Value: "erika@muster.de"}, {Type: "home", Value: "erika@example.org"}},
		Phones: []Entry{{Type: "cell", Value: "+49 170 1"}}, Notes: "Zeile 1\nZeile 2, mit Komma", Birthday: &bday})
	if err != nil {
		t.Fatal(err)
	}
	card.SetValue(vcard.FieldPhoto, "data:image/png;base64,AAAA") // set by a phone
	data, err := EncodeCard(card)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseCard(data)
	if err != nil {
		t.Fatal(err)
	}
	c := ContactFrom(parsed)
	if c.Name != "Erika Mustermann" || c.FirstName != "Erika" || c.LastName != "Mustermann" || c.Organization != "Muster GmbH" ||
		len(c.Emails) != 2 || c.Emails[0].Type != "work" || c.Emails[1].Type != "home" || len(c.Phones) != 1 || c.Phones[0].Type != "cell" ||
		c.Notes != "Zeile 1\nZeile 2, mit Komma" || c.Birthday == nil || *c.Birthday != bday {
		t.Fatalf("contact = %+v", c)
	}
	// Editing in the web interface keeps fields it does not know.
	in := ContactInput{Name: c.Name, FirstName: c.FirstName, LastName: c.LastName, Emails: c.Emails[:1], Phones: c.Phones, Organization: c.Organization}
	if err := ApplyContact(parsed, in); err != nil {
		t.Fatal(err)
	}
	if parsed.Value(vcard.FieldPhoto) == "" || len(parsed[vcard.FieldEmail]) != 1 || parsed.Value(vcard.FieldBirthday) != "" {
		t.Fatalf("after edit: %+v", parsed)
	}
	info := CardInfo(parsed)
	if info.UID != "u1" || len(info.Emails) != 1 || !strings.Contains(info.Search, "muster gmbh") {
		t.Fatalf("info = %+v", info)
	}
	if err := ApplyContact(parsed, ContactInput{}); err == nil {
		t.Fatal("empty contact must be rejected")
	}
}

func TestSplitAndMerge(t *testing.T) {
	cal := mustCal(t, `BEGIN:VCALENDAR
VERSION:2.0
PRODID:x
BEGIN:VTIMEZONE
TZID:Europe/Berlin
BEGIN:STANDARD
DTSTART:19701025T030000
TZOFFSETFROM:+0200
TZOFFSETTO:+0100
END:STANDARD
END:VTIMEZONE
BEGIN:VEVENT
UID:a
DTSTAMP:20260101T000000Z
DTSTART;TZID=Europe/Berlin:20261201T100000
SUMMARY:A
END:VEVENT
BEGIN:VEVENT
DTSTART:20261202T100000Z
SUMMARY:B ohne UID
END:VEVENT
END:VCALENDAR
`)
	parts := Split(cal, func() string { return "generated" })
	if len(parts) != 2 {
		t.Fatalf("parts = %d", len(parts))
	}
	if len(parts[0].Children) != 2 || parts[0].Children[0].Name != ical.CompTimezone {
		t.Fatal("first part must carry its time zone")
	}
	if len(parts[1].Children) != 1 {
		t.Fatal("second part needs no time zone")
	}
	if m, err := Validate(parts[1]); err != nil || m.UID != "generated" {
		t.Fatalf("generated uid: %+v %v", m, err)
	}
	merged, err := Merge("Kalender", [][]byte{EncodeLenient(parts[0]), EncodeLenient(parts[1])})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(merged), "BEGIN:VEVENT") != 2 || strings.Count(string(merged), "BEGIN:VTIMEZONE") != 1 {
		t.Fatalf("merged:\n%s", merged)
	}
}

func TestParseWhen(t *testing.T) {
	for in, want := range map[string]string{
		"2026-11-21":                "2026-11-21",
		"2026-11-21T10:00":          "2026-11-21T10:00:00",
		"2026-11-21T08:00:00.000Z":  "2026-11-21T08:00:00Z",
		"2026-11-21T10:00:00+02:00": "2026-11-21T08:00:00Z",
	} {
		w, err := ParseWhen(in, false)
		if err != nil || w.String() != want {
			t.Errorf("%s -> %q, %v (want %q)", in, w.String(), err, want)
		}
	}
	if _, err := ParseWhen("morgen", false); err == nil {
		t.Error("garbage must fail")
	}
}

func strPtr(s string) *string { return &s }
