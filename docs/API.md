# Postwerk HTTP-API (v1)

Alle Endpunkte liegen unter `/api`. Request- und Response-Bodies sind JSON (`Content-Type: application/json`),
sofern nicht anders angegeben. Zeitangaben sind RFC-3339-Strings (UTC).

## Konventionen

- **Session:** Nach dem Login setzt der Server das Cookie `pw_session` (HttpOnly, SameSite=Strict).
  Sobald das Cookie `Secure` ist (HTTPS aktiv), trägt es den `__Host-`-Präfix.
- **CSRF:** Jeder Request mit Methode `POST`, `PUT`, `PATCH` oder `DELETE` muss den Header
  `X-Postwerk: 1` tragen, sonst antwortet der Server mit `403`. Zusätzlich wird ein von einem
  Browser gesendeter `Origin`-Header gegen den eigenen Host (bzw. die konfigurierte öffentliche URL)
  geprüft; fremde Ursprünge werden mit `403` abgelehnt. Requests ohne `Origin` (CLI, Mail-Clients) bleiben erlaubt.
- **Fehler:** HTTP-Status ≥ 400 mit Body `{ "error": "<code>", "message": "<Text für Menschen>" }`.
  Wichtige Codes: `unauthorized` (401), `totp_required` (401), `forbidden` (403), `not_found` (404),
  `invalid` (400), `conflict` (409), `rate_limited` (429), `ai_disabled` (503).
- **Rollen:** `admin` darf alles unter `/api/admin`, `user` nur eigene Daten.
- **Listen** mit Paginierung: Query `limit` (Default 50, max 200) und `offset`; Antwort `{ total, items }`.

## Typen (TypeScript-Notation)

```ts
type Role = "admin" | "user";

interface Me {
  id: number; email: string; name: string; role: Role;
  totpEnabled: boolean; quotaBytes: number; usedBytes: number;
}

interface Domain {
  name: string;
  catchAll: string | null;      // Zieladresse für Catch-all oder null
  dkimSelector: string;
  accountCount: number;
  aliasCount: number;
  createdAt: string;
}

interface DnsRecord { type: "MX" | "TXT" | "CNAME" | "A" | "AAAA" | "TLSA" | "SRV"; name: string; value: string; purpose: string; }

interface Account {
  id: number; email: string; name: string; role: Role;
  quotaBytes: number;          // 0 = unbegrenzt
  usedBytes: number; disabled: boolean; totpEnabled: boolean;
  aliases: string[]; createdAt: string;
}

interface Alias { id: number; address: string; targets: string[]; createdAt: string; }

interface QueueItem {
  id: number; from: string; to: string[]; size: number; attempts: number;
  nextAttempt: string; lastError: string | null; createdAt: string;
}

interface LogEntry { time: string; level: "debug" | "info" | "warn" | "error"; component: string; message: string; fields: Record<string, string>; }

interface Ban { ip: string; reason: string; until: string; failures: number; }

interface Dashboard {
  stats: {
    accounts: number; domains: number; messages: number; storageBytes: number; queueSize: number;
    received24h: number; sent24h: number; spam24h: number; virus24h: number; rejected24h: number;
  };
  series: { hours: string[]; received: number[]; sent: number[]; spam: number[] }; // letzte 24 Stunden
  categories: { name: string; count: number }[];   // KI-Kategorien der letzten 7 Tage
  recentEvents: LogEntry[];
  version: string; uptimeSeconds: number;
  ai: { enabled: boolean; reachable: boolean; model: string };
}

interface Settings {
  server: { hostname: string; maxMessageSize: number; };
  tls: { acme: { enabled: boolean; email: string; directoryUrl: string; domains: string[] }; status: string };
  spam: {
    enabled: boolean; junkThreshold: number; rejectThreshold: number;
    greylisting: { enabled: boolean; delaySeconds: number };
    dnsbl: { enabled: boolean; servers: string[] };
    bayes: { enabled: boolean; autoLearn: boolean; minTokens: number };
  };
  antivirus: { enabled: boolean; clamdAddress: string; action: "reject" | "quarantine" };
  ai: {
    enabled: boolean; baseUrl: string; apiKey: string /* maskiert "••••" wenn gesetzt */;
    chatModel: string; embeddingModel: string; timeoutSeconds: number;
    features: { spamCheck: boolean; classify: boolean; priority: boolean; summarize: boolean;
                replyDraft: boolean; semanticSearch: boolean; extractEvents: boolean };
    categories: string[];          // z. B. ["Rechnung","Newsletter","Support","Privat"]
  };
  dms: { paperless: { enabled: boolean; url: string; token: string /* maskiert */; autoForward: boolean; categories: string[]; accounts: string[] } };
  security: { bruteForce: { maxAttempts: number; windowSeconds: number; banSeconds: number } };
}

interface Mailbox { id: number; name: string; role: "inbox" | "sent" | "drafts" | "trash" | "junk" | "archive" | null; total: number; unread: number; }

interface Address { name: string; email: string; }

interface MessageSummary {
  id: number; mailboxId: number; threadId: number;
  from: Address; to: Address[]; subject: string; preview: string;
  receivedAt: string; size: number; seen: boolean; flagged: boolean; hasAttachments: boolean;
  tags: string[]; category: string | null; priority: "high" | "normal" | "low";
  spamScore: number; summary: string | null;  // kurze KI-Zusammenfassung, falls vorhanden
  aiSuspicious: boolean;                       // KI-Analyse vermutet Prompt-Injection
}

interface Attachment { id: number; filename: string; contentType: string; size: number; }

interface AuthResults { spf: string; dkim: string; dmarc: string; } // "pass" | "fail" | "none" | ...

interface ExtractedEvent { title: string; start: string; end: string | null; location: string | null; description: string | null; }
interface ExtractedTask { title: string; due: string | null; }

interface MessageDetail extends MessageSummary {
  cc: Address[]; replyTo: Address[]; messageId: string; inReplyTo: string | null;
  textBody: string; htmlBody: string | null;   // HTML ist NICHT bereinigt: Client muss sanitizen
  attachments: Attachment[]; auth: AuthResults;
  virus: string | null;                         // Name der Signatur, falls gefunden
  ai: { summary: string | null; reasons: string[]; events: ExtractedEvent[]; tasks: ExtractedTask[] } ;
}

// Zeitwerte: Termine mit Uhrzeit in UTC ("2026-11-21T08:00:00Z") oder als lokale Zeit ohne Zone
// ("2026-11-21T10:00:00"); ganztägige Termine als Datum ("2026-11-21"), `end` dann exklusiv.
interface CalendarEvent { id: number; calendarId: number; uid: string; title: string; start: string; end: string | null; allDay: boolean;
  location: string | null; description: string | null; rrule: string | null; instance: string | null; sourceMessageId: number | null; }
interface Task { id: number; calendarId: number; uid: string; title: string; due: string | null; done: boolean; description: string | null;
  sourceMessageId: number | null; createdAt: string; }

interface Vacation { enabled: boolean; subject: string; body: string; from: string | null; until: string | null; }
interface AppPassword { id: number; name: string; createdAt: string; lastUsedAt: string | null; }
```

## Authentifizierung

| Methode | Pfad | Body | Antwort |
|---|---|---|---|
| POST | `/api/auth/login` | `{ email, password, totp? }` | `Me` (setzt Cookie); `401 totp_required` wenn 2FA fehlt |
| POST | `/api/auth/logout` | – | `{ ok: true }` |
| GET | `/api/auth/me` | – | `Me` |

## Eigenes Konto (`/api/account`)

| Methode | Pfad | Body | Antwort |
|---|---|---|---|
| POST | `/password` | `{ current, new }` | `{ ok }` |
| POST | `/totp/setup` | – | `{ secret, otpauthUrl }` |
| POST | `/totp/enable` | `{ code }` | `{ ok }` |
| POST | `/totp/disable` | `{ password }` | `{ ok }` |
| GET | `/app-passwords` | – | `AppPassword[]` |
| POST | `/app-passwords` | `{ name }` | `AppPassword & { password }` (nur einmal sichtbar) |
| DELETE | `/app-passwords/{id}` | – | `{ ok }` |
| GET / PUT | `/vacation` | `Vacation` | `Vacation` |

## Verwaltung (`/api/admin`, nur Rolle `admin`)

| Methode | Pfad | Body | Antwort |
|---|---|---|---|
| GET | `/dashboard` | – | `Dashboard` |
| GET | `/domains` | – | `Domain[]` |
| POST | `/domains` | `{ name }` | `Domain` (erzeugt einen aktiven RSA- und einen vorbereiteten Ed25519-DKIM-Schlüssel) |
| PATCH | `/domains/{name}` | `{ catchAll? }` | `Domain` |
| DELETE | `/domains/{name}` | – | `{ ok }` |
| GET | `/domains/{name}/dns` | – | `DnsRecord[]` |
| GET | `/accounts?q=` | – | `{ total, items: Account[] }` |
| POST | `/accounts` | `{ email, name, password, role, quotaBytes }` | `Account` |
| GET | `/accounts/{id}` | – | `Account` |
| PATCH | `/accounts/{id}` | `{ name?, password?, role?, quotaBytes?, disabled?, resetTotp? }` | `Account` |
| DELETE | `/accounts/{id}` | – | `{ ok }` |
| GET | `/aliases` | – | `Alias[]` |
| POST | `/aliases` | `{ address, targets }` | `Alias` |
| PATCH | `/aliases/{id}` | `{ targets }` | `Alias` |
| DELETE | `/aliases/{id}` | – | `{ ok }` |
| GET | `/queue` | – | `QueueItem[]` |
| POST | `/queue/{id}/retry` | – | `{ ok }` |
| DELETE | `/queue/{id}` | – | `{ ok }` |
| GET | `/logs?level=&q=&limit=` | – | `LogEntry[]` (neueste zuerst) |
| GET | `/security/bans` | – | `Ban[]` |
| DELETE | `/security/bans/{ip}` | – | `{ ok }` |
| GET | `/settings` | – | `Settings` |
| PUT | `/settings` | `Partial<Settings>` (abschnittsweise) | `Settings` |
| POST | `/settings/ai/test` | – | `{ ok, latencyMs, models: string[], error? }` |

## Webmail (`/api/mail`)

| Methode | Pfad | Body | Antwort |
|---|---|---|---|
| GET | `/mailboxes` | – | `Mailbox[]` |
| POST | `/mailboxes` | `{ name }` | `Mailbox` |
| PATCH | `/mailboxes/{id}` | `{ name }` | `Mailbox` |
| DELETE | `/mailboxes/{id}` | – | `{ ok }` (nur ohne Rolle) |
| GET | `/messages?mailbox=&q=&tag=&category=&unread=&limit=&offset=` | – | `{ total, items: MessageSummary[] }` |
| GET | `/messages/{id}` | – | `MessageDetail` |
| GET | `/messages/{id}/raw` | – | `message/rfc822` |
| GET | `/messages/{id}/attachments/{attId}` | – | Datei-Download |
| PATCH | `/messages/{id}` | `{ seen?, flagged?, mailboxId?, tags? }` | `MessageSummary` |
| DELETE | `/messages/{id}` | – | `{ ok }` (in Papierkorb, im Papierkorb endgültig) |
| POST | `/messages/bulk` | `{ ids, action, mailboxId? }` action: `read`/`unread`/`flag`/`unflag`/`move`/`delete`/`spam`/`notspam` | `{ ok, count }` |
| GET | `/threads/{threadId}` | – | `MessageDetail[]` (chronologisch) |
| POST | `/send` | `{ to, cc?, bcc?, subject, text, html?, inReplyTo?: number, attachments?: {filename, contentType, dataBase64}[] }` | `{ ok, id }` |
| POST | `/drafts` | wie `/send` plus `draftId?` | `{ id }` |
| GET | `/tags` | – | `{ name: string; count: number }[]` |

`spam` / `notspam` trainieren zusätzlich den Bayes-Filter und verschieben nach Junk bzw. Inbox.

