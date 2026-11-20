# Postwerk auf Unraid einrichten

Diese Anleitung führt von der Installation bis zur ersten zugestellten Mail. Du brauchst:

- Unraid 6.12 oder neuer mit aktiviertem Docker
- eine eigene Domain, deren DNS du bearbeiten kannst
- eine feste öffentliche IPv4-Adresse oder zumindest eine, die sich selten ändert. Ein passender
  Reverse-DNS-Eintrag (PTR) beim Internetanbieter verbessert die Zustellbarkeit deutlich.
- Zugriff auf deinen Router für Portweiterleitungen

> **Hinweis zu Heimanschlüssen:** Viele Anbieter sperren ausgehend Port 25 oder vergeben IP-Adressen,
> die auf Blocklisten stehen. Dann funktioniert der Empfang trotzdem, für den Versand trägst du einen
> Smarthost ein (siehe Schritt 6).

---

## 1. Templates installieren

### Variante A: ein Befehl im Unraid-Terminal (empfohlen)

Öffne in Unraid oben rechts das Terminal (`>_`) und führe aus:

```bash
curl -fsSL https://raw.githubusercontent.com/gottschalkfelix4-source/postwerk/main/unraid/install.sh | bash
```

Das Skript legt die Templates **Postwerk** und **Postwerk-ClamAV** in
`/boot/config/plugins/dockerMan/templates-user/` ab und erstellt den Ordner `/mnt/user/appdata/postwerk`.

### Variante B: Template-Repository eintragen

1. **Docker** → ganz unten **Template repositories** öffnen.
2. Diese URL in eine neue Zeile eintragen und speichern:
   ```
   https://github.com/gottschalkfelix4-source/postwerk
   ```

### Variante C: nur das Template per URL laden

```bash
wget -O /boot/config/plugins/dockerMan/templates-user/my-postwerk.xml \
  https://raw.githubusercontent.com/gottschalkfelix4-source/postwerk/main/unraid/postwerk.xml
```

---

## 2. Optional: ClamAV starten

Wenn du eingehende Mails auf Viren prüfen willst, starte zuerst ClamAV:

1. **Docker → Container hinzufügen**, bei **Vorlage** `Postwerk-ClamAV` wählen.
2. Ohne Änderungen **Anwenden** klicken.
3. Der erste Start lädt die Virensignaturen (etwa 1–3 Minuten). ClamAV braucht rund 1,5 GB RAM.

---

## 3. Postwerk-Container anlegen

**Docker → Container hinzufügen**, bei **Vorlage** `Postwerk` wählen. Eigentlich musst du nur den
**Hostnamen** eintragen (z. B. `mail.deinedomain.de`). Die Felder „Admin-E-Mail“ und „Admin-Passwort“
bleiben leer, das Admin-Konto legst du gleich im Setup-Assistenten an.

Die Ports sind schon passend vorbelegt:

| Zweck | Unraid-Port | im Container |
|---|---|---|
| Weboberfläche HTTP | 8880 | 8080 |
| Weboberfläche HTTPS | 8843 | 8443 |
| SMTP eingehend | 25 | 2525 |
| Versand STARTTLS | 587 | 5870 |
| Versand TLS | 465 | 4650 |
| POP3 / POP3S | 110 / 995 | 1100 / 9950 |

Unraid selbst belegt 80 und 443, deshalb läuft die Weboberfläche auf 8880/8843.
Unter **Erweiterte Ansicht** findest du PUID/PGID (Standard 99/100 = `nobody:users`), Zeitzone,
Smarthost und Reverse-Proxy-Optionen.

**Anwenden** klicken.

## 3a. Setup-Assistent

1. Den **Setup-Token** aus dem Log holen:
   ```bash
   docker logs Postwerk 2>&1 | grep -i setup_token
   ```
   Er steht auch in `/mnt/user/appdata/postwerk/setup-token.txt`.
