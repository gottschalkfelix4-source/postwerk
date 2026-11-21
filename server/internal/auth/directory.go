package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"postwerk/internal/ldapdir"
	"postwerk/internal/store"
)

var (
	// ErrDirectoryUnavailable: the LDAP directory could not be asked.
	ErrDirectoryUnavailable = errors.New("directory unavailable")
	// ErrPasswordLoginDisabled: only single sign-on is allowed for the web
	// interface and the account is no local administrator.
	ErrPasswordLoginDisabled = errors.New("password login disabled")
)

// NoPassword is stored as password hash of accounts without a local password
// (LDAP and SSO accounts). It is no valid bcrypt hash, so it never matches.
const NoPassword = "!"

var defaultDirectory ldapdir.Directory = ldapdir.New()

func (a *Authenticator) log() *slog.Logger {
	if a.Log != nil {
		return a.Log
	}
	return slog.Default()
}

func (a *Authenticator) directory() ldapdir.Directory {
	if a.LDAP != nil {
		return a.LDAP
	}
	return defaultDirectory
}

// ldapConfig returns the LDAP settings and whether LDAP is active.
func (a *Authenticator) ldapConfig() (store.LDAPSettings, bool) {
	if a.Settings == nil {
		return store.LDAPSettings{}, false
	}
	c := a.Settings.Get().LDAP
	return c, c.Enabled && strings.TrimSpace(c.URL) != ""
}

// PasswordLoginAllowed reports whether acct may sign into the web interface
// with a password. With "only SSO" active, only local administrators can
// (break-glass access when the identity provider is down).
func (a *Authenticator) PasswordLoginAllowed(acct *store.Account) bool {
	if a.Settings == nil {
		return true
	}
	o := a.Settings.Get().OIDC
	if !o.Enabled || !o.OnlySSO {
		return true
	}
	return acct.Source == store.SourceLocal && acct.Role == "admin"
}

// primary verifies the main password of acct (nil for an unknown address).
// Directory users are verified by LDAP bind and, for unknown addresses,
// provisioned if enabled; the returned account may therefore be new.
func (a *Authenticator) primary(ctx context.Context, email, password string, acct *store.Account) (*store.Account, bool, error) {
	if acct == nil {
		if cfg, on := a.ldapConfig(); on && cfg.AutoProvision {
			return a.ldapProvision(ctx, cfg, email, password)
		}
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return nil, false, nil
	}
	switch acct.Source {
	case store.SourceLDAP:
		cfg, on := a.ldapConfig()
		if !on {
			_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
			return acct, false, nil
		}
		return a.ldapVerify(ctx, cfg, acct, password)
	case store.SourceOIDC:
		// SSO accounts have no password; protocols use app passwords.
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return acct, false, nil
	}
	return acct, CheckPassword(acct.PasswordHash, password), nil
}

func (a *Authenticator) ldapVerify(ctx context.Context, cfg store.LDAPSettings, acct *store.Account, password string) (*store.Account, bool, error) {
	e, err := a.directory().Authenticate(ctx, cfg, acct.Email, password)
	switch {
	case errors.Is(err, ldapdir.ErrInvalidCredentials):
		return acct, false, nil
	case errors.Is(err, ldapdir.ErrNotFound):
		if cfg.SyncDisabled && !acct.Disabled {
			t := true
			if err := a.Store.UpdateAccount(ctx, acct.ID, store.AccountPatch{Disabled: &t}); err != nil {
				return acct, false, err
			}
			_ = a.Store.DeleteAccountSessions(ctx, acct.ID)
			acct.Disabled = true
			a.log().Warn("ldap user no longer in directory, account disabled", "component", "auth", "user", acct.Email)
		}
		return acct, false, nil
	case err != nil:
		a.log().Error("ldap authentication failed", "component", "auth", "user", acct.Email, "err", err)
		return acct, false, fmt.Errorf("%w: %v", ErrDirectoryUnavailable, err)
	}
	a.syncFromDirectory(ctx, cfg, acct, e)
	return acct, true, nil
}

