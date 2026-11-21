package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"postwerk/internal/pim"
)

func davTestStore(t *testing.T) (*Store, *Account) {
	t.Helper()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	a := &Account{Email: "anna@example.test", PasswordHash: "x", Role: "user"}
	if err := st.CreateAccount(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	return st, a
}

const icsTask = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:x\r\nBEGIN:VTODO\r\nUID:task-1\r\nDTSTAMP:20260101T000000Z\r\nSUMMARY:%s\r\nDUE;VALUE=DATE:20261224\r\nEND:VTODO\r\nEND:VCALENDAR\r\n"

func TestCalendarObjectETagAndCTag(t *testing.T) {
	st, a := davTestStore(t)
	ctx := context.Background()
	cals, err := st.ListCollections(ctx, a.ID, KindCalendar)
	if err != nil || len(cals) != 1 || cals[0].Name != "Kalender" || !cals[0].Supports("VEVENT") || !cals[0].Supports("VTODO") {
		t.Fatalf("default calendars = %+v, %v", cals, err)
	}
	books, err := st.ListCollections(ctx, a.ID, KindAddressBook)
	if err != nil || len(books) != 1 || books[0].Name != "Kontakte" {
		t.Fatalf("default address books = %+v, %v", books, err)
	}
	c := cals[0]
	seq0 := c.SyncSeq

	data := []byte(strings.Replace(icsTask, "%s", "Geschenke", 1))
	o, created, err := st.PutObject(ctx, c, "task-1.ics", data, PutOptions{IfNoneMatch: true})
	if err != nil || !created {
		t.Fatalf("put: %v created=%v", err, created)
	}
	if o.ETag != ETagOf(data) || o.Component != "VTODO" || o.UID != "task-1" {
		t.Fatalf("object = %+v", o)
	}
	if c.SyncSeq <= seq0 {
		t.Fatal("CTag must increase on create")
	}
	if !strings.Contains(string(o.Data), "Geschenke") {
		t.Fatal("data must be stored unchanged")
	}

	// Conditional writes.
	if _, _, err := st.PutObject(ctx, c, "task-1.ics", data, PutOptions{IfNoneMatch: true}); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("If-None-Match on existing: %v", err)
	}
	if _, _, err := st.PutObject(ctx, c, "task-1.ics", data, PutOptions{IfMatch: "bogus"}); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("wrong If-Match: %v", err)
	}
	seq1 := c.SyncSeq
	data2 := []byte(strings.Replace(icsTask, "%s", "Geschenke kaufen", 1))
	o2, created, err := st.PutObject(ctx, c, "task-1.ics", data2, PutOptions{IfMatch: o.ETag})
	if err != nil || created || o2.ETag == o.ETag || c.SyncSeq <= seq1 {
		t.Fatalf("update: %v created=%v etag %s->%s seq %d->%d", err, created, o.ETag, o2.ETag, seq1, c.SyncSeq)
	}
	if _, _, err := st.PutObject(ctx, c, "copy.ics", data2, PutOptions{}); !errors.Is(err, ErrUIDConflict) {
		t.Fatalf("duplicate UID: %v", err)
	}
	// Events are not accepted in a task-only calendar.
	todo := &Collection{AccountID: a.ID, Kind: KindCalendar, Name: "Aufgaben", Components: []string{"VEVENT"}}
	if err := st.CreateCollection(ctx, todo); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PutObject(ctx, todo, "x.ics", data2, PutOptions{}); !errors.Is(err, pim.ErrInvalid) {
		t.Fatalf("unsupported component: %v", err)
	}

	// Changes and tombstones.
	since := c.SyncSeq
	if err := st.DeleteObject(ctx, c, "task-1.ics", ""); err != nil {
		t.Fatal(err)
	}
	changed, deleted, err := st.DAVChanges(ctx, c, since)
	if err != nil || len(changed) != 0 || len(deleted) != 1 || deleted[0] != "task-1.ics" {
		t.Fatalf("changes = %v %v %v", changed, deleted, err)
	}
	all, _, _ := st.DAVChanges(ctx, c, 0)
	if len(all) != 0 {
		t.Fatalf("full sync after delete returned %d objects", len(all))
	}
}

