package store

import (
	"context"
	"testing"
	"time"
)

func TestQuarantineList(t *testing.T) {
	ctx := context.Background()
	st, a := newOpsStore(t)
	second := &Account{Email: "zwei@example.test", PasswordHash: "x", Role: "user"}
	if err := st.CreateAccount(ctx, second); err != nil {
		t.Fatal(err)
	}
	inbox, err := st.MailboxByRole(ctx, a.ID, "inbox")
	if err != nil {
		t.Fatal(err)
	}
	junk, err := st.MailboxByRole(ctx, a.ID, "junk")
	if err != nil {
		t.Fatal(err)
	}
	secondJunk, err := st.MailboxByRole(ctx, second.ID, "junk")
	if err != nil {
		t.Fatal(err)
	}
	inboxMsg := putMessage(t, st, a, inbox, "im Posteingang")
	junkOld := putMessage(t, st, a, junk, "spam eins")
	junkNew := putMessage(t, st, a, junk, "spam zwei")
	secondJunkMsg := putMessage(t, st, second, secondJunk, "spam beim zweiten Konto")

	items, emails, err := st.QuarantineList(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 quarantined messages, got %d", len(items))
	}
	// Newest first: secondJunkMsg, junkNew, junkOld (inserted in this order).
	want := []int64{secondJunkMsg.ID, junkNew.ID, junkOld.ID}
	for i, id := range want {
		if items[i].ID != id {
			t.Fatalf("order: items[%d] = %d, want %d", i, items[i].ID, id)
		}
	}
	if items[0].AccountID != second.ID || items[1].AccountID != a.ID {
		t.Fatalf("account ids: %+v", items)
	}
	if emails[a.ID] != a.Email || emails[second.ID] != second.Email {
		t.Fatalf("email map: %+v", emails)
	}
	if items[1].Account != a.Email || items[0].Account != second.Email {
		t.Fatalf("resolved account emails: %+v", items)
	}
	// Inbox messages must never appear.
	for _, it := range items {
		if it.ID == inboxMsg.ID {
			t.Fatal("inbox message leaked into quarantine list")
		}
	}
	// Limit is respected.
	if got, _, err := st.QuarantineList(ctx, 2); err != nil || len(got) != 2 {
		t.Fatalf("limit 2: got %d, %v", len(got), err)
	}
}

func TestMessagesSince(t *testing.T) {
	ctx := context.Background()
	st, a := newOpsStore(t)
	inbox, err := st.MailboxByRole(ctx, a.ID, "inbox")
	if err != nil {
		t.Fatal(err)
	}
	put := func(subject string, age time.Duration) *Message {
		t.Helper()
		h, err := st.Blobs.Put([]byte(subject))
		if err != nil {
			t.Fatal(err)
		}
		m := &Message{AccountID: a.ID, MailboxID: inbox, Subject: subject, ReceivedAt: time.Now().Add(-age), BlobHash: h}
		if err := st.InsertMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	old := put("alt", 48*time.Hour)
	fresh := put("neu", 5*time.Minute)
	cutoff := time.Now().Add(-time.Hour)
	msgs, err := st.MessagesSince(ctx, a.ID, cutoff, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].ID != fresh.ID || old.ID == fresh.ID {
		t.Fatalf("expected only the fresh message, got %+v", msgs)
	}
	if _, err := st.MessagesSince(ctx, a.ID, time.Now().Add(time.Hour), 10); err != nil {
		t.Fatal(err)
	}
}
