// API and types for sender authentication: DKIM key rotation, implicit
// postmaster/abuse aliases and received DMARC/TLS reports.
import { http } from "./client";
import type { Ok } from "./types";

const enc = encodeURIComponent;

export type DkimAlgorithm = "rsa" | "ed25519";
export type DkimState = "pending" | "active" | "retired";

export interface DkimKey {
  id: number;
  domain: string;
  selector: string;
  algorithm: DkimAlgorithm;
  state: DkimState;
  createdAt: string;
  activatedAt: string | null;
  retiredAt: string | null;
  /** Retired keys: when the DNS record may be removed. */
  removableAt: string | null;
  dnsName: string;
  dnsValue: string;
}

export interface DkimPublication {
  name: string;
  published: boolean;
  found: string[];
  error?: string;
}

/** Implicit postmaster@/abuse@ address of a domain (read-only). */
export interface SystemAlias {
  address: string;
  targets: string[];
  system: true;
  /** Target comes from the settings instead of the first admin account. */
  configured: boolean;
}

export interface DmarcPolicyPublished { domain: string; adkim: string; aspf: string; p: string; sp: string; pct: string; }

export interface DmarcAuthResult { domain: string; selector?: string; scope?: string; result: string; }

export interface DmarcRecord {
  sourceIp: string;
  count: number;
  disposition: string;
  dkim: string;
  spf: string;
  headerFrom: string;
  envelopeFrom: string;
  dkimResults: DmarcAuthResult[];
  spfResults: DmarcAuthResult[];
}

export interface DmarcReport {
  id: number;
  domain: string;
  orgName: string;
  email: string;
  reportId: string;
  begin: string;
  end: string;
  policy: DmarcPolicyPublished;
  messages: number;
  failed: number;
  receivedAt: string;
  records?: DmarcRecord[];
}

export interface DmarcDomainSummary { domain: string; reports: number; messages: number; passed: number; failed: number; reporters: string[]; }

export interface DmarcSource {
  domain: string;
  sourceIp: string;
  count: number;
  spfPass: number;
  dkimPass: number;
  dmarcPass: number;
  quarantined: number;
  rejected: number;
  reporters: string[];
  headerFrom: string[];
}

export interface DmarcReportsOverview { summary: DmarcDomainSummary[]; sources: DmarcSource[]; reports: DmarcReport[]; }

export interface TlsFailure {
  resultType: string;
  sendingMtaIp: string;
  receivingMxHostname: string;
  receivingMxHelo?: string;
  receivingIp: string;
  failedSessionCount: number;
  additionalInfo?: string;
  failureReasonCode?: string;
}

export interface TlsReport {
  id: number;
  domain: string;
  orgName: string;
  email: string;
  reportId: string;
  begin: string;
  end: string;
  policyType: string;
  policyString: string[];
  mxHosts: string[];
  successCount: number;
  failureCount: number;
  failures: TlsFailure[];
  receivedAt: string;
}

export interface TlsDomainSummary { domain: string; reports: number; success: number; failure: number; reporters: string[]; }

export interface TlsFailureGroup {
  domain: string;
  resultType: string;
  receivingMxHostname: string;
  sessions: number;
  sendingIps: string[];
  reporters: string[];
  lastSeen: string;
}

export interface TlsReportsOverview { summary: TlsDomainSummary[]; failures: TlsFailureGroup[]; reports: TlsReport[]; }

export interface ReportQuery { domain?: string; days?: number; }

export const mailAuthApi = {
  dkimKeys: (domain: string) => http.get<DkimKey[]>(`/api/admin/domains/${enc(domain)}/dkim`),
  createDkimKey: (domain: string, algorithm: DkimAlgorithm) =>
    http.post<DkimKey>(`/api/admin/domains/${enc(domain)}/dkim`, { algorithm }),
  checkDkimKey: (domain: string, id: number) =>
    http.post<DkimPublication>(`/api/admin/domains/${enc(domain)}/dkim/${id}/check`, {}),
  activateDkimKey: (domain: string, id: number, force = false) =>
    http.post<DkimKey[]>(`/api/admin/domains/${enc(domain)}/dkim/${id}/activate`, { force }),
  deleteDkimKey: (domain: string, id: number) => http.delete<Ok>(`/api/admin/domains/${enc(domain)}/dkim/${id}`),

  systemAliases: () => http.get<SystemAlias[]>("/api/admin/aliases/system"),

  dmarcReports: (q: ReportQuery) => http.get<DmarcReportsOverview>("/api/admin/reports/dmarc", { query: { domain: q.domain, days: q.days } }),
  dmarcReport: (id: number) => http.get<DmarcReport>(`/api/admin/reports/dmarc/${id}`),
  deleteDmarcReport: (id: number) => http.delete<Ok>(`/api/admin/reports/dmarc/${id}`),
  tlsReports: (q: ReportQuery) => http.get<TlsReportsOverview>("/api/admin/reports/tls", { query: { domain: q.domain, days: q.days } }),
  deleteTlsReport: (id: number) => http.delete<Ok>(`/api/admin/reports/tls/${id}`),
};
