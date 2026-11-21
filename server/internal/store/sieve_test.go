package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestSieveScripts(t *testing.T) {
	ctx := context.Background()
	st, a := newOpsStore(t)

	if _, err := st.ActiveSieveScript(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("active without scripts: %v", err)
	}
	if err := st.PutSieveScript(ctx, a.ID, "eins", "keep;"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutSieveScript(ctx, a.ID, "zwei", "discard;"); err != nil {
		t.Fatal(err)
	}
	// Replacing keeps a single row.
	if err := st.PutSieveScript(ctx, a.ID, "eins", "stop;"); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListSieveScripts(ctx, a.ID)
	if err != nil || len(list) != 2 || list[0].Name != "eins" || list[0].Size != 5 || list[0].Content != "" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	sc, err := st.GetSieveScript(ctx, a.ID, "eins")
	if err != nil || sc.Content != "stop;" || sc.Active {
		t.Fatalf("get = %+v, %v", sc, err)
	}

	// Exactly one active script.
	if err := st.SetActiveSieveScript(ctx, a.ID, "eins"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetActiveSieveScript(ctx, a.ID, "zwei"); err != nil {
		t.Fatal(err)
	}
	act, err := st.ActiveSieveScript(ctx, a.ID)
	if err != nil || act.Name != "zwei" {
		t.Fatalf("active = %+v, %v", act, err)
	}
	list, _ = st.ListSieveScripts(ctx, a.ID)
	if list[0].Active || !list[1].Active {
		t.Fatalf("active flags = %+v", list)
	}
	if err := st.SetActiveSieveScript(ctx, a.ID, "fehlt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("activate missing: %v", err)
	}
	if act, _ := st.ActiveSieveScript(ctx, a.ID); act == nil || act.Name != "zwei" {
		t.Fatal("failed activation changed the active script")
	}

	// The active script cannot be deleted.
	if err := st.DeleteSieveScript(ctx, a.ID, "zwei"); !errors.Is(err, ErrSieveActive) {
		t.Fatalf("delete active: %v", err)
	}
	if err := st.RenameSieveScript(ctx, a.ID, "zwei", "eins"); !errors.Is(err, ErrConflict) {
		t.Fatalf("rename onto existing: %v", err)
	}
	if err := st.RenameSieveScript(ctx, a.ID, "zwei", "drei"); err != nil {
		t.Fatal(err)
	}
	if act, _ := st.ActiveSieveScript(ctx, a.ID); act == nil || act.Name != "drei" {
		t.Fatal("rename lost the active flag")
	}
	if err := st.RenameSieveScript(ctx, a.ID, "fehlt", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rename missing: %v", err)
	}
	if err := st.SetActiveSieveScript(ctx, a.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ActiveSieveScript(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deactivate: %v", err)
	}
	if err := st.DeleteSieveScript(ctx, a.ID, "drei"); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteSieveScript(ctx, a.ID, "drei"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}

	// Names and limits.
	for _, bad := range []string{"", " x", "a\nb", "tab\t", strings.Repeat("n", MaxSieveNameLen+1)} {
		if err := st.PutSieveScript(ctx, a.ID, bad, "keep;"); !errors.Is(err, ErrSieveName) {
			t.Errorf("name %q accepted: %v", bad, err)
		}
	}
	if err := st.PutSieveScript(ctx, a.ID, "groß", strings.Repeat("#", MaxSieveScriptSize+1)); !errors.Is(err, ErrSieveQuota) {
		t.Fatalf("oversized script: %v", err)
	}
	// "eins" still exists.
	for i := 0; i < MaxSieveScripts-2; i++ {
		if err := st.PutSieveScript(ctx, a.ID, fmt.Sprintf("s%02d", i), "keep;"); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SieveHaveSpace(ctx, a.ID, "neu", 10); err != nil {
		t.Fatalf("have space below limit: %v", err)
	}
	if err := st.PutSieveScript(ctx, a.ID, "voll", "keep;"); err != nil {
		t.Fatal(err)
	}
	if err := st.SieveHaveSpace(ctx, a.ID, "noch-eins", 10); !errors.Is(err, ErrSieveQuota) {
		t.Fatalf("have space at limit: %v", err)
	}
	if err := st.SieveHaveSpace(ctx, a.ID, "voll", 10); err != nil {
		t.Fatalf("replacing at limit: %v", err)
	}
	if err := st.PutSieveScript(ctx, a.ID, "noch-eins", "keep;"); !errors.Is(err, ErrSieveQuota) {
		t.Fatalf("script count limit: %v", err)
	}

	// Scripts disappear with the account.
	if err := st.DeleteAccount(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := st.ListSieveScripts(ctx, a.ID); len(list) != 0 {
		t.Fatalf("scripts survived account deletion: %d", len(list))
	}
}

func TestSieveVacationTracking(t *testing.T) {
	ctx := context.Background()
	st, a := newOpsStore(t)
	if !st.SieveVacationShouldReply(ctx, a.ID, "h1:x@y.z", 7) {
		t.Fatal("first reply not due")
	}
	if st.SieveVacationShouldReply(ctx, a.ID, "h1:x@y.z", 7) {
		t.Fatal("second reply within days")
	}
	if !st.SieveVacationShouldReply(ctx, a.ID, "h2:x@y.z", 7) {
		t.Fatal("other handle must reply")
	}
}

func TestMailboxByName(t *testing.T) {
	ctx := context.Background()
	st, a := newOpsStore(t)
	inbox, _ := st.MailboxByRole(ctx, a.ID, "inbox")
	for _, n := range []string{"INBOX", "inbox", "Posteingang", "posteingang"} {
		if id, err := st.MailboxByName(ctx, a.ID, n); err != nil || id != inbox {
			t.Errorf("%s -> %d, %v", n, id, err)
		}
	}
	mb, err := st.CreateMailbox(ctx, a.ID, "Projekte/Alpha")
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := st.MailboxByName(ctx, a.ID, "projekte/alpha"); id != mb.ID {
		t.Fatal("case-insensitive lookup failed")
	}
	if _, err := st.MailboxByName(ctx, a.ID, "Fehlt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestSubaddressResolution(t *testing.T) {
	ctx := context.Background()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateDomain(ctx, "example.test", "pw", "key"); err != nil {
		t.Fatal(err)
	}
	for _, e := range []string{"alice@example.test", "bob+special@example.test", "carol@example.test"} {
		if err := st.CreateAccount(ctx, &Account{Email: e, PasswordHash: "x", Role: "user"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.CreateAlias(ctx, "team@example.test", []string{"alice@example.test", "extern@other.test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAlias(ctx, "alice+alias@example.test", []string{"carol@example.test"}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		rcpt           string
		local, externl string
	}{
		{"alice+news@example.test", "alice@example.test", ""},
		{"Alice+News@Example.test", "alice@example.test", ""},
		{"alice+a+b@example.test", "alice@example.test", ""},
		{"team+x@example.test", "alice@example.test", "extern@other.test"},
		// An existing address with "+" is used as it is.
		{"bob+special@example.test", "bob+special@example.test", ""},
		{"alice+alias@example.test", "carol@example.test", ""},
		{"nobody+x@example.test", "", ""},
		{"+x@example.test", "", ""},
		// Remote addresses are not rewritten.
		{"x+y@other.test", "", "x+y@other.test"},
	}
	for _, c := range cases {
		local, external, err := st.ResolveRecipient(ctx, c.rcpt)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(local, ",") != c.local || strings.Join(external, ",") != c.externl {
			t.Errorf("%s -> %v %v, want %q %q", c.rcpt, local, external, c.local, c.externl)
		}
	}
	// Catch-all still applies to unknown sub-addresses.
	target := "carol@example.test"
	if err := st.SetCatchAll(ctx, "example.test", &target); err != nil {
		t.Fatal(err)
	}
	if local, _, _ := st.ResolveRecipient(ctx, "nobody+x@example.test"); len(local) != 1 || local[0] != target {
		t.Fatalf("catch-all for sub-address: %v", local)
	}
}
