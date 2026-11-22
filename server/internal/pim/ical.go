// Package pim converts calendar objects (iCalendar, RFC 5545) and contacts
// (vCard, RFC 6350) between their wire formats and the JSON shapes used by
// the web interface.
package pim

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	_ "time/tzdata" // TZID resolution must not depend on the host's zoneinfo

	"github.com/emersion/go-ical"
	"github.com/teambition/rrule-go"
)

const ProdID = "-//Postwerk//Postwerk Mail Server//DE"

// ErrInvalid marks data that is not a valid calendar object resource.
var ErrInvalid = errors.New("invalid calendar data")

// ParseCalendar decodes a single VCALENDAR object.
func ParseCalendar(data []byte) (*ical.Calendar, error) {
	cal, err := ical.NewDecoder(bytes.NewReader(data)).Decode()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return cal, nil
}

// EncodeCalendar serializes a calendar.
func EncodeCalendar(cal *ical.Calendar) ([]byte, error) {
	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(cal); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ---- time values ----

// When is a DTSTART/DTEND/DUE value: a date, a floating local time or an
// absolute instant.
type When struct {
	T        time.Time // floating values and dates are stored as UTC wall clock
	Date     bool
	Floating bool
	TZID     string // original TZID when it could be resolved
}

func (w When) IsZero() bool { return w.T.IsZero() }

// Unix returns the instant used for range filtering (floating times are
// treated as UTC).
func (w When) Unix() int64 { return w.T.Unix() }

// String renders the value for the JSON API: "2006-01-02" for dates,
// "2006-01-02T15:04:05" for floating times and RFC 3339 UTC otherwise.
func (w When) String() string {
	switch {
	case w.T.IsZero():
		return ""
	case w.Date:
		return w.T.Format("2006-01-02")
	case w.Floating:
		return w.T.Format("2006-01-02T15:04:05")
	}
	return w.T.UTC().Format(time.RFC3339)
}

// Add shifts the value by d, keeping its kind.
func (w When) Add(d time.Duration) When {
	w.T = w.T.Add(d)
	return w
}

// PropWhen parses a date or date-time property. Unknown TZIDs (for example
// Windows zone names) fall back to floating time instead of failing.
func PropWhen(p *ical.Prop) (When, error) {
	if p == nil {
		return When{}, errors.New("missing property")
	}
	v := strings.TrimSpace(p.Value)
	if p.ValueType() == ical.ValueDate || len(v) == 8 {
		t, err := time.ParseInLocation("20060102", v, time.UTC)
		if err != nil {
			return When{}, err
		}
		return When{T: t, Date: true}, nil
	}
	if strings.HasSuffix(v, "Z") {
		t, err := time.ParseInLocation("20060102T150405Z", v, time.UTC)
		if err != nil {
			return When{}, err
		}
		return When{T: t}, nil
	}
	if tzid := p.Params.Get(ical.PropTimezoneID); tzid != "" {
		if loc, err := loadLocation(tzid); err == nil {
			t, err := time.ParseInLocation("20060102T150405", v, loc)
			if err != nil {
				return When{}, err
			}
			return When{T: t, TZID: loc.String()}, nil
		}
	}
	t, err := time.ParseInLocation("20060102T150405", v, time.UTC)
	if err != nil {
		return When{}, err
	}
	return When{T: t, Floating: true}, nil
}

// windowsZones maps the most common Windows zone names (sent by Outlook)
// to IANA names.
var windowsZones = map[string]string{
	"W. Europe Standard Time":        "Europe/Berlin",
	"Central Europe Standard Time":   "Europe/Budapest",
	"Romance Standard Time":          "Europe/Paris",
	"Central European Standard Time": "Europe/Warsaw",
	"GMT Standard Time":              "Europe/London",
	"UTC":                            "UTC",
	"Eastern Standard Time":          "America/New_York",
	"Pacific Standard Time":          "America/Los_Angeles",
}

func loadLocation(tzid string) (*time.Location, error) {
	tzid = strings.Trim(tzid, `"`)
	if n, ok := windowsZones[tzid]; ok {
		tzid = n
	}
	// Some clients prefix the TZID with a vendor path ("/mozilla.org/.../Europe/Berlin").
	loc, err := time.LoadLocation(tzid)
	if err != nil && strings.Contains(tzid, "/") {
		parts := strings.Split(strings.Trim(tzid, "/"), "/")
		for i := 0; i < len(parts)-1 && err != nil; i++ {
			loc, err = time.LoadLocation(strings.Join(parts[i:], "/"))
		}
	}
	return loc, err
}

// SetWhen stores w in props under name.
func SetWhen(props ical.Props, name string, w When) {
	p := ical.NewProp(name)
	switch {
	case w.Date:
		p.SetValueType(ical.ValueDate)
		p.Value = w.T.Format("20060102")
	case w.Floating:
		p.Value = w.T.Format("20060102T150405")
	case w.TZID != "":
		loc, err := loadLocation(w.TZID)
		if err != nil {
			p.Value = w.T.UTC().Format("20060102T150405Z")
			break
		}
		p.Params.Set(ical.PropTimezoneID, loc.String())
		p.Value = w.T.In(loc).Format("20060102T150405")
	default:
		p.Value = w.T.UTC().Format("20060102T150405Z")
	}
	props.Set(p)
}

// ParseWhen parses an API time value: "2006-01-02" (date), RFC 3339 with
// offset (instant) or "2006-01-02T15:04[:05]" (floating local time).
func ParseWhen(s string, allDay bool) (When, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return When{}, errors.New("empty time")
	}
	if t, err := time.ParseInLocation("2006-01-02", s, time.UTC); err == nil {
		return When{T: t, Date: true}, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		if allDay {
			return When{T: time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), Date: true}, nil
		}
		return When{T: t.UTC()}, nil
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			if allDay {
				return When{T: time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), Date: true}, nil
			}
			return When{T: t, Floating: true}, nil
		}
	}
	return When{}, fmt.Errorf("invalid time %q", s)
}

