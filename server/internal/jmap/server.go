package jmap

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"postwerk/internal/auth"
	"postwerk/internal/delivery"
	"postwerk/internal/metrics"
	"postwerk/internal/store"
)

// Delivery is the part of delivery.Service JMAP needs: submission (DKIM,
// sender checks, queue) and storing messages into mailboxes.
type Delivery interface {
	Submit(ctx context.Context, acct *store.Account, from string, rcpts []string, data []byte, saveSent bool) (*store.Message, error)
	ImportMessage(ctx context.Context, acct *store.Account, mailboxID int64, raw []byte, opt delivery.ImportOptions) (*store.Message, error)
}

// Server serves the JMAP endpoints.
type Server struct {
	Store    *store.Store
	Auth     *auth.Authenticator
	Delivery Delivery
	// MaxSize returns the maximum message (and upload) size.
	MaxSize func() int64
	Log     *slog.Logger
	// AllowInsecureAuth permits HTTP Basic authentication without HTTPS
	// (development only).
	AllowInsecureAuth bool
	// ClientInfo returns the effective client IP and whether the original
	// request used HTTPS (after evaluating trusted proxies).
	ClientInfo func(r *http.Request) (ip string, https bool)
	// CookieAuth authenticates a request by the web session cookie.
	CookieAuth func(r *http.Request) (*store.Account, error)

	initOnce sync.Once
	cacheKey []byte
	cacheMu  sync.Mutex
	cache    map[string]cachedAuth
	limitMu  sync.Mutex
	active   map[string]int // concurrent requests/uploads per account
}

type cachedAuth struct {
	accountID int64
	expires   time.Time
}

// authCacheTTL bounds how long verified Basic credentials are remembered,
// so not every request pays the bcrypt cost. A changed password or revoked
// app password stops working after at most this long.
const authCacheTTL = 2 * time.Minute

func (s *Server) init() {
	s.initOnce.Do(func() {
		s.cacheKey = make([]byte, 32)
		_, _ = rand.Read(s.cacheKey)
		s.cache = map[string]cachedAuth{}
		s.active = map[string]int{}
		if s.Log == nil {
			s.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
		}
	})
}

// Register adds the JMAP routes to mux.
func (s *Server) Register(mux *http.ServeMux) {
	s.init()
	mux.HandleFunc("GET /.well-known/jmap", s.handleSession)
	mux.HandleFunc("GET /jmap/session", s.handleSession)
	mux.HandleFunc("POST /jmap/api", s.handleAPI)
	mux.HandleFunc("POST /jmap/api/", s.handleAPI)
	mux.HandleFunc("POST /jmap/upload/{accountId}", s.handleUpload)
	mux.HandleFunc("POST /jmap/upload/{accountId}/", s.handleUpload)
	mux.HandleFunc("GET /jmap/download/{accountId}/{blobId}/{name}", s.handleDownload)
	mux.HandleFunc("GET /jmap/eventsource", s.handleEventSource)
}

func randomID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Server) maxSize() int64 {
	if s.MaxSize != nil {
		if n := s.MaxSize(); n > 0 {
			return n
		}
	}
	return 50 << 20
}

