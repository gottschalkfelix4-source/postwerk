package store

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestIDsAreNeverReused(t *testing.T) {
	st, a := davTestStore(t)
	ctx := context.Background()
	inbox, err := st.MailboxByRole(ctx, a.ID, "inbox")
	if err != nil {
		t.Fatal(err)
	}
	insert := func(msgID string) *Message {
		t.Helper()
		m := &Message{AccountID: a.ID, MailboxID: inbox, MessageID: msgID, Subject: msgID, ReceivedAt: time.Now(), Size: 10, BlobHash: "00" + msgID}
		if err := st.InsertMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	// Messages and threads.
	insert("a@x")
	b := insert("b@x")
	if b.ThreadID != b.ID {
		t.Fatalf("new thread id = %d, want %d", b.ThreadID, b.ID)
	}
	if err := st.DeleteMessage(ctx, a.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	c := insert("c@x")
	if c.ID <= b.ID || c.ThreadID <= b.ThreadID {
		t.Fatalf("message id reused: deleted %d (thread %d), new %d (thread %d)", b.ID, b.ThreadID, c.ID, c.ThreadID)
	}
	// A reply keeps the thread of the message it refers to.
	r := &Message{AccountID: a.ID, MailboxID: inbox, MessageID: "r@x", InReplyTo: "c@x", ReceivedAt: time.Now(), Size: 1, BlobHash: "00r"}
	if err := st.InsertMessage(ctx, r); err != nil {
		t.Fatal(err)
	}
	if r.ThreadID != c.ID || r.ID <= c.ID {
		t.Fatalf("reply = id %d thread %d, want thread %d", r.ID, r.ThreadID, c.ID)
	}

	// Mailboxes.
	mb, err := st.CreateMailbox(ctx, a.ID, "Projekt")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteMailbox(ctx, a.ID, mb.ID); err != nil {
		t.Fatal(err)
	}
	mb2, err := st.CreateMailbox(ctx, a.ID, "Projekt")
	if err != nil {
		t.Fatal(err)
	}
	if mb2.ID <= mb.ID {
		t.Fatalf("mailbox id reused: %d -> %d", mb.ID, mb2.ID)
	}

	// Submissions.
	sub := &Submission{IdentityID: "I1", EmailID: c.ID, ThreadID: c.ThreadID, EnvelopeJSON: "{}", SendAt: time.Now()}
	if err := st.CreateSubmission(ctx, a.ID, sub); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteSubmission(ctx, a.ID, sub.ID); err != nil {
		t.Fatal(err)
	}
	sub2 := &Submission{IdentityID: "I1", EmailID: c.ID, ThreadID: c.ThreadID, EnvelopeJSON: "{}", SendAt: time.Now()}
	if err := st.CreateSubmission(ctx, a.ID, sub2); err != nil {
		t.Fatal(err)
	}
	if sub2.ID <= sub.ID {
		t.Fatalf("submission id reused: %d -> %d", sub.ID, sub2.ID)
	}

	// Accounts.
	x := &Account{Email: "x@example.test", PasswordHash: "x", Role: "user"}
	if err := st.CreateAccount(ctx, x); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, `DELETE FROM accounts WHERE id = ?`, x.ID); err != nil {
		t.Fatal(err)
	}
	y := &Account{Email: "y@example.test", PasswordHash: "x", Role: "user"}
	if err := st.CreateAccount(ctx, y); err != nil {
		t.Fatal(err)
	}
	if y.ID <= x.ID {
		t.Fatalf("account id reused: %d -> %d", x.ID, y.ID)
	}

	// DAV collections (their id is part of the sync token).
	cal := &Collection{AccountID: a.ID, Kind: KindCalendar, Name: "Urlaub", Slug: "urlaub"}
	if err := st.CreateCollection(ctx, cal); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteCollection(ctx, a.ID, cal.ID); err != nil {
		t.Fatal(err)
	}
	cal2 := &Collection{AccountID: a.ID, Kind: KindCalendar, Name: "Urlaub", Slug: "urlaub"}
	if err := st.CreateCollection(ctx, cal2); err != nil {
		t.Fatal(err)
	}
	if cal2.ID <= cal.ID {
		t.Fatalf("collection id reused: %d -> %d", cal.ID, cal2.ID)
	}
}

// Rows written without the counter (older code, manual inserts) are never
// collided with.
func TestNextIDFollowsExistingRows(t *testing.T) {
	st, a := davTestStore(t)
	ctx := context.Background()
	if _, err := st.DB.ExecContext(ctx, `INSERT INTO mailboxes(id, account_id, name, uid_validity, created_at) VALUES (1000, ?, 'Alt', 1, 1)`, a.ID); err != nil {
		t.Fatal(err)
	}
	mb, err := st.CreateMailbox(ctx, a.ID, "Neu")
	if err != nil {
		t.Fatal(err)
	}
	if mb.ID != 1001 {
		t.Fatalf("id = %d, want 1001", mb.ID)
	}
	var last int64
	if err := st.DB.QueryRowContext(ctx, `SELECT last FROM id_seq WHERE name = 'mailboxes'`).Scan(&last); err != nil || last != 1001 {
		t.Fatalf("id_seq = %d, %v", last, err)
	}
	if err := st.tx(ctx, func(tx *sql.Tx) error { _, err := nextID(ctx, tx, "sessions"); return err }); err == nil {
		t.Fatal("unknown tables must be rejected")
	}
}
