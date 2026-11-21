// Package api serves the JSON API, Prometheus metrics and the embedded web UI.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"postwerk/internal/ai"
	"postwerk/internal/auth"
	"postwerk/internal/backup"
	"postwerk/internal/config"
	"postwerk/internal/delivery"
	"postwerk/internal/dyndns"
	"postwerk/internal/httpport"
	"postwerk/internal/logx"
	"postwerk/internal/metrics"
	"postwerk/internal/migrate"
	"postwerk/internal/spam"
	"postwerk/internal/store"
	"postwerk/internal/tlsutil"
)

const sessionCookie = "pw_session"

// sessionTTL is the maximum session lifetime; sessions also end after
// store.SessionIdleTimeout without use.
const sessionTTL = 14 * 24 * time.Hour

// Request body limits.
const (
	smallBodyLimit   = 64 << 10 // login, setup and other unauthenticated endpoints
	defaultBodyLimit = 10 << 20 // ordinary JSON requests
)

type Server struct {
	Cfg        *config.Config
	Store      *store.Store
	Settings   *store.SettingsCache
	Auth       *auth.Authenticator
	Delivery   *delivery.Service
	AI         *ai.Client
	AIWorker   *ai.Worker
	Spam       *spam.Filter
	Logs       *logx.Ring
	Log        *slog.Logger
	UI         fs.FS
	TLS        *tlsutil.Dynamic
	Version    string
	Started    time.Time
	SetupToken string
	Migrations *migrate.Manager
	DynDNS     *dyndns.Updater
	HTTPPort   *httpport.Controller
	Backups    *backup.Manager

	setupMu sync.Mutex
}

type apiError struct {
	Status  int    `json:"-"`
	Code    string `json:"error"`
	Message string `json:"message"`
}

func (e *apiError) Error() string { return e.Message }

func errf(status int, code, msg string) *apiError { return &apiError{status, code, msg} }

var (
	errUnauthorized = errf(401, "unauthorized", "Nicht angemeldet")
	errForbidden    = errf(403, "forbidden", "Keine Berechtigung")
	errNotFound     = errf(404, "not_found", "Nicht gefunden")
)

func invalid(msg string) *apiError { return errf(400, "invalid", msg) }

type handler func(w http.ResponseWriter, r *http.Request) (any, error)
type userHandler func(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error)

func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) writeErr(w http.ResponseWriter, r *http.Request, err error) {
	var ae *apiError
	switch {
	case errors.As(err, &ae):
	case errors.Is(err, store.ErrNotFound):
		ae = errNotFound
	case errors.Is(err, store.ErrConflict):
		ae = errf(409, "conflict", "Existiert bereits oder ist in Verwendung")
	case errors.Is(err, ai.ErrDisabled):
		ae = errf(503, "ai_disabled", "KI-Funktionen sind deaktiviert")
	default:
		// Never leak internal details; the reference ties the response to the log line.
		id := auth.RandomToken(4)
		s.Log.Error("request failed", "component", "api", "path", r.URL.Path, "request_id", id, "err", err)
		ae = errf(500, "internal", "Interner Fehler (Referenz "+id+"). Details stehen im Server-Log.")
	}
	s.writeJSON(w, ae.Status, ae)
}

func (s *Server) wrap(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out, err := h(w, r)
		if err != nil {
			s.writeErr(w, r, err)
			return
		}
		if out == nil {
			return // handler wrote the response itself
		}
		s.writeJSON(w, http.StatusOK, out)
	}
}

// allowedDuringPasswordChange lists the endpoints usable while an account
// must change its password first.
var allowedDuringPasswordChange = map[string]bool{
	"GET /api/auth/me":           true,
	"POST /api/account/password": true,
}

func (s *Server) user(h userHandler) http.HandlerFunc {
	return s.wrap(func(w http.ResponseWriter, r *http.Request) (any, error) {
		u, err := s.currentUser(r)
		if err != nil {
			return nil, err
		}
		if u.MustChangePassword && !allowedDuringPasswordChange[r.Method+" "+r.URL.Path] {
			return nil, errf(403, "password_change_required", "Bitte zuerst ein neues Passwort festlegen")
		}
		return h(w, r, u)
	})
}

