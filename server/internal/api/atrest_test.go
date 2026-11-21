package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"

	"postwerk/internal/atrest"
	"postwerk/internal/auth"
	"postwerk/internal/export"
	"postwerk/internal/store"
)

func testPublicKey(t *testing.T, email string) string {
	t.Helper()
	e, err := openpgp.NewEntity("Anna", "", email, &packet.Config{Algorithm: packet.PubKeyAlgoEdDSA})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	w, _ := armor.Encode(&buf, openpgp.PublicKeyType, nil)
	e.Serialize(w)
	w.Close()
	return buf.String()
}

func TestMailEncryptionEndpoints(t *testing.T) {
	e := newAuthEnv(t)
	e.s.AtRest = &atrest.Manager{Store: e.s.Store}
	c := e.login(t, "")
	pub := testPublicKey(t, e.acct.Email)

	rec := e.do(t, "POST", "/api/account/encryption/inspect", map[string]any{"kind": "pgp", "publicKey": pub}, c)
	if rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte(`"matchesAccount":true`)) {
		t.Fatalf("inspect: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(t, "PUT", "/api/account/encryption", map[string]any{"kind": "pgp", "publicKey": "kaputt"}, c); rec.Code != 400 {
		t.Fatalf("invalid key accepted: %d", rec.Code)
	}
	rec = e.do(t, "PUT", "/api/account/encryption", map[string]any{"kind": "pgp", "publicKey": pub, "hideSubject": true}, c)
	if rec.Code != 200 {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	cfg, err := e.s.Store.GetMailEncryption(context.Background(), e.acct.ID)
	if err != nil || !cfg.HideSubject || cfg.Fingerprint == "" {
		t.Fatalf("stored config: %+v %v", cfg, err)
	}
	// Changing options without re-sending the key keeps it.
	if rec := e.do(t, "PUT", "/api/account/encryption", map[string]any{"hideSubject": false}, c); rec.Code != 200 {
		t.Fatalf("options update: %d %s", rec.Code, rec.Body)
	}
	if cfg, _ := e.s.Store.GetMailEncryption(context.Background(), e.acct.ID); cfg.HideSubject || cfg.PublicKey == "" {
		t.Fatal("options not updated")
	}
	if rec := e.do(t, "POST", "/api/account/encryption/encrypt-existing", nil, c); rec.Code != 200 {
		t.Fatalf("encrypt existing: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(t, "DELETE", "/api/account/encryption", nil, c); rec.Code != 200 {
		t.Fatalf("delete: %d", rec.Code)
	}
	if _, err := e.s.Store.GetMailEncryption(context.Background(), e.acct.ID); err != store.ErrNotFound {
		t.Fatal("opt-out not stored")
	}
	// Admin endpoint is not for users.
	if rec := e.do(t, "GET", "/api/admin/security/encryption", nil, c); rec.Code != 403 {
		t.Fatalf("admin status for user: %d", rec.Code)
	}
}

func TestDataExportEndpoints(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()
	e.s.Exports = &export.Manager{Store: e.s.Store, DataDir: e.dir}
	e.s.AtRest = &atrest.Manager{Store: e.s.Store}
	c := e.login(t, "")

	rec := e.do(t, "POST", "/api/account/exports", nil, c)
	if rec.Code != 200 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	var ex store.DataExport
	json.Unmarshal(rec.Body.Bytes(), &ex)
	e.s.Exports.Wait()
	if rec := e.do(t, "POST", "/api/account/exports", nil, c); rec.Code != 429 {
		t.Fatalf("rate limit: %d", rec.Code)
	}
	rec = e.do(t, "GET", fmt.Sprintf("/api/account/exports/%d/download", ex.ID), nil, c)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("download: %d %s", rec.Code, rec.Body)
	}
	if _, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len())); err != nil {
		t.Fatal("download is not a zip:", err)
	}

	// Another user cannot download it; an admin can and may export others.
	h, _ := auth.HashPassword(testPassword)
	admin := &store.Account{Email: "admin@example.test", PasswordHash: h, Role: "admin"}
	if err := e.s.Store.CreateAccount(ctx, admin); err != nil {
		t.Fatal(err)
	}
	e.acct = admin
	ac := e.login(t, "")
	if rec := e.do(t, "GET", fmt.Sprintf("/api/account/exports/%d/download", ex.ID), nil, ac); rec.Code != 404 {
		t.Fatalf("foreign download via user endpoint: %d", rec.Code)
	}
	if rec := e.do(t, "GET", fmt.Sprintf("/api/admin/exports/%d/download", ex.ID), nil, ac); rec.Code != 200 {
		t.Fatalf("admin download: %d", rec.Code)
	}
	annaID := ex.AccountID
	if rec := e.do(t, "POST", fmt.Sprintf("/api/admin/accounts/%d/exports", annaID), nil, ac); rec.Code != 200 {
		t.Fatalf("admin export: %d %s", rec.Code, rec.Body)
	}
	e.s.Exports.Wait()
	rec = e.do(t, "GET", fmt.Sprintf("/api/admin/accounts/%d/exports", annaID), nil, ac)
	var list []store.DataExport
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 2 {
		t.Fatalf("admin sees %d exports", len(list))
	}
	if rec := e.do(t, "GET", "/api/admin/security/encryption?stats=1", nil, ac); rec.Code != 200 ||
		!bytes.Contains(rec.Body.Bytes(), []byte(`"source":"data-file"`)) {
		t.Fatalf("encryption status: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(t, "PUT", "/api/admin/security/encryption/blobs", map[string]any{"enabled": true}, ac); rec.Code != 200 || !e.s.Store.BlobEncryption() {
		t.Fatalf("enable blob encryption: %d %s", rec.Code, rec.Body)
	}
}
