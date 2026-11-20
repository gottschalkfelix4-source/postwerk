import { useState, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Activity,
  ChevronRight,
  CircleCheck,
  CircleX,
  ExternalLink,
  Globe,
  Info,
  KeyRound,
  ListOrdered,
  PenLine,
  Server,
  TriangleAlert,
  Zap,
} from "lucide-react";
import { adminApi } from "@/api/endpoints";
import type {
  DynDNSPreset,
  DynDNSPresetId,
  DynDNSStatus,
  RelayPreset,
  RelayPresetId,
  RelayTestResult,
  Settings,
  SettingsUpdate,
} from "@/api/types";
import { formatDateTime } from "@/lib/format";
import { cn, deepEqual } from "@/lib/utils";
import { Badge, ChipInput, Field, Input, Notice, PasswordInput, Segmented, Skeleton, Spinner } from "@/components/ui";
import { CheckRow, isHttpUrl } from "@/pages/admin/proxy/ProxyParts";

export const MASK = "••••";
const DEFAULT_IP_CHECK = "https://api.ipify.org";

export type DynDNSSettings = Settings["dyndns"];
export type RelaySettings = Settings["relay"];

// ---------------------------------------------------------------------------
// Data helpers
// ---------------------------------------------------------------------------

export const dynIpKeys = {
  dyndnsPresets: ["admin", "dyndns-presets"] as const,
  dyndnsStatus: ["admin", "dyndns-status"] as const,
  relayPresets: ["admin", "relay-presets"] as const,
};

export function useDynDNSPresets() {
  return useQuery({ queryKey: dynIpKeys.dyndnsPresets, queryFn: adminApi.dyndnsPresets, staleTime: 30 * 60_000 });
}

export function useRelayPresets() {
  return useQuery({ queryKey: dynIpKeys.relayPresets, queryFn: adminApi.relayPresets, staleTime: 5 * 60_000 });
}

/** GET /api/admin/dyndns/status – only meaningful once DynDNS is enabled and saved. */
export function useDynDNSStatus(enabled: boolean) {
  return useQuery({ queryKey: dynIpKeys.dyndnsStatus, queryFn: adminApi.dyndnsStatus, enabled, staleTime: 0, retry: false });
}

/** POST /api/admin/dyndns/update. `before` runs first, e.g. to save pending changes. */
export function useDynDNSUpdate(before?: () => Promise<unknown>) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async () => {
      if (before) await before();
      return adminApi.dyndnsUpdate();
    },
    onSuccess: async (s) => {
      // A refetch started by saving may still be running; it must not overwrite the fresh result.
      await qc.cancelQueries({ queryKey: dynIpKeys.dyndnsStatus });
      qc.setQueryData(dynIpKeys.dyndnsStatus, s);
    },
  });
}

/** POST /api/admin/relay/test. The endpoint checks the stored settings, so `before` can save first. */
export function useRelayTest(before?: () => Promise<unknown>) {
  return useMutation({
    mutationFn: async (): Promise<RelayTestResult> => {
      if (before) await before();
      return adminApi.testRelay();
    },
  });
}

/** Normalizes the `dyndns` section with a fixed key order (drafts are compared via JSON). */
export function dyndnsOf(d: Partial<DynDNSSettings> | undefined): DynDNSSettings {
  return {
    enabled: d?.enabled ?? false,
    preset: d?.preset ?? "custom",
    updateUrl: d?.updateUrl ?? "",
    hostnames: d?.hostnames ?? [],
    username: d?.username ?? "",
    password: d?.password ?? "",
    ipCheckUrl: d?.ipCheckUrl ?? DEFAULT_IP_CHECK,
    intervalMinutes: d?.intervalMinutes ?? 5,
  };
}

/** Normalizes the `relay` section with a fixed key order. */
export function relayOf(r: Partial<RelaySettings> | undefined): RelaySettings {
  return {
    enabled: r?.enabled ?? false,
    preset: r?.preset ?? "custom",
    host: r?.host ?? "",
    port: r?.port ?? 587,
    security: r?.security ?? "starttls",
    username: r?.username ?? "",
    password: r?.password ?? "",
    spfInclude: r?.spfInclude ?? "",
  };
}

const CUSTOM_DYNDNS: DynDNSPreset = {
  id: "custom",
  name: "Eigener Dienst",
  updateUrl: "",
  usernameHint: "",
  passwordHint: "",
  docsUrl: null,
  notes: null,
};