// syncFromDirectory copies name, quota and admin role from the directory.
func (a *Authenticator) syncFromDirectory(ctx context.Context, cfg store.LDAPSettings, acct *store.Account, e *ldapdir.Entry) {
	var p store.AccountPatch
	changed := false
	if e.Name != "" && e.Name != acct.Name {
		name := e.Name
		p.Name, acct.Name, changed = &name, name, true
	}
	if e.HasQuota && e.QuotaBytes != acct.QuotaBytes {
		q := e.QuotaBytes
		p.QuotaBytes, acct.QuotaBytes, changed = &q, q, true
	}
	if strings.TrimSpace(cfg.AdminFilter) != "" {
		role := "user"
		if e.Admin {
			role = "admin"
		}
		if role != acct.Role {
			p.Role, acct.Role, changed = &role, role, true
		}
	}
	if !changed {
		return
	}
	if err := a.Store.UpdateAccount(ctx, acct.ID, p); err != nil {
		a.log().Error("updating account from ldap failed", "component", "auth", "user", acct.Email, "err", err)
	}
}

// ldapProvision creates the account of a directory user on the first
// successful login. The domain must already exist in Postwerk.
func (a *Authenticator) ldapProvision(ctx context.Context, cfg store.LDAPSettings, email, password string) (*store.Account, bool, error) {
	domainOK, err := a.Store.DomainExists(ctx, store.DomainOf(email))
	if err != nil {
		return nil, false, err
	}
	if !domainOK {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return nil, false, nil
	}
	if _, err := a.Store.GetAlias(ctx, email); err == nil {
		// The address is an alias; it cannot become an account.
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return nil, false, nil
	}
	e, err := a.directory().Authenticate(ctx, cfg, email, password)
	switch {
	case errors.Is(err, ldapdir.ErrInvalidCredentials), errors.Is(err, ldapdir.ErrNotFound):
		return nil, false, nil
	case err != nil:
		a.log().Error("ldap authentication failed", "component", "auth", "user", email, "err", err)
		return nil, false, fmt.Errorf("%w: %v", ErrDirectoryUnavailable, err)
	}
	acct, err := a.ProvisionAccount(ctx, email, e.Name, e.Admin, quotaFor(e, cfg.DefaultQuotaBytes), store.SourceLDAP)
	if err != nil {
		return nil, false, err
	}
	a.log().Info("account provisioned from ldap", "component", "auth", "user", acct.Email, "role", acct.Role)
	return acct, true, nil
}

func quotaFor(e *ldapdir.Entry, def int64) int64 {
	if e.HasQuota {
		return e.QuotaBytes
	}
	return def
}

// ProvisionAccount creates an account without local password for a
// directory or SSO user. A concurrent creation of the same address returns
// the existing account.
func (a *Authenticator) ProvisionAccount(ctx context.Context, email, name string, admin bool, quota int64, source string) (*store.Account, error) {
	role := "user"
	if admin {
		role = "admin"
	}
	acct := &store.Account{Email: store.Normalize(email), Name: strings.TrimSpace(name), PasswordHash: NoPassword, Role: role,
		QuotaBytes: quota, Source: source}
	if err := a.Store.CreateAccount(ctx, acct); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return a.Store.GetAccountByEmail(ctx, email)
		}
		return nil, err
	}
	return a.Store.GetAccount(ctx, acct.ID)
}

// looksLikeAppPassword reports whether p has the shape of a generated app
// password. For LDAP accounts those are checked first, so a mail client with
// an app password does not cause failed binds (and lockouts) in the directory.
func looksLikeAppPassword(p string) bool {
	n := NormalizeAppPassword(p)
	if len(n) != 16 {
		return false
	}
	for _, c := range n {
		if !strings.ContainsRune(friendlyAlphabet, c) {
			return false
		}
	}
	return true
}

// appPassword checks password against the account's app passwords.
func (a *Authenticator) appPassword(ctx context.Context, acct *store.Account, password string) (bool, error) {
	apps, err := a.Store.ListAppPasswords(ctx, acct.ID)
	if err != nil {
		return false, err
	}
	normalized := NormalizeAppPassword(password)
	for _, p := range apps {
		if CheckPassword(p.Hash, normalized) {
			a.Store.TouchAppPassword(ctx, p.ID)
			return true, nil
		}
	}
	return false, nil
}
