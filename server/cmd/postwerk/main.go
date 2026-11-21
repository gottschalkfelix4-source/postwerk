// Command postwerk runs the Postwerk mail server: SMTP, submission, IMAP, POP3,
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

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"postwerk/internal/ai"
	"postwerk/internal/api"
	"postwerk/internal/atrest"
	"postwerk/internal/auth"
	"postwerk/internal/backup"
	"postwerk/internal/config"
	"postwerk/internal/delivery"
	"postwerk/internal/dms"
	"postwerk/internal/dyndns"
	"postwerk/internal/export"
	"postwerk/internal/httpport"
	"postwerk/internal/imapd"
	"postwerk/internal/jmap"
	"postwerk/internal/logx"
	"postwerk/internal/mailauth"
	"postwerk/internal/managesieve"
	"postwerk/internal/message"
	"postwerk/internal/metrics"
	"postwerk/internal/migrate"
	"postwerk/internal/notify"
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
	err := run(cfg, ring, log)
	if errors.Is(err, errRestart) {
		log.Warn("restarting", "component", "backup")
		if err := reexec(); err != nil {
			log.Error("restart failed – please restart the container", "err", err)
			os.Exit(1)
		}
	}
	if err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// errRestart ends run so main can start the process again (after a restore).
var errRestart = errors.New("restart requested")