const CUSTOM_RELAY: RelayPreset = {
  id: "custom",
  name: "Eigener Server",
  host: "",
  port: 587,
  security: "starttls",
  usernameHint: "",
  spfInclude: "",
  docsUrl: null,
  notes: null,
};

/** STRATO first, "custom" last; a custom entry is added when the server does not list one. */
function presetList<P extends { id: string }>(presets: P[] | undefined, custom: P): P[] {
  const list = presets ?? [];
  const all = list.some((p) => p.id === "custom") ? list : [...list, custom];
  const rank = (id: string) => (id === "strato" ? 0 : id === "custom" ? 2 : 1);
  return [...all].sort((a, b) => rank(a.id) - rank(b.id));
}

export const dyndnsPresetList = (p: DynDNSPreset[] | undefined) => presetList(p, CUSTOM_DYNDNS);
export const relayPresetList = (p: RelayPreset[] | undefined) => presetList(p, CUSTOM_RELAY);

export function applyDynDNSPreset(d: DynDNSSettings, p: DynDNSPreset): DynDNSSettings {
  if (p.id === "custom") return { ...d, preset: "custom", updateUrl: p.updateUrl || (d.preset === "custom" ? d.updateUrl : "") };
  return { ...d, preset: p.id as DynDNSPresetId, updateUrl: p.updateUrl };
}

export function applyRelayPreset(r: RelaySettings, p: RelayPreset): RelaySettings {
  if (p.id === "custom") return { ...r, preset: "custom" };
  return { ...r, preset: p.id as RelayPresetId, host: p.host, port: p.port, security: p.security, spfInclude: p.spfInclude };
}

/** Switches DynDNS on and fills empty fields: host name, STRATO as provider. */
export function withDynDNSSuggestions(d: DynDNSSettings, hostname: string, presets: DynDNSPreset[] | undefined): DynDNSSettings {
  let next: DynDNSSettings = { ...d, enabled: true };
  if (next.hostnames.length === 0 && hostname) next.hostnames = [hostname];
  if (!next.updateUrl) {
    const list = dyndnsPresetList(presets);
    const p = list.find((x) => x.id === "strato") ?? list.find((x) => x.id !== "custom");
    if (p) next = applyDynDNSPreset(next, p);
    // STRATO expects the (sub)domain that is updated as user name.
    if (next.preset === "strato" && !next.username && hostname) next.username = hostname;
  }
  return next;
}

/** Switches the smarthost on and preselects STRATO when no server is set yet. */
export function withRelaySuggestions(r: RelaySettings, presets: RelayPreset[] | undefined): RelaySettings {
  let next: RelaySettings = { ...r, enabled: true };
  if (!next.host) {
    const list = relayPresetList(presets);
    const p = list.find((x) => x.id === "strato") ?? list.find((x) => x.id !== "custom");
    if (p) next = applyRelayPreset(next, p);
  }
  return next;
}

const HOST_RE = /^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9-]{2,63}$/i;

export function hostnameError(entry: string): string | null {
  return HOST_RE.test(entry) ? null : "Bitte einen vollständigen Hostnamen angeben, z. B. mail.beispiel.de.";
}

export function dyndnsErrors(d: DynDNSSettings) {
  return {
    hostnames: d.hostnames.length === 0,
    username: !d.username.trim(),
    password: !d.password,
    updateUrl: d.preset === "custom" && !isHttpUrl(d.updateUrl),
    ipCheckUrl: !!d.ipCheckUrl.trim() && !isHttpUrl(d.ipCheckUrl),
    interval: !Number.isInteger(d.intervalMinutes) || d.intervalMinutes < 1,
  };
}

export function dyndnsValid(d: DynDNSSettings): boolean {
  return !d.enabled || !Object.values(dyndnsErrors(d)).some(Boolean);
}

export function relayErrors(r: RelaySettings) {
  return {
    host: !r.host.trim(),
    port: !Number.isInteger(r.port) || r.port < 1 || r.port > 65535,
  };
}

export function relayValid(r: RelaySettings): boolean {
  return !r.enabled || !Object.values(relayErrors(r)).some(Boolean);
}

/**
 * Payload for PUT /api/admin/settings, or null when nothing changed. Switching
 * off only sends `enabled: false`; an unchanged (masked) password is left out.
 */
