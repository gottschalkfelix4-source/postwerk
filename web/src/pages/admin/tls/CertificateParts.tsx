import { useState, type ReactNode } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import {
  CircleCheck,
  CircleX,
  FileKey,
  FolderSearch,
  Globe,
  RefreshCw,
  RotateCw,
  ScanSearch,
  ShieldAlert,
  TriangleAlert,
  UserCog,
  type LucideIcon,
} from "lucide-react";
import { adminApi } from "@/api/endpoints";
import { errorMessage } from "@/api/client";
import type { CertificateFile, HttpPortMode, Settings, SettingsUpdate, TlsIssueResult, TlsMode, TlsTestResult } from "@/api/types";
import { formatDate } from "@/lib/format";
import { cn, isEmail } from "@/lib/utils";
import { Badge, Button, ErrorState, Field, Input, Notice, Skeleton } from "@/components/ui";

// ---------------------------------------------------------------------------
// Draft helpers
// ---------------------------------------------------------------------------

export interface TlsDraft {
  mode: TlsMode;
  acme: Settings["tls"]["acme"];
  files: Settings["tls"]["files"];
  httpPort: HttpPortMode;
}

/** Editable part of the `tls` section with a fixed key order (drafts are compared via JSON). */
export function tlsDraftOf(tls: Settings["tls"]): TlsDraft {
  const acme = {
    enabled: tls.acme?.enabled ?? false,
    email: tls.acme?.email ?? "",
    directoryUrl: tls.acme?.directoryUrl ?? "",
    domains: tls.acme?.domains ?? [],
  };
  return {
    // Older servers without `mode` only know the ACME switch.
    mode: tls.mode ?? (acme.enabled ? "acme" : "selfsigned"),
    acme,
    files: { certPath: tls.files?.certPath ?? "", keyPath: tls.files?.keyPath ?? "" },
    httpPort: tls.httpPort ?? "open",
  };
}

/** Body for PUT /api/admin/settings (without the read-only status). */
export function tlsUpdate(d: TlsDraft): SettingsUpdate {
  return { tls: { mode: d.mode, acme: { ...d.acme, enabled: d.mode === "acme" }, files: d.files, httpPort: d.httpPort } };
}

/** A Traefik acme.json contains the key itself, so no key path is needed. */
export function isAcmeJson(path: string): boolean {
  return /\.json$/i.test(path.trim());
}

export function tlsDraftErrors(d: TlsDraft) {
  return {
    email: d.mode === "acme" && !isEmail(d.acme.email),
    domains: d.mode === "acme" && d.acme.domains.length === 0,
    certPath: d.mode === "files" && !d.files.certPath.trim(),
    keyPath: d.mode === "files" && !d.files.keyPath.trim() && !isAcmeJson(d.files.certPath),
  };
}

export function tlsDraftValid(d: TlsDraft): boolean {
  return !Object.values(tlsDraftErrors(d)).some(Boolean);
}

export const tlsModeLabels: Record<TlsMode, string> = {
  files: "Zertifikat vom Reverse-Proxy",
  acme: "Let’s Encrypt direkt (ACME)",
  selfsigned: "Selbstsigniert",
};

const PERMISSION_RE = /permission denied|zugriff verweigert/i;

export function isPermissionError(message: string | null | undefined): boolean {
  return !!message && PERMISSION_RE.test(message);
}

// ---------------------------------------------------------------------------
// Status
// ---------------------------------------------------------------------------

export function tlsStatusTone(status: string): "red" | "green" | "neutral" {
  return /fehl|error|fail|expired|abgelaufen/i.test(status) ? "red" : /ok|valid|gültig|aktiv|active/i.test(status) ? "green" : "neutral";
}