// ---- object structure ----

// Meta is the information extracted from a calendar object resource for
// indexing.
type Meta struct {
	Component string
	UID       string
	Summary   string
	Start     *int64 // first instance start (unix)
	End       *int64 // last instance end (unix); nil with Start set: open-ended
}

// Validate checks the constraints of RFC 4791 section 4.1 and returns the
// object's index data.
func Validate(cal *ical.Calendar) (Meta, error) {
	var m Meta
	if cal.Props.Get(ical.PropMethod) != nil {
		return m, fmt.Errorf("%w: METHOD is not allowed in stored objects", ErrInvalid)
	}
	for _, c := range cal.Children {
		if c.Name == ical.CompTimezone {
			continue
		}
		if m.Component == "" {
			m.Component = c.Name
		} else if c.Name != m.Component {
			return m, fmt.Errorf("%w: mixed component types", ErrInvalid)
		}
		uid, _ := c.Props.Text(ical.PropUID)
		if m.UID == "" {
			m.UID = uid
		} else if uid != "" && uid != m.UID {
			return m, fmt.Errorf("%w: several UIDs in one object", ErrInvalid)
		}
	}
	if m.Component == "" {
		return m, fmt.Errorf("%w: no calendar component", ErrInvalid)
	}
	if m.UID == "" {
		return m, fmt.Errorf("%w: UID missing", ErrInvalid)
	}
	master := Master(cal)
	m.Summary, _ = master.Props.Text(ical.PropSummary)
	if start, _, ok := startEnd(master); ok {
		if _, err := recurrence(master, start); err != nil {
			return m, fmt.Errorf("%w: RRULE: %v", ErrInvalid, err)
		}
	}
	m.Start, m.End = timeRange(cal, master)
	return m, nil
}

// Master returns the main component (the one without RECURRENCE-ID).
func Master(cal *ical.Calendar) *ical.Component {
	var first *ical.Component
	for _, c := range cal.Children {
		if c.Name == ical.CompTimezone {
			continue
		}
		if first == nil {
			first = c
		}
		if c.Props.Get(ical.PropRecurrenceID) == nil {
			return c
		}
	}
	return first
}

