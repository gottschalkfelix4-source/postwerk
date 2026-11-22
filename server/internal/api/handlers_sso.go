package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"postwerk/internal/auth"
	"postwerk/internal/ldapdir"
	"postwerk/internal/sso"
	"postwerk/internal/store"
)

// Single sign-on (OpenID Connect) and LDAP directory endpoints.
//
// Decisions:
//   - SSO logins skip Postwerk's own TOTP: the identity provider is
//     responsible for MFA. TOTP still protects password logins.
//   - Accounts are matched by the verified email address. Only accounts
//     created through SSO (source "oidc") get their admin role from the
//     admin claim; local and LDAP accounts keep their role.
//   - A local account that must change its generated password still has to
//     do so after an SSO login, because that password stays valid for
//     password logins. Accounts without local password never have to.
//   - Accounts without local password (source "oidc") confirm sensitive
//     changes (app passwords, ...) by a fresh SSO login instead of a password.

const (
	oidcBindingCookie = "pw_oidc"
	oidcCookiePath    = "/api/auth/oidc"
	oidcCallbackPath  = "/api/auth/oidc/callback"
	// ssoReauthWindow is how long an SSO login counts as confirmation for
	// sensitive changes of accounts without a password.
	ssoReauthWindow = 15 * time.Minute
	ldapSecretMsg   = "LDAP: Die Server-URL wurde geändert – bitte das Bind-Passwort erneut eingeben."
	oidcSecretMsg   = "SSO: Die Issuer-URL wurde geändert – bitte das Client-Secret erneut eingeben."
	defaultSSOLabel = "Mit Single Sign-on anmelden"
)

func (s *Server) ssoManager() *sso.Manager {
	s.ssoOnce.Do(func() {
		if s.SSO == nil {
			s.SSO = sso.New()
		}
	})
	return s.SSO
}

