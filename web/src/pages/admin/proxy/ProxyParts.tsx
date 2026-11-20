import { Fragment, useEffect, useRef, useState, type ReactNode } from "react";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import {
  CircleCheck,
  CircleX,
  FileCode,
  FileKey,
  Info,
  MailWarning,
  MonitorSmartphone,
  Network,
  RefreshCw,
  TriangleAlert,
  Waypoints,
  type LucideIcon,
} from "lucide-react";
import { adminApi } from "@/api/endpoints";
import type { ProxyConfigFile, ProxyDetect, ProxyGuide, ProxyKind, Settings } from "@/api/types";
import { useDebounced } from "@/lib/hooks";
import { cn, storageGet, storageSet } from "@/lib/utils";
import { Badge, Button, ChipInput, CopyButton, ErrorState, Field, Input, Notice, Skeleton, Spinner, SwitchRow } from "@/components/ui";

// ---------------------------------------------------------------------------
// Data helpers
// ---------------------------------------------------------------------------

export const proxyKeys = {
  detect: ["admin", "proxy-detect"] as const,
  guide: (kind: ProxyKind, upstream: string) => ["admin", "proxy-guide", kind, upstream] as const,
};

/** GET /api/admin/proxy/detect – always fresh, it describes the current request. */
export function useProxyDetect(enabled = true) {
  return useQuery({ queryKey: proxyKeys.detect, queryFn: adminApi.proxyDetect, staleTime: 0, retry: false, enabled });
}

export type WebSettings = Settings["web"];

/** Normalizes the `web` section with a fixed key order (drafts are compared via JSON). */
export function webOf(w: Partial<WebSettings> | undefined): WebSettings {
  return {
    publicUrl: w?.publicUrl ?? "",
    behindProxy: w?.behindProxy ?? false,
    trustedProxies: w?.trustedProxies ?? [],
    secureCookies: w?.secureCookies ?? false,
  };
}

/** Fills empty proxy fields with values derived from the detection. */
export function withProxySuggestions(w: WebSettings, detect: ProxyDetect | undefined, hostname: string): WebSettings {
  const publicUrl = w.publicUrl || detect?.suggestedPublicUrl || (hostname ? `https://${hostname}` : "");
  return {
    publicUrl,
    behindProxy: true,
    trustedProxies: w.trustedProxies.length > 0 ? w.trustedProxies : detect?.suggestedTrustedProxy ? [detect.suggestedTrustedProxy] : [],
    secureCookies: w.behindProxy ? w.secureCookies : w.secureCookies || publicUrl.startsWith("https://"),
  };
}

/** Initial draft for the setup step: suggests "yes" when the request came through a proxy. */
export function initialWebDraft(settings: Settings, detect: ProxyDetect | undefined, hostname: string): WebSettings {
  const cur = webOf(settings.web);
  if (cur.behindProxy || detect?.viaProxy) return withProxySuggestions(cur, detect, hostname);
  return cur;
}

const IPV4_RE = /^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$/;

/**
 * Suggests the upstream address from the host the browser used. Only safe when
 * the request reached Postwerk directly or the host is a literal IP address.
 */
export function suggestUpstream(detect: ProxyDetect | undefined): string {
  const host = detect?.requestHost?.trim();
  if (!host) return "";
  const m = /^(\[[^\]]+\]|[^:]+)(?::(\d+))?$/.exec(host);
  if (!m) return "";
  const [, name, port] = m;
  if (IPV4_RE.test(name)) return `http://${host}`;
  if (!detect?.viaProxy && port) return `http://${host}`;
  return "";
}

export function isHttpUrl(value: string): boolean {
  try {
    const u = new URL(value.trim());
    return (u.protocol === "http:" || u.protocol === "https:") && !!u.hostname;
  } catch {
    return false;
  }
}