// overrides returns the components that replace single occurrences.
func overrides(cal *ical.Calendar, master *ical.Component) []*ical.Component {
	var out []*ical.Component
	for _, c := range cal.Children {
		if c != master && c.Name == master.Name && c.Props.Get(ical.PropRecurrenceID) != nil {
			out = append(out, c)
		}
	}
	return out
}

// startEnd returns the start and exclusive end of a component. VTODOs use
// DTSTART/DUE; a missing end means a point in time (or one day for dates).
func startEnd(c *ical.Component) (When, When, bool) {
	sp := c.Props.Get(ical.PropDateTimeStart)
	if c.Name == ical.CompToDo && sp == nil {
		sp = c.Props.Get(ical.PropDue)
	}
	if sp == nil {
		return When{}, When{}, false
	}
	start, err := PropWhen(sp)
	if err != nil {
		return When{}, When{}, false
	}
	endName := ical.PropDateTimeEnd
	if c.Name == ical.CompToDo {
		endName = ical.PropDue
	}
	if ep := c.Props.Get(endName); ep != nil {
		if end, err := PropWhen(ep); err == nil && !end.T.Before(start.T) {
			return start, end, true
		}
	}
	if dp := c.Props.Get(ical.PropDuration); dp != nil {
		if d, err := dp.Duration(); err == nil && d >= 0 {
			return start, start.Add(d), true
		}
	}
	if start.Date {
		return start, start.Add(24 * time.Hour), true
	}
	return start, start, true
}

const maxInstances = 5000

// maxSteps bounds how many occurrences are walked to reach a time window, so
// a dense rule far in the past cannot keep a request busy.
const maxSteps = 200000

