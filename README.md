# Postwerk

Ein selbst gehosteter Mailserver in einem einzigen Go-Binary – mit eigener Weboberfläche für
Verwaltung und Webmail und KI-Funktionen, die komplett lokal laufen können.

- **Protokolle:** SMTP, Submission (587/465), IMAP mit IDLE (993/143), POP3, Weboberfläche + REST-API
- **Sicherheit:** TLS mit Let’s Encrypt, SPF/DKIM/DMARC, Spamfilter mit Bayes, DNSBL und Greylisting,
  ClamAV, 2FA, App-Passwörter, Brute-Force-Sperre
- **Einrichtung & Umzug:** Setup-Assistent mit Live-DNS-Prüfung und automatischem Eintragen der
  DNS-Records bei IONOS, STRATO, Hetzner, Cloudflare, netcup, INWX und deSEC, Migrationsassistent für den Umzug per
  IMAP von Gmail, Outlook, mailcow & Co., Import von mbox/eml-Dateien
- **KI:** Spam-/Phishing-Erkennung, Kategorien, Prioritäten, automatisches Einsortieren in Ordner,
  Zusammenfassungen, Antwortentwürfe, semantische Suche, Termine/Aufgaben aus Mails, Paperless-ngx-Anbindung – **lokal mit Ollama**
  oder über externe Dienste mit **OpenAI-kompatibler** (OpenAI, OpenRouter, Mistral, vLLM …) oder
  **Anthropic-kompatibler** API (Claude)

**Linux-VPS:** fertige Compose-Datei zum Kopieren, siehe [docs/VPS.md](docs/VPS.md):

```bash
mkdir -p /opt/postwerk && cd /opt/postwerk
curl -fsSL https://raw.githubusercontent.com/gottschalkfelix4-source/postwerk/main/deploy/vps/docker-compose.yml -o docker-compose.yml
nano docker-compose.yml   # POSTWERK_HOSTNAME eintragen
docker compose up -d
```

**Unraid:** Template-Installation mit einem Befehl, siehe [docs/UNRAID.md](docs/UNRAID.md):

```bash
curl -fsSL https://raw.githubusercontent.com/gottschalkfelix4-source/postwerk/main/unraid/install.sh | bash
```

Was schon fertig ist und was noch kommt (IMAP, JMAP, Sieve, CalDAV/CardDAV, LDAP/OIDC …), steht in
[docs/ROADMAP.md](docs/ROADMAP.md). Die API ist in [docs/API.md](docs/API.md) beschrieben.

## Schnellstart mit Docker

```bash
git clone https://github.com/gottschalkfelix4-source/postwerk.git && cd postwerk
cp .env.example .env        # Hostname und Admin-Adresse eintragen
docker compose pull         # fertiges Image von ghcr.io (oder: docker compose up -d --build)
docker compose up -d
docker compose exec postwerk postwerk setup-token   # Token für den Setup-Assistenten
```

Dann `http://<server>/` öffnen: Der Setup-Assistent legt Domain und Admin-Konto an, prüft die
DNS-Einträge und richtet TLS, Virenscan und KI ein. Wer lieber alles vorgibt, setzt
`POSTWERK_ADMIN_EMAIL` (und optional `POSTWERK_ADMIN_PASSWORD`) in der `.env`.

Ohne Compose:

```bash
docker run -d --name postwerk --restart unless-stopped \
  -e POSTWERK_HOSTNAME=mail.example.com -e POSTWERK_ADMIN_EMAIL=admin@example.com \
  -p 25:2525 -p 587:5870 -p 465:4650 -p 993:9930 -p 995:9950 -p 80:8080 -p 443:8443 \
  -v postwerk-data:/data ghcr.io/gottschalkfelix4-source/postwerk:latest
```

Mit Virenscanner und lokalem LLM:

```bash
docker compose --profile clamav --profile ai up -d --build
```

In der Oberfläche dann unter **Einstellungen → Virenscan** ClamAV aktivieren (`clamav:3310`) und unter
**Einstellungen → KI** die KI einschalten (Basis-URL `http://ollama:11434/v1`). Die Modelle
(`qwen2.5:7b` und `nomic-embed-text`) lädt der Hilfscontainer `ollama-pull` beim ersten Start.

### Ports

