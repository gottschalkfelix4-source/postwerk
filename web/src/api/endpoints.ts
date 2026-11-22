import { ApiError, buildUrl, http } from "./client";
import type {
  Account,
  AiStatus,
  AiTestResult,
  ChatGptLogin,
  ChatGptStatus,
  Alias,
  AccountSession,
  AppPassword,
  BackupArchive,
  BackupRun,
  BackupStatus,
  BackupTarget,
  BackupTestResult,
  Ban,
  BulkRequest,
  AddressBook,
  AddressSuggestion,
  Calendar,
  CalendarEvent,
  CalendarInput,
  Contact,
  ContactInput,
  DavImportResult,
  DavInfo,
  EventInput,
  CertificateFile,
  ClamavTestResult,
  CreateAccountRequest,
  CreateMigrationRequest,
  CreatedAppPassword,
  CreateTaskRequest,
  Dashboard,
  DnsApplyRequest,
  DnsApplyResult,
  DnsCheck,
  DnsPlan,
  DnsPlanRequest,
  DnsProviderInfo,
  DnsRecord,
  Domain,
  DraftRequest,
  DynDNSPreset,
  DynDNSStatus,
  ExtractResult,
  ImportResult,
  LogEntry,
  LoginRequest,
  Mailbox,
  Me,
  MessageDetail,
  MessageQuery,
  MessageSummary,
  MigrationJob,
  MigrationTestRequest,
  MigrationTestResult,
  Ok,
  Paged,
  ProxyDetect,
  ProxyGuide,
  ProxyKind,
  PublicIp,
  QueueItem,
  RelayPresets,
  RelayTestResult,
  ReplyDraft,
  ReplyRequest,
  RestartMigrationRequest,
  ScoredMessage,
  SendRequest,
  Settings,
  SettingsUpdate,
  SetupInitializeRequest,
  SetupStatus,
  SummarizeRequest,
  SystemCheck,
  SummarizeResult,
  TagCount,
  Task,
  TlsTestRequest,
  TlsTestResult,
  TotpSetup,
  TotpEnableResult,
  RecoveryCodes,
  RecoveryCodesStatus,
  UpdateAccountRequest,
  UpdateMessageRequest,
  UpdateTaskRequest,
  Vacation, TlsIssueResult } from "./types";

import type { SieveList, SieveScript, SieveTestRequest, SieveTestResult } from "./types";

const enc = encodeURIComponent;

export const authApi = {
  login: (body: LoginRequest) => http.post<Me>("/api/auth/login", body, { skipAuthRedirect: true }),
  /** endSessionUrl: also sign out at the identity provider (after SSO login). */
  logout: () => http.post<Ok & { endSessionUrl?: string }>("/api/auth/logout", undefined, { skipAuthRedirect: true }),
  me: (skipAuthRedirect = false) => http.get<Me>("/api/auth/me", { skipAuthRedirect }),
};

export const accountApi = {
  changePassword: (current: string, next: string, revokeAppPasswords = false) =>
    http.post<Ok>("/api/account/password", { current, new: next, revokeAppPasswords }),
  totpSetup: () => http.post<TotpSetup>("/api/account/totp/setup"),
  totpEnable: (code: string, password: string) =>
    http.post<TotpEnableResult>("/api/account/totp/enable", { code, password }),
  totpDisable: (password: string, code: string) => http.post<Ok>("/api/account/totp/disable", { password, code }),
  recoveryCodes: () => http.get<RecoveryCodesStatus>("/api/account/recovery-codes"),
  regenerateRecoveryCodes: (password: string, code: string) =>
    http.post<RecoveryCodes>("/api/account/recovery-codes", { password, code }),
  appPasswords: () => http.get<AppPassword[]>("/api/account/app-passwords"),
  createAppPassword: (name: string, password: string, code = "") =>
    http.post<CreatedAppPassword>("/api/account/app-passwords", { name, password, code }),
  deleteAppPassword: (id: number) => http.delete<Ok>(`/api/account/app-passwords/${id}`),
  sessions: () => http.get<AccountSession[]>("/api/account/sessions"),
  deleteSession: (id: string) => http.delete<Ok>(`/api/account/sessions/${enc(id)}`),
  logoutOtherSessions: () => http.post<Ok & { ended: number }>("/api/account/sessions/logout-others"),
  vacation: () => http.get<Vacation>("/api/account/vacation"),
  saveVacation: (v: Vacation) => http.put<Vacation>("/api/account/vacation", v),
  dav: () => http.get<DavInfo>("/api/account/dav"),
  appleProfileUrl: buildUrl("/api/account/apple-profile"),
};

