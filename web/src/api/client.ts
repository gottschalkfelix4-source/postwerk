import type { ApiErrorBody } from "./types";

/** Error thrown for every failed API request. */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
  }
}

export function isApiError(err: unknown, code?: string): err is ApiError {
  return err instanceof ApiError && (code === undefined || err.code === code);
}

/** Human-readable message for any thrown value. */
export function errorMessage(err: unknown): string {
  if (err instanceof ApiError) return err.message;
  if (err instanceof Error) return err.message;
  return "Unbekannter Fehler.";
}

type Method = "GET" | "POST" | "PUT" | "PATCH" | "DELETE";

export type QueryValue = string | number | boolean | null | undefined;

export interface RequestOptions {
  query?: Record<string, QueryValue>;
  body?: unknown;
  signal?: AbortSignal;
  /** Do not trigger the global redirect to /login on 401 (used by login itself). */
  skipAuthRedirect?: boolean;
}

let unauthorizedHandler: () => void = () => {
  if (!window.location.pathname.startsWith("/login")) {
    window.location.assign("/login");
  }
};

/** Replace the handler that runs when the session is missing or expired. */
export function setUnauthorizedHandler(handler: () => void): void {
  unauthorizedHandler = handler;
}

const defaultMessages: Record<number, [string, string]> = {
  400: ["invalid", "Die Anfrage ist ungültig."],
  401: ["unauthorized", "Bitte melde dich an."],
  403: ["forbidden", "Keine Berechtigung."],
  404: ["not_found", "Nicht gefunden."],
  409: ["conflict", "Konflikt mit vorhandenen Daten."],
  429: ["rate_limited", "Zu viele Anfragen. Bitte warte einen Moment."],
  503: ["unavailable", "Dienst momentan nicht verfügbar."],
};

export function buildUrl(path: string, query?: Record<string, QueryValue>): string {
  if (!query) return path;
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    if (value === undefined || value === null || value === "") continue;
    params.set(key, String(value));
  }
  const qs = params.toString();
  return qs ? `${path}?${qs}` : path;
}

export async function request<T>(method: Method, path: string, opts: RequestOptions = {}): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json" };
  if (method !== "GET") headers["X-Postwerk"] = "1";

  let body: string | undefined;
  if (opts.body !== undefined) {
    headers["Content-Type"] = "application/json";
    body = JSON.stringify(opts.body);
  }

  let res: Response;
  try {
    res = await fetch(buildUrl(path, opts.query), {
      method,
      headers,
      body,
      credentials: "include",
      signal: opts.signal,
    });
  } catch (err) {
    if (err instanceof DOMException && err.name === "AbortError") throw err;
    throw new ApiError(0, "network", "Der Server ist nicht erreichbar.");
  }

  if (!res.ok) {
    const [fallbackCode, fallbackMessage] = defaultMessages[res.status] ?? [
      res.status >= 500 ? "server_error" : "error",
      `Anfrage fehlgeschlagen (HTTP ${res.status}).`,
    ];
    let code = fallbackCode;
    let message = fallbackMessage;
    try {
      const data = (await res.json()) as Partial<ApiErrorBody>;
      if (data && typeof data.error === "string") code = data.error;
      if (data && typeof data.message === "string" && data.message) message = data.message;
    } catch {
      // Body is not JSON; keep the defaults.
    }
    if (res.status === 401 && code === "unauthorized" && !opts.skipAuthRedirect) {
      unauthorizedHandler();
    }
    throw new ApiError(res.status, code, message);
  }

  if (res.status === 204) return undefined as T;
  const contentType = res.headers.get("content-type") ?? "";
  if (contentType.includes("application/json")) return (await res.json()) as T;
  return (await res.text()) as unknown as T;
}

export const http = {
  get: <T>(path: string, opts?: RequestOptions) => request<T>("GET", path, opts),
  post: <T>(path: string, body?: unknown, opts?: RequestOptions) =>
    request<T>("POST", path, { ...opts, body }),
  put: <T>(path: string, body?: unknown, opts?: RequestOptions) =>
    request<T>("PUT", path, { ...opts, body }),
  patch: <T>(path: string, body?: unknown, opts?: RequestOptions) =>
    request<T>("PATCH", path, { ...opts, body }),
  delete: <T>(path: string, opts?: RequestOptions) => request<T>("DELETE", path, opts),
};