/** Validates an IP address or CIDR network for the trusted proxy list. */
export function proxyEntryError(entry: string): string | null {
  const [ip, prefix, ...rest] = entry.split("/");
  if (rest.length > 0) return "Ungültige Angabe.";
  const v4 = IPV4_RE.test(ip);
  const v6 = !v4 && ip.includes(":") && /^[0-9a-f:.]+$/i.test(ip);
  if (!v4 && !v6) return "Bitte eine IP-Adresse oder ein Netz in CIDR-Schreibweise angeben, z. B. 172.17.0.0/16.";
  if (prefix !== undefined) {
    const n = Number(prefix);
    if (!/^\d+$/.test(prefix) || n > (v4 ? 32 : 128)) return "Ungültige Präfixlänge.";
  }
  return null;
}

const KIND_KEY = "postwerk.proxy.kind";
const UPSTREAM_KEY = "postwerk.proxy.upstream";

const kinds: ProxyKind[] = ["npm", "traefik", "caddy", "nginx", "swag", "other"];

/**
 * Proxy type and upstream address. Both only feed the guide and are not part
 * of the settings, so they are remembered in this browser.
 */
export function useGuideInput(detect: ProxyDetect | undefined) {
  const [kind, setKindState] = useState<ProxyKind | null>(() => {
    const v = storageGet(KIND_KEY);
    return v && (kinds as string[]).includes(v) ? (v as ProxyKind) : null;
  });
  const [typed, setTyped] = useState<string | null>(() => storageGet(UPSTREAM_KEY));
  const suggestion = suggestUpstream(detect);
  return {
    kind,
    setKind: (k: ProxyKind) => {
      setKindState(k);
      storageSet(KIND_KEY, k);
    },
    upstream: typed ?? suggestion,
    suggestion,
    setUpstream: (v: string) => {
      setTyped(v);
      storageSet(UPSTREAM_KEY, v);
    },
  };
}

// ---------------------------------------------------------------------------
// Small building blocks
// ---------------------------------------------------------------------------