export const adminApi = {
  dashboard: () => http.get<Dashboard>("/api/admin/dashboard"),

  domains: () => http.get<Domain[]>("/api/admin/domains"),
  createDomain: (name: string) => http.post<Domain>("/api/admin/domains", { name }),
  updateDomain: (name: string, body: { catchAll?: string | null }) =>
    http.patch<Domain>(`/api/admin/domains/${enc(name)}`, body),
  deleteDomain: (name: string) => http.delete<Ok>(`/api/admin/domains/${enc(name)}`),
  domainDns: (name: string) => http.get<DnsRecord[]>(`/api/admin/domains/${enc(name)}/dns`),
  domainDnsCheck: (name: string) => http.get<DnsCheck[]>(`/api/admin/domains/${enc(name)}/dns/check`),

  dnsProviders: () => http.get<DnsProviderInfo[]>("/api/admin/dns/providers"),
  domainDnsPlan: (name: string, body: DnsPlanRequest) =>
    http.post<DnsPlan>(`/api/admin/domains/${enc(name)}/dns/plan`, body),
  domainDnsApply: (name: string, body: DnsApplyRequest) =>
    http.post<DnsApplyResult>(`/api/admin/domains/${enc(name)}/dns/apply`, body),

  accounts: (params: { q?: string; limit?: number; offset?: number }) =>
    http.get<Paged<Account>>("/api/admin/accounts", { query: params }),
  account: (id: number) => http.get<Account>(`/api/admin/accounts/${id}`),
  createAccount: (body: CreateAccountRequest) => http.post<Account>("/api/admin/accounts", body),
  updateAccount: (id: number, body: UpdateAccountRequest) =>
    http.patch<Account>(`/api/admin/accounts/${id}`, body),
  deleteAccount: (id: number) => http.delete<Ok>(`/api/admin/accounts/${id}`),

  aliases: () => http.get<Alias[]>("/api/admin/aliases"),
  createAlias: (address: string, targets: string[]) =>
    http.post<Alias>("/api/admin/aliases", { address, targets }),
  updateAlias: (id: number, targets: string[]) =>
    http.patch<Alias>(`/api/admin/aliases/${id}`, { targets }),
  deleteAlias: (id: number) => http.delete<Ok>(`/api/admin/aliases/${id}`),

  queue: () => http.get<QueueItem[]>("/api/admin/queue"),
  retryQueueItem: (id: number) => http.post<Ok>(`/api/admin/queue/${id}/retry`),
  deleteQueueItem: (id: number) => http.delete<Ok>(`/api/admin/queue/${id}`),

  logs: (params: { level?: string; q?: string; limit?: number }) =>
    http.get<LogEntry[]>("/api/admin/logs", { query: params }),

  bans: () => http.get<Ban[]>("/api/admin/security/bans"),
  unban: (ip: string) => http.delete<Ok>(`/api/admin/security/bans/${enc(ip)}`),

  settings: () => http.get<Settings>("/api/admin/settings"),
  saveSettings: (body: SettingsUpdate) => http.put<Settings>("/api/admin/settings", body),
  testAi: () => http.post<AiTestResult>("/api/admin/settings/ai/test"),
  chatgptStatus: () => http.get<ChatGptStatus>("/api/admin/ai/chatgpt"),
  chatgptLogin: () => http.post<ChatGptLogin>("/api/admin/ai/chatgpt/login"),
  chatgptLoginStatus: (id: string) => http.get<ChatGptLogin>(`/api/admin/ai/chatgpt/login/${enc(id)}`),
  chatgptLogout: () => http.post<Ok>("/api/admin/ai/chatgpt/logout"),

  completeSetup: () => http.post<Ok>("/api/admin/setup/complete"),
  systemCheck: () => http.get<SystemCheck>("/api/admin/system/check"),
  testClamav: (address: string) => http.post<ClamavTestResult>("/api/admin/system/test-clamav", { address }),
  /** Queries api.ipify.org on the server: only call on explicit user action. */
  publicIp: () => http.get<PublicIp>("/api/admin/system/public-ip"),

  proxyDetect: () => http.get<ProxyDetect>("/api/admin/proxy/detect"),
  /** `upstream` = address under which the proxy reaches Postwerk, e.g. http://192.168.1.10:8880. */
  proxyGuide: (kind: ProxyKind, upstream: string, signal?: AbortSignal) =>
    http.get<ProxyGuide>("/api/admin/proxy/guide", { query: { kind, upstream }, signal }),
  tlsCertificates: () => http.get<CertificateFile[]>("/api/admin/tls/certificates"),
  testTlsFiles: (body: TlsTestRequest) => http.post<TlsTestResult>("/api/admin/tls/test", body),
  issueTls: () => http.post<TlsIssueResult>("/api/admin/tls/issue", {}),

  dyndnsPresets: () => http.get<DynDNSPreset[]>("/api/admin/dyndns/presets"),
  dyndnsStatus: () => http.get<DynDNSStatus>("/api/admin/dyndns/status"),
  /** Checks the public IP now and reports it only if it differs; `force` always reports. */
  dyndnsUpdate: (force = false) => http.post<DynDNSStatus>("/api/admin/dyndns/update", force ? { force: true } : undefined),
  relayPresets: () => http.get<RelayPresets>("/api/admin/relay/presets"),
  /** Tests the stored smarthost settings (connection, TLS, login). */
  testRelay: () => http.post<RelayTestResult>("/api/admin/relay/test"),

  backupStatus: () => http.get<{ status: BackupStatus; runs: BackupRun[] }>("/api/admin/backups/status"),
  backupArchives: () => http.get<{ archives: BackupArchive[]; error?: string }>("/api/admin/backups"),
  startBackup: () => http.post<BackupStatus>("/api/admin/backups"),
  deleteBackup: (name: string) => http.delete<Ok>(`/api/admin/backups/${encodeURIComponent(name)}`),
  restoreBackup: (name: string, password: string, accountPassword: string, code: string) =>
    http.post<BackupStatus>(`/api/admin/backups/${encodeURIComponent(name)}/restore`, { password, accountPassword, code }),
  backupDownloadToken: (name: string, password: string, code: string) =>
    http.post<{ token: string }>(`/api/admin/backups/${encodeURIComponent(name)}/download-token`, { password, code }),
  testBackupTarget: (target: BackupTarget) => http.post<BackupTestResult>("/api/admin/backups/test", target),
  uploadBackup: (file: File, opts: Omit<UploadOptions, "mailboxId"> = {}) =>
    uploadMultipart<{ name: string }>("/api/admin/backups/upload", file, {}, opts),
  backupDownloadUrl: (name: string, token: string) =>
    `/api/admin/backups/${encodeURIComponent(name)}/download?token=${encodeURIComponent(token)}`,
  migrations: () => http.get<MigrationJob[]>("/api/admin/migrations"),
  migration: (id: number) => http.get<MigrationJob>(`/api/admin/migrations/${id}`),
  testMigration: (body: MigrationTestRequest) => http.post<MigrationTestResult>("/api/admin/migrations/test", body),
  createMigration: (body: CreateMigrationRequest) => http.post<MigrationJob>("/api/admin/migrations", body),
  cancelMigration: (id: number) => http.post<Ok>(`/api/admin/migrations/${id}/cancel`),
  restartMigration: (id: number, body: RestartMigrationRequest) =>
    http.post<MigrationJob>(`/api/admin/migrations/${id}/restart`, body),
  deleteMigration: (id: number) => http.delete<Ok>(`/api/admin/migrations/${id}`),
};