func run(cfg *config.Config, ring *logx.Ring, log *slog.Logger) error {
	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(sigCtx)
	defer cancel()
	restart := false

	// A restore prepared in the web interface is swapped in before the
	// database is opened.
	if name, err := backup.ApplyPending(cfg.DataDir); err != nil {
		log.Error("restore failed, keeping current data", "component", "backup", "err", err)
	} else if name != "" {
		log.Warn("backup restored", "component", "backup", "archive", name)
	}

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
	smtpTLS, imapTLS, pop3TLS := tp.MailConfig("smtp"), tp.MailConfig("imap"), tp.MailConfig("pop3")
	log.Info("tls ready", "component", "tls", "status", tp.Status())

	guard := auth.NewGuard(func() auth.Limits {
		b := settings.Get().Security.BruteForce
		return auth.Limits{MaxAttempts: b.MaxAttempts, Window: time.Duration(b.WindowSeconds) * time.Second, BanFor: time.Duration(b.BanSeconds) * time.Second}
	})
	guard.OnBan = func(ip, reason string) {
		log.Warn("ip banned after failed logins", "component", "security", "ip", ip, "reason", reason)
	}
	guard.Store = st
	if err := guard.Load(ctx); err != nil {
		log.Error("loading persisted bans failed", "component", "security", "err", err)
	} else if n := len(guard.List()); n > 0 {
		log.Info("persisted bans restored", "component", "security", "count", n)
	}
	authn := &auth.Authenticator{Store: st, Guard: guard, Settings: settings, Log: log}

	aiClient := ai.NewClient(settings)
	aiWorker := ai.NewWorker(aiClient, st, log)
	aiWorker.Hooks.OnAnalyzed = paperlessHook(st, settings, log)

	spamFilter := &spam.Filter{Store: st, Settings: settings}
	hostname := func() string { return settings.Get().Server.Hostname }
	q := queue.New(st, hostname, relayFunc(cfg, settings), log)

	svc := &delivery.Service{Store: st, Settings: settings, Auth: authn, Spam: spamFilter, AI: aiWorker, Queue: q, Log: log.With("component", "delivery")}
	q.Bounce = svc.Bounce
	q.Delay = delayNotifier(st, svc, q, hostname, log)
	q.STS = queue.NewMTASTS(st, log.With("component", "queue"))
	if q.DANE = queue.NewSystemDANE(); q.DANE == nil {
		log.Info("dane disabled: no resolv.conf", "component", "queue")
	}
	migrations := migrate.NewManager(st, svc, log)
	dyn := dyndns.New(settings, log)
	go dyn.Run(ctx)

	// Operational notifications; the warning source is attached once the
	// API server exists. Send limit hits and failed backups trigger an
	// immediate evaluation.
	notifier := &notify.Service{Store: st, Settings: settings, Mailer: svc, Log: log}
	svc.OnSendLimit = func(*store.Account, string, int) { notifier.Trigger() }
	// A new ChatGPT sign-in resumes the AI analysis; a lost one is reported.
	aiClient.Log = log
	aiClient.OnChatGPTChange = func(connected bool) {
		if connected {
			aiWorker.Resume()
			aiWorker.Notify()
		}
		notifier.Trigger()
	}

	backups := &backup.Manager{Store: st, Settings: settings, DataDir: cfg.DataDir, Version: version, Log: log,
		Restart: func() { restart = true; cancel() }, OnFailure: notifier.Trigger}
	go backups.Run(ctx)

	logMasterKey(st, log)
	atRest := &atrest.Manager{Store: st, Log: log}
	exports := &export.Manager{Store: st, DataDir: cfg.DataDir, Log: log}
	go exports.Run(ctx)

	go q.Run(ctx)
	go aiWorker.Run(ctx)
	go housekeeping(ctx, st, settings, tp, log)

	maxSize := func() int64 { return settings.Get().Server.MaxMessageSize }
	smtpLog := log.With("component", "smtp")
	listeners := []func(context.Context) error{}
	if cfg.SMTPAddr != "" {
		srv := &smtp.Server{Addr: cfg.SMTPAddr, Mode: smtp.ModeInbound, TLSConfig: smtpTLS, Hostname: hostname, MaxSize: maxSize, Backend: svc, Log: smtpLog}
		listeners = append(listeners, srv.ListenAndServe)
	}
	if cfg.SubmissionAddr != "" {
		srv := &smtp.Server{Addr: cfg.SubmissionAddr, Mode: smtp.ModeSubmission, TLSConfig: smtpTLS, Hostname: hostname, MaxSize: maxSize, Backend: svc, Log: smtpLog, AllowInsecureAuth: cfg.AllowInsecureAuth,
			MaxRcpts: store.MaxSendLimitRecipientsPerMessage}
		listeners = append(listeners, srv.ListenAndServe)
	}
	if cfg.SubmissionsAddr != "" {
		srv := &smtp.Server{Addr: cfg.SubmissionsAddr, Mode: smtp.ModeSubmission, ImplicitTLS: true, TLSConfig: smtpTLS, Hostname: hostname, MaxSize: maxSize, Backend: svc, Log: smtpLog,
			MaxRcpts: store.MaxSendLimitRecipientsPerMessage}
		listeners = append(listeners, srv.ListenAndServe)
	}
	popAuth := func(ctx context.Context, ip, user, pass string) (*store.Account, error) {
		return authn.Protocol(ctx, ip, user, pass, "pop3")
	}
	imapAuth := func(ctx context.Context, ip, user, pass string) (*store.Account, error) {
		return authn.Protocol(ctx, ip, user, pass, "imap")
	}
	imapLog := log.With("component", "imap")
	if cfg.IMAPAddr != "" {
		srv := &imapd.Server{Addr: cfg.IMAPAddr, TLSConfig: imapTLS, Store: st, Delivery: svc, Auth: imapAuth, Banned: guard.Banned, MaxSize: maxSize, Log: imapLog, AllowInsecureAuth: cfg.AllowInsecureAuth}
		listeners = append(listeners, srv.ListenAndServe)
	}
	if cfg.IMAPSAddr != "" {
		srv := &imapd.Server{Addr: cfg.IMAPSAddr, ImplicitTLS: true, TLSConfig: imapTLS, Store: st, Delivery: svc, Auth: imapAuth, Banned: guard.Banned, MaxSize: maxSize, Log: imapLog}
		listeners = append(listeners, srv.ListenAndServe)
	}
	pop3Log := log.With("component", "pop3")
	if cfg.POP3Addr != "" {
		srv := &pop3.Server{Addr: cfg.POP3Addr, TLSConfig: pop3TLS, Store: st, Auth: popAuth, Banned: guard.Banned, Hostname: hostname, Log: pop3Log, AllowInsecureAuth: cfg.AllowInsecureAuth}
		listeners = append(listeners, srv.ListenAndServe)
	}
	if cfg.POP3SAddr != "" {
		srv := &pop3.Server{Addr: cfg.POP3SAddr, ImplicitTLS: true, TLSConfig: pop3TLS, Store: st, Auth: popAuth, Banned: guard.Banned, Hostname: hostname, Log: pop3Log}
		listeners = append(listeners, srv.ListenAndServe)
	}

	jmapSrv := &jmap.Server{Store: st, Auth: authn, Delivery: svc, MaxSize: maxSize, Log: log.With("component", "jmap"), AllowInsecureAuth: cfg.AllowInsecureAuth}
	go jmapSrv.Run(ctx)
	if cfg.ManageSieveAddr != "" {
		srv := &managesieve.Server{Addr: cfg.ManageSieveAddr, TLSConfig: tp.MailConfig("managesieve"), Store: st, Banned: guard.Banned,
			Log: log.With("component", "managesieve"), AllowInsecureAuth: cfg.AllowInsecureAuth,
			Auth: func(ctx context.Context, ip, user, pass string) (*store.Account, error) {
				return authn.Protocol(ctx, ip, user, pass, "managesieve")
			}}
		listeners = append(listeners, srv.ListenAndServe)
	}

	apiSrv := &api.Server{
		Cfg: cfg, Store: st, Settings: settings, Auth: authn, Delivery: svc, AI: aiClient, AIWorker: aiWorker,
		Spam: spamFilter, Logs: ring, Log: log, UI: webui.FS(), TLS: tp, Version: version, Started: time.Now(), SetupToken: setupToken, Migrations: migrations, DynDNS: dyn, Backups: backups,
		JMAP: jmapSrv,
		AtRest: atRest, Exports: exports, Notifier: notifier,
	}
	notifier.Warnings = apiSrv.OpsWarnings
	go notifier.Run(ctx)
	handler := apiSrv.Handler()
	httpCtl := &httpport.Controller{
		Addr: cfg.HTTPAddr, HTTPSAddr: cfg.HTTPSAddr, Handler: handler, Settings: settings, TLS: tp,
		ForceOpen: cfg.HTTPAlwaysOpen, TrustProxy: cfg.TrustProxy, Log: log,
		SetupDone: func() bool { return apiSrv.SetupDone(context.Background()) },
	}
	apiSrv.HTTPPort = httpCtl
	go httpCtl.Run(ctx)
	// Internal health endpoint for the Docker healthcheck (never published),
	// so closing port 80 does not mark the container unhealthy. It also
	// serves /metrics without a token for local scraping.
	if cfg.HealthAddr != "" {
		healthMux := http.NewServeMux()
		healthMux.Handle("GET /metrics", promhttp.HandlerFor(metrics.Registry, promhttp.HandlerOpts{}))
		healthMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
		healthSrv := &http.Server{Addr: cfg.HealthAddr, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second,
			IdleTimeout: 120 * time.Second, Handler: healthMux}
		go func() { <-ctx.Done(); shutdown(healthSrv) }()
		go func() { _ = healthSrv.ListenAndServe() }()
	}
	if cfg.HTTPSAddr != "" {
		// No global ReadTimeout/WriteTimeout: large uploads and downloads must
		// keep working; request bodies get per-request deadlines in the API.
		httpsSrv := &http.Server{Addr: cfg.HTTPSAddr, Handler: handler, TLSConfig: tlsConfig, ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout: 120 * time.Second}
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
		if restart && sigCtx.Err() == nil {
			return errRestart
		}
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
	// A generated password must be replaced at the first login.
	if err := st.CreateAccount(ctx, &store.Account{Email: email, Name: "Administrator", PasswordHash: h, Role: "admin", MustChangePassword: generated}); err != nil {
		return "", err
	}
	if generated {
		// Never log the password itself: it goes to a file only the owner can read.
		path := filepath.Join(cfg.DataDir, api.AdminPasswordFile)
		if err := os.WriteFile(path, []byte(pw+"\n"), 0o600); err != nil {
			return "", fmt.Errorf("write %s: %w", path, err)
		}
		log.Warn("setup_token nicht nötig: Admin-Konto aus POSTWERK_ADMIN_EMAIL angelegt – das erzeugte Passwort steht in der Datei; bei der ersten Anmeldung muss es geändert werden",
			"component", "setup", "email", email, "password_file", path)
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

// housekeeping purges expired sessions hourly and runs the daily cleanup
// (Trash/Junk retention, vacation replies, orphaned blobs). It also
// registers the operational metrics.
func housekeeping(ctx context.Context, st *store.Store, settings *store.SettingsCache, tp *tlsutil.Dynamic, log *slog.Logger) {
	metrics.RegisterOps(opsSources(st, tp))
	log = log.With("component", "housekeeping")
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	// The first daily run happens about an hour after start, not during boot.
	lastDaily := time.Now().Add(-23 * time.Hour)
	for {
		st.PurgeExpired(ctx)
		if err := st.PruneSendLog(ctx, time.Now()); err != nil {
			log.Error("send log cleanup failed", "err", err)
		}
		if time.Since(lastDaily) >= 24*time.Hour {
			lastDaily = time.Now()
			dailyCleanup(ctx, st, settings.Get().Server.Retention, log)
		}
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
		if !cfg.Enabled || !cfg.AutoForward || !m.HasAttachments || a.Category == "" || a.Suspicious {
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
