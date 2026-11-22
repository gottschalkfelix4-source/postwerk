// Package ldapdir looks up and authenticates users in an LDAP directory.
//
// Passwords are verified the classic way: the service account searches the
// user by email address, then Postwerk binds as the found DN with the given
// password. The password never appears in a search filter, and every value
// put into a filter is escaped.
package ldapdir

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-ldap/ldap/v3"

	"postwerk/internal/store"
)

var (
	// ErrNotFound: no (unique) directory entry for the address.
	ErrNotFound = errors.New("ldap: user not found")
	// ErrInvalidCredentials: the user exists but the password is wrong.
	ErrInvalidCredentials = errors.New("ldap: invalid credentials")
	// ErrAmbiguous: the user filter matches several entries.
	ErrAmbiguous = errors.New("ldap: user filter matches more than one entry")
)

// Timeout bounds connecting and every single LDAP operation.
const Timeout = 10 * time.Second

// MaxList bounds the number of users returned by List.
const MaxList = 10000

// Entry is a directory user mapped to Postwerk fields.
type Entry struct {
	DN    string `json:"dn"`
	Email string `json:"email"`
	Name  string `json:"name"`
	// QuotaBytes is only meaningful if HasQuota is set.
	QuotaBytes int64 `json:"quotaBytes"`
	HasQuota   bool  `json:"hasQuota"`
	// Admin reports whether the entry matches the admin filter (always
	// false without a filter).
	Admin bool `json:"admin"`
	// Attributes holds all readable attributes (only for Lookup with
	// allAttributes; secrets and binary values are left out).
	Attributes map[string][]string `json:"attributes,omitempty"`
}

// Directory is the interface used by the authenticator (replaceable in tests).
type Directory interface {
	// Authenticate finds the user by email and binds as that user.
	Authenticate(ctx context.Context, cfg store.LDAPSettings, email, password string) (*Entry, error)
	// Lookup finds the user by email with the service account.
	Lookup(ctx context.Context, cfg store.LDAPSettings, email string, allAttributes bool) (*Entry, error)
	// List returns all users matching the user filter that have a mail attribute.
	List(ctx context.Context, cfg store.LDAPSettings) ([]Entry, error)
	// Ping connects and binds with the service account.
	Ping(ctx context.Context, cfg store.LDAPSettings) error
}

// Client implements Directory with github.com/go-ldap/ldap/v3. It opens one
// connection per operation; logins are rare enough that no pool is needed.
type Client struct{}

// New returns a directory client.
func New() *Client { return &Client{} }

// Validate checks the settings for obvious mistakes.
func Validate(cfg store.LDAPSettings) error {
	u, err := url.Parse(strings.TrimSpace(cfg.URL))
	if err != nil || u.Host == "" || (u.Scheme != "ldap" && u.Scheme != "ldaps") {
		return errors.New("Die URL muss mit ldap:// oder ldaps:// beginnen und einen Server enthalten")
	}
	if cfg.StartTLS && u.Scheme == "ldaps" {
		return errors.New("StartTLS ist nur mit ldap:// sinnvoll – ldaps:// ist bereits verschlüsselt")
	}
	// Every web, IMAP and SMTP login sends the user's password to the
	// directory; unencrypted only where the traffic stays on this host or
	// in the Docker network.
	if u.Scheme == "ldap" && !cfg.StartTLS && !localHost(u.Hostname()) {
		return errors.New("Unverschlüsseltes ldap:// würde Passwörter im Klartext übertragen – bitte ldaps:// oder StartTLS verwenden (ohne Verschlüsselung nur für localhost oder einen Docker-Dienstnamen)")
	}
	if strings.TrimSpace(cfg.BaseDN) == "" {
		return errors.New("Bitte eine Basis-DN angeben")
	}
	if strings.TrimSpace(cfg.MailAttribute) == "" {
		return errors.New("Bitte das Attribut für die E-Mail-Adresse angeben")
	}
	if !strings.Contains(cfg.UserFilter, "%s") {
		return errors.New("Der Benutzerfilter muss %s als Platzhalter für die E-Mail-Adresse enthalten")
	}
	if _, err := ldap.CompileFilter(userFilter(cfg, "test@example.org")); err != nil {
		return fmt.Errorf("Der Benutzerfilter ist ungültig: %v", err)
	}
	if f := normFilter(cfg.AdminFilter); f != "" {
		if _, err := ldap.CompileFilter(f); err != nil {
			return fmt.Errorf("Der Admin-Filter ist ungültig: %v", err)
		}
	}
	return nil
}

// localHost reports whether host is a loopback address, localhost or a
// single-label name such as a Docker service ("lldap").
func localHost(host string) bool {
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return host == "localhost" || !strings.Contains(host, ".")
}

// userFilter puts the escaped address into the configured filter.
func userFilter(cfg store.LDAPSettings, email string) string {
	return strings.ReplaceAll(strings.TrimSpace(cfg.UserFilter), "%s", ldap.EscapeFilter(email))
}