export const setupApi = {
  status: () => http.get<SetupStatus>("/api/setup/status", { skipAuthRedirect: true }),
  initialize: (body: SetupInitializeRequest) =>
    http.post<Me>("/api/setup/initialize", body, { skipAuthRedirect: true }),
};

export const mailApi = {
  mailboxes: () => http.get<Mailbox[]>("/api/mail/mailboxes"),
  createMailbox: (name: string) => http.post<Mailbox>("/api/mail/mailboxes", { name }),
  renameMailbox: (id: number, name: string) => http.patch<Mailbox>(`/api/mail/mailboxes/${id}`, { name }),
  deleteMailbox: (id: number) => http.delete<Ok>(`/api/mail/mailboxes/${id}`),

  messages: (q: MessageQuery, signal?: AbortSignal) =>
    http.get<Paged<MessageSummary>>("/api/mail/messages", {
      signal,
      query: {
        mailbox: q.mailbox,
        q: q.q,
        tag: q.tag,
        category: q.category,
        unread: q.unread ? "true" : undefined,
        limit: q.limit,
        offset: q.offset,
      },
    }),
  message: (id: number) => http.get<MessageDetail>(`/api/mail/messages/${id}`),
  updateMessage: (id: number, body: UpdateMessageRequest) =>
    http.patch<MessageSummary>(`/api/mail/messages/${id}`, body),
  deleteMessage: (id: number) => http.delete<Ok>(`/api/mail/messages/${id}`),
  bulk: (body: BulkRequest) => http.post<{ ok: boolean; count: number }>("/api/mail/messages/bulk", body),
  thread: (threadId: number) => http.get<MessageDetail[]>(`/api/mail/threads/${threadId}`),
  send: (body: SendRequest) => http.post<{ ok: boolean; id: number }>("/api/mail/send", body),
  saveDraft: (body: DraftRequest) => http.post<{ id: number }>("/api/mail/drafts", body),
  tags: () => http.get<TagCount[]>("/api/mail/tags"),

  importFile: uploadMailImport,

  rawUrl: (id: number) => `/api/mail/messages/${id}/raw`,
  attachmentUrl: (id: number, attId: number) => `/api/mail/messages/${id}/attachments/${attId}`,
};

