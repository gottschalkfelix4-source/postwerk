# Roadmap

Postwerk wird in Meilensteinen gebaut. Jeder Meilenstein ergibt einen lauffähigen Stand.

Legende: ✅ fertig · 🟡 teilweise · ⏳ geplant

## Meilenstein 1 – Fundament (aktueller Stand)

| Bereich | Funktion | Status |
|---|---|---|
| Protokolle | SMTP eingehend (Port 25) mit STARTTLS, PIPELINING, SIZE, 8BITMIME | ✅ |
| | Submission 587 (STARTTLS) und 465 (TLS) mit AUTH PLAIN/LOGIN | ✅ |
| | POP3 / POP3S mit STLS, UIDL, TOP | ✅ |
| | Ausgehende Warteschlange: MX-Auflösung, Wiederholungen, Bounces, optionaler Smarthost | ✅ |
| TLS | Let’s Encrypt über ACME (HTTP-01/TLS-ALPN-01), eigene Zertifikate, selbstsigniert als Fallback | ✅ |
| Authentifizierung | SPF-, DKIM- und DMARC-Prüfung eingehend, DKIM-Signatur ausgehend (RSA-2048) | ✅ |
| Verzeichnis | Mehrere Domains, Konten, Aliase (auch extern), Catch-all, Quotas | ✅ |
| | Empfohlene DNS-Einträge je Domain (MX, SPF, DKIM, DMARC, MTA-STS, TLS-RPT, SRV) | ✅ |
| Filter | Spam-Scoring (Auth-Ergebnisse, Header-Heuristiken, Phishing-Muster, gefährliche Anhänge) | ✅ |
| | Bayes-Filter mit Auto-Learn und Training über „Spam / kein Spam“ | ✅ |
| | DNSBL, Greylisting | ✅ |
| | Virenscan über ClamAV (INSTREAM) mit Ablehnen oder Quarantäne | ✅ |
| Sicherheit | 2FA (TOTP), App-Passwörter, Brute-Force-Sperre für Web, SMTP und POP3 | ✅ |
| Betrieb | Web-Verwaltung, REST-API, Live-Logs, Prometheus-Metriken, Dashboard | ✅ |
| | Backup-Befehl (konsistenter Snapshot im laufenden Betrieb), Notfall-Passwort-Reset | ✅ |
| | MTA-STS-Policy unter `/.well-known/mta-sts.txt` (Modus „testing“) | ✅ |
| Webmail | Eigenes Webmail mit Ordnern, Threads, Suche, Tags, Entwürfen, Anhängen | ✅ |
| | Abwesenheitsnotiz | ✅ |
| Einrichtung | Setup-Assistent mit Setup-Token, Live-DNS-Prüfung und System-Check (PTR, Port 25, ClamAV, KI) | ✅ |
| | DNS-Einträge automatisch beim Anbieter setzen: IONOS, Hetzner, Cloudflare, netcup, INWX, deSEC (offizielle APIs), STRATO (inoffiziell, TXT/CNAME) | ✅ |
| | Reverse-Proxy: Erkennung, vertrauenswürdige Proxys, fertige Konfigurationen (NPM, Traefik, Caddy, nginx, SWAG), Zertifikat vom Proxy übernehmen inkl. Traefik-acme.json, automatisches Neuladen | ✅ |
| | Dynamische IP: DynDNS (STRATO, INWX, deSEC, No-IP, dyndns2) nur bei IP-Wechsel, Smarthost mit Vorlagen und Verbindungstest, SPF-Include automatisch | ✅ |
| Umzug | Migrationsassistent per IMAP (Ordnerzuordnung, Flags, Datum, Duplikaterkennung, CSV für viele Postfächer) | ✅ |
| | Import von .mbox, .eml und .zip im Webmail | ✅ |
| KI | OpenAI-kompatible Endpunkte (Ollama, vLLM, LocalAI, OpenAI …) | ✅ |
| | LLM-Spam- und Phishing-Erkennung | ✅ |
| | Kategorien und Tags, Priorität / dringende Mails | ✅ |
| | Zusammenfassungen von Mails und Threads | ✅ |
| | Antwortentwürfe mit Ton und eigener Anweisung | ✅ |
| | Semantische Suche über Embeddings | ✅ |
| | Termine und Aufgaben extrahieren, in Kalender/Aufgabenliste übernehmen | ✅ |
| | Anhänge an Paperless-ngx (manuell oder automatisch nach Kategorie) | ✅ |

## Meilenstein 2 – Clients und Filterregeln

- ⏳ IMAP4rev2 (RFC 9051) inkl. IDLE, CONDSTORE, MOVE – damit Thunderbird, Apple Mail, K-9 vollständig funktionieren
- ⏳ Sieve (RFC 5228) mit ManageSieve, Regel-Editor im Webmail; Abwesenheitsnotiz wandert auf Sieve `vacation`
- ⏳ JMAP Core + Mail (RFC 8620/8621) als moderne API, Webmail auf JMAP umstellen
- ⏳ Autoconfig/Autodiscover-Endpunkte für Mailclients

## Meilenstein 3 – Groupware und Zustellbarkeit

- ⏳ CalDAV und CardDAV (Kalender, Kontakte, Aufgaben als VTODO); der interne Kalender wird darauf migriert
- ⏳ MTA-STS-Policy ausliefern und ausgehend erzwingen, DANE (TLSA) ausgehend prüfen
- ⏳ ARC-Signatur und -Prüfung (wichtig für Weiterleitungen), SRS für Alias-Weiterleitungen
- ⏳ DMARC- und TLS-RPT-Berichte empfangen und auswerten
- ⏳ IPv6-DNSBL, Rate-Limits pro Konto, ausgehender Spam-Schutz

## Meilenstein 4 – Identität und Datenschutz

- ⏳ LDAP-Verzeichnis als Kontenquelle
- ⏳ OIDC-SSO (z. B. Authentik, Keycloak) für die Weboberfläche, OAUTHBEARER für Clients
- ⏳ Verschlüsselung im Ruhezustand: eingehende Mails mit dem S/MIME- oder OpenPGP-Schlüssel des Kontos verschlüsseln
- ⏳ Backups: Zeitplan in der Oberfläche, Aufbewahrung, S3-Ziel, Export/Import einzelner Postfächer
- ⏳ Postgres als alternativer Metadatenspeicher, S3 für Blobs

## Meilenstein 5 – Feinschliff

- ⏳ Mehrsprachige Oberfläche, Rich-Text-Editor im Composer, Signaturen, Identitäten
- ⏳ Push-Benachrichtigungen im Browser, PWA
- ⏳ Mailing-Listen, Gruppenpostfächer, freigegebene Ordner
