# Postwerk

Ein selbst gehosteter Mailserver in einem einzigen Go-Binary – mit eigener Weboberfläche für
Verwaltung und Webmail und KI-Funktionen, die komplett lokal laufen können.

- **Protokolle:** SMTP, Submission (587/465), POP3, Weboberfläche + REST-API
- **Sicherheit:** TLS mit Let’s Encrypt, SPF/DKIM/DMARC, Spamfilter mit Bayes, DNSBL und Greylisting,
  ClamAV, 2FA, App-Passwörter, Brute-Force-Sperre
- **KI:** Spam-/Phishing-Erkennung, Kategorien, Prioritäten, Zusammenfassungen, Antwortentwürfe,
  semantische Suche, Termine/Aufgaben aus Mails, Paperless-ngx-Anbindung – über jeden
  OpenAI-kompatiblen Endpunkt, also auch **lokal mit Ollama oder vLLM**

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
docker compose logs postwerk | grep -i admin   # erzeugtes Admin-Passwort, falls keins gesetzt
```

Ohne Compose:

```bash
docker run -d --name postwerk --restart unless-stopped \
  -e POSTWERK_HOSTNAME=mail.example.com -e POSTWERK_ADMIN_EMAIL=admin@example.com \
  -p 25:2525 -p 587:5870 -p 465:4650 -p 995:9950 -p 80:8080 -p 443:8443 \
  -v postwerk-data:/data ghcr.io/gottschalkfelix4-source/postwerk:latest
```

Danach ist die Oberfläche unter `http://<server>/` erreichbar.

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
| 110 / 995 | 1100 / 9950 | POP3 / POP3S |
| 80 / 443 | 8080 / 8443 | Weboberfläche, ACME (über `HTTP_PORT`/`HTTPS_PORT` in `.env` änderbar) |

Der Dienst läuft ohne Root-Rechte als `PUID:PGID` (Standard 1000:1000, auf Unraid 99:100), deshalb
lauschen die Dienste intern auf hohen Ports.

### DNS

Für jede Domain zeigt **Verwaltung → Domains → Domain öffnen** alle nötigen Einträge (MX, SPF, DKIM,
DMARC, MTA-STS, TLS-RPT, SRV) mit Kopier-Knopf. Zusätzlich braucht der Server einen A-Record für den
Hostnamen und einen passenden Reverse-DNS-Eintrag (PTR) beim Provider.

### Let’s Encrypt

Unter **Einstellungen → TLS** ACME aktivieren, E-Mail eintragen und den Container neu starten.
Port 80 oder 443 muss dafür aus dem Internet erreichbar sein.

### Port 25 gesperrt?

Viele Heimanschlüsse sperren ausgehend Port 25. Dann in `.env` einen Smarthost eintragen
(`POSTWERK_RELAY_HOST`, `POSTWERK_RELAY_USER`, `POSTWERK_RELAY_PASSWORD`).

### Backup und Notfall-Zugang

```bash
# Konsistente Sicherung (Datenbank-Snapshot + Mails + Zertifikate), läuft auch im Betrieb
docker compose exec postwerk postwerk backup /data/backups

# Passwort eines Kontos setzen und 2FA zurücksetzen
docker compose exec postwerk postwerk reset-password admin@example.com 'neues-langes-passwort'
```

## Entwicklung

Voraussetzungen: Go ≥ 1.26, Node.js ≥ 20.

```bash
# Oberfläche bauen (landet in server/internal/webui/dist und wird ins Binary eingebettet)
cd web && npm install && npm run build && cd ..

# Server starten (Standard-Ports für Entwicklung: HTTP 8080, SMTP 2525, Submission 5870, POP3 1110)
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
  internal/pop3/       POP3-Server
  internal/delivery/   Mailfluss: Prüfungen, Filter, Zustellung, Versand, Abwesenheitsnotiz
  internal/queue/      Ausgehende Warteschlange, MX-Zustellung, Bounces
  internal/mailauth/   SPF, DKIM, DMARC
  internal/spam/       Spam-Scoring, Bayes, DNSBL, Greylisting
  internal/av/         ClamAV-Client
  internal/ai/         OpenAI-kompatibler Client, Analyse-Worker, Embeddings
  internal/dms/        Paperless-ngx
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
| `POSTWERK_POP3_ADDR` / `POSTWERK_POP3S_ADDR` | `:1110` / – | POP3 |
| `POSTWERK_ADMIN_EMAIL` / `POSTWERK_ADMIN_PASSWORD` | – | Erstes Admin-Konto |
| `POSTWERK_TLS_CERT` / `POSTWERK_TLS_KEY` | – | Eigenes Zertifikat |
| `POSTWERK_RELAY_HOST` / `_USER` / `_PASSWORD` | – | Smarthost |
| `POSTWERK_METRICS_TOKEN` | – | Bearer-Token für `/metrics` |
| `POSTWERK_SECURE_COOKIES` | `false` | Cookie nur über HTTPS |
| `POSTWERK_TRUST_PROXY` | `false` | Client-IP aus `X-Forwarded-For` |
| `POSTWERK_ALLOW_INSECURE_AUTH` | `false` | Anmeldung ohne TLS (nur Entwicklung) |
| `POSTWERK_DEBUG` | `false` | Ausführliche Logs |
