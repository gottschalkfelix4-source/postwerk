# Roadmap

Postwerk wird in Meilensteinen gebaut. Jeder Meilenstein ergibt einen lauffähigen Stand.

Legende: ✅ fertig · 🟡 teilweise · ⏳ geplant

## Meilenstein 1 – Fundament (aktueller Stand)

| Bereich | Funktion | Status |
|---|---|---|
| Protokolle | SMTP eingehend (Port 25) mit STARTTLS, PIPELINING, SIZE, 8BITMIME | ✅ |
| | Submission 587 (STARTTLS) und 465 (TLS) mit AUTH PLAIN/LOGIN | ✅ |
| | IMAP4rev1/rev2 mit IDLE, MOVE, UIDPLUS, SPECIAL-USE, LIST-STATUS, ESEARCH | ✅ |
| | POP3 / POP3S mit STLS, UIDL, TOP | ✅ |
| | Ausgehende Warteschlange: MX-Auflösung, Wiederholungen, Bounces, optionaler Smarthost | ✅ |
| TLS | Let’s Encrypt über ACME (HTTP-01/TLS-ALPN-01), eigene Zertifikate, selbstsigniert als Fallback | ✅ |
| Authentifizierung | SPF-, DKIM- und DMARC-Prüfung eingehend (auch Ed25519-Signaturen), DKIM-Signatur ausgehend | ✅ |
| | DKIM-Schlüsselwechsel je Domain (neu → prüfen → aktivieren, alter Schlüssel 7 Tage veröffentlicht), Doppelsignatur RSA + Ed25519 (RFC 8463) | ✅ |
| | ARC (RFC 8617): Kette eingehend prüfen, vertrauenswürdige Weiterleiter schwächen DMARC-Ablehnungen ab, weitergeleitete Mails werden versiegelt | ✅ |
| | DMARC-Aggregat- und TLS-RPT-Berichte (RFC 8460) empfangen, auswerten und unter „Berichte“ anzeigen, Berichtsmails auf Wunsch nicht zustellen | ✅ |
| | postmaster@ und abuse@ für jede Domain automatisch (an das erste Admin-Konto oder eine eingestellte Adresse) | ✅ |
| Verzeichnis | Mehrere Domains, Konten, Aliase (auch extern), Catch-all, Quotas | ✅ |
| | Sub-Adressierung (`name+stichwort@domain`) | ✅ |
| | Empfohlene DNS-Einträge je Domain (MX, SPF, DKIM, DMARC, MTA-STS, TLS-RPT, SRV) | ✅ |
| Filter | Spam-Scoring (Auth-Ergebnisse, Header-Heuristiken, Phishing-Muster, gefährliche Anhänge) | ✅ |
| | Bayes-Filter mit Auto-Learn und Training über „Spam / kein Spam“ | ✅ |
| | DNSBL (IPv4 und IPv6 nach RFC 5782), Greylisting | ✅ |
| | Virenscan über ClamAV (INSTREAM) mit Ablehnen oder Quarantäne | ✅ |
| Sicherheit | 2FA (TOTP), App-Passwörter, Brute-Force-Sperre für Web, SMTP, IMAP und POP3 | ✅ |
| Betrieb | Web-Verwaltung, REST-API, Live-Logs, Prometheus-Metriken, Dashboard | ✅ |
| | Backup im Web-Interface: Archive lokal, per WebDAV oder SMB, Zeitplan, Aufbewahrung, AES-256-Verschlüsselung, Download/Upload, Wiederherstellung per Klick | ✅ |
| | Backup-/Restore-Befehl, Notfall-Passwort-Reset | ✅ |
| | MTA-STS-Policy unter `/.well-known/mta-sts.txt` (Modus none/testing/enforce umschaltbar, Zertifikatsprüfung vor „enforce“) | ✅ |
| | JMAP (RFC 8620/8621) als zusätzliche Client-API, siehe [API.md](API.md#jmap-rfc-8620--rfc-8621) | ✅ |
| Webmail | Eigenes Webmail mit Ordnern, Threads, Suche, Tags, Entwürfen, Anhängen (über die REST-API) | ✅ |
| | Abwesenheitsnotiz | ✅ |
| | Filterregeln (Sieve) mit Regel-Editor, Experten-Modus und ManageSieve | ✅ |
| Einrichtung | Setup-Assistent mit Setup-Token, Live-DNS-Prüfung und System-Check (PTR, Port 25, ClamAV, KI) | ✅ |
| | DNS-Einträge automatisch beim Anbieter setzen: IONOS, Hetzner, Cloudflare, netcup, INWX, deSEC (offizielle APIs), STRATO (inoffiziell, TXT/CNAME) | ✅ |
| | Reverse-Proxy: Erkennung, vertrauenswürdige Proxys, fertige Konfigurationen (NPM, Traefik, Caddy, nginx, SWAG), Zertifikat vom Proxy übernehmen inkl. Traefik-acme.json, automatisches Neuladen | ✅ |
| | Dynamische IP: DynDNS (STRATO, INWX, deSEC, No-IP, dyndns2) nur bei IP-Wechsel, Smarthost mit Vorlagen und Verbindungstest, SPF-Include automatisch | ✅ |
| Umzug | Migrationsassistent per IMAP (Ordnerzuordnung, Flags, Datum, Duplikaterkennung, CSV für viele Postfächer) | ✅ |
| | Import von .mbox, .eml und .zip im Webmail | ✅ |
| KI | OpenAI-kompatible Endpunkte (Ollama, vLLM, LocalAI, OpenAI …) und Anthropic-kompatible API, eigener Embedding-Endpunkt | ✅ |
| | LLM-Spam- und Phishing-Erkennung | ✅ |
| | Schutz vor Prompt-Injection (nur sichtbarer Text, Zufallsmarkierungen, Verdachtserkennung ohne automatische Ablage, Warnhinweis im Webmail) | ✅ |
| | Kategorien und Tags, Priorität / dringende Mails | ✅ |
| | Automatisch in Ordner einsortieren (je Kategorie oder in die eigenen Ordner der Benutzer) | ✅ |
| | Zusammenfassungen von Mails und Threads | ✅ |
| | Antwortentwürfe mit Ton und eigener Anweisung | ✅ |
| | Semantische Suche über Embeddings | ✅ |
| | Termine und Aufgaben extrahieren, in Kalender/Aufgabenliste übernehmen | ✅ |
| | Anhänge an Paperless-ngx (manuell oder automatisch nach Kategorie) | ✅ |

## Meilenstein 2 – Clients und Filterregeln

- ⏳ IMAP CONDSTORE/QRESYNC für schnelleren Abgleich großer Postfächer (das JMAP-Änderungsprotokoll führt bereits eine Modseq pro Konto)
- ✅ Sieve (RFC 5228) mit fileinto, reject/ereject, envelope, imap4flags, copy, body, variables, vacation, relational, regex, subaddress, date und mailbox; Probelauf über die API
- ✅ ManageSieve (RFC 5804, Port 4190, STARTTLS) für Mailprogramme wie Thunderbird
- ✅ Regel-Editor „Filterregeln“ im Webmail mit Experten-Modus und Test gegen Nachrichten aus dem Posteingang
- ✅ Sub-Adressierung: `name+stichwort@domain` landet bei `name@domain`, Sieve sieht das Stichwort über `:detail`
- ⏳ Abwesenheitsnotiz der Weboberfläche auf Sieve `vacation` umstellen (läuft vorerst getrennt, nach dem Skript)
- ✅ JMAP Core + Mail + Submission (RFC 8620/8621) mit Push per EventSource, Upload/Download und Änderungsprotokoll
- ⏳ Webmail auf JMAP umstellen (das Webmail nutzt weiterhin die REST-API)
- ⏳ Autoconfig/Autodiscover-Endpunkte für Mailclients

## Meilenstein 3 – Groupware und Zustellbarkeit

- ✅ CalDAV und CardDAV (Kalender, Kontakte, Aufgaben als VTODO, sync-collection, mehrere Kalender/Adressbücher); der interne Kalender ist darauf migriert, Kontakte und Adress-Vervollständigung im Webmail, Apple-Profil
- ✅ MTA-STS ausgehend erzwingen (RFC 8461, Modus enforce/testing) und DANE/TLSA prüfen (RFC 7672, wenn der Resolver DNSSEC validiert)
- ✅ MTA-STS-Modus der eigenen Policy in der Oberfläche umschalten; die `id` im TXT-Eintrag folgt der Policy
- ✅ Aufräumen: ausgemusterte DKIM-Schlüssel und alte CalDAV/CardDAV-Löschvermerke (sync-token danach ungültig, RFC 6578) automatisch entfernen; JMAP-IDs werden nie wiederverwendet
- ✅ ARC-Signatur und -Prüfung (wichtig für Weiterleitungen), SRS für Alias-Weiterleitungen
- ✅ DMARC- und TLS-RPT-Berichte empfangen und auswerten
- ⏳ DMARC- und TLS-RPT-Berichte selbst versenden
- ✅ DNSBL-Abfragen für IPv6-Absender (RFC 5782), auch für die eigene IPv6-Adresse im Dashboard
- ✅ Sendelimits pro Konto (Empfänger pro Nachricht, Nachrichten pro Stunde, externe Empfänger pro Tag) mit Ausnahmen pro Konto
- ✅ Benachrichtigungen bei Betriebswarnungen per Mail und Webhook (neu, tägliche Erinnerung, behoben)
- ⏳ Weiterer ausgehender Spam-Schutz (z. B. Inhaltsprüfung ausgehender Mails)

## Meilenstein 4 – Identität und Datenschutz

- ✅ LDAP-Verzeichnis als Kontenquelle (Bind-Prüfung für Web, IMAP, SMTP und POP3, automatische Kontoanlage, Admin-Gruppe, Abgleich)
- ✅ OIDC-SSO (z. B. Authentik, Keycloak) für die Weboberfläche: Authorization Code mit PKCE, automatische Kontoanlage, Admin-Claim, „nur SSO“ mit lokalem Notzugang ([Anleitung](SSO.md))
- ⏳ OAUTHBEARER/XOAUTH2 für Mail-Programme (bis dahin App-Passwörter)
- ✅ Verschlüsselung im Ruhezustand: eingehende Mails mit dem S/MIME- oder OpenPGP-Schlüssel des Kontos verschlüsseln (PGP/MIME bzw. S/MIME, optional mit verborgenem Betreff, bestehende Nachrichten per Hintergrundauftrag)
- ✅ Geheimnisse in der Datenbank (API-Schlüssel, Passwörter, TOTP, DKIM, SRS) mit Master-Schlüssel verschlüsselt (AES-256-GCM, Schlüssel aus Umgebungsvariable, Datei oder `master.key`)
- ✅ Optionale Speicherverschlüsselung aller Nachrichten-Dateien mit dem Master-Schlüssel inkl. Umwandlung vorhandener Dateien
- ✅ DSGVO-Export: alle Daten eines Kontos als ZIP (mbox je Ordner, iCalendar je Kalender, vCard je Adressbuch, Kontodaten, Sitzungen), für Benutzer selbst und für Admins
- ⏳ Backups: S3-Ziel, Wiederherstellen einzelner Postfächer aus einem Archiv
- ⏳ Postgres als alternativer Metadatenspeicher, S3 für Blobs

## Meilenstein 5 – Feinschliff

- ⏳ Mehrsprachige Oberfläche, Rich-Text-Editor im Composer, Signaturen, Identitäten
- ⏳ Push-Benachrichtigungen im Browser, PWA
- ⏳ Mailing-Listen, Gruppenpostfächer, freigegebene Ordner