func (s *Server) admin(h userHandler) http.HandlerFunc {
	return s.user(func(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
		if u.Role != "admin" {
			return nil, errForbidden
		}
		return h(w, r, u)
	})
}

// sessionHash returns the token hash of the request's session cookie.
func sessionHash(r *http.Request) string {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return ""
	}
	return auth.HashToken(c.Value)
}

func (s *Server) currentUser(r *http.Request) (*store.Account, error) {
	h := sessionHash(r)
	if h == "" {
		return nil, errUnauthorized
	}
	id, err := s.Store.SessionAccount(r.Context(), h)
	if err != nil {
		return nil, errUnauthorized
	}
	u, err := s.Store.GetAccount(r.Context(), id)
	if err != nil || u.Disabled {
		return nil, errUnauthorized
	}
	return u, nil
}

// bodyLimit returns the maximum JSON body size for a request.
func (s *Server) bodyLimit(r *http.Request) int64 {
	p := r.URL.Path
	switch {
	case strings.HasPrefix(p, "/api/auth/"), strings.HasPrefix(p, "/api/setup/"):
		return smallBodyLimit
	case p == "/api/mail/send" || p == "/api/mail/drafts":
		// Attachments travel base64-encoded inside the JSON body.
		if s.Settings != nil {
			if n := s.Settings.Get().Server.MaxMessageSize*4/3 + 1<<20; n > defaultBodyLimit {
				return n
			}
		}
	}
	return defaultBodyLimit
}

// streamingUpload reports endpoints that read large multipart bodies with
// their own limits.
func streamingUpload(r *http.Request) bool {
	return r.URL.Path == "/api/mail/import" || r.URL.Path == "/api/admin/backups/upload"
}

// bodyReadTimeout bounds how long reading a request body may take, so slow
// clients cannot hold connections forever.
func bodyReadTimeout(r *http.Request) time.Duration {
	p := r.URL.Path
	switch {
	case streamingUpload(r):
		return 0
	case p == "/api/mail/send" || p == "/api/mail/drafts":
		return 15 * time.Minute
	case strings.HasPrefix(p, "/api/auth/"), strings.HasPrefix(p, "/api/setup/"):
		return 30 * time.Second
	}
	return 2 * time.Minute
}

// deadlineBody lifts the body read deadline once the body was read
// completely, so the deadline cannot cancel a slow handler afterwards.
type deadlineBody struct {
	io.ReadCloser
	rc   *http.ResponseController
	done bool
}

func (b *deadlineBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && !b.done {
		b.done = true
		_ = b.rc.SetReadDeadline(time.Time{})
	}
	return n, err
}

// decode reads a JSON body. The size limit is applied by the middleware
// (see bodyLimit); the inner limit only guards direct handler calls.
func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 64<<20))
	if err := dec.Decode(v); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return errf(413, "too_large", "Die Anfrage ist zu groß")
		}
		return invalid("Ungültiger JSON-Body")
	}
	return nil
}

func pathID(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil {
		return 0, invalid("Ungültige ID")
	}
	return id, nil
}

func queryInt(r *http.Request, key string, def, max int) int {
	v, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil || v < 0 {
		return def
	}
	if max > 0 && v > max {
		return max
	}
	return v
}

func ok() any { return map[string]bool{"ok": true} }

func (s *Server) clientIP(r *http.Request) string {
	return s.resolveClient(r).client
}

type clientInfo struct {
	remote  string // direct peer
	client  string // effective client after evaluating trusted proxies
	trusted bool   // remote is a trusted proxy
	https   bool   // original request used HTTPS
}

func (s *Server) behindProxy() (bool, []string) {
	w := s.Settings.Get().Web
	return w.BehindProxy || s.Cfg.TrustProxy, w.TrustedProxies
}

// isTrustedProxy decides whether X-Forwarded-* from ip may be believed
// (without an explicit list: loopback only).
func isTrustedProxy(ip net.IP, list []string) bool { return httpport.TrustedProxy(ip, list) }