// safeRule rejects rules the recurrence library cannot expand in bounded
// time. Its sub-daily loops spin forever when no BYHOUR/BYMINUTE/BYSECOND
// value is reachable with the given INTERVAL, so second- and minute-based
// rules are refused and hourly rules must be able to hit one of their hours.
func safeRule(opt *rrule.ROption) error {
	switch opt.Freq {
	case rrule.SECONDLY, rrule.MINUTELY:
		return fmt.Errorf("%w: FREQ=SECONDLY/MINUTELY is not supported", ErrInvalid)
	case rrule.HOURLY:
		if len(opt.Byhour) == 0 {
			return nil
		}
		iv := opt.Interval
		if iv < 1 {
			iv = 1
		}
		g := gcd(iv, 24)
		h0 := opt.Dtstart.Hour()
		for _, h := range opt.Byhour {
			if ((h-h0)%g+g)%g == 0 {
				return nil
			}
		}
		return fmt.Errorf("%w: BYHOUR is unreachable with this INTERVAL", ErrInvalid)
	}
	return nil
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// recurrence builds the rule set of a component, or nil if it does not recur.
func recurrence(c *ical.Component, start When) (*rrule.Set, error) {
	rp := c.Props.Get(ical.PropRecurrenceRule)
	if rp == nil && len(c.Props.Values(ical.PropRecurrenceDates)) == 0 {
		return nil, nil
	}
	set := &rrule.Set{}
	if rp != nil {
		opt, err := rrule.StrToROption(rp.Value)
		if err != nil {
			return nil, err
		}
		opt.Dtstart = start.T
		if err := safeRule(opt); err != nil {
			return nil, err
		}
		r, err := rrule.NewRRule(*opt)
		if err != nil {
			return nil, err
		}
		set.RRule(r)
	}
	set.DTStart(start.T)
	for _, p := range c.Props.Values(ical.PropRecurrenceDates) {
		for _, v := range strings.Split(p.Value, ",") {
			pp := p
			pp.Value = v
			if w, err := PropWhen(&pp); err == nil {
				set.RDate(sameBase(w, start))
			}
		}
	}
	for _, p := range c.Props.Values(ical.PropExceptionDates) {
		for _, v := range strings.Split(p.Value, ",") {
			pp := p
			pp.Value = v
			if w, err := PropWhen(&pp); err == nil {
				set.ExDate(sameBase(w, start))
			}
		}
	}
	return set, nil
}

// sameBase converts w into the time base of ref, so EXDATE/RECURRENCE-ID
// values compare equal to generated instances.
func sameBase(w, ref When) time.Time {
	switch {
	case ref.Date:
		return time.Date(w.T.Year(), w.T.Month(), w.T.Day(), ref.T.Hour(), ref.T.Minute(), ref.T.Second(), 0, ref.T.Location())
	case ref.Floating && !w.Floating && !w.Date:
		return w.T // best effort: absolute exceptions on floating series
	}
	return w.T.In(ref.T.Location())
}

func timeRange(cal *ical.Calendar, master *ical.Component) (*int64, *int64) {
	start, end, ok := startEnd(master)
	if !ok {
		return nil, nil
	}
	s, e := start.Unix(), end.Unix()
	if rp := master.Props.Get(ical.PropRecurrenceRule); rp != nil {
		if r := strings.ToUpper(rp.Value); !strings.Contains(r, "COUNT=") && !strings.Contains(r, "UNTIL=") {
			return &s, nil // endless series
		}
	}
	set, err := recurrence(master, start)
	if err == nil && set != nil {
		dur := end.T.Sub(start.T)
		last := start.T
		n := 0
		it := set.Iterator()
		for {
			t, more := it()
			if !more {
				break
			}
			if n++; n > maxInstances {
				return &s, nil // open-ended (or practically so)
			}
			last = t
		}
		e = last.Add(dur).Unix()
	}
	for _, o := range overrides(cal, master) {
		if os, oe, ok := startEnd(o); ok {
			if os.Unix() < s {
				s = os.Unix()
			}
			if oe.Unix() > e {
				e = oe.Unix()
			}
		}
	}
	return &s, &e
}

// Instance is one occurrence of an event.
type Instance struct {
	Comp      *ical.Component
	Start     When
	End       When
	HasEnd    bool
	Recurring bool
}

// Expand returns the occurrences of the calendar's master component that
// overlap [from, to). Floating times are compared as UTC.
func Expand(cal *ical.Calendar, from, to time.Time) []Instance {
	master := Master(cal)
	if master == nil {
		return nil
	}
	start, end, ok := startEnd(master)
	if !ok {
		return nil
	}
	hasEnd := master.Props.Get(ical.PropDateTimeEnd) != nil || master.Props.Get(ical.PropDuration) != nil || start.Date
	overlaps := func(s, e When) bool {
		if e.T.Equal(s.T) {
			return !s.T.Before(from) && s.T.Before(to)
		}
		return s.T.Before(to) && e.T.After(from)
	}
	set, err := recurrence(master, start)
	if err != nil || set == nil {
		if overlaps(start, end) {
			return []Instance{{Comp: master, Start: start, End: end, HasEnd: hasEnd}}
		}
		return nil
	}
	ovs := overrides(cal, master)
	replaced := map[int64]bool{}
	for _, o := range ovs {
		if w, err := PropWhen(o.Props.Get(ical.PropRecurrenceID)); err == nil {
			replaced[sameBase(w, start).Unix()] = true
		}
	}
	dur := end.T.Sub(start.T)
	var out []Instance
	lo := from.Add(-dur - time.Second)
	it := set.Iterator()
	for steps := 0; steps < maxSteps; steps++ {
		t, more := it()
		if !more || !t.Before(to) {
			break
		}
		if t.Before(lo) || replaced[t.Unix()] {
			continue
		}
		s := start
		s.T = t
		e := s.Add(dur)
		if !overlaps(s, e) {
			continue
		}
		out = append(out, Instance{Comp: master, Start: s, End: e, HasEnd: hasEnd, Recurring: true})
		if len(out) >= 1000 {
			break
		}
	}
	for _, o := range ovs {
		if s, e, ok := startEnd(o); ok && overlaps(s, e) {
			oh := o.Props.Get(ical.PropDateTimeEnd) != nil || o.Props.Get(ical.PropDuration) != nil || s.Date
			out = append(out, Instance{Comp: o, Start: s, End: e, HasEnd: oh, Recurring: true})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start.T.Before(out[j].Start.T) })
	return out
}

// ---- events ----

// Event is the JSON shape of one event occurrence.
type Event struct {
	ID              int64   `json:"id"`
	CalendarID      int64   `json:"calendarId"`
	UID             string  `json:"uid"`
	Title           string  `json:"title"`
	Start           string  `json:"start"`
	End             *string `json:"end"`
	AllDay          bool    `json:"allDay"`
	Location        *string `json:"location"`
	Description     *string `json:"description"`
	RRule           *string `json:"rrule"`
	Instance        *string `json:"instance"`
	SourceMessageID *int64  `json:"sourceMessageId"`
}

// EventInput is the JSON body for creating or updating an event.
type EventInput struct {
	CalendarID      *int64  `json:"calendarId"`
	Title           string  `json:"title"`
	Start           string  `json:"start"`
	End             *string `json:"end"`
	AllDay          bool    `json:"allDay"`
	Location        *string `json:"location"`
	Description     *string `json:"description"`
	RRule           *string `json:"rrule"`
	Instance        *string `json:"instance"`
	SourceMessageID *int64  `json:"sourceMessageId"`
}

func optText(c *ical.Component, name string) *string {
	v, err := c.Props.Text(name)
	if err != nil || v == "" {
		return nil
	}
	return &v
}

// EventFromInstance renders one occurrence.
func EventFromInstance(master *ical.Component, in Instance) Event {
	c := in.Comp
	ev := Event{Title: textOr(c, ical.PropSummary, "(ohne Titel)"), Start: in.Start.String(), AllDay: in.Start.Date,
		Location: optText(c, ical.PropLocation), Description: optText(c, ical.PropDescription)}
	ev.UID, _ = master.Props.Text(ical.PropUID)
	if in.HasEnd {
		e := in.End.String()
		ev.End = &e
	}
	if rp := master.Props.Get(ical.PropRecurrenceRule); rp != nil {
		r := rp.Value
		ev.RRule = &r
	}
	if in.Recurring {
		s := in.Start.String()
		ev.Instance = &s
	}
	return ev
}

func textOr(c *ical.Component, name, def string) string {
	if v, err := c.Props.Text(name); err == nil && v != "" {
		return v
	}
	return def
}

func setOptText(c *ical.Component, name string, v *string) {
	if v == nil || strings.TrimSpace(*v) == "" {
		c.Props.Del(name)
		return
	}
	c.Props.SetText(name, strings.TrimSpace(*v))
}

func touch(c *ical.Component) {
	now := time.Now().UTC()
	c.Props.SetDateTime(ical.PropDateTimeStamp, now)
	c.Props.SetDateTime(ical.PropLastModified, now)
	seq := 0
	if p := c.Props.Get(ical.PropSequence); p != nil {
		seq, _ = p.Int()
		seq++
	}
	p := ical.NewProp(ical.PropSequence)
	p.Value = fmt.Sprint(seq)
	p.SetValueType(ical.ValueInt)
	c.Props.Set(p)
}

// newCalendar wraps a component in a VCALENDAR.
func newCalendar(c *ical.Component) *ical.Calendar {
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, ProdID)
	cal.Children = append(cal.Children, c)
	return cal
}