export function dyndnsChange(draft: DynDNSSettings, saved: DynDNSSettings): SettingsUpdate["dyndns"] | null {
  if (!draft.enabled) return saved.enabled ? { enabled: false } : null;
  if (deepEqual(draft, saved)) return null;
  const payload: SettingsUpdate["dyndns"] = { ...draft };
  if (draft.password === saved.password) delete payload.password;
  return payload;
}

export function relayChange(draft: RelaySettings, saved: RelaySettings): SettingsUpdate["relay"] | null {
  if (!draft.enabled) return saved.enabled ? { enabled: false } : null;
  if (deepEqual(draft, saved)) return null;
  const payload: SettingsUpdate["relay"] = { ...draft };
  if (draft.password === saved.password) delete payload.password;
  return payload;
}

/** Fallback texts for presets whose hints are empty. */
const dyndnsHints: Record<string, { username: string; password: string }> = {
  strato: {
    username: "Die Domain bzw. Subdomain, z. B. mail.deinedomain.de",
    password: "DynDNS-Passwort aus dem STRATO-Login → Sicherheit → Passwörter festlegen",
  },
};

// ---------------------------------------------------------------------------
// Small building blocks
// ---------------------------------------------------------------------------

/** Password-style input for masked secrets: shows a hint when a value is stored. */
export function MaskedSecretInput({
  id,
  value,
  onChange,
  placeholder,
  invalid,
  disabled,
}: {
  id?: string;
  value: string;
  onChange: (v: string) => void;
  placeholder?: string;
  invalid?: boolean;
  disabled?: boolean;
}) {
  const masked = value === MASK;
  return (
    <PasswordInput
      id={id}
      value={masked ? "" : value}
      placeholder={masked ? "•••••••• (gespeichert)" : placeholder}
      autoComplete="new-password"
      invalid={invalid}
      disabled={disabled}
      onChange={(e) => onChange(e.target.value === "" && masked ? MASK : e.target.value)}
    />
  );
}

/** Collapsible group for rarely needed fields. */
export function Disclosure({ title, children, defaultOpen = false }: { title: string; children: ReactNode; defaultOpen?: boolean }) {
  const [open, setOpen] = useState(defaultOpen);
  return (
    <div>
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
        className="inline-flex items-center gap-1 rounded-md text-sm font-medium text-zinc-600 hover:text-zinc-900 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-indigo-500/50 dark:text-zinc-400 dark:hover:text-zinc-100"
      >
        <ChevronRight className={cn("h-4 w-4 transition-transform", open && "rotate-90")} />
        {title}
      </button>
      {open && <div className="animate-fade-in mt-3 space-y-4">{children}</div>}
    </div>
  );
}

function initialsOf(name: string): string {
  const words = name.replace(/[^\p{L}\p{N} ]/gu, " ").split(/\s+/).filter(Boolean);
  if (words.length === 0) return "?";
  if (words.length === 1) return words[0].slice(0, 2).toUpperCase();
  return (words[0][0] + words[1][0]).toUpperCase();
}

function hostOf(url: string): string {
  try {
    return new URL(url).host;
  } catch {
    return url;
  }
}