// normFilter accepts filters with or without surrounding parentheses.
func normFilter(f string) string {
	f = strings.TrimSpace(f)
	if f != "" && !strings.HasPrefix(f, "(") {
		f = "(" + f + ")"
	}
	return f
}

// connect dials the server, applies timeouts and StartTLS and binds with the
// service account. The connection is closed when ctx ends.
func (c *Client) connect(ctx context.Context, cfg store.LDAPSettings) (*ldap.Conn, func(), error) {
	raw := strings.TrimSpace(cfg.URL)
	u, err := url.Parse(raw)
	if err != nil {
		return nil, nil, err
	}
	tc := &tls.Config{ServerName: u.Hostname(), InsecureSkipVerify: cfg.SkipTLSVerify, MinVersion: tls.VersionTLS12} //nolint:gosec // opt-in, warned in the UI
	conn, err := ldap.DialURL(raw, ldap.DialWithDialer(&net.Dialer{Timeout: Timeout}), ldap.DialWithTLSConfig(tc))
	if err != nil {
		return nil, nil, fmt.Errorf("connect: %w", err)
	}
	conn.SetTimeout(Timeout)
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	done := func() {
		stop()
		conn.Close()
	}
	if cfg.StartTLS && u.Scheme == "ldap" {
		if err := conn.StartTLS(tc); err != nil {
			done()
			return nil, nil, fmt.Errorf("starttls: %w", err)
		}
	}
	if err := serviceBind(conn, cfg); err != nil {
		done()
		return nil, nil, err
	}
	return conn, done, nil
}

func serviceBind(conn *ldap.Conn, cfg store.LDAPSettings) error {
	var err error
	if strings.TrimSpace(cfg.BindDN) == "" {
		err = conn.UnauthenticatedBind("")
	} else {
		err = conn.Bind(strings.TrimSpace(cfg.BindDN), cfg.BindPassword)
	}
	if err != nil {
		return fmt.Errorf("service bind: %w", err)
	}
	return nil
}

