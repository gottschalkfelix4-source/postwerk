package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"postwerk/internal/ldapdir/ldaptest"
	"postwerk/internal/store"
)

type ldapEnv struct {
	a   *Authenticator
	st  *store.Store
	srv *ldaptest.Server
	set *store.SettingsCache
}

func newLDAPEnv(t *testing.T) *ldapEnv {
	t.Helper()
	srv, err := ldaptest.Start(
		ldaptest.Entry{DN: "cn=svc,dc=example,dc=org", Password: "svc"},
		ldaptest.Entry{DN: "uid=lena,ou=people,dc=example,dc=org", Password: "lena-ldap-pw", Attrs: map[string][]string{
			"objectClass": {"person"}, "cn": {"Lena LDAP"}, "mail": {"lena@example.org"}, "quota": {"1G"},
			"memberOf": {"cn=mail-admins,dc=example,dc=org"},
		}},
		ldaptest.Entry{DN: "uid=otto,ou=people,dc=example,dc=org", Password: "otto-ldap-pw", Attrs: map[string][]string{
			"objectClass": {"person"}, "cn": {"Otto"}, "mail": {"otto@other.org"},
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	set, err := st.LoadSettings(ctx, "mail.example.org")
	if err != nil {
		t.Fatal(err)
	}
	cur := set.Get()
	cur.LDAP.Enabled = true
	cur.LDAP.URL = srv.URL()
	cur.LDAP.BindDN, cur.LDAP.BindPassword = "cn=svc,dc=example,dc=org", "svc"
	cur.LDAP.BaseDN = "dc=example,dc=org"
	cur.LDAP.QuotaAttribute = "quota"
	cur.LDAP.AdminFilter = "(memberOf=cn=mail-admins,dc=example,dc=org)"
	cur.LDAP.AutoProvision = true
	if err := set.Save(ctx, cur); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateDomain(ctx, "example.org", "pw1", "key"); err != nil {
		t.Fatal(err)
	}
	g := NewGuard(func() Limits { return Limits{MaxAttempts: 100, Window: time.Minute, BanFor: time.Minute} })
	return &ldapEnv{a: &Authenticator{Store: st, Guard: g, Settings: set}, st: st, srv: srv, set: set}
}

func TestLDAPProvisionAndLogin(t *testing.T) {
	e := newLDAPEnv(t)
	ctx := context.Background()

	acct, err := e.a.Interactive(ctx, "203.0.113.1", "lena@example.org", "lena-ldap-pw", "")
	if err != nil {
		t.Fatal(err)
	}
	if acct.Source != store.SourceLDAP || acct.Role != "admin" || acct.Name != "Lena LDAP" || acct.QuotaBytes != 1<<30 {
		t.Fatalf("provisioned account = %+v", acct)
	}
	if acct.PasswordHash != NoPassword {
		t.Fatal("ldap account must not get a local password")
	}
	if _, err := e.a.Interactive(ctx, "203.0.113.1", "lena@example.org", "falsch", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("wrong password: %v", err)
	}
	// IMAP/SMTP use the same directory password.
	if _, err := e.a.Protocol(ctx, "203.0.113.1", "lena@example.org", "lena-ldap-pw", "imap"); err != nil {
		t.Fatalf("protocol: %v", err)
	}
	// Domain not in Postwerk: no provisioning.
	if _, err := e.a.Interactive(ctx, "203.0.113.1", "otto@other.org", "otto-ldap-pw", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown domain: %v", err)
	}
	if _, err := e.st.GetAccountByEmail(ctx, "otto@other.org"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("account for unknown domain created")
	}
	// Re-authentication for sensitive changes uses the directory too.
	if err := e.a.Reauth(ctx, "203.0.113.1", acct, "lena-ldap-pw", "", false); err != nil {
		t.Fatalf("reauth: %v", err)
	}
}

func TestLDAPAppPasswordFirst(t *testing.T) {
	e := newLDAPEnv(t)
	ctx := context.Background()
	acct, err := e.a.Interactive(ctx, "203.0.113.1", "lena@example.org", "lena-ldap-pw", "")
	if err != nil {
		t.Fatal(err)
	}
	pw := NewAppPassword()
	h, _ := HashPassword(NormalizeAppPassword(pw))
	if _, err := e.st.CreateAppPassword(ctx, acct.ID, "Thunderbird", h); err != nil {
		t.Fatal(err)
	}
	before := len(e.srv.Binds())
	if _, err := e.a.Protocol(ctx, "203.0.113.1", "lena@example.org", pw, "imap"); err != nil {
		t.Fatalf("app password: %v", err)
	}
	if got := len(e.srv.Binds()); got != before {
		t.Fatalf("app password caused %d ldap binds", got-before)
	}
}

func TestLDAPUserRemoved(t *testing.T) {
	e := newLDAPEnv(t)
	ctx := context.Background()
	acct, err := e.a.Interactive(ctx, "203.0.113.1", "lena@example.org", "lena-ldap-pw", "")
	if err != nil {
		t.Fatal(err)
	}
	// The user vanishes from the directory.
	e.srv.SetEntries(ldaptest.Entry{DN: "cn=svc,dc=example,dc=org", Password: "svc"})
	if _, err := e.a.Interactive(ctx, "203.0.113.1", "lena@example.org", "lena-ldap-pw", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("removed user: %v", err)
	}
	got, _ := e.st.GetAccount(ctx, acct.ID)
	if !got.Disabled {
		t.Fatal("account should be disabled after the user left the directory")
	}
}

func TestLDAPUnavailable(t *testing.T) {
	e := newLDAPEnv(t)
	ctx := context.Background()
	if _, err := e.a.Interactive(ctx, "203.0.113.1", "lena@example.org", "lena-ldap-pw", ""); err != nil {
		t.Fatal(err)
	}
	e.srv.Close()
	if _, err := e.a.Interactive(ctx, "203.0.113.1", "lena@example.org", "lena-ldap-pw", ""); !errors.Is(err, ErrDirectoryUnavailable) {
		t.Fatalf("err = %v", err)
	}
	// An outage must not disable the account.
	acct, _ := e.st.GetAccountByEmail(ctx, "lena@example.org")
	if acct.Disabled {
		t.Fatal("account disabled during outage")
	}
}

func TestLocalAccountsUnaffected(t *testing.T) {
	e := newLDAPEnv(t)
	ctx := context.Background()
	h, _ := HashPassword("lokales-passwort")
	if err := e.st.CreateAccount(ctx, &store.Account{Email: "admin@example.org", PasswordHash: h, Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.a.Interactive(ctx, "203.0.113.1", "admin@example.org", "lokales-passwort", ""); err != nil {
		t.Fatalf("local login: %v", err)
	}
	// Disabling LDAP locks out LDAP accounts but not local ones.
	cur := e.set.Get()
	cur.LDAP.Enabled = false
	_ = e.set.Save(ctx, cur)
	if _, err := e.a.Interactive(ctx, "203.0.113.1", "lena@example.org", "lena-ldap-pw", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ldap disabled: %v", err)
	}
	if _, err := e.a.Protocol(ctx, "203.0.113.1", "admin@example.org", "lokales-passwort", "smtp"); err != nil {
		t.Fatalf("local protocol: %v", err)
	}
}

func TestOnlySSO(t *testing.T) {
	e := newLDAPEnv(t)
	ctx := context.Background()
	h, _ := HashPassword("lokales-passwort")
	_ = e.st.CreateAccount(ctx, &store.Account{Email: "admin@example.org", PasswordHash: h, Role: "admin"})
	_ = e.st.CreateAccount(ctx, &store.Account{Email: "user@example.org", PasswordHash: h, Role: "user"})
	cur := e.set.Get()
	cur.OIDC.Enabled, cur.OIDC.OnlySSO = true, true
	_ = e.set.Save(ctx, cur)
	if _, err := e.a.Interactive(ctx, "203.0.113.1", "user@example.org", "lokales-passwort", ""); !errors.Is(err, ErrPasswordLoginDisabled) {
		t.Fatalf("user: %v", err)
	}
	if _, err := e.a.Interactive(ctx, "203.0.113.1", "lena@example.org", "lena-ldap-pw", ""); !errors.Is(err, ErrPasswordLoginDisabled) {
		t.Fatalf("ldap admin: %v", err)
	}
	// Break-glass: local administrators keep password access.
	if _, err := e.a.Interactive(ctx, "203.0.113.1", "admin@example.org", "lokales-passwort", ""); err != nil {
		t.Fatalf("local admin: %v", err)
	}
	// Mail protocols are not affected by "only SSO".
	if _, err := e.a.Protocol(ctx, "203.0.113.1", "user@example.org", "lokales-passwort", "imap"); err != nil {
		t.Fatalf("protocol: %v", err)
	}
}

func TestOIDCAccountHasNoPassword(t *testing.T) {
	e := newLDAPEnv(t)
	ctx := context.Background()
	acct, err := e.a.ProvisionAccount(ctx, "sso@example.org", "SSO", false, 0, store.SourceOIDC)
	if err != nil {
		t.Fatal(err)
	}
	for _, pw := range []string{"", NoPassword, "irgendwas"} {
		if _, err := e.a.Interactive(ctx, "203.0.113.1", acct.Email, pw, ""); !errors.Is(err, ErrInvalid) {
			t.Fatalf("password %q accepted: %v", pw, err)
		}
	}
	app := NewAppPassword()
	h, _ := HashPassword(NormalizeAppPassword(app))
	_, _ = e.st.CreateAppPassword(ctx, acct.ID, "Handy", h)
	if _, err := e.a.Protocol(ctx, "203.0.113.1", acct.Email, app, "imap"); err != nil {
		t.Fatalf("app password: %v", err)
	}
}