func (s *Server) clientInfo(r *http.Request) (string, bool) {
	if s.ClientInfo != nil {
		return s.ClientInfo(r)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return host, r.TLS != nil
}

// ---- errors ----

// problem writes a request-level error (RFC 7807 problem details).
func (s *Server) problem(w http.ResponseWriter, status int, typ, detail string, extra ...string) {
	body := map[string]any{"status": status, "detail": detail}
	if typ != "" {
		body["type"] = typ
	} else {
		body["type"] = "about:blank"
	}
	for i := 0; i+1 < len(extra); i += 2 {
		body[extra[i]] = extra[i+1]
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	_ = json.NewEncoder(w).Encode(v)
}

// ---- authentication ----

// authenticate resolves the account of a request: HTTP Basic (account or
// app password, HTTPS only) or the web session cookie. Cookie-authenticated
// state-changing requests must carry the X-Postwerk CSRF header.
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) (*store.Account, bool) {
	s.init()
	ip, https := s.clientInfo(r)
	secure := https || s.AllowInsecureAuth
	if h := r.Header.Get("Authorization"); h != "" {
		scheme, _, _ := strings.Cut(h, " ")
		if !strings.EqualFold(scheme, "Basic") {
			s.challenge(w, secure, "unsupported authorization scheme")
			return nil, false
		}
		if !secure {
			s.problem(w, http.StatusForbidden, "", "Basic authentication requires HTTPS")
			return nil, false
		}
		user, pass, ok := r.BasicAuth()
		if !ok {
			s.challenge(w, secure, "malformed credentials")
			return nil, false
		}
		acct, err := s.checkBasic(r.Context(), ip, strings.TrimSpace(user), pass)
		if err != nil {
			if errors.Is(err, auth.ErrBanned) {
				s.problem(w, http.StatusTooManyRequests, "", "too many failed logins")
				return nil, false
			}
			s.Log.Warn("authentication failed", "component", "jmap", "user", user, "ip", ip)
			s.challenge(w, secure, "invalid credentials")
			return nil, false
		}
		return acct, true
	}
	if s.CookieAuth != nil {
		if acct, err := s.CookieAuth(r); err == nil && acct != nil {
			if acct.MustChangePassword {
				s.problem(w, http.StatusForbidden, "", "password change required")
				return nil, false
			}
			if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("X-Postwerk") != "1" {
				s.problem(w, http.StatusForbidden, "", "missing CSRF header X-Postwerk")
				return nil, false
			}
			return acct, true
		}
	}
	s.challenge(w, secure, "authentication required")
	return nil, false
}

// challenge answers 401; the Basic challenge is only offered where
// credentials would not travel in the clear.
func (s *Server) challenge(w http.ResponseWriter, secure bool, detail string) {
	if secure {
		w.Header().Set("WWW-Authenticate", `Basic realm="Postwerk JMAP", charset="UTF-8"`)
	}
	s.problem(w, http.StatusUnauthorized, "", detail)
}

func (s *Server) credKey(user, pass string) string {
	m := hmac.New(sha256.New, s.cacheKey)
	m.Write([]byte(store.Normalize(user)))
	m.Write([]byte{0})
	m.Write([]byte(pass))
	return string(m.Sum(nil))
}

func (s *Server) checkBasic(ctx context.Context, ip, user, pass string) (*store.Account, error) {
	if s.Auth == nil {
		return nil, auth.ErrInvalid
	}
	if s.Auth.Guard != nil && s.Auth.Guard.Banned(ip) {
		return nil, auth.ErrBanned
	}
	key := s.credKey(user, pass)
	s.cacheMu.Lock()
	c, ok := s.cache[key]
	s.cacheMu.Unlock()
	if ok && time.Now().Before(c.expires) {
		if acct, err := s.Store.GetAccount(ctx, c.accountID); err == nil && !acct.Disabled {
			return acct, nil
		}
	}
	acct, err := s.Auth.Protocol(ctx, ip, user, pass, "jmap")
	if err != nil {
		metrics.AuthAttempts.WithLabelValues("jmap", "failure").Inc()
		s.cacheMu.Lock()
		delete(s.cache, key)
		s.cacheMu.Unlock()
		return nil, err
	}
	metrics.AuthAttempts.WithLabelValues("jmap", "success").Inc()
	s.cacheMu.Lock()
	if len(s.cache) > 1000 {
		for k, v := range s.cache {
			if time.Now().After(v.expires) {
				delete(s.cache, k)
			}
		}
		if len(s.cache) > 1000 {
			s.cache = map[string]cachedAuth{}
		}
	}
	s.cache[key] = cachedAuth{acct.ID, time.Now().Add(authCacheTTL)}
	s.cacheMu.Unlock()
	return acct, nil
}

// acquire enforces a per-account concurrency limit; release must be called.
func (s *Server) acquire(kind string, accountID int64, limit int) (release func(), ok bool) {
	key := kind + ":" + strconv.FormatInt(accountID, 10)
	s.limitMu.Lock()
	defer s.limitMu.Unlock()
	if s.active[key] >= limit {
		return nil, false
	}
	s.active[key]++
	return func() {
		s.limitMu.Lock()
		if s.active[key]--; s.active[key] <= 0 {
			delete(s.active, key)
		}
		s.limitMu.Unlock()
	}, true
}

// ---- session resource ----

func baseURL(r *http.Request, https bool) string {
	scheme := "http"
	if https {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (s *Server) sessionState(acct *store.Account) string {
	aliases := append([]string{}, acct.Aliases...)
	sort.Strings(aliases)
	h := sha256.Sum256([]byte(strconv.FormatInt(acct.ID, 10) + "\x00" + acct.Email + "\x00" + acct.Name + "\x00" + strings.Join(aliases, ",")))
	return hex.EncodeToString(h[:8])
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	_, https := s.clientInfo(r)
	base := baseURL(r, https)
	accountID := fmtID(prefixAccount, acct.ID)
	maxUpload := s.maxSize()
	writeJSON(w, map[string]any{
		"capabilities": map[string]any{
			capCore: map[string]any{
				"maxSizeUpload":         maxUpload,
				"maxConcurrentUpload":   maxConcurrentUpload,
				"maxSizeRequest":        maxSizeRequest,
				"maxConcurrentRequests": maxConcurrentRequests,
				"maxCallsInRequest":     maxCallsInRequest,
				"maxObjectsInGet":       maxObjectsInGet,
				"maxObjectsInSet":       maxObjectsInSet,
				"collationAlgorithms":   []string{"i;ascii-casemap", "i;unicode-casemap"},
			},
			capMail:       map[string]any{},
			capSubmission: map[string]any{},
		},
		"accounts": map[string]any{
			accountID: map[string]any{
				"name":       acct.Email,
				"isPersonal": true,
				"isReadOnly": false,
				"accountCapabilities": map[string]any{
					capMail: map[string]any{
						"maxMailboxesPerEmail":       1,
						"maxMailboxDepth":            nil,
						"maxSizeMailboxName":         maxMailboxName,
						"maxSizeAttachmentsPerEmail": maxUpload,
						"emailQuerySortOptions":      emailSortOptions,
						"mayCreateTopLevelMailbox":   true,
					},
					capSubmission: map[string]any{
						"maxDelayedSend":       0,
						"submissionExtensions": map[string]any{},
					},
				},
			},
		},
		"primaryAccounts": map[string]string{
			capCore: accountID, capMail: accountID, capSubmission: accountID,
		},
		"username":       acct.Email,
		"apiUrl":         base + "/jmap/api",
		"downloadUrl":    base + "/jmap/download/{accountId}/{blobId}/{name}?type={type}",
		"uploadUrl":      base + "/jmap/upload/{accountId}/",
		"eventSourceUrl": base + "/jmap/eventsource?types={types}&closeafter={closeafter}&ping={ping}",
		"state":          s.sessionState(acct),
	})
}

// ---- API endpoint ----

func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	release, ok := s.acquire("api", acct.ID, maxConcurrentRequests)
	if !ok {
		s.problem(w, http.StatusTooManyRequests, "urn:ietf:params:jmap:error:limit", "too many concurrent requests", "limit", "maxConcurrentRequests")
		return
	}
	defer release()
	if ct := r.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(strings.ToLower(ct), "application/json") {
		s.problem(w, http.StatusBadRequest, "urn:ietf:params:jmap:error:notJSON", "Content-Type must be application/json")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxSizeRequest))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.problem(w, http.StatusRequestEntityTooLarge, "urn:ietf:params:jmap:error:limit", "request too large", "limit", "maxSizeRequest")
			return
		}
		s.problem(w, http.StatusBadRequest, "urn:ietf:params:jmap:error:notJSON", "could not read request")
		return
	}
	if !json.Valid(body) {
		s.problem(w, http.StatusBadRequest, "urn:ietf:params:jmap:error:notJSON", "request is not valid JSON")
		return
	}
	var req request
	if err := json.Unmarshal(body, &req); err != nil || req.Using == nil || req.MethodCalls == nil {
		s.problem(w, http.StatusBadRequest, "urn:ietf:params:jmap:error:notRequest", "request does not match the JMAP Request type")
		return
	}
	using := map[string]bool{}
	for _, u := range req.Using {
		switch u {
		case capCore, capMail, capSubmission:
			using[u] = true
		default:
			s.problem(w, http.StatusBadRequest, "urn:ietf:params:jmap:error:unknownCapability", "unsupported capability "+u)
			return
		}
	}
	if len(req.MethodCalls) > maxCallsInRequest {
		s.problem(w, http.StatusBadRequest, "urn:ietf:params:jmap:error:limit", "too many method calls", "limit", "maxCallsInRequest")
		return
	}
	c := &call{srv: s, ctx: r.Context(), acct: acct, using: using, created: map[string]string{}}
	for k, v := range req.CreatedIDs {
		c.created[k] = v
	}
	resp := responseBody{MethodResponses: c.run(&req), SessionState: s.sessionState(acct)}
	if req.CreatedIDs != nil {
		resp.CreatedIDs = c.created
	}
	writeJSON(w, resp)
}

// Run prunes the change log, stale uploads and old submissions once a day.
func (s *Server) Run(ctx context.Context) {
	s.init()
	// The first run happens an hour after start, not during boot.
	t := time.NewTimer(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		t.Reset(24 * time.Hour)
		if err := s.Store.PruneChanges(ctx, 30*24*time.Hour, 100000, 24*time.Hour); err != nil {
			s.Log.Error("pruning jmap change log failed", "component", "jmap", "err", err)
		}
	}
}