| Host | Container | Zweck |
|---|---|---|
| 25 | 2525 | SMTP eingehend |
| 587 | 5870 | Versand mit STARTTLS |
| 465 | 4650 | Versand mit TLS |
| 143 / 993 | 1430 / 9930 | IMAP / IMAPS |
| 110 / 995 | 1100 / 9950 | POP3 / POP3S |
| 80 / 443 | 8080 / 8443 | Weboberfläche, ACME (über `HTTP_PORT`/`HTTPS_PORT` in `.env` änderbar) |

Der Dienst läuft ohne Root-Rechte als `PUID:PGID` (Standard 1000:1000, auf Unraid 99:100), deshalb
lauschen die Dienste intern auf hohen Ports.

### DNS

Für jede Domain zeigt **Verwaltung → Domains → Domain öffnen** alle nötigen Einträge (MX, SPF, DKIM,
DMARC, MTA-STS, TLS-RPT, SRV) mit Kopier-Knopf. Zusätzlich braucht der Server einen A-Record für den
Hostnamen und einen passenden Reverse-DNS-Eintrag (PTR) beim Provider.

### Reverse-Proxy

Hinter Nginx Proxy Manager, Traefik, Caddy oder SWAG führt der Setup-Assistent (Schritt
„Reverse-Proxy“) durch die Einrichtung und liefert fertige Konfigurationen. Postwerk übernimmt dann
das Zertifikat des Proxys für SMTP/IMAP/POP3 (Ordner schreibgeschützt nach `/certs` einbinden) und lädt
es bei Erneuerung automatisch neu. Die Mail-Ports laufen nicht über den Proxy.

### Let’s Encrypt

Unter **Einstellungen → TLS** „Let's Encrypt direkt“ wählen und die E-Mail eintragen; die Änderung
wirkt ohne Neustart. Port 80 oder 443 muss dafür direkt bei Postwerk ankommen (ohne Reverse-Proxy).

### Dynamische IP oder Port 25 gesperrt?

Bei Heimanschlüssen mit wechselnder IP hält Postwerk den A-Record per **DynDNS** aktuell (STRATO,
INWX, deSEC, No-IP oder beliebiger dyndns2-Dienst) und verschickt ausgehende Mails über einen
**Smarthost** (Vorlagen u. a. für STRATO, IONOS, Brevo, Mailjet, Amazon SES). Beides richtet der
Setup-Assistent im Schritt „Internetanschluss“ ein; der SPF-Eintrag wird automatisch um den Smarthost
ergänzt. Die Umgebungsvariablen `POSTWERK_RELAY_*` funktionieren weiterhin und haben Vorrang.

### Backup und Notfall-Zugang

Unter **Verwaltung → Backup** legst du Sicherungen des gesamten Systems an: Datenbank, alle Mails,
Kalender, Einstellungen, DKIM-Schlüssel und Zertifikate in einem Archiv. Die Archive liegen lokal,
auf einem **WebDAV**-Speicher (Nextcloud, Synology, Hetzner Storage Box …) oder auf einer
**SMB-Freigabe** (NAS, Unraid, Windows). Dazu gibt es Zeitplan, Aufbewahrung, Verschlüsselung mit
Passwort (AES-256), Herunterladen, Hochladen und Wiederherstellen per Klick. Bei einer Wiederherstellung
startet der Server kurz neu, der vorherige Stand bleibt unter `/data/pre-restore-…` erhalten.

```bash
# Sicherung von der Kommandozeile (Standard: /data/backups), läuft auch im Betrieb
docker compose exec postwerk postwerk backup

# Archiv beim nächsten Start einspielen, danach neu starten
docker compose exec postwerk postwerk restore /data/backups/postwerk-20261121-033000.tar.gz.enc
docker compose restart postwerk

# Passwort eines Kontos setzen und 2FA zurücksetzen
docker compose exec postwerk postwerk reset-password admin@example.com 'neues-langes-passwort'
```

## Entwicklung

Voraussetzungen: Go ≥ 1.26, Node.js ≥ 20.

```bash
# Oberfläche bauen (landet in server/internal/webui/dist und wird ins Binary eingebettet)
cd web && npm install && npm run build && cd ..

# Server starten (Standard-Ports für Entwicklung: HTTP 8080, SMTP 2525, Submission 5870, IMAP 1143, POP3 1110)
cd server
POSTWERK_ALLOW_INSECURE_AUTH=true POSTWERK_ADMIN_EMAIL=admin@example.test POSTWERK_ADMIN_PASSWORD=admin-passwort go run ./cmd/postwerk
```