## KI (`/api/ai`)

| Methode | Pfad | Body | Antwort |
|---|---|---|---|
| GET | `/status` | – | `{ enabled, features: Settings["ai"]["features"], categories: string[], dmsEnabled: boolean }` |
| POST | `/summarize` | `{ messageId } \| { threadId }` | `{ summary, bullets: string[], suspicious: boolean }` |
| POST | `/reply` | `{ messageId, instruction?, tone?: "formal"\|"friendly"\|"short" }` | `{ subject, body, suspicious: boolean }` |
| GET | `/search?q=&limit=` | – | `{ items: (MessageSummary & { score: number })[] }` |
| POST | `/extract` | `{ messageId }` | `{ events: ExtractedEvent[], tasks: ExtractedTask[], suspicious: boolean }` |
| POST | `/reclassify` | `{ messageId }` | `MessageSummary` |

Schutz vor Prompt-Injection: An das Modell geht nur der Text, den ein Mensch in der Mail sieht (ohne
HTML-Kommentare, per CSS versteckte Elemente, winzige oder hintergrundfarbene Schrift und unsichtbare
Unicode-Zeichen), eingerahmt von zufälligen Markierungen je Anfrage. Fällt eine Mail durch typische
Steuerversuche oder viel versteckten Text auf (oder meldet das Modell selbst einen Verdacht), wird sie nicht
automatisch einsortiert oder an Paperless weitergeleitet, bekommt keine hohe Priorität und die KI kann ihren
Spam-Wert nur erhöhen. `suspicious`/`aiSuspicious` zeigen den Verdacht an; Antworten des Modells werden auf
erlaubte Werte, Längen und reinen Text geprüft.

## Kalender, Aufgaben, DMS

Termine und Aufgaben liegen als iCalendar-Objekte (VEVENT/VTODO) in Kalendern und sind dieselben
Daten, die CalDAV-Clients sehen (siehe „Kalender und Kontakte (CalDAV/CardDAV)“).

| Methode | Pfad | Body | Antwort |
|---|---|---|---|
| GET | `/api/calendar/events?from=&to=&calendar=` | – | `CalendarEvent[]`; Serien (RRULE) sind im Zeitraum in Einzeltermine aufgelöst (gleiche `id`, `instance` = Beginn des Vorkommens) |
| POST | `/api/calendar/events` | `EventInput` | `CalendarEvent` |
| PUT | `/api/calendar/events/{id}` | `EventInput & { instance? }` | `CalendarEvent` (ändert die ganze Serie; mit `instance` wird die Serie um den Abstand `start − instance` verschoben) |
| DELETE | `/api/calendar/events/{id}` | – | `{ ok }` (ganze Serie) |
| GET | `/api/tasks?calendar=` | – | `Task[]` |
| POST | `/api/tasks` | `{ calendarId?, title, due?, description?, sourceMessageId? }` | `Task` |
| PATCH | `/api/tasks/{id}` | `{ calendarId?, title?, due?, done?, description? }` | `Task` |
| DELETE | `/api/tasks/{id}` | – | `{ ok }` |
| POST | `/api/dms/forward` | `{ messageId, attachmentId }` | `{ ok }` |

`EventInput = { calendarId?, title, start, end?, allDay?, location?, description?, rrule?, sourceMessageId? }` –
`rrule` ohne „RRULE:“ (z. B. `FREQ=WEEKLY;COUNT=10`); bei PUT bedeutet fehlend „unverändert“, `""` „keine Wiederholung“.
Eigenschaften, die die Weboberfläche nicht kennt (Erinnerungen, Teilnehmer …), bleiben beim Bearbeiten erhalten.

## Sonstiges

- `GET /metrics` – Prometheus-Format. Über die öffentlichen Ports nur mit `Authorization: Bearer <METRICS_TOKEN>` (ohne gesetzten Token: 404); ohne Token intern am Health-Port erreichbar.
- `GET /healthz` – `ok`.
- `GET /.well-known/mta-sts.txt` – MTA-STS-Policy im eingestellten Modus (`mailauth.mtaSts.mode`: `none`, `testing` (Standard) oder `enforce`, `maxAge` in Sekunden). Die `id` im TXT-Eintrag `_mta-sts` ist ein Hash der Policy und ändert sich mit ihr.

## Einrichtung (Setup-Assistent)

Postwerk kennt drei Setup-Zustände:

- `initial` – es gibt noch kein Konto. Der Server hat beim Start einen **Setup-Token** ins Log
  geschrieben (und nach `/data/setup-token.txt`; anzeigen mit `postwerk setup-token`). Nur damit lässt sich das erste Admin-Konto anlegen.
- `configure` – Konten existieren, der Assistent wurde aber noch nicht abgeschlossen. Admins werden nach
  dem Login in den Assistenten geleitet (überspringbar).
- `done` – fertig eingerichtet.

```ts
interface SetupStatus { mode: "initial" | "configure" | "done"; hostname: string; version: string; }

type CheckStatus = "ok" | "missing" | "mismatch" | "warning" | "error" | "info";

interface DnsCheck {
  type: DnsRecord["type"]; name: string; expected: string; found: string[];
  status: CheckStatus; purpose: string; required: boolean;
}

interface SystemCheck {
  hostname: string;
  hostnameIps: string[];              // A/AAAA des Hostnamens
  ptr: { ip: string; names: string[]; matches: boolean }[];   // Reverse-DNS je IP
  port25Outbound: CheckStatus;        // "ok" | "error" (gesperrt) | "warning"
  port25Detail: string;
  tls: string;                        // wie Settings.tls.status
  clamav: CheckStatus;                // "ok" | "error" | "info" (deaktiviert)
  clamavDetail: string;
  ai: CheckStatus;                    // "ok" | "error" | "info"
  aiDetail: string;
}
```

| Methode | Pfad | Body | Antwort |
|---|---|---|---|
| GET | `/api/setup/status` | – (ohne Anmeldung) | `SetupStatus` |
| POST | `/api/setup/initialize` | `{ token, hostname, domain, adminEmail, adminName, adminPassword }` – nur im Modus `initial` | `Me` (setzt Session-Cookie) |
| POST | `/api/setup/restore/archives` | `{ token, target: BackupSettings["target"] }` – nur im Modus `initial` | `{ archives: { name, size, modified, encrypted }[]; target; error? }` |
| POST | `/api/setup/restore/start` | `{ token, target, name, password, masterKey }` | `BackupStatus`; Wiederherstellung läuft im Hintergrund, danach Neustart |
| POST | `/api/setup/restore/upload` | multipart: Felder `token`, `password`, `masterKey` **vor** `file` | `{ manifest }`; das Archiv wird beim Empfang entpackt und geprüft, danach Neustart |
| POST | `/api/setup/restore/resume` | `{ token, masterKey }` | `{ manifest }`; setzt eine Wiederherstellung fort, die auf den Master-Schlüssel wartet |
| POST | `/api/setup/restore/status` | `{ token }` | `BackupStatus` (Fortschritt, `last.code` bei fehlendem Schlüssel) |
| POST | `/api/admin/setup/complete` | – | `{ ok }` (Modus → `done`) |
| GET | `/api/admin/system/check` | – | `SystemCheck` |
| POST | `/api/admin/system/test-clamav` | `{ address }` | `{ ok, error? }` |
| GET | `/api/admin/domains/{name}/dns/check` | – | `DnsCheck[]` (Live-Abfrage im DNS) |