// validRRule accepts an RRULE value (with or without "RRULE:" prefix).
func validRRule(s string) (string, error) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "RRULE:"))
	if s == "" {
		return "", nil
	}
	opt, err := rrule.StrToROption(s)
	if err != nil {
		return "", fmt.Errorf("Ungültige Wiederholungsregel: %v", err)
	}
	if opt.Freq == rrule.SECONDLY || opt.Freq == rrule.MINUTELY {
		return "", fmt.Errorf("Wiederholung im Sekunden- oder Minutentakt wird nicht unterstützt")
	}
	return strings.ToUpper(s), nil
}

// NewEvent builds a calendar object for a new event.
func NewEvent(uid string, in EventInput) (*ical.Calendar, error) {
	c := ical.NewComponent(ical.CompEvent)
	c.Props.SetText(ical.PropUID, uid)
	c.Props.SetDateTime(ical.PropCreated, time.Now().UTC())
	if err := ApplyEvent(c, in, true); err != nil {
		return nil, err
	}
	return newCalendar(c), nil
}

// ApplyEvent writes the input into an existing VEVENT, keeping properties the
// web interface does not know (alarms, attendees, ...). With an Instance the
// series is moved by the offset between Instance and Start.
func ApplyEvent(c *ical.Component, in EventInput, isNew bool) error {
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return errors.New("Titel fehlt")
	}
	start, err := ParseWhen(in.Start, in.AllDay)
	if err != nil {
		return errors.New("Ungültiger Beginn")
	}
	var end *When
	if in.End != nil && strings.TrimSpace(*in.End) != "" {
		e, err := ParseWhen(*in.End, in.AllDay || start.Date)
		if err != nil {
			return errors.New("Ungültiges Ende")
		}
		if e.Date != start.Date {
			return errors.New("Beginn und Ende müssen beide ganztägig oder beide mit Uhrzeit sein")
		}
		if e.T.Before(start.T) {
			return errors.New("Das Ende liegt vor dem Beginn")
		}
		end = &e
	}
	if !isNew {
		oldStart, _, ok := startEnd(c)
		var inst When
		var ierr error = errors.New("no instance")
		if in.Instance != nil && *in.Instance != "" {
			inst, ierr = ParseWhen(*in.Instance, start.Date)
		}
		switch {
		case ok && ierr == nil && inst.Date == start.Date && oldStart.Date == start.Date:
			// Editing one occurrence moves the whole series by the same
			// offset; the series keeps its kind of time (zone, floating).
			dur := time.Duration(0)
			if end != nil {
				dur = end.T.Sub(start.T)
			}
			start = oldStart.Add(start.T.Sub(inst.T))
			if end != nil {
				e := start.Add(dur)
				end = &e
			}
		case ok:
			start = keepZone(start, oldStart)
			if end != nil {
				e := keepZone(*end, oldStart)
				end = &e
			}
		}
	}
	c.Props.SetText(ical.PropSummary, title)
	SetWhen(c.Props, ical.PropDateTimeStart, start)
	c.Props.Del(ical.PropDuration)
	if end != nil {
		SetWhen(c.Props, ical.PropDateTimeEnd, *end)
	} else {
		c.Props.Del(ical.PropDateTimeEnd)
	}
	setOptText(c, ical.PropLocation, in.Location)
	setOptText(c, ical.PropDescription, in.Description)
	if in.RRule != nil {
		r, err := validRRule(*in.RRule)
		if err != nil {
			return err
		}
		if r == "" {
			c.Props.Del(ical.PropRecurrenceRule)
		} else {
			p := ical.NewProp(ical.PropRecurrenceRule)
			p.SetValueType(ical.ValueRecurrence)
			p.Value = r
			c.Props.Set(p)
		}
	}
	touch(c)
	return nil
}

