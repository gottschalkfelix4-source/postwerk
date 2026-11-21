package ldapdir

import (
	"context"
	"errors"
	"strings"
	"testing"

	"postwerk/internal/ldapdir/ldaptest"
	"postwerk/internal/store"
)

const (
	serviceDN = "cn=postwerk,ou=services,dc=example,dc=org"
	servicePW = "service-secret"
)

func testServer(t *testing.T) (*ldaptest.Server, store.LDAPSettings) {
	t.Helper()
	srv, err := ldaptest.Start(
		ldaptest.Entry{DN: serviceDN, Password: servicePW, Attrs: map[string][]string{"cn": {"postwerk"}}},
		ldaptest.Entry{DN: "uid=anna,ou=people,dc=example,dc=org", Password: "anna-pw", Attrs: map[string][]string{
			"objectClass": {"person", "inetOrgPerson"}, "uid": {"anna"}, "cn": {"Anna Beispiel"},
			"mail": {"Anna@Example.org", "a.beispiel@example.org"}, "mailQuota": {"2G"}, "userPassword": {"{SSHA}xyz"},
			"memberOf": {"cn=mail-admins,ou=groups,dc=example,dc=org"},
		}},
		ldaptest.Entry{DN: "uid=bob,ou=people,dc=example,dc=org", Password: "bob-pw", Attrs: map[string][]string{
			"objectClass": {"person"}, "uid": {"bob"}, "cn": {"Bob"}, "mail": {"bob@example.org"},
		}},
		ldaptest.Entry{DN: "uid=nomail,ou=people,dc=example,dc=org", Password: "x", Attrs: map[string][]string{
			"objectClass": {"person"}, "uid": {"nomail"}, "cn": {"Ohne Mail"},
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	cfg := store.DefaultSettings("mail.example.org").LDAP
	cfg.Enabled = true
	cfg.URL = srv.URL()
	cfg.BindDN = serviceDN
	cfg.BindPassword = servicePW
	cfg.BaseDN = "dc=example,dc=org"
	cfg.QuotaAttribute = "mailQuota"
	cfg.AdminFilter = "memberOf=cn=mail-admins,ou=groups,dc=example,dc=org"
	return srv, cfg
}

func TestAuthenticate(t *testing.T) {
	srv, cfg := testServer(t)
	c := New()
	ctx := context.Background()

	e, err := c.Authenticate(ctx, cfg, "anna@example.org", "anna-pw")
	if err != nil {
		t.Fatal(err)
	}
	if e.DN != "uid=anna,ou=people,dc=example,dc=org" || e.Name != "Anna Beispiel" || !e.Admin {
		t.Fatalf("unexpected entry %+v", e)
	}
	if !e.HasQuota || e.QuotaBytes != 2<<30 {
		t.Fatalf("quota = %d (%v)", e.QuotaBytes, e.HasQuota)
	}
	// A second mail value works as well.
	if _, err := c.Authenticate(ctx, cfg, "a.beispiel@example.org", "anna-pw"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Authenticate(ctx, cfg, "anna@example.org", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password: %v", err)
	}
	if _, err := c.Authenticate(ctx, cfg, "anna@example.org", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("empty password: %v", err)
	}
	if _, err := c.Authenticate(ctx, cfg, "nobody@example.org", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown user: %v", err)
	}
	b, err := c.Authenticate(ctx, cfg, "bob@example.org", "bob-pw")
	if err != nil || b.Admin || b.HasQuota {
		t.Fatalf("bob: %+v %v", b, err)
	}

	// Filter input is escaped and passwords never reach a filter.
	if _, err := c.Authenticate(ctx, cfg, "*)(uid=*", "anna-pw"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("injection: %v", err)
	}
	for _, f := range srv.Filters() {
		if strings.Contains(f, "anna-pw") || strings.Contains(f, "bob-pw") {
			t.Fatalf("password in filter %q", f)
		}
		if strings.Contains(f, "(uid=*)") {
			t.Fatalf("unescaped filter %q", f)
		}
	}
}

func TestServiceBindFailure(t *testing.T) {
	_, cfg := testServer(t)
	cfg.BindPassword = "falsch"
	_, err := New().Lookup(context.Background(), cfg, "anna@example.org", false)
	if err == nil || errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "service bind") {
		t.Fatalf("err = %v", err)
	}
}

func TestLookupAttributes(t *testing.T) {
	_, cfg := testServer(t)
	e, err := New().Lookup(context.Background(), cfg, "anna@example.org", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := e.Attributes["userPassword"]; ok {
		t.Fatal("password attribute must not be shown")
	}
	if got := e.Attributes["uid"]; len(got) != 1 || got[0] != "anna" {
		t.Fatalf("uid = %v", got)
	}
}

func TestAmbiguous(t *testing.T) {
	_, cfg := testServer(t)
	cfg.UserFilter = "(|(mail=%s)(objectClass=person))"
	if _, err := New().Lookup(context.Background(), cfg, "bob@example.org", false); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("err = %v", err)
	}
}

func TestList(t *testing.T) {
	_, cfg := testServer(t)
	list, err := New().List(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Email != "anna@example.org" || list[1].Email != "bob@example.org" {
		t.Fatalf("list = %+v", list)
	}
	if !list[0].Admin || list[1].Admin {
		t.Fatalf("admin flags wrong: %+v", list)
	}
}

func TestValidate(t *testing.T) {
	_, cfg := testServer(t)
	if err := Validate(cfg); err != nil {
		t.Fatal(err)
	}
	bad := []func(*store.LDAPSettings){
		func(c *store.LDAPSettings) { c.URL = "http://ldap.example.org" },
		func(c *store.LDAPSettings) { c.URL = "ldaps://ldap.example.org"; c.StartTLS = true },
		func(c *store.LDAPSettings) { c.UserFilter = "(mail=x)" },
		func(c *store.LDAPSettings) { c.UserFilter = "(&(mail=%s)" },
		func(c *store.LDAPSettings) { c.BaseDN = "" },
		func(c *store.LDAPSettings) { c.AdminFilter = "(memberOf=x" },
	}
	for i, f := range bad {
		c := cfg
		f(&c)
		if Validate(c) == nil {
			t.Errorf("case %d accepted", i)
		}
	}
}

func TestParseQuota(t *testing.T) {
	for in, want := range map[string]int64{"1024": 1024, "1K": 1024, "5 MB": 5 << 20, "2g": 2 << 30, "1.5G": 3 << 29} {
		if got, ok := ParseQuota(in); !ok || got != want {
			t.Errorf("%q = %d %v, want %d", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "abc", "-1"} {
		if _, ok := ParseQuota(in); ok {
			t.Errorf("%q accepted", in)
		}
	}
}