// resolveClient walks X-Forwarded-For from right to left, skipping trusted
// proxies, so a client cannot spoof its address by sending the header.
func (s *Server) resolveClient(r *http.Request) clientInfo {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	info := clientInfo{remote: host, client: host, https: r.TLS != nil}
	enabled, list := s.behindProxy()
	if !enabled || !isTrustedProxy(net.ParseIP(host), list) {
		return info
	}
	info.trusted = true
	// The header may appear several times; together the lines form one list.
	if vals := r.Header.Values("X-Forwarded-For"); len(vals) > 0 {
		parts := strings.Split(strings.Join(vals, ","), ",")
		for i := len(parts) - 1; i >= 0; i-- {
			ip := strings.TrimSpace(parts[i])
			if net.ParseIP(ip) == nil {
				break
			}
			info.client = ip
			if !isTrustedProxy(net.ParseIP(ip), list) {
				break
			}
		}
	} else if v := r.Header.Get("X-Real-IP"); net.ParseIP(strings.TrimSpace(v)) != nil {
		info.client = strings.TrimSpace(v)
	}
	if httpport.ForwardedHTTPS(r) {
		info.https = true
	}
	return info
}

// secureCookie reports whether the session cookie must carry the Secure flag:
// always once the web interface is only reachable via HTTPS with a valid
// certificate, otherwise when the request itself used HTTPS.
func (s *Server) secureCookie(r *http.Request) bool {
	return s.Cfg.SecureCookies || s.Settings.Get().Web.SecureCookies || s.resolveClient(r).https || s.HTTPPort.ForcesHTTPS()
}

