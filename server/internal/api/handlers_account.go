package api

import (
	"errors"
	"net/http"
	"strings"

	"postwerk/internal/auth"
	"postwerk/internal/store"
)

type meResp struct {
	ID          int64  `json:"id"`
	Email       string `json:"email"`
	Name        string `json:"name"`
	Role        string `json:"role"`
	TOTPEnabled bool   `json:"totpEnabled"`
	QuotaBytes  int64  `json:"quotaBytes"`
	UsedBytes   int64  `json:"usedBytes"`
}

func toMe(a *store.Account) meResp {
	return meResp{a.ID, a.Email, a.Name, a.Role, a.TOTPEnabled, a.QuotaBytes, a.UsedBytes}
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
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = s.Store.DeleteSession(r.Context(), auth.HashToken(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	return ok(), nil
}

func (s *Server) me(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	return toMe(u), nil
}

func validPassword(p string) error {
	if len(p) < 10 {
		return invalid("Das Passwort muss mindestens 10 Zeichen lang sein")
	}
	return nil
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if !auth.CheckPassword(u.PasswordHash, req.Current) {
		return nil, invalid("Aktuelles Passwort ist falsch")
	}
	if err := validPassword(req.New); err != nil {
		return nil, err
	}
	h, err := auth.HashPassword(req.New)
	if err != nil {
		return nil, err
	}
	return ok(), s.Store.UpdateAccount(r.Context(), u.ID, store.AccountPatch{PasswordHash: &h})
}

func (s *Server) totpSetup(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	if u.TOTPEnabled {
		return nil, invalid("2FA ist bereits aktiv")
	}
	secret := auth.NewTOTPSecret()
	if err := s.Store.UpdateAccount(r.Context(), u.ID, store.AccountPatch{TOTPSecret: &secret}); err != nil {
		return nil, err
	}
	return map[string]string{"secret": secret, "otpauthUrl": auth.TOTPURL("Postwerk", u.Email, secret)}, nil
}

func (s *Server) totpEnable(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var req struct{ Code string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if u.TOTPSecret == "" || !auth.ValidateTOTP(u.TOTPSecret, req.Code) {
		return nil, invalid("Code ist ungültig")
	}
	t := true
	return ok(), s.Store.UpdateAccount(r.Context(), u.ID, store.AccountPatch{TOTPEnabled: &t})
}

func (s *Server) totpDisable(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var req struct{ Password string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if !auth.CheckPassword(u.PasswordHash, req.Password) {
		return nil, invalid("Passwort ist falsch")
	}
	f, empty := false, ""
	return ok(), s.Store.UpdateAccount(r.Context(), u.ID, store.AccountPatch{TOTPEnabled: &f, TOTPSecret: &empty})
}

func (s *Server) listAppPasswords(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	return s.Store.ListAppPasswords(r.Context(), u.ID)
}

func (s *Server) createAppPassword(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var req struct{ Name string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return nil, invalid("Name fehlt")
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
	tok := auth.RandomToken(32)
	if err := s.Store.CreateSession(r.Context(), auth.HashToken(tok), accountID, sessionTTL); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: tok, Path: "/", HttpOnly: true, Secure: s.secureCookie(r),
		SameSite: http.SameSiteStrictMode, MaxAge: int(sessionTTL.Seconds()),
	})
	return nil
}
