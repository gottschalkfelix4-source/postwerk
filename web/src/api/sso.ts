// Single sign-on (OpenID Connect) and LDAP directory endpoints.
import { http } from "./client";
import type { AccountSource, LdapSettings } from "./types";

/** Public: whether the SSO button is shown on the login page. */
export interface SsoStatus {
  enabled: boolean;
  label: string;
  /** Password login only for local administrators (break-glass). */
  onlySso: boolean;
}

export interface SsoInfo {
  redirectUri: string;
  postLogoutRedirectUri: string;
  publicUrlConfigured: boolean;
  localAdmins: number;
}

export interface OidcDiscovery {
  issuer: string;
  authorizationEndpoint: string;
  tokenEndpoint: string;
  userinfoEndpoint: string;
  endSessionEndpoint: string;
  scopesSupported: string[] | null;
  pkceMethods: string[] | null;
}

export interface OidcTestResult {
  ok: boolean;
  latencyMs: number;
  error?: string;
  discovery?: OidcDiscovery;
}

export interface LdapEntry {
  dn: string;
  email: string;
  name: string;
  quotaBytes: number;
  hasQuota: boolean;
  admin: boolean;
  attributes?: Record<string, string[]>;
}

export interface LdapTestResult {
  ok: boolean;
  latencyMs: number;
  error?: string;
  entry?: LdapEntry;
  account?: { id: number; source: AccountSource };
}

export type LdapSyncStatus = "exists" | "other_source" | "missing" | "no_domain" | "alias" | "created";

export interface LdapSyncResult {
  users: { email: string; name: string; dn: string; admin: boolean; status: LdapSyncStatus; accountId?: number }[];
  orphans: { id: number; email: string; disabled: boolean; source: AccountSource }[];
  created: number;
  disabled: number;
  truncated: boolean;
}

/** URL that starts the SSO login (a full-page navigation, not fetch). */
export const ssoStartUrl = "/api/auth/oidc/start";

export const ssoApi = {
  status: () => http.get<SsoStatus>("/api/auth/sso", { skipAuthRedirect: true }),
  info: () => http.get<SsoInfo>("/api/admin/sso/info"),
  testOidc: (issuerUrl: string) => http.post<OidcTestResult>("/api/admin/oidc/test", { issuerUrl }),
  testLdap: (email: string, config?: Partial<LdapSettings>) =>
    http.post<LdapTestResult>("/api/admin/ldap/test", { email, config }),
  syncLdap: (apply: boolean) => http.post<LdapSyncResult>("/api/admin/ldap/sync", { apply }),
};
