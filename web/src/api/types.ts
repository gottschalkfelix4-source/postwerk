// Types of the Postwerk HTTP API (v1), taken verbatim from docs/API.md.
// The section at the bottom contains request/response shapes that the
// contract only describes inline in its endpoint tables.

export type Role = "admin" | "user";

export interface Me {
  id: number; email: string; name: string; role: Role;
  totpEnabled: boolean; quotaBytes: number; usedBytes: number;
  /** A new password must be set before the account can be used (generated initial password). */
  mustChangePassword: boolean;
  /** Where the password lives: Postwerk ("local"), LDAP directory or SSO only ("oidc"). */
  source: AccountSource;
}

export type AccountSource = "local" | "ldap" | "oidc";

export interface Domain {
  name: string;
  catchAll: string | null;      // target address for catch-all or null
  dkimSelector: string;
  accountCount: number;
  aliasCount: number;
  createdAt: string;
}

export interface DnsRecord {
  key: string;                 // "a" | "mx" | "spf" | "dkim" | "dmarc" | "mta-sts-txt" | "mta-sts-cname" | "tlsrpt" | "srv-…" | "autoconfig"
  type: "MX" | "TXT" | "CNAME" | "A" | "AAAA" | "TLSA" | "SRV";
  name: string;                // FQDN with trailing dot (zone notation)
  value: string;               // value in zone notation
  title: string;               // plain-language title, e.g. "Posteingang (MX)"
  purpose: string;             // one or two plain-language sentences
  required: boolean;
  // Split into the fields DNS providers ask for (docs/API.md, "DNS-Einträge als Formularfelder").
  zone: string;                // the domain
  host: string;                // name relative to the domain; "" = the domain itself
  outsideZone: boolean;        // belongs to another domain (host name lives elsewhere)
  content: string;             // value without trailing dot (A/TXT/CNAME); empty for MX/SRV
  priority: number | null;     // MX, SRV
  weight: number | null;       // SRV
  port: number | null;         // SRV
  target: string | null;       // MX/SRV target without trailing dot
  service: string | null;      // SRV, without underscore, e.g. "submissions"
  protocol: string | null;     // SRV, e.g. "tcp"
}

export interface Account {
  id: number; email: string; name: string; role: Role;
  quotaBytes: number;          // 0 = unlimited
  usedBytes: number; disabled: boolean; totpEnabled: boolean;
  aliases: string[]; createdAt: string;
  source: AccountSource;
}

export interface Alias { id: number; address: string; targets: string[]; createdAt: string; }

export interface QueueItem {
  id: number; from: string; to: string[]; size: number; attempts: number;
  nextAttempt: string; lastError: string | null; createdAt: string;
}

export interface LogEntry { time: string; level: "debug" | "info" | "warn" | "error"; component: string; message: string; fields: Record<string, string>; }

export interface Ban { ip: string; reason: string; until: string; failures: number; }

export interface Dashboard {
  stats: {
    accounts: number; domains: number; messages: number; storageBytes: number; queueSize: number;
    received24h: number; sent24h: number; spam24h: number; virus24h: number; rejected24h: number;
  };
  series: { hours: string[]; received: number[]; sent: number[]; spam: number[] }; // last 24 hours
  categories: { name: string; count: number }[];   // AI categories of the last 7 days
  recentEvents: LogEntry[];
  version: string; uptimeSeconds: number;
  ai: { enabled: boolean; reachable: boolean; model: string };
  /** Operational problems (certificate, queue, backup, blocklists, PTR). */
  warnings?: DashboardWarning[];
}

export interface DashboardWarning {
  id: string;
  severity: "warning" | "error";
  title: string;
  detail: string;
  link?: string; // route in the web interface
}

export type HttpPortMode = "open" | "redirect" | "auto-close";

export interface TlsIssueResult {
  ok: boolean; names: string[]; notAfter: string | null; issuer: string; error?: string;
  httpPort: "open" | "redirect" | "closed"; httpStatus: string;
}

