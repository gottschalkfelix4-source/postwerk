package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"postwerk/internal/delivery"
	"postwerk/internal/message"
	"postwerk/internal/store"
)

func mailTestServer(t *testing.T) (*Server, *store.Account, *store.Account) {
	t.Helper()
	s := newTestServer(t, store.WebSettings{})
	ctx := context.Background()
	if err := s.Store.CreateDomain(ctx, "example.test", "pw1", ""); err != nil {
		t.Fatal(err)
	}
	user := &store.Account{Email: "user@example.test", Role: "user", PasswordHash: "x"}
	admin := &store.Account{Email: "admin@example.test", Role: "admin", PasswordHash: "x"}
	for _, a := range []*store.Account{user, admin} {
		if err := s.Store.CreateAccount(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	s.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	s.Delivery = &delivery.Service{Store: s.Store, Settings: s.Settings, Log: s.Log}
	return s, user, admin
}

func storeWithAttachment(t *testing.T, s *Server, acct *store.Account, filename string) *store.Message {
	t.Helper()
	ctx := context.Background()
	raw := message.Build(message.Draft{
		From: store.Address{Email: "x@other.test"}, To: []store.Address{{Email: acct.Email}}, Subject: "Anhang", MessageID: "1@other.test",
		Attach: []message.OutAttachment{{Filename: filename, ContentType: "application/pdf", Data: []byte("%PDF")}},
	})
	inbox, _ := s.Store.MailboxByRole(ctx, acct.ID, "inbox")
	m, err := s.Delivery.ImportMessage(ctx, acct, inbox, raw, delivery.ImportOptions{SkipAI: true})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func attachmentReq(m *store.Message, att int64) *http.Request {
	r := httptest.NewRequest("GET", "/api/messages/x/attachments/y", nil)
	r.SetPathValue("id", strconv.FormatInt(m.ID, 10))
	r.SetPathValue("att", strconv.FormatInt(att, 10))
	return r
}

func TestAttachmentBlockedForVirus(t *testing.T) {
	s, user, admin := mailTestServer(t)
	for _, acct := range []*store.Account{user, admin} {
		m := storeWithAttachment(t, s, acct, "bericht.pdf")
		if _, err := s.Store.DB.Exec(`UPDATE messages SET virus = 'Eicar-Test' WHERE id = ?`, m.ID); err != nil {
			t.Fatal(err)
		}
		m, _ = s.Store.GetMessage(context.Background(), acct.ID, m.ID)
		w := httptest.NewRecorder()
		_, err := s.attachment(w, attachmentReq(m, 2), acct)
		if acct.Role == "admin" {
			if err != nil || w.Body.String() != "%PDF" {
				t.Fatalf("admin download: %v %q", err, w.Body.String())
			}
			continue
		}
		var ae *apiError
		if !errors.As(err, &ae) || ae.Status != 403 {
			t.Fatalf("user download of infected attachment: %v", err)
		}
		if _, err := s.rawMessage(httptest.NewRecorder(), attachmentReq(m, 0), acct); !errors.As(err, &ae) || ae.Status != 403 {
			t.Fatalf("user raw download of infected message: %v", err)
		}
	}
}

func TestAttachmentFilenameSanitized(t *testing.T) {
	s, user, _ := mailTestServer(t)
	m := storeWithAttachment(t, s, user, "rechnung‮fdp.exe")
	d, err := s.detail(m)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Attachments) != 1 || d.Attachments[0].Filename != "rechnungfdp.exe" {
		t.Fatalf("attachments = %+v", d.Attachments)
	}
	w := httptest.NewRecorder()
	if _, err := s.attachment(w, attachmentReq(m, d.Attachments[0].ID), user); err != nil {
		t.Fatal(err)
	}
	if cd := w.Header().Get("Content-Disposition"); strings.ContainsRune(cd, '‮') || !strings.Contains(cd, "rechnungfdp.exe") {
		t.Fatalf("content-disposition = %q", cd)
	}
}

func TestComposeAttachmentContentType(t *testing.T) {
	s, user, _ := mailTestServer(t)
	req := composeReq{To: []string{"user@example.test"}, Subject: "x", Text: "y"}
	req.Attachments = append(req.Attachments, struct {
		Filename    string `json:"filename"`
		ContentType string `json:"contentType"`
		DataBase64  string `json:"dataBase64"`
	}{"a.txt", "text/plain\r\nBcc: victim@evil.test", base64.StdEncoding.EncodeToString([]byte("hi"))})
	raw, _, _, err := s.buildDraft(httptest.NewRequest("POST", "/", nil), user, &req)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("\r\nBcc:")) {
		t.Fatalf("content type injected a header:\n%s", raw)
	}
}

func TestSaveDraftQuota(t *testing.T) {
	s, user, _ := mailTestServer(t)
	q := int64(10)
	if err := s.Store.UpdateAccount(context.Background(), user.ID, store.AccountPatch{QuotaBytes: &q}); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(composeReq{To: []string{"x@other.test"}, Subject: "Entwurf", Text: "lang genug für mehr als zehn Bytes"})
	r := httptest.NewRequest("POST", "/api/drafts", bytes.NewReader(body))
	_, err := s.saveDraft(httptest.NewRecorder(), r, user)
	var ae *apiError
	if !errors.As(err, &ae) || ae.Status != 507 {
		t.Fatalf("expected quota error, got %v", err)
	}
	r = httptest.NewRequest("POST", "/api/send", bytes.NewReader(body))
	if _, err := s.send(httptest.NewRecorder(), r, user); !errors.As(err, &ae) || ae.Status != 507 {
		t.Fatalf("expected quota error on send (sent copy), got %v", err)
	}
}