// Handler builds the complete HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.routes(mux)

	mux.HandleFunc("GET /.well-known/mta-sts.txt", s.mtaSTS)
	mux.HandleFunc("GET /mail/config-v1.1.xml", s.autoconfig)
	mux.HandleFunc("GET /.well-known/autoconfig/mail/config-v1.1.xml", s.autoconfig)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	metricsHandler := promhttp.HandlerFor(metrics.Registry, promhttp.HandlerOpts{})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		// Without a token, metrics are only served on the internal health
		// listener (POSTWERK_HEALTH_ADDR).
		t := s.Cfg.MetricsToken
		if t == "" {
			http.NotFound(w, r)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+t)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		metricsHandler.ServeHTTP(w, r)
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		s.writeErr(w, r, errNotFound)
	})
	mux.Handle("/", s.static())

	return s.middleware(mux)
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; "+
			"font-src 'self' data: https://fonts.gstatic.com; img-src 'self' data: blob: https: http:; frame-src 'self' about: blob: data:; "+
			"connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'self'")
		if r.TLS != nil && s.HTTPPort.ValidCert() {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch:
			// Only bodies get a deadline: for body-less requests net/http
			// already watches the connection, and a deadline there would
			// cancel long-running handlers.
			if d := bodyReadTimeout(r); d > 0 && r.Body != nil && r.Body != http.NoBody && r.ContentLength != 0 {
				rc := http.NewResponseController(w)
				if rc.SetReadDeadline(time.Now().Add(d)) == nil {
					r.Body = &deadlineBody{ReadCloser: r.Body, rc: rc}
				}
			}
			if !streamingUpload(r) && r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, s.bodyLimit(r))
			}
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			switch r.Method {
			case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
				if r.Header.Get("X-Postwerk") != "1" {
					s.writeErr(w, r, errf(403, "forbidden", "CSRF-Header fehlt"))
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) static() http.Handler {
	fileServer := http.FileServer(http.FS(s.UI))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		if f, err := s.UI.Open(p); err == nil {
			st, _ := f.Stat()
			f.Close()
			if st != nil && !st.IsDir() {
				if strings.HasPrefix(p, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		// SPA fallback.
		idx, err := fs.ReadFile(s.UI, "index.html")
		if err != nil {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("Postwerk: Web-Oberfläche wurde nicht mitgebaut (web/ mit npm run build bauen)."))
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(idx)
	})
}

func (s *Server) routes(m *http.ServeMux) {
	// auth
	m.HandleFunc("POST /api/auth/login", s.wrap(s.login))
	m.HandleFunc("POST /api/auth/logout", s.wrap(s.logout))
	m.HandleFunc("GET /api/auth/me", s.user(s.me))

	// setup
	m.HandleFunc("GET /api/setup/status", s.wrap(s.setupStatus))
	m.HandleFunc("POST /api/setup/initialize", s.wrap(s.setupInitialize))
	m.HandleFunc("POST /api/admin/setup/complete", s.admin(s.setupComplete))
	m.HandleFunc("GET /api/admin/system/check", s.admin(s.systemCheck))
	m.HandleFunc("POST /api/admin/system/test-clamav", s.admin(s.testClamAV))
	m.HandleFunc("GET /api/admin/domains/{name}/dns/check", s.admin(s.domainDNSCheck))

	// reverse proxy & tls
	m.HandleFunc("GET /api/admin/proxy/detect", s.admin(s.proxyDetect))
	m.HandleFunc("GET /api/admin/proxy/guide", s.admin(s.proxyGuide))
	m.HandleFunc("GET /api/admin/tls/certificates", s.admin(s.tlsCertificates))
	m.HandleFunc("POST /api/admin/tls/test", s.admin(s.tlsTest))
	m.HandleFunc("POST /api/admin/tls/issue", s.admin(s.tlsIssue))

	// dynamic ip: dyndns & smarthost
	m.HandleFunc("GET /api/admin/dyndns/presets", s.admin(s.dyndnsPresets))
	m.HandleFunc("GET /api/admin/dyndns/status", s.admin(s.dyndnsStatus))
	m.HandleFunc("POST /api/admin/dyndns/update", s.admin(s.dyndnsUpdate))
	m.HandleFunc("GET /api/admin/relay/presets", s.admin(s.relayPresets))
	m.HandleFunc("POST /api/admin/relay/test", s.admin(s.relayTest))

	// dns providers
	m.HandleFunc("GET /api/admin/dns/providers", s.admin(s.dnsProviders))
	m.HandleFunc("POST /api/admin/domains/{name}/dns/plan", s.admin(s.domainDNSPlan))
	m.HandleFunc("POST /api/admin/domains/{name}/dns/apply", s.admin(s.domainDNSApply))
	m.HandleFunc("GET /api/admin/system/public-ip", s.admin(s.publicIP))

	// migration & import
	m.HandleFunc("POST /api/admin/migrations/test", s.admin(s.migrationTest))
	m.HandleFunc("GET /api/admin/migrations", s.admin(s.listMigrations))
	m.HandleFunc("POST /api/admin/migrations", s.admin(s.createMigration))
	m.HandleFunc("GET /api/admin/migrations/{id}", s.admin(s.getMigration))
	m.HandleFunc("POST /api/admin/migrations/{id}/cancel", s.admin(s.cancelMigration))
	m.HandleFunc("POST /api/admin/migrations/{id}/restart", s.admin(s.restartMigration))
	m.HandleFunc("DELETE /api/admin/migrations/{id}", s.admin(s.deleteMigration))
	m.HandleFunc("POST /api/mail/import", s.user(s.importFile))

	// own account
	m.HandleFunc("POST /api/account/password", s.user(s.changePassword))
	m.HandleFunc("POST /api/account/totp/setup", s.user(s.totpSetup))
	m.HandleFunc("POST /api/account/totp/enable", s.user(s.totpEnable))
	m.HandleFunc("POST /api/account/totp/disable", s.user(s.totpDisable))
	m.HandleFunc("GET /api/account/app-passwords", s.user(s.listAppPasswords))
	m.HandleFunc("POST /api/account/app-passwords", s.user(s.createAppPassword))
	m.HandleFunc("DELETE /api/account/app-passwords/{id}", s.user(s.deleteAppPassword))
	m.HandleFunc("GET /api/account/sessions", s.user(s.listSessions))
	m.HandleFunc("DELETE /api/account/sessions/{id}", s.user(s.deleteSession))
	m.HandleFunc("POST /api/account/sessions/logout-others", s.user(s.logoutOtherSessions))
	m.HandleFunc("GET /api/account/recovery-codes", s.user(s.recoveryCodesStatus))
	m.HandleFunc("POST /api/account/recovery-codes", s.user(s.regenerateRecoveryCodes))
	m.HandleFunc("GET /api/account/vacation", s.user(s.getVacation))
	m.HandleFunc("PUT /api/account/vacation", s.user(s.putVacation))

	// admin
	m.HandleFunc("GET /api/admin/dashboard", s.admin(s.dashboard))
	m.HandleFunc("GET /api/admin/domains", s.admin(s.listDomains))
	m.HandleFunc("POST /api/admin/domains", s.admin(s.createDomain))
	m.HandleFunc("PATCH /api/admin/domains/{name}", s.admin(s.patchDomain))
	m.HandleFunc("DELETE /api/admin/domains/{name}", s.admin(s.deleteDomain))
	m.HandleFunc("GET /api/admin/domains/{name}/dns", s.admin(s.domainDNS))
	m.HandleFunc("GET /api/admin/accounts", s.admin(s.listAccounts))
	m.HandleFunc("POST /api/admin/accounts", s.admin(s.createAccount))
	m.HandleFunc("GET /api/admin/accounts/{id}", s.admin(s.getAccount))
	m.HandleFunc("PATCH /api/admin/accounts/{id}", s.admin(s.patchAccount))
	m.HandleFunc("DELETE /api/admin/accounts/{id}", s.admin(s.deleteAccount))
	m.HandleFunc("GET /api/admin/aliases", s.admin(s.listAliases))
	m.HandleFunc("POST /api/admin/aliases", s.admin(s.createAlias))
	m.HandleFunc("PATCH /api/admin/aliases/{id}", s.admin(s.patchAlias))
	m.HandleFunc("DELETE /api/admin/aliases/{id}", s.admin(s.deleteAlias))
	m.HandleFunc("GET /api/admin/queue", s.admin(s.listQueue))
	m.HandleFunc("POST /api/admin/queue/{id}/retry", s.admin(s.retryQueue))
	m.HandleFunc("DELETE /api/admin/queue/{id}", s.admin(s.deleteQueue))
	m.HandleFunc("GET /api/admin/logs", s.admin(s.logs))
	m.HandleFunc("GET /api/admin/security/bans", s.admin(s.bans))
	m.HandleFunc("DELETE /api/admin/security/bans/{ip}", s.admin(s.unban))
	m.HandleFunc("GET /api/admin/backups/status", s.admin(s.backupStatus))
	m.HandleFunc("GET /api/admin/backups", s.admin(s.backupArchives))
	m.HandleFunc("POST /api/admin/backups", s.admin(s.backupStart))
	m.HandleFunc("POST /api/admin/backups/upload", s.admin(s.backupUpload))
	m.HandleFunc("POST /api/admin/backups/test", s.admin(s.backupTest))
	m.HandleFunc("GET /api/admin/backups/{name}/download", s.admin(s.backupDownload))
	m.HandleFunc("POST /api/admin/backups/{name}/restore", s.admin(s.backupRestore))
	m.HandleFunc("DELETE /api/admin/backups/{name}", s.admin(s.backupDelete))
	m.HandleFunc("GET /api/admin/settings", s.admin(s.getSettings))
	m.HandleFunc("PUT /api/admin/settings", s.admin(s.putSettings))
	m.HandleFunc("POST /api/admin/settings/ai/test", s.admin(s.testAI))

	// mail
	m.HandleFunc("GET /api/mail/mailboxes", s.user(s.listMailboxes))
	m.HandleFunc("POST /api/mail/mailboxes", s.user(s.createMailbox))
	m.HandleFunc("PATCH /api/mail/mailboxes/{id}", s.user(s.renameMailbox))
	m.HandleFunc("DELETE /api/mail/mailboxes/{id}", s.user(s.deleteMailbox))
	m.HandleFunc("GET /api/mail/messages", s.user(s.listMessages))
	m.HandleFunc("GET /api/mail/messages/{id}", s.user(s.getMessage))
	m.HandleFunc("GET /api/mail/messages/{id}/raw", s.user(s.rawMessage))
	m.HandleFunc("GET /api/mail/messages/{id}/attachments/{att}", s.user(s.attachment))
	m.HandleFunc("PATCH /api/mail/messages/{id}", s.user(s.patchMessage))
	m.HandleFunc("DELETE /api/mail/messages/{id}", s.user(s.deleteMessage))
	m.HandleFunc("POST /api/mail/messages/bulk", s.user(s.bulk))
	m.HandleFunc("GET /api/mail/threads/{id}", s.user(s.thread))
	m.HandleFunc("POST /api/mail/send", s.user(s.send))
	m.HandleFunc("POST /api/mail/drafts", s.user(s.saveDraft))
	m.HandleFunc("GET /api/mail/tags", s.user(s.tags))

	// ai
	m.HandleFunc("GET /api/ai/status", s.user(s.aiStatus))
	m.HandleFunc("POST /api/ai/summarize", s.user(s.aiSummarize))
	m.HandleFunc("POST /api/ai/reply", s.user(s.aiReply))
	m.HandleFunc("GET /api/ai/search", s.user(s.aiSearch))
	m.HandleFunc("POST /api/ai/extract", s.user(s.aiExtract))
	m.HandleFunc("POST /api/ai/reclassify", s.user(s.aiReclassify))

	// calendar, tasks, dms
	m.HandleFunc("GET /api/calendar/events", s.user(s.listEvents))
	m.HandleFunc("POST /api/calendar/events", s.user(s.createEvent))
	m.HandleFunc("DELETE /api/calendar/events/{id}", s.user(s.deleteEvent))
	m.HandleFunc("GET /api/tasks", s.user(s.listTasks))
	m.HandleFunc("POST /api/tasks", s.user(s.createTask))
	m.HandleFunc("PATCH /api/tasks/{id}", s.user(s.patchTask))
	m.HandleFunc("DELETE /api/tasks/{id}", s.user(s.deleteTask))
	m.HandleFunc("POST /api/dms/forward", s.user(s.dmsForward))
}

// background returns a context detached from the request for slow work.
func background() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Minute)
}

