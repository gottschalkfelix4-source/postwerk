package migrate

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// DiagStep is one stage of the connection diagnosis.
type DiagStep struct {
	Step   string `json:"step"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// Diagnosis explains why a connection to the source server failed.
type Diagnosis struct {
	Steps []DiagStep `json:"diagnostics"`
	Hint  string     `json:"hint"`
}

func tcpReachable(ctx context.Context, addr string, timeout time.Duration) error {
	d := net.Dialer{Timeout: timeout}
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	c.Close()
	return nil
}

func shortErr(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "timeout"):
		return "Zeitüberschreitung (keine Antwort)"
	case strings.Contains(msg, "refused"):
		return "Verbindung abgelehnt"
	case strings.Contains(msg, "no such host"):
		return "Name nicht gefunden"
	case strings.Contains(msg, "network is unreachable"):
		return "Netz nicht erreichbar"
	}
	return msg
}

// Diagnose checks DNS, internet access, the port (and the alternative IMAP
// port), TLS and finally the login, and derives a concrete hint. prevErr is
// the error of the attempt that is being diagnosed: if it was a rejected
// login, no second login is made (providers block IPs after repeated failures).
func Diagnose(ctx context.Context, src Source, user, pass string, prevErr error) Diagnosis {
	var dg Diagnosis
	add := func(step string, ok bool, detail string) { dg.Steps = append(dg.Steps, DiagStep{step, ok, detail}) }

	ips, err := net.DefaultResolver.LookupIPAddr(ctx, src.Host)
	if err != nil || len(ips) == 0 {
		add("Servername auflösen", false, fmt.Sprintf("%s: %s", src.Host, shortErr(err)))
		dg.Hint = "Der Servername lässt sich nicht auflösen. Schreibweise prüfen; falls sie stimmt, hat der Postwerk-Container ein DNS-Problem."
		return dg
	}
	all := []string{}
	for _, ip := range ips {
		all = append(all, ip.IP.String())
	}
	add("Servername auflösen", true, src.Host+" → "+strings.Join(all, ", "))

	// General internet access (independent of the mail provider).
	internet := false
	for _, probe := range []string{"1.1.1.1:443", "8.8.8.8:443", "9.9.9.9:443"} {
		if tcpReachable(ctx, probe, 4*time.Second) == nil {
			internet = true
			break
		}
	}
	if internet {
		add("Internetzugang des Servers", true, "ausgehende Verbindungen funktionieren")
	} else {
		add("Internetzugang des Servers", false, "weder 1.1.1.1 noch 8.8.8.8 noch 9.9.9.9 erreichbar")
	}

	port := strconv.Itoa(src.Port)
	target := net.JoinHostPort(src.Host, port)
	if err := tcpReachable(ctx, target, 8*time.Second); err != nil {
		add("Verbindung zu "+target, false, shortErr(err))
		alt, altSec := 143, "starttls"
		if src.Port == 143 {
			alt, altSec = 993, "tls"
		}
		altAddr := net.JoinHostPort(src.Host, strconv.Itoa(alt))
		altErr := tcpReachable(ctx, altAddr, 6*time.Second)
		if altErr == nil {
			add("Verbindung zu "+altAddr, true, "erreichbar")
		} else {
			add("Verbindung zu "+altAddr, false, shortErr(altErr))
		}
		switch {
		case !internet:
			dg.Hint = "Der Server, auf dem Postwerk läuft, kommt nicht ins Internet. Das liegt nicht an Benutzername oder Passwort. Prüfe das Docker-Netzwerk des Containers (Unraid: Netzwerktyp „bridge“), eine Firewall oder ein VPN, über das der Server ins Netz geht."
		case altErr == nil:
			dg.Hint = fmt.Sprintf("Port %d ist von deinem Server aus blockiert, Port %d aber erreichbar. Stelle die Verschlüsselung auf %s (Port %d) um.", src.Port, alt, map[string]string{"tls": "SSL/TLS", "starttls": "STARTTLS"}[altSec], alt)
		default:
			dg.Hint = fmt.Sprintf("Der Server hat Internet, erreicht %s aber weder auf Port %d noch auf %d. Das liegt nicht an Benutzername oder Passwort. Mögliche Ursachen: eine Router- oder Firewall-Regel bzw. ein VPN blockiert ausgehende Mail-Ports, oder der Anbieter hat deine IP nach mehreren Versuchen vorübergehend gesperrt – dann in 15–30 Minuten erneut versuchen.", src.Host, src.Port, alt)
		}
		return dg
	}
	add("Verbindung zu "+target, true, "erreichbar")

	if src.Security == "tls" {
		d := &net.Dialer{Timeout: 10 * time.Second}
		conn, err := tls.DialWithDialer(d, "tcp", target, &tls.Config{ServerName: src.Host, MinVersion: tls.VersionTLS12})
		if err != nil {
			add("TLS-Verschlüsselung", false, err.Error())
			dg.Hint = "Die Verbindung steht, aber TLS scheitert. Passt die Verschlüsselung zum Port (993 = SSL/TLS, 143 = STARTTLS)?"
			return dg
		}
		conn.Close()
		add("TLS-Verschlüsselung", true, "Zertifikat gültig")
	}

	loginErr := prevErr
	if loginErr == nil || !strings.Contains(loginErr.Error(), "Anmeldung fehlgeschlagen") {
		c, derr := dial(ctx, src, user, pass)
		if derr == nil {
			c.Logout()
		}
		loginErr = derr
	}
	if loginErr != nil {
		add("Anmeldung", false, loginErr.Error())
		dg.Hint = "Der Server ist erreichbar, lehnt aber die Anmeldung ab. Prüfe Benutzername und Passwort – meist ist der Benutzername die vollständige E-Mail-Adresse. Bei Gmail, iCloud, Outlook und GMX ist oft ein App-Passwort nötig und IMAP muss aktiviert sein."
		return dg
	}
	add("Anmeldung", true, "erfolgreich")
	return dg
}
