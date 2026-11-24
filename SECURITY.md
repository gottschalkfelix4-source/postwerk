# Security Policy

Postwerk nimmt Berichte über Sicherheitsprobleme ernst. Bitte melde Funde
vertraulich, bevor du Details öffentlich machst, damit wir Betroffene warnen
können, bevor ein Exploit verbreitet wird.

## Melden eines Problems

- **Am besten:** eine GitHub-Security-Advisory über die Projektseite
  (Private security advisory) anlegen oder eine E-Mail an die Maintainer.
- Bitte so viele Details wie möglich angeben:
  - Betroffene Komponente und Version (Binary / Image-Tag / Commit-Hash)
  - Art der Schwachstelle (z. B. XSS, Auth-Bybass, DoS, SSRF, TLS …)
  - Schritte zum Reproduzieren als PoC (ohne Daten anderer Nutzer)
  - Ggf. vorgeschlagener Fix
- Keine Massen-Reports und keine Ausnutzung von Konten / Daten Dritter im
  Rahmen des PoC.

## Was abgedeckt ist

- Eingehender Schwerpunkt: Web-UI & REST-API, Authentifizierung und Sessions,
  SMTP/Submission/IMAP/POP3/JMAP, Sieve, Backup/Restore, Verschlüsselung im
  Ruhezustand, Verschlüsselung der Mail im Transit.
- Klare Nicht-Ziele (sind dokumentierte Betriebsverantwortung):
  - Konfigurationseingaben, die bewusst vollen Zugriff voraussetzen
    (z. B. `POSTWERK_ALLOW_INSECURE_AUTH=true`, Reverse-Proxy-Trust,
    Admin-seitig konfigurierte Backend-URLs wie WebDAV/SMB/KI/Smarthost).
  - Kompromittierter Host / kompromittierte `.env` (physischer Zugriff,
    Root im Container/auf dem Host).

## Verifizierung und Offenlegung

- Wir stellen den Report nach bestem Wissen nach, testen einen Fix und halten
  dich auf dem Laufenden.
- Nach einem verfügbaren Fix veröffentlichen wir gemeinsam mit dir einen
  Advisory. Standard-Frist für Koordination: 90 Tage ab Bestätigung,
  verlängerbar, wenn ein Fix mehr Zeit braucht.
