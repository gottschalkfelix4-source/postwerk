package atrest

import (
	"bytes"
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"

	"postwerk/internal/store"
)

func storeMsg(t *testing.T, st *store.Store, accountID, mailbox int64, raw string) *store.Message {
	t.Helper()
	h, err := st.Blobs.Put([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	m := &store.Message{AccountID: accountID, MailboxID: mailbox, Subject: "Geheime Besprechung", Preview: "Der Tresorcode",
		SearchText: "tresorcode", ReceivedAt: time.Now(), Size: int64(len(raw)), BlobHash: h}
	if err := st.InsertMessage(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	return m
}

func decrypt(t *testing.T, raw []byte, key *openpgp.Entity) string {
	t.Helper()
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	_, params, _ := mime.ParseMediaType(m.Header.Get("Content-Type"))
	mr := multipart.NewReader(m.Body, params["boundary"])
	mr.NextPart()
	p, _ := mr.NextPart()
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

func TestEncryptExisting(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a := &store.Account{Email: "bob@example.test", PasswordHash: "x", Role: "user"}
	if err := st.CreateAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	inbox, _ := st.MailboxByRole(ctx, a.ID, "inbox")
	drafts, _ := st.MailboxByRole(ctx, a.ID, "drafts")
	m1 := storeMsg(t, st, a.ID, inbox, sample)
	m2 := storeMsg(t, st, a.ID, drafts, strings.Replace(sample, "abc@", "draft@", 1))

	mgr := &Manager{Store: st}
	if err := mgr.EncryptExisting(ctx, a.ID, nil); err == nil {
		t.Fatal("job ran without configured key")
	}
	priv, pub := newPGPKey(t)
	st.SetMailEncryption(ctx, &store.MailEncryption{AccountID: a.ID, Kind: KindPGP, PublicKey: string(pub)})

	var job Job
	if err := mgr.EncryptExisting(ctx, a.ID, func(fn func(*Job)) { fn(&job) }); err != nil {
		t.Fatal(err)
	}
	if job.Total != 1 || job.Converted != 1 || job.Failed != 0 {
		t.Fatalf("job = %+v", job)
	}
	got, _ := st.GetMessage(ctx, a.ID, m1.ID)
	raw, _ := st.Blobs.Get(got.BlobHash)
	if !IsEncrypted(raw) || got.UID == m1.UID || got.Size != int64(len(raw)) || got.Preview != "" || !got.AIDone {
		t.Fatalf("message not replaced: %+v", got)
	}
	if _, err := st.Blobs.Get(m1.BlobHash); err == nil {
		t.Fatal("plaintext blob kept")
	}
	if decrypt(t, raw, priv) != sample {
		t.Fatal("decrypted content differs")
	}
	if hits, _, _ := st.ListMessages(ctx, store.MessageQuery{AccountID: a.ID, MailboxID: inbox, Query: "tresorcode", Limit: 10}); len(hits) != 0 {
		t.Fatal("old search text kept")
	}
	d, _ := st.GetMessage(ctx, a.ID, m2.ID)
	if draw, _ := st.Blobs.Get(d.BlobHash); IsEncrypted(draw) {
		t.Fatal("draft encrypted")
	}

	// A second run skips already encrypted messages.
	job = Job{}
	mgr.EncryptExisting(ctx, a.ID, func(fn func(*Job)) { fn(&job) })
	if job.Skipped != 1 || job.Converted != 0 {
		t.Fatalf("second run = %+v", job)
	}

	// Background variant.
	if _, err := mgr.StartEncryptExisting(a.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for mgr.AccountJob(a.ID).Running && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if j := mgr.AccountJob(a.ID); j.Running || j.FinishedAt == nil || j.Error != "" {
		t.Fatalf("background job = %+v", j)
	}
}

func TestBlobMigrationJob(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h, _ := st.Blobs.Put([]byte("Subject: x\r\n\r\nbody"))
	mgr := &Manager{Store: st}
	if _, err := mgr.StartBlobMigration(true); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for mgr.BlobJob().Running && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if j := mgr.BlobJob(); j.Total != 1 || j.Converted != 1 || j.Error != "" {
		t.Fatalf("job = %+v", j)
	}
	stats, _ := st.Blobs.Stats(context.Background())
	if stats.Encrypted != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	if b, _ := st.Blobs.Get(h); string(b) != "Subject: x\r\n\r\nbody" {
		t.Fatal("content changed")
	}
}
