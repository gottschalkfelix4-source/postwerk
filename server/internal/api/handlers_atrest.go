package api

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"postwerk/internal/atrest"
	"postwerk/internal/export"
	"postwerk/internal/store"
	"postwerk/internal/vault"
)

// routesAtRest registers encryption at rest and data export endpoints.
func (s *Server) routesAtRest(m *http.ServeMux) {
	// own account: encrypted mail storage
	m.HandleFunc("GET /api/account/encryption", s.user(s.getMailEncryption))
	m.HandleFunc("POST /api/account/encryption/inspect", s.user(s.inspectEncryptionKey))
	m.HandleFunc("PUT /api/account/encryption", s.user(s.putMailEncryption))
	m.HandleFunc("DELETE /api/account/encryption", s.user(s.deleteMailEncryption))
	m.HandleFunc("POST /api/account/encryption/encrypt-existing", s.user(s.encryptExisting))

	// own account: data export
	m.HandleFunc("GET /api/account/exports", s.user(s.listOwnExports))
	m.HandleFunc("POST /api/account/exports", s.user(s.startOwnExport))
	m.HandleFunc("GET /api/account/exports/{id}/download", s.user(s.downloadOwnExport))

	// admin: exports of any account
	m.HandleFunc("GET /api/admin/accounts/{id}/exports", s.admin(s.listAccountExports))
	m.HandleFunc("POST /api/admin/accounts/{id}/exports", s.admin(s.startAccountExport))
	m.HandleFunc("GET /api/admin/exports/{id}/download", s.admin(s.downloadAdminExport))

	// admin: master key and blob encryption
	m.HandleFunc("GET /api/admin/security/encryption", s.admin(s.encryptionStatus))
	m.HandleFunc("PUT /api/admin/security/encryption/blobs", s.admin(s.putBlobEncryption))
	m.HandleFunc("POST /api/admin/security/encryption/blobs/migrate", s.admin(s.migrateBlobs))
}

// encryptedNotice replaces the empty body of an encrypted message.
const encryptedNotice = "Diese Nachricht ist mit deinem öffentlichen Schlüssel verschlüsselt gespeichert " +
	"(Verschlüsselung im Ruhezustand). Die Weboberfläche kann sie nicht anzeigen – öffne sie in einem " +
	"Mail-Programm mit deinem privaten Schlüssel (z. B. Thunderbird, Apple Mail mit S/MIME, Outlook)."

// markEncrypted flags encrypted messages and explains why there is no body.
func markEncrypted(d *messageDetail, raw []byte) {
	if !atrest.IsEncrypted(raw) {
		return
	}
	d.Encrypted = true
	if strings.TrimSpace(d.TextBody) == "" && d.HTMLBody == nil {
		d.TextBody = encryptedNotice
	}
}

// ---- encrypted mail storage ----

type mailEncryptionResponse struct {
	Config *store.MailEncryption `json:"config"`
	Job    *atrest.Job           `json:"job"`
	// Encrypted counts stored messages per state for the UI.
	Messages int `json:"messages"`
}

func (s *Server) mailEncryptionState(r *http.Request, u *store.Account) (*mailEncryptionResponse, error) {
	out := &mailEncryptionResponse{}
	cfg, err := s.Store.GetMailEncryption(r.Context(), u.ID)
	switch {
	case err == nil:
		out.Config = cfg
	case !errors.Is(err, store.ErrNotFound):
		return nil, err
	}
	if s.AtRest != nil {
		out.Job = s.AtRest.AccountJob(u.ID)
	}
	if refs, err := s.Store.EncryptionCandidates(r.Context(), u.ID); err == nil {
		out.Messages = len(refs)
	}
	return out, nil
}

func (s *Server) getMailEncryption(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	return s.mailEncryptionState(r, u)
}

type encryptionKeyRequest struct {
	Kind        string `json:"kind"`
	PublicKey   string `json:"publicKey"`
	HideSubject bool   `json:"hideSubject"`
}

func parseKeyRequest(req encryptionKeyRequest) (*atrest.KeyInfo, error) {
	info, err := atrest.ParseKey(req.Kind, []byte(strings.TrimSpace(req.PublicKey)))
	if err != nil {
		return nil, invalid(err.Error())
	}
	return info, nil
}

