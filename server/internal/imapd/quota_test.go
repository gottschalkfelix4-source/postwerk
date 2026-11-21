package imapd

import (
	"context"
	"errors"
	"testing"

	"github.com/emersion/go-imap/v2"

	"postwerk/internal/store"
)

func TestCopyOverQuota(t *testing.T) {
	f := setup(t)
	f.deliver(t, "Eins", "erste Nachricht mit etwas Inhalt")
	used := f.st.UsedBytes(context.Background(), f.acct.ID)
	// Leave room for less than a second copy.
	quota := used + used/2
	if err := f.st.UpdateAccount(context.Background(), f.acct.ID, store.AccountPatch{QuotaBytes: &quota}); err != nil {
		t.Fatal(err)
	}
	c := f.dial(t, nil)
	if err := c.Create("Ablage", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Select("INBOX", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	_, err := c.Copy(imap.SeqSetNum(1), "Ablage").Wait()
	var ie *imap.Error
	if !errors.As(err, &ie) || ie.Code != imap.ResponseCodeOverQuota {
		t.Fatalf("expected OVERQUOTA, got %v", err)
	}
	if got := f.st.UsedBytes(context.Background(), f.acct.ID); got != used {
		t.Fatalf("copy stored despite quota: used %d -> %d", used, got)
	}
	// Direct store call reports ErrQuota as well.
	msgs, _ := f.st.IMAPMessages(context.Background(), mustInbox(t, f))
	dest, _ := f.st.EnsureMailbox(context.Background(), f.acct.ID, "Ablage")
	if _, err := f.st.CopyMessage(context.Background(), f.acct.ID, msgs[0].ID, dest); !errors.Is(err, store.ErrQuota) {
		t.Fatalf("CopyMessage: %v", err)
	}
}

func mustInbox(t *testing.T, f *fixture) int64 {
	t.Helper()
	id, err := f.st.MailboxByRole(context.Background(), f.acct.ID, "inbox")
	if err != nil {
		t.Fatal(err)
	}
	return id
}