// keepZone keeps the TZID of the previous value when the new value is an
// instant, so events created on a phone keep their time zone.
func keepZone(w, old When) When {
	if old.TZID != "" && !w.Date && !w.Floating {
		w.TZID = old.TZID
	}
	return w
}

// ---- tasks ----

// Task is the JSON shape of a VTODO.
type Task struct {
	ID              int64     `json:"id"`
	CalendarID      int64     `json:"calendarId"`
	UID             string    `json:"uid"`
	Title           string    `json:"title"`
	Due             *string   `json:"due"`
	Done            bool      `json:"done"`
	Description     *string   `json:"description"`
	SourceMessageID *int64    `json:"sourceMessageId"`
	CreatedAt       time.Time `json:"createdAt"`
}

// TaskInput is the JSON body for creating or patching a task.
type TaskInput struct {
	CalendarID      *int64  `json:"calendarId"`
	Title           *string `json:"title"`
	Due             *string `json:"due"`
	Done            *bool   `json:"done"`
	Description     *string `json:"description"`
	SourceMessageID *int64  `json:"sourceMessageId"`
}

// TaskFrom renders the master VTODO of a calendar object.
func TaskFrom(cal *ical.Calendar) Task {
	c := Master(cal)
	t := Task{Title: textOr(c, ical.PropSummary, "(ohne Titel)"), Description: optText(c, ical.PropDescription)}
	t.UID, _ = c.Props.Text(ical.PropUID)
	if w, err := PropWhen(c.Props.Get(ical.PropDue)); err == nil {
		s := w.String()
		t.Due = &s
	}
	status, _ := c.Props.Text(ical.PropStatus)
	t.Done = strings.EqualFold(status, "COMPLETED") || c.Props.Get(ical.PropCompleted) != nil
	return t
}

