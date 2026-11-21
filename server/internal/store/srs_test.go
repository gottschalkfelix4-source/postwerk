package store

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestSRSSecretPersisted(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	a, err := st.SRSSecret(context.Background())
	if err != nil || len(a) != 32 {
		t.Fatalf("secret: %x %v", a, err)
	}
	b, _ := st.SRSSecret(context.Background())
	if !bytes.Equal(a, b) {
		t.Fatal("secret changed between calls")
	}
	st.Close()
	st, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, _ := st.SRSSecret(context.Background())
	if !bytes.Equal(a, c) {
		t.Fatal("secret not persisted")
	}
}

func TestCheckQuota(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	a := &Account{Email: "q@example.test", PasswordHash: "x", Role: "user", QuotaBytes: 100}
	if err := st.CreateAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := st.CheckQuota(ctx, a.ID, 100); err != nil {
		t.Fatalf("exactly at quota: %v", err)
	}
	if err := st.CheckQuota(ctx, a.ID, 101); !errors.Is(err, ErrQuota) {
		t.Fatalf("over quota: %v", err)
	}
	zero := int64(0)
	if err := st.UpdateAccount(ctx, a.ID, AccountPatch{QuotaBytes: &zero}); err != nil {
		t.Fatal(err)
	}
	if err := st.CheckQuota(ctx, a.ID, 1<<40); err != nil {
		t.Fatalf("unlimited: %v", err)
	}
}