export const sieveApi = {
  list: () => http.get<SieveList>("/api/mail/sieve"),
  get: (name: string) => http.get<SieveScript>(`/api/mail/sieve/${enc(name)}`),
  put: (name: string, content: string, activate = false) =>
    http.put<SieveScript>(`/api/mail/sieve/${enc(name)}`, { content, activate }),
  remove: (name: string) => http.delete<Ok>(`/api/mail/sieve/${enc(name)}`),
  activate: (name: string) => http.post<Ok>(`/api/mail/sieve/${enc(name)}/activate`),
  deactivate: () => http.post<Ok>("/api/mail/sieve/deactivate"),
  check: (content: string) => http.post<{ ok: boolean }>("/api/mail/sieve/check", { content }),
  test: (body: SieveTestRequest) => http.post<SieveTestResult>("/api/mail/sieve/test", body),
};

export const aiApi = {
  status: () => http.get<AiStatus>("/api/ai/status"),
  summarize: (body: SummarizeRequest) => http.post<SummarizeResult>("/api/ai/summarize", body),
  reply: (body: ReplyRequest) => http.post<ReplyDraft>("/api/ai/reply", body),
  search: (q: string, limit = 50, signal?: AbortSignal) =>
    http.get<{ items: ScoredMessage[] }>("/api/ai/search", { query: { q, limit }, signal }),
  extract: (messageId: number) => http.post<ExtractResult>("/api/ai/extract", { messageId }),
  reclassify: (messageId: number) => http.post<MessageSummary>("/api/ai/reclassify", { messageId }),
};

export const calendarApi = {
  events: (from: string, to: string, calendar?: number) =>
    http.get<CalendarEvent[]>("/api/calendar/events", { query: { from, to, calendar } }),
  createEvent: (body: EventInput) => http.post<CalendarEvent>("/api/calendar/events", body),
  updateEvent: (id: number, body: EventInput) => http.put<CalendarEvent>(`/api/calendar/events/${id}`, body),
  deleteEvent: (id: number) => http.delete<Ok>(`/api/calendar/events/${id}`),

  calendars: () => http.get<Calendar[]>("/api/calendars"),
  createCalendar: (body: CalendarInput & { name: string }) => http.post<Calendar>("/api/calendars", body),
  updateCalendar: (id: number, body: CalendarInput) => http.patch<Calendar>(`/api/calendars/${id}`, body),
  deleteCalendar: (id: number) => http.delete<Ok>(`/api/calendars/${id}`),
  importIcs: (id: number, data: string) => http.post<DavImportResult>(`/api/calendars/${id}/import`, { data }),
  exportUrl: (id: number) => buildUrl(`/api/calendars/${id}/export`),
};

export const addressBooksApi = {
  list: () => http.get<AddressBook[]>("/api/addressbooks"),
  create: (name: string) => http.post<AddressBook>("/api/addressbooks", { name }),
  update: (id: number, name: string) => http.patch<AddressBook>(`/api/addressbooks/${id}`, { name }),
  remove: (id: number) => http.delete<Ok>(`/api/addressbooks/${id}`),
};

