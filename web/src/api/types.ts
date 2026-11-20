// Types of the Postwerk HTTP API (v1), taken verbatim from docs/API.md.
// The section at the bottom contains request/response shapes that the
// contract only describes inline in its endpoint tables.

export type Role = "admin" | "user";

export interface Me {
  id: number; email: string; name: string; role: Role;
  totpEnabled: boolean; quotaBytes: number; usedBytes: number;
}

export interface Domain {
  name: string;
  catchAll: string | null;      // target address for catch-all or null
  dkimSelector: string;
  accountCount: number;
  aliasCount: number;
  createdAt: string;
}

export interface DnsRecord { type: "MX" | "TXT" | "CNAME" | "A" | "AAAA" | "TLSA" | "SRV"; name: string; value: string; purpose: string; }

export interface Account {
  id: number; email: string; name: string; role: Role;
  quotaBytes: number;          // 0 = unlimited
  usedBytes: number; disabled: boolean; totpEnabled: boolean;
  aliases: string[]; createdAt: string;
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
}

export interface Settings {
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
    enabled: boolean; baseUrl: string; apiKey: string /* masked "••••" when set */;
    chatModel: string; embeddingModel: string; timeoutSeconds: number;
    features: { spamCheck: boolean; classify: boolean; priority: boolean; summarize: boolean;
                replyDraft: boolean; semanticSearch: boolean; extractEvents: boolean };
    categories: string[];          // e.g. ["Rechnung","Newsletter","Support","Privat"]
  };
  dms: { paperless: { enabled: boolean; url: string; token: string /* masked */; autoForward: boolean; categories: string[] } };
  security: { bruteForce: { maxAttempts: number; windowSeconds: number; banSeconds: number } };
}

export interface Mailbox { id: number; name: string; role: "inbox" | "sent" | "drafts" | "trash" | "junk" | "archive" | null; total: number; unread: number; }

export interface Address { name: string; email: string; }

export interface MessageSummary {
  id: number; mailboxId: number; threadId: number;
  from: Address; to: Address[]; subject: string; preview: string;
  receivedAt: string; size: number; seen: boolean; flagged: boolean; hasAttachments: boolean;
  tags: string[]; category: string | null; priority: "high" | "normal" | "low";
  spamScore: number; summary: string | null;  // short AI summary, if available
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

export interface CalendarEvent { id: number; title: string; start: string; end: string | null; location: string | null; description: string | null; sourceMessageId: number | null; }
export interface Task { id: number; title: string; due: string | null; done: boolean; sourceMessageId: number | null; createdAt: string; }

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

export interface CreateAccountRequest { email: string; name: string; password: string; role: Role; quotaBytes: number; }

export interface UpdateAccountRequest {
  name?: string; password?: string; role?: Role; quotaBytes?: number; disabled?: boolean; resetTotp?: boolean;
}

export interface AiTestResult { ok: boolean; latencyMs: number; models: string[]; error?: string; }

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

export interface SummarizeResult { summary: string; bullets: string[]; }

export type ReplyTone = "formal" | "friendly" | "short";

export interface ReplyRequest { messageId: number; instruction?: string; tone?: ReplyTone; }

export interface ReplyDraft { subject: string; body: string; }

export type ScoredMessage = MessageSummary & { score: number };

export interface ExtractResult { events: ExtractedEvent[]; tasks: ExtractedTask[]; }

export interface CreateTaskRequest { title: string; due?: string | null; sourceMessageId?: number | null; }

export interface UpdateTaskRequest { title?: string; due?: string | null; done?: boolean; }

// ---------------------------------------------------------------------------
// Setup wizard (docs/API.md, "Einrichtung (Setup-Assistent)").
// ---------------------------------------------------------------------------

export interface SetupStatus { mode: "initial" | "configure" | "done"; hostname: string; version: string; }

export type CheckStatus = "ok" | "missing" | "mismatch" | "warning" | "error" | "info";

export interface DnsCheck {
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

export interface MigrationTestResult { ok: boolean; folders: SourceFolder[]; error?: string; }

export interface CreateMigrationRequest {
  name?: string; source: MigrationSource; accounts: MigrationAccountInput[]; options: MigrationOptions;
}

export interface RestartMigrationRequest { accounts: { targetEmail: string; password: string }[]; }