// publicBase returns the external base URL of the web interface: the
// configured public URL or, without one, scheme and host of the request.
func (s *Server) publicBase(r *http.Request) string {
	if u := strings.TrimRight(strings.TrimSpace(s.Settings.Get().Web.PublicURL), "/"); u != "" {
		return u
	}
	scheme := "http"
	if s.resolveClient(r).https {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// ---- public ----

func (s *Server) ssoStatus(w http.ResponseWriter, r *http.Request) (any, error) {
	c := s.Settings.Get().OIDC
	on := sso.Configured(c)
	label := strings.TrimSpace(c.ButtonLabel)
	if label == "" {
		label = defaultSSOLabel
	}
	return map[string]any{"enabled": on, "label": label, "onlySso": on && c.OnlySSO}, nil
}

func (s *Server) setBindingCookie(w http.ResponseWriter, r *http.Request, v string, maxAge int) {
	// Lax: the callback is a top-level navigation coming from the identity provider.
	http.SetCookie(w, &http.Cookie{Name: oidcBindingCookie, Value: v, Path: oidcCookiePath, MaxAge: maxAge, HttpOnly: true,
		Secure: s.secureCookie(r), SameSite: http.SameSiteLaxMode})
}

// ssoFail ends a failed SSO attempt on the login page with an error code.
func (s *Server) ssoFail(w http.ResponseWriter, r *http.Request, code string) {
	s.setBindingCookie(w, r, "", -1)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/login?sso_error="+url.QueryEscape(code), http.StatusFound)
}

func (s *Server) oidcStart(w http.ResponseWriter, r *http.Request) {
	if s.Auth.Guard.Banned(s.clientIP(r)) {
		s.ssoFail(w, r, "rate_limited")
		return
	}
	cfg := s.Settings.Get().OIDC
	if !sso.Configured(cfg) {
		s.ssoFail(w, r, "sso_disabled")
		return
	}
	authURL, binding, err := s.ssoManager().Begin(r.Context(), cfg, s.publicBase(r)+oidcCallbackPath)
	if err != nil {
		s.Log.Error("sso login could not be started", "component", "auth", "err", err)
		s.ssoFail(w, r, "unavailable")
		return
	}
	s.setBindingCookie(w, r, binding, int(sso.StateTTL.Seconds()))
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, authURL, http.StatusFound)
}

func (s *Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if s.Auth.Guard.Banned(ip) {
		s.ssoFail(w, r, "rate_limited")
		return
	}
	q := r.URL.Query()
	cfg := s.Settings.Get().OIDC
	if e := q.Get("error"); e != "" {
		s.Log.Warn("sso login denied by identity provider", "component", "auth", "ip", ip, "error", e, "description", q.Get("error_description"))
		s.ssoFail(w, r, "denied")
		return
	}
	binding := ""
	if c, err := r.Cookie(oidcBindingCookie); err == nil {
		binding = c.Value
	}
	id, err := s.ssoManager().Finish(r.Context(), cfg, q.Get("state"), q.Get("code"), binding)
	if err != nil {
		code := "failed"
		switch {
		case errors.Is(err, sso.ErrNotConfigured):
			code = "sso_disabled"
		case errors.Is(err, sso.ErrUnknownState), errors.Is(err, sso.ErrBrowserMismatch), errors.Is(err, sso.ErrConfigChanged):
			code = "state"
		case errors.Is(err, sso.ErrEmailUnverified):
			code = "email_unverified"
		case errors.Is(err, sso.ErrEmailMissing):
			code = "email_missing"
		}
		s.Log.Warn("sso login failed", "component", "auth", "ip", ip, "err", err)
		s.ssoFail(w, r, code)
		return
	}
	acct, code, err := s.ssoAccount(r.Context(), cfg, id)
	if err != nil {
		s.Log.Error("sso account mapping failed", "component", "auth", "user", id.Email, "err", err)
		s.ssoFail(w, r, "failed")
		return
	}
	if code != "" {
		s.Log.Warn("sso login rejected", "component", "auth", "ip", ip, "user", id.Email, "reason", code)
		s.ssoFail(w, r, code)
		return
	}
	if err := s.startSessionVia(w, r, acct.ID, store.SessionOIDC); err != nil {
		s.Log.Error("sso session failed", "component", "auth", "err", err)
		s.ssoFail(w, r, "failed")
		return
	}
	s.Auth.Guard.Success(ip, acct.Email)
	s.setBindingCookie(w, r, "", -1)
	s.Log.Info("web login via sso", "component", "auth", "ip", ip, "user", acct.Email)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/", http.StatusFound)
}

// ssoAccount maps a verified identity to an account, provisioning it if
// allowed. A non-empty code rejects the login.
func (s *Server) ssoAccount(ctx context.Context, cfg store.OIDCSettings, id *sso.Identity) (*store.Account, string, error) {
	acct, err := s.Store.GetAccountByEmail(ctx, id.Email)
	switch {
	case errors.Is(err, store.ErrNotFound):
		if !cfg.AutoProvision {
			return nil, "no_account", nil
		}
		if ok, err := s.Store.DomainExists(ctx, store.DomainOf(id.Email)); err != nil || !ok {
			return nil, "no_account", err
		}
		if _, err := s.Store.GetAlias(ctx, id.Email); err == nil {
			return nil, "no_account", nil
		}
		acct, err = s.Auth.ProvisionAccount(ctx, id.Email, id.Name, id.AdminChecked && id.Admin, cfg.DefaultQuotaBytes, store.SourceOIDC)
		if err != nil {
			return nil, "", err
		}
		s.Log.Info("account provisioned via sso", "component", "auth", "user", acct.Email, "role", acct.Role)
	case err != nil:
		return nil, "", err
	case acct.Source == store.SourceLocal && acct.Role == "admin":
		// Local administrators are the break-glass accounts and sign in with
		// their password (and 2FA); an identity provider that lets someone
		// claim their address must not open them.
		return nil, "local_admin", nil
	}
	if acct.Disabled {
		return nil, "disabled", nil
	}
	if code, err := s.bindSSOSubject(ctx, cfg, acct, id); code != "" || err != nil {
		return nil, code, err
	}
	var p store.AccountPatch
	if acct.Source == store.SourceOIDC && id.AdminChecked {
		role := "user"
		if id.Admin {
			role = "admin"
		}
		if role != acct.Role {
			p.Role = &role
		}
	}
	if acct.Source != store.SourceLocal && acct.MustChangePassword {
		f := false
		p.MustChangePassword = &f
	}
	if acct.Name == "" && id.Name != "" {
		p.Name = &id.Name
	}
	if p != (store.AccountPatch{}) {
		if err := s.Store.UpdateAccount(ctx, acct.ID, p); err != nil {
			return nil, "", err
		}
	}
	return acct, "", nil
}

// bindSSOSubject ties an account to the provider's subject (sub) on its
// first SSO login. Later logins must come from the same subject, so a
// provider user who changes their address to someone else's cannot take
// over that account. A changed issuer (new provider) rebinds.
func (s *Server) bindSSOSubject(ctx context.Context, cfg store.OIDCSettings, acct *store.Account, id *sso.Identity) (string, error) {
	if id.Subject == "" {
		return "failed", nil
	}
	key := fmt.Sprintf("oidc-sub:%d", acct.ID)
	issuer := strings.TrimRight(strings.TrimSpace(cfg.IssuerURL), "/")
	want := issuer + " " + id.Subject
	have, err := s.Store.KV(ctx, key)
	if err != nil {
		return "", err
	}
	if have == want {
		return "", nil
	}
	if have != "" {
		if oldIssuer, _, _ := strings.Cut(have, " "); oldIssuer == issuer {
			return "sso_mismatch", nil
		}
	}
	return "", s.Store.SetKV(ctx, key, want)
}

// reauth confirms the identity of the signed-in user before a sensitive
// change. Accounts without local password confirm by a recent SSO login.
func (s *Server) reauth(r *http.Request, u *store.Account, password, code string, needCode bool) error {
	if u.Source == store.SourceOIDC {
		method, created, err := s.Store.SessionMeta(r.Context(), sessionHash(r))
		if err != nil {
			return errUnauthorized
		}
		if method == store.SessionOIDC && time.Since(created) < ssoReauthWindow {
			return nil
		}
		return errf(403, "sso_reauth_required", "Bitte zur Bestätigung abmelden und erneut per Single Sign-on anmelden – danach ist die Änderung 15 Minuten lang möglich.")
	}
	return reauthError(s.Auth.Reauth(r.Context(), s.clientIP(r), u, password, code, needCode))
}

// externalPassword rejects password changes for accounts whose password is
// not managed by Postwerk.
func externalPassword(u *store.Account) error {
	switch u.Source {
	case store.SourceLDAP:
		return errf(400, "external_password", "Das Passwort dieses Kontos wird im Verzeichnis (LDAP) verwaltet und kann hier nicht geändert werden.")
	case store.SourceOIDC:
		return errf(400, "external_password", "Dieses Konto meldet sich per Single Sign-on an und hat kein Postwerk-Passwort.")
	}
	return nil
}

// ---- admin ----

func (s *Server) ssoInfo(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	n, err := s.Store.CountLocalAdmins(r.Context())
	if err != nil {
		return nil, err
	}
	base := s.publicBase(r)
	return map[string]any{
		"redirectUri":           base + oidcCallbackPath,
		"postLogoutRedirectUri": base + "/login",
		"publicUrlConfigured":   strings.TrimSpace(s.Settings.Get().Web.PublicURL) != "",
		"localAdmins":           n,
	}, nil
}

func (s *Server) oidcTest(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var req struct {
		IssuerURL string `json:"issuerUrl"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	issuer := strings.TrimSpace(req.IssuerURL)
	if issuer == "" {
		issuer = strings.TrimSpace(s.Settings.Get().OIDC.IssuerURL)
	}
	if err := validIssuer(issuer); err != nil {
		return nil, err
	}
	start := time.Now()
	d, err := s.ssoManager().Discover(r.Context(), issuer)
	out := map[string]any{"ok": err == nil, "latencyMs": time.Since(start).Milliseconds()}
	if err != nil {
		out["error"] = err.Error()
	} else {
		out["discovery"] = d
	}
	return out, nil
}

func validIssuer(issuer string) error {
	u, err := url.Parse(issuer)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return invalid("SSO: Bitte eine gültige Issuer-URL angeben (https://…)")
	}
	return nil
}

// ldapConfigFrom returns the saved LDAP settings, overlaid with an unsaved
// draft from the request (the masked bind password is only reused for the
// same server).
func (s *Server) ldapConfigFrom(raw json.RawMessage) (store.LDAPSettings, error) {
	saved := s.Settings.Get().LDAP
	cfg := saved
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return cfg, invalid("Ungültige LDAP-Konfiguration")
		}
		if err := (maskedSecret{&cfg.BindPassword, saved.BindPassword, cfg.URL, saved.URL, ldapSecretMsg}).restore(); err != nil {
			return cfg, err
		}
	}
	if err := ldapdir.Validate(cfg); err != nil {
		return cfg, invalid("LDAP: " + err.Error())
	}
	return cfg, nil
}

func ldapErrorText(err error) string {
	switch {
	case errors.Is(err, ldapdir.ErrNotFound):
		return "Kein Benutzer mit dieser E-Mail-Adresse gefunden – Basis-DN, Benutzerfilter und Mail-Attribut prüfen."
	case errors.Is(err, ldapdir.ErrAmbiguous):
		return "Der Benutzerfilter passt auf mehrere Einträge – er muss genau einen Benutzer finden."
	case strings.Contains(err.Error(), "service bind"):
		return "Anmeldung des Dienstkontos fehlgeschlagen (Bind-DN/Passwort prüfen): " + err.Error()
	}
	return err.Error()
}

func (s *Server) ldapTest(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var req struct {
		Email  string          `json:"email"`
		Config json.RawMessage `json:"config"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	cfg, err := s.ldapConfigFrom(req.Config)
	if err != nil {
		return nil, err
	}
	email := store.Normalize(req.Email)
	start := time.Now()
	out := map[string]any{"ok": false}
	if email == "" {
		// Connection and service bind only.
		err = s.directory().Ping(r.Context(), cfg)
		if err == nil {
			out["ok"] = true
		}
	} else {
		var e *ldapdir.Entry
		e, err = s.directory().Lookup(r.Context(), cfg, email, true)
		if err == nil {
			out["ok"] = true
			out["entry"] = e
			acct, aerr := s.Store.GetAccountByEmail(r.Context(), email)
			if aerr == nil {
				out["account"] = map[string]any{"id": acct.ID, "source": acct.Source}
			}
		}
	}
	out["latencyMs"] = time.Since(start).Milliseconds()
	if err != nil {
		out["error"] = ldapErrorText(err)
	}
	return out, nil
}

func (s *Server) directory() ldapdir.Directory {
	if s.Auth.LDAP != nil {
		return s.Auth.LDAP
	}
	return ldapdir.New()
}

type ldapSyncUser struct {
	Email string `json:"email"`
	Name  string `json:"name"`
	DN    string `json:"dn"`
	Admin bool   `json:"admin"`
	// Status: "exists" (LDAP account), "other_source" (account with local or
	// SSO password), "missing" (can be created), "no_domain" (domain not in
	// Postwerk), "alias" (address is an alias) or "created".
	Status    string `json:"status"`
	AccountID int64  `json:"accountId,omitempty"`
}

func (s *Server) ldapSync(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var req struct {
		Apply bool `json:"apply"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	cfg, err := s.ldapConfigFrom(nil)
	if err != nil {
		return nil, err
	}
	if req.Apply && !cfg.Enabled {
		return nil, invalid("LDAP ist nicht aktiviert")
	}
	entries, err := s.directory().List(r.Context(), cfg)
	if err != nil {
		return nil, errf(502, "ldap_error", ldapErrorText(err))
	}
	refs, err := s.Store.AccountRefs(r.Context(), "")
	if err != nil {
		return nil, err
	}
	byEmail := map[string]store.AccountRef{}
	for _, a := range refs {
		byEmail[a.Email] = a
	}
	domains := map[string]bool{}
	users := []ldapSyncUser{}
	inDir := map[string]bool{}
	created := 0
	for _, e := range entries {
		inDir[e.Email] = true
		su := ldapSyncUser{Email: e.Email, Name: e.Name, DN: e.DN, Admin: e.Admin}
		if a, ok := byEmail[e.Email]; ok {
			su.AccountID = a.ID
			su.Status = "exists"
			if a.Source != store.SourceLDAP {
				su.Status = "other_source"
			}
		} else {
			d := store.DomainOf(e.Email)
			known, seen := domains[d]
			if !seen {
				known, _ = s.Store.DomainExists(r.Context(), d)
				domains[d] = known
			}
			su.Status = "missing"
			if !known {
				su.Status = "no_domain"
			} else if _, err := s.Store.GetAlias(r.Context(), e.Email); err == nil {
				su.Status = "alias"
			}
			if req.Apply && su.Status == "missing" {
				q := cfg.DefaultQuotaBytes
				if e.HasQuota {
					q = e.QuotaBytes
				}
				acct, err := s.Auth.ProvisionAccount(r.Context(), e.Email, e.Name, e.Admin && strings.TrimSpace(cfg.AdminFilter) != "", q, store.SourceLDAP)
				if err != nil {
					return nil, err
				}
				su.Status, su.AccountID = "created", acct.ID
				created++
			}
		}
		users = append(users, su)
	}
	orphans := []store.AccountRef{}
	disabled := 0
	for _, a := range refs {
		if a.Source != store.SourceLDAP || inDir[a.Email] {
			continue
		}
		if req.Apply && cfg.SyncDisabled && !a.Disabled && a.ID != u.ID {
			t := true
			if err := s.Store.UpdateAccount(r.Context(), a.ID, store.AccountPatch{Disabled: &t}); err != nil {
				return nil, err
			}
			_ = s.Store.DeleteAccountSessions(r.Context(), a.ID)
			a.Disabled = true
			disabled++
		}
		orphans = append(orphans, a)
	}
	if req.Apply {
		s.Log.Info("ldap sync applied", "component", "admin", "created", created, "disabled", disabled, "by", u.Email)
	}
	return map[string]any{"users": users, "orphans": orphans, "created": created, "disabled": disabled,
		"truncated": len(entries) >= ldapdir.MaxList}, nil
}

// validateDirectorySettings normalizes and checks the LDAP and OIDC sections.
func (s *Server) validateDirectorySettings(ctx context.Context, c *store.Settings) error {
	l := &c.LDAP
	for _, p := range []*string{&l.URL, &l.BindDN, &l.BaseDN, &l.UserFilter, &l.MailAttribute, &l.NameAttribute, &l.QuotaAttribute, &l.AdminFilter} {
		*p = strings.TrimSpace(*p)
	}
	if l.UserFilter == "" {
		l.UserFilter = "(&(objectClass=person)(mail=%s))"
	}
	if l.MailAttribute == "" {
		l.MailAttribute = "mail"
	}
	if l.DefaultQuotaBytes < 0 || c.OIDC.DefaultQuotaBytes < 0 {
		return invalid("Der Standard-Speicherplatz darf nicht negativ sein")
	}
	if l.Enabled {
		if err := ldapdir.Validate(*l); err != nil {
			return invalid("LDAP: " + err.Error())
		}
	}
	o := &c.OIDC
	for _, p := range []*string{&o.IssuerURL, &o.ClientID, &o.ButtonLabel, &o.EmailClaim, &o.AdminClaim, &o.AdminValue} {
		*p = strings.TrimSpace(*p)
	}
	if o.EmailClaim == "" {
		o.EmailClaim = "email"
	}
	if o.ButtonLabel == "" {
		o.ButtonLabel = defaultSSOLabel
	}
	if len([]rune(o.ButtonLabel)) > 60 {
		return invalid("SSO: Die Beschriftung des Anmelde-Buttons ist zu lang (höchstens 60 Zeichen)")
	}
	scopes := []string{}
	for _, sc := range o.Scopes {
		for _, part := range strings.Fields(sc) {
			scopes = append(scopes, part)
		}
	}
	o.Scopes = scopes
	if o.AdminValue != "" && o.AdminClaim == "" {
		return invalid("SSO: Für den Admin-Wert bitte auch den Claim angeben (z. B. groups)")
	}
	if o.Enabled {
		if err := validIssuer(o.IssuerURL); err != nil {
			return err
		}
		if o.ClientID == "" {
			return invalid("SSO: Bitte die Client-ID angeben")
		}
		if o.OnlySSO {
			n, err := s.Store.CountLocalAdmins(ctx)
			if err != nil {
				return err
			}
			if n == 0 {
				return invalid("SSO: „Nur SSO erlauben“ braucht mindestens ein aktives lokales Administratorkonto als Notzugang.")
			}
		}
	}
	return nil
}

// accountSourceValid reports whether src is a known account source.
func accountSourceValid(src string) bool {
	return src == store.SourceLocal || src == store.SourceLDAP || src == store.SourceOIDC
}

// noPasswordHash is stored for accounts without local password.
const noPasswordHash = auth.NoPassword