2. `http://<Unraid-IP>:8880` öffnen (oder Postwerk-Icon → **WebUI**). Der Assistent startet automatisch.
3. Er führt dich durch:
   - **Server & Admin:** Hostname, erste Domain, dein Admin-Konto
   - **DNS:** prüft live, welche Einträge schon stimmen (MX, SPF, DKIM, DMARC …), mit Kopier-Knöpfen
   - **System-Check:** Reverse-DNS, ausgehender Port 25, ClamAV, KI
   - **TLS:** Let's Encrypt einschalten
   - **Schutz & KI:** Virenscan, Spamfilter, Ollama, jeweils mit Verbindungstest
   - **Daten übernehmen:** direkt weiter zum Migrationsassistenten

Den Assistenten kannst du überspringen und später fortsetzen. Er erscheint nach dem Login, bis du ihn abschließt.

## 4. Router: Ports weiterleiten

Leite im Router diese Ports aus dem Internet an die IP deines Unraid-Servers weiter:

| extern | an Unraid | wofür |
|---|---|---|
| 25 | 25 | Mails von anderen Servern empfangen (Pflicht) |
| 465 und 587 | 465 und 587 | Versand mit Mailprogrammen unterwegs |
| 995 | 995 | Abruf unterwegs |
| 80 | 8880 | Let's-Encrypt-Prüfung (nur nötig, wenn Postwerk das Zertifikat selbst holt) |

Die Weboberfläche solltest du **nicht** direkt ins Internet stellen, sondern über VPN (z. B. WireGuard
in Unraid) oder einen Reverse-Proxy mit HTTPS erreichen.

---

## 5. DNS-Einträge setzen

In Postwerk unter **Verwaltung → Domains → deine Domain** stehen alle Einträge mit Kopier-Knopf:

- **A** `mail.deinedomain.de` → deine öffentliche IP
- **MX** `deinedomain.de` → `10 mail.deinedomain.de.`
- **TXT (SPF)** `deinedomain.de` → `v=spf1 mx -all`
- **TXT (DKIM)** `pwJJJJMM._domainkey.deinedomain.de` → Schlüssel aus der Oberfläche
- **TXT (DMARC)** `_dmarc.deinedomain.de` → `v=DMARC1; p=quarantine; …`
- optional MTA-STS, TLS-RPT, SRV- und Autoconfig-Einträge

Den **PTR-Eintrag** (Reverse-DNS) für deine IP setzt dein Internetanbieter, meist im Kundenportal.
Er sollte auf `mail.deinedomain.de` zeigen.

---

## 6. Einstellungen in Postwerk

Melde dich mit dem Admin-Konto an und öffne **Verwaltung → Einstellungen**.

**TLS / Let's Encrypt**
1. ACME aktivieren und deine E-Mail eintragen.
2. Postwerk-Container neu starten. Port 80 muss dafür von außen auf 8880 weitergeleitet sein.

Läuft bei dir schon ein Reverse-Proxy mit Zertifikaten (z. B. Nginx Proxy Manager), kannst du
stattdessen dessen Zertifikat nutzen: Zertifikatsdateien nach `/mnt/user/appdata/postwerk/certs/`
kopieren und im Template unter **Erweiterte Ansicht** zwei Variablen ergänzen:
`POSTWERK_TLS_CERT=/data/certs/fullchain.pem` und `POSTWERK_TLS_KEY=/data/certs/privkey.pem`.

**Virenscan** (wenn du Schritt 2 gemacht hast)
- Aktivieren, Adresse `<Unraid-IP>:3310`, Aktion „Ablehnen“ oder „Quarantäne“.

**Smarthost** (falls Port 25 ausgehend gesperrt ist)
- Im Template unter **Erweiterte Ansicht**: Smarthost (`smtp.anbieter.de:587`), Benutzer und Passwort.

**KI mit Ollama** (optional, komplett lokal)
1. Ollama aus den **Community Apps** installieren (mit GPU-Unterstützung, falls vorhanden).
2. Im Unraid-Terminal die Modelle laden:
   ```bash
   docker exec ollama ollama pull qwen2.5:7b
   docker exec ollama ollama pull nomic-embed-text
   ```