export const contactsApi = {
  list: (q?: string, addressBook?: number, signal?: AbortSignal) =>
    http.get<Contact[]>("/api/contacts", { query: { q, addressBook }, signal }),
  get: (id: number) => http.get<Contact>(`/api/contacts/${id}`),
  create: (body: ContactInput) => http.post<Contact>("/api/contacts", body),
  update: (id: number, body: ContactInput) => http.put<Contact>(`/api/contacts/${id}`, body),
  remove: (id: number) => http.delete<Ok>(`/api/contacts/${id}`),
  importVcf: (data: string, addressBookId?: number) =>
    http.post<DavImportResult>("/api/contacts/import", { data, ...(addressBookId ? { addressBookId } : {}) }),
  exportUrl: (addressBook?: number) => buildUrl("/api/contacts/export", { addressBook }),
  suggest: (q: string, limit = 10, signal?: AbortSignal) =>
    http.get<AddressSuggestion[]>("/api/contacts/suggest", { query: { q, limit }, signal }),
};

export const tasksApi = {
  list: () => http.get<Task[]>("/api/tasks"),
  create: (body: CreateTaskRequest) => http.post<Task>("/api/tasks", body),
  update: (id: number, body: UpdateTaskRequest) => http.patch<Task>(`/api/tasks/${id}`, body),
  remove: (id: number) => http.delete<Ok>(`/api/tasks/${id}`),
};

export const dmsApi = {
  forward: (messageId: number, attachmentId: number) =>
    http.post<Ok>("/api/dms/forward", { messageId, attachmentId }),
};

export const metricsUrl = buildUrl("/metrics");

export interface UploadOptions {
  /** Target mailbox; omitted means the inbox. */
  mailboxId?: number;
  /** Called with the fraction (0..1) of bytes sent. */
  onProgress?: (fraction: number) => void;
  signal?: AbortSignal;
}

/**
 * POST /api/mail/import as multipart/form-data. Uses XMLHttpRequest because
 * fetch does not report upload progress. The browser sets the multipart
 * Content-Type (with boundary) itself.
 */
function uploadMailImport(file: File, opts: UploadOptions = {}): Promise<ImportResult> {
  const fields: Record<string, string> = {};
  if (opts.mailboxId !== undefined) fields.mailboxId = String(opts.mailboxId);
  return uploadMultipart<ImportResult>("/api/mail/import", file, fields, opts);
}

function uploadMultipart<T>(url: string, file: File, fields: Record<string, string>, opts: UploadOptions = {}): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const form = new FormData();
    for (const [k, v] of Object.entries(fields)) form.append(k, v);
    form.append("file", file, file.name);

    const xhr = new XMLHttpRequest();
    xhr.open("POST", url);
    xhr.withCredentials = true;
    xhr.setRequestHeader("X-Postwerk", "1");
    xhr.setRequestHeader("Accept", "application/json");

    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable && opts.onProgress) opts.onProgress(e.total > 0 ? e.loaded / e.total : 0);
    };
    xhr.onload = () => {
      let data: unknown = null;
      try {
        data = xhr.responseText ? JSON.parse(xhr.responseText) : null;
      } catch {
        data = null;
      }
      if (xhr.status >= 200 && xhr.status < 300) {
        resolve(data as T);
        return;
      }
      const body = (data ?? {}) as { error?: unknown; message?: unknown };
      const code = typeof body.error === "string" ? body.error : xhr.status === 413 ? "too_large" : "error";
      const message =
        typeof body.message === "string" && body.message
          ? body.message
          : xhr.status === 413
            ? "Die Datei ist zu groß."
            : xhr.status === 401
              ? "Bitte melde dich an."
              : `Hochladen fehlgeschlagen (HTTP ${xhr.status}).`;
      reject(new ApiError(xhr.status, code, message));
    };
    xhr.onerror = () => reject(new ApiError(0, "network", "Der Server ist nicht erreichbar."));
    xhr.onabort = () => reject(new DOMException("Upload abgebrochen", "AbortError"));

    if (opts.signal) {
      if (opts.signal.aborted) {
        reject(new DOMException("Upload abgebrochen", "AbortError"));
        return;
      }
      opts.signal.addEventListener("abort", () => xhr.abort(), { once: true });
    }
    xhr.send(form);
  });
}