export interface Settings {
  server: {
    hostname: string;
    maxMessageSize: number;
    /** Days until Trash/Junk are emptied automatically (0 = never). Missing in old settings. */
    retention?: {
      trashDays: number;
      junkDays: number;
      /** Delete retired DKIM keys automatically (default on). */
      dkimCleanup?: boolean;
      /** Days deleted calendar entries/contacts are remembered for sync clients (0 = forever). */
      davTombstoneDays?: number;
    };
  };
  web: {
    publicUrl: string;            // e.g. "https://mail.example.com" (empty = unknown)
    behindProxy: boolean;
    trustedProxies: string[];     // IPs or CIDRs whose X-Forwarded-For/-Proto are trusted.
                                  // Empty + behindProxy = trust private networks and loopback.
    secureCookies: boolean;       // session cookie only over HTTPS
  };
  tls: {
    mode: TlsMode;
    acme: { enabled: boolean; email: string; directoryUrl: string; domains: string[] };  // enabled is derived from mode
    files: { certPath: string; keyPath: string };   // paths inside the container; certPath may also be a Traefik acme.json
    /** Port 80: "open" (UI over HTTP), "redirect" (only ACME + redirect), "auto-close" (closed while the certificate is valid). */
    httpPort: HttpPortMode;
    status: string;               // read-only
    httpStatus: string;           // read-only: current state of port 80
  };
  spam: {
    enabled: boolean; junkThreshold: number; rejectThreshold: number;
    greylisting: { enabled: boolean; delaySeconds: number };
    dnsbl: { enabled: boolean; servers: string[] };
    bayes: { enabled: boolean; autoLearn: boolean; minTokens: number };
  };
  antivirus: { enabled: boolean; clamdAddress: string; action: "reject" | "quarantine" };
  ai: {
    enabled: boolean;
    /** Wire protocol of baseUrl: OpenAI-compatible (Ollama, OpenAI, …), Anthropic Messages API, or "chatgpt"
     *  (ChatGPT subscription via device-code sign-in; baseUrl/apiKey unused, see adminApi.chatgpt*). */
    provider: AiProvider;
    baseUrl: string; apiKey: string /* masked "••••" when set */;
    chatModel: string; embeddingModel: string; timeoutSeconds: number;
    /** Optional separate OpenAI-compatible endpoint for embeddings (needed with "anthropic" and "chatgpt"). */
    embeddingBaseUrl: string; embeddingApiKey: string /* masked */;
    maxTokens: number;            // anthropic: max_tokens per request
    features: { spamCheck: boolean; classify: boolean; priority: boolean; summarize: boolean;
                replyDraft: boolean; semanticSearch: boolean; extractEvents: boolean };
    categories: string[];          // e.g. ["Rechnung","Newsletter","Support","Privat"]
    /** Move analyzed messages out of the inbox into folders. */
    autoFile: {
      enabled: boolean;
      /** "categories": one folder per category (created on demand); "folders": AI picks one of the user's own folders. */
      mode: "categories" | "folders";
      categoryFolders: Record<string, string>;   // category → folder name ("" = stays in the inbox)
      keepImportant: boolean;                    // high priority stays in the inbox
    };
  };
  dms: { paperless: { enabled: boolean; url: string; token: string /* masked */; autoForward: boolean; categories: string[]; accounts: string[] } };
  security: { bruteForce: { maxAttempts: number; windowSeconds: number; banSeconds: number } };
  dyndns: {
    enabled: boolean;
    preset: DynDNSPresetId;
    updateUrl: string;            // dyndns2 endpoint; prefilled for presets
    hostnames: string[];          // e.g. ["mail.example.com"]
    username: string;             // STRATO: the domain or subdomain
    password: string;             // masked "••••" when set
    ipCheckUrl: string;           // service returning the public IPv4 as plain text
    intervalMinutes: number;      // check interval (default 5, min. 1)
  };
  relay: {                        // smarthost for outgoing mail
    enabled: boolean;
    preset: RelayPresetId;
    host: string;
    port: number;
    security: RelaySecurity;
    username: string;
    password: string;             // masked
    spfInclude: string;           // e.g. "include:_spf.strato.com" – added to the SPF suggestion
  };
  backup: BackupSettings;
  ldap: LdapSettings;
  oidc: OidcSettings;
  /** Sender authentication: trusted ARC sealers and the postmaster@/abuse@ target. */
  mailauth: {
    trustedArcSealers: string[];
    postmasterAddress: string;   // empty = first admin account
    /** Our own MTA-STS policy: mode and max_age in seconds. */
    mtaSts?: MtaStsSettings;
  };
  /** Outbound send limits per account (defaults; overrides per account). */
  limits: SendLimitSettings;
  /** Operational warnings by mail and webhook. */
  notifications: NotificationSettings;
}