/** Provider tiles; STRATO comes first and is highlighted. */
function PresetTiles<P extends { id: string; name: string }>({
  presets,
  value,
  onChange,
  detail,
  compact,
  disabled,
  label,
}: {
  presets: P[];
  value: string;
  onChange: (p: P) => void;
  detail: (p: P) => string;
  compact?: boolean;
  disabled?: boolean;
  label: string;
}) {
  return (
    <div
      className={cn("grid gap-2.5", compact ? "grid-cols-2 sm:grid-cols-3" : "sm:grid-cols-2 lg:grid-cols-3", disabled && "pointer-events-none opacity-60")}
      role="radiogroup"
      aria-label={label}
    >
      {presets.map((p) => {
        const active = p.id === value;
        const strato = p.id === "strato";
        const custom = p.id === "custom";
        const text = detail(p);
        return (
          <button
            key={p.id}
            type="button"
            role="radio"
            aria-checked={active}
            disabled={disabled}
            onClick={() => onChange(p)}
            className={cn(
              "flex min-w-0 flex-col gap-2 rounded-xl border p-3 text-left transition",
              "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-indigo-500/50",
              active
                ? "border-indigo-400 bg-indigo-50/60 ring-1 ring-indigo-400 dark:border-indigo-500/60 dark:bg-indigo-500/10 dark:ring-indigo-500/60"
                : strato
                  ? "border-indigo-200 bg-indigo-50/20 hover:border-indigo-300 hover:bg-indigo-50/50 dark:border-indigo-500/30 dark:bg-indigo-500/[0.04] dark:hover:bg-indigo-500/[0.08]"
                  : "border-zinc-200 hover:border-zinc-300 hover:bg-zinc-50 dark:border-zinc-800 dark:hover:border-zinc-700 dark:hover:bg-zinc-800/50",
            )}
          >
            <span className="flex min-w-0 items-center gap-2.5">
              <span
                className={cn(
                  "flex h-8 w-8 shrink-0 items-center justify-center rounded-lg text-xs font-bold",
                  strato
                    ? "bg-indigo-600 text-white dark:bg-indigo-500"
                    : custom
                      ? "bg-zinc-100 text-zinc-500 dark:bg-zinc-800 dark:text-zinc-400"
                      : "bg-zinc-100 text-zinc-600 dark:bg-zinc-800 dark:text-zinc-300",
                )}
                aria-hidden
              >
                {custom ? <PenLine className="h-4 w-4" /> : initialsOf(p.name)}
              </span>
              <span className="min-w-0 flex-1 truncate text-sm font-medium text-zinc-900 dark:text-zinc-100" title={p.name}>
                {p.name}
              </span>
              {strato && !compact && <Badge tone="indigo">Empfohlen</Badge>}
            </span>
            {!compact && text && <span className={cn("truncate text-xs text-zinc-500 dark:text-zinc-400", !custom && "font-mono")}>{text}</span>}
          </button>
        );
      })}
    </div>
  );
}

function PresetMeta({ notes, docsUrl }: { notes: string | null; docsUrl: string | null }) {
  if (!notes && !docsUrl) return null;
  return (
    <div className="flex items-start gap-2 text-xs leading-relaxed text-zinc-500 dark:text-zinc-400">
      <Info className="mt-px h-3.5 w-3.5 shrink-0" />
      <span>
        {notes}
        {docsUrl && (
          <>
            {notes ? " " : ""}
            <a
              href={docsUrl}
              target="_blank"
              rel="noreferrer noopener"
              className="inline-flex items-center gap-1 font-medium text-indigo-600 hover:underline dark:text-indigo-300"
            >
              Anleitung des Anbieters <ExternalLink className="h-3 w-3" />
            </a>
          </>
        )}
      </span>
    </div>
  );
}

function NumberField({
  id,
  value,
  onChange,
  min,
  max,
  suffix,
  invalid,
  disabled,
  className,
}: {
  id?: string;
  value: number;
  onChange: (v: number) => void;
  min?: number;
  max?: number;
  suffix?: string;
  invalid?: boolean;
  disabled?: boolean;
  className?: string;
}) {
  return (
    <div className="flex items-center gap-2">
      <Input
        id={id}
        type="number"
        value={Number.isFinite(value) ? String(value) : ""}
        min={min}
        max={max}
        onChange={(e) => onChange(e.target.value === "" ? 0 : Number(e.target.value))}
        invalid={invalid}
        disabled={disabled}
        className={cn("w-28 tabular-nums", className)}
      />
      {suffix && <span className="text-sm text-zinc-500 dark:text-zinc-400">{suffix}</span>}
    </div>
  );
}

// ---------------------------------------------------------------------------
// DynDNS
// ---------------------------------------------------------------------------

