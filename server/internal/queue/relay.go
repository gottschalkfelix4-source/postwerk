package queue

// RelayPreset describes a common smarthost.
type RelayPreset struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Host         string  `json:"host"`
	Port         int     `json:"port"`
	Security     string  `json:"security"`
	UsernameHint string  `json:"usernameHint"`
	SPFInclude   string  `json:"spfInclude"`
	DocsURL      *string `json:"docsUrl"`
	Notes        *string `json:"notes"`
}

func sp(s string) *string { return &s }

// RelayPresets lists well-known smarthosts. The SPF includes were checked
// against the providers' published records.
var RelayPresets = []RelayPreset{
	{ID: "strato", Name: "STRATO", Host: "smtp.strato.de", Port: 465, Security: "tls",
		UsernameHint: "Vollständige E-Mail-Adresse eines STRATO-Postfachs", SPFInclude: "include:_spf.strato.com",
		DocsURL: sp("https://www.strato.de/faq/mail/e-mailserver-adressen-ports-ssl-tls/"),
		Notes:   sp("Benötigt ein STRATO-Mailpostfach. Port 465 (TLS) ist die Empfehlung von STRATO; 587 mit STARTTLS geht ebenfalls.")},
	{ID: "ionos", Name: "IONOS", Host: "smtp.ionos.de", Port: 587, Security: "starttls",
		UsernameHint: "Vollständige E-Mail-Adresse eines IONOS-Postfachs", SPFInclude: "include:_spf-eu.ionos.com"},
	{ID: "brevo", Name: "Brevo (Sendinblue)", Host: "smtp-relay.brevo.com", Port: 587, Security: "starttls",
		UsernameHint: "SMTP-Login aus dem Brevo-Konto", SPFInclude: "include:spf.brevo.com",
		Notes: sp("Die Absender-Domain muss bei Brevo verifiziert sein.")},
	{ID: "mailjet", Name: "Mailjet", Host: "in-v3.mailjet.com", Port: 587, Security: "starttls",
		UsernameHint: "API-Key", SPFInclude: "include:spf.mailjet.com"},
	{ID: "smtp2go", Name: "SMTP2GO", Host: "mail.smtp2go.com", Port: 587, Security: "starttls",
		UsernameHint: "SMTP-Benutzer aus SMTP2GO", SPFInclude: "include:spf.smtp2go.com"},
	{ID: "ses", Name: "Amazon SES (Frankfurt)", Host: "email-smtp.eu-central-1.amazonaws.com", Port: 587, Security: "starttls",
		UsernameHint: "SES-SMTP-Benutzername", SPFInclude: "include:amazonses.com"},
	{ID: "custom", Name: "Eigener Smarthost", Port: 587, Security: "starttls", UsernameHint: "Benutzername"},
}
