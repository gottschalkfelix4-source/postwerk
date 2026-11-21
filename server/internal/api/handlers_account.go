package api

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"postwerk/internal/auth"
	"postwerk/internal/store"
)

// AdminPasswordFile holds a generated initial admin password (see bootstrap);
// it is removed once that password has been changed.
const AdminPasswordFile = "admin-password.txt"

type meResp struct {
	ID                 int64  `json:"id"`
	Email              string `json:"email"`
	Name               string `json:"name"`
	Role               string `json:"role"`
	TOTPEnabled        bool   `json:"totpEnabled"`
	QuotaBytes         int64  `json:"quotaBytes"`
	UsedBytes          int64  `json:"usedBytes"`
	MustChangePassword bool   `json:"mustChangePassword"`
	// Source: "local", "ldap" or "oidc" (see store.SourceLocal, ...).
	Source string `json:"source"`
}

func toMe(a *store.Account) meResp {
	return meResp{a.ID, a.Email, a.Name, a.Role, a.TOTPEnabled, a.QuotaBytes, a.UsedBytes, a.MustChangePassword, a.Source}
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct{ Email, Password, TOTP string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	ip := s.clientIP(r)
	acct, err := s.Auth.Interactive(r.Context(), ip, req.Email, req.Password, req.TOTP)
	switch {
	case errors.Is(err, auth.ErrTOTPRequired):
		return nil, errf(401, "totp_required", "Bitte Code aus der Authenticator-App eingeben")
	case errors.Is(err, auth.ErrBanned):
		return nil, errf(429, "rate_limited", "Zu viele Fehlversuche. Bitte später erneut versuchen.")
	case errors.Is(err, auth.ErrAccountLocked):
		return nil, errf(403, "forbidden", "Konto ist deaktiviert")
	case errors.Is(err, auth.ErrPasswordLoginDisabled):
		return nil, errf(403, "sso_only", "Die Anmeldung mit Passwort ist deaktiviert – bitte Single Sign-on verwenden.")
	case errors.Is(err, auth.ErrDirectoryUnavailable):
		return nil, errf(503, "directory_unavailable", "Der Verzeichnisdienst (LDAP) ist nicht erreichbar. Bitte später erneut versuchen.")
	case errors.Is(err, auth.ErrInvalid):
		s.Log.Warn("web login failed", "component", "auth", "ip", ip, "user", req.Email)
		return nil, errf(401, "unauthorized", "E-Mail oder Passwort falsch")
	case err != nil:
		return nil, err
	}
	if err := s.startSession(w, r, acct.ID); err != nil {
		return nil, err
	}
	s.Log.Info("web login", "component", "auth", "ip", ip, "user", acct.Email)
	return toMe(acct), nil
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) (any, error) {
	out := map[string]any{"ok": true}
	if h := sessionHash(r); h != "" {
		// After an SSO login, optionally end the session at the identity provider too.
		if method, _, err := s.Store.SessionMeta(r.Context(), h); err == nil && method == store.SessionOIDC {
			if cfg := s.Settings.Get().OIDC; cfg.RPLogout {
				if u, err := s.ssoManager().EndSessionURL(r.Context(), cfg, s.publicBase(r)+"/login"); err == nil && u != "" {
					out["endSessionUrl"] = u
				}
			}
		}
		_ = s.Store.DeleteSession(r.Context(), h)
	}
	s.clearSessionCookie(w, r)
	return out, nil
}

func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		Secure: s.secureCookie(r), SameSite: http.SameSiteStrictMode})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	return toMe(u), nil
}

func validPassword(p string) error {
	if len(p) < 10 {
		return invalid("Das Passwort muss mindestens 10 Zeichen lang sein")
	}
	if len(p) > auth.MaxPasswordBytes {
		return invalid("Das Passwort darf höchstens 72 Byte lang sein (Umlaute zählen doppelt)")
	}
	return nil
}