Fehlercodes: `invalid_token` (403), `already_initialized` (409); bei der Wiederherstellung zusätzlich
die Codes aus [Backup → Wiederherstellung](#wiederherstellung-und-serverumzug). `SetupStatus` enthält im
Modus `initial` ggf. `restoreError`: eine Wiederherstellung, die beim Start zurückgenommen wurde.

## Migration (Import von einem anderen Mailserver)

Quelle ist ein beliebiger IMAP-Server (Gmail, Outlook, mailcow, Dovecot, Stalwart, Synology …).
Zugangsdaten der Quelle werden **nur im Arbeitsspeicher** gehalten, nie gespeichert. Nach einem
Neustart steht ein laufender Job auf `interrupted` und kann mit neu eingegebenen Passwörtern wieder
gestartet werden; bereits importierte Nachrichten werden übersprungen (Abgleich über Message-ID).

```ts
type ImapSecurity = "tls" | "starttls" | "none";

interface MigrationSource { host: string; port: number; security: ImapSecurity; }

interface SourceFolder {
  name: string;                 // Pfad auf der Quelle, z. B. "INBOX" oder "Projekte/2025"
  messages: number;
  specialUse: string | null;    // "\Sent", "\Drafts", "\Junk", "\Trash", "\Archive" …
  target: string;               // vorgeschlagenes Ziel: Rolle ("inbox","sent",…) oder Ordnername
}

interface MigrationAccountInput {
  targetEmail: string;
  username: string;             // Login auf der Quelle
  password: string;             // Passwort / App-Passwort der Quelle
  createAccount?: { name: string; password: string };  // Zielkonto neu anlegen
}

interface MigrationOptions {
  includeTrash: boolean;        // Papierkorb mitnehmen (Standard false)
  includeJunk: boolean;         // Spam mitnehmen (Standard false)
  excludeFolders: string[];     // Quellordner, die übersprungen werden
  since: string | null;         // nur Nachrichten ab Datum (YYYY-MM-DD)
  analyzeWithAI: boolean;       // importierte Mails durch die KI analysieren lassen (Standard false)
}

type MigrationStatus = "pending" | "running" | "done" | "failed" | "cancelled" | "interrupted";

interface MigrationAccountProgress {
  targetEmail: string; username: string; status: MigrationStatus;
  foldersTotal: number; foldersDone: number; currentFolder: string | null;
  messagesTotal: number; messagesDone: number;
  imported: number; skipped: number; failed: number;
  error: string | null;
}

interface MigrationJob {
  id: number; name: string; source: MigrationSource; status: MigrationStatus;
  options: MigrationOptions;
  createdAt: string; startedAt: string | null; finishedAt: string | null;
  accounts: MigrationAccountProgress[];
  log: LogEntry[];              // die letzten 100 Einträge, neueste zuerst
}

interface ImportResult { imported: number; skipped: number; failed: number; errors: string[]; }
```

| Methode | Pfad | Body | Antwort |
|---|---|---|---|
| POST | `/api/admin/migrations/test` | `{ source: MigrationSource, username, password }` | `{ ok, folders: SourceFolder[], error? }` |
| GET | `/api/admin/migrations` | – | `MigrationJob[]` (neueste zuerst) |
| POST | `/api/admin/migrations` | `{ name?, source, accounts: MigrationAccountInput[], options }` | `MigrationJob` (startet sofort) |
| GET | `/api/admin/migrations/{id}` | – | `MigrationJob` |
| POST | `/api/admin/migrations/{id}/cancel` | – | `{ ok }` |
| POST | `/api/admin/migrations/{id}/restart` | `{ accounts: { targetEmail, password }[] }` | `MigrationJob` |
| DELETE | `/api/admin/migrations/{id}` | – | `{ ok }` (nur beendete Jobs) |
| POST | `/api/mail/import` | `multipart/form-data`: `file` (.mbox, .eml oder .zip mit .eml/.mbox), optional `mailboxId` | `ImportResult` |

`/api/mail/import` importiert in das eigene Konto (Selbstbedienung, z. B. Thunderbird-Export).
Ohne `mailboxId` landen Nachrichten im Posteingang; bei .zip werden Unterordner als Ordner angelegt.
Maximale Dateigröße: 2 GB.

## DNS-Anbieter (automatische Einrichtung der DNS-Einträge)

Postwerk kann die empfohlenen DNS-Einträge einer Domain direkt beim DNS-Anbieter setzen. Ablauf:
Anbieter wählen → Zugangsdaten eingeben → **Vorschau** (Vergleich alt/neu) → ausgewählte Änderungen
**anwenden** → DNS-Prüfung erneut ausführen. Zugangsdaten werden nur für die jeweilige Anfrage
verwendet und **nicht gespeichert**.

Regeln der Vorschau:
- Fremde Einträge bleiben unberührt (z. B. andere TXT-Einträge wie Google-Verifizierungen).
- TXT-Einträge werden über ihr Präfix zugeordnet (`v=spf1`, `v=DKIM1`, `v=DMARC1`, `v=STSv1`, `v=TLSRPTv1`).
- Eine vorhandene SPF-Regel wird **zusammengeführt** (bestehende `include:`/`ip4:`/`ip6:` bleiben erhalten, `mx` kommt dazu).
- Vorhandene MX-Einträge werden **ersetzt** (Warnung, denn danach gehen Mails an Postwerk).
- Typen, die der Anbieter nicht unterstützt (z. B. MX/A bei Strato), erscheinen als `manual`.

```ts
interface DnsProviderField {
  key: string; label: string; type: "text" | "password";
  required: boolean; help: string | null; placeholder: string | null;
}

interface DnsProviderInfo {
  id: string;                 // "ionos" | "strato" | "hetzner" | "cloudflare" | "netcup" | "inwx" | "desec"
  name: string;               // Anzeigename
  official: boolean;          // false = inoffizielle Anbindung (z. B. Strato über den Kundenlogin)
  description: string;        // ein Satz
  docsUrl: string | null;     // wo man den API-Schlüssel bekommt
  fields: DnsProviderField[];
  supports: string[];         // unterstützte Record-Typen, z. B. ["A","AAAA","MX","TXT","CNAME","SRV"]
  notes: string | null;       // Hinweise/Einschränkungen
}

interface DnsPlanRequest {
  provider: string;
  credentials: Record<string, string>;
  ipv4?: string | null;       // öffentliche IPv4 für den A-Record des Hostnamens (sonst "manual")
  ipv6?: string | null;       // optional AAAA
}

type DnsChangeAction = "create" | "update" | "unchanged" | "manual";

interface DnsChange {
  key: string;                // stabiler Schlüssel, z. B. "mx", "spf", "dkim", "dmarc", "a", "aaaa", "mta-sts-txt" …
  type: string;
  name: string;               // FQDN mit Punkt am Ende
  action: DnsChangeAction;
  current: string[];          // vorhandene Werte (nur die zugeordneten)
  desired: string;            // Zielwert (bei MX "10 mail.example.com.")
  purpose: string;
  required: boolean;
  warning: string | null;     // z. B. "Bestehende MX-Einträge werden ersetzt"
  reason: string | null;      // Begründung bei "manual"
}

interface DnsPlan { zone: string; provider: string; changes: DnsChange[]; }

interface DnsApplyResult {
  applied: string[];                         // keys
  failed: { key: string; error: string }[];
  manual: string[];                          // keys, die von Hand gesetzt werden müssen
}
```

| Methode | Pfad | Body | Antwort |
|---|---|---|---|
| GET | `/api/admin/dns/providers` | – | `DnsProviderInfo[]` |
| POST | `/api/admin/domains/{name}/dns/plan` | `DnsPlanRequest` | `DnsPlan` |
| POST | `/api/admin/domains/{name}/dns/apply` | `DnsPlanRequest & { keys: string[] }` | `DnsApplyResult` |
| GET | `/api/admin/system/public-ip` | – | `{ ipv4: string \| null; ipv6: string \| null }` (fragt `api.ipify.org` ab, nur auf Knopfdruck aufrufen) |

Fehler: `dns_provider_error` (502) mit Meldung des Anbieters, z. B. bei falschen Zugangsdaten;
`invalid` (400) bei unbekanntem Anbieter oder fehlenden Feldern.

## Reverse-Proxy und Zertifikatsquelle

Hinter einem Reverse-Proxy (Nginx Proxy Manager, Traefik, Caddy, nginx/SWAG …) läuft nur die
**Weboberfläche** über den Proxy. Die Mail-Ports (25, 465, 587, 110/995) gehen weiterhin per
Portfreigabe direkt an Postwerk. Für TLS auf den Mail-Ports kann Postwerk das Zertifikat des Proxys
übernehmen (Dateien oder Traefik-`acme.json`) und lädt es bei Erneuerung automatisch neu.
Änderungen an `web` und `tls` wirken **ohne Neustart**.

### Neue/erweiterte Einstellungsabschnitte (`GET/PUT /api/admin/settings`)

```ts
interface Settings {
  // … bestehende Abschnitte …
  web: {
    publicUrl: string;            // z. B. "https://mail.example.com" (leer = unbekannt)
    behindProxy: boolean;
    trustedProxies: string[];     // IPs oder CIDRs, deren X-Forwarded-For/-Proto vertraut wird.
                                  // Leer + behindProxy = private Netze und Loopback vertrauen.
    secureCookies: boolean;       // Session-Cookie nur über HTTPS
  };
  tls: {
    mode: "selfsigned" | "acme" | "files";
    acme: { enabled: boolean; email: string; directoryUrl: string; domains: string[] };  // enabled wird aus mode abgeleitet
    files: { certPath: string; keyPath: string };   // Pfade im Container; certPath darf auch eine Traefik-acme.json sein
    status: string;               // nur lesend
  };
}
```

### Typen

```ts
type ProxyKind = "npm" | "traefik" | "caddy" | "nginx" | "swag" | "other";

interface ProxyDetect {
  viaProxy: boolean;              // Anfrage kam mit X-Forwarded-*-Headern
  remoteIp: string;               // direkte Gegenstelle (= Proxy, falls viaProxy)
  clientIp: string;               // so sieht Postwerk den Browser nach Auswertung der Einstellungen
  trusted: boolean;               // remoteIp gilt aktuell als vertrauenswürdiger Proxy
  forwardedFor: string | null;
  forwardedProto: string | null;
  forwardedHost: string | null;
  requestHost: string;
  suggestedTrustedProxy: string | null;   // Vorschlag für trustedProxies
  suggestedPublicUrl: string | null;      // aus X-Forwarded-Proto/-Host abgeleitet
}

interface ProxyConfigFile { name: string; language: "nginx" | "yaml" | "caddy" | "text"; content: string; }

interface ProxyGuide {
  kind: ProxyKind;
  title: string;                  // z. B. "Nginx Proxy Manager"
  steps: string[];                // nummerierte Anleitung (Klartext, kann `Code` in Backticks enthalten)
  files: ProxyConfigFile[];       // Konfigurationen zum Kopieren
  certificate: {                  // wie Postwerk an das Zertifikat kommt
    recommendedMode: "files" | "acme";
    hostPathHint: string | null;  // wo der Proxy seine Zertifikate ablegt (Host/Appdata)
    containerPathExample: string | null; // passender certPath in Postwerk, z. B. "/certs/live/npm-1/fullchain.pem"
    mountHint: string;            // wie das Verzeichnis nach /certs eingebunden wird
  };
  notes: string[];                // Warnungen/Hinweise
}

interface CertificateFile {
  certPath: string;               // im Container
  keyPath: string | null;         // gefundener passender Schlüssel (null bei acme.json, dort steckt er drin)
  source: "files" | "traefik";
  names: string[];                // SANs
  issuer: string;
  notAfter: string;
  matchesHostname: boolean;       // deckt den Mailserver-Hostnamen ab
}
```

### Endpunkte

| Methode | Pfad | Body / Query | Antwort |
|---|---|---|---|
| GET | `/api/admin/proxy/detect` | – | `ProxyDetect` |
| GET | `/api/admin/proxy/guide?kind=&upstream=` | `upstream` = Adresse, unter der der Proxy Postwerk erreicht, z. B. `http://192.168.188.129:8880` oder `http://postwerk:8080` | `ProxyGuide` |
| GET | `/api/admin/tls/certificates` | – | `CertificateFile[]` (durchsucht das Zertifikatsverzeichnis, Standard `/certs`) |
| POST | `/api/admin/tls/test` | `{ certPath, keyPath }` | `{ ok, names, notAfter, matchesHostname, error? }` |

Das Zertifikatsverzeichnis im Container ist `/certs` (Umgebungsvariable `POSTWERK_CERTS_DIR`); dorthin
bindet man den Zertifikatsordner des Proxys **schreibgeschützt** ein.

## Dynamische IP: DynDNS und Smarthost

Bei einem Internetanschluss mit wechselnder IP hält Postwerk den A-Record des Mailserver-Hostnamens
per **DynDNS** aktuell (Protokoll `dyndns2`, z. B. STRATO) und verschickt ausgehende Mails über einen
**Smarthost**, weil Privatkunden-IPs von großen Mailanbietern meist abgelehnt werden (Spamhaus-PBL,
kein Reverse-DNS). Postwerk ruft den DynDNS-Dienst nur bei einer IP-Änderung auf (sonst droht eine
Sperre wegen „abuse“). Die öffentliche IP ermittelt es über einen einstellbaren Dienst
(Standard `https://api.ipify.org`).

### Neue Einstellungsabschnitte (`GET/PUT /api/admin/settings`)

```ts
interface Settings {
  // … bestehende Abschnitte …
  dyndns: {
    enabled: boolean;
    preset: "strato" | "inwx" | "desec" | "noip" | "custom";
    updateUrl: string;            // dyndns2-Endpunkt; bei Presets vorbelegt
    hostnames: string[];          // z. B. ["mail.example.com"]
    username: string;             // STRATO: die Domain bzw. Subdomain
    password: string;             // maskiert "••••" wenn gesetzt
    ipCheckUrl: string;           // Dienst, der die öffentliche IPv4 als Text liefert
    intervalMinutes: number;      // Prüfintervall (Standard 5, min. 1)
  };
  relay: {                        // Smarthost für ausgehende Mails
    enabled: boolean;
    preset: "strato" | "ionos" | "brevo" | "mailjet" | "smtp2go" | "ses" | "custom";
    host: string;
    port: number;
    security: "tls" | "starttls";
    username: string;
    password: string;             // maskiert
    spfInclude: string;           // z. B. "include:_spf.strato.com" – wird im SPF-Vorschlag ergänzt
  };
}
```

Umgebungsvariablen `POSTWERK_RELAY_*` überschreiben `relay` weiterhin (dann ist `relay` in der
Oberfläche nur lesbar; `GET /api/admin/relay/presets` liefert dazu `envOverride: true`).

### Typen und Endpunkte

```ts
interface DynDNSPreset { id: string; name: string; updateUrl: string; usernameHint: string; passwordHint: string; docsUrl: string | null; notes: string | null; }
interface RelayPreset  { id: string; name: string; host: string; port: number; security: "tls" | "starttls"; usernameHint: string; spfInclude: string; docsUrl: string | null; notes: string | null; }

interface DynDNSStatus {
  enabled: boolean;
  currentIp: string | null;       // zuletzt ermittelte öffentliche IP
  dnsIps: Record<string, string[]>;   // aktuelle A-Records je Hostname (Live-Abfrage)
  lastUpdate: string | null;      // Zeitpunkt der letzten erfolgreichen Meldung
  lastResult: string | null;      // Antwort des Dienstes, z. B. "good 203.0.113.7" / "nochg …"
  error: string | null;           // z. B. "badauth – Benutzername oder DynDNS-Passwort falsch"
  pausedUntil: string | null;     // nach "abuse"/"badauth" pausiert
  nextCheck: string | null;
}
```

| Methode | Pfad | Body | Antwort |
|---|---|---|---|
| GET | `/api/admin/dyndns/presets` | – | `DynDNSPreset[]` |
| GET | `/api/admin/dyndns/status` | – | `DynDNSStatus` |
| POST | `/api/admin/dyndns/update` | – | `DynDNSStatus` (jetzt sofort prüfen und – nur bei Abweichung – melden; mit `{ force: true }` immer melden) |
| GET | `/api/admin/relay/presets` | – | `{ presets: RelayPreset[]; envOverride: boolean }` |
| POST | `/api/admin/relay/test` | – (prüft gespeicherte Einstellungen) | `{ ok: boolean; detail: string }` (Verbindung, TLS, Anmeldung) |

## DNS-Einträge als Formularfelder

`GET /api/admin/domains/{name}/dns` liefert jeden Eintrag zusätzlich in Einzelfelder zerlegt, so wie
DNS-Anbieter sie in ihren Formularen abfragen. `GET …/dns/check` enthält dieselben `key`/`title`.

```ts
interface DnsRecord {
  key: string;                 // "a" | "mx" | "spf" | "dkim" | "dmarc" | "mta-sts-txt" | "mta-sts-cname" | "tlsrpt" | "srv-submissions" | "srv-submission" | "srv-pop3s" | "autoconfig"
  type: "A" | "MX" | "TXT" | "CNAME" | "SRV" | "AAAA" | "TLSA";
  name: string;                // FQDN mit Punkt (Zonen-Ansicht)
  value: string;               // Wert in Zonen-Schreibweise
  title: string;               // verständlicher Titel, z. B. "Posteingang (MX)"
  purpose: string;             // ein bis zwei Sätze in einfacher Sprache
  required: boolean;
  zone: string;                // die Domain
  host: string;                // Name relativ zur Domain; "" = die Domain selbst (bei vielen Anbietern "@" oder leer lassen)
  outsideZone: boolean;        // gehört zu einer anderen Domain (Hostname liegt woanders)
  content: string;             // Wert ohne Punkt am Ende (A/TXT/CNAME); bei MX/SRV leer
  priority: number | null;     // MX, SRV
  weight: number | null;       // SRV
  port: number | null;         // SRV
  target: string | null;       // MX/SRV-Ziel ohne Punkt am Ende
  service: string | null;      // SRV, ohne Unterstrich, z. B. "submissions"
  protocol: string | null;     // SRV, z. B. "tcp"
}
// DnsCheck erhält zusätzlich: key: string; title: string;
```

## Backup

Archive des gesamten Systems (`postwerk-JJJJMMTT-HHMMSS.tar.gz`, verschlüsselt mit Endung `.enc`).
Inhalt: zuerst `manifest.json`, dann `data/` – ein Snapshot der Datenbank (alle Konten, Domains mit
DKIM-Schlüsseln, Aliase, Sieve-Filter, Kalender, Kontakte, Berichte, Einstellungen), alle Mail-Blobs,
Zertifikate und `master.key`, sofern der Master-Schlüssel im Datenverzeichnis liegt. Nicht enthalten:
`backups/`, `exports/`, Setup-Token und Reste früherer Wiederherstellungen. Verschlüsselte Archive
beginnen mit `PWBACKUP2` (Parameter für scrypt und 16-Byte-Salt), danach folgen AES-256-GCM-Blöcke à
64 KiB; ältere `PWBACKUP1`-Archive bleiben lesbar.

```ts
interface BackupManifest {
  format: number;           // Archivformat, derzeit 1
  version: string;          // Postwerk-Version, die das Archiv geschrieben hat
  hostname: string;
  createdAt: string;
  domains: number; accounts: number; messages: number;
  encrypted: boolean;
  schema: number;           // Datenbankschema
  keyFingerprint: string;   // Fingerabdruck des Master-Schlüssels der Geheimnisse
  keyInArchive: boolean;    // master.key liegt im Archiv
}
```

### Einstellungen (`GET/PUT /api/admin/settings`, Abschnitt `backup`)

```ts
interface BackupSettings {
  target: {
    type: "local" | "webdav" | "smb";
    localPath: string;                 // leer = /data/backups
    webdav: { url: string; username: string; password: string /* maskiert */; skipTlsVerify: boolean };
    smb: { host: string; port: number; share: string; path: string; username: string; password: string /* maskiert */; domain: string };
  };
  schedule: { enabled: boolean; frequency: "hourly" | "daily" | "weekly"; time: string /* HH:MM */; weekday: number /* 0 = So */ };
  keep: number;                        // Archive auf dem Ziel behalten, 0 = alle
  encrypt: boolean;
  password: string;                    // maskiert, mind. 12 Zeichen
}
```

### Endpunkte

| Methode | Pfad | Body | Antwort |
|---|---|---|---|
| GET | `/api/admin/backups/status` | – | `{ status: { job, last, target, nextRun }; runs: BackupRun[] }` |
| GET | `/api/admin/backups` | – | `{ archives: { name, size, modified, encrypted }[]; error?: string }` |
| POST | `/api/admin/backups` | – | Sicherung starten (409, wenn schon eine läuft) |
| POST | `/api/admin/backups/test` | `BackupSettings["target"]` | `{ ok, target, archives, latencyMs, error? }` |
| POST | `/api/admin/backups/upload` | multipart, Feld `file` | `{ name }` |
| POST | `/api/admin/backups/{name}/download-token` | `{ password, code }` (Anmeldepasswort, ggf. 2FA-Code) | `{ token }`: einmalig, 2 Minuten gültig |
| GET | `/api/admin/backups/{name}/download?token=…` | – | Archiv als Datei (403 ohne gültiges Token) |
| POST | `/api/admin/backups/{name}/restore` | `{ password, accountPassword, code, masterKey? }` (`password` = Archiv-Passwort, leer = eingestelltes; `accountPassword`/`code` = Anmeldepasswort und ggf. 2FA-Code; `masterKey` nur für Archive ohne `master.key`) | startet die Wiederherstellung, danach Neustart |
| DELETE | `/api/admin/backups/{name}` | – | `Ok` |

### Wiederherstellung und Serverumzug

Eine Wiederherstellung läuft in zwei Schritten. Zuerst wird das Archiv im laufenden Betrieb nach
`/data/restore-staging` entpackt und geprüft; erst wenn alles stimmt, wird es zum Einspielen beim nächsten
Start vorgemerkt und der Server startet neu. Geprüft wird:

1. **Archivformat:** `manifest.json` muss der erste Eintrag sein; ein Format oder Datenbankschema, das
   neuer ist als die laufende Version, wird abgelehnt (`archive_format`, 422) – erst Postwerk aktualisieren.
2. **Passwort und Unversehrtheit:** falsches Passwort (`archive_password`, 400), abgeschnittene oder
   veränderte Archive, unbekannte Pfade und eine beschädigte Datenbank (`PRAGMA quick_check`) führen zum
   Abbruch.
3. **Master-Schlüssel:** Der Schlüssel, mit dem der Server nach dem Neustart arbeiten würde
   (`POSTWERK_MASTER_KEY`, `POSTWERK_MASTER_KEY_FILE` oder `master.key` aus dem Archiv), muss den
   Prüfwert der Datenbank entschlüsseln. Fehlt er im Archiv, antwortet die API mit
   `master_key_required` (422), passt er nicht, mit `master_key_mismatch` (422). Das entpackte Archiv
   bleibt dann liegen; mit dem Schlüssel des alten Servers geht es per `…/restore/resume` (Assistent)
   bzw. erneutem Aufruf mit `masterKey` weiter. Ein angegebener Schlüssel wird als `master.key` in die
   wiederhergestellten Daten geschrieben, sofern der Server keine `POSTWERK_MASTER_KEY*`-Variable nutzt.
4. **Öffnen:** Die Datenbank wird probehalber so geöffnet wie beim Start (inkl. ausstehender Migrationen).

Schlägt einer der Schritte fehl, bleibt der laufende Server unverändert. Beim Start werden die bisherigen
Daten nach `/data/pre-restore-<zeit>` verschoben und die neuen eingesetzt. Lässt sich der
wiederhergestellte Stand dann trotzdem nicht öffnen, nimmt Postwerk den Tausch zurück: die alten Daten
kommen zurück, die neuen landen in `/data/failed-restore-<zeit>`, und der Grund erscheint auf der
Backup-Seite bzw. als `restoreError` im Setup-Status. Es bleibt jeweils nur der neueste dieser Ordner.

Auf einem **frisch installierten Server** (noch kein Konto) bietet der Setup-Assistent „Aus Backup
wiederherstellen“ an; die Endpunkte unter `/api/setup/restore/…` sind mit dem Setup-Token geschützt
(siehe [Einrichtung](#einrichtung-setup-assistent)).

## KI: in Ordner einsortieren

Abschnitt `ai.autoFile` in den Einstellungen:

```ts
interface AutoFile {
  enabled: boolean;
  mode: "categories" | "folders";          // je Kategorie fester Ordner | KI wählt aus den eigenen Ordnern des Kontos
  categoryFolders: Record<string, string>; // Kategorie → Ordner ("" = bleibt im Posteingang), "/" für Unterordner
  keepImportant: boolean;                  // Priorität "high" bleibt im Posteingang
}
```

Verschoben werden nur Nachrichten, die nach der Analyse noch im Posteingang liegen. Im Modus
`categories` werden fehlende Ordner pro Konto angelegt, im Modus `folders` nie.

## KI: ChatGPT-Abo (Anmeldung mit ChatGPT)

Mit `ai.provider = "chatgpt"` laufen die KI-Anfragen über das ChatGPT-Abo (Plus, Pro, …) des
Admins statt über einen API-Schlüssel; `baseUrl` und `apiKey` werden dann nicht verwendet. Die
Anmeldung nutzt den Gerätecode-Ablauf von `auth.openai.com`: Der Server fordert einen Code an, der
Admin gibt ihn auf der Bestätigungsseite ein, der Server fragt im Hintergrund nach (Code 15 Minuten
gültig) und speichert die Tokens mit dem Master-Schlüssel verschlüsselt außerhalb der Einstellungen.
Tokens verlassen den Server nie – weder über `/api/admin/settings` noch über die Endpunkte unten.
Das Access-Token wird kurz vor Ablauf und nach einer Ablehnung (HTTP 401) automatisch erneuert;
scheitert die Erneuerung endgültig, gilt die Verbindung als getrennt und das Dashboard (und damit die
Benachrichtigungen, Kategorie `system`) meldet `ai-chatgpt-auth`. Ein erreichtes Nutzungslimit des
Abos (HTTP 429) erscheint als `ai-chatgpt-limit`; die Analyse neuer Mails pausiert bis zum
Zurücksetzen und wird danach nachgeholt. Embeddings bietet das Abo nicht: Für die semantische Suche
ist `ai.embeddingBaseUrl` (z. B. Ollama) nötig. Lehnt die Bestätigungsseite den Code ab, muss in
ChatGPT unter Einstellungen → Sicherheit die Gerätecode-Anmeldung aktiviert werden.

```ts
interface ChatGptLogin {
  id: string;
  status: "pending" | "success" | "expired" | "denied" | "failed" | "canceled";
  userCode: string;          // auf verificationUrl einzugeben
  verificationUrl: string;   // https://auth.openai.com/codex/device
  expiresAt: string;         // RFC 3339
  interval: number;          // Abfrageintervall des Servers in Sekunden
  error?: string;
  email?: string; plan?: string;   // bei "success"
}

interface ChatGptStatus {
  connected: boolean;
  email?: string; plan?: string;   // z. B. "Plus"
  expiresAt?: string;              // Ablauf des aktuellen Access-Tokens
  connectedAt?: string;
  error?: string;                  // Verbindung verloren (Erneuerung gescheitert)
  limit?: string; limitUntil?: string;   // aktives Nutzungslimit
  login?: ChatGptLogin;            // laufende Anmeldung
}
```

| Methode | Pfad (`/api/admin`) | Body | Antwort |
|---|---|---|---|
| GET | `/ai/chatgpt` | – | `ChatGptStatus` |
| POST | `/ai/chatgpt/login` | – | `ChatGptLogin` (startet die Anmeldung, bricht eine laufende ab) |
| GET | `/ai/chatgpt/login/{id}` | – | `ChatGptLogin` (404, wenn die Anmeldung unbekannt ist) |
| POST | `/ai/chatgpt/logout` | – | `{ ok }` (widerruft das Refresh-Token, löscht die Anmeldung) |

Fehler beim Start: `chatgpt_error` (502) mit Meldung des Anmeldedienstes. `/settings/ai/test`
prüft die Anmeldung und sendet eine kurze Testanfrage; `models` enthält dann die Modellliste des
Codex-Katalogs (andere Modellnamen sind frei eintragbar).

## JMAP (RFC 8620 / RFC 8621)

Neben der REST-API spricht Postwerk JMAP für Mailclients und eigene Skripte. Das Webmail nutzt
weiterhin `/api`; JMAP ist eine zusätzliche Schnittstelle auf denselben Daten (Änderungen über
IMAP, Webmail und JMAP sind sofort überall sichtbar).

### Endpunkte

| Methode | Pfad | Zweck |
|---|---|---|
| GET | `/.well-known/jmap` (auch `/jmap/session`) | Session-Objekt mit Capabilities, Konto und URLs |
| POST | `/jmap/api` | Methodenaufrufe (`application/json`) |
| POST | `/jmap/upload/{accountId}/` | Blob hochladen, Body = Rohdaten, `Content-Type` = Medientyp |
| GET | `/jmap/download/{accountId}/{blobId}/{name}?type={type}` | Blob herunterladen (immer `Content-Disposition: attachment`, `nosniff`) |
| GET | `/jmap/eventsource?types={types}&closeafter={closeafter}&ping={ping}` | Push per Server-Sent Events (`StateChange`) |

Capabilities: `urn:ietf:params:jmap:core`, `urn:ietf:params:jmap:mail`, `urn:ietf:params:jmap:submission`.
Die Kontokennung hat die Form `A<id>` und steht in `primaryAccounts`.

### Anmeldung

- **HTTP Basic** mit E-Mail-Adresse und Kontopasswort oder App-Passwort (bei aktiver 2FA nur App-Passwort),
  gleiche Prüfung und Brute-Force-Sperre wie IMAP/SMTP. **Nur über HTTPS** (oder hinter einem
  vertrauenswürdigen Proxy mit `X-Forwarded-Proto: https`); über HTTP antwortet der Server mit `403`.
  Für Tests ohne TLS gilt `POSTWERK_ALLOW_INSECURE_AUTH=true`.
  Geprüfte Zugangsdaten werden bis zu 2 Minuten im Speicher gehalten; ein geändertes Passwort oder ein
  widerrufenes App-Passwort wirkt deshalb spätestens nach 2 Minuten.
- **Session-Cookie** der Weboberfläche (`pw_session`): für `POST` ist wie bei `/api` der Header
  `X-Postwerk: 1` Pflicht (CSRF-Schutz). `GET` (Session, Download, EventSource) geht ohne Header.
- Bearer-Token werden nicht unterstützt.

### Methoden

| Typ | Methoden |
|---|---|
| Core | `Core/echo` |
| Mailbox | `get`, `changes`, `query`, `queryChanges`¹, `set` |
| Thread | `get`, `changes` |
| Email | `get`, `changes`, `query`, `queryChanges`¹, `set`, `import`, `parse` |
| SearchSnippet | `get` |
| Identity | `get`, `changes`² |
| EmailSubmission | `get`, `changes`, `query`, `set` (mit `onSuccessUpdateEmail` / `onSuccessDestroyEmail`) |

¹ liefert immer `cannotCalculateChanges` – der Client führt die Abfrage einfach neu aus.
² Identitäten sind die Kontoadresse und die Aliase, die auf das Konto zeigen; bei Änderungen `cannotCalculateChanges`.

Unterstützt werden Rückverweise (`#ids` mit `resultOf`/`name`/`path` inkl. `/*`), Erzeugungs-IDs
(`#c1` in `ids`, `mailboxIds`, `emailId`, `parentId`, `onSuccessUpdateEmail`), `createdIds` und die
Standardfehler (`unknownMethod`, `invalidArguments`, `invalidResultReference`, `accountNotFound`,
`requestTooLarge`, `stateMismatch`, `cannotCalculateChanges`, `anchorNotFound`, `unsupportedFilter`,
`unsupportedSort` …). Limits stehen im Session-Objekt (`maxCallsInRequest` 32, `maxObjectsInGet`/`maxObjectsInSet` 500,
`maxSizeRequest` 10 MB, `maxSizeUpload` = maximale Nachrichtengröße, `maxConcurrentUpload` 4).

### Hinweise für Clients

- **Eine Mailbox je E-Mail** (`maxMailboxesPerEmail: 1`). Verschieben per Patch
  `{"mailboxIds/<alt>": null, "mailboxIds/<neu>": true}`; mehrere Mailboxen ergeben `invalidProperties`.
- **Ordnerhierarchie** über `/` im Namen (wie IMAP): `Kunden/2024` erscheint als `2024` mit `parentId` von `Kunden`.
  Ordner mit Rolle (`inbox`, `drafts`, `sent`, `archive`, `junk`, `trash`) können nicht gelöscht werden,
  der Posteingang kann auch nicht umbenannt werden.
- **Keywords**: `$seen`, `$flagged`, `$answered`, `$draft` entsprechen den IMAP-Flags, weitere Keywords
  (z. B. `$forwarded`, `$junk`) werden als IMAP-Keywords gespeichert (klein geschrieben).
- **Email/query**: Filter `inMailbox`, `inMailboxOtherThan`, `before`, `after`, `minSize`, `maxSize`,
  `hasKeyword`, `notKeyword`, `all/some/noneInThreadHaveKeyword`, `hasAttachment`, `text`, `body`, `from`, `to`,
  `cc`, `bcc`, `subject`, `header` sowie `AND`/`OR`/`NOT`; Sortierung `receivedAt`, `sentAt` (= Empfangszeit), `size`,
  `from`, `to`, `subject`, `hasKeyword`; `collapseThreads`, `position`/`anchor`/`limit` (höchstens 1000 IDs).
  `header`- und `bcc`-Filter lesen die Nachrichten und sind auf Konten mit bis zu 20 000 Nachrichten begrenzt.
- **Blob-IDs**: `G<sha256>` für ganze Nachrichten und Uploads, `G<sha256>_<teil>_<teil>` für MIME-Teile
  (auch Teile angehängter Nachrichten). Uploads verfallen nach 24 Stunden, wenn sie nicht verwendet werden.
- **Entwürfe** entstehen mit `Email/set` `create` aus `textBody`/`htmlBody`/`attachments` (Anhänge per
  hochgeladener `blobId`) oder `bodyStructure`; fertige Nachrichten kommen per `Email/import` aus einem Upload.
- **Senden** mit `EmailSubmission/set`: Der Server nimmt die gespeicherte E-Mail, entfernt `Bcc`, prüft den
  Absender (From/Sender müssen zum Konto gehören, sonst `forbiddenFrom`), signiert per DKIM und stellt lokal zu
  bzw. legt die Nachricht in die Warteschlange. Ohne `envelope` ergeben sich die Empfänger aus To/Cc/Bcc.
  Die Ablage in „Gesendet“ erledigt `onSuccessUpdateEmail`, z. B.
  `{"#s1": {"mailboxIds/<Entwürfe>": null, "mailboxIds/<Gesendet>": true, "keywords/$draft": null}}`.
  Verzögertes Senden und Zurückholen gibt es nicht (`undoStatus` ist immer `final`).
- **Änderungen**: Jede Änderung an Ordnern, Nachrichten und Threads erhöht eine fortlaufende Nummer pro Konto
  (Grundlage für `state` und `/changes`). Gelöschte Objekte werden 30 Tage gemerkt (höchstens 100 000 je Konto
  und Typ); ältere Zustände ergeben `cannotCalculateChanges`, dann lädt der Client neu.
- **Push**: Die EventSource sendet `event: state` mit `{"@type":"StateChange","changed":{"A1":{"Email":"42",…}}}`
  für die Typen `Mailbox`, `Email`, `Thread`, `EmailDelivery` (neue eingehende Mail) und `EmailSubmission`;
  `ping` (10–600 s) sendet `event: ping`, `closeafter=state` beendet nach dem ersten Ereignis.
  Hinter nginx o. Ä. die Pufferung für `/jmap/eventsource` abschalten (der Server sendet `X-Accel-Buffering: no`).
- CORS-Header werden nicht gesendet: Web-Clients müssen unter derselben Adresse laufen (oder über einen Proxy).

Beispiel (curl):

```sh
curl -u anna@example.org:APP-PASSWORT https://mail.example.org/.well-known/jmap
curl -u anna@example.org:APP-PASSWORT -H 'Content-Type: application/json' https://mail.example.org/jmap/api -d '{
  "using": ["urn:ietf:params:jmap:core", "urn:ietf:params:jmap:mail"],
  "methodCalls": [
    ["Email/query", {"accountId": "A1", "filter": {"inMailbox": "M1"}, "sort": [{"property": "receivedAt", "isAscending": false}], "limit": 10}, "q"],
    ["Email/get", {"accountId": "A1", "#ids": {"resultOf": "q", "name": "Email/query", "path": "/ids"},
                   "properties": ["subject", "from", "receivedAt", "keywords", "preview"]}, "g"]
  ]}'
```

## Verzeichnis (LDAP) und Single Sign-on (OIDC)

Konten haben eine Anmeldequelle `source`: `"local"` (Passwort in Postwerk), `"ldap"` (Passwort wird per
Bind gegen das Verzeichnis geprüft) oder `"oidc"` (kein Passwort, Weboberfläche nur per SSO). Das Feld
steht in `Me` und `Account`; `POST /api/admin/accounts` und `PATCH /api/admin/accounts/{id}` nehmen
`source` an (für `ldap`/`oidc` ohne Passwort; beim Wechsel auf `local` ist `password` Pflicht).
App-Passwörter und 2FA bleiben immer in Postwerk. Konfiguration von Authentik und Keycloak:
[SSO.md](SSO.md).

Neue Fehlercodes: `sso_only` (403, Passwort-Login für die Weboberfläche abgeschaltet),
`directory_unavailable` (503, LDAP nicht erreichbar), `external_password` (400, Passwort wird extern
verwaltet), `sso_reauth_required` (403, SSO-Konto muss sich für die Bestätigung neu anmelden).

### Einstellungen (`GET/PUT /api/admin/settings`, Abschnitte `ldap` und `oidc`)

```ts
interface LdapSettings {
  enabled: boolean;
  url: string;                 // ldap://… oder ldaps://…
  startTls: boolean;           // nur mit ldap://
  skipTlsVerify: boolean;
  bindDn: string;              // Dienstkonto, leer = anonym
  bindPassword: string;        // maskiert, an die URL gebunden
  baseDn: string;
  userFilter: string;          // Standard "(&(objectClass=person)(mail=%s))", %s = maskierte Adresse
  mailAttribute: string;       // Standard "mail"
  nameAttribute: string;       // Standard "cn"
  quotaAttribute: string;      // optional, Byte oder mit Einheit (5G)
  adminFilter: string;         // optional, z. B. "memberOf=cn=mail-admins,ou=groups,dc=example,dc=org"
  autoProvision: boolean;      // Konto beim ersten Login anlegen (Domain muss existieren)
  defaultQuotaBytes: number;
  syncDisabled: boolean;       // nicht mehr gefundene Benutzer deaktivieren
}

interface OidcSettings {
  enabled: boolean;
  issuerUrl: string;
  clientId: string;
  clientSecret: string;        // maskiert, an die Issuer-URL gebunden
  scopes: string[];            // Standard ["openid", "email", "profile"]
  buttonLabel: string;
  emailClaim: string;          // Standard "email"
  allowUnverifiedEmail: boolean; // sonst muss email_verified=true sein
  autoProvision: boolean;
  defaultQuotaBytes: number;
  adminClaim: string;          // optional, z. B. "groups"
  adminValue: string;          // z. B. "mail-admins"
  onlySso: boolean;            // Passwort-Login (Web) nur noch für lokale Admins
  rpLogout: boolean;           // beim Abmelden auch beim Anbieter abmelden
}
```

`onlySso` lässt sich nur speichern, wenn mindestens ein aktives lokales Administratorkonto existiert.

### Endpunkte

| Methode | Pfad | Body | Antwort |
|---|---|---|---|
| GET | `/api/auth/sso` (öffentlich) | – | `{ enabled, label, onlySso }` |
| GET | `/api/auth/oidc/start` | – | Weiterleitung zum Identitätsanbieter (setzt Cookie `pw_oidc`) |
| GET | `/api/auth/oidc/callback` | `?code&state` | setzt `pw_session`, Weiterleitung auf `/`; Fehler: `/login?sso_error=<code>` |
| POST | `/api/auth/logout` | – | `{ ok, endSessionUrl? }` (nach SSO-Login mit `rpLogout`) |
| GET | `/api/admin/sso/info` | – | `{ redirectUri, postLogoutRedirectUri, publicUrlConfigured, localAdmins }` |
| POST | `/api/admin/oidc/test` | `{ issuerUrl? }` | `{ ok, latencyMs, discovery?, error? }` |
| POST | `/api/admin/ldap/test` | `{ email?, config?: Partial<LdapSettings> }` | `{ ok, latencyMs, entry?, account?, error? }` |
| POST | `/api/admin/ldap/sync` | `{ apply }` | `{ users[], orphans[], created, disabled, truncated }` |

`sso_error`-Codes: `state` (abgelaufen, schon benutzt oder anderer Browser), `denied`, `email_unverified`,
`email_missing`, `no_account`, `disabled`, `sso_disabled`, `unavailable`, `rate_limited`, `failed`.

Ablauf: Authorization Code mit PKCE (S256). `state` und `nonce` liegen serverseitig (10 Minuten, einmal
verwendbar) und sind über das Cookie `pw_oidc` an den Browser gebunden, der die Anmeldung begonnen hat.
Das ID-Token wird auf Signatur, Issuer, Audience, Ablauf und Nonce geprüft. Eine SSO-Anmeldung
überspringt Postwerks eigene 2FA (die MFA liegt beim Anbieter). Konten ohne Passwort (`source: "oidc"`)
bestätigen sensible Änderungen wie neue App-Passwörter durch eine höchstens 15 Minuten alte SSO-Anmeldung.
## Verschlüsselung im Ruhezustand

### Master-Schlüssel (Geheimnisse in der Datenbank)

Geheimnisse werden in SQLite mit AES-256-GCM verschlüsselt gespeichert (`enc:v1:<base64(nonce|ciphertext)>`):
alle Einstellungsfelder mit dem Struct-Tag `secret:"true"` (KI-/Embedding-Schlüssel, Paperless-Token,
DynDNS-, Relay-, Backup-, WebDAV- und SMB-Passwort; neue Felder wie LDAP/OIDC-Secrets brauchen nur den
Tag), TOTP-Geheimnisse, DKIM-Schlüssel und das SRS-Geheimnis. Klartext aus älteren Versionen wird beim
Start automatisch verschlüsselt. Die API liefert Geheimnisse weiterhin maskiert (`••••`).

Schlüsselquelle in dieser Reihenfolge:

1. `POSTWERK_MASTER_KEY` – 32 Byte, base64 (`openssl rand -base64 32`)
2. `POSTWERK_MASTER_KEY_FILE` – Pfad zu einer Datei mit dem base64-Schlüssel
3. `/data/master.key` – wird beim ersten Start erzeugt (Modus 0600)

Ein verschlüsselter Prüfwert in der Datenbank erkennt einen falschen Schlüssel: Postwerk startet dann
nicht, statt Geheimnisse zu verlieren.

**Backups:** Liegt der Schlüssel als `master.key` im Datenverzeichnis, ist er **immer** im Archiv
enthalten – ohne ihn wären nach einer Wiederherstellung alle Geheimnisse verloren. Unverschlüsselte
Archive schützen die Geheimnisse deshalb nicht; die Backup-Seite weist darauf hin. Kommt der Schlüssel
aus `POSTWERK_MASTER_KEY` oder einer Datei außerhalb des Datenverzeichnisses, ist er in keinem Archiv
(auch eine liegengebliebene `master.key` wird dann ausgelassen) – für jede Wiederherstellung muss
derselbe Schlüssel gesetzt sein oder bei der Wiederherstellung angegeben werden (siehe
[Wiederherstellung und Serverumzug](#wiederherstellung-und-serverumzug)). Das Manifest jedes Archivs
nennt den Fingerabdruck des Schlüssels. `GET /api/admin/backups/status` enthält dazu
`status.masterKey: { source: "env" | "env-file" | "data-file"; inArchive: boolean; fingerprint: string }`.

### Speicherverschlüsselung der Nachrichten (Admin, optional)

Mit eingeschalteter Option werden neue Blobs mit einem aus dem Master-Schlüssel abgeleiteten Schlüssel
verschlüsselt (Datei beginnt mit `\0PWBLOB1`, 16-Byte-Salt, AES-256-GCM-Blöcke à 64 KiB, Blockzähler und
Ende-Markierung im Nonce). Der Dateiname bleibt der SHA-256 des Klartexts, das Lesen ist transparent.
Schützt vor Festplatten-/Backup-Diebstahl nur, wenn der Master-Schlüssel außerhalb des Datenverzeichnisses
liegt. Standard: aus.

```ts
interface EncryptionStatus {
  masterKey: { source: "env" | "env-file" | "data-file"; file?: string; inDataDir: boolean; fingerprint: string };
  blobs: { enabled: boolean; stats?: { total: number; encrypted: number; bytes: number }; job: ConversionJob | null };
  mailEncryptionAccounts: number;
  protectedSecrets: string[];
}
interface ConversionJob {
  running: boolean; encrypt: boolean; done: number; total: number;
  converted: number; skipped: number; failed: number; error?: string;
  startedAt: string; finishedAt: string | null;

## Absender-Authentifizierung: DKIM-Schlüssel, ARC, postmaster@, Berichte

### DKIM-Schlüssel und Schlüsselwechsel

Jede Domain kann mehrere DKIM-Schlüssel haben. Ausgehende Mails werden mit **allen aktiven** Schlüsseln
signiert (höchstens einer je Algorithmus, also RSA und optional Ed25519 nach RFC 8463).

- `pending`: erzeugt und für den DNS vorgesehen, signiert noch nicht.
- `active`: signiert. Das Aktivieren eines neuen Schlüssels mustert den aktiven Schlüssel desselben Algorithmus aus.
- `retired`: signiert nicht mehr, soll aber noch 7 Tage im DNS bleiben (`removableAt`). Erst danach kann er gelöscht werden.
  Ist `server.retention.dkimCleanup` an (Standard), löscht die tägliche Aufräumrunde den Schlüssel weitere
  7 Tage später selbst (`autoDeleteAt`). Der TXT-Eintrag gelöschter Schlüssel erscheint unter
  `dkim-dns-cleanup` und wird von der automatischen DNS-Einrichtung als Aktion `delete` angeboten.

Der aktive RSA-Schlüssel ist zugleich `Domain.dkimSelector`. Die empfohlenen DNS-Einträge
(`/domains/{name}/dns`) enthalten zusätzlich jeden weiteren Schlüssel unter dem Key `dkim-<selector>`;
die automatische Einrichtung beim DNS-Anbieter setzt aktive und vorbereitete Schlüssel.

```ts
interface DkimKey {
  id: number; domain: string; selector: string;
  algorithm: "rsa" | "ed25519";
  state: "pending" | "active" | "retired";
  createdAt: string; activatedAt: string | null; retiredAt: string | null;
  removableAt: string | null;   // retired: ab wann der DNS-Eintrag entfernt werden kann
  autoDeleteAt: string | null;  // retired: wann der Schlüssel automatisch gelöscht wird (null = aus)
  dnsName: string;              // "<selector>._domainkey.<domain>."
  dnsValue: string;             // "v=DKIM1; k=rsa|ed25519; p=..."
}
interface DkimPublication { name: string; published: boolean; found: string[]; error?: string }

## Filterregeln (Sieve)

Jedes Konto kann bis zu 20 Sieve-Skripte (RFC 5228, je höchstens 64 KB) speichern; höchstens eines ist
aktiv. Unterstützte Erweiterungen: `fileinto`, `reject`, `ereject`, `envelope`, `imap4flags`, `copy`,
`body`, `variables`, `vacation`, `relational`, `regex`, `subaddress`, `date`, `mailbox`,
`comparator-i;ascii-numeric`. Dieselben Skripte lassen sich per ManageSieve (RFC 5804, Port 4190,
`POSTWERK_MANAGESIEVE_ADDR`, Anmeldung mit AUTHENTICATE PLAIN erst nach STARTTLS) verwalten.

Zustellung:

- Das aktive Skript läuft bei der lokalen Zustellung nach Spam- und Virenprüfung, bevor die Nachricht im
  Posteingang landet. Als Spam eingestufte Nachrichten (auch DMARC-Quarantäne und Viren-Quarantäne)
  gehen ohne Skript in den Spam-Ordner.
- `fileinto` sucht den Ordner per Name (`INBOX` = Posteingang); fehlt er, landet die Nachricht im
  Posteingang, außer bei `:create`.
- `redirect` (höchstens 5 je Nachricht) stellt lokal direkt zu und schickt externe Ziele mit SRS-Absender
  über die Warteschlange. Der Kopf `X-Sieve-Redirected-By` verhindert Schleifen zwischen Konten.
- `reject`/`ereject` lehnen bei einem einzigen Empfänger direkt im SMTP-Dialog ab (550), sonst geht eine
  Unzustellbarkeitsnachricht mit dem Grund an den Absender.
- Fehler zur Laufzeit führen zum impliziten `keep` (Posteingang).
- Die Abwesenheitsnotiz aus den Kontoeinstellungen bleibt unabhängig und antwortet nach dem Skript, sofern
  die Nachricht in einem Ordner abgelegt wurde und das Skript nicht selbst `vacation` ausgeführt hat.
- Sub-Adressierung: `name+stichwort@domain` wird an `name@domain` zugestellt, wenn die volle Adresse
  weder Konto noch Alias ist. `envelope :detail "to"` liefert `stichwort`.

```ts
interface SieveScriptInfo { name: string; active: boolean; size: number; updatedAt: string; }
interface SieveScript extends SieveScriptInfo { content: string; }
interface SieveList { scripts: SieveScriptInfo[]; active: string | null; capabilities: string[]; maxSize: number; maxScripts: number; }
interface SieveError { error: "invalid_script"; message: string; detail: string; line: number; column: number; }
interface SieveAction {
  type: "keep" | "fileinto" | "redirect" | "discard" | "reject" | "ereject" | "vacation";
  mailbox?: string; create?: boolean; copy?: boolean; flags?: string[]; address?: string; reason?: string;
  vacation?: { days: number; subject?: string; from?: string; addresses?: string[]; handle?: string; reason: string };
  implicit?: boolean;              // implizites keep
}
interface SieveTestResult { actions: SieveAction[]; implicitKeep: boolean; script: string; error?: string; }
```

| Methode | Pfad | Body | Antwort |
|---|---|---|---|
| GET | `/api/admin/domains/{name}/dkim` | – | `DkimKey[]` |
| POST | `/api/admin/domains/{name}/dkim` | `{ algorithm: "rsa" \| "ed25519" }` | `DkimKey` (Zustand `pending`; 409, wenn es schon einen neuen Schlüssel dieses Typs gibt) |
| POST | `/api/admin/domains/{name}/dkim/{id}/check` | – | `DkimPublication` (Live-Abfrage im DNS) |
| POST | `/api/admin/domains/{name}/dkim/{id}/activate` | `{ force?: boolean }` | `DkimKey[]`; ohne `force` 409 `dkim_not_published`, solange der Eintrag nicht im DNS steht |
| DELETE | `/api/admin/domains/{name}/dkim/{id}` | – | `{ ok }` (nur `pending`, oder `retired` nach Ablauf der 7 Tage; sonst 409) |
| GET | `/api/admin/domains/{name}/dkim-dns-cleanup` | – | `{ selector, dnsName }[]` – TXT-Einträge gelöschter Schlüssel, die noch im DNS stehen können |
| DELETE | `/api/admin/domains/{name}/dkim-dns-cleanup/{selector}` | – | `{ ok }` – Eintrag als entfernt markieren |
| GET | `/api/admin/mta-sts` | – | `{ mode, maxAge, policy, id, checks: { host, purpose: "mx" \| "policy", ok, detail }[], enforceReady }` |

Bestehende Installationen übernehmen den bisherigen Schlüssel beim Update als aktiven RSA-Schlüssel.

### ARC (RFC 8617)

Eingehende Mails mit ARC-Headern werden geprüft (`cv=none/pass/fail`, Sätze `i=1..50`); das Ergebnis
steht als `arc=` im `Authentication-Results`-Header. Scheitert DMARC, ist die ARC-Kette aber gültig und
von einem vertrauenswürdigen Weiterleiter versiegelt, gilt eine lokale Ausnahme:

- hat der Weiterleiter laut seinem `ARC-Authentication-Results` DMARC (oder eine passende DKIM-Signatur)
  bestanden, wird die Mail normal zugestellt,
- sonst wird aus `p=reject` eine Quarantäne (Spam-Ordner).

Die Ausnahme steht als `dmarc=fail (policy overridden: arc=pass, sealed by …)` im Header. Mails, die
über einen Alias an externe Ziele weitergeleitet werden, erhalten einen neuen ARC-Satz (AAR mit unseren
Prüfergebnissen, AMS über die Nachricht, AS über die Kette), signiert mit dem aktiven DKIM-Schlüssel der
Alias-Domain. Eine bereits als `cv=fail` markierte Kette wird nicht weiter versiegelt.

Einstellungsabschnitt `mailauth` (`GET/PUT /api/admin/settings`):

```ts
interface MailAuthSettings {
  trustedArcSealers: string[];  // d= der ARC-Seals, Subdomains eingeschlossen
                                // Standard: google.com, microsoft.com, outlook.com, icloud.com, yahoo.com
  postmasterAddress: string;    // Ziel für postmaster@/abuse@ (leer = erstes aktives Admin-Konto)
  skipReportDelivery: boolean;  // vollständig ausgewertete Berichtsmails nicht zustellen (Standard: false)
}
```

### postmaster@ und abuse@

Für jede Domain ohne eigenes Konto oder eigenen Alias mit diesen Adressen werden `postmaster@` und
`abuse@` automatisch an `mailauth.postmasterAddress` bzw. das erste aktive Admin-Konto zugestellt
(vor einem Catch-all).

| Methode | Pfad | Body | Antwort |
|---|---|---|---|
| GET | `/api/admin/aliases/system` | – | `{ address, targets: string[], system: true, configured: boolean }[]` (nur lesbar) |

### DMARC- und TLS-Berichte

Mails an `postmaster@`, `dmarc@`, `dmarc-reports@`, `rua@`, `tlsrpt@` oder `tls-reports@` einer eigenen
Domain (oder mit Betreff „Report Domain: …“ bzw. Header `TLS-Report-Domain`) werden nach Berichten
durchsucht: DMARC-Aggregatberichte als XML, ZIP oder GZIP, TLS-Berichte als JSON oder GZIP. Entpackt
werden höchstens 20 MB je Mail. Gespeichert werden nur Berichte über eigene Domains (und deren
Subdomains), doppelt eingehende Berichte nur einmal. Die Mail selbst wird normal zugestellt, außer
`mailauth.skipReportDelivery` ist gesetzt: Dann entfällt die Zustellung an die Berichtsadressen, sofern
alle Berichte der Mail eigene Domains betreffen, gespeichert wurden und nichts unlesbar war. Andere
Empfänger derselben Mail bekommen sie weiterhin; Mails, die sich nicht vollständig auswerten lassen,
werden immer zugestellt. Berichte werden nach 400 Tagen gelöscht.

```ts
interface DmarcDomainSummary { domain: string; reports: number; messages: number; passed: number; failed: number; reporters: string[] }
interface DmarcSource {
  domain: string; sourceIp: string; count: number;
  spfPass: number; dkimPass: number; dmarcPass: number;   // Anzahl Nachrichten (ausgerichtete Ergebnisse)
  quarantined: number; rejected: number; reporters: string[]; headerFrom: string[];
}
interface DmarcReport {
  id: number; domain: string; orgName: string; email: string; reportId: string;
  begin: string; end: string; policy: { domain: string; adkim: string; aspf: string; p: string; sp: string; pct: string };
  messages: number; failed: number; receivedAt: string;
  records?: {
    sourceIp: string; count: number; disposition: string; dkim: string; spf: string; headerFrom: string; envelopeFrom: string;
    dkimResults: { domain: string; selector?: string; result: string }[];
    spfResults: { domain: string; scope?: string; result: string }[];
  }[];
}
interface TlsDomainSummary { domain: string; reports: number; success: number; failure: number; reporters: string[] }
interface TlsFailureGroup {
  domain: string; resultType: string; receivingMxHostname: string; sessions: number;
  sendingIps: string[]; reporters: string[]; lastSeen: string;
}
interface TlsReport {
  id: number; domain: string; orgName: string; email: string; reportId: string; begin: string; end: string;
  policyType: string; policyString: string[]; mxHosts: string[]; successCount: number; failureCount: number;
  failures: { resultType: string; sendingMtaIp: string; receivingMxHostname: string; receivingIp: string;
              failedSessionCount: number; additionalInfo?: string; failureReasonCode?: string }[];
  receivedAt: string;
}
```

| Methode | Pfad | Body | Antwort |
|---|---|---|---|
| GET | `/api/admin/security/encryption` | `?stats=1` zählt die Blob-Dateien | `EncryptionStatus` |
| PUT | `/api/admin/security/encryption/blobs` | `{ enabled, migrate }` | `EncryptionStatus` (`migrate`: vorhandene Blobs sofort umwandeln) |
| POST | `/api/admin/security/encryption/blobs/migrate` | `{ encrypt }` | `EncryptionStatus` (409, wenn schon eine Umwandlung läuft) |

### Mails mit dem eigenen Schlüssel verschlüsseln (pro Konto)

Benutzer hinterlegen einen öffentlichen OpenPGP-Schlüssel oder ein S/MIME-Zertifikat (RSA). Jede danach
gespeicherte Nachricht (Zustellung, Sieve, Import, IMAP-APPEND, Gesendet-Kopie; nicht: Entwürfe) wird als
letzter Schritt vor dem Speichern verschlüsselt – PGP/MIME (RFC 3156, `multipart/encrypted`) bzw. S/MIME
(`application/pkcs7-mime; smime-type=enveloped-data`, AES-256-CBC). Der verschlüsselte Teil ist die
komplette Originalnachricht; außen bleiben nur `From`, `To`, `Subject` (optional als `...`), `Date` und
`Message-ID`. Bereits verschlüsselte Nachrichten werden nicht doppelt verschlüsselt. KI-Analyse,
Paperless, Vorschau und Volltextsuche entfallen für verschlüsselte Nachrichten. Schlägt die
Verschlüsselung fehl, wird nicht im Klartext gespeichert (die Zustellung wird temporär abgelehnt).
Abgelaufene OpenPGP-Schlüssel werden weiter verwendet, widerrufene nicht.
`GET /api/mail/messages/{id}` enthält `encrypted: boolean`.

```ts
interface MailEncryptionConfig {
  kind: "pgp" | "smime"; publicKey: string; fingerprint: string; description: string;
  algorithm: string; expires: string | null; hideSubject: boolean; createdAt: string; updatedAt: string;
}
interface KeyInfo { kind: "pgp" | "smime"; fingerprint: string; description: string; emails: string[]; algorithm: string; expires: string | null }
```

| Methode | Pfad | Body | Antwort |
|---|---|---|---|
| GET | `/api/account/encryption` | – | `{ config: MailEncryptionConfig \| null; job: ConversionJob \| null; messages: number }` |
| POST | `/api/account/encryption/inspect` | `{ kind, publicKey }` | `{ key: KeyInfo; matchesAccount: boolean }` (400 bei ungültigem oder privatem Schlüssel) |
| PUT | `/api/account/encryption` | `{ kind, publicKey, hideSubject, password, code }` (ohne `publicKey`: nur Optionen ändern; mit neuem Schlüssel sind Passwort und ggf. 2FA-Code nötig) | wie GET |
| DELETE | `/api/account/encryption` | – | wie GET (neue Mails wieder im Klartext, verschlüsselte bleiben verschlüsselt) |
| POST | `/api/account/encryption/encrypt-existing` | `{ password, code }` | wie GET; verschlüsselt vorhandene Nachrichten im Hintergrund (neue UID, Vorschau/KI-Daten werden entfernt) |

## Datenexport (DSGVO)

ZIP mit `konto.json` (Kontodaten, Abwesenheitsnotiz, Verschlüsselungs-Einstellung – ohne Geheimnisse),
`app-passwoerter.json` (nur Namen/Nutzung), `sitzungen.json`, `mail/<Ordner>.mbox` (mboxrd, eine Datei pro
Ordner), `mail/ordner.json`, `kalender.ics` (VEVENT/VTODO) und `LIESMICH.txt`. Weitere Bereiche (z. B.
Kontakte) hängen sich über `export.RegisterSection` ein. Verschlüsselte Nachrichten werden so exportiert,
wie sie gespeichert sind. Erstellt wird im Hintergrund nach `data/exports/` (nicht im Backup); der
Download ist 24 Stunden gültig, danach löscht der Aufräumjob die Datei. Selbst angeforderte Exporte sind
auf einen pro Stunde begrenzt (429 `rate_limited`), Admin-Exporte nicht.

```ts
interface DataExport {
  id: number; accountId: number; requestedBy: number;
  status: "pending" | "running" | "done" | "failed" | "expired";
  phase: string; done: number; total: number; size: number; error?: string;
  createdAt: string; finishedAt: string | null; expiresAt: string | null;
}
```

| Methode | Pfad | Body | Antwort |
|---|---|---|---|
| GET | `/api/account/exports` | – | `DataExport[]` (eigene, neueste zuerst) |
| POST | `/api/account/exports` | – | `DataExport` (429 bei mehr als einem Export pro Stunde, 409 während ein Export läuft) |
| GET | `/api/account/exports/{id}/download` | – | ZIP |
| GET | `/api/admin/accounts/{id}/exports` | – | `DataExport[]` des Kontos |
| POST | `/api/admin/accounts/{id}/exports` | – | `DataExport` |
| GET | `/api/admin/exports/{id}/download` | – | ZIP (wird protokolliert) |

| GET | `/api/admin/reports/dmarc?domain=&days=30` | – | `{ summary: DmarcDomainSummary[]; sources: DmarcSource[]; reports: DmarcReport[] }` (Quellen mit Fehlern zuerst) |
| GET | `/api/admin/reports/dmarc/{id}` | – | `DmarcReport` mit `records` |
| DELETE | `/api/admin/reports/dmarc/{id}` | – | `{ ok }` |
| GET | `/api/admin/reports/tls?domain=&days=30` | – | `{ summary: TlsDomainSummary[]; failures: TlsFailureGroup[]; reports: TlsReport[] }` |
| DELETE | `/api/admin/reports/tls/{id}` | – | `{ ok }` |

| GET | `/api/mail/sieve` | – | `SieveList` |
| GET | `/api/mail/sieve/{name}` | – | `SieveScript` |
| PUT | `/api/mail/sieve/{name}` | `{ content, activate? }` | `SieveScript`; ungültig → 400 `SieveError` |
| DELETE | `/api/mail/sieve/{name}` | – | `Ok`; aktives Skript → 409 |
| POST | `/api/mail/sieve/{name}/activate` | – | `Ok` |
| POST | `/api/mail/sieve/deactivate` | – | `Ok` |
| POST | `/api/mail/sieve/check` | `{ content }` | `{ ok: true }` oder 400 `SieveError` |
| POST | `/api/mail/sieve/test` | `{ content?, messageId?, raw?, envelopeFrom?, envelopeTo? }` | `SieveTestResult` |

`message` in `SieveError` hat die Form `Zeile 3, Spalte 5: „;“ fehlt nach „keep“`. Der Test ist ein
Probelauf ohne Wirkung; ohne `content` wird das aktive Skript verwendet, als Umschlag-Absender dient
ohne `envelopeFrom` die Absenderadresse der Nachricht.

Der Regel-Editor im Webmail speichert seine Regeln als Skript `postwerk-regeln` (die Regeln zusätzlich als
JSON im Kommentar `# rules:`); wurde das Skript von Hand geändert, zeigt der Editor einen Hinweis.

## Kalender und Kontakte (CalDAV/CardDAV)

Postwerk ist CalDAV- (RFC 4791) und CardDAV-Server (RFC 6352). Alle Kalender und Adressbücher eines
Kontos sind über die JSON-API und per DAV erreichbar; beide arbeiten auf denselben Daten
(Tabellen `dav_collections`, `calendar_objects`, `address_objects`). Beim ersten Start nach dem Update
werden die bisherigen Tabellen `events`/`tasks` einmalig in iCalendar-Objekte des Standardkalenders
übernommen (die alten Zeilen bleiben unangetastet liegen).

### Typen

```ts
interface Calendar { id: number; slug: string; name: string; color: string; description: string; components: string[]; url: string; }
interface AddressBook { id: number; slug: string; name: string; color: string; description: string; components: string[]; url: string; count: number; }
interface ContactEntry { type: string; value: string; }   // E-Mail: home|work|other; Telefon: cell|home|work|other
interface Contact { id: number; addressBookId: number; uid: string; name: string; firstName: string; lastName: string;
  emails: ContactEntry[]; phones: ContactEntry[]; organization: string; title: string; notes: string; birthday: string | null; updatedAt: string; }
type ContactInput = Omit<Contact, "id" | "uid" | "updatedAt" | "addressBookId"> & { addressBookId?: number };
interface ImportResult { imported: number; updated: number; skipped: number; }
interface Suggestion { name: string; email: string; source: "contact" | "recent"; }
```

`url` ist der DAV-Pfad der Sammlung. Jedes Konto hat mindestens den Kalender „Kalender“ (VEVENT und VTODO)
und das Adressbuch „Kontakte“; sie werden beim ersten Zugriff angelegt.

### Endpunkte

| Methode | Pfad | Body | Antwort |
|---|---|---|---|
| GET | `/api/calendars` | – | `Calendar[]` |
| POST | `/api/calendars` | `{ name, color?, description?, components? }` | `Calendar` |
| PATCH | `/api/calendars/{id}` | `{ name?, color?, description? }` | `Calendar` |
| DELETE | `/api/calendars/{id}` | – | `{ ok }`; `409` beim letzten Kalender |
| GET | `/api/calendars/{id}/export` | – | `.ics`-Datei |
| POST | `/api/calendars/{id}/import` | `{ data: string }` (ICS-Text) | `ImportResult` (gleiche UID = Aktualisierung) |
| GET | `/api/addressbooks` | – | `AddressBook[]` |
| POST / PATCH / DELETE | `/api/addressbooks[/{id}]` | `{ name }` | wie bei Kalendern |
| GET | `/api/contacts?q=&addressBook=&limit=` | – | `Contact[]` (Suche in Name, Organisation, E-Mail, Telefon) |
| GET | `/api/contacts/{id}` | – | `Contact` |
| POST | `/api/contacts` | `ContactInput` | `Contact` |
| PUT | `/api/contacts/{id}` | `ContactInput` | `Contact` (unbekannte vCard-Felder wie Foto oder Adresse bleiben erhalten) |
| DELETE | `/api/contacts/{id}` | – | `{ ok }` |
| GET | `/api/contacts/export?addressBook=` | – | `.vcf`-Datei (alle oder ein Adressbuch) |
| POST | `/api/contacts/import` | `{ addressBookId?, data: string }` (vCard-Text) | `ImportResult` |
| GET | `/api/contacts/suggest?q=&limit=` | – | `Suggestion[]`: Kontakte, danach zuletzt angeschriebene Adressen (Gesendet) |
| GET | `/api/account/dav` | – | `{ caldavUrl, carddavUrl, principalUrl, server, username }` |
| GET | `/api/account/apple-profile` | – | `.mobileconfig` (unsigniert) mit IMAP, SMTP, CalDAV und CardDAV, ohne Passwort |

### DAV-Server

| Pfad | Bedeutung |
|---|---|
| `/.well-known/caldav`, `/.well-known/carddav` | `301` auf `/dav/` (RFC 6764) |
| `/dav/principals/<E-Mail>/` | Principal (`current-user-principal`, `calendar-home-set`, `addressbook-home-set`) |
| `/dav/calendars/<E-Mail>/<kalender>/<name>.ics` | Kalender und Objekte |
| `/dav/addressbooks/<E-Mail>/<adressbuch>/<name>.vcf` | Adressbücher und vCards |

- **Anmeldung:** HTTP Basic mit Kontopasswort oder App-Passwort (bei aktivierter 2FA nur App-Passwort), über
  dieselbe Brute-Force-Sperre wie IMAP/SMTP (Protokoll `dav`). Nur über HTTPS, direkt oder über einen
  vertrauenswürdigen Reverse-Proxy mit `X-Forwarded-Proto: https`; unverschlüsselte Anfragen werden mit `403`
  abgelehnt (Ausnahme: `POSTWERK_ALLOW_INSECURE_AUTH` für Tests). Der CSRF-Header der JSON-API gilt für `/dav/` nicht.
- **Methoden:** `OPTIONS`, `PROPFIND` (Depth 0/1), `PROPPATCH` (Name, Farbe, Beschreibung), `GET`/`HEAD`
  (auf eine Sammlung: alles als eine Datei), `PUT` mit `If-Match`/`If-None-Match`, `DELETE`, `MKCALENDAR`,
  erweitertes `MKCOL` (RFC 5689), `MOVE` zwischen Sammlungen.
- **REPORT:** `calendar-query` (inkl. `time-range` mit Wiederholungen), `calendar-multiget`,
  `addressbook-query`, `addressbook-multiget`, `sync-collection` (RFC 6578).
- **Eigenschaften:** u. a. `getetag`, `getctag` (CalendarServer), `sync-token`, `calendar-color` (Apple),
  `supported-calendar-component-set`, `supported-report-set`, `current-user-privilege-set`.
- Gespeichert wird der Text so, wie der Client ihn schickt; die ETag-Antwort auf `PUT` entspricht deshalb
  exakt dem gespeicherten Objekt. Wiederholungsregeln bleiben unverändert erhalten.
- DNS: `_caldavs._tcp` / `_carddavs._tcp` (SRV auf Port 443) und je ein TXT `path=/dav/` stehen in der DNS-Anleitung.

## Sendelimits und Benachrichtigungen

### Sendelimits pro Konto

Begrenzt den Versand jedes Kontos (auch Admins), damit ein übernommenes Konto nicht unbegrenzt Spam
verschicken kann. Gezählt werden Versand über SMTP (587/465), Webmail (`POST /api/mail/send`), JMAP
`EmailSubmission/set` und Sieve-Weiterleitungen (`redirect`) an externe Adressen. Die Stunden- und
Tagesfenster gleiten und liegen in SQLite (überstehen Neustarts).

- **Empfänger pro Nachricht:** alle Umschlag-Empfänger, auch lokale. SMTP lehnt jeden weiteren
  `RCPT TO` mit `452 4.5.3 Too many recipients` ab; Webmail antwortet `429 send_limit`, JMAP mit
  dem SetError `tooManyRecipients`.
- **Nachrichten pro Stunde / externe Empfänger pro Tag:** nur Empfänger, die den Server verlassen.
  SMTP antwortet vorübergehend mit `450 4.7.1` (schon beim `RCPT TO` externer Empfänger, sonst nach `DATA`),
  Webmail mit `429 send_limit`, JMAP mit `forbiddenToSend` (deutsche Beschreibung). Eine Sieve-
  Weiterleitung über dem Limit entfällt, die Nachricht bleibt im Posteingang.
- Jeder Treffer wird protokolliert, zählt `postwerk_send_limit_hits_total{kind}` hoch, erscheint
  24 Stunden als Dashboard-Warnung `sendlimit-<Konto-ID>` und löst – falls eingerichtet – sofort eine
  Benachrichtigung aus.

```ts
// GET/PUT /api/admin/settings, Abschnitt "limits" (0 = unbegrenzt)
interface SendLimitSettings {
  maxRecipientsPerMessage: number;   // Standard 100, höchstens 1000
  maxMessagesPerHour: number;        // Standard 300
  maxRecipientsPerDay: number;       // Standard 2000
}
// pro Konto: null = Standard, 0 = unbegrenzt; exempt hebt alle Limits auf
interface SendLimitOverride {
  maxRecipientsPerMessage: number | null;
  maxMessagesPerHour: number | null;
  maxRecipientsPerDay: number | null;
  exempt: boolean;
}
interface SendLimitInfo {
  override: SendLimitOverride;
  defaults: SendLimitSettings;
  effective: SendLimitSettings & { exempt: boolean };
  usage: { messagesLastHour: number; recipientsLastHour: number; messagesLastDay: number; recipientsLastDay: number };
  hitsToday: number;                 // Limit-Treffer in den letzten 24 Stunden
  lastHit: string | null;
}
```

### Benachrichtigungen

Die Warnungen des Dashboards (Zertifikat, Sicherung, Warteschlange, Sperrlisten, PTR, Sendelimits)
werden alle 5 Minuten geprüft; fehlgeschlagene Sicherungen und Sendelimit-Treffer sofort. Jede neue
Warnung erzeugt eine Mail, solange sie aktiv bleibt höchstens alle 24 Stunden eine Erinnerung, und
nach dem Verschwinden eine kurze „Behoben“-Mail. Der Zustand liegt in der Tabelle `notify_state`.
Absender ist `postmaster@` der zum Hostnamen passenden Domain; die Mails sind DKIM-signiert und gehen
mit leerem Umschlag-Absender in die Warteschlange, Fehlschläge erzeugen daher keine Bounces.

```ts
// GET/PUT /api/admin/settings, Abschnitt "notifications"
interface NotificationSettings {
  enabled: boolean;
  recipients: string[];              // leer = alle Admin-Konten
  categories: ("certificate" | "backup" | "queue" | "blacklist" | "dns" | "sendlimit" | "system")[];
  minSeverity: "warning" | "error";
  webhookUrl: string;                // optional, http:// oder https://
  webhookToken: string;              // maskiert; als "Authorization: Bearer …" gesendet
}
// Webhook: POST application/json
interface WebhookPayload {
  event: "new" | "reminder" | "resolved" | "test";
  id: string; category: string; severity: "warning" | "error";
  title: string; message: string;
  priority: number;                  // Gotify-Skala: 8 Fehler, 5 Warnung, 3 behoben/Test
  host: string; link?: string; since: string; time: string;
}
```

| Methode | Pfad | Body | Antwort |
|---|---|---|---|
| GET | `/api/admin/accounts/{id}/send-limits` | – | `SendLimitInfo` |
| PUT | `/api/admin/accounts/{id}/send-limits` | `SendLimitOverride` | `SendLimitInfo` |
| GET | `/api/admin/notifications/state` | – | `{ key, severity, title, firstSeen, lastSeen, lastNotified }[]` |
| POST | `/api/admin/notifications/test` | – | `{ recipients: string[]; mailError?: string; webhook: boolean; webhookError?: string }` |