/** Server-wide default send limits; 0 = unlimited. */
export interface SendLimitSettings {
  maxRecipientsPerMessage: number; // local and remote envelope recipients (max. 1000)
  maxMessagesPerHour: number;      // messages with at least one remote recipient
  maxRecipientsPerDay: number;     // remote recipients in 24 hours
}

export type NotificationCategory = "certificate" | "backup" | "queue" | "blacklist" | "dns" | "sendlimit" | "system";

export interface NotificationSettings {
  enabled: boolean;
  recipients: string[];            // empty = all admin accounts
  categories: NotificationCategory[];
  minSeverity: "warning" | "error";
  webhookUrl: string;              // optional, http(s)
  webhookToken: string;            // masked; sent as Bearer token
}

export type MtaStsMode = "none" | "testing" | "enforce";

export interface MtaStsSettings {
  mode: MtaStsMode;
  maxAge: number;
}

/** Certificate check of one host the MTA-STS policy depends on. */
export interface MtaStsCheck {
  host: string;
  purpose: "mx" | "policy";
  ok: boolean;
  detail: string;
}

export interface MtaStsStatus {
  mode: MtaStsMode;
  maxAge: number;
  policy: string;
  /** id of the _mta-sts TXT record (changes with the policy). */
  id: string;
  checks: MtaStsCheck[];
  enforceReady: boolean;
}

/** LDAP directory as account source (secrets masked). */
export interface LdapSettings {
  enabled: boolean;
  url: string;                    // ldap://… or ldaps://…
  startTls: boolean;
  skipTlsVerify: boolean;
  bindDn: string;
  bindPassword: string;           // masked
  baseDn: string;
  userFilter: string;             // %s = escaped email address
  mailAttribute: string;
  nameAttribute: string;
  quotaAttribute: string;
  adminFilter: string;            // e.g. memberOf=cn=mail-admins,ou=groups,dc=example,dc=org
  autoProvision: boolean;
  defaultQuotaBytes: number;
  syncDisabled: boolean;
}

/** OpenID Connect single sign-on for the web interface (secrets masked). */
export interface OidcSettings {
  enabled: boolean;
  issuerUrl: string;
  clientId: string;
  clientSecret: string;           // masked
  scopes: string[];
  buttonLabel: string;
  emailClaim: string;
  allowUnverifiedEmail: boolean;
  autoProvision: boolean;
  defaultQuotaBytes: number;
  adminClaim: string;
  adminValue: string;
  onlySso: boolean;
  rpLogout: boolean;
}

export type BackupTargetType = "local" | "webdav" | "smb";
export type BackupFrequency = "hourly" | "daily" | "weekly";

export interface BackupTarget {
  type: BackupTargetType;
  localPath: string;              // empty = <data>/backups
  webdav: { url: string; username: string; password: string /* masked */; skipTlsVerify: boolean };
  smb: { host: string; port: number; share: string; path: string; username: string; password: string /* masked */; domain: string };
}

export interface BackupSettings {
  target: BackupTarget;
  schedule: { enabled: boolean; frequency: BackupFrequency; time: string /* HH:MM */; weekday: number /* 0 = Sonntag */ };
  keep: number;                   // archives kept on the target (0 = all)
  encrypt: boolean;
  password: string;               // masked
}

