# Postwerk auf einem Linux-VPS

Auf einem VPS mit fester IP läuft ein Mailserver am zuverlässigsten: Du kannst Reverse-DNS setzen,
die IP steht auf keiner Privatkunden-Sperrliste, und Port 25 ist meist frei.

## Voraussetzungen

| | Minimum | Empfohlen |
|---|---|---|
| Arbeitsspeicher | 1 GB (nur Postwerk) | 4 GB mit Virenscanner, 8+ GB mit lokaler KI |
| Speicher | 10 GB | je nach Postfachgröße |
| System | beliebiges Linux mit Docker und Docker Compose | Debian oder Ubuntu |
| Netz | feste öffentliche IPv4 | dazu IPv6 |

**Port 25 beim Anbieter prüfen:** Ohne ausgehenden Port 25 kann Postwerk nicht direkt an andere
Mailserver zustellen.
- **Hetzner Cloud:** Port 25 ist für neue Konten gesperrt, wird aber auf Antrag freigeschaltet
  (Support-Anfrage nach der ersten bezahlten Rechnung).
- **netcup, IONOS, Strato-VPS, Contabo:** in der Regel offen.
- **AWS, Google Cloud, Azure, Oracle, DigitalOcean:** sperren Port 25 meist dauerhaft. Dann einen
  Smarthost nutzen (Setup-Assistent → Internetanschluss → Smarthost).

## Installation

**1. Docker installieren** (falls noch nicht vorhanden):

```bash
curl -fsSL https://get.docker.com | sh
```

**2. Compose-Datei holen:**

```bash
mkdir -p /opt/postwerk && cd /opt/postwerk
curl -fsSL https://raw.githubusercontent.com/gottschalkfelix4-source/postwerk/main/deploy/vps/docker-compose.yml -o docker-compose.yml
```

**3. Hostname eintragen.** In `docker-compose.yml` die mit `ÄNDERN` markierten Werte anpassen,
mindestens `POSTWERK_HOSTNAME` (z. B. `mail.deinedomain.de`):

```bash
nano docker-compose.yml
```

**4. Firewall öffnen** (Beispiel für `ufw`; bei Cloud-Anbietern zusätzlich die Firewall im Kundenmenü):

```bash
ufw allow 25,80,443,465,587,993,995/tcp
```

**5. Starten:**

```bash
docker compose up -d
```

Mit Virenscanner und lokaler KI:

```bash
docker compose --profile clamav --profile ai up -d
```

**6. Setup-Token anzeigen und Assistenten öffnen:**

```bash
docker compose exec postwerk postwerk setup-token
```

Kommt „bereits eingerichtet“, wurde das Admin-Konto schon angelegt, entweder über `POSTWERK_ADMIN_EMAIL`
oder bei einem früheren Start mit demselben Datenvolume. Dann einfach mit diesem Konto anmelden
(Passwort vergessen: `docker compose exec postwerk postwerk reset-password <e-mail> '<passwort>'`).

Dann `http://<IP-des-Servers>/` im Browser öffnen. Der Setup-Assistent führt durch:
- Domain und Admin-Konto
- DNS-Einträge: automatisch bei Hetzner, IONOS, Cloudflare, netcup, INWX, deSEC, STRATO, oder
  von Hand mit Feldern genau wie beim Anbieter
- System-Check
- Let's Encrypt: im Schritt TLS „Let's Encrypt direkt“ wählen, Port 80 „Nach dem Zertifikat schließen“
  und „Zertifikat jetzt holen“ (siehe unten)
- Virenscanner (`clamav:3310`) und KI (`http://ollama:11434/v1`)

## Zertifikat und Port 80

Im Setup-Assistenten (Schritt **TLS**) „Let's Encrypt direkt“ wählen. Für Port 80 gibt es drei Stufen:

| Einstellung | Verhalten |
|---|---|
| **Nach dem Zertifikat schließen** (empfohlen) | Port 80 ist nur offen, bis das Zertifikat da ist. Etwa 32 Tage vor Ablauf öffnet Postwerk ihn automatisch zur Verlängerung und schließt ihn danach wieder. |
| Nur Umleitung auf HTTPS | Port 80 bleibt offen, beantwortet aber nur die Let's-Encrypt-Prüfung und leitet alles andere auf HTTPS um. |
| Offen lassen | Weboberfläche auch über HTTP (nur für Tests). |

Mit **„Zertifikat jetzt holen“** fragt Postwerk Let's Encrypt sofort an und zeigt das Ergebnis. Danach
erreichst du die Weboberfläche unter `https://mail.deinedomain.de`. Port 80 schließt Postwerk nur, wenn
ein gültiges Zertifikat vorliegt und HTTPS läuft, damit du dich nicht aussperrst. Die Verlängerung
klappt bei geschlossenem Port 80 zusätzlich über Port 443.