// NewTask builds a calendar object for a new task.
func NewTask(uid string, in TaskInput) (*ical.Calendar, error) {
	c := ical.NewComponent(ical.CompToDo)
	c.Props.SetText(ical.PropUID, uid)
	c.Props.SetDateTime(ical.PropCreated, time.Now().UTC())
	c.Props.SetText(ical.PropStatus, "NEEDS-ACTION")
	if err := ApplyTask(c, in); err != nil {
		return nil, err
	}
	return newCalendar(c), nil
}

// ApplyTask patches a VTODO with the non-nil input fields.
func ApplyTask(c *ical.Component, in TaskInput) error {
	if in.Title != nil {
		t := strings.TrimSpace(*in.Title)
		if t == "" {
			return errors.New("Titel fehlt")
		}
		c.Props.SetText(ical.PropSummary, t)
	} else if c.Props.Get(ical.PropSummary) == nil {
		return errors.New("Titel fehlt")
	}
	if in.Due != nil {
		if strings.TrimSpace(*in.Due) == "" {
			c.Props.Del(ical.PropDue)
		} else {
			w, err := ParseWhen(*in.Due, false)
			if err != nil {
				return errors.New("Ungültiges Fälligkeitsdatum")
			}
			if sp := c.Props.Get(ical.PropDateTimeStart); sp != nil {
				// DUE must not be earlier than DTSTART and must share its type.
				c.Props.Del(ical.PropDateTimeStart)
			}
			SetWhen(c.Props, ical.PropDue, w)
		}
	}
	if in.Description != nil {
		setOptText(c, ical.PropDescription, in.Description)
	}
	if in.Done != nil {
		if *in.Done {
			c.Props.SetText(ical.PropStatus, "COMPLETED")
			c.Props.SetDateTime(ical.PropCompleted, time.Now().UTC())
			p := ical.NewProp(ical.PropPercentComplete)
			p.SetValueType(ical.ValueInt)
			p.Value = "100"
			c.Props.Set(p)
		} else {
			c.Props.SetText(ical.PropStatus, "NEEDS-ACTION")
			c.Props.Del(ical.PropCompleted)
			c.Props.Del(ical.PropPercentComplete)
		}
	}
	touch(c)
	return nil
}

// ---- import / export ----

// Split breaks a calendar (for example an .ics export with many events)
// into one object per UID, each carrying the time zones it references.
// Components without UID get one.
func Split(cal *ical.Calendar, newUID func() string) []*ical.Calendar {
	var zones []*ical.Component
	groups := map[string]*ical.Calendar{}
	var order []string
	for _, c := range cal.Children {
		switch c.Name {
		case ical.CompTimezone:
			zones = append(zones, c)
			continue
		case ical.CompEvent, ical.CompToDo, ical.CompJournal:
		default:
			continue
		}
		uid, _ := c.Props.Text(ical.PropUID)
		if uid == "" {
			uid = newUID()
			c.Props.SetText(ical.PropUID, uid)
		}
		if c.Props.Get(ical.PropDateTimeStamp) == nil {
			c.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
		}
		g, ok := groups[uid]
		if !ok {
			g = ical.NewCalendar()
			g.Props.SetText(ical.PropVersion, "2.0")
			g.Props.SetText(ical.PropProductID, ProdID)
			groups[uid] = g
			order = append(order, uid)
		}
		g.Children = append(g.Children, c)
	}
	out := make([]*ical.Calendar, 0, len(order))
	for _, uid := range order {
		g := groups[uid]
		used := map[string]bool{}
		for _, c := range g.Children {
			for _, props := range c.Props {
				for _, p := range props {
					if tz := p.Params.Get(ical.PropTimezoneID); tz != "" {
						used[tz] = true
					}
				}
			}
		}
		var tzs []*ical.Component
		for _, z := range zones {
			if id, _ := z.Props.Text(ical.PropTimezoneID); used[id] {
				tzs = append(tzs, z)
			}
		}
		g.Children = append(tzs, g.Children...)
		out = append(out, g)
	}
	return out
}

