import { buildUrl, http } from "./client";
import type {
  Account,
  AiStatus,
  AiTestResult,
  Alias,
  AppPassword,
  Ban,
  BulkRequest,
  CalendarEvent,
  CreateAccountRequest,
  CreatedAppPassword,
  CreateTaskRequest,
  Dashboard,
  DnsRecord,
  Domain,
  DraftRequest,
  ExtractResult,
  LogEntry,
  LoginRequest,
  Mailbox,
  Me,
  MessageDetail,
  MessageQuery,
  MessageSummary,
  Ok,
  Paged,
  QueueItem,
  ReplyDraft,
  ReplyRequest,
  ScoredMessage,
  SendRequest,
  Settings,
  SettingsUpdate,
  SummarizeRequest,
  SummarizeResult,
  TagCount,
  Task,
  TotpSetup,
  UpdateAccountRequest,
  UpdateMessageRequest,
  UpdateTaskRequest,
  Vacation,
} from "./types";

const enc = encodeURIComponent;

export const authApi = {
  login: (body: LoginRequest) => http.post<Me>("/api/auth/login", body, { skipAuthRedirect: true }),
  logout: () => http.post<Ok>("/api/auth/logout", undefined, { skipAuthRedirect: true }),
  me: (skipAuthRedirect = false) => http.get<Me>("/api/auth/me", { skipAuthRedirect }),
};

export const accountApi = {
  changePassword: (current: string, next: string) =>
    http.post<Ok>("/api/account/password", { current, new: next }),
  totpSetup: () => http.post<TotpSetup>("/api/account/totp/setup"),
  totpEnable: (code: string) => http.post<Ok>("/api/account/totp/enable", { code }),
  totpDisable: (password: string) => http.post<Ok>("/api/account/totp/disable", { password }),
  appPasswords: () => http.get<AppPassword[]>("/api/account/app-passwords"),
  createAppPassword: (name: string) =>
    http.post<CreatedAppPassword>("/api/account/app-passwords", { name }),
  deleteAppPassword: (id: number) => http.delete<Ok>(`/api/account/app-passwords/${id}`),
  vacation: () => http.get<Vacation>("/api/account/vacation"),
  saveVacation: (v: Vacation) => http.put<Vacation>("/api/account/vacation", v),
};

export const adminApi = {
  dashboard: () => http.get<Dashboard>("/api/admin/dashboard"),

  domains: () => http.get<Domain[]>("/api/admin/domains"),
  createDomain: (name: string) => http.post<Domain>("/api/admin/domains", { name }),
  updateDomain: (name: string, body: { catchAll?: string | null }) =>
    http.patch<Domain>(`/api/admin/domains/${enc(name)}`, body),
  deleteDomain: (name: string) => http.delete<Ok>(`/api/admin/domains/${enc(name)}`),
  domainDns: (name: string) => http.get<DnsRecord[]>(`/api/admin/domains/${enc(name)}/dns`),

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

  rawUrl: (id: number) => `/api/mail/messages/${id}/raw`,
  attachmentUrl: (id: number, attId: number) => `/api/mail/messages/${id}/attachments/${attId}`,
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
  events: (from: string, to: string) => http.get<CalendarEvent[]>("/api/calendar/events", { query: { from, to } }),
  createEvent: (body: Omit<CalendarEvent, "id">) => http.post<CalendarEvent>("/api/calendar/events", body),
  deleteEvent: (id: number) => http.delete<Ok>(`/api/calendar/events/${id}`),
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