/** Provider, host names, credentials and advanced options of the DynDNS client. */
export function DynDNSFields({
  value,
  onChange,
  presets,
  presetsLoading,
  hostname,
  showErrors,
  compact,
  advanced,
  afterPresets,
}: {
  value: DynDNSSettings;
  onChange: (v: DynDNSSettings) => void;
  presets: DynDNSPreset[] | undefined;
  presetsLoading?: boolean;
  hostname: string;
  showErrors?: boolean;
  compact?: boolean;
  /** Where the advanced fields go: a collapsible group (default) or the caller renders them via `DynDNSAdvancedFields`. */
  advanced?: "disclosure" | "none";
  /** Content shown right below the provider tiles, e.g. preparation steps. */
  afterPresets?: ReactNode;
}) {
  const list = dyndnsPresetList(presets);
  const preset = list.find((p) => p.id === value.preset);
  const errors = dyndnsErrors(value);
  const set = (patch: Partial<DynDNSSettings>) => onChange({ ...value, ...patch });
  const usernameHint = preset?.usernameHint || dyndnsHints[value.preset]?.username || "Benutzername beim DynDNS-Anbieter.";
  const passwordHint = preset?.passwordHint || dyndnsHints[value.preset]?.password || "Passwort bzw. Token für die DynDNS-Aktualisierung.";
  const err = (flag: boolean) => showErrors && flag;

  return (
    <div className="space-y-5">
      <div className="space-y-2">
        <div className="text-sm font-medium text-zinc-800 dark:text-zinc-200">Anbieter</div>
        {presetsLoading ? (
          <Skeleton className="h-20 rounded-xl" />
        ) : (
          <PresetTiles
            presets={list}
            value={value.preset}
            onChange={(p) => onChange(applyDynDNSPreset(value, p))}
            detail={(p) => (p.id === "custom" ? "Eigene dyndns2-Update-URL" : hostOf(p.updateUrl))}
            compact={compact}
            label="DynDNS-Anbieter"
          />
        )}
        {preset && <PresetMeta notes={preset.notes} docsUrl={preset.docsUrl} />}
      </div>

      {afterPresets}

      {value.preset === "custom" && (
        <Field
          label="Update-URL"
          required
          help={
            <>
              dyndns2-Endpunkt des Anbieters, z. B. <code className="font-mono">https://dyndns.beispiel.de/nic/update</code>
            </>
          }
          error={err(errors.updateUrl) ? "Bitte eine vollständige Adresse mit http:// oder https:// angeben." : null}
        >
          {(id) => (
            <Input
              id={id}
              value={value.updateUrl}
              onChange={(e) => set({ updateUrl: e.target.value })}
              placeholder="https://dyndns.beispiel.de/nic/update"
              invalid={!!err(errors.updateUrl)}
              spellCheck={false}
              className="font-mono text-[13px]"
            />
          )}
        </Field>
      )}

      <Field
        label="Hostnamen"
        required
        help="Diese Namen zeigen per A-Record auf deine aktuelle IP. Weitere mit Enter hinzufügen."
        error={err(errors.hostnames) ? "Mindestens ein Hostname ist erforderlich." : null}
      >
        {(id) => (
          <ChipInput
            id={id}
            value={value.hostnames}
            onChange={(v) => set({ hostnames: v })}
            placeholder={hostname || "mail.beispiel.de"}
            validate={hostnameError}
            transform={(v) => v.trim().toLowerCase().replace(/\.$/, "")}
            suggestions={hostname ? [hostname] : undefined}
          />
        )}
      </Field>

      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Benutzername" required help={usernameHint} error={err(errors.username) ? "Bitte den Benutzernamen angeben." : null}>
          {(id) => (
            <Input
              id={id}
              value={value.username}
              onChange={(e) => set({ username: e.target.value })}
              placeholder={value.preset === "strato" ? hostname || "mail.deinedomain.de" : undefined}
              autoComplete="off"
              spellCheck={false}
              invalid={!!err(errors.username)}
            />
          )}
        </Field>
        <Field label="Passwort" required help={passwordHint} error={err(errors.password) ? "Bitte das DynDNS-Passwort angeben." : null}>
          {(id) => <MaskedSecretInput id={id} value={value.password} onChange={(v) => set({ password: v })} invalid={!!err(errors.password)} />}
        </Field>
      </div>

      {advanced !== "none" && (
        <Disclosure title="Erweitert" defaultOpen={errors.ipCheckUrl || errors.interval}>
          <DynDNSAdvancedFields value={value} onChange={onChange} showErrors />
        </Disclosure>
      )}
    </div>
  );
}