3. In Postwerk unter **Einstellungen → KI**: Basis-URL `http://<Unraid-IP>:11434/v1`,
   Modelle eintragen, **Verbindung testen**, dann aktivieren.

**Paperless-ngx** (optional)
- Unter **Einstellungen → DMS** URL (z. B. `http://<Unraid-IP>:8000`) und API-Token eintragen.
  Mit „Automatisch weiterleiten“ landen PDF-Anhänge von Mails der Kategorie „Rechnung“ direkt in Paperless.

---

## 7. Konten anlegen und Mailprogramm einrichten

1. **Verwaltung → Konten → Neues Konto**.
2. Im Webmail unter **Einstellungen** am besten 2FA aktivieren und für Mailprogramme ein
   **App-Passwort** erzeugen.
3. Thunderbird findet die Einstellungen über den Autoconfig-Eintrag selbst. Manuell:

| | Server | Port | Sicherheit |
|---|---|---|---|
| Eingang (POP3) | `mail.deinedomain.de` | 995 | SSL/TLS |
| Ausgang (SMTP) | `mail.deinedomain.de` | 465 | SSL/TLS |

Benutzername ist die volle E-Mail-Adresse. IMAP folgt in einer späteren Version (siehe Roadmap).

---

## 7a. Von einem anderen Mailserver umziehen

**Verwaltung → Migration → Neue Migration** übernimmt Postfächer per IMAP von jedem anderen Server
(Gmail, Outlook/Microsoft 365, iCloud, GMX, WEB.DE, IONOS, Strato, T-Online, mailcow, Synology, Dovecot …):

1. **Quelle** wählen (Voreinstellungen für die großen Anbieter) oder eigenen Server eintragen.
2. **Testanmeldung** mit einem Postfach: Du siehst alle Ordner mit Anzahl und Ziel („Sent Items“ →
   „Gesendet“ usw.) und kannst Ordner abwählen.
3. **Postfächer** eintragen, einzeln oder per **CSV** für eine ganze Domain
   (`zieladresse;quellbenutzer;quellpasswort;name;neues_passwort`). Fehlende Zielkonten und Domains legt
   Postwerk dabei an.
4. **Optionen:** Papierkorb und Spam mitnehmen, nur Mails ab einem Datum, KI-Analyse für importierte Mails.
5. **Starten.** Der Fortschritt läuft live mit.

Gelesen- und Markiert-Status sowie das ursprüngliche Empfangsdatum bleiben erhalten. Bereits importierte
Nachrichten werden erkannt (Message-ID), ein zweiter Lauf holt also nur Neues nach. Das ist praktisch,
um kurz vor der DNS-Umstellung noch einmal zu synchronisieren. Quell-Passwörter werden nicht
gespeichert; nach einem Neustart fragt „Erneut starten“ sie neu ab.

> Gmail, iCloud, Outlook und GMX verlangen meist ein **App-Passwort** und aktiviertes IMAP.

**Selbst importieren:** Jeder Benutzer kann im Webmail unter **Einstellungen → Import** eigene Exporte
hochladen (`.mbox` aus Thunderbird oder Apple Mail, `.eml`, `.zip`).

## Backup, Update, Fehlersuche

**Backup** – konsistente Sicherung im laufenden Betrieb:

```bash
docker exec Postwerk postwerk backup /data/backups
```

Die Sicherung liegt dann unter `/mnt/user/appdata/postwerk/backups/`. Alternativ den ganzen
appdata-Ordner mit dem Plugin „Appdata Backup“ sichern (Container wird dabei kurz gestoppt).

**Passwort vergessen:**

```bash
docker exec Postwerk postwerk reset-password admin@deinedomain.de 'neues-langes-passwort'
```

**Update:** Docker → Postwerk → **Nach Updates suchen** → **Update anwenden**.

**Logs:** In der Oberfläche unter **Telemetrie → Logs** oder im Terminal:

```bash
docker logs -f Postwerk
```

**Prüfen, ob Port 25 von außen erreichbar ist** (von einem Rechner außerhalb deines Netzes):

```bash
nc -vz mail.deinedomain.de 25
```