// Merge combines stored objects into one calendar for export.
func Merge(name string, objects [][]byte) ([]byte, error) {
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, ProdID)
	if name != "" {
		cal.Props.SetText("X-WR-CALNAME", name)
	}
	zones := map[string]bool{}
	for _, data := range objects {
		c, err := ParseCalendar(data)
		if err != nil {
			continue
		}
		for _, child := range c.Children {
			if child.Name == ical.CompTimezone {
				id, _ := child.Props.Text(ical.PropTimezoneID)
				if zones[id] {
					continue
				}
				zones[id] = true
			}
			cal.Children = append(cal.Children, child)
		}
	}
	if len(cal.Children) == 0 {
		// The encoder refuses empty calendars; write the frame by hand.
		return []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:" + ProdID + "\r\nEND:VCALENDAR\r\n"), nil
	}
	var buf bytes.Buffer
	if err := writeLenient(&buf, cal.Component); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// writeLenient encodes a component tree without the strict validation of
// the go-ical encoder (imported data is not always perfectly conformant).
func writeLenient(buf *bytes.Buffer, c *ical.Component) error {
	buf.WriteString("BEGIN:" + c.Name + "\r\n")
	names := make([]string, 0, len(c.Props))
	for n := range c.Props {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		for _, p := range c.Props[n] {
			line := n
			pnames := make([]string, 0, len(p.Params))
			for k := range p.Params {
				pnames = append(pnames, k)
			}
			sort.Strings(pnames)
			for _, k := range pnames {
				vals := make([]string, len(p.Params[k]))
				for i, v := range p.Params[k] {
					if strings.ContainsAny(v, ";:,") {
						v = `"` + v + `"`
					}
					vals[i] = v
				}
				line += ";" + k + "=" + strings.Join(vals, ",")
			}
			line += ":" + p.Value
			writeFolded(buf, line)
		}
	}
	for _, child := range c.Children {
		if err := writeLenient(buf, child); err != nil {
			return err
		}
	}
	buf.WriteString("END:" + c.Name + "\r\n")
	return nil
}

// writeFolded writes a content line folded at 75 octets (RFC 5545 3.1).
func writeFolded(buf *bytes.Buffer, line string) {
	const limit = 75
	for len(line) > limit {
		cut := limit
		for cut > 0 && !utf8Start(line[cut]) {
			cut--
		}
		if cut == 0 {
			cut = limit
		}
		buf.WriteString(line[:cut] + "\r\n ")
		line = line[cut:]
	}
	buf.WriteString(line + "\r\n")
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// Single returns the master component as one (unexpanded) instance.
func Single(cal *ical.Calendar) (Instance, bool) {
	master := Master(cal)
	if master == nil {
		return Instance{}, false
	}
	start, end, ok := startEnd(master)
	if !ok {
		return Instance{}, false
	}
	hasEnd := master.Props.Get(ical.PropDateTimeEnd) != nil || master.Props.Get(ical.PropDuration) != nil || start.Date
	return Instance{Comp: master, Start: start, End: end, HasEnd: hasEnd}, true
}

// EncodeLenient encodes a calendar, falling back to a non-validating writer
// for imported data the strict encoder rejects.
func EncodeLenient(cal *ical.Calendar) []byte {
	if data, err := EncodeCalendar(cal); err == nil {
		return data
	}
	var buf bytes.Buffer
	_ = writeLenient(&buf, cal.Component)
	return buf.Bytes()
}
