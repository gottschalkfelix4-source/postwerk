package store

import (
	"context"
	"os"
	"testing"
	"time"
)

func newOpsStore(t *testing.T) (*Store, *Account) {
	t.Helper()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	a := &Account{Email: "u@example.test", PasswordHash: "x", Role: "user"}
	if err := st.CreateAccount(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	return st, a
}

func putMessage(t *testing.T, st *Store, a *Account, mailbox int64, body string) *Message {
	t.Helper()
	h, err := st.Blobs.Put([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	m := &Message{AccountID: a.ID, MailboxID: mailbox, Subject: body, ReceivedAt: time.Now().AddDate(-1, 0, 0), BlobHash: h}
	if err := st.InsertMessage(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestPurgeOldMailboxMessages(t *testing.T) {
	ctx := context.Background()
	st, a := newOpsStore(t)
	trash, _ := st.MailboxByRole(ctx, a.ID, "trash")
	inbox, _ := st.MailboxByRole(ctx, a.ID, "inbox")
	old := putMessage(t, st, a, trash, "alt im Papierkorb")
	moved := putMessage(t, st, a, trash, "wieder herausgeholt")
	keep := putMessage(t, st, a, inbox, "im Posteingang")

	// First run only notes the messages, even though they were received a year ago.
	if n, err := st.PurgeOldMailboxMessages(ctx, "trash", 24*time.Hour, 100); err != nil || n != 0 {
		t.Fatalf("first run = %d, %v", n, err)
	}
	// Pretend housekeeping saw them 2 days ago; one is moved back to the inbox.
	st.DB.Exec(`UPDATE retention_seen SET seen_at = ?`, time.Now().Add(-48*time.Hour).Unix())
	st.DB.Exec(`UPDATE messages SET mailbox_id = ? WHERE id = ?`, inbox, moved.ID)
	n, err := st.PurgeOldMailboxMessages(ctx, "trash", 24*time.Hour, 100)
	if err != nil || n != 1 {
		t.Fatalf("second run = %d, %v", n, err)
	}
	if _, err := st.GetMessage(ctx, a.ID, old.ID); err == nil {
		t.Error("old trash message survived")
	}
	for _, m := range []*Message{moved, keep} {
		if _, err := st.GetMessage(ctx, a.ID, m.ID); err != nil {
			t.Errorf("message %q deleted", m.Subject)
		}
	}
	if _, err := st.Blobs.Get(old.BlobHash); err == nil {
		t.Error("blob of deleted message not released")
	}
	// Moving the message to the trash again restarts its clock.
	st.DB.Exec(`UPDATE messages SET mailbox_id = ? WHERE id = ?`, trash, moved.ID)
	if n, _ := st.PurgeOldMailboxMessages(ctx, "trash", 24*time.Hour, 100); n != 0 {
		t.Fatalf("re-trashed message deleted immediately")
	}
}

func TestPurgeOrphanBlobs(t *testing.T) {
	ctx := context.Background()
	st, a := newOpsStore(t)
	inbox, _ := st.MailboxByRole(ctx, a.ID, "inbox")
	used := putMessage(t, st, a, inbox, "referenziert")
	orphan, _ := st.Blobs.Put([]byte("verwaist"))
	fresh, _ := st.Blobs.Put([]byte("gerade geschrieben"))
	queued, _ := st.Blobs.Put([]byte("in der Warteschlange"))
	st.Enqueue(ctx, "a@example.test", []string{"b@example.org"}, queued, 1)
	old := time.Now().Add(-48 * time.Hour)
	for _, h := range []string{used.BlobHash, orphan, queued} {
		os.Chtimes(st.Blobs.path(h), old, old)
	}
	tmp := st.Blobs.path(orphan) + "x.tmp"
	os.WriteFile(tmp, []byte("rest"), 0o600)
	os.Chtimes(tmp, old, old)

	files, _, err := st.PurgeOrphanBlobs(ctx, 24*time.Hour)
	if err != nil || files != 2 {
		t.Fatalf("purged %d, %v", files, err)
	}
	for _, h := range []string{used.BlobHash, fresh, queued} {
		if _, err := st.Blobs.Get(h); err != nil {
			t.Errorf("blob %s removed", h[:8])
		}
	}
	if _, err := st.Blobs.Get(orphan); err == nil {
		t.Error("orphan blob kept")
	}
}

func TestKVAndAppPasswords(t *testing.T) {
	ctx := context.Background()
	st, a := newOpsStore(t)
	st.SetKV(ctx, "queue-delay:1", "100")
	st.SetKV(ctx, "queue-delay:2", "200")
	st.SetKV(ctx, "mtasts:example.org", "{}")
	if v, _ := st.KV(ctx, "queue-delay:2"); v != "200" {
		t.Fatalf("kv = %q", v)
	}
	if n, err := st.DeleteKVPrefix(ctx, "queue-delay:", map[string]bool{"2": true}); err != nil || n != 1 {
		t.Fatalf("delete prefix = %d, %v", n, err)
	}
	if v, _ := st.KV(ctx, "mtasts:example.org"); v != "{}" {
		t.Fatal("other keys touched")
	}
	st.CreateAppPassword(ctx, a.ID, "Handy", "hash")
	st.CreateAppPassword(ctx, a.ID, "Laptop", "hash")
	if err := st.DeleteAccountAppPasswords(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if l, _ := st.ListAppPasswords(ctx, a.ID); len(l) != 0 {
		t.Fatalf("app passwords left: %d", len(l))
	}
	if _, ok := st.OldestQueued(ctx); ok {
		t.Fatal("empty queue has an oldest message")
	}
}