// mtaSTS serves the MTA-STS policy (RFC 8461). It starts in "testing" mode so
// misconfigured TLS never blocks delivery; switch to "enforce" once reports look good.
func (s *Server) mtaSTS(w http.ResponseWriter, r *http.Request) {
	host := strings.ToLower(r.Host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	domain, isSTS := strings.CutPrefix(host, "mta-sts.")
	if isSTS {
		if ok, _ := s.Store.DomainExists(r.Context(), domain); !ok {
			http.NotFound(w, r)
			return
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "max-age=3600")
	fmt.Fprintf(w, "version: STSv1\r\nmode: testing\r\nmx: %s\r\nmax_age: 604800\r\n", s.Settings.Get().Server.Hostname)
}

// autoconfig serves the Thunderbird autoconfiguration document.
func (s *Server) autoconfig(w http.ResponseWriter, r *http.Request) {
	domain := store.DomainOf(r.URL.Query().Get("emailaddress"))
	if domain == "" {
		host := strings.ToLower(r.Host)
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		domain = strings.TrimPrefix(host, "autoconfig.")
	}
	if ok, _ := s.Store.DomainExists(r.Context(), domain); !ok {
		http.NotFound(w, r)
		return
	}
	host := html.EscapeString(s.Settings.Get().Server.Hostname)
	d := html.EscapeString(domain)
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<clientConfig version="1.1">
  <emailProvider id="%[1]s">
    <domain>%[1]s</domain>
    <displayName>%[1]s (Postwerk)</displayName>
    <displayShortName>%[1]s</displayShortName>
    <incomingServer type="imap">
      <hostname>%[2]s</hostname>
      <port>993</port>
      <socketType>SSL</socketType>
      <username>%%EMAILADDRESS%%</username>
      <authentication>password-cleartext</authentication>
    </incomingServer>
    <incomingServer type="pop3">
      <hostname>%[2]s</hostname>
      <port>995</port>
      <socketType>SSL</socketType>
      <username>%%EMAILADDRESS%%</username>
      <authentication>password-cleartext</authentication>
    </incomingServer>
    <outgoingServer type="smtp">
      <hostname>%[2]s</hostname>
      <port>465</port>
      <socketType>SSL</socketType>
      <username>%%EMAILADDRESS%%</username>
      <authentication>password-cleartext</authentication>
    </outgoingServer>
  </emailProvider>
</clientConfig>
`, d, host)
}