Für die Arbeit an der Oberfläche `npm run dev` in `web/` starten – Vite leitet `/api` an `localhost:8080` weiter.

Tests: `cd server && go test ./...`

### Aufbau

```
server/
  cmd/postwerk/        Einstiegspunkt, verdrahtet alle Dienste
  internal/smtp/       SMTP-Server (eingehend + Submission)
  internal/imapd/      IMAP-Server (IMAP4rev1/rev2, IDLE, MOVE, UIDPLUS, SPECIAL-USE)
  internal/pop3/       POP3-Server
  internal/delivery/   Mailfluss: Prüfungen, Filter, Zustellung, Versand, Abwesenheitsnotiz
  internal/queue/      Ausgehende Warteschlange, MX-Zustellung, Bounces
  internal/mailauth/   SPF, DKIM, DMARC
  internal/spam/       Spam-Scoring, Bayes, DNSBL, Greylisting
  internal/av/         ClamAV-Client
  internal/backup/     Archive, Verschlüsselung, Ziele (lokal, WebDAV, SMB), Zeitplan, Wiederherstellung
  internal/ai/         KI-Client (OpenAI-/Anthropic-kompatibel), Analyse-Worker, Embeddings
  internal/dms/        Paperless-ngx
  internal/migrate/    IMAP-Migration, mbox/eml/zip-Import
  internal/dnsprov/    DNS-Anbieter (IONOS, STRATO, Hetzner, Cloudflare, netcup, INWX, deSEC)
  internal/dyndns/     DynDNS-Aktualisierung bei dynamischer IP
  internal/api/        REST-API, statische Oberfläche
  internal/store/      SQLite + Blob-Speicher
web/                   React-Oberfläche (Verwaltung + Webmail)
```

### Umgebungsvariablen

| Variable | Standard | Bedeutung |
|---|---|---|
| `POSTWERK_DATA_DIR` | `./data` | Datenbank, Mails, Zertifikate |
| `POSTWERK_HOSTNAME` | `localhost` | Hostname (nur beim ersten Start, danach in den Einstellungen) |
| `POSTWERK_HTTP_ADDR` / `POSTWERK_HTTPS_ADDR` | `:8080` / – | Weboberfläche |
| `POSTWERK_SMTP_ADDR` | `:2525` | SMTP eingehend |
| `POSTWERK_SUBMISSION_ADDR` / `POSTWERK_SUBMISSIONS_ADDR` | `:5870` / – | Versand |
| `POSTWERK_IMAP_ADDR` / `POSTWERK_IMAPS_ADDR` | `:1143` / – | IMAP (STARTTLS) / IMAPS |
| `POSTWERK_POP3_ADDR` / `POSTWERK_POP3S_ADDR` | `:1110` / – | POP3 |
| `POSTWERK_ADMIN_EMAIL` / `POSTWERK_ADMIN_PASSWORD` | – | Erstes Admin-Konto |
| `POSTWERK_TLS_CERT` / `POSTWERK_TLS_KEY` | – | Eigenes Zertifikat (überschreibt die Einstellung) |
| `POSTWERK_CERTS_DIR` | `/certs` | Ordner mit Zertifikaten des Reverse-Proxys |
| `POSTWERK_RELAY_HOST` / `_USER` / `_PASSWORD` | – | Smarthost |
| `POSTWERK_METRICS_TOKEN` | – | Bearer-Token für `/metrics` über 80/443; ohne Token gibt es `/metrics` nur intern am Health-Port (`127.0.0.1:8099/metrics` im Container) |
| `POSTWERK_SECURE_COOKIES` | `false` | Cookie nur über HTTPS |
| `POSTWERK_TRUST_PROXY` | `false` | Proxy-Auswertung erzwingen (sonst im Setup-Assistenten) |
| `POSTWERK_ALLOW_INSECURE_AUTH` | `false` | Anmeldung ohne TLS (nur Entwicklung) |
| `POSTWERK_HTTP_ALWAYS_OPEN` | `false` | Port 80 immer offen lassen (Notfall, überstimmt die Einstellung) |
| `POSTWERK_DEBUG` | `false` | Ausführliche Logs |