export interface BackupArchive { name: string; size: number; modified: string; encrypted: boolean; }

export interface BackupJob {
  kind: "backup" | "restore";
  trigger: string;
  archive: string;
  phase: string;
  done: number;
  total: number;
  startedAt: string;
}

export interface BackupResult { kind: "backup" | "restore"; archive: string; ok: boolean; error?: string; finishedAt: string; }

export interface BackupStatus { job: BackupJob | null; last: BackupResult | null; target: string; nextRun: string | null; }

export interface BackupRun {
  id: number;
  trigger: "manual" | "schedule" | "restore";
  status: "running" | "success" | "failed";
  archive: string;
  target: string;
  size: number;
  files: number;
  error: string;
  startedAt: string;
  finishedAt: string | null;
}

export interface BackupTestResult { ok: boolean; target: string; archives: number; latencyMs: number; error?: string; }

export interface Mailbox { id: number; name: string; role: "inbox" | "sent" | "drafts" | "trash" | "junk" | "archive" | null; total: number; unread: number; }

export interface Address { name: string; email: string; }

export interface MessageSummary {
  id: number; mailboxId: number; threadId: number;
  from: Address; to: Address[]; subject: string; preview: string;
  receivedAt: string; size: number; seen: boolean; flagged: boolean; hasAttachments: boolean;
  tags: string[]; category: string | null; priority: "high" | "normal" | "low";
  spamScore: number; summary: string | null;  // short AI summary, if available
  aiSuspicious?: boolean;                      // AI analysis suspects a prompt injection
}

export interface Attachment { id: number; filename: string; contentType: string; size: number; }

export interface AuthResults { spf: string; dkim: string; dmarc: string; } // "pass" | "fail" | "none" | ...

export interface ExtractedEvent { title: string; start: string; end: string | null; location: string | null; description: string | null; }
export interface ExtractedTask { title: string; due: string | null; }

export interface MessageDetail extends MessageSummary {
  cc: Address[]; replyTo: Address[]; messageId: string; inReplyTo: string | null;
  textBody: string; htmlBody: string | null;   // HTML is NOT sanitized: the client must sanitize it
  attachments: Attachment[]; auth: AuthResults;
  virus: string | null;                         // signature name, if found
  ai: { summary: string | null; reasons: string[]; events: ExtractedEvent[]; tasks: ExtractedTask[] } ;
}

/** Event or occurrence of a recurring event. All-day events use plain dates with an exclusive end. */
export interface CalendarEvent {
  id: number; calendarId: number; uid: string; title: string; start: string; end: string | null; allDay: boolean;
  location: string | null; description: string | null; rrule: string | null; instance: string | null; sourceMessageId: number | null;
}
export interface EventInput {
  calendarId?: number; title: string; start: string; end?: string | null; allDay?: boolean;
  location?: string | null; description?: string | null; rrule?: string; sourceMessageId?: number | null;
  /** On update: occurrence start the edit is based on; the server shifts the series by start - instance. */
  instance?: string | null;
}
export interface Task {
  id: number; calendarId: number; uid: string; title: string; due: string | null; done: boolean;
  description: string | null; sourceMessageId: number | null; createdAt: string;
}

export interface Calendar { id: number; name: string; color: string; description: string; components: string[]; url: string; }
export interface CalendarInput { name?: string; color?: string; description?: string; components?: string[]; }
export interface AddressBook { id: number; name: string; description: string; url: string; count: number; }
export interface DavImportResult { imported: number; updated: number; skipped: number; }

export interface ContactEntry { type: string; value: string; }
export interface ContactInput {
  addressBookId?: number; name: string; firstName: string; lastName: string; emails: ContactEntry[]; phones: ContactEntry[];
  organization: string; title: string; notes: string; birthday: string | null;
}
export interface Contact extends ContactInput { id: number; addressBookId: number; uid: string; updatedAt: string; }
export interface AddressSuggestion { name: string; email: string; source: "contact" | "recent"; }
export interface DavInfo { caldavUrl: string; carddavUrl: string; principalUrl: string; server: string; username: string; }

