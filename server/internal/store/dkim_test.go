package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestDKIMStateMachine(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	keys := []DKIMKey{
		{ID: 1, Selector: "pw202601", Algorithm: DKIMAlgRSA, State: DKIMActive},
		{ID: 2, Selector: "pw202601e", Algorithm: DKIMAlgEd25519, State: DKIMActive},
		{ID: 3, Selector: "pw202611", Algorithm: DKIMAlgRSA, State: DKIMPending},
	}
	// Only one pending key per algorithm.
	var se *DKIMStateError
	if err := checkNewDKIMKey(keys, DKIMAlgRSA); !errors.As(err, &se) {
		t.Fatalf("second pending rsa key: %v", err)
	}
	if err := checkNewDKIMKey(keys, DKIMAlgEd25519); err != nil {
		t.Fatalf("pending ed25519 key: %v", err)
	}
	if err := checkNewDKIMKey(keys, "dsa"); err == nil {
		t.Fatal("unknown algorithm accepted")
	}

	// Activating the pending RSA key retires the active RSA key only.
	changes, err := planDKIMActivation(keys, 3, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 {
		t.Fatalf("changes: %+v", changes)
	}
	byID := map[int64]DKIMKey{}
	for _, c := range changes {
		byID[c.ID] = c
	}
	if byID[1].State != DKIMRetired || byID[1].RetiredAt == nil || !byID[1].RetiredAt.Equal(now) {
		t.Fatalf("old key: %+v", byID[1])
	}
	if byID[3].State != DKIMActive || byID[3].ActivatedAt == nil {
		t.Fatalf("new key: %+v", byID[3])
	}
	if _, ok := byID[2]; ok {
		t.Fatal("ed25519 key must stay active")
	}
	// Active or retired keys cannot be activated again; unknown ids fail.
	if _, err := planDKIMActivation(keys, 1, now); !errors.As(err, &se) {
		t.Fatalf("activate active key: %v", err)
	}
	if _, err := planDKIMActivation(keys, 99, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown key: %v", err)
	}

	// Deletion: never active keys, retired keys only after the grace period.
	if err := checkDKIMDelete(keys[0], now); !errors.As(err, &se) {
		t.Fatalf("delete active: %v", err)
	}
	if err := checkDKIMDelete(keys[2], now); err != nil {
		t.Fatalf("delete pending: %v", err)
	}
	retired := byID[1]
	if err := checkDKIMDelete(retired, now.Add(24*time.Hour)); !errors.As(err, &se) {
		t.Fatalf("delete freshly retired: %v", err)
	}
	if err := checkDKIMDelete(retired, now.Add(DKIMRetireGrace+time.Minute)); err != nil {
		t.Fatalf("delete after grace: %v", err)
	}
	if r := retired.RemovableAt(); r == nil || !r.Equal(now.Add(DKIMRetireGrace)) {
		t.Fatalf("removable at: %v", r)
	}

	// Selector naming.
	if s := NextDKIMSelector(keys, DKIMAlgRSA, now); s != "pw202611-2" {
		t.Fatalf("rsa selector: %s", s)
	}
	if s := NextDKIMSelector(keys, DKIMAlgEd25519, now); s != "pw202611e" {
		t.Fatalf("ed25519 selector: %s", s)
	}
}

func TestDKIMKeyRotationPersistence(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	if err := st.CreateDomain(ctx, "example.test", "pw1", "RSA-1"); err != nil {
		t.Fatal(err)
	}
	keys, err := st.ListDKIMKeys(ctx, "example.test")
	if err != nil || len(keys) != 1 || keys[0].State != DKIMActive || keys[0].Selector != "pw1" || keys[0].Algorithm != DKIMAlgRSA {
		t.Fatalf("initial keys: %+v %v", keys, err)
	}
	if _, err := st.AddDKIMKey(ctx, "example.test", "pw1e", DKIMAlgEd25519, "ED-1", DKIMActive); err != nil {
		t.Fatal(err)
	}
	pending, err := st.AddDKIMKey(ctx, "example.test", "pw2", DKIMAlgRSA, "RSA-2", DKIMPending)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddDKIMKey(ctx, "example.test", "pw3", DKIMAlgRSA, "RSA-3", DKIMPending); err == nil {
		t.Fatal("second pending rsa key accepted")
	}
	if _, err := st.AddDKIMKey(ctx, "example.test", "pw2", DKIMAlgEd25519, "X", DKIMPending); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate selector: %v", err)
	}

	active, _ := st.SigningKeys(ctx, "example.test")
	if len(active) != 2 || active[0].Selector != "pw1" || active[1].Selector != "pw1e" {
		t.Fatalf("signing keys before rotation: %+v", active)
	}

	if err := st.ActivateDKIMKey(ctx, "example.test", pending.ID); err != nil {
		t.Fatal(err)
	}
	active, _ = st.SigningKeys(ctx, "example.test")
	if len(active) != 2 || active[0].Selector != "pw2" || active[0].PrivateKey != "RSA-2" {
		t.Fatalf("signing keys after rotation: %+v", active)
	}
	d, _ := st.GetDomain(ctx, "example.test")
	if d.DKIMSelector != "pw2" || d.DKIMPrivateKey != "RSA-2" {
		t.Fatalf("domain columns not updated: %s", d.DKIMSelector)
	}
	keys, _ = st.ListDKIMKeys(ctx, "example.test")
	var old *DKIMKey
	for i := range keys {
		if keys[i].Selector == "pw1" {
			old = &keys[i]
		}
	}
	if old == nil || old.State != DKIMRetired || old.RetiredAt == nil {
		t.Fatalf("old key: %+v", old)
	}
	// Retired keys stay for the grace period, active keys cannot be removed.
	var se *DKIMStateError
	if err := st.DeleteDKIMKey(ctx, "example.test", old.ID); !errors.As(err, &se) {
		t.Fatalf("delete retired: %v", err)
	}
	if err := st.DeleteDKIMKey(ctx, "example.test", pending.ID); !errors.As(err, &se) {
		t.Fatalf("delete active: %v", err)
	}
	if err := st.ActivateDKIMKey(ctx, "other.test", pending.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign domain: %v", err)
	}
	// Keys go away with the domain.
	if err := st.DeleteDomain(ctx, "example.test"); err != nil {
		t.Fatal(err)
	}
	if keys, _ := st.ListDKIMKeys(ctx, "example.test"); len(keys) != 0 {
		t.Fatalf("keys left after domain deletion: %+v", keys)
	}
}

