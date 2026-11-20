// Package config holds the static bootstrap configuration read from the
// environment. Runtime settings that can be changed in the web UI live in the
// database (see store.Settings).
package config

import (
	"os"
	"strconv"
	"strings"
)

type Config struct {
	DataDir  string
	Hostname string

	HTTPAddr  string // plain HTTP (UI, API, ACME HTTP-01)
	HTTPSAddr string // HTTPS (UI, API); empty disables

	SMTPAddr        string // inbound MTA (25)
	SubmissionAddr  string // submission with STARTTLS (587)
	SubmissionsAddr string // submission with implicit TLS (465)
	IMAPAddr        string // 143
	IMAPSAddr       string // 993
	POP3Addr        string // 110
	POP3SAddr       string // 995

	TLSCertFile string
	TLSKeyFile  string
	// CertsDir is scanned for certificates taken over from a reverse proxy.
	CertsDir string

	AdminEmail    string
	AdminPassword string

	MetricsToken string

	// Optional smarthost for outbound mail (e.g. when port 25 is blocked).
	RelayHost     string
	RelayUser     string
	RelayPassword string

	// AllowInsecureAuth permits AUTH without TLS (development only).
	AllowInsecureAuth bool
	// SecureCookies marks the session cookie as Secure.
	SecureCookies bool
	// TrustProxy uses X-Forwarded-For / X-Real-IP for the client address
	// (only enable behind a reverse proxy).
	TrustProxy bool
	// HTTPAlwaysOpen keeps port 80 open regardless of the setting (emergency).
	HTTPAlwaysOpen bool
	// HealthAddr serves /healthz for the container healthcheck.
	HealthAddr string
	Debug      bool
}

func Load() *Config {
	return &Config{
		DataDir:           env("POSTWERK_DATA_DIR", "./data"),
		Hostname:          env("POSTWERK_HOSTNAME", "localhost"),
		HTTPAddr:          env("POSTWERK_HTTP_ADDR", ":8080"),
		HTTPSAddr:         env("POSTWERK_HTTPS_ADDR", ""),
		SMTPAddr:          env("POSTWERK_SMTP_ADDR", ":2525"),
		SubmissionAddr:    env("POSTWERK_SUBMISSION_ADDR", ":5870"),
		SubmissionsAddr:   env("POSTWERK_SUBMISSIONS_ADDR", ""),
		IMAPAddr:          env("POSTWERK_IMAP_ADDR", ":1143"),
		IMAPSAddr:         env("POSTWERK_IMAPS_ADDR", ""),
		POP3Addr:          env("POSTWERK_POP3_ADDR", ":1110"),
		POP3SAddr:         env("POSTWERK_POP3S_ADDR", ""),
		TLSCertFile:       env("POSTWERK_TLS_CERT", ""),
		TLSKeyFile:        env("POSTWERK_TLS_KEY", ""),
		CertsDir:          env("POSTWERK_CERTS_DIR", "/certs"),
		AdminEmail:        env("POSTWERK_ADMIN_EMAIL", ""),
		AdminPassword:     env("POSTWERK_ADMIN_PASSWORD", ""),
		MetricsToken:      env("POSTWERK_METRICS_TOKEN", ""),
		RelayHost:         env("POSTWERK_RELAY_HOST", ""),
		RelayUser:         env("POSTWERK_RELAY_USER", ""),
		RelayPassword:     env("POSTWERK_RELAY_PASSWORD", ""),
		AllowInsecureAuth: envBool("POSTWERK_ALLOW_INSECURE_AUTH", false),
		SecureCookies:     envBool("POSTWERK_SECURE_COOKIES", false),
		TrustProxy:        envBool("POSTWERK_TRUST_PROXY", false),
		HTTPAlwaysOpen:    envBool("POSTWERK_HTTP_ALWAYS_OPEN", false),
		HealthAddr:        env("POSTWERK_HEALTH_ADDR", "127.0.0.1:8099"),
		Debug:             envBool("POSTWERK_DEBUG", false),
	}
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return strings.TrimSpace(v)
	}
	return def
}

func envBool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return def
	}
	return b
}