export interface Vacation { enabled: boolean; subject: string; body: string; from: string | null; until: string | null; }
export interface AppPassword { id: number; name: string; createdAt: string; lastUsedAt: string | null; }

// ---------------------------------------------------------------------------
// Request / response shapes described inline in the endpoint tables.
// ---------------------------------------------------------------------------

export interface ApiErrorBody { error: string; message: string; }

export interface Ok { ok: boolean; }

export interface Paged<T> { total: number; items: T[]; }

export interface LoginRequest { email: string; password: string; totp?: string; }

export interface TotpSetup { secret: string; otpauthUrl: string; }

export type CreatedAppPassword = AppPassword & { password: string };

/** Response of enabling 2FA: one-time recovery codes, shown only once. */
export interface TotpEnableResult { ok: boolean; recoveryCodes: string[]; }

export interface RecoveryCodesStatus { remaining: number; }

export interface RecoveryCodes { recoveryCodes: string[]; }

/** A signed-in browser session of the own account. */
export interface AccountSession {
  id: string;
  createdAt: string;
  lastUsedAt: string | null;
  expiresAt: string;
  ip: string;
  userAgent: string;
  current: boolean;
}

export interface CreateAccountRequest { email: string; name: string; password: string; role: Role; quotaBytes: number; source?: AccountSource; }

export interface UpdateAccountRequest {
  name?: string; password?: string; role?: Role; quotaBytes?: number; disabled?: boolean; resetTotp?: boolean; source?: AccountSource;
}

export type AiProvider = "openai" | "anthropic" | "chatgpt";

/** Device-code sign-in with a ChatGPT subscription (runs on the server). */
export interface ChatGptLogin {
  id: string;
  status: "pending" | "success" | "expired" | "denied" | "failed" | "canceled";
  userCode: string;
  verificationUrl: string;
  expiresAt: string;
  interval: number;
  error?: string;
  email?: string;
  plan?: string;
}

/** State of the ChatGPT sign-in; tokens never leave the server. */
export interface ChatGptStatus {
  connected: boolean;
  email?: string;
  plan?: string;
  expiresAt?: string;
  connectedAt?: string;
  /** Set when the sign-in was lost (refresh failed). */
  error?: string;
  /** Active usage limit of the plan. */
  limit?: string;
  limitUntil?: string;
  /** Sign-in currently waiting for the code to be entered. */
  login?: ChatGptLogin;
}

export interface AiTestResult {
  ok: boolean; latencyMs: number; models: string[]; error?: string;
  chatOk?: boolean; embeddingOk?: boolean; embeddingError?: string;
}

/** Body of PUT /api/admin/settings: sections may be sent individually. */
export type SettingsUpdate = { [K in keyof Settings]?: DeepPartial<Settings[K]> };

export type DeepPartial<T> = T extends (infer U)[]
  ? U[]
  : T extends object
    ? { [K in keyof T]?: DeepPartial<T[K]> }
    : T;

export interface MessageQuery {
  mailbox?: number; q?: string; tag?: string; category?: string; unread?: boolean;
  limit?: number; offset?: number;
}

export interface UpdateMessageRequest { seen?: boolean; flagged?: boolean; mailboxId?: number; tags?: string[]; }

export type BulkAction = "read" | "unread" | "flag" | "unflag" | "move" | "delete" | "spam" | "notspam";

export interface BulkRequest { ids: number[]; action: BulkAction; mailboxId?: number; }

export interface OutgoingAttachment { filename: string; contentType: string; dataBase64: string; }

export interface SendRequest {
  to: string[]; cc?: string[]; bcc?: string[]; subject: string; text: string; html?: string;
  inReplyTo?: number; draftId?: number; attachments?: OutgoingAttachment[];
}

export type DraftRequest = SendRequest & { draftId?: number };

export interface TagCount { name: string; count: number; }

