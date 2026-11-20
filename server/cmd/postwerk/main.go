// Command postwerk runs the Postwerk mail server: SMTP, submission, POP3,
// the HTTP API with the web interface, the outbound queue and AI workers.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"postwerk/internal/ai"
	"postwerk/internal/api"
	"postwerk/internal/auth"
	"postwerk/internal/config"
	"postwerk/internal/delivery"
	"postwerk/internal/dms"
	"postwerk/internal/dyndns"
	"postwerk/internal/httpport"
	"postwerk/internal/logx"
	"postwerk/internal/mailauth"
	"postwerk/internal/message"
	"postwerk/internal/migrate"
	"postwerk/internal/pop3"
	"postwerk/internal/queue"
	"postwerk/internal/smtp"
	"postwerk/internal/spam"
	"postwerk/internal/store"
	"postwerk/internal/tlsutil"
	"postwerk/internal/webui"
)

var version = "0.1.0-dev"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Println("postwerk", version)
		return
	}
	cfg := config.Load()
	if len(os.Args) > 1 && os.Args[1] != "serve" {
		if err := command(cfg, os.Args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "Fehler:", err)
			os.Exit(1)
		}
		return
	}
	ring := logx.NewRing(5000)
	log := logx.New(ring, cfg.Debug)
	slog.SetDefault(log)
	if err := run(cfg, ring, log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(cfg *config.Config, ring *logx.Ring, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	settings, err := st.LoadSettings(ctx, cfg.Hostname)
	if err != nil {
		return fmt.Errorf("load settings: %w", err)
	}
	setupToken, err := bootstrap(ctx, cfg, st, log)
	if err != nil {
		return err
	}

	tp := &tlsutil.Dynamic{
		DataDir:  cfg.DataDir,
		Settings: func() store.TLSSettings { return settings.Get().TLS },
		Hostname: func() string { return settings.Get().Server.Hostname },
		EnvCert:  cfg.TLSCertFile, EnvKey: cfg.TLSKeyFile,
		ExtraHost: func(ctx context.Context, host string) bool {
			for _, prefix := range []string{"mta-sts.", "autoconfig."} {
				if d, ok := strings.CutPrefix(host, prefix); ok {
					exists, _ := st.DomainExists(ctx, d)
					return exists
				}
			}
			return false
		},
	}
	tlsConfig := tp.Config()
	log.Info("tls ready", "component", "tls", "status", tp.Status())

	guard := auth.NewGuard(func() auth.Limits {
		b := settings.Get().Security.BruteForce
		return auth.Limits{MaxAttempts: b.MaxAttempts, Window: time.Duration(b.WindowSeconds) * time.Second, BanFor: time.Duration(b.BanSeconds) * time.Second}
	})
	guard.OnBan = func(ip, reason string) {
		log.Warn("ip banned after failed logins", "component", "security", "ip", ip, "reason", reason)
	}
	authn := &auth.Authenticator{Store: st, Guard: guard}

	aiClient := ai.NewClient(settings)
	aiWorker := ai.NewWorker(aiClient, st, log)
	aiWorker.Hooks.OnAnalyzed = paperlessHook(st, settings, log)

	spamFilter := &spam.Filter{Store: st, Settings: settings}
	hostname := func() string { return settings.Get().Server.Hostname }
	q := queue.New(st, hostname, relayFunc(cfg, settings), log)

	svc := &delivery.Service{Store: st, Settings: settings, Auth: authn, Spam: spamFilter, AI: aiWorker, Queue: q, Log: log.With("component", "delivery")}
	q.Bounce = svc.Bounce
	migrations := migrate.NewManager(st, svc, log)
	dyn := dyndns.New(settings, log)
	go dyn.Run(ctx)

	go q.Run(ctx)
	go aiWorker.Run(ctx)
	go housekeeping(ctx, st)

	maxSize := func() int64 { return settings.Get().Server.MaxMessageSize }
	smtpLog := log.With("component", "smtp")
	listeners := []func(context.Context) error{}
	if cfg.SMTPAddr != "" {
		srv := &smtp.Server{Addr: cfg.SMTPAddr, Mode: smtp.ModeInbound, TLSConfig: tlsConfig, Hostname: hostname, MaxSize: maxSize, Backend: svc, Log: smtpLog}
		listeners = append(listeners, srv.ListenAndServe)
	}
	if cfg.SubmissionAddr != "" {
		srv := &smtp.Server{Addr: cfg.SubmissionAddr, Mode: smtp.ModeSubmission, TLSConfig: tlsConfig, Hostname: hostname, MaxSize: maxSize, Backend: svc, Log: smtpLog, AllowInsecureAuth: cfg.AllowInsecureAuth}
		listeners = append(listeners, srv.ListenAndServe)
	}
	if cfg.SubmissionsAddr != "" {
		srv := &smtp.Server{Addr: cfg.SubmissionsAddr, Mode: smtp.ModeSubmission, ImplicitTLS: true, TLSConfig: tlsConfig, Hostname: hostname, MaxSize: maxSize, Backend: svc, Log: smtpLog}
		listeners = append(listeners, srv.ListenAndServe)
	}
	popAuth := func(ctx context.Context, ip, user, pass string) (*store.Account, error) {
		return authn.Protocol(ctx, ip, user, pass, "pop3")
	}
	pop3Log := log.With("component", "pop3")
	if cfg.POP3Addr != "" {
		srv := &pop3.Server{Addr: cfg.POP3Addr, TLSConfig: tlsConfig, Store: st, Auth: popAuth, Banned: guard.Banned, Hostname: hostname, Log: pop3Log, AllowInsecureAuth: cfg.AllowInsecureAuth}
		listeners = append(listeners, srv.ListenAndServe)
	}
	if cfg.POP3SAddr != "" {
		srv := &pop3.Server{Addr: cfg.POP3SAddr, ImplicitTLS: true, TLSConfig: tlsConfig, Store: st, Auth: popAuth, Banned: guard.Banned, Hostname: hostname, Log: pop3Log}
		listeners = append(listeners, srv.ListenAndServe)
	}

	apiSrv := &api.Server{
		Cfg: cfg, Store: st, Settings: settings, Auth: authn, Delivery: svc, AI: aiClient, AIWorker: aiWorker,
		Spam: spamFilter, Logs: ring, Log: log, UI: webui.FS(), TLS: tp, Version: version, Started: time.Now(), SetupToken: setupToken, Migrations: migrations, DynDNS: dyn,
	}
	handler := apiSrv.Handler()
	httpCtl := &httpport.Controller{
		Addr: cfg.HTTPAddr, HTTPSAddr: cfg.HTTPSAddr, Handler: handler, Settings: settings, TLS: tp,
		ForceOpen: cfg.HTTPAlwaysOpen, Log: log,
	}
	apiSrv.HTTPPort = httpCtl
	go httpCtl.Run(ctx)
	// Internal health endpoint for the Docker healthcheck (never published),
	// so closing port 80 does not mark the container unhealthy.
	if cfg.HealthAddr != "" {
		healthSrv := &http.Server{Addr: cfg.HealthAddr, ReadHeaderTimeout: 5 * time.Second,
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })}
		go func() { <-ctx.Done(); shutdown(healthSrv) }()
		go func() { _ = healthSrv.ListenAndServe() }()
	}
	if cfg.HTTPSAddr != "" {
		httpsSrv := &http.Server{Addr: cfg.HTTPSAddr, Handler: handler, TLSConfig: tlsConfig, ReadHeaderTimeout: 10 * time.Second}
		listeners = append(listeners, func(ctx context.Context) error {
			log.Info("listening", "component", "http", "proto", "https", "addr", cfg.HTTPSAddr)
			go func() { <-ctx.Done(); shutdown(httpsSrv) }()
			if err := httpsSrv.ListenAndServeTLS("", ""); !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		})
	}

	errc := make(chan error, len(listeners))
	for _, l := range listeners {
		go func(l func(context.Context) error) { errc <- l(ctx) }(l)
	}
	log.Info("postwerk started", "version", version, "hostname", hostname())
	select {
	case <-ctx.Done():
		log.Info("shutting down")
		return nil
	case err := <-errc:
		return err
	}
}