func (s *Server) inspectEncryptionKey(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var req encryptionKeyRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	info, err := parseKeyRequest(req)
	if err != nil {
		return nil, err
	}
	return map[string]any{"key": info, "matchesAccount": keyMatches(info, u)}, nil
}

// keyMatches reports whether the key names the account or one of its aliases.
func keyMatches(info *atrest.KeyInfo, u *store.Account) bool {
	for _, e := range info.Emails {
		if e == u.Email {
			return true
		}
		for _, a := range u.Aliases {
			if e == a {
				return true
			}
		}
	}
	return false
}

func (s *Server) putMailEncryption(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var req encryptionKeyRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.PublicKey) == "" {
		// Only the options change: keep the stored key.
		cur, err := s.Store.GetMailEncryption(r.Context(), u.ID)
		if err != nil {
			return nil, invalid("Bitte einen öffentlichen Schlüssel angeben")
		}
		req.Kind, req.PublicKey = cur.Kind, cur.PublicKey
	}
	info, err := parseKeyRequest(req)
	if err != nil {
		return nil, err
	}
	cfg := &store.MailEncryption{AccountID: u.ID, Kind: req.Kind, PublicKey: strings.TrimSpace(req.PublicKey), Fingerprint: info.Fingerprint,
		Description: info.Description, Algorithm: info.Algorithm, Expires: info.Expires, HideSubject: req.HideSubject}
	if err := s.Store.SetMailEncryption(r.Context(), cfg); err != nil {
		return nil, err
	}
	s.Log.Info("encryption at rest enabled", "component", "atrest", "user", u.Email, "kind", req.Kind, "fingerprint", info.Fingerprint)
	return s.mailEncryptionState(r, u)
}

func (s *Server) deleteMailEncryption(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	if err := s.Store.DeleteMailEncryption(r.Context(), u.ID); err != nil {
		return nil, err
	}
	s.Log.Info("encryption at rest disabled", "component", "atrest", "user", u.Email)
	return s.mailEncryptionState(r, u)
}

func (s *Server) encryptExisting(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	if s.AtRest == nil {
		return nil, errf(503, "unavailable", "Hintergrundaufträge sind nicht verfügbar")
	}
	if _, err := s.Store.GetMailEncryption(r.Context(), u.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, invalid("Zuerst einen öffentlichen Schlüssel hinterlegen")
		}
		return nil, err
	}
	if _, err := s.AtRest.StartEncryptExisting(u.ID); err != nil {
		if errors.Is(err, atrest.ErrBusy) {
			return nil, errf(409, "busy", err.Error())
		}
		return nil, err
	}
	s.Log.Info("encrypting stored messages", "component", "atrest", "user", u.Email)
	return s.mailEncryptionState(r, u)
}

// ---- data export ----

func exportErr(err error) error {
	var rl *export.RateLimitError
	switch {
	case errors.As(err, &rl):
		return errf(429, "rate_limited", rl.Error())
	case errors.Is(err, export.ErrBusy):
		return errf(409, "busy", err.Error())
	}
	return err
}

func (s *Server) listOwnExports(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	return s.Store.ListDataExports(r.Context(), u.ID, u.ID)
}

func (s *Server) startOwnExport(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	if s.Exports == nil {
		return nil, errf(503, "unavailable", "Datenexport ist nicht verfügbar")
	}
	e, err := s.Exports.Start(r.Context(), u.ID, u.ID, false)
	if err != nil {
		return nil, exportErr(err)
	}
	s.Log.Info("data export requested", "component", "export", "user", u.Email, "export", e.ID)
	return e, nil
}

func (s *Server) downloadOwnExport(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	e, err := s.Store.GetDataExport(r.Context(), id)
	if err != nil || e.AccountID != u.ID || e.RequestedBy != u.ID {
		return nil, errNotFound
	}
	return s.serveExport(w, r, e, u.Email)
}

func (s *Server) listAccountExports(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	if _, err := s.Store.GetAccount(r.Context(), id); err != nil {
		return nil, err
	}
	return s.Store.ListDataExports(r.Context(), id, 0)
}