**Notfall** – Port 80 wieder öffnen, falls die Weboberfläche nicht erreichbar ist:

```bash
docker compose exec postwerk postwerk http-port open
```

Der Server übernimmt das innerhalb einer Minute.

## Reverse-DNS (PTR) setzen

Der wichtigste Schritt für gute Zustellbarkeit: Die IP des Servers muss per Reverse-DNS auf den
Hostnamen zeigen (`203.0.113.10` → `mail.deinedomain.de`). Das stellst du beim **VPS-Anbieter** ein,
nicht beim DNS-Anbieter der Domain:

| Anbieter | Wo |
|---|---|
| Hetzner Cloud | Cloud Console → Server → Netzwerk → Reverse DNS |
| netcup | Server Control Panel → Netzwerk → rDNS |
| IONOS | Cloud Panel → Netzwerk → Öffentliche IP → Reverse DNS |
| Contabo | Kundenpanel → Reverse DNS |

Der Setup-Assistent zeigt im System-Check an, ob der Eintrag passt.

## IPv6

Hat das Docker-Netz des Containers kein IPv6, nimmt Docker eingehende IPv6-Verbindungen auf den
veröffentlichten Ports trotzdem an und reicht sie über den `docker-proxy` per IPv4 weiter. Postwerk
sieht dann nicht die echte Adresse des Gegenübers, sondern die Gateway-Adresse des Docker-Netzes
(z. B. `172.18.0.1`). Die Folgen: SPF-Prüfungen gehen schief, Sperrlisten und Greylisting greifen
nicht, und die Sperre nach Fehlanmeldungen trifft alle IPv6-Nutzer auf einmal. Wähle deshalb eine der
beiden Varianten:

**Variante A – Mail nur über IPv4 (am einfachsten):**

1. Für den Mail-Hostnamen (`mail.deinedomain.de`) **keinen AAAA-Record** anlegen. Andere Mailserver
   und Mailprogramme verbinden sich dann per IPv4.
2. Die Ports in der `docker-compose.yml` nur auf IPv4 veröffentlichen, damit Docker gar nicht erst
   auf IPv6 lauscht – vor jede Portzeile `0.0.0.0:` setzen, z. B. `"0.0.0.0:25:2525"` und
   `"0.0.0.0:993:9930"`.

**Variante B – IPv6 durchreichen:**

1. Docker 27 oder neuer verwenden (`docker version`); dort ist `ip6tables` standardmäßig aktiv.
   Bei älteren Versionen in `/etc/docker/daemon.json` `{"experimental": true, "ip6tables": true}`
   eintragen und Docker neu starten (`systemctl restart docker`).
2. In der `docker-compose.yml` unten beim Netz `mail` den IPv6-Block einkommentieren
   (`enable_ipv6` mit dem privaten Subnetz `fd00:5057:6b::/64`) und `docker compose up -d` ausführen.
3. Prüfen: Nach einer Verbindung per IPv6 (z. B. IMAP vom Handy im Mobilfunknetz) muss im Log
   (`docker compose logs postwerk`) die echte IPv6-Adresse stehen, nicht `172.x.x.x`.
4. Postwerk stellt dann auch **ausgehend per IPv6** zu. Dafür braucht die IPv6-Adresse ebenfalls
   einen PTR-Eintrag auf `mail.deinedomain.de` (siehe oben), einen AAAA-Record und einen Platz im
   SPF-Eintrag (`mx` oder `ip6:…`). Fehlt der PTR, lehnen große Anbieter wie Gmail die Mails ab.

## Handy und Mailprogramm einrichten

iPhone, Android, Outlook und Thunderbird verbinden sich per **IMAP**. Ordner, Gelesen-Status und
Gesendete Mails bleiben dabei mit dem Webmail abgeglichen, neue Mails kommen per IDLE sofort an.

| | Server | Port | Sicherheit |
|---|---|---|---|
| Eingang (IMAP) | `mail.deinedomain.de` | 993 | SSL/TLS |
| Ausgang (SMTP) | `mail.deinedomain.de` | 465 | SSL/TLS (alternativ 587 mit STARTTLS) |

Benutzername ist die volle E-Mail-Adresse. Mit aktivierter Zwei-Faktor-Anmeldung brauchst du ein
**App-Passwort** (Webmail → Einstellungen). Die Ordner Entwürfe, Gesendet, Spam, Papierkorb und Archiv
werden automatisch zugeordnet.

**iPhone/iPad:** Einstellungen → Apps → Mail → Mail-Accounts → Account hinzufügen → Andere →
Mail-Account hinzufügen. Nach dem Klick auf „Weiter“ **IMAP** wählen und bei beiden Servern
`mail.deinedomain.de` eintragen.