func TestAddressObjectSearch(t *testing.T) {
	st, a := davTestStore(t)
	ctx := context.Background()
	books, _ := st.ListCollections(ctx, a.ID, KindAddressBook)
	card, _ := pim.NewCard("c-1", pim.ContactInput{FirstName: "Erika", LastName: "Mustermann",
		Emails: []pim.Entry{{Type: "work", Value: "Erika@Example.org"}}, Phones: []pim.Entry{{Type: "cell", Value: "+49 170 1234567"}}})
	data, _ := pim.EncodeCard(card)
	if _, _, err := st.PutObject(ctx, books[0], "c-1.vcf", data, PutOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"erika", "muster", "example.org", "1701234567", ""} {
		res, err := st.ListAddressObjects(ctx, a.ID, 0, q, 0)
		if err != nil || len(res) != 1 {
			t.Fatalf("search %q = %d, %v", q, len(res), err)
		}
	}
	if res, _ := st.ListAddressObjects(ctx, a.ID, 0, "nobody", 0); len(res) != 0 {
		t.Fatal("unexpected match")
	}
	if _, _, err := st.PutObject(ctx, books[0], "bad.vcf", []byte("BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Ohne UID\r\nEND:VCARD\r\n"), PutOptions{}); !errors.Is(err, pim.ErrInvalidCard) {
		t.Fatalf("card without UID: %v", err)
	}
}

func TestLegacyCalendarMigration(t *testing.T) {
	st, a := davTestStore(t)
	ctx := context.Background()
	// Rows written by earlier versions into the old tables.
	if _, err := st.DB.Exec(`INSERT INTO events(account_id, title, start, end_time, location, description, source_message_id) VALUES
		(?, 'Projekt-Kickoff', '2026-11-20T09:00:00Z', '2026-11-20T10:30:00Z', 'Raum 1', 'Agenda folgt', 42),
		(?, 'Urlaub', '2026-12-24', NULL, NULL, NULL, NULL),
		(?, 'Kaputt', 'irgendwann', NULL, NULL, NULL, NULL)`, a.ID, a.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`INSERT INTO tasks(account_id, title, due, done, created_at) VALUES (?, 'Rechnung zahlen', '2026-11-30', 1, 1700000000)`, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`DELETE FROM settings WHERE key = 'pim_legacy_migrated'`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // the second run must not duplicate anything
		if err := migrateLegacyCalendar(ctx, st.DB); err != nil {
			t.Fatal(err)
		}
	}
	events, err := st.ListCalendarObjects(ctx, a.ID, "VEVENT", 0, nil, nil)
	if err != nil || len(events) != 2 {
		t.Fatalf("migrated events = %d, %v", len(events), err)
	}
	var kickoff *DavObject
	for _, e := range events {
		if strings.Contains(string(e.Data), "Projekt-Kickoff") {
			kickoff = e
		}
	}
	if kickoff == nil || kickoff.SourceMessageID == nil || *kickoff.SourceMessageID != 42 {
		t.Fatalf("kickoff = %+v", kickoff)
	}
	for _, want := range []string{"DTSTART:20261120T090000Z", "DTEND:20261120T103000Z", "LOCATION:Raum 1", "DESCRIPTION:Agenda folgt"} {
		if !strings.Contains(string(kickoff.Data), want) {
			t.Fatalf("missing %q in\n%s", want, kickoff.Data)
		}
	}
	tasks, _ := st.ListCalendarObjects(ctx, a.ID, "VTODO", 0, nil, nil)
	if len(tasks) != 1 || !strings.Contains(string(tasks[0].Data), "STATUS:COMPLETED") || !strings.Contains(string(tasks[0].Data), "DUE;VALUE=DATE:20261130") {
		t.Fatalf("tasks = %+v", tasks)
	}
	if tasks[0].CreatedAt.Unix() != 1700000000 {
		t.Fatalf("created_at not kept: %v", tasks[0].CreatedAt)
	}
	// Range queries use the indexed times.
	from, to := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC).Unix(), time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC).Unix()
	inRange, _ := st.ListCalendarObjects(ctx, a.ID, "VEVENT", 0, &from, &to)
	if len(inRange) != 1 {
		t.Fatalf("range query = %d", len(inRange))
	}
}