// TestDKIMMigration checks that the schema migration moves the key of an
// existing domain into the key table.
func TestDKIMMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	all := migrations
	idx := -1
	for i, m := range all {
		if strings.Contains(m, "CREATE TABLE dkim_keys") {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("dkim migration not found")
	}
	migrations = all[:idx]
	err = migrate(db)
	migrations = all
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO domains(name, dkim_selector, dkim_private_key, created_at) VALUES ('old.test', 'pw202001', 'PEM', 1577836800)`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	var sel, alg, state string
	var act int64
	if err := db.QueryRow(`SELECT selector, algorithm, state, activated_at FROM dkim_keys WHERE domain = 'old.test'`).Scan(&sel, &alg, &state, &act); err != nil {
		t.Fatal(err)
	}
	if sel != "pw202001" || alg != "rsa" || state != "active" || act != 1577836800 {
		t.Fatalf("migrated key: %s %s %s %d", sel, alg, state, act)
	}
}

func TestPurgeRetiredDKIMKeys(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	if err := st.CreateDomain(ctx, "example.test", "pw1", "RSA-1"); err != nil {
		t.Fatal(err)
	}
	next, err := st.AddDKIMKey(ctx, "example.test", "pw2", DKIMAlgRSA, "RSA-2", DKIMPending)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ActivateDKIMKey(ctx, "example.test", next.ID); err != nil {
		t.Fatal(err)
	}
	// Freshly retired: kept.
	if gone, err := st.PurgeRetiredDKIMKeys(ctx, DKIMCleanupMargin); err != nil || len(gone) != 0 {
		t.Fatalf("fresh purge = %+v, %v", gone, err)
	}
	// Past the grace period but within the margin: still kept.
	retire := func(ago time.Duration) {
		if _, err := st.DB.ExecContext(ctx, `UPDATE dkim_keys SET retired_at = ? WHERE selector = 'pw1'`, time.Now().Add(-ago).Unix()); err != nil {
			t.Fatal(err)
		}
	}
	retire(DKIMRetireGrace + time.Hour)
	if gone, _ := st.PurgeRetiredDKIMKeys(ctx, DKIMCleanupMargin); len(gone) != 0 {
		t.Fatalf("purged within margin: %+v", gone)
	}
	retire(DKIMRetireGrace + DKIMCleanupMargin + time.Hour)
	gone, err := st.PurgeRetiredDKIMKeys(ctx, DKIMCleanupMargin)
	if err != nil || len(gone) != 1 || gone[0].Selector != "pw1" || gone[0].Domain != "example.test" {
		t.Fatalf("purge = %+v, %v", gone, err)
	}
	keys, _ := st.ListDKIMKeys(ctx, "example.test")
	if len(keys) != 1 || keys[0].Selector != "pw2" || keys[0].State != DKIMActive {
		t.Fatalf("remaining keys: %+v", keys)
	}
	if sels := st.DKIMDNSCleanup(ctx, "example.test"); len(sels) != 1 || sels[0] != "pw1" {
		t.Fatalf("dns cleanup = %v", sels)
	}
	// A new key with the same selector withdraws the cleanup.
	if _, err := st.AddDKIMKey(ctx, "example.test", "pw1", DKIMAlgRSA, "RSA-3", DKIMPending); err != nil {
		t.Fatal(err)
	}
	if sels := st.DKIMDNSCleanup(ctx, "example.test"); len(sels) != 0 {
		t.Fatalf("dns cleanup after reuse = %v", sels)
	}
}

// TestIDSeqMigration checks that the id counters start above every id the
// change log has reported, including rows deleted before the upgrade.
func TestIDSeqMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	all := migrations
	idx := -1
	for i, m := range all {
		if m == idSeqMigration {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("id_seq migration not found")
	}
	migrations = all[:idx]
	err = migrate(db)
	migrations = all
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO accounts(id, email, password_hash, created_at) VALUES (3, 'a@x.test', 'x', 1)`,
		`INSERT INTO mailboxes(id, account_id, name, uid_validity, created_at) VALUES (5, 3, 'INBOX', 1, 1)`,
		`INSERT INTO messages(id, account_id, mailbox_id, uid, received_at, size, blob_hash) VALUES (7, 3, 5, 1, 1, 1, 'h')`,
		// Email 42 and mailbox 9 were deleted before the upgrade.
		`INSERT INTO jmap_changes(account_id, type, obj_id, modseq, destroyed, changed_at) VALUES (3, 'Email', 42, 1, 1, 1)`,
		`INSERT INTO jmap_changes(account_id, type, obj_id, modseq, destroyed, changed_at) VALUES (3, 'Mailbox', 9, 2, 1, 1)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{"messages": 42, "mailboxes": 9, "accounts": 3, "jmap_submissions": 0, "dav_collections": 0}
	for name, w := range want {
		var last int64
		if err := db.QueryRow(`SELECT last FROM id_seq WHERE name = ?`, name).Scan(&last); err != nil || last != w {
			t.Errorf("id_seq %s = %d, %v; want %d", name, last, err, w)
		}
	}
}