// relayFunc returns the smarthost: environment variables win over settings.
func relayFunc(cfg *config.Config, settings *store.SettingsCache) func() queue.Relay {
	return func() queue.Relay {
		if cfg.RelayHost != "" {
			host := cfg.RelayHost
			if _, _, err := net.SplitHostPort(host); err != nil {
				host = net.JoinHostPort(host, "587")
			}
			sec := "starttls"
			if strings.HasSuffix(host, ":465") {
				sec = "tls"
			}
			return queue.Relay{Host: host, Security: sec, User: cfg.RelayUser, Password: cfg.RelayPassword}
		}
		r := settings.Get().Relay
		if !r.Enabled || r.Host == "" {
			return queue.Relay{}
		}
		port := r.Port
		if port == 0 {
			port = 587
		}
		return queue.Relay{Host: net.JoinHostPort(r.Host, strconv.Itoa(port)), Security: r.Security, User: r.Username, Password: r.Password}
	}
}

func shutdown(srv *http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

// bootstrap prepares an empty install. With POSTWERK_ADMIN_EMAIL set it
// creates the first domain and admin account directly; otherwise it returns a
// one-time setup token for the web setup assistant.
func bootstrap(ctx context.Context, cfg *config.Config, st *store.Store, log *slog.Logger) (string, error) {
	n, err := st.CountAccounts(ctx)
	if err != nil {
		return "", err
	}
	if n > 0 {
		// Mention "setup_token" so `logs | grep setup_token` always explains itself.
		log.Info("setup_token nicht nötig: Postwerk ist bereits eingerichtet – mit einem Admin-Konto anmelden (Passwort vergessen: postwerk reset-password)",
			"component", "setup", "admins", strings.Join(adminEmails(ctx, st), ","))
		return "", nil
	}
	email := store.Normalize(cfg.AdminEmail)
	if email == "" {
		token := auth.NewAppPassword()
		path := filepath.Join(cfg.DataDir, "setup-token.txt")
		_ = os.WriteFile(path, []byte(token+"\n"), 0o600)
		log.Warn("Noch kein Konto: Weboberfläche öffnen und den Setup-Assistenten mit diesem setup_token starten", "component", "setup", "setup_token", token, "file", path)
		return token, nil
	}
	domain := store.DomainOf(email)
	if ok, _ := st.DomainExists(ctx, domain); !ok {
		key, err := mailauth.GenerateKey()
		if err != nil {
			return "", err
		}
		if err := st.CreateDomain(ctx, domain, "pw"+time.Now().Format("200601"), key); err != nil {
			return "", err
		}
	}
	pw := cfg.AdminPassword
	generated := pw == ""
	if generated {
		pw = auth.NewAppPassword()
	}
	h, err := auth.HashPassword(pw)
	if err != nil {
		return "", err
	}
	if err := st.CreateAccount(ctx, &store.Account{Email: email, Name: "Administrator", PasswordHash: h, Role: "admin"}); err != nil {
		return "", err
	}
	if generated {
		log.Warn("setup_token nicht nötig: Admin-Konto aus POSTWERK_ADMIN_EMAIL angelegt – Passwort nach der ersten Anmeldung ändern", "component", "setup", "email", email, "password", pw)
	} else {
		log.Info("setup_token nicht nötig: Admin-Konto aus POSTWERK_ADMIN_EMAIL/POSTWERK_ADMIN_PASSWORD angelegt", "component", "setup", "email", email)
	}
	return "", nil
}

// adminEmails lists the addresses of all admin accounts.
func adminEmails(ctx context.Context, st *store.Store) []string {
	accts, _, err := st.ListAccounts(ctx, "", 200, 0)
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range accts {
		if a.Role == "admin" {
			out = append(out, a.Email)
		}
	}
	return out
}

func housekeeping(ctx context.Context, st *store.Store) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		st.PurgeExpired(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// paperlessHook forwards supported attachments of messages whose AI category
// is configured for automatic DMS import.
func paperlessHook(st *store.Store, settings *store.SettingsCache, log *slog.Logger) func(context.Context, *store.Message, *ai.Analysis) {
	log = log.With("component", "dms")
	return func(ctx context.Context, m *store.Message, a *ai.Analysis) {
		cfg := settings.Get().DMS.Paperless
		if !cfg.Enabled || !cfg.AutoForward || !m.HasAttachments || a.Category == "" {
			return
		}
		match := false
		for _, c := range cfg.Categories {
			if strings.EqualFold(c, a.Category) {
				match = true
			}
		}
		if !match {
			return
		}
		if inbox, err := st.MailboxByRole(ctx, m.AccountID, "inbox"); err != nil || inbox != m.MailboxID {
			return
		}
		raw, err := st.Blobs.Get(m.BlobHash)
		if err != nil {
			return
		}
		p, err := message.Parse(raw, true)
		if err != nil {
			return
		}
		for _, att := range p.Attachments {
			if !dms.Supported(att.ContentType, att.Filename) {
				continue
			}
			if err := dms.Forward(ctx, cfg, att.Filename, att.ContentType, att.Data, m.Subject); err != nil {
				log.Error("paperless forward failed", "message", m.ID, "file", att.Filename, "err", err)
				continue
			}
			log.Info("attachment forwarded to paperless", "message", m.ID, "file", att.Filename, "category", a.Category)
		}
	}
}