func (c *Client) Authenticate(ctx context.Context, cfg store.LDAPSettings, email, password string) (*Entry, error) {
	conn, done, err := c.connect(ctx, cfg)
	if err != nil {
		return nil, err
	}
	defer done()
	e, err := find(conn, cfg, email, false)
	if err != nil {
		return nil, err
	}
	if password == "" {
		// An empty password would be an "unauthenticated bind", which many
		// servers accept without checking anything.
		return nil, ErrInvalidCredentials
	}
	if err := conn.Bind(e.DN, password); err != nil {
		if ldap.IsErrorWithCode(err, ldap.LDAPResultInvalidCredentials) {
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("user bind: %w", err)
	}
	return e, nil
}

func (c *Client) Ping(ctx context.Context, cfg store.LDAPSettings) error {
	_, done, err := c.connect(ctx, cfg)
	if err != nil {
		return err
	}
	done()
	return nil
}

func (c *Client) Lookup(ctx context.Context, cfg store.LDAPSettings, email string, allAttributes bool) (*Entry, error) {
	conn, done, err := c.connect(ctx, cfg)
	if err != nil {
		return nil, err
	}
	defer done()
	return find(conn, cfg, email, allAttributes)
}

func (c *Client) List(ctx context.Context, cfg store.LDAPSettings) ([]Entry, error) {
	conn, done, err := c.connect(ctx, cfg)
	if err != nil {
		return nil, err
	}
	defer done()
	mailAttr := strings.TrimSpace(cfg.MailAttribute)
	// "(mail=%s)" becomes "(mail=*)": every user of the filter.
	filter := "(&" + strings.ReplaceAll(strings.TrimSpace(cfg.UserFilter), "%s", "*") + "(" + ldap.EscapeFilter(mailAttr) + "=*))"
	req := ldap.NewSearchRequest(strings.TrimSpace(cfg.BaseDN), ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, int(Timeout/time.Second), false,
		filter, attributeList(cfg, false), nil)
	res, err := conn.SearchWithPaging(req, 500)
	if err != nil && (res == nil || len(res.Entries) == 0) {
		return nil, fmt.Errorf("search: %w", err)
	}
	out := []Entry{}
	for _, le := range res.Entries {
		for _, mail := range le.GetAttributeValues(mailAttr) {
			mail = store.Normalize(mail)
			if mail == "" || !strings.Contains(mail, "@") {
				continue
			}
			e := mapEntry(cfg, le, mail)
			out = append(out, e)
			break
		}
		if len(out) >= MaxList {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Email < out[j].Email })
	if f := normFilter(cfg.AdminFilter); f != "" {
		for i := range out {
			out[i].Admin, _ = matches(conn, out[i].DN, f)
		}
	}
	return out, nil
}

// find searches exactly one user whose mail attribute equals email.
func find(conn *ldap.Conn, cfg store.LDAPSettings, email string, allAttributes bool) (*Entry, error) {
	email = store.Normalize(email)
	if email == "" {
		return nil, ErrNotFound
	}
	req := ldap.NewSearchRequest(strings.TrimSpace(cfg.BaseDN), ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 2, int(Timeout/time.Second), false,
		userFilter(cfg, email), attributeList(cfg, allAttributes), nil)
	res, err := conn.Search(req)
	if err != nil {
		if ldap.IsErrorWithCode(err, ldap.LDAPResultSizeLimitExceeded) {
			return nil, ErrAmbiguous
		}
		if ldap.IsErrorWithCode(err, ldap.LDAPResultNoSuchObject) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("search: %w", err)
	}
	switch len(res.Entries) {
	case 0:
		return nil, ErrNotFound
	case 1:
	default:
		return nil, ErrAmbiguous
	}
	le := res.Entries[0]
	// The filter may match on other attributes (uid, ...); the account is
	// only this user's if the address is one of its mail values.
	found := false
	for _, v := range le.GetAttributeValues(strings.TrimSpace(cfg.MailAttribute)) {
		if store.Normalize(v) == email {
			found = true
			break
		}
	}
	if !found {
		return nil, ErrNotFound
	}
	e := mapEntry(cfg, le, email)
	if allAttributes {
		e.Attributes = readableAttributes(le)
	}
	if f := normFilter(cfg.AdminFilter); f != "" {
		admin, err := matches(conn, e.DN, f)
		if err != nil {
			return nil, fmt.Errorf("admin filter: %w", err)
		}
		e.Admin = admin
	}
	return &e, nil
}

// matches evaluates filter against the entry dn (base-object search).
func matches(conn *ldap.Conn, dn, filter string) (bool, error) {
	req := ldap.NewSearchRequest(dn, ldap.ScopeBaseObject, ldap.NeverDerefAliases, 1, int(Timeout/time.Second), false,
		filter, []string{"1.1"}, nil)
	res, err := conn.Search(req)
	if err != nil {
		if ldap.IsErrorWithCode(err, ldap.LDAPResultNoSuchObject) {
			return false, nil
		}
		return false, err
	}
	return len(res.Entries) > 0, nil
}

func attributeList(cfg store.LDAPSettings, all bool) []string {
	attrs := []string{}
	if all {
		attrs = append(attrs, "*")
	}
	for _, a := range []string{cfg.MailAttribute, cfg.NameAttribute, cfg.QuotaAttribute} {
		if a = strings.TrimSpace(a); a != "" {
			attrs = append(attrs, a)
		}
	}
	return attrs
}

func mapEntry(cfg store.LDAPSettings, le *ldap.Entry, email string) Entry {
	e := Entry{DN: le.DN, Email: email}
	if a := strings.TrimSpace(cfg.NameAttribute); a != "" {
		e.Name = strings.TrimSpace(le.GetAttributeValue(a))
	}
	if a := strings.TrimSpace(cfg.QuotaAttribute); a != "" {
		if q, ok := ParseQuota(le.GetAttributeValue(a)); ok {
			e.QuotaBytes, e.HasQuota = q, true
		}
	}
	return e
}

// ParseQuota reads a quota value: plain bytes or a number with the suffix
// K, M, G or T (optionally followed by B, binary multiples).
func ParseQuota(v string) (int64, bool) {
	v = strings.ToUpper(strings.TrimSpace(v))
	if v == "" {
		return 0, false
	}
	v = strings.TrimSuffix(v, "B")
	mult := int64(1)
	if n := len(v); n > 0 {
		switch v[n-1] {
		case 'K':
			mult = 1 << 10
		case 'M':
			mult = 1 << 20
		case 'G':
			mult = 1 << 30
		case 'T':
			mult = 1 << 40
		}
		if mult > 1 {
			v = strings.TrimSpace(v[:n-1])
		}
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil || n < 0 || n*float64(mult) > 1<<62 {
		return 0, false
	}
	return int64(n * float64(mult)), true
}

// readableAttributes returns the entry's attributes for display, leaving out
// secrets and binary data.
func readableAttributes(le *ldap.Entry) map[string][]string {
	out := map[string][]string{}
	for _, a := range le.Attributes {
		name := strings.ToLower(a.Name)
		if strings.Contains(name, "password") || strings.Contains(name, "pwd") || name == "unicodepwd" || strings.Contains(name, "secret") {
			continue
		}
		vals := []string{}
		for i, v := range a.Values {
			if i == 20 {
				vals = append(vals, fmt.Sprintf("… (%d weitere)", len(a.Values)-20))
				break
			}
			if !utf8.ValidString(v) {
				vals = append(vals, fmt.Sprintf("<binär, %d Byte>", len(v)))
				continue
			}
			if len(v) > 300 {
				v = strings.ToValidUTF8(v[:300], "") + "…"
			}
			vals = append(vals, v)
		}
		out[a.Name] = vals
	}
	return out
}