export interface AiStatus { enabled: boolean; features: Settings["ai"]["features"]; categories: string[]; dmsEnabled: boolean; }

export type SummarizeRequest = { messageId: number } | { threadId: number };

export interface SummarizeResult { summary: string; bullets: string[]; suspicious?: boolean; }

export type ReplyTone = "formal" | "friendly" | "short";

export interface ReplyRequest { messageId: number; instruction?: string; tone?: ReplyTone; }

export interface ReplyDraft { subject: string; body: string; suspicious?: boolean; }

export type ScoredMessage = MessageSummary & { score: number };

export interface ExtractResult { events: ExtractedEvent[]; tasks: ExtractedTask[]; suspicious?: boolean; }

export interface CreateTaskRequest { calendarId?: number; title: string; due?: string | null; description?: string | null; sourceMessageId?: number | null; }

export interface UpdateTaskRequest { title?: string; due?: string | null; done?: boolean; description?: string | null; }

// ---------------------------------------------------------------------------
// Setup wizard (docs/API.md, "Einrichtung (Setup-Assistent)").
// ---------------------------------------------------------------------------

export interface SetupStatus { mode: "initial" | "configure" | "done"; hostname: string; version: string; }

export type CheckStatus = "ok" | "missing" | "mismatch" | "warning" | "error" | "info";

export interface DnsCheck {
  key: string; title: string;
  type: DnsRecord["type"]; name: string; expected: string; found: string[];
  status: CheckStatus; purpose: string; required: boolean;
}

export interface SystemCheck {
  hostname: string;
  hostnameIps: string[];              // A/AAAA of the hostname
  ptr: { ip: string; names: string[]; matches: boolean }[];   // reverse DNS per IP
  port25Outbound: CheckStatus;        // "ok" | "error" (blocked) | "warning"
  port25Detail: string;
  tls: string;                        // same as Settings.tls.status
  clamav: CheckStatus;                // "ok" | "error" | "info" (disabled)
  clamavDetail: string;
  ai: CheckStatus;                    // "ok" | "error" | "info"
  aiDetail: string;
}

export interface SetupInitializeRequest {
  token: string; hostname: string; domain: string; adminEmail: string; adminName: string; adminPassword: string;
}

export interface ClamavTestResult { ok: boolean; error?: string; }

// ---------------------------------------------------------------------------
// Migration (docs/API.md, "Migration").
// ---------------------------------------------------------------------------

export type ImapSecurity = "tls" | "starttls" | "none";

export interface MigrationSource { host: string; port: number; security: ImapSecurity; }

export interface SourceFolder {
  name: string;                 // path on the source, e.g. "INBOX" or "Projekte/2025"
  messages: number;
  specialUse: string | null;    // "\Sent", "\Drafts", "\Junk", "\Trash", "\Archive" …
  target: string;               // suggested target: role ("inbox","sent",…) or folder name
}

export interface MigrationAccountInput {
  targetEmail: string;
  username: string;             // login on the source
  password: string;             // password / app password on the source
  createAccount?: { name: string; password: string };  // create the target account
}

export interface MigrationOptions {
  includeTrash: boolean;        // migrate trash (default false)
  includeJunk: boolean;         // migrate junk (default false)
  excludeFolders: string[];     // source folders to skip
  since: string | null;         // only messages since date (YYYY-MM-DD)
  analyzeWithAI: boolean;       // run imported messages through the AI (default false)
}

export type MigrationStatus = "pending" | "running" | "done" | "failed" | "cancelled" | "interrupted";

export interface MigrationAccountProgress {
  targetEmail: string; username: string; status: MigrationStatus;
  foldersTotal: number; foldersDone: number; currentFolder: string | null;
  messagesTotal: number; messagesDone: number;
  imported: number; skipped: number; failed: number;
  error: string | null;
}

export interface MigrationJob {
  id: number; name: string; source: MigrationSource; status: MigrationStatus;
  options: MigrationOptions;
  createdAt: string; startedAt: string | null; finishedAt: string | null;
  accounts: MigrationAccountProgress[];
  log: LogEntry[];              // the last 100 entries, newest first
}

