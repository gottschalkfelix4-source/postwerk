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