export function TlsStatusBox({ status }: { status: string }) {
  const tone = tlsStatusTone(status);
  return (
    <div className="flex items-start gap-3 rounded-lg border border-zinc-200 bg-zinc-50 p-3 dark:border-zinc-800 dark:bg-zinc-950/50">
      <Badge tone={tone} dot>
        {tone === "green" ? "OK" : tone === "red" ? "Problem" : "Info"}
      </Badge>
      <p className="min-w-0 break-words text-sm text-zinc-700 dark:text-zinc-300">{status || "Keine Angabe"}</p>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Mode picker
// ---------------------------------------------------------------------------

const modes: { id: TlsMode; icon: LucideIcon; title: string; text: string }[] = [
  {
    id: "files",
    icon: FileKey,
    title: "Zertifikat vom Reverse-Proxy übernehmen",
    text: "Postwerk liest das Zertifikat, das dein Proxy ohnehin ausstellt und erneuert.",
  },
  {
    id: "acme",
    icon: Globe,
    title: "Let’s Encrypt direkt (ACME)",
    text: "Postwerk holt und erneuert das Zertifikat selbst. Port 80 muss direkt ankommen.",
  },
  {
    id: "selfsigned",
    icon: ShieldAlert,
    title: "Selbstsigniert",
    text: "Nur für Tests – Mail-Programme und andere Server warnen.",
  },
];

export function TlsModePicker({
  value,
  onChange,
  recommended,
  stacked,
}: {
  value: TlsMode;
  onChange: (mode: TlsMode) => void;
  recommended: TlsMode | null;
  /** One tile per row with the icon on the left, for narrow columns. */
  stacked?: boolean;
}) {
  return (
    <div className={cn("grid gap-2.5", !stacked && "md:grid-cols-3")} role="radiogroup" aria-label="Zertifikatsquelle">
      {modes.map((m) => {
        const active = m.id === value;
        const icon = (
          <span
            className={cn(
              "flex h-8 w-8 shrink-0 items-center justify-center rounded-lg",
              m.id === "selfsigned"
                ? "bg-amber-50 text-amber-600 dark:bg-amber-500/15 dark:text-amber-300"
                : "bg-indigo-50 text-indigo-600 dark:bg-indigo-500/15 dark:text-indigo-300",
            )}
          >
            <m.icon className="h-4 w-4" />
          </span>
        );
        const badge = recommended === m.id && <Badge tone="green">Empfohlen</Badge>;
        return (
          <button
            key={m.id}
            type="button"
            role="radio"
            aria-checked={active}
            onClick={() => onChange(m.id)}
            className={cn(
              "flex min-w-0 gap-2 rounded-xl border p-3 text-left transition",
              stacked ? "items-start gap-3" : "flex-col",
              "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-indigo-500/50",
              active
                ? "border-indigo-400 bg-indigo-50/60 ring-1 ring-indigo-400 dark:border-indigo-500/60 dark:bg-indigo-500/10 dark:ring-indigo-500/60"
                : "border-zinc-200 hover:border-zinc-300 hover:bg-zinc-50 dark:border-zinc-800 dark:hover:border-zinc-700 dark:hover:bg-zinc-800/50",
            )}
          >
            {stacked ? (
              <>
                {icon}
                <span className="min-w-0 flex-1">
                  <span className="flex flex-wrap items-center gap-2">
                    <span className="text-sm font-medium text-zinc-900 dark:text-zinc-100">{m.title}</span>
                    {badge}
                  </span>
                  <span className="mt-0.5 block text-xs leading-relaxed text-zinc-500 dark:text-zinc-400">{m.text}</span>
                </span>
              </>
            ) : (
              <>
                <span className="flex w-full items-start justify-between gap-2">
                  {icon}
                  {badge}
                </span>
                <span className="text-sm font-medium text-zinc-900 dark:text-zinc-100">{m.title}</span>
                <span className="text-xs leading-relaxed text-zinc-500 dark:text-zinc-400">{m.text}</span>
              </>
            )}
          </button>
        );
      })}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Notices per mode
// ---------------------------------------------------------------------------

export function AcmeNotice({ behindProxy }: { behindProxy: boolean }) {
  return (
    <Notice tone="warning" icon={TriangleAlert} title="Bitte beachten">
      <ul className="list-disc space-y-0.5 pl-4">
        <li>Port 80 oder 443 muss von außen direkt bei Postwerk ankommen, damit die Zertifizierungsstelle die Domain prüfen kann.</li>
        <li>Alle Namen müssen per DNS auf diesen Server zeigen.</li>
        <li>
          {behindProxy ? "Du nutzt einen Reverse-Proxy: " : "Hinter einem Reverse-Proxy "}
          ist das meist nicht möglich, weil der Proxy Port 80 belegt und die Prüfung selbst beantwortet. Übernimm dann besser das
          Zertifikat des Proxys.
        </li>
      </ul>
    </Notice>
  );
}

// ---------------------------------------------------------------------------
// Port 80 policy and immediate issuance (ACME mode)
// ---------------------------------------------------------------------------

const HTTP_PORT_OPTIONS: { value: HttpPortMode; title: string; text: string; recommended?: boolean }[] = [
  {
    value: "auto-close",
    title: "Nach dem Zertifikat schließen",
    text: "Port 80 bleibt nur offen, bis das Zertifikat da ist. Etwa 32 Tage vor Ablauf öffnet Postwerk ihn automatisch zur Verlängerung und schließt ihn danach wieder.",
    recommended: true,
  },
  {
    value: "redirect",
    title: "Nur Umleitung auf HTTPS",
    text: "Port 80 bleibt offen, beantwortet aber nur die Let’s-Encrypt-Prüfung und leitet alles andere auf HTTPS um.",
  },
  {
    value: "open",
    title: "Offen bis zum Zertifikat",
    text: "Während der Einrichtung ist die Weboberfläche auch über HTTP erreichbar. Sobald die Einrichtung abgeschlossen ist und Postwerk ein gültiges Let’s-Encrypt-Zertifikat hat, leitet Port 80 automatisch auf HTTPS um (hinter einem Reverse-Proxy oder mit eigenen Zertifikatsdateien bleibt HTTP offen).",
  },
];

export function HttpPortPicker({
  value,
  onChange,
  status,
}: {
  value: HttpPortMode;
  onChange: (v: HttpPortMode) => void;
  status?: string | null;
}) {
  return (
    <div className="space-y-2">
      <div className="text-sm font-medium text-zinc-900 dark:text-zinc-100">Port 80 (HTTP)</div>
      <div role="radiogroup" className="space-y-2">
        {HTTP_PORT_OPTIONS.map((o) => {
          const active = value === o.value;
          return (
            <button
              key={o.value}
              type="button"
              role="radio"
              aria-checked={active}
              onClick={() => onChange(o.value)}
              className={cn(
                "flex w-full items-start gap-3 rounded-lg border p-3 text-left transition-colors",
                active
                  ? "border-indigo-500 bg-indigo-50/60 ring-1 ring-indigo-500 dark:border-indigo-400 dark:bg-indigo-500/10 dark:ring-indigo-400"
                  : "border-zinc-200 hover:border-zinc-300 dark:border-zinc-800 dark:hover:border-zinc-700",
              )}
            >
              <span
                className={cn(
                  "mt-0.5 flex h-4 w-4 shrink-0 items-center justify-center rounded-full border",
                  active ? "border-indigo-600 dark:border-indigo-400" : "border-zinc-300 dark:border-zinc-600",
                )}
              >
                {active && <span className="h-2 w-2 rounded-full bg-indigo-600 dark:bg-indigo-400" />}
              </span>
              <span className="min-w-0">
                <span className="flex flex-wrap items-center gap-2 text-sm font-medium text-zinc-900 dark:text-zinc-100">
                  {o.title}
                  {o.recommended && <Badge tone="green">Empfohlen</Badge>}
                </span>
                <span className="mt-0.5 block text-xs text-zinc-500 dark:text-zinc-400">{o.text}</span>
              </span>
            </button>
          );
        })}
      </div>
      {status && (
        <p className="flex items-start gap-1.5 text-xs text-zinc-600 dark:text-zinc-400">
          <Globe className="mt-px h-3.5 w-3.5 shrink-0" />
          <span>
            Aktuell: {status}
          </span>
        </p>
      )}
      <p className="text-xs text-zinc-500 dark:text-zinc-400">
        Notfall, falls die Weboberfläche nicht mehr erreichbar ist:{" "}
        <code className="rounded bg-zinc-100 px-1 py-0.5 font-mono dark:bg-zinc-800">docker compose exec postwerk postwerk http-port open</code>
      </p>
    </div>
  );
}

/** Fetches the certificate right now (ACME issuance) and shows the result. */
export function IssueCertificateButton({ disabledReason }: { disabledReason?: string | null }) {
  const issue = useMutation({ mutationFn: adminApi.issueTls });
  const r: TlsIssueResult | undefined = issue.data;
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-3">
        <Button icon={RotateCw} onClick={() => issue.mutate()} loading={issue.isPending} disabled={!!disabledReason}>
          Zertifikat jetzt holen
        </Button>
        <span className="text-xs text-zinc-500 dark:text-zinc-400">
          {disabledReason ?? "Fragt Let’s Encrypt sofort an – das kann bis zu einer Minute dauern."}
        </span>
      </div>
      {issue.isError && (
        <Notice tone="error" icon={CircleX} title="Abruf fehlgeschlagen">
          {errorMessage(issue.error)}
        </Notice>
      )}
      {r && r.ok && (
        <Notice tone="success" icon={CircleCheck} title="Zertifikat aktiv">
          <span className="block">
            {r.names.join(", ")} – ausgestellt von {r.issuer || "unbekannt"}
            {r.notAfter ? `, gültig bis ${formatDate(r.notAfter)}` : ""}.
          </span>
          <span className="mt-1 block text-xs opacity-80">{r.httpStatus}</span>
        </Notice>
      )}
      {r && !r.ok && (
        <Notice tone="error" icon={CircleX} title="Noch kein gültiges Zertifikat">
          <span className="block break-words">{r.error}</span>
          <span className="mt-1 block text-xs opacity-80">
            Häufige Ursachen: Der Hostname zeigt per DNS noch nicht auf diesen Server, oder Port 80/443 ist von außen nicht erreichbar
            (Firewall beim VPS-Anbieter).
          </span>
        </Notice>
      )}
    </div>
  );
}

export function SelfSignedNotice() {
  return (
    <Notice tone="warning" icon={ShieldAlert} title="Nur für Tests geeignet">
      Mail-Programme zeigen bei einem selbstsignierten Zertifikat Warnungen, und viele Mailserver stufen die Verbindung als unsicher ein.
      Für den Betrieb das Zertifikat des Reverse-Proxys übernehmen oder Let’s Encrypt verwenden.
    </Notice>
  );
}

/** Key files of NPM/Caddy/certbot are root-only; Postwerk usually runs as PUID 99. */
export function PermissionHint({ className }: { className?: string }) {
  return (
    <Notice tone="warning" icon={UserCog} title="Privater Schlüssel nicht lesbar" className={className}>
      Postwerk läuft als Benutzer PUID (Unraid: 99) und kann den privaten Schlüssel nicht lesen. Lösung: im Template{" "}
      <code className="font-mono">PUID=0</code> setzen (Postwerk läuft dann als root im Container) – bei Traefik alternativ{" "}
      <code className="font-mono">traefik-certs-dumper</code> verwenden, das lesbare PEM-Dateien schreibt.
    </Notice>
  );
}

// ---------------------------------------------------------------------------
// Certificate files
// ---------------------------------------------------------------------------

export const tlsKeys = { certificates: ["admin", "tls-certificates"] as const };

const DAY = 24 * 60 * 60 * 1000;

function expiry(notAfter: string): { expired: boolean; soon: boolean } {
  const t = new Date(notAfter).getTime();
  if (!Number.isFinite(t)) return { expired: false, soon: false };
  const now = Date.now();
  return { expired: t < now, soon: t >= now && t - now < 14 * DAY };
}

function MatchBadge({ matches, hostname }: { matches: boolean; hostname: string }) {
  const host = hostname || "Hostname";
  return matches ? (
    <Badge tone="green" icon={CircleCheck}>
      passt zu {host}
    </Badge>
  ) : (
    <Badge tone="amber" icon={TriangleAlert}>
      passt nicht zu {host}
    </Badge>
  );
}

function ExpiryBadge({ notAfter }: { notAfter: string }) {
  const e = expiry(notAfter);
  if (e.expired) return <Badge tone="red">abgelaufen</Badge>;
  if (e.soon) return <Badge tone="amber">läuft bald ab</Badge>;
  return null;
}

function CertificateItem({
  cert,
  hostname,
  selected,
  onSelect,
}: {
  cert: CertificateFile;
  hostname: string;
  selected: boolean;
  onSelect: () => void;
}) {
  const unusable = !!cert.error;
  const [primary, ...others] = cert.names.length > 0 ? cert.names : [cert.certPath.split("/").pop() ?? cert.certPath];
  return (
    <button
      type="button"
      disabled={unusable}
      onClick={onSelect}
      aria-pressed={selected}
      className={cn(
        "flex w-full min-w-0 gap-3 rounded-xl border p-3 text-left transition",
        "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-indigo-500/50",
        unusable
          ? "cursor-not-allowed border-zinc-200 bg-zinc-50/70 dark:border-zinc-800 dark:bg-zinc-950/40"
          : selected
            ? "border-indigo-400 bg-indigo-50/60 ring-1 ring-indigo-400 dark:border-indigo-500/60 dark:bg-indigo-500/10 dark:ring-indigo-500/60"
            : "border-zinc-200 hover:border-zinc-300 hover:bg-zinc-50 dark:border-zinc-800 dark:hover:border-zinc-700 dark:hover:bg-zinc-800/50",
      )}
    >
      <span
        className={cn(
          "flex h-8 w-8 shrink-0 items-center justify-center rounded-lg",
          unusable
            ? "bg-zinc-100 text-zinc-400 dark:bg-zinc-800 dark:text-zinc-500"
            : selected
              ? "bg-indigo-600 text-white dark:bg-indigo-500"
              : "bg-zinc-100 text-zinc-500 dark:bg-zinc-800 dark:text-zinc-400",
        )}
      >
        <FileKey className="h-4 w-4" />
      </span>
      <span className={cn("min-w-0 flex-1 space-y-1", unusable && "opacity-70")}>
        <span className="flex flex-wrap items-center gap-1.5">
          <span className="mr-1 break-all text-sm font-medium text-zinc-900 dark:text-zinc-100">{primary}</span>
          <MatchBadge matches={cert.matchesHostname} hostname={hostname} />
          {cert.source === "traefik" && <Badge tone="purple">Traefik acme.json</Badge>}
          <ExpiryBadge notAfter={cert.notAfter} />
        </span>
        {others.length > 0 && <span className="block break-words text-xs text-zinc-600 dark:text-zinc-400">Weitere Namen: {others.join(", ")}</span>}
        <span className="block text-xs text-zinc-500 dark:text-zinc-400">
          {cert.issuer ? `${cert.issuer} · ` : ""}gültig bis {formatDate(cert.notAfter) || "–"}
        </span>
        <span className="block break-all font-mono text-[11px] text-zinc-400 dark:text-zinc-500">
          {cert.certPath}
          {cert.keyPath ? ` · ${cert.keyPath}` : ""}
        </span>
        {cert.error && <span className="block break-words text-xs font-medium text-red-600 dark:text-red-400">{cert.error}</span>}
      </span>
    </button>
  );
}

function MountHelp() {
  return (
    <ul className="list-disc space-y-1 pl-4 text-left">
      <li>
        <strong className="font-medium">Unraid:</strong> im Postwerk-Template das Feld „Zertifikate vom Reverse-Proxy“ auf den
        Zertifikatsordner des Proxys setzen.
      </li>
      <li>
        <strong className="font-medium">Docker Compose:</strong> ein Volume wie{" "}
        <code className="break-all font-mono">/pfad/zu/den/proxy-zertifikaten:/certs:ro</code> ergänzen.
      </li>
    </ul>
  );
}

function TestResultBox({ result, hostname, permissionHint }: { result: TlsTestResult; hostname: string; permissionHint: boolean }) {
  if (!result.ok) {
    return (
      <div className="space-y-3">
        <Notice tone="error" icon={CircleX} title="Zertifikat kann nicht verwendet werden">
          {result.error && <span className="break-words font-mono text-xs">{result.error}</span>}
        </Notice>
        {permissionHint && isPermissionError(result.error) && <PermissionHint />}
      </div>
    );
  }
  return (
    <Notice tone="success" icon={CircleCheck} title="Zertifikat und Schlüssel passen zusammen">
      <div className="space-y-1.5">
        <div className="flex flex-wrap items-center gap-1.5">
          <MatchBadge matches={result.matchesHostname} hostname={hostname} />
          {result.notAfter && <ExpiryBadge notAfter={result.notAfter} />}
        </div>
        {result.names.length > 0 && <div className="break-words text-xs">Namen: {result.names.join(", ")}</div>}
        {result.notAfter && <div className="text-xs">Gültig bis {formatDate(result.notAfter)}</div>}
      </div>
    </Notice>
  );
}

/**
 * Certificate selection for mode "files": certificates found under /certs,
 * manual paths and a test of the chosen pair.
 */
export function CertificateFilesPanel({
  value,
  onChange,
  hostname,
  footer,
}: {
  value: Settings["tls"]["files"];
  onChange: (files: Settings["tls"]["files"]) => void;
  hostname: string;
  footer?: ReactNode;
}) {
  const list = useQuery({ queryKey: tlsKeys.certificates, queryFn: adminApi.tlsCertificates, staleTime: 0, retry: false });
  const [result, setResult] = useState<TlsTestResult | null>(null);
  const test = useMutation({
    mutationFn: adminApi.testTlsFiles,
    onSuccess: setResult,
    onError: (err) => setResult({ ok: false, names: [], notAfter: "", matchesHostname: false, error: errorMessage(err) }),
  });

  const set = (patch: Partial<Settings["tls"]["files"]>) => {
    onChange({ ...value, ...patch });
    setResult(null);
  };

  const certs = list.data ?? [];
  // Unusable entries are almost always unreadable private keys (root-only files).
  const anyUnusable = certs.some((c) => !!c.error);
  const keyOptional = isAcmeJson(value.certPath);

  return (
    <div className="space-y-5">
      <div className="space-y-2.5">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h4 className="text-sm font-semibold text-zinc-900 dark:text-zinc-100">Gefundene Zertifikate</h4>
          <Button size="sm" variant="ghost" icon={RefreshCw} loading={list.isFetching} onClick={() => void list.refetch()}>
            Erneut suchen
          </Button>
        </div>
        {list.isLoading ? (
          <div className="space-y-2">
            <Skeleton className="h-20 rounded-xl" />
            <Skeleton className="h-20 rounded-xl" />
          </div>
        ) : list.error ? (
          <ErrorState error={list.error} onRetry={() => void list.refetch()} className="py-8" />
        ) : certs.length === 0 ? (
          <div className="rounded-xl border border-dashed border-zinc-300 p-4 dark:border-zinc-700">
            <div className="flex items-start gap-3">
              <span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-zinc-100 text-zinc-400 dark:bg-zinc-800 dark:text-zinc-500">
                <FolderSearch className="h-[18px] w-[18px]" />
              </span>
              <div className="min-w-0 space-y-2 text-sm text-zinc-600 dark:text-zinc-300">
                <p className="font-medium text-zinc-900 dark:text-zinc-100">
                  Unter <code className="font-mono">/certs</code> wurden keine Zertifikate gefunden
                </p>
                <p className="text-xs leading-relaxed text-zinc-500 dark:text-zinc-400">
                  Binde den Zertifikatsordner deines Reverse-Proxys <strong className="font-medium">schreibgeschützt</strong> nach{" "}
                  <code className="font-mono">/certs</code> in den Postwerk-Container ein und starte den Container neu:
                </p>
                <div className="text-xs leading-relaxed text-zinc-500 dark:text-zinc-400">
                  <MountHelp />
                </div>
                <Button size="sm" icon={RefreshCw} loading={list.isFetching} onClick={() => void list.refetch()}>
                  Erneut suchen
                </Button>
              </div>
            </div>
          </div>
        ) : (
          <div className="space-y-2">
            {certs.map((c, i) => (
              <CertificateItem
                key={`${c.certPath}#${i}`}
                cert={c}
                hostname={hostname}
                selected={value.certPath === c.certPath && value.keyPath === (c.keyPath ?? "")}
                onSelect={() => set({ certPath: c.certPath, keyPath: c.keyPath ?? "" })}
              />
            ))}
          </div>
        )}
        {anyUnusable && <PermissionHint />}
      </div>

      <div className="space-y-3">
        <h4 className="text-sm font-semibold text-zinc-900 dark:text-zinc-100">Pfade im Container</h4>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Zertifikat (Kette)" required help="PEM mit vollständiger Kette, z. B. fullchain.pem – oder eine Traefik-acme.json.">
            {(id) => (
              <Input
                id={id}
                value={value.certPath}
                onChange={(e) => set({ certPath: e.target.value })}
                placeholder="/certs/live/npm-1/fullchain.pem"
                spellCheck={false}
                className="font-mono text-[13px]"
              />
            )}
          </Field>
          <Field
            label="Privater Schlüssel"
            required={!keyOptional}
            help={keyOptional ? "Bei einer acme.json steckt der Schlüssel in der Datei – das Feld kann leer bleiben." : "z. B. privkey.pem"}
          >
            {(id) => (
              <Input
                id={id}
                value={value.keyPath}
                onChange={(e) => set({ keyPath: e.target.value })}
                placeholder={keyOptional ? "(in acme.json enthalten)" : "/certs/live/npm-1/privkey.pem"}
                spellCheck={false}
                className="font-mono text-[13px]"
              />
            )}
          </Field>
        </div>
        <div className="flex flex-wrap items-center gap-3">
          <Button
            icon={ScanSearch}
            loading={test.isPending}
            disabled={!value.certPath.trim()}
            onClick={() => test.mutate({ certPath: value.certPath.trim(), keyPath: value.keyPath.trim() })}
          >
            Prüfen
          </Button>
          <span className="text-xs text-zinc-500 dark:text-zinc-400">Liest Zertifikat und Schlüssel testweise, ohne etwas zu speichern.</span>
        </div>
        {/* The hint is shown once: above with the list, or here for the test. */}
        {result && <TestResultBox result={result} hostname={hostname} permissionHint={!anyUnusable} />}
      </div>

      <Notice tone="info" icon={RotateCw}>
        Postwerk lädt das Zertifikat automatisch neu, sobald der Proxy es erneuert – ohne Neustart.
      </Notice>
      {footer}
    </div>
  );
}
