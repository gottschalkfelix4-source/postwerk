package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"postwerk/internal/delivery"
	"postwerk/internal/message"
	"postwerk/internal/store"
)

type httptestRequest struct {
	r *http.Request
	w *httptest.ResponseRecorder
}

func jsonReq(t *testing.T, method, name string, body any) *httptestRequest {
	t.Helper()
	var b strings.Builder
	if body != nil {
		if err := json.NewEncoder(&b).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, "/api/mail/sieve/x", strings.NewReader(b.String()))
	if name != "" {
		r.SetPathValue("name", name)
	}
	return &httptestRequest{r, httptest.NewRecorder()}
}

func TestSieveAPI(t *testing.T) {
	s, user, _ := mailTestServer(t)

	// Invalid script: 400 with line and column.
	req := jsonReq(t, "PUT", "regeln", map[string]any{"content": "require \"fileinto\";\nif true {\n  fileinto \"A\"\n}", "activate": true})
	out, err := s.putSieve(req.w, req.r, user)
	if err != nil || out != nil || req.w.Code != 400 {
		t.Fatalf("invalid put: %v %v %d", out, err, req.w.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(req.w.Body.Bytes(), &body)
	if body["error"] != "invalid_script" || body["line"].(float64) != 4 || !strings.HasPrefix(body["message"].(string), "Zeile 4, Spalte 1:") {
		t.Fatalf("error body = %v", body)
	}

	// Valid script, activated.
	script := "require \"fileinto\";\nif header :contains \"subject\" \"Rechnung\" { fileinto \"Rechnungen\"; }\n"
	req = jsonReq(t, "PUT", "regeln", map[string]any{"content": script, "activate": true})
	out, err = s.putSieve(req.w, req.r, user)
	if err != nil {
		t.Fatal(err)
	}
	if sc := out.(*store.SieveScript); !sc.Active || sc.Content != script {
		t.Fatalf("put = %+v", sc)
	}
	req = jsonReq(t, "PUT", "zweites", map[string]any{"content": "keep;"})
	if _, err := s.putSieve(req.w, req.r, user); err != nil {
		t.Fatal(err)
	}
	list, err := s.listSieve(nil, jsonReq(t, "GET", "", nil).r, user)
	if err != nil {
		t.Fatal(err)
	}
	l := list.(sieveList)
	if len(l.Scripts) != 2 || l.Active == nil || *l.Active != "regeln" || len(l.Capabilities) == 0 {
		t.Fatalf("list = %+v", l)
	}

	// The active script cannot be deleted.
	req = jsonReq(t, "DELETE", "regeln", nil)
	var ae *apiError
	if _, err := s.deleteSieve(req.w, req.r, user); !errors.As(err, &ae) || ae.Status != 409 {
		t.Fatalf("delete active: %v", err)
	}
	req = jsonReq(t, "POST", "zweites", nil)
	if _, err := s.activateSieve(req.w, req.r, user); err != nil {
		t.Fatal(err)
	}
	req = jsonReq(t, "DELETE", "regeln", nil)
	if _, err := s.deleteSieve(req.w, req.r, user); err != nil {
		t.Fatal(err)
	}
	req = jsonReq(t, "GET", "regeln", nil)
	if _, err := s.getSieve(req.w, req.r, user); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("get deleted: %v", err)
	}
	req = jsonReq(t, "POST", "", nil)
	if _, err := s.deactivateSieve(req.w, req.r, user); err != nil {
		t.Fatal(err)
	}
	req = jsonReq(t, "GET", "bad\nname", nil)
	if _, err := s.getSieve(req.w, req.r, user); !errors.As(err, &ae) || ae.Status != 400 {
		t.Fatalf("invalid name: %v", err)
	}

	// Check endpoint.
	req = jsonReq(t, "POST", "", map[string]any{"content": "keep;"})
	if out, err := s.checkSieve(req.w, req.r, user); err != nil || out == nil {
		t.Fatalf("check: %v", err)
	}
	req = jsonReq(t, "POST", "", map[string]any{"content": "foo;"})
	if out, _ := s.checkSieve(req.w, req.r, user); out != nil || req.w.Code != 400 || !strings.Contains(req.w.Body.String(), "Unbekannter Befehl") {
		t.Fatalf("check invalid: %d %s", req.w.Code, req.w.Body.String())
	}
}

func TestSieveTestEndpoint(t *testing.T) {
	s, user, admin := mailTestServer(t)
	raw := message.Build(message.Draft{From: store.Address{Email: "chef@firma.test"}, To: []store.Address{{Email: user.Email}}, Subject: "Rechnung März", Text: "Hallo"})
	inbox, _ := s.Store.MailboxByRole(t.Context(), user.ID, "inbox")
	m, err := s.Delivery.ImportMessage(t.Context(), user, inbox, raw, delivery.ImportOptions{SkipAI: true})
	if err != nil {
		t.Fatal(err)
	}
	content := `require ["fileinto", "imap4flags"]; if address :is "from" "chef@firma.test" { addflag "\\Flagged"; fileinto "Chef"; }`
	req := jsonReq(t, "POST", "", map[string]any{"content": content, "messageId": m.ID})
	out, err := s.testSieve(req.w, req.r, user)
	if err != nil {
		t.Fatal(err)
	}
	res := out.(sieveTestResult)
	if len(res.Actions) != 1 || res.Actions[0].Type != "fileinto" || res.Actions[0].Mailbox != "Chef" || res.ImplicitKeep || res.Actions[0].Flags[0] != `\Flagged` {
		t.Fatalf("result = %+v", res)
	}
	// Raw message instead of a stored one.
	req = jsonReq(t, "POST", "", map[string]any{"content": `require "reject"; reject "nein";`, "raw": "From: a@b.test\r\nSubject: x\r\n\r\ny"})
	out, err = s.testSieve(req.w, req.r, user)
	if err != nil || out.(sieveTestResult).Actions[0].Type != "reject" {
		t.Fatalf("raw test: %+v %v", out, err)
	}
	// Run-time errors are reported, the message would be kept.
	req = jsonReq(t, "POST", "", map[string]any{"content": `require "reject"; keep; reject "x";`, "raw": "Subject: x\r\n\r\ny"})
	out, err = s.testSieve(req.w, req.r, user)
	if r := out.(sieveTestResult); err != nil || r.Error == "" || !r.ImplicitKeep {
		t.Fatalf("runtime error: %+v %v", out, err)
	}
	// Other users' messages are not accessible.
	req = jsonReq(t, "POST", "", map[string]any{"content": "keep;", "messageId": m.ID})
	if _, err := s.testSieve(req.w, req.r, admin); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("foreign message: %v", err)
	}
	// Without content and without an active script.
	req = jsonReq(t, "POST", "", map[string]any{"messageId": m.ID})
	var ae *apiError
	if _, err := s.testSieve(req.w, req.r, user); !errors.As(err, &ae) || ae.Status != 400 {
		t.Fatalf("no active script: %v", err)
	}
}