/** Plain text with `code` segments in backticks. */
export function RichText({ text }: { text: string }) {
  const parts = text.split(/`([^`]+)`/);
  return (
    <>
      {parts.map((p, i) =>
        i % 2 === 1 ? (
          <code key={i} className="break-all rounded bg-black/[0.06] px-1 py-px font-mono text-[0.9em] dark:bg-white/10">
            {p}
          </code>
        ) : (
          <Fragment key={i}>{p}</Fragment>
        ),
      )}
    </>
  );
}

/** Copyable monospace value (paths, URLs). */
export function PathBox({ value }: { value: string }) {
  return (
    <div className="flex items-start gap-1">
      <code className="block min-w-0 flex-1 break-all rounded-md bg-zinc-50 px-2 py-1 font-mono text-xs leading-relaxed text-zinc-700 ring-1 ring-inset ring-zinc-200 dark:bg-zinc-950 dark:text-zinc-300 dark:ring-zinc-800">
        {value}
      </code>
      <CopyButton value={value} />
    </div>
  );
}

const kindMeta: Record<ProxyKind, { name: string; text: string; short: string; tone: string }> = {
  npm: {
    name: "Nginx Proxy Manager",
    short: "NP",
    text: "Weboberfläche für nginx, auf Unraid weit verbreitet.",
    tone: "bg-rose-50 text-rose-600 dark:bg-rose-500/15 dark:text-rose-300",
  },
  traefik: {
    name: "Traefik",
    short: "Tr",
    text: "Konfiguration über Docker-Labels oder dynamische Dateien.",
    tone: "bg-sky-50 text-sky-700 dark:bg-sky-500/15 dark:text-sky-300",
  },
  caddy: {
    name: "Caddy",
    short: "Ca",
    text: "Automatisches HTTPS mit einer kurzen Caddyfile.",
    tone: "bg-teal-50 text-teal-700 dark:bg-teal-500/15 dark:text-teal-300",
  },
  nginx: {
    name: "nginx",
    short: "Nx",
    text: "Klassische Konfiguration mit einem server-Block.",
    tone: "bg-emerald-50 text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-300",
  },
  swag: {
    name: "SWAG",
    short: "Sw",
    text: "nginx-Container von linuxserver.io mit Certbot.",
    tone: "bg-violet-50 text-violet-700 dark:bg-violet-500/15 dark:text-violet-300",
  },
  other: {
    name: "Anderer",
    short: "",
    text: "Allgemeine Hinweise für andere Reverse-Proxys.",
    tone: "bg-zinc-100 text-zinc-500 dark:bg-zinc-800 dark:text-zinc-400",
  },
};

export function proxyKindName(kind: ProxyKind): string {
  return kindMeta[kind].name;
}

function KindIcon({ kind }: { kind: ProxyKind }) {
  const meta = kindMeta[kind];
  return (
    <span className={cn("flex h-8 w-8 shrink-0 items-center justify-center rounded-lg text-xs font-bold", meta.tone)} aria-hidden>
      {meta.short || <Network className="h-4 w-4" />}
    </span>
  );
}

/** Tiles for choosing the reverse proxy software. */
export function ProxyKindPicker({
  value,
  onChange,
  compact,
}: {
  value: ProxyKind | null;
  onChange: (kind: ProxyKind) => void;
  compact?: boolean;
}) {
  return (
    <div className={cn("grid gap-2.5", compact ? "grid-cols-2 sm:grid-cols-3" : "sm:grid-cols-2 lg:grid-cols-3")} role="radiogroup" aria-label="Reverse-Proxy">
      {kinds.map((k) => {
        const active = k === value;
        const meta = kindMeta[k];
        return (
          <button
            key={k}
            type="button"
            role="radio"
            aria-checked={active}
            onClick={() => onChange(k)}
            className={cn(
              "flex min-w-0 flex-col gap-2 rounded-xl border p-3 text-left transition",
              "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-indigo-500/50",
              active
                ? "border-indigo-400 bg-indigo-50/60 ring-1 ring-indigo-400 dark:border-indigo-500/60 dark:bg-indigo-500/10 dark:ring-indigo-500/60"
                : "border-zinc-200 hover:border-zinc-300 hover:bg-zinc-50 dark:border-zinc-800 dark:hover:border-zinc-700 dark:hover:bg-zinc-800/50",
            )}
          >
            <span className="flex min-w-0 items-center gap-2.5">
              <KindIcon kind={k} />
              <span className="truncate text-sm font-medium text-zinc-900 dark:text-zinc-100" title={meta.name}>
                {compact && k === "npm" ? "NPM" : meta.name}
              </span>
            </span>
            {!compact && <span className="line-clamp-2 text-xs leading-relaxed text-zinc-500 dark:text-zinc-400">{meta.text}</span>}
          </button>
        );
      })}
    </div>
  );
}

/** Result of the automatic detection shown next to the "behind a proxy?" question. */
export function DetectNotice({ detect, loading, suggested }: { detect: ProxyDetect | undefined; loading?: boolean; suggested?: boolean }) {
  if (loading) return <Skeleton className="h-12 rounded-lg" />;
  if (!detect) return null;
  if (detect.viaProxy) {
    return (
      <Notice tone="success" icon={Waypoints} title="Reverse-Proxy erkannt">
        Deine Anfrage kam über einen Proxy von <code className="font-mono">{detect.remoteIp}</code>.
        {suggested && " „Ja“ ist deshalb vorausgewählt."}
      </Notice>
    );
  }
  return (
    <div className="flex items-start gap-3 rounded-lg border border-zinc-200 bg-zinc-50 px-3.5 py-3 text-sm text-zinc-600 dark:border-zinc-800 dark:bg-zinc-950/50 dark:text-zinc-400">
      <Info className="mt-0.5 h-4 w-4 shrink-0 text-zinc-400" />
      <span>
        Deine Anfrage kam direkt von <code className="font-mono text-zinc-800 dark:text-zinc-200">{detect.remoteIp}</code> – es wurde kein
        Proxy erkannt.
      </span>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Form fields
// ---------------------------------------------------------------------------

export function PublicUrlField({ value, onChange, hostname }: { value: string; onChange: (v: string) => void; hostname: string }) {
  const invalid = !!value.trim() && !isHttpUrl(value);
  return (
    <Field
      label="Öffentliche URL"
      help="Unter dieser Adresse rufst du Postwerk im Browser auf. Sie wird u. a. für Links in E-Mails verwendet."
      error={invalid ? "Bitte eine vollständige Adresse mit http:// oder https:// angeben." : null}
    >
      {(id) => (
        <Input
          id={id}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          placeholder={`https://${hostname || "mail.beispiel.de"}`}
          invalid={invalid}
          spellCheck={false}
          className="font-mono text-[13px]"
        />
      )}
    </Field>
  );
}