// reauthError translates a failed re-authentication into an API error.
func reauthError(err error) error {
	switch {
	case errors.Is(err, auth.ErrBanned):
		return errf(429, "rate_limited", "Zu viele Fehlversuche. Bitte später erneut versuchen.")
	case errors.Is(err, auth.ErrInvalid):
		return errf(400, "wrong_password", "Aktuelles Passwort ist falsch")
	case errors.Is(err, auth.ErrTOTPRequired):
		return errf(400, "totp_required", "Bitte Code aus der Authenticator-App oder einen Wiederherstellungscode eingeben")
	case errors.Is(err, auth.ErrInvalidCode):
		return errf(400, "invalid_code", "Code ist ungültig")
	case errors.Is(err, auth.ErrTOTPReused):
		return errf(400, "code_reused", "Dieser Code wurde bereits verwendet – bitte den nächsten Code abwarten")
	case errors.Is(err, auth.ErrDirectoryUnavailable):
		return errf(503, "directory_unavailable", "Der Verzeichnisdienst (LDAP) ist nicht erreichbar. Bitte später erneut versuchen.")
	}
	return err
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var req struct {
		Current            string `json:"current"`
		New                string `json:"new"`
		RevokeAppPasswords bool   `json:"revokeAppPasswords"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if err := externalPassword(u); err != nil {
		return nil, err
	}
	if err := s.Auth.Reauth(r.Context(), s.clientIP(r), u, req.Current, "", false); err != nil {
		return nil, reauthError(err)
	}
	if err := validPassword(req.New); err != nil {
		return nil, err
	}
	if req.New == req.Current {
		return nil, invalid("Das neue Passwort muss sich vom bisherigen unterscheiden")
	}
	h, err := auth.HashPassword(req.New)
	if err != nil {
		return nil, err
	}
	f := false
	if err := s.Store.UpdateAccount(r.Context(), u.ID, store.AccountPatch{PasswordHash: &h, MustChangePassword: &f}); err != nil {
		return nil, err
	}
	if u.MustChangePassword && s.Cfg != nil && s.Cfg.DataDir != "" {
		_ = os.Remove(filepath.Join(s.Cfg.DataDir, AdminPasswordFile))
	}
	if req.RevokeAppPasswords {
		if err := s.Store.DeleteAppPasswords(r.Context(), u.ID); err != nil {
			return nil, err
		}
	}
	// End every session (including stolen ones) and continue with a new one.
	if err := s.reissueSession(w, r, u.ID); err != nil {
		return nil, err
	}
	s.Log.Info("password changed", "component", "auth", "user", u.Email, "app_passwords_revoked", req.RevokeAppPasswords)
	return ok(), nil
}

func (s *Server) totpSetup(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	if u.TOTPEnabled {
		return nil, invalid("2FA ist bereits aktiv")
	}
	if u.Source == store.SourceOIDC {
		return nil, invalid("Dieses Konto meldet sich per Single Sign-on an – die Zwei-Faktor-Anmeldung übernimmt der Identitätsanbieter.")
	}
	secret := auth.NewTOTPSecret()
	if err := s.Store.UpdateAccount(r.Context(), u.ID, store.AccountPatch{TOTPSecret: &secret}); err != nil {
		return nil, err
	}
	return map[string]string{"secret": secret, "otpauthUrl": auth.TOTPURL("Postwerk", u.Email, secret)}, nil
}

func (s *Server) totpEnable(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var req struct{ Code, Password string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if u.TOTPEnabled {
		return nil, invalid("2FA ist bereits aktiv")
	}
	if u.Source == store.SourceOIDC {
		return nil, invalid("Dieses Konto meldet sich per Single Sign-on an – die Zwei-Faktor-Anmeldung übernimmt der Identitätsanbieter.")
	}
	if err := s.reauth(r, u, req.Password, "", false); err != nil {
		return nil, err
	}
	if u.TOTPSecret == "" {
		return nil, invalid("Code ist ungültig")
	}
	valid, err := s.Auth.VerifyTOTP(r.Context(), u, req.Code)
	if err != nil {
		return nil, reauthError(err)
	}
	if !valid {
		return nil, invalid("Code ist ungültig")
	}
	codes, hashes := auth.NewRecoveryCodes()
	if err := s.Store.ReplaceRecoveryCodes(r.Context(), u.ID, hashes); err != nil {
		return nil, err
	}
	t := true
	if err := s.Store.UpdateAccount(r.Context(), u.ID, store.AccountPatch{TOTPEnabled: &t}); err != nil {
		return nil, err
	}
	if _, err := s.Store.DeleteOtherSessions(r.Context(), u.ID, sessionHash(r)); err != nil {
		return nil, err
	}
	s.Log.Info("2fa enabled", "component", "auth", "user", u.Email)
	return map[string]any{"ok": true, "recoveryCodes": codes}, nil
}

func (s *Server) totpDisable(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var req struct{ Password, Code string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if !u.TOTPEnabled {
		return nil, invalid("2FA ist nicht aktiv")
	}
	if err := s.reauth(r, u, req.Password, req.Code, true); err != nil {
		return nil, err
	}
	f, empty := false, ""
	if err := s.Store.UpdateAccount(r.Context(), u.ID, store.AccountPatch{TOTPEnabled: &f, TOTPSecret: &empty}); err != nil {
		return nil, err
	}
	if err := s.Store.ReplaceRecoveryCodes(r.Context(), u.ID, nil); err != nil {
		return nil, err
	}
	if _, err := s.Store.DeleteOtherSessions(r.Context(), u.ID, sessionHash(r)); err != nil {
		return nil, err
	}
	s.Log.Info("2fa disabled", "component", "auth", "user", u.Email)
	return ok(), nil
}

func (s *Server) recoveryCodesStatus(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	n, err := s.Store.CountRecoveryCodes(r.Context(), u.ID)
	if err != nil {
		return nil, err
	}
	return map[string]int{"remaining": n}, nil
}

func (s *Server) regenerateRecoveryCodes(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var req struct{ Password, Code string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if !u.TOTPEnabled {
		return nil, invalid("Wiederherstellungscodes gibt es nur mit aktiver Zwei-Faktor-Anmeldung")
	}
	if err := s.reauth(r, u, req.Password, req.Code, true); err != nil {
		return nil, err
	}
	codes, hashes := auth.NewRecoveryCodes()
	if err := s.Store.ReplaceRecoveryCodes(r.Context(), u.ID, hashes); err != nil {
		return nil, err
	}
	s.Log.Info("recovery codes regenerated", "component", "auth", "user", u.Email)
	return map[string]any{"recoveryCodes": codes}, nil
}

func (s *Server) listAppPasswords(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	return s.Store.ListAppPasswords(r.Context(), u.ID)
}

func (s *Server) createAppPassword(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var req struct{ Name, Password, Code string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return nil, invalid("Name fehlt")
	}
	if len(req.Name) > 100 {
		return nil, invalid("Name ist zu lang")
	}
	if err := s.reauth(r, u, req.Password, req.Code, true); err != nil {
		return nil, err
	}
	n, err := s.Store.CountAppPasswords(r.Context(), u.ID)
	if err != nil {
		return nil, err
	}
	if n >= store.MaxAppPasswords {
		return nil, invalid("Es sind höchstens 20 App-Passwörter möglich – bitte zuerst ein altes widerrufen")
	}
	pw := auth.NewAppPassword()
	h, err := auth.HashPassword(auth.NormalizeAppPassword(pw))
	if err != nil {
		return nil, err
	}
	p, err := s.Store.CreateAppPassword(r.Context(), u.ID, req.Name, h)
	if err != nil {
		return nil, err
	}
	return struct {
		store.AppPassword
		Password string `json:"password"`
	}{*p, pw}, nil
}

func (s *Server) deleteAppPassword(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	return ok(), s.Store.DeleteAppPassword(r.Context(), u.ID, id)
}

// ---- sessions ----

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	list, err := s.Store.ListSessions(r.Context(), u.ID)
	if err != nil {
		return nil, err
	}
	cur := store.SessionID(sessionHash(r))
	for i := range list {
		list[i].Current = list[i].ID == cur
	}
	return list, nil
}

func (s *Server) deleteSession(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	id := r.PathValue("id")
	if err := s.Store.DeleteSessionByID(r.Context(), u.ID, id); err != nil {
		return nil, err
	}
	if id == store.SessionID(sessionHash(r)) {
		s.clearSessionCookie(w, r)
	}
	return ok(), nil
}

func (s *Server) logoutOtherSessions(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	n, err := s.Store.DeleteOtherSessions(r.Context(), u.ID, sessionHash(r))
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "ended": n}, nil
}

// ---- vacation ----

func (s *Server) getVacation(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	return s.Store.GetVacation(r.Context(), u.ID)
}

func (s *Server) putVacation(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var v store.Vacation
	if err := decode(r, &v); err != nil {
		return nil, err
	}
	if v.Enabled && strings.TrimSpace(v.Body) == "" {
		return nil, invalid("Bitte einen Text für die Abwesenheitsnotiz angeben")
	}
	if err := s.Store.SetVacation(r.Context(), u.ID, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// startSession creates a session and sets the cookie.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, accountID int64) error {
	return s.startSessionVia(w, r, accountID, store.SessionPassword)
}

// startSessionVia creates a session started by method (password or SSO).
func (s *Server) startSessionVia(w http.ResponseWriter, r *http.Request, accountID int64, method string) error {
	tok := auth.RandomToken(32)
	if err := s.Store.CreateSessionVia(r.Context(), auth.HashToken(tok), accountID, sessionTTL, s.clientIP(r), r.UserAgent(), method); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: tok, Path: "/", HttpOnly: true, Secure: s.secureCookie(r),
		SameSite: http.SameSiteStrictMode, MaxAge: int(sessionTTL.Seconds()),
	})
	return nil
}

// reissueSession ends all sessions of the account and starts a new one for
// the current client.
func (s *Server) reissueSession(w http.ResponseWriter, r *http.Request, accountID int64) error {
	if err := s.Store.DeleteAccountSessions(r.Context(), accountID); err != nil {
		return err
	}
	return s.startSession(w, r, accountID)
}