export interface ImportResult { imported: number; skipped: number; failed: number; errors: string[]; }

export interface MigrationTestRequest { source: MigrationSource; username: string; password: string; }

export interface MigrationDiagStep { step: string; ok: boolean; detail: string; }
export interface MigrationTestResult {
  ok: boolean; folders: SourceFolder[]; error?: string;
  /** Step-by-step connection diagnosis (only when ok is false). */
  diagnostics?: MigrationDiagStep[];
  /** Concrete advice derived from the diagnosis. */
  hint?: string;
}

export interface CreateMigrationRequest {
  name?: string; source: MigrationSource; accounts: MigrationAccountInput[]; options: MigrationOptions;
}

export interface RestartMigrationRequest { accounts: { targetEmail: string; password: string }[]; }

// ---------------------------------------------------------------------------
// DNS providers (docs/API.md, "DNS-Anbieter (automatische Einrichtung der DNS-Einträge)").
// ---------------------------------------------------------------------------

export interface DnsProviderField {
  key: string; label: string; type: "text" | "password";
  required: boolean; help: string | null; placeholder: string | null;
}

export interface DnsProviderInfo {
  id: string;                 // "ionos" | "strato" | "hetzner" | "cloudflare" | "netcup" | "inwx" | "desec"
  name: string;               // display name
  official: boolean;          // false = unofficial integration (e.g. Strato via the customer login)
  description: string;        // one sentence
  docsUrl: string | null;     // where to get the API key
  fields: DnsProviderField[];
  supports: string[];         // supported record types, e.g. ["A","AAAA","MX","TXT","CNAME","SRV"]
  notes: string | null;       // notes / limitations
}

export interface DnsPlanRequest {
  provider: string;
  credentials: Record<string, string>;
  ipv4?: string | null;       // public IPv4 for the A record of the host name (otherwise "manual")
  ipv6?: string | null;       // optional AAAA
}

export type DnsChangeAction = "create" | "update" | "unchanged" | "manual" | "delete";

export interface DnsChange {
  key: string;                // stable key, e.g. "mx", "spf", "dkim", "dmarc", "a", "aaaa", "mta-sts-txt" …
  type: string;
  name: string;               // FQDN with trailing dot
  action: DnsChangeAction;
  current: string[];          // existing values (only the matched ones)
  desired: string;            // target value (for MX "10 mail.example.com.")
  purpose: string;
  required: boolean;
  warning: string | null;     // e.g. "Bestehende MX-Einträge werden ersetzt"
  reason: string | null;      // reason for "manual"
}

export interface DnsPlan { zone: string; provider: string; changes: DnsChange[]; }

export interface DnsApplyRequest extends DnsPlanRequest { keys: string[]; }

export interface DnsApplyResult {
  applied: string[];                         // keys
  failed: { key: string; error: string }[];
  manual: string[];                          // keys that have to be set by hand
}

export interface PublicIp { ipv4: string | null; ipv6: string | null; }

// ---------------------------------------------------------------------------
// Reverse proxy and certificate source (docs/API.md, "Reverse-Proxy und Zertifikatsquelle").
// ---------------------------------------------------------------------------

export type TlsMode = "selfsigned" | "acme" | "files";

export type ProxyKind = "npm" | "traefik" | "caddy" | "nginx" | "swag" | "other";

export interface ProxyDetect {
  viaProxy: boolean;              // request carried X-Forwarded-* headers
  remoteIp: string;               // direct peer (= the proxy, if viaProxy)
  clientIp: string;               // how Postwerk sees the browser after applying the settings
  trusted: boolean;               // remoteIp currently counts as a trusted proxy
  forwardedFor: string | null;
  forwardedProto: string | null;
  forwardedHost: string | null;
  requestHost: string;
  suggestedTrustedProxy: string | null;   // suggestion for trustedProxies
  suggestedPublicUrl: string | null;      // derived from X-Forwarded-Proto/-Host
}