export function UpstreamField({ value, onChange, suggestion }: { value: string; onChange: (v: string) => void; suggestion: string }) {
  const presets = [...new Set([suggestion, "http://postwerk:8080"].filter(Boolean))];
  return (
    <Field
      label="Adresse, unter der der Proxy Postwerk erreicht"
      help={
        <>
          Dieses Ziel trägst du im Proxy ein, z. B. <code className="font-mono">http://192.168.1.10:8880</code> (Server-IP und Port der
          Weboberfläche). Hängen Proxy und Postwerk im selben Docker-Netzwerk, nimm <code className="font-mono">http://postwerk:8080</code>.
        </>
      }
    >
      {(id) => (
        <div className="space-y-2">
          <Input
            id={id}
            value={value}
            onChange={(e) => onChange(e.target.value)}
            placeholder="http://192.168.1.10:8880"
            spellCheck={false}
            className="font-mono text-[13px]"
          />
          {presets.length > 0 && (
            <div className="flex flex-wrap gap-1.5">
              {presets.map((p) => (
                <button
                  key={p}
                  type="button"
                  onClick={() => onChange(p)}
                  className={cn(
                    "rounded-md px-2 py-1 font-mono text-xs ring-1 ring-inset transition",
                    value === p
                      ? "bg-indigo-50 text-indigo-700 ring-indigo-200 dark:bg-indigo-500/15 dark:text-indigo-300 dark:ring-indigo-500/30"
                      : "text-zinc-600 ring-zinc-200 hover:bg-zinc-50 dark:text-zinc-400 dark:ring-zinc-700 dark:hover:bg-zinc-800",
                  )}
                >
                  {p}
                </button>
              ))}
            </div>
          )}
        </div>
      )}
    </Field>
  );
}

export function TrustedProxiesField({
  value,
  onChange,
  suggestion,
}: {
  value: string[];
  onChange: (v: string[]) => void;
  suggestion: string | null | undefined;
}) {
  return (
    <Field
      label="Vertrauenswürdige Proxys"
      help="IP-Adressen oder Netze (CIDR), deren X-Forwarded-Header Postwerk glaubt. Leer = alle privaten Netze und localhost."
    >
      {(id) => (
        <ChipInput
          id={id}
          value={value}
          onChange={onChange}
          placeholder="z. B. 172.17.0.0/16"
          validate={proxyEntryError}
          transform={(v) => v.trim().toLowerCase()}
          suggestions={suggestion && !value.includes(suggestion) ? [suggestion] : undefined}
        />
      )}
    </Field>
  );
}