/** IP check service and interval. */
export function DynDNSAdvancedFields({
  value,
  onChange,
  showErrors,
}: {
  value: DynDNSSettings;
  onChange: (v: DynDNSSettings) => void;
  showErrors?: boolean;
}) {
  const errors = dyndnsErrors(value);
  return (
    <div className="grid gap-4 sm:grid-cols-[minmax(0,1fr)_auto]">
      <Field
        label="IP-Prüfdienst"
        help="Liefert die öffentliche IPv4-Adresse als Text. Leer = Standard."
        error={showErrors && errors.ipCheckUrl ? "Bitte eine vollständige Adresse mit http:// oder https:// angeben." : null}
      >
        {(id) => (
          <Input
            id={id}
            value={value.ipCheckUrl}
            onChange={(e) => onChange({ ...value, ipCheckUrl: e.target.value })}
            placeholder={DEFAULT_IP_CHECK}
            invalid={!!showErrors && errors.ipCheckUrl}
            spellCheck={false}
            className="font-mono text-[13px]"
          />
        )}
      </Field>
      <Field label="Prüfintervall" help="Mindestens 1 Minute." error={showErrors && errors.interval ? "Mindestens 1 Minute." : null}>
        {(id) => (
          <NumberField
            id={id}
            value={value.intervalMinutes}
            min={1}
            suffix="Minuten"
            invalid={!!showErrors && errors.interval}
            onChange={(v) => onChange({ ...value, intervalMinutes: v })}
          />
        )}
      </Field>
    </div>
  );
}

/** Numbered preparation steps in the STRATO customer login. */
export function StratoPrepNotice({ hostname, domain }: { hostname: string; domain: string }) {
  const d = domain || "deinedomain.de";
  const sub = hostname && hostname.split(".").length >= 3 ? hostname : `mail.${d}`;
  const steps: ReactNode[] = [
    <>
      Im STRATO-Login die Subdomain <code className="font-mono">{sub}</code> anlegen.
    </>,
    <>Für diese Subdomain DynDNS aktivieren und das DynDNS-Passwort setzen.</>,
    <>
      Den MX-Eintrag der Domain <code className="font-mono">{d}</code> auf <code className="font-mono">{sub}</code> stellen.
    </>,
  ];
  return (
    <div className="rounded-xl border border-indigo-200 bg-indigo-50/40 p-4 dark:border-indigo-500/30 dark:bg-indigo-500/[0.05]">
      <h4 className="flex items-center gap-2 text-sm font-semibold text-zinc-900 dark:text-zinc-100">
        <ListOrdered className="h-4 w-4 text-indigo-500" /> Vorbereitung bei STRATO
      </h4>
      <ol className="mt-3 space-y-2.5">
        {steps.map((s, i) => (
          <li key={i} className="flex gap-3">
            <span className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-indigo-50 text-xs font-semibold text-indigo-700 ring-1 ring-inset ring-indigo-200 dark:bg-indigo-500/15 dark:text-indigo-300 dark:ring-indigo-500/30">
              {i + 1}
            </span>
            <p className="min-w-0 pt-0.5 text-sm leading-relaxed text-zinc-700 dark:text-zinc-300">{s}</p>
          </li>
        ))}
      </ol>
    </div>
  );
}

/** DynDNS in the router is an equally good alternative. */
export const routerDynDNSHint = "Wer DynDNS schon in der FRITZ!Box (oder einem anderen Router) eingerichtet hat, lässt es hier aus.";

