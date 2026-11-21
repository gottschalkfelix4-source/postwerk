package delivery_test

import (
	"bytes"
	"context"
	"io"
	"mime"
	"mime/multipart"
	netmail "net/mail"
	"net/smtp"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"

	"postwerk/internal/atrest"
	"postwerk/internal/delivery"
	"postwerk/internal/store"
)

func pgpKey(t *testing.T) (*openpgp.Entity, string) {
	t.Helper()
	e, err := openpgp.NewEntity("Alice", "", "alice@example.test", &packet.Config{Algorithm: packet.PubKeyAlgoEdDSA})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	w, _ := armor.Encode(&buf, openpgp.PublicKeyType, nil)
	e.Serialize(w)
	w.Close()
	return e, buf.String()
}

func decryptPGPMIME(t *testing.T, raw []byte, key *openpgp.Entity) string {
	t.Helper()
	m, err := netmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	_, params, _ := mime.ParseMediaType(m.Header.Get("Content-Type"))
	mr := multipart.NewReader(m.Body, params["boundary"])
	mr.NextPart()
	p, err := mr.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	block, err := armor.Decode(p)
	if err != nil {
		t.Fatal(err)
	}
	md, err := openpgp.ReadMessage(block.Body, openpgp.EntityList{key}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(md.UnverifiedBody)
	return string(b)
}

func enableEncryption(t *testing.T, st *store.Store, accountID int64, pub string, hide bool) {
	t.Helper()
	info, err := atrest.ParseKey(atrest.KindPGP, []byte(pub))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetMailEncryption(context.Background(), &store.MailEncryption{AccountID: accountID, Kind: atrest.KindPGP, PublicKey: pub,
		Fingerprint: info.Fingerprint, HideSubject: hide}); err != nil {
		t.Fatal(err)
	}
}

func TestInboundEncryptedAtRest(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	alice, _ := e.st.GetAccountByEmail(ctx, "alice@example.test")
	bob, _ := e.st.GetAccountByEmail(ctx, "bob@example.test")
	priv, pub := pgpKey(t)
	enableEncryption(t, e.st, alice.ID, pub, false)

	if err := smtp.SendMail(e.smtp, nil, "sender@sender.test", []string{"team@example.test"}, []byte(testMail)); err != nil {
		t.Fatalf("send: %v", err)
	}
	list, _, _ := e.st.ListMessages(ctx, store.MessageQuery{AccountID: alice.ID, Limit: 10})
	if len(list) != 1 {
		t.Fatalf("alice has %d messages", len(list))
	}
	m := list[0]
	raw, _ := e.st.Blobs.Get(m.BlobHash)
	if !atrest.IsEncrypted(raw) || bytes.Contains(raw, []byte("Hallo zusammen")) {
		t.Fatalf("stored message not encrypted:\n%s", raw)
	}
	if m.Subject != "Hallo Team" || m.From.Email != "sender@sender.test" || m.Preview != "" || m.Size != int64(len(raw)) {
		t.Fatalf("metadata: %+v", m)
	}
	full, _ := e.st.GetMessageAny(ctx, m.ID)
	if !full.AIDone {
		t.Fatal("encrypted message queued for AI analysis")
	}
	if hits, _, _ := e.st.ListMessages(ctx, store.MessageQuery{AccountID: alice.ID, Query: "zusammen", Limit: 10}); len(hits) != 0 {
		t.Fatal("body text searchable after encryption")
	}
	plain := decryptPGPMIME(t, raw, priv)
	if !strings.Contains(plain, "Hallo zusammen,") || !strings.HasPrefix(plain, "Received:") {
		t.Fatalf("decrypted message:\n%s", plain)
	}
	// Bob did not opt in.
	bl, _, _ := e.st.ListMessages(ctx, store.MessageQuery{AccountID: bob.ID, Limit: 10})
	braw, _ := e.st.Blobs.Get(bl[0].BlobHash)
	if atrest.IsEncrypted(braw) || bl[0].Preview == "" {
		t.Fatal("message for bob encrypted")
	}

	// Drafts stay editable.
	drafts, _ := e.st.MailboxByRole(ctx, alice.ID, "drafts")
	d, err := e.svc.StoreMessage(ctx, alice.ID, drafts, []byte(testMail), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if draw, _ := e.st.Blobs.Get(d.BlobHash); atrest.IsEncrypted(draw) {
		t.Fatal("draft encrypted")
	}
	// IMAP APPEND / imports go through ImportMessage.
	inbox, _ := e.st.MailboxByRole(ctx, alice.ID, "inbox")
	im, err := e.svc.ImportMessage(ctx, alice, inbox, []byte(strings.Replace(testMail, "abc@", "imp@", 1)), delivery.ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if iraw, _ := e.st.Blobs.Get(im.BlobHash); !atrest.IsEncrypted(iraw) || !im.AIDone {
		t.Fatal("imported message not encrypted")
	}

	// Opt-out: new mail is stored in plaintext again.
	if err := e.st.DeleteMailEncryption(ctx, alice.ID); err != nil {
		t.Fatal(err)
	}
	pm, err := e.svc.StoreMessage(ctx, alice.ID, inbox, []byte(strings.Replace(testMail, "abc@", "plain@", 1)), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if praw, _ := e.st.Blobs.Get(pm.BlobHash); atrest.IsEncrypted(praw) {
		t.Fatal("message encrypted after opt-out")
	}
}

func TestEncryptedAtRestHiddenSubject(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	alice, _ := e.st.GetAccountByEmail(ctx, "alice@example.test")
	priv, pub := pgpKey(t)
	enableEncryption(t, e.st, alice.ID, pub, true)
	inbox, _ := e.st.MailboxByRole(ctx, alice.ID, "inbox")
	m, err := e.svc.StoreMessage(ctx, alice.ID, inbox, []byte(testMail), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := e.st.Blobs.Get(m.BlobHash)
	if m.Subject != "..." || bytes.Contains(raw, []byte("Hallo Team")) {
		t.Fatalf("subject not hidden: %q", m.Subject)
	}
	if !strings.Contains(decryptPGPMIME(t, raw, priv), "Subject: Hallo Team") {
		t.Fatal("real subject missing inside")
	}
}
