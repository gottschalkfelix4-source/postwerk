package api

import (
	"context"
	"crypto/sha256"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"

	"postwerk/internal/dav"
	"postwerk/internal/store"
)

func (s *Server) pimRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/calendars", s.user(s.listCollections(store.KindCalendar)))
	m.HandleFunc("POST /api/calendars", s.user(s.createCollection(store.KindCalendar)))
	m.HandleFunc("PATCH /api/calendars/{id}", s.user(s.patchCollection(store.KindCalendar)))
	m.HandleFunc("DELETE /api/calendars/{id}", s.user(s.deleteCollection(store.KindCalendar)))
	m.HandleFunc("GET /api/calendars/{id}/export", s.user(s.exportCalendar))
	m.HandleFunc("POST /api/calendars/{id}/import", s.user(s.importCalendar))

	m.HandleFunc("GET /api/addressbooks", s.user(s.listCollections(store.KindAddressBook)))
	m.HandleFunc("POST /api/addressbooks", s.user(s.createCollection(store.KindAddressBook)))
	m.HandleFunc("PATCH /api/addressbooks/{id}", s.user(s.patchCollection(store.KindAddressBook)))
	m.HandleFunc("DELETE /api/addressbooks/{id}", s.user(s.deleteCollection(store.KindAddressBook)))
	m.HandleFunc("GET /api/contacts", s.user(s.listContacts))
	m.HandleFunc("POST /api/contacts", s.user(s.createContact))
	m.HandleFunc("GET /api/contacts/suggest", s.user(s.suggestContacts))
	m.HandleFunc("GET /api/contacts/export", s.user(s.exportContacts))
	m.HandleFunc("POST /api/contacts/import", s.user(s.importContacts))
	m.HandleFunc("GET /api/contacts/{id}", s.user(s.getContact))
	m.HandleFunc("PUT /api/contacts/{id}", s.user(s.updateContact))
	m.HandleFunc("DELETE /api/contacts/{id}", s.user(s.deleteContact))

	m.HandleFunc("GET /api/account/dav", s.user(s.davInfo))
	m.HandleFunc("GET /api/account/apple-profile", s.user(s.appleProfile))
}

// davHandler builds the CalDAV/CardDAV server. It authenticates every
// request with HTTP Basic (account or app password) through the brute-force
// guard and only over HTTPS (directly or via a trusted proxy).
func (s *Server) davHandler() http.Handler {
	return &dav.Handler{
		Store: s.Store,
		Authenticate: func(ctx context.Context, ip, user, pass string) (*store.Account, error) {
			return s.Auth.Protocol(ctx, ip, user, pass, "dav")
		},
		Client: func(r *http.Request) (string, bool) {
			ci := s.resolveClient(r)
			return ci.client, ci.https
		},
		AllowInsecure: func() bool { return s.Cfg.AllowInsecureAuth },
		Log:           s.Log,
	}
}

func (s *Server) mountDAV(mux *http.ServeMux) {
	h := s.davHandler()
	mux.Handle(dav.Prefix+"/", h)
	mux.Handle(dav.Prefix, h)
	mux.Handle("/.well-known/caldav", h)
	mux.Handle("/.well-known/carddav", h)
}

func (s *Server) davBase() string {
	return "https://" + s.Settings.Get().Server.Hostname
}

func (s *Server) davInfo(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	base := s.davBase()
	e := url.PathEscape(u.Email)
	return map[string]string{
		"caldavUrl":    base + dav.Prefix + "/calendars/" + e + "/",
		"carddavUrl":   base + dav.Prefix + "/addressbooks/" + e + "/",
		"principalUrl": base + dav.Prefix + "/principals/" + e + "/",
		"server":       s.Settings.Get().Server.Hostname,
		"username":     u.Email,
	}, nil
}

// profileUUID derives a stable payload UUID, so installing the profile again
// replaces the previous one.
func profileUUID(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	b := sum[:16]
	b[6] = b[6]&0x0f | 0x50
	b[8] = b[8]&0x3f | 0x80
	return strings.ToUpper(fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]))
}

