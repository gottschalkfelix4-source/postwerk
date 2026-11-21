import { http } from "./client";

/** Types and calls for encryption at rest and GDPR data exports. */

export type EncryptionKind = "pgp" | "smime";

export interface KeyInfo {
  kind: EncryptionKind;
  fingerprint: string;
  description: string;
  emails: string[];
  algorithm: string;
  expires: string | null;
}

export interface MailEncryptionConfig {
  kind: EncryptionKind;
  publicKey: string;
  fingerprint: string;
  description: string;
  algorithm: string;
  expires: string | null;
  hideSubject: boolean;
  createdAt: string;
  updatedAt: string;
}

export interface ConversionJob {
  running: boolean;
  encrypt: boolean;
  done: number;
  total: number;
  converted: number;
  skipped: number;
  failed: number;
  error?: string;
  startedAt: string;
  finishedAt: string | null;
}

export interface MailEncryptionState {
  config: MailEncryptionConfig | null;
  job: ConversionJob | null;
  messages: number;
}

export interface DataExport {
  id: number;
  accountId: number;
  requestedBy: number;
  status: "pending" | "running" | "done" | "failed" | "expired";
  phase: string;
  done: number;
  total: number;
  size: number;
  error?: string;
  createdAt: string;
  finishedAt: string | null;
  expiresAt: string | null;
}

export type MasterKeySource = "env" | "env-file" | "data-file";

export interface EncryptionStatus {
  masterKey: { source: MasterKeySource; file?: string; inDataDir: boolean; fingerprint: string };
  blobs: { enabled: boolean; stats?: { total: number; encrypted: number; bytes: number }; job: ConversionJob | null };
  mailEncryptionAccounts: number;
  protectedSecrets: string[];
}

export const atRestApi = {
  mailEncryption: () => http.get<MailEncryptionState>("/api/account/encryption"),
  inspectKey: (kind: EncryptionKind, publicKey: string) =>
    http.post<{ key: KeyInfo; matchesAccount: boolean }>("/api/account/encryption/inspect", { kind, publicKey }),
  saveMailEncryption: (body: { kind?: EncryptionKind; publicKey?: string; hideSubject: boolean }) =>
    http.put<MailEncryptionState>("/api/account/encryption", body),
  disableMailEncryption: () => http.delete<MailEncryptionState>("/api/account/encryption"),
  encryptExisting: () => http.post<MailEncryptionState>("/api/account/encryption/encrypt-existing"),

  exports: () => http.get<DataExport[]>("/api/account/exports"),
  startExport: () => http.post<DataExport>("/api/account/exports"),
  exportDownloadUrl: (id: number) => `/api/account/exports/${id}/download`,

  accountExports: (accountId: number) => http.get<DataExport[]>(`/api/admin/accounts/${accountId}/exports`),
  startAccountExport: (accountId: number) => http.post<DataExport>(`/api/admin/accounts/${accountId}/exports`),
  adminExportDownloadUrl: (id: number) => `/api/admin/exports/${id}/download`,

  encryptionStatus: (stats = false) => http.get<EncryptionStatus>("/api/admin/security/encryption", { query: { stats: stats ? 1 : undefined } }),
  setBlobEncryption: (enabled: boolean, migrate: boolean) =>
    http.put<EncryptionStatus>("/api/admin/security/encryption/blobs", { enabled, migrate }),
  migrateBlobs: (encrypt: boolean) => http.post<EncryptionStatus>("/api/admin/security/encryption/blobs/migrate", { encrypt }),
};

export const atRestKeys = {
  mailEncryption: ["account", "encryption"] as const,
  exports: ["account", "exports"] as const,
  accountExports: (id: number) => ["admin", "accounts", id, "exports"] as const,
  encryptionStatus: ["admin", "security", "encryption"] as const,
};
