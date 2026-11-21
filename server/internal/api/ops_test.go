package api

import (
	"context"
	"errors"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"postwerk/internal/store"
)

func TestMaskedSecretRestore(t *testing.T) {
	cases := []struct {
		cur, old, ep, oldEp string
		want                string
		wantErr             bool
	}{
		{mask, "geheim", "https://dav.example.com/x", "https://dav.example.com/x", "geheim", false},
		{mask, "geheim", "https://DAV.example.com/x/ ", "https://dav.example.com/x", "geheim", false},
		{mask, "geheim", "https://evil.example.net/", "https://dav.example.com/x", "", true},
		{mask, "", "https://evil.example.net/", "https://dav.example.com/x", "", false}, // nothing stored, nothing leaks
		{"neu", "geheim", "https://evil.example.net/", "https://dav.example.com/x", "neu", false},
		{mask, "geheim", "", "", "geheim", false}, // secret without endpoint (backup password)
	}
	for i, c := range cases {
		v := c.cur
		err := maskedSecret{&v, c.old, c.ep, c.oldEp, "geändert"}.restore()
		if (err != nil) != c.wantErr {
			t.Errorf("case %d: err = %v", i, err)
			continue
		}
		if err == nil && v != c.want {
			t.Errorf("case %d: value = %q, want %q", i, v, c.want)
		}
		if err != nil {
			var ae *apiError
			if !errors.As(err, &ae) || ae.Status != 400 {
				t.Errorf("case %d: want 400, got %v", i, err)
			}
		}
	}
}

func TestValidateBackupPassword(t *testing.T) {
	base := func() store.BackupSettings {
		b := store.DefaultSettings("mail.example.test").Backup
		return b
	}
	b := base()
	b.Encrypt, b.Password = true, "kurz12345"
	old := base()
	if err := validateBackup(&b, &old); err == nil {
		t.Fatal("short password accepted")
	}
	b.Password = "zwölf-zeichen"
	if err := validateBackup(&b, &old); err != nil {
		t.Fatalf("12 characters rejected: %v", err)
	}
	// An existing short password keeps working until it is changed.
	b.Password = "alt-kurz"
	old.Encrypt, old.Password = true, "alt-kurz"
	if err := validateBackup(&b, &old); err != nil {
		t.Fatalf("unchanged legacy password rejected: %v", err)
	}
	b.Password = ""
	if err := validateBackup(&b, &old); err == nil {
		t.Fatal("empty password accepted")
	}
}

func TestImportSerializedPerAccount(t *testing.T) {
	s := &Server{}
	acct := &store.Account{ID: 4711, Role: "user"}
	runningImports.Store(acct.ID, struct{}{})
	defer runningImports.Delete(acct.ID)
	req := httptest.NewRequest("POST", "/api/mail/import", strings.NewReader(""))
	_, err := s.importFile(httptest.NewRecorder(), req, acct)
	var ae *apiError
	if !errors.As(err, &ae) || ae.Status != 409 {
		t.Fatalf("second import: %v", err)
	}
}

func TestDNSBLAndPTRChecks(t *testing.T) {
	n := &netChecks{
		lookupIPs: func(_ context.Context, host string) ([]string, error) {
			return []string{"192.0.2.10", "10.0.0.5", "2001:db8::1", "198.51.100.7"}, nil
		},
		lookupA: func(_ context.Context, name string) ([]string, error) {
			switch name {
			case "10.2.0.192.zen.spamhaus.org":
				return []string{"127.0.0.4"}, nil
			case "7.100.51.198.zen.spamhaus.org":
				return []string{"127.255.255.254"}, nil // public resolver blocked: not a listing
			case "1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2.b.barracudacentral.org":
				return []string{"127.0.0.2"}, nil // IPv6 listing (RFC 5782)
			}
			return nil, &net.DNSError{IsNotFound: true}
		},
		lookupPTR: func(_ context.Context, ip string) ([]string, error) {
			if ip == "192.0.2.10" {
				return []string{"mail.example.test."}, nil
			}
			return []string{"static.provider.test."}, nil
		},
	}
	ctx := context.Background()
	ips := publicIPs(ctx, n.lookupIPs, "mail.example.test")
	if len(ips) != 3 {
		t.Fatalf("public IPs = %v", ips)
	}
	bl := n.checkDNSBL(ctx, ips)
	if len(bl) != 2 || !strings.Contains(bl[0].Title, "192.0.2.10") || bl[0].Severity != "error" ||
		!strings.Contains(bl[1].Title, "2001:db8::1") || !strings.Contains(bl[1].Title, "barracudacentral") {
		t.Fatalf("dnsbl = %+v", bl)
	}
	ptr := n.checkPTR(ctx, "mail.example.test", onlyIPv4(ips))
	if len(ptr) != 1 || !strings.Contains(ptr[0].Title, "198.51.100.7") || !strings.Contains(ptr[0].Detail, "static.provider.test") {
		t.Fatalf("ptr = %+v", ptr)
	}

	// snapshot refreshes in the background and caches the results.
	if w := n.snapshot("mail.example.test"); len(w) != 0 {
		t.Fatalf("first snapshot should be empty while refreshing: %v", w)
	}
	deadline := time.Now().Add(5 * time.Second)
	var w []dashWarning
	for time.Now().Before(deadline) {
		if w = n.snapshot("mail.example.test"); len(w) == 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(w) != 3 {
		t.Fatalf("cached warnings = %+v", w)
	}
}

func TestBackupInterval(t *testing.T) {
	b := store.DefaultSettings("x").Backup
	if backupInterval(b) != 0 {
		t.Error("disabled schedule must have no interval")
	}
	b.Schedule.Enabled = true
	for f, want := range map[string]time.Duration{"hourly": time.Hour, "daily": 24 * time.Hour, "weekly": 7 * 24 * time.Hour} {
		b.Schedule.Frequency = f
		if got := backupInterval(b); got != want {
			t.Errorf("%s = %v", f, got)
		}
	}
}