// appleProfile serves an unsigned configuration profile (.mobileconfig) for
// iOS/macOS with mail (IMAP/SMTP), CalDAV and CardDAV. It contains no
// password; the device asks for it during installation.
func (s *Server) appleProfile(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	host := s.Settings.Get().Server.Hostname
	x := html.EscapeString
	email, name := x(u.Email), x(u.Name)
	if name == "" {
		name = email
	}
	principal := x(dav.Prefix + "/principals/" + url.PathEscape(u.Email) + "/")
	id := "de.postwerk." + strings.NewReplacer("@", ".", "+", ".").Replace(strings.ToLower(u.Email))
	uuid := func(kind string) string { return profileUUID(host, u.Email, kind) }
	body := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>PayloadContent</key>
	<array>
		<dict>
			<key>EmailAccountDescription</key><string>%[1]s</string>
			<key>EmailAccountName</key><string>%[2]s</string>
			<key>EmailAccountType</key><string>EmailTypeIMAP</string>
			<key>EmailAddress</key><string>%[1]s</string>
			<key>IncomingMailServerAuthentication</key><string>EmailAuthPassword</string>
			<key>IncomingMailServerHostName</key><string>%[3]s</string>
			<key>IncomingMailServerPortNumber</key><integer>993</integer>
			<key>IncomingMailServerUseSSL</key><true/>
			<key>IncomingMailServerUsername</key><string>%[1]s</string>
			<key>OutgoingMailServerAuthentication</key><string>EmailAuthPassword</string>
			<key>OutgoingMailServerHostName</key><string>%[3]s</string>
			<key>OutgoingMailServerPortNumber</key><integer>465</integer>
			<key>OutgoingMailServerUseSSL</key><true/>
			<key>OutgoingMailServerUsername</key><string>%[1]s</string>
			<key>OutgoingPasswordSameAsIncomingPassword</key><true/>
			<key>PayloadDisplayName</key><string>E-Mail (%[1]s)</string>
			<key>PayloadIdentifier</key><string>%[5]s.mail</string>
			<key>PayloadType</key><string>com.apple.mail.managed</string>
			<key>PayloadUUID</key><string>%[6]s</string>
			<key>PayloadVersion</key><integer>1</integer>
		</dict>
		<dict>
			<key>CalDAVAccountDescription</key><string>Kalender (%[1]s)</string>
			<key>CalDAVHostName</key><string>%[3]s</string>
			<key>CalDAVPort</key><integer>443</integer>
			<key>CalDAVPrincipalURL</key><string>%[4]s</string>
			<key>CalDAVUseSSL</key><true/>
			<key>CalDAVUsername</key><string>%[1]s</string>
			<key>PayloadDisplayName</key><string>Kalender (%[1]s)</string>
			<key>PayloadIdentifier</key><string>%[5]s.caldav</string>
			<key>PayloadType</key><string>com.apple.caldav.account</string>
			<key>PayloadUUID</key><string>%[7]s</string>
			<key>PayloadVersion</key><integer>1</integer>
		</dict>
		<dict>
			<key>CardDAVAccountDescription</key><string>Kontakte (%[1]s)</string>
			<key>CardDAVHostName</key><string>%[3]s</string>
			<key>CardDAVPort</key><integer>443</integer>
			<key>CardDAVPrincipalURL</key><string>%[4]s</string>
			<key>CardDAVUseSSL</key><true/>
			<key>CardDAVUsername</key><string>%[1]s</string>
			<key>PayloadDisplayName</key><string>Kontakte (%[1]s)</string>
			<key>PayloadIdentifier</key><string>%[5]s.carddav</string>
			<key>PayloadType</key><string>com.apple.carddav.account</string>
			<key>PayloadUUID</key><string>%[8]s</string>
			<key>PayloadVersion</key><integer>1</integer>
		</dict>
	</array>
	<key>PayloadDescription</key><string>E-Mail, Kalender und Kontakte für %[1]s. Das Passwort (bei aktivierter Zwei-Faktor-Anmeldung ein App-Passwort) wird bei der Installation abgefragt.</string>
	<key>PayloadDisplayName</key><string>Postwerk: %[1]s</string>
	<key>PayloadIdentifier</key><string>%[5]s</string>
	<key>PayloadRemovalDisallowed</key><false/>
	<key>PayloadType</key><string>Configuration</string>
	<key>PayloadUUID</key><string>%[9]s</string>
	<key>PayloadVersion</key><integer>1</integer>
</dict>
</plist>
`, email, name, x(host), principal, x(id), uuid("mail"), uuid("caldav"), uuid("carddav"), uuid("profile"))
	w.Header().Set("Content-Type", "application/x-apple-aspen-config")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, downloadName("postwerk-"+u.Email, ".mobileconfig")))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(body))
	return nil, nil
}