export function SecureCookiesRow({ checked, onChange }: { checked: boolean; onChange: (v: boolean) => void }) {
  const viaHttp = typeof window !== "undefined" && window.location.protocol === "http:";
  return (
    <div className="space-y-3">
      <SwitchRow
        label="Sichere Cookies"
        description="Das Anmelde-Cookie wird nur über HTTPS übertragen. Empfohlen, sobald die Weboberfläche per https erreichbar ist."
        checked={checked}
        onChange={onChange}
      />
      {checked && viaHttp && (
        <Notice tone="warning" icon={TriangleAlert}>
          Du rufst Postwerk gerade über <code className="font-mono">http://</code> auf. Mit sicheren Cookies klappt die Anmeldung danach nur
          noch über die https-Adresse.
        </Notice>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Guide
// ---------------------------------------------------------------------------

/** Mail ports never go through the web proxy. */
export function MailPortsNotice() {
  return (
    <Notice tone="warning" icon={MailWarning} title="Die Mail-Ports 25, 465, 587 und 995 gehen nicht über den Proxy">
      Nur die Weboberfläche läuft über den Reverse-Proxy. Leite die Mail-Ports im Router bzw. in Unraid direkt an Postwerk weiter (TCP) –
      Mail-Programme und andere Mailserver verbinden sich ohne Umweg mit Postwerk.
    </Notice>
  );
}

function ConfigFileBlock({ file }: { file: ProxyConfigFile }) {
  return (
    <div className="overflow-hidden rounded-lg bg-zinc-900 ring-1 ring-inset ring-zinc-800 dark:bg-zinc-950">
      <div className="flex items-center justify-between gap-2 border-b border-zinc-800 py-1 pl-3 pr-1.5">
        <span className="flex min-w-0 items-center gap-2">
          <FileCode className="h-3.5 w-3.5 shrink-0 text-zinc-500" />
          <span className="truncate font-mono text-xs text-zinc-200">{file.name}</span>
          <span className="shrink-0 text-[10px] font-medium uppercase tracking-wide text-zinc-500">{file.language}</span>
        </span>
        <CopyButton value={file.content} label="Kopieren" className="text-zinc-400 hover:bg-zinc-800 hover:text-zinc-100 dark:hover:bg-zinc-800" />
      </div>
      <pre className="max-h-96 overflow-auto px-3 py-2.5 font-mono text-[12.5px] leading-relaxed text-zinc-100">
        <code>{file.content}</code>
      </pre>
    </div>
  );
}

function CertificateHelp({ guide, hostname, action }: { guide: ProxyGuide; hostname: string; action?: ReactNode }) {
  const c = guide.certificate;
  return (
    <div className="space-y-3 rounded-xl border border-indigo-200 bg-indigo-50/40 p-4 dark:border-indigo-500/30 dark:bg-indigo-500/[0.05]">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h4 className="flex items-center gap-2 text-sm font-semibold text-zinc-900 dark:text-zinc-100">
          <FileKey className="h-4 w-4 text-indigo-500" /> Zertifikat für die Mail-Ports
        </h4>
        <Badge tone="indigo">{c.recommendedMode === "files" ? "Empfohlen: vom Proxy übernehmen" : "Empfohlen: Let’s Encrypt direkt"}</Badge>
      </div>
      <p className="text-sm leading-relaxed text-zinc-600 dark:text-zinc-300">
        Mail-Programme verbinden sich direkt mit Postwerk. Damit sie keine Zertifikatswarnung zeigen, braucht Postwerk ein gültiges Zertifikat
        für <code className="font-mono">{hostname || "deinen Hostnamen"}</code>
        {c.recommendedMode === "files"
          ? " – am einfachsten das, welches dein Proxy ohnehin ausstellt und erneuert."
          : " – Postwerk kann es selbst per ACME beziehen."}
      </p>
      <dl className="space-y-3 text-sm">
        {c.hostPathHint && (
          <div className="space-y-1">
            <dt className="text-xs font-medium text-zinc-500 dark:text-zinc-400">Hier legt der Proxy seine Zertifikate ab</dt>
            <dd>
              <PathBox value={c.hostPathHint} />
            </dd>
          </div>
        )}
        {c.mountHint && (
          <div className="space-y-1">
            <dt className="text-xs font-medium text-zinc-500 dark:text-zinc-400">So bindest du den Ordner ein</dt>
            <dd className="leading-relaxed text-zinc-700 dark:text-zinc-300">
              <RichText text={c.mountHint} />
            </dd>
          </div>
        )}
        {c.containerPathExample && (
          <div className="space-y-1">
            <dt className="text-xs font-medium text-zinc-500 dark:text-zinc-400">Passender Zertifikatspfad in Postwerk</dt>
            <dd>
              <PathBox value={c.containerPathExample} />
            </dd>
          </div>
        )}
      </dl>
      {action && <div className="pt-1">{action}</div>}
    </div>
  );
}

/** Step-by-step instructions for the chosen proxy, reloaded when kind or upstream change. */
export function ProxyGuidePanel({
  kind,
  upstream,
  hostname,
  certificateAction,
}: {
  kind: ProxyKind | null;
  upstream: string;
  hostname: string;
  certificateAction?: ReactNode;
}) {
  const debounced = useDebounced(upstream.trim(), 500);
  const q = useQuery({
    queryKey: proxyKeys.guide(kind ?? "other", debounced),
    queryFn: ({ signal }) => adminApi.proxyGuide(kind ?? "other", debounced, signal),
    enabled: kind !== null,
    placeholderData: keepPreviousData,
    staleTime: 5 * 60_000,
  });

  if (kind === null) {
    return (
      <div className="space-y-4">
        <MailPortsNotice />
        <p className="rounded-lg border border-dashed border-zinc-300 px-4 py-6 text-center text-sm text-zinc-500 dark:border-zinc-700 dark:text-zinc-400">
          Wähle oben deinen Reverse-Proxy, um die passende Anleitung zu sehen.
        </p>
      </div>
    );
  }

  const g = q.data;
  return (
    <div className="space-y-4">
      <MailPortsNotice />
      {q.isLoading ? (
        <div className="space-y-2">
          <Skeleton className="h-5 w-48 rounded" />
          <Skeleton className="h-24 rounded-lg" />
          <Skeleton className="h-40 rounded-lg" />
        </div>
      ) : q.error || !g ? (
        <ErrorState error={q.error} onRetry={() => void q.refetch()} />
      ) : (
        <div className={cn("space-y-4 transition-opacity", q.isFetching && q.isPlaceholderData && "opacity-60")}>
          <div className="flex items-center gap-2">
            <h4 className="text-sm font-semibold text-zinc-900 dark:text-zinc-100">Anleitung für {g.title}</h4>
            {q.isFetching && <Spinner className="h-3.5 w-3.5 text-zinc-400" />}
          </div>
          {g.steps.length > 0 && (
            <ol className="space-y-2.5">
              {g.steps.map((s, i) => (
                <li key={i} className="flex gap-3">
                  <span className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-indigo-50 text-xs font-semibold text-indigo-700 ring-1 ring-inset ring-indigo-200 dark:bg-indigo-500/15 dark:text-indigo-300 dark:ring-indigo-500/30">
                    {i + 1}
                  </span>
                  <p className="min-w-0 pt-0.5 text-sm leading-relaxed text-zinc-700 dark:text-zinc-300">
                    <RichText text={s} />
                  </p>
                </li>
              ))}
            </ol>
          )}
          {g.files.map((f) => (
            <ConfigFileBlock key={f.name} file={f} />
          ))}
          {g.notes.length > 0 && (
            <Notice tone="info" icon={Info} title="Hinweise">
              <ul className="list-disc space-y-1 pl-4">
                {g.notes.map((n, i) => (
                  <li key={i}>
                    <RichText text={n} />
                  </li>
                ))}
              </ul>
            </Notice>
          )}
          <CertificateHelp guide={g} hostname={hostname} action={certificateAction} />
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Connection check
// ---------------------------------------------------------------------------

type RowState = "ok" | "warn" | "bad" | "neutral";

function CheckRow({ icon: Icon, label, state, children }: { icon: LucideIcon; label: string; state: RowState; children: ReactNode }) {
  const StateIcon = state === "ok" ? CircleCheck : state === "bad" ? CircleX : state === "warn" ? TriangleAlert : null;
  return (
    <div className="flex items-start gap-3 py-2.5">
      <Icon className="mt-0.5 h-4 w-4 shrink-0 text-zinc-400" />
      <div className="min-w-0 flex-1">
        <div className="text-sm font-medium text-zinc-800 dark:text-zinc-200">{label}</div>
        <div className="mt-0.5 text-xs leading-relaxed text-zinc-500 dark:text-zinc-400">{children}</div>
      </div>
      {StateIcon && (
        <StateIcon
          className={cn(
            "mt-0.5 h-4 w-4 shrink-0",
            state === "ok" ? "text-emerald-500" : state === "bad" ? "text-red-500" : "text-amber-500",
          )}
        />
      )}
    </div>
  );
}

/** Shows how Postwerk currently sees the browser's request. */
export function ProxyCheckPanel({
  detect,
  loading,
  error,
  onRecheck,
  note,
}: {
  detect: ProxyDetect | undefined;
  loading: boolean;
  error: unknown;
  onRecheck: () => void;
  note?: ReactNode;
}) {
  const d = detect;
  const sameAsProxy = !!d && d.viaProxy && d.clientIp === d.remoteIp;
  return (
    <div className="rounded-xl border border-zinc-200 p-4 dark:border-zinc-800">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <h4 className="text-sm font-semibold text-zinc-900 dark:text-zinc-100">Verbindung prüfen</h4>
          <p className="mt-0.5 text-xs leading-relaxed text-zinc-500 dark:text-zinc-400">
            Öffne Postwerk über die öffentliche URL und prüfe, ob die Anfrage über den Proxy ankommt.
          </p>
        </div>
        <Button icon={RefreshCw} onClick={onRecheck} loading={loading}>
          Erneut prüfen
        </Button>
      </div>
      {note && <p className="mt-2 flex items-start gap-1.5 text-xs text-zinc-500 dark:text-zinc-400"><Info className="mt-px h-3.5 w-3.5 shrink-0" />{note}</p>}
      {error && !d ? (
        <p className="mt-3 text-sm text-red-600 dark:text-red-400">Die Prüfung ist fehlgeschlagen. Bitte versuche es erneut.</p>
      ) : !d ? (
        <Skeleton className="mt-3 h-28 rounded-lg" />
      ) : (
        <div className="mt-2 divide-y divide-zinc-100 dark:divide-zinc-800">
          <CheckRow icon={Waypoints} label="Anfrage über den Proxy" state={d.viaProxy ? "ok" : "warn"}>
            {d.viaProxy ? (
              <>
                Ja, von <code className="font-mono">{d.remoteIp}</code>
                {d.forwardedProto || d.forwardedHost ? (
                  <>
                    {" "}
                    ({[d.forwardedProto, d.forwardedHost].filter(Boolean).join("://")})
                  </>
                ) : null}
              </>
            ) : (
              <>
                Nein – die Anfrage kam direkt von <code className="font-mono">{d.remoteIp}</code>. Rufe Postwerk über die öffentliche URL auf.
              </>
            )}
          </CheckRow>
          <CheckRow icon={Network} label="Proxy gilt als vertrauenswürdig" state={!d.viaProxy ? "neutral" : d.trusted ? "ok" : "bad"}>
            {!d.viaProxy ? (
              "Erst prüfbar, wenn die Anfrage über den Proxy kommt."
            ) : d.trusted ? (
              <>
                Ja – <code className="font-mono">{d.remoteIp}</code> darf X-Forwarded-Header setzen.
              </>
            ) : (
              <>
                Nein – nimm <code className="font-mono">{d.suggestedTrustedProxy || d.remoteIp}</code> in die vertrauenswürdigen Proxys auf und
                speichere.
              </>
            )}
          </CheckRow>
          <CheckRow icon={MonitorSmartphone} label="Deine IP-Adresse aus Sicht von Postwerk" state={!d.viaProxy ? "neutral" : sameAsProxy ? "warn" : "ok"}>
            <code className="font-mono text-zinc-700 dark:text-zinc-300">{d.clientIp || "–"}</code>
            {d.viaProxy &&
              (sameAsProxy
                ? " – das ist die Adresse des Proxys. Ohne echte Client-IP greifen Sperren bei Fehlversuchen für alle Benutzer gleichzeitig."
                : " – das sollte die IP-Adresse deines Browsers sein.")}
          </CheckRow>
        </div>
      )}
    </div>
  );
}

/**
 * Keeps `secureCookies` in sync with the URL scheme when the user edits the
 * URL, until the switch itself was flipped. The initial value is left alone.
 */
export function useCookieFollowsUrl(publicUrl: string | undefined, setSecure: (v: boolean) => void) {
  const [touched, setTouched] = useState(false);
  const last = useRef(publicUrl);
  useEffect(() => {
    const prev = last.current;
    last.current = publicUrl;
    if (touched || publicUrl === undefined || prev === undefined || prev === publicUrl) return;
    const url = publicUrl.trim();
    if (/^https:\/\//i.test(url)) setSecure(true);
    else if (/^http:\/\//i.test(url)) setSecure(false);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [publicUrl, touched]);
  return () => setTouched(true);
}