func (s *Server) startAccountExport(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	acct, err := s.Store.GetAccount(r.Context(), id)
	if err != nil {
		return nil, err
	}
	if s.Exports == nil {
		return nil, errf(503, "unavailable", "Datenexport ist nicht verfügbar")
	}
	e, err := s.Exports.Start(r.Context(), id, u.ID, true)
	if err != nil {
		return nil, exportErr(err)
	}
	s.Log.Info("data export requested by admin", "component", "export", "account", acct.Email, "by", u.Email, "export", e.ID)
	return e, nil
}

func (s *Server) downloadAdminExport(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	e, err := s.Store.GetDataExport(r.Context(), id)
	if err != nil {
		return nil, err
	}
	acct, err := s.Store.GetAccount(r.Context(), e.AccountID)
	if err != nil {
		return nil, err
	}
	s.Log.Info("data export downloaded by admin", "component", "export", "account", acct.Email, "by", u.Email, "export", e.ID)
	return s.serveExport(w, r, e, acct.Email)
}

func (s *Server) serveExport(w http.ResponseWriter, r *http.Request, e *store.DataExport, email string) (any, error) {
	if s.Exports == nil {
		return nil, errNotFound
	}
	p, err := s.Exports.Path(e)
	if err != nil {
		return nil, errf(404, "not_found", "Der Export ist nicht (mehr) verfügbar")
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, errNotFound
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	name := fmt.Sprintf("postwerk-export-%s-%s.zip", safeFilePart(email), e.CreatedAt.Format("2006-01-02"))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, name, info.ModTime(), f)
	return nil, nil
}

func safeFilePart(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_' || r == '@' {
			return r
		}
		return '_'
	}, s)
}

// ---- master key and blob encryption ----

type masterKeyStatus struct {
	Source      string `json:"source"` // env | env-file | data-file
	File        string `json:"file,omitempty"`
	InDataDir   bool   `json:"inDataDir"`
	Fingerprint string `json:"fingerprint"`
}

type blobEncryptionStatus struct {
	Enabled bool             `json:"enabled"`
	Stats   *store.BlobStats `json:"stats,omitempty"`
	Job     *atrest.Job      `json:"job"`
}

func (s *Server) encryptionStatus(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	k := s.Store.Vault
	mk := masterKeyStatus{Source: k.Source, InDataDir: k.InDataDir, Fingerprint: k.Fingerprint()}
	if k.Source != vault.SourceEnv {
		mk.File = k.File
	}
	blobs := blobEncryptionStatus{Enabled: s.Store.BlobEncryption()}
	if s.AtRest != nil {
		blobs.Job = s.AtRest.BlobJob()
	}
	if r.URL.Query().Get("stats") == "1" {
		st, err := s.Store.Blobs.Stats(r.Context())
		if err != nil {
			return nil, err
		}
		blobs.Stats = &st
	}
	var accounts int
	_ = s.Store.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM mail_encryption`).Scan(&accounts)
	return map[string]any{"masterKey": mk, "blobs": blobs, "mailEncryptionAccounts": accounts,
		"protectedSecrets": []string{"settings", "totp", "dkim", "srs"}}, nil
}

func (s *Server) putBlobEncryption(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var req struct {
		Enabled bool `json:"enabled"`
		// Migrate converts the existing blobs right away.
		Migrate bool `json:"migrate"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if err := s.Store.SetBlobEncryption(r.Context(), req.Enabled); err != nil {
		return nil, err
	}
	s.Log.Info("blob encryption changed", "component", "atrest", "enabled", req.Enabled, "by", u.Email)
	if req.Migrate {
		if err := s.startBlobMigration(req.Enabled); err != nil {
			return nil, err
		}
	}
	return s.encryptionStatus(w, r, u)
}

func (s *Server) migrateBlobs(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var req struct {
		Encrypt bool `json:"encrypt"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if err := s.startBlobMigration(req.Encrypt); err != nil {
		return nil, err
	}
	s.Log.Info("blob conversion started", "component", "atrest", "encrypt", req.Encrypt, "by", u.Email)
	return s.encryptionStatus(w, r, u)
}

func (s *Server) startBlobMigration(encrypt bool) error {
	if s.AtRest == nil {
		return errf(503, "unavailable", "Hintergrundaufträge sind nicht verfügbar")
	}
	if _, err := s.AtRest.StartBlobMigration(encrypt); err != nil {
		if errors.Is(err, atrest.ErrBusy) {
			return errf(409, "busy", err.Error())
		}
		return err
	}
	return nil
}
