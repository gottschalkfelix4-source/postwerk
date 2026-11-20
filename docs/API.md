# Postwerk HTTP-API (v1)

Alle Endpunkte liegen unter `/api`. Request- und Response-Bodies sind JSON (`Content-Type: application/json`),
sofern nicht anders angegeben. Zeitangaben sind RFC-3339-Strings (UTC).

## Konventionen

- **Session:** Nach dem Login setzt der Server das Cookie `pw_session` (HttpOnly, SameSite=Strict).
- **CSRF:** Jeder Request mit Methode `POST`, `PUT`, `PATCH` oder `DELETE` muss den Header
  `X-Postwerk: 1` tragen, sonst antwortet der Server mit `403`.
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
  dms: { paperless: { enabled: boolean; url: string; token: string /* maskiert */; autoForward: boolean; categories: string[] } };
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

interface CalendarEvent { id: number; title: string; start: string; end: string | null; location: string | null; description: string | null; sourceMessageId: number | null; }
interface Task { id: number; title: string; due: string | null; done: boolean; sourceMessageId: number | null; createdAt: string; }

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
| POST | `/domains` | `{ name }` | `Domain` (erzeugt DKIM-Schlüssel) |
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
| POST | `/summarize` | `{ messageId } \| { threadId }` | `{ summary, bullets: string[] }` |
| POST | `/reply` | `{ messageId, instruction?, tone?: "formal"\|"friendly"\|"short" }` | `{ subject, body }` |
| GET | `/search?q=&limit=` | – | `{ items: (MessageSummary & { score: number })[] }` |
| POST | `/extract` | `{ messageId }` | `{ events: ExtractedEvent[], tasks: ExtractedTask[] }` |
| POST | `/reclassify` | `{ messageId }` | `MessageSummary` |

## Kalender, Aufgaben, DMS

| Methode | Pfad | Body | Antwort |
|---|---|---|---|
| GET | `/api/calendar/events?from=&to=` | – | `CalendarEvent[]` |
| POST | `/api/calendar/events` | `Omit<CalendarEvent,"id">` | `CalendarEvent` |
| DELETE | `/api/calendar/events/{id}` | – | `{ ok }` |
| GET | `/api/tasks` | – | `Task[]` |
| POST | `/api/tasks` | `{ title, due?, sourceMessageId? }` | `Task` |
| PATCH | `/api/tasks/{id}` | `{ title?, due?, done? }` | `Task` |
| DELETE | `/api/tasks/{id}` | – | `{ ok }` |
| POST | `/api/dms/forward` | `{ messageId, attachmentId }` | `{ ok }` |

## Sonstiges

- `GET /metrics` – Prometheus-Format (optional mit `Authorization: Bearer <METRICS_TOKEN>`).
- `GET /healthz` – `ok`.
- `GET /.well-known/mta-sts.txt` – MTA-STS-Policy (ab Meilenstein 3).

## Einrichtung (Setup-Assistent)

Postwerk kennt drei Setup-Zustände:

- `initial` – es gibt noch kein Konto. Der Server hat beim Start einen **Setup-Token** ins Log
  geschrieben (und nach `/data/setup-token.txt`). Nur damit lässt sich das erste Admin-Konto anlegen.
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
| POST | `/api/admin/setup/complete` | – | `{ ok }` (Modus → `done`) |
| GET | `/api/admin/system/check` | – | `SystemCheck` |
| POST | `/api/admin/system/test-clamav` | `{ address }` | `{ ok, error? }` |
| GET | `/api/admin/domains/{name}/dns/check` | – | `DnsCheck[]` (Live-Abfrage im DNS) |

Fehlercodes: `invalid_token` (403), `already_initialized` (409).

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