Port 993 muss in der Firewall offen sein (Schritt 4) und in der `docker-compose.yml` stehen. Ältere
Compose-Dateien ohne die Zeile `"993:9930"` ergänzen und `docker compose up -d` ausführen.

## Kalender und Kontakte auf dem iPhone

Postwerk ist zugleich CalDAV- und CardDAV-Server. Kalender, Aufgaben (Erinnerungen) und Kontakte aus
dem Webmail erscheinen damit auf iPhone, iPad und Mac; Änderungen auf dem Gerät landen sofort im Webmail.
Der Zugang läuft über HTTPS (Port 443), es wird kein weiterer Port gebraucht.

**Am einfachsten:** Im Webmail unter **Konto & Sicherheit → Kalender & Kontakte synchronisieren** das
**Apple-Profil herunterladen** und auf dem Gerät installieren (Einstellungen → Allgemein → VPN und
Geräteverwaltung). Das Profil richtet Mail, Kalender und Kontakte in einem Schritt ein; das Passwort
fragt das Gerät bei der Installation ab.

**Von Hand:** Einstellungen → Apps → Kalender (bzw. Kontakte) → Kalender-Accounts → Account hinzufügen →
Andere → **CalDAV-Account hinzufügen** (bzw. **CardDAV-Account hinzufügen**):

| Feld | Eintrag |
|---|---|
| Server | `mail.deinedomain.de` |
| Benutzername | volle E-Mail-Adresse |
| Passwort | Kontopasswort, mit aktivierter Zwei-Faktor-Anmeldung ein **App-Passwort** |
| Beschreibung | frei wählbar |

Das iPhone findet die richtigen Adressen selbst (`/.well-known/caldav`). Klappt das nicht, unter
„Erweiterte Einstellungen“ die Account-URL `https://mail.deinedomain.de/dav/principals/<E-Mail>/` eintragen.
Android (DAVx⁵), Thunderbird und macOS nutzen dieselben Angaben; die genauen URLs zeigt das Webmail an.
Mit den DNS-Einträgen `_caldavs._tcp` und `_carddavs._tcp` (DNS-Anleitung im Admin-Bereich) reicht
in vielen Apps sogar die E-Mail-Adresse.

Aus Sicherheitsgründen nimmt Postwerk Anmeldungen für Kalender und Kontakte nur über HTTPS an. Hinter
einem Reverse-Proxy muss dieser `X-Forwarded-Proto: https` setzen und als vertrauenswürdig eingetragen
sein (macht die Konfiguration aus dem Setup-Assistenten bereits); WebDAV-Methoden wie `PROPFIND` und
`REPORT` und den `Authorization`-Header unverändert durchreichen.

## Betrieb

**Updates:**

```bash
cd /opt/postwerk && docker compose pull && docker compose up -d
```

**Backup:** Unter **Verwaltung → Backup → Einstellungen** das Ziel wählen und den Zeitplan
einschalten. Auf einem VPS empfiehlt sich ein Ziel außerhalb des Servers, z. B. eine
Hetzner Storage Box oder Nextcloud per **WebDAV** oder ein NAS zu Hause per **SMB**, dazu
**Archive verschlüsseln** mit einem Passwort, das du außerhalb des Servers aufbewahrst.
„Speicherziel testen“ prüft Schreiben, Auflisten und Löschen.

Von der Kommandozeile geht es auch (landet in `/data/backups`, also im Volume `postwerk_postwerk-data`):

```bash
docker compose exec postwerk postwerk backup
```

**Wiederherstellen:** Verwaltung → Backup → beim Archiv auf ↺ klicken. Der Server spielt das Archiv
ein und startet neu (10–30 Sekunden). Der vorherige Stand bleibt einmalig unter
`/data/pre-restore-<zeit>` erhalten. Auf einem neuen Server: Postwerk starten, Archiv unter
Verwaltung → Backup hochladen und wiederherstellen.

**Logs:**

```bash
docker compose logs -f postwerk
```

**Passwort vergessen:**

```bash
docker compose exec postwerk postwerk reset-password admin@deinedomain.de 'neues-langes-passwort'
```

## Mit vorhandenem Reverse-Proxy (Caddy, Traefik, nginx)

Läuft auf dem VPS schon ein Reverse-Proxy auf 80/443, die beiden Web-Ports in der Compose-Datei nur
lokal binden und den Proxy davorsetzen:

```yaml
      - "127.0.0.1:8080:8080"   # statt "80:8080"
      # "443:8443" entfernen
```

Den Rest erklärt der Setup-Assistent im Schritt **Reverse-Proxy**, mit fertiger Konfiguration für
Caddy, Traefik, nginx und Nginx Proxy Manager und der Übernahme des Proxy-Zertifikats für die Mail-Ports.