/** Live status of the DynDNS client. */
export function DynDNSStatusPanel({
  status,
  loading,
  error,
  note,
}: {
  status: DynDNSStatus | undefined;
  loading?: boolean;
  error?: unknown;
  note?: ReactNode;
}) {
  const s = status;
  const hosts = s ? Object.entries(s.dnsIps ?? {}) : [];
  return (
    <div className="rounded-xl border border-zinc-200 p-4 dark:border-zinc-800">
      <div className="flex items-center gap-2">
        <h4 className="text-sm font-semibold text-zinc-900 dark:text-zinc-100">Status</h4>
        {loading && <Spinner className="h-3.5 w-3.5 text-zinc-400" />}
        {s && !s.enabled && <Badge>Deaktiviert</Badge>}
      </div>
      {note && (
        <p className="mt-1 flex items-start gap-1.5 text-xs text-zinc-500 dark:text-zinc-400">
          <Info className="mt-px h-3.5 w-3.5 shrink-0" />
          {note}
        </p>
      )}
      {!s ? (
        error ? (
          <p className="mt-3 text-sm text-red-600 dark:text-red-400">Der Status konnte nicht abgerufen werden.</p>
        ) : (
          <Skeleton className="mt-3 h-24 rounded-lg" />
        )
      ) : (
        <div className="mt-2 space-y-3">
          <div className="divide-y divide-zinc-100 dark:divide-zinc-800">
            <CheckRow icon={Globe} label="Aktuelle öffentliche IP" state="neutral">
              {s.currentIp ? (
                <code className="font-mono text-zinc-700 dark:text-zinc-300">{s.currentIp}</code>
              ) : (
                "Noch nicht ermittelt."
              )}
            </CheckRow>
            {hosts.map(([host, ips]) => {
              const ok = !!s.currentIp && ips.includes(s.currentIp);
              return (
                <CheckRow key={host} icon={Server} label={`A-Record ${host}`} state={ok ? "ok" : ips.length === 0 ? "bad" : "warn"}>
                  {ips.length === 0 ? (
                    "Kein A-Record gefunden."
                  ) : (
                    <>
                      <code className={cn("font-mono", ok ? "text-emerald-600 dark:text-emerald-400" : "text-zinc-700 dark:text-zinc-300")}>
                        {ips.join(", ")}
                      </code>
                      {s.currentIp && (ok ? " – entspricht der aktuellen IP." : " – weicht von der aktuellen IP ab.")}
                    </>
                  )}
                </CheckRow>
              );
            })}
            <CheckRow icon={Activity} label="Letzte Antwort des Dienstes" state="neutral">
              {s.lastResult ? <code className="break-all font-mono text-zinc-700 dark:text-zinc-300">{s.lastResult}</code> : "Noch keine Meldung."}
              {s.lastUpdate && <span className="block">Zuletzt erfolgreich gemeldet: {formatDateTime(s.lastUpdate)}</span>}
              {s.nextCheck && <span className="block">Nächste Prüfung: {formatDateTime(s.nextCheck)}</span>}
            </CheckRow>
          </div>
          {s.error && (
            <Notice tone="error" icon={CircleX} title="Fehler bei der Aktualisierung">
              <span className="break-words">{s.error}</span>
            </Notice>
          )}
          {s.pausedUntil && (
            <Notice tone="warning" icon={TriangleAlert} title={`Pausiert bis ${formatDateTime(s.pausedUntil)}`}>
              Der Dienst hat die Meldung abgelehnt („badauth“ oder „abuse“). Damit der Zugang nicht gesperrt wird, meldet Postwerk bis dahin
              nichts. Prüfe Benutzername und Passwort.
            </Notice>
          )}
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Smarthost
// ---------------------------------------------------------------------------

export function RelayEnvNotice() {
  return (
    <Notice tone="warning" icon={KeyRound} title="Über Umgebungsvariablen festgelegt">
      Der Smarthost wird über die Umgebungsvariablen <code className="font-mono">POSTWERK_RELAY_*</code> des Containers konfiguriert. Die Werte
      sind hier nur lesbar – ändere sie in der Container-Konfiguration.
    </Notice>
  );
}

/** Provider, server, security and credentials of the smarthost. */
export function RelayFields({
  value,
  onChange,
  presets,
  presetsLoading,
  readOnly,
  showErrors,
  compact,
}: {
  value: RelaySettings;
  onChange: (v: RelaySettings) => void;
  presets: RelayPreset[] | undefined;
  presetsLoading?: boolean;
  readOnly?: boolean;
  showErrors?: boolean;
  compact?: boolean;
}) {
  const list = relayPresetList(presets);
  const preset = list.find((p) => p.id === value.preset);
  const errors = relayErrors(value);
  const set = (patch: Partial<RelaySettings>) => onChange({ ...value, ...patch });
  const err = (flag: boolean) => !!showErrors && flag;
  const usernameHint =
    preset?.usernameHint || (value.preset === "strato" ? "Die vollständige E-Mail-Adresse des STRATO-Postfachs." : "Benutzername für die SMTP-Anmeldung.");

  return (
    <div className="space-y-5">
      <div className="space-y-2">
        <div className="text-sm font-medium text-zinc-800 dark:text-zinc-200">Anbieter</div>
        {presetsLoading ? (
          <Skeleton className="h-20 rounded-xl" />
        ) : (
          <PresetTiles
            presets={list}
            value={value.preset}
            onChange={(p) => onChange(applyRelayPreset(value, p))}
            detail={(p) => (p.id === "custom" ? "Eigener SMTP-Server" : `${p.host}:${p.port}`)}
            compact={compact}
            disabled={readOnly}
            label="Smarthost-Anbieter"
          />
        )}
        {preset && <PresetMeta notes={preset.notes} docsUrl={preset.docsUrl} />}
      </div>


      <div className="grid gap-4 sm:grid-cols-[minmax(0,1fr)_auto]">
        <Field label="Server" required error={err(errors.host) ? "Bitte den SMTP-Server angeben." : null}>
          {(id) => (
            <Input
              id={id}
              value={value.host}
              onChange={(e) => set({ host: e.target.value.trim() })}
              placeholder="smtp.beispiel.de"
              disabled={readOnly}
              invalid={err(errors.host)}
              spellCheck={false}
              className="font-mono text-[13px]"
            />
          )}
        </Field>
        <Field label="Port" required error={err(errors.port) ? "1–65535" : null}>
          {(id) => (
            <NumberField id={id} value={value.port} min={1} max={65535} onChange={(v) => set({ port: v })} invalid={err(errors.port)} disabled={readOnly} className="w-24" />
          )}
        </Field>
      </div>

      <Field label="Sicherheit" help="TLS verschlüsselt von Anfang an (meist Port 465), STARTTLS schaltet die Verschlüsselung nach dem Verbindungsaufbau zu (meist Port 587).">
        {() => (
          <Segmented
            items={[
              { value: "tls", label: "TLS" },
              { value: "starttls", label: "STARTTLS" },
            ]}
            value={value.security}
            onChange={(v) => set({ security: v })}
            disabled={readOnly}
            className="w-56"
          />
        )}
      </Field>

      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Benutzer" help={usernameHint}>
          {(id) => (
            <Input
              id={id}
              value={value.username}
              onChange={(e) => set({ username: e.target.value })}
              placeholder={value.preset === "strato" ? "name@deinedomain.de" : undefined}
              autoComplete="off"
              spellCheck={false}
              disabled={readOnly}
            />
          )}
        </Field>
        <Field label="Passwort">
          {(id) => <MaskedSecretInput id={id} value={value.password} onChange={(v) => set({ password: v })} disabled={readOnly} />}
        </Field>
      </div>

      {value.preset === "custom" && !readOnly ? (
        <Field
          label="SPF-Ergänzung"
          help={
            <>
              Optional, z. B. <code className="font-mono">include:_spf.beispiel.de</code> – wird in den SPF-Eintrag aufgenommen.
            </>
          }
        >
          {(id) => (
            <Input
              id={id}
              value={value.spfInclude}
              onChange={(e) => set({ spfInclude: e.target.value })}
              placeholder="include:_spf.beispiel.de"
              spellCheck={false}
              className="font-mono text-[13px]"
            />
          )}
        </Field>
      ) : value.spfInclude ? (
        <p className="flex items-start gap-2 text-xs leading-relaxed text-zinc-500 dark:text-zinc-400">
          <Info className="mt-px h-3.5 w-3.5 shrink-0" />
          <span>
            <code className="font-mono text-zinc-700 dark:text-zinc-300">{value.spfInclude}</code> wird in den SPF-Eintrag aufgenommen.
          </span>
        </p>
      ) : null}
    </div>
  );
}

/** Result of POST /api/admin/relay/test. */
export function RelayTestPanel({ result, pending }: { result: RelayTestResult | null; pending?: boolean }) {
  if (pending) {
    return (
      <div className="flex items-center gap-3 rounded-lg border border-zinc-200 bg-zinc-50 p-3 text-sm text-zinc-600 dark:border-zinc-800 dark:bg-zinc-950/50 dark:text-zinc-400">
        <Zap className="h-4 w-4 animate-pulse text-indigo-500" /> Verbindung wird getestet …
      </div>
    );
  }
  if (!result) return null;
  return (
    <Notice
      tone={result.ok ? "success" : "error"}
      icon={result.ok ? CircleCheck : CircleX}
      title={result.ok ? "Verbindung erfolgreich" : "Verbindung fehlgeschlagen"}
    >
      {result.detail && <span className="break-words font-mono text-xs">{result.detail}</span>}
    </Notice>
  );
}

/** What a dynamic IP means for reverse DNS and incoming mail. */
export function DynamicIpNotice() {
  return (
    <Notice tone="info" icon={Info} title="Reverse-DNS und eingehende Mails">
      Einen Reverse-DNS-Eintrag (PTR) gibt es für dynamische IP-Adressen nicht – mit Smarthost ist das unkritisch, weil ausgehende Mails über
      dessen Server laufen. Eingehende Mails funktionieren normal, solange Port 25 im Router an Postwerk weitergeleitet ist.
    </Notice>
  );
}