export interface ProxyConfigFile { name: string; language: "nginx" | "yaml" | "caddy" | "text"; content: string; }

export interface ProxyGuide {
  kind: ProxyKind;
  title: string;                  // e.g. "Nginx Proxy Manager"
  steps: string[];                // numbered instructions (plain text, may contain `code` in backticks)
  files: ProxyConfigFile[];       // configurations to copy
  certificate: {                  // how Postwerk gets the certificate
    recommendedMode: "files" | "acme";
    hostPathHint: string | null;  // where the proxy stores its certificates (host/appdata)
    containerPathExample: string | null; // matching certPath in Postwerk, e.g. "/certs/live/npm-1/fullchain.pem"
    mountHint: string;            // how the directory is mounted to /certs
  };
  notes: string[];                // warnings / notes
}

export interface CertificateFile {
  certPath: string;               // inside the container
  keyPath: string | null;         // matching key found (null for acme.json, the key is inside)
  source: "files" | "traefik";
  names: string[];                // SANs
  issuer: string;
  notAfter: string;
  matchesHostname: boolean;       // covers the mail server host name
  error: string | null;           // found but unusable, e.g. private key not readable (permissions)
}

export interface TlsTestRequest { certPath: string; keyPath: string; }

export interface TlsTestResult { ok: boolean; names: string[]; notAfter: string; matchesHostname: boolean; error?: string; }

// ---------------------------------------------------------------------------
// Dynamic IP: DynDNS and smarthost (docs/API.md, "Dynamische IP: DynDNS und Smarthost").
// ---------------------------------------------------------------------------

export type DynDNSPresetId = "strato" | "inwx" | "desec" | "noip" | "custom";
export type RelayPresetId = "strato" | "ionos" | "brevo" | "mailjet" | "smtp2go" | "ses" | "custom";
export type RelaySecurity = "tls" | "starttls";

export interface DynDNSPreset { id: string; name: string; updateUrl: string; usernameHint: string; passwordHint: string; docsUrl: string | null; notes: string | null; }
export interface RelayPreset  { id: string; name: string; host: string; port: number; security: RelaySecurity; usernameHint: string; spfInclude: string; docsUrl: string | null; notes: string | null; }

export interface DynDNSStatus {
  enabled: boolean;
  currentIp: string | null;       // last detected public IP
  dnsIps: Record<string, string[]>;   // current A records per host name (live lookup)
  lastUpdate: string | null;      // time of the last successful update
  lastResult: string | null;      // service response, e.g. "good 203.0.113.7" / "nochg …"
  error: string | null;           // e.g. "badauth – Benutzername oder DynDNS-Passwort falsch"
  pausedUntil: string | null;     // paused after "abuse"/"badauth"
  nextCheck: string | null;
}

export interface RelayPresets { presets: RelayPreset[]; envOverride: boolean; }

export interface RelayTestResult { ok: boolean; detail: string; }

// ---------------------------------------------------------------------------
// Sieve filter rules (docs/API.md, "Filterregeln (Sieve)").
// ---------------------------------------------------------------------------

export interface SieveScriptInfo { name: string; active: boolean; size: number; updatedAt: string; }
export interface SieveScript extends SieveScriptInfo { content: string; }
export interface SieveList { scripts: SieveScriptInfo[]; active: string | null; capabilities: string[]; maxSize: number; maxScripts: number; }

export interface SieveVacation { days: number; subject?: string; from?: string; addresses?: string[]; handle?: string; reason: string; }
export interface SieveAction {
  type: "keep" | "fileinto" | "redirect" | "discard" | "reject" | "ereject" | "vacation";
  mailbox?: string; create?: boolean; copy?: boolean; flags?: string[]; address?: string; reason?: string;
  vacation?: SieveVacation; implicit?: boolean;
}
export interface SieveTestRequest { content?: string; messageId?: number; raw?: string; }
export interface SieveTestResult { actions: SieveAction[]; implicitKeep: boolean; script: string; error?: string; }
