package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Account sources: where the password of an account is verified.
const (
	SourceLocal = "local" // bcrypt hash in Postwerk
	SourceLDAP  = "ldap"  // bind against the LDAP directory
	SourceOIDC  = "oidc"  // no password; web login via the identity provider
)

// Session methods: how a web session was started.
const (
	SessionPassword = "password"
	SessionOIDC     = "oidc"
)

// LDAPSettings connect Postwerk to an LDAP directory (OpenLDAP, Active
// Directory, Authentik/Keycloak LDAP outposts, lldap, ...) as account source.
type LDAPSettings struct {
	Enabled bool `json:"enabled"`
	// URL is ldap://host[:port] or ldaps://host[:port].
	URL           string `json:"url"`
	StartTLS      bool   `json:"startTls"`
	SkipTLSVerify bool   `json:"skipTlsVerify"`
	// BindDN/BindPassword is the service account used to search users
	// (empty BindDN = anonymous search).
	BindDN       string `json:"bindDn"`
	BindPassword string `json:"bindPassword" secret:"true"`
	BaseDN       string `json:"baseDn"`
	// UserFilter finds a user by email address; %s is replaced by the
	// escaped address.
	UserFilter     string `json:"userFilter"`
	MailAttribute  string `json:"mailAttribute"`
	NameAttribute  string `json:"nameAttribute"`
	QuotaAttribute string `json:"quotaAttribute"`
	// AdminFilter (optional) is evaluated against the user entry; matching
	// users get the admin role, e.g. (memberOf=cn=mail-admins,ou=groups,dc=example,dc=org).
	AdminFilter string `json:"adminFilter"`
	// AutoProvision creates the Postwerk account on the first successful
	// login (the domain must exist in Postwerk).
	AutoProvision     bool  `json:"autoProvision"`
	DefaultQuotaBytes int64 `json:"defaultQuotaBytes"`
	// SyncDisabled disables Postwerk accounts whose user vanished from the
	// directory (detected at login and by the sync).
	SyncDisabled bool `json:"syncDisabled"`
}

// OIDCSettings configure single sign-on for the web interface via OpenID
// Connect (Authentik, Keycloak, Authelia, Zitadel, ...).
type OIDCSettings struct {
	Enabled      bool     `json:"enabled"`
	IssuerURL    string   `json:"issuerUrl"`
	ClientID     string   `json:"clientId"`
	ClientSecret string   `json:"clientSecret" secret:"true"`
	Scopes       []string `json:"scopes"`
	ButtonLabel  string   `json:"buttonLabel"`
	EmailClaim   string   `json:"emailClaim"`
	// AllowUnverifiedEmail accepts tokens without email_verified=true.
	AllowUnverifiedEmail bool  `json:"allowUnverifiedEmail"`
	AutoProvision        bool  `json:"autoProvision"`
	DefaultQuotaBytes    int64 `json:"defaultQuotaBytes"`
	// AdminClaim/AdminValue (optional) grant the admin role to SSO-provisioned
	// accounts, e.g. claim "groups" containing "mail-admins".
	AdminClaim string `json:"adminClaim"`
	AdminValue string `json:"adminValue"`
	// OnlySSO disables password logins for the web interface, except for
	// local administrator accounts (break-glass access).
	OnlySSO bool `json:"onlySso"`
	// RPLogout also ends the session at the identity provider on logout
	// (end_session_endpoint).
	RPLogout bool `json:"rpLogout"`
}

const defaultDirectoryQuota = 5 << 30

// applyDirectoryDefaults fills the defaults of the LDAP and OIDC sections.
func applyDirectoryDefaults(s *Settings) {
	s.LDAP = LDAPSettings{
		UserFilter:        "(&(objectClass=person)(mail=%s))",
		MailAttribute:     "mail",
		NameAttribute:     "cn",
		DefaultQuotaBytes: defaultDirectoryQuota,
		SyncDisabled:      true,
	}
	s.OIDC = OIDCSettings{
		Scopes:            []string{"openid", "email", "profile"},
		ButtonLabel:       "Mit Single Sign-on anmelden",
		EmailClaim:        "email",
		DefaultQuotaBytes: defaultDirectoryQuota,
	}
}

// CountLocalAdmins returns the number of enabled administrators with a local
// password (usable as break-glass accounts when only SSO is allowed).
func (s *Store) CountLocalAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts WHERE role = 'admin' AND disabled = 0 AND source = ?`, SourceLocal).Scan(&n)
	return n, err
}

// AccountRef is a short reference to an account.
type AccountRef struct {
	ID       int64  `json:"id"`
	Email    string `json:"email"`
	Disabled bool   `json:"disabled"`
	Source   string `json:"source"`
}

// AccountRefs returns all accounts, optionally limited to one source.
func (s *Store) AccountRefs(ctx context.Context, source string) ([]AccountRef, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, email, disabled, source FROM accounts WHERE ? = '' OR source = ? ORDER BY email`, source, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AccountRef{}
	for rows.Next() {
		var a AccountRef
		if err := rows.Scan(&a.ID, &a.Email, &a.Disabled, &a.Source); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SessionMeta returns how and when a session was started.
func (s *Store) SessionMeta(ctx context.Context, tokenHash string) (method string, created time.Time, err error) {
	var c int64
	err = s.DB.QueryRowContext(ctx, `SELECT method, created_at FROM sessions WHERE token_hash = ?`, tokenHash).Scan(&method, &c)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, ErrNotFound
	}
	if err != nil {
		return "", time.Time{}, err
	}
	return method, ts(c), nil
}
