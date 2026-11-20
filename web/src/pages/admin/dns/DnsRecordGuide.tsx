import { useMemo, useState, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronRight, CircleAlert, Info, MapPin, RefreshCw, TriangleAlert, Wand2 } from "lucide-react";
import { adminApi } from "@/api/endpoints";
import type { DnsCheck, DnsRecord, SystemCheck } from "@/api/types";
import { cn, storageGet, storageSet } from "@/lib/utils";
import {
  Badge,
  Button,
  CopyButton,
  EmptyState,
  ErrorState,
  Notice,
  Segmented,
  Skeleton,
  Switch,
  TBody,
  THead,
  Table,
  Td,
  Th,
  Tr,
  type BadgeTone,
} from "@/components/ui";
import { setupKeys } from "@/pages/setup/context";
import { DnsAutoSetup } from "./DnsAutoSetup";

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

export interface DnsRecordGuideProps {
  domain: string;
  /** Check results from the caller. Without them the guide offers its own "Jetzt prüfen" button. */
  check?: DnsCheck[];
  /** The caller is (re)loading `check`. */
  checking?: boolean;
  /** Tighter spacing, e.g. inside the setup wizard. */
  compact?: boolean;
}

/**
 * Recommended DNS records of a domain, explained for people without DNS
 * knowledge: one card per record with the form fields named exactly like the
 * chosen DNS provider names them.
 */
export function DnsRecordGuide({ domain, check, checking, compact }: DnsRecordGuideProps) {
  const qc = useQueryClient();
  const [provider, setProvider] = useProvider();
  const [autoOpen, setAutoOpen] = useState(false);
  const [showOptional, setShowOptional] = useState(false);
  const [expert, setExpert] = useState(false);
  const [checkRequested, setCheckRequested] = useState(false);

  const records = useQuery({
    queryKey: ["admin", "domain-dns", domain],
    queryFn: () => adminApi.domainDns(domain),
    enabled: !!domain,
  });

  const external = check !== undefined;
  // Disabled until requested, but still shows a result that is already cached
  // (e.g. from the automatic setup dialog).
  const ownCheck = useQuery({
    queryKey: setupKeys.dnsCheck(domain),
    queryFn: () => adminApi.domainDnsCheck(domain),
    enabled: !external && checkRequested && !!domain,
    staleTime: 0,
  });
  const checks = external ? check : ownCheck.data;
  const isChecking = external ? !!checking : ownCheck.isFetching;

  const lookup = useMemo(() => checkLookup(checks), [checks]);
  const list = records.data ?? [];
  const required = list.filter((r) => r.required);
  const optional = list.filter((r) => !r.required);
  const requiredOk = required.filter((r) => lookup(r)?.status === "ok").length;
  const requiredChecked = required.some((r) => lookup(r));

  const defaultIpv4 =
    qc.getQueryData<SystemCheck>(setupKeys.systemCheck)?.hostnameIps.find((ip) => /^\d{1,3}(\.\d{1,3}){3}$/.test(ip)) ?? null;

  const runCheck = () => {
    if (checkRequested) void ownCheck.refetch();
    else setCheckRequested(true);
  };

  return (
    <div className={cn(compact ? "space-y-4" : "space-y-5")}>
      {/* Head: provider choice */}
      <div className="space-y-3">
        <p className="text-sm text-zinc-700 dark:text-zinc-300">
          Trage diese Einträge bei deinem DNS-Anbieter ein (dort, wo du die Domain gekauft hast).
        </p>
        <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
          <div className="flex w-full flex-col gap-1.5 sm:w-auto sm:flex-row sm:items-center sm:gap-2.5">
            <span className="text-xs font-medium text-zinc-500 dark:text-zinc-400">Dein Anbieter</span>
            <Segmented
              items={providerItems}
              value={provider}
              onChange={setProvider}
              className="w-full sm:w-auto [&>button]:px-1.5 sm:[&>button]:px-3"
            />
          </div>
          {/* The setup wizard shows its own prominent auto-setup card. */}
          {!compact && (
            <button
              type="button"
              onClick={() => setAutoOpen(true)}
              disabled={!domain}
              className="inline-flex items-center gap-1.5 text-sm font-medium text-indigo-600 hover:underline disabled:opacity-50 dark:text-indigo-300"
            >
              <Wand2 className="h-3.5 w-3.5" />
              Lieber automatisch eintragen?
            </button>
          )}
          {!external && (
            <Button size="sm" icon={RefreshCw} onClick={runCheck} loading={isChecking} disabled={!domain} className="sm:ml-auto">
              {checks ? "Erneut prüfen" : "Jetzt prüfen"}
            </Button>
          )}
        </div>
        {providerHints[provider] && (
          <p className="flex items-start gap-1.5 text-xs leading-relaxed text-zinc-500 dark:text-zinc-400">
            <Info className="mt-px h-3.5 w-3.5 shrink-0" />
            <span>{providerHints[provider]}</span>
          </p>
        )}
        {!external && ownCheck.error && (
          <Notice tone="error" icon={CircleAlert} title="Prüfung fehlgeschlagen">
            {ownCheck.error instanceof Error ? ownCheck.error.message : "Unbekannter Fehler."}
          </Notice>
        )}
      </div>

      {records.isLoading ? (
        <div className="space-y-3">
          <Skeleton className="h-40 rounded-xl" />
          <Skeleton className="h-40 rounded-xl" />
          <Skeleton className="h-32 rounded-xl" />
        </div>
      ) : records.error ? (
        <ErrorState error={records.error} onRetry={() => void records.refetch()} />
      ) : list.length === 0 ? (
        <EmptyState compact title="Keine Einträge" />
      ) : (
        <>
          <section className="space-y-3">
            <div className="flex flex-wrap items-center gap-2">
              <h3 className="text-sm font-semibold text-zinc-900 dark:text-zinc-50">Pflicht-Einträge</h3>
              <CountPill>{required.length}</CountPill>
              {!external && requiredChecked && (
                <Badge tone={requiredOk === required.length ? "green" : "amber"} className="ml-auto">
                  {requiredOk} von {required.length} passen
                </Badge>
              )}
            </div>
            <div className={cn(compact ? "space-y-2.5" : "space-y-3")}>
              {required.map((r) => (
                <RecordCard key={r.key || `${r.type}-${r.name}`} record={r} provider={provider} check={lookup(r)} checking={isChecking} compact={compact} />
              ))}
            </div>
          </section>

          {optional.length > 0 && (
            <section className="space-y-3">
              <button
                type="button"
                onClick={() => setShowOptional((v) => !v)}
                aria-expanded={showOptional}
                className="group flex w-full items-center gap-2 rounded-lg text-left"
              >
                <ChevronRight
                  className={cn("h-4 w-4 shrink-0 text-zinc-400 transition-transform", showOptional && "rotate-90")}
                />
                <span className="text-sm font-semibold text-zinc-900 group-hover:text-indigo-700 dark:text-zinc-50 dark:group-hover:text-indigo-300">
                  Optional – kannst du auch später oder gar nicht eintragen
                </span>
                <CountPill>{optional.length}</CountPill>
              </button>
              {showOptional && (
                <div className={cn(compact ? "space-y-2.5" : "space-y-3")}>
                  {optional.map((r) => (
                    <RecordCard key={r.key || `${r.type}-${r.name}`} record={r} provider={provider} check={lookup(r)} checking={isChecking} compact={compact} />
                  ))}
                </div>
              )}
            </section>
          )}

          <section className="space-y-3 border-t border-zinc-100 pt-4 dark:border-zinc-800">
            <label className="flex cursor-pointer items-center justify-between gap-4">
              <span className="min-w-0">
                <span className="block text-sm font-medium text-zinc-800 dark:text-zinc-200">Expertenansicht</span>
                <span className="block text-xs text-zinc-500 dark:text-zinc-400">Alle Einträge als kompakte Zonen-Tabelle (Typ, Name, Wert).</span>
              </span>
              <Switch checked={expert} onChange={setExpert} size="sm" aria-label="Expertenansicht" />
            </label>
            {expert && <ZoneTable records={list} />}
          </section>
        </>
      )}

      <DnsAutoSetup
        open={autoOpen}
        onClose={() => setAutoOpen(false)}
        domain={domain}
        defaultIpv4={defaultIpv4}
        onApplied={() => {
          setCheckRequested(true);
          void qc.invalidateQueries({ queryKey: setupKeys.dnsCheck(domain) });
          void qc.invalidateQueries({ queryKey: ["admin", "domain-dns", domain] });
        }}
      />
    </div>
  );
}

// ---------------------------------------------------------------------------
// Provider choice
// ---------------------------------------------------------------------------

type ProviderId = "generic" | "strato" | "ionos" | "cloudflare";

const PROVIDER_KEY = "postwerk.dnsGuide.provider";
const providerIds: ProviderId[] = ["generic", "strato", "ionos", "cloudflare"];

const providerItems: { value: ProviderId; label: string }[] = [
  { value: "generic", label: "Allgemein" },
  { value: "strato", label: "STRATO" },
  { value: "ionos", label: "IONOS" },
  { value: "cloudflare", label: "Cloudflare" },
];

const providerHints: Record<ProviderId, string | null> = {
  generic:
    "Die Felder heißen bei jedem Anbieter etwas anders. Steht bei „Name/Host“ „leer lassen“, verlangen manche Anbieter stattdessen ein @.",
  strato: null,
  ionos: "Wo bei IONOS? Domains & SSL → bei der Domain auf das Zahnrad → DNS → „Datensatz hinzufügen“. „@“ steht für die Domain selbst.",
  cloudflare: "Wo bei Cloudflare? Domain auswählen → DNS → Einträge → „Eintrag hinzufügen“. „@“ steht für die Domain selbst.",
};

function useProvider(): [ProviderId, (p: ProviderId) => void] {
  const [provider, setProvider] = useState<ProviderId>(() => {
    const v = storageGet(PROVIDER_KEY);
    return v && (providerIds as string[]).includes(v) ? (v as ProviderId) : "generic";
  });
  return [
    provider,
    (p) => {
      setProvider(p);
      storageSet(PROVIDER_KEY, p);
    },
  ];
}

// ---------------------------------------------------------------------------
// Field mapping per provider
// ---------------------------------------------------------------------------

interface GuideField {
  label: string;
  value: string;
  /** How an empty value is shown: "leer lassen" or "@". */
  empty?: "blank" | "at";
  /** Picked from a list in the provider's form – nothing to copy. */
  choice?: boolean;
  /** Not a literal value (e.g. "<öffentliche IPv4 des Servers>"). */
  placeholder?: boolean;
}

interface GuideNote {
  tone: "info" | "warning";
  text: ReactNode;
}

interface RecordGuide {
  fields: GuideField[];
  notes: GuideNote[];
  /** Where the form is found in the provider's customer area. */
  where?: string;
}

const STRATO_DNS = "Domains → Domainverwaltung → Zahnrad bei der Domain → DNS";

function stripDot(s: string): string {
  return s.replace(/\.$/, "");
}

function isPlaceholder(v: string): boolean {
  return v.startsWith("<") && v.endsWith(">");
}

function isDynDns(r: DnsRecord): boolean {
  return r.type === "A" && r.content.startsWith("<wird per DynDNS");
}

function num(n: number | null): string {
  return n === null || n === undefined ? "" : String(n);
}

/** SRV: the part of the host after "_service._protocol" ("" = the domain itself). */
function srvRest(r: DnsRecord): string {
  const prefix = `_${r.service ?? ""}._${r.protocol ?? ""}`.toLowerCase();
  const host = r.host.toLowerCase();
  if (host === prefix) return "";
  if (host.startsWith(prefix + ".")) return r.host.slice(prefix.length + 1);
  return r.host;
}

/** Content for A/TXT/CNAME, falling back to the zone value. */
function content(r: DnsRecord): string {
  return r.content || stripDot(r.value);
}

function buildGuide(r: DnsRecord, provider: ProviderId): RecordGuide {
  const fqdn = stripDot(r.name);
  const notes: GuideNote[] = [];
  if (r.outsideZone) {
    notes.push({ tone: "info", text: "Dieser Eintrag gehört zur Domain des Hostnamens und wird dort eingetragen." });
  }
  const name = (label: string, empty: "blank" | "at", host = r.host): GuideField =>
    r.outsideZone ? { label: "Name", value: fqdn } : { label, value: host, empty };
  const valueField = (label: string): GuideField => {
    const v = content(r);
    return { label, value: v, placeholder: isPlaceholder(v) };
  };
  const target = stripDot(r.target ?? "");
  const proto = (r.protocol ?? "").toUpperCase();

  switch (provider) {
    case "generic": {
      const n = name("Name/Host", "blank");
      switch (r.type) {
        case "A":
          return { fields: [n, valueField("IPv4-Adresse")], notes };
        case "MX":
          return { fields: [n, { label: "Priorität", value: num(r.priority) }, { label: "Mailserver", value: target }], notes };
        case "CNAME":
          return { fields: [n, valueField("Ziel")], notes };
        case "SRV":
          return {
            fields: [
              { label: "Service", value: `_${r.service ?? ""}` },
              { label: "Protokoll", value: `_${r.protocol ?? ""}` },
              name("Name/Host", "blank", srvRest(r)),
              { label: "Priorität", value: num(r.priority) },
              { label: "Gewicht", value: num(r.weight) },
              { label: "Port", value: num(r.port) },
              { label: "Ziel", value: target },
            ],
            notes,
          };
        default:
          return { fields: [n, valueField("Wert")], notes };
      }
    }

    case "strato": {
      switch (r.type) {
        case "A": {
          if (r.outsideZone) {
            return { fields: [{ label: "Name", value: fqdn }, valueField("IP-Adresse")], notes };
          }
          const sub = r.host ? `${r.host}.${r.zone}` : r.zone;
          notes.push({
            tone: "info",
            text: r.host ? (
              <>
                Bei STRATO gehört der A-Record zur (Sub-)Domain selbst: lege zuerst die Subdomain <Mono>{sub}</Mono> an und setze dort den
                A-Record.
              </>
            ) : (
              "Bei STRATO gehört der A-Record zur Domain selbst: setze ihn in den DNS-Einstellungen der Domain."
            ),
          });
          return {
            fields: [{ label: r.host ? "Subdomain" : "Domain", value: sub }, valueField("IP-Adresse")],
            notes,
            where: r.host
              ? `Domains → Domainverwaltung → Zahnrad bei der Subdomain ${sub} → DNS → A-Record`
              : `${STRATO_DNS} → A-Record`,
          };
        }
        case "MX": {
          notes.push({ tone: "info", text: "Bei STRATO in der MX-Einstellung „Eigener Mailserver“ wählen." });
          const fields: GuideField[] = [
            { label: "Mailserver", value: target },
            { label: "Priorität", value: num(r.priority) },
          ];
          if (r.outsideZone) fields.unshift({ label: "Name", value: fqdn });
          return { fields, notes, where: `${STRATO_DNS} → MX-Record` };
        }
        case "TXT":
        case "CNAME": {
          if (r.key === "spf") {
            notes.push({
              tone: "warning",
              text: "Bei STRATO die eigene SPF-Einstellung auf „keine“ bzw. „eigene“ stellen, sonst gibt es zwei SPF-Einträge.",
            });
          }
          return {
            fields: [name("Präfix", "blank"), { label: "Typ", value: r.type, choice: true }, valueField("Wert")],
            notes,
            where: `${STRATO_DNS} → TXT- und CNAME-Records`,
          };
        }
        case "SRV": {
          const fields: GuideField[] = [
            { label: "_Service._", value: r.service ?? "" },
            { label: "Protokoll", value: r.protocol ?? "" },
            { label: "Priorität", value: num(r.priority) },
            { label: "Gewicht", value: num(r.weight) },
            { label: "Port", value: num(r.port) },
            { label: "Ziel", value: target },
          ];
          if (r.outsideZone) fields.unshift({ label: "Name", value: fqdn });
          return { fields, notes, where: `${STRATO_DNS} → SRV-Records` };
        }
        default:
          return { fields: [name("Präfix", "blank"), valueField("Wert")], notes, where: STRATO_DNS };
      }
    }

    case "ionos": {
      const n = name("Hostname", "at");
      switch (r.type) {
        case "A":
          return { fields: [n, valueField("Zeigt auf")], notes };
        case "CNAME":
          return { fields: [n, valueField("Zeigt auf")], notes };
        case "MX":
          return { fields: [n, { label: "Zeigt auf", value: target }, { label: "Priorität", value: num(r.priority) }], notes };
        case "SRV":
          return {
            fields: [
              { label: "Service", value: r.service ?? "" },
              { label: "Protokoll", value: proto, choice: true },
              name("Hostname", "at", srvRest(r)),
              { label: "Zeigt auf", value: target },
              { label: "Priorität", value: num(r.priority) },
              { label: "Gewichtung", value: num(r.weight) },
              { label: "Port", value: num(r.port) },
            ],
            notes,
          };
        default:
          return { fields: [n, valueField("Wert")], notes };
      }
    }

    case "cloudflare": {
      const n = name("Name", "at");
      const proxy: GuideNote = { tone: "warning", text: "Proxy-Status auf „Nur DNS“ stellen (graue Wolke)." };
      switch (r.type) {
        case "A":
          return { fields: [n, valueField("IPv4-Adresse")], notes: [...notes, proxy] };
        case "CNAME":
          return { fields: [n, valueField("Ziel")], notes: [...notes, proxy] };
        case "MX":
          return { fields: [n, { label: "Mailserver", value: target }, { label: "Priorität", value: num(r.priority) }], notes };
        case "SRV":
          return {
            fields: [
              { label: "Service", value: `_${r.service ?? ""}` },
              { label: "Protokoll", value: proto, choice: true },
              name("Name", "at", srvRest(r)),
              { label: "Priorität", value: num(r.priority) },
              { label: "Gewichtung", value: num(r.weight) },
              { label: "Port", value: num(r.port) },
              { label: "Ziel", value: target },
            ],
            notes,
          };
        default:
          return { fields: [n, valueField("Inhalt")], notes };
      }
    }
  }
}

// ---------------------------------------------------------------------------
// Check status
// ---------------------------------------------------------------------------

function checkLookup(checks: DnsCheck[] | undefined): (r: DnsRecord) => DnsCheck | undefined {
  const byKey = new Map<string, DnsCheck>();
  const byName = new Map<string, DnsCheck>();
  for (const c of checks ?? []) {
    if (c.key) byKey.set(c.key, c);
    byName.set(`${c.type}|${stripDot(c.name).toLowerCase()}`, c);
  }
  return (r) => (r.key ? byKey.get(r.key) : undefined) ?? byName.get(`${r.type}|${stripDot(r.name).toLowerCase()}`);
}

function statusOf(c: DnsCheck | undefined, required: boolean, checking: boolean): { label: string; tone: BadgeTone } {
  if (!c) return { label: checking ? "Wird geprüft …" : "Noch nicht geprüft", tone: "neutral" };
  switch (c.status) {
    case "ok":
      return { label: "Passt", tone: "green" };
    case "missing":
      return required ? { label: "Fehlt", tone: "red" } : { label: "Nicht eingetragen", tone: "neutral" };
    case "mismatch":
      return { label: "Weicht ab", tone: "amber" };
    case "warning":
      return { label: "Bitte prüfen", tone: "amber" };
    case "error":
      return { label: "Nicht prüfbar", tone: "neutral" };
    default:
      return { label: "Hinweis", tone: "neutral" };
  }
}

// ---------------------------------------------------------------------------
// Cards
// ---------------------------------------------------------------------------

const typeTones: Record<string, BadgeTone> = {
  MX: "indigo",
  TXT: "teal",
  CNAME: "purple",
  A: "blue",
  AAAA: "blue",
  TLSA: "amber",
  SRV: "pink",
};

const providerNames: Record<ProviderId, string> = {
  generic: "Allgemein",
  strato: "STRATO",
  ionos: "IONOS",
  cloudflare: "Cloudflare",
};

function RecordCard({
  record: r,
  provider,
  check,
  checking,
  compact,
}: {
  record: DnsRecord;
  provider: ProviderId;
  check: DnsCheck | undefined;
  checking: boolean;
  compact?: boolean;
}) {
  const status = statusOf(check, r.required, checking);
  const dyn = isDynDns(r);
  const guide = dyn ? null : buildGuide(r, provider);
  const showFound = check && (check.status === "mismatch" || check.status === "warning") && check.found.length > 0;

  return (
    <article
      className={cn(
        "rounded-xl border border-zinc-200 bg-white dark:border-zinc-800 dark:bg-zinc-950/40",
        compact ? "p-3.5" : "p-4",
      )}
    >
      <header>
        <div className="flex items-start justify-between gap-3">
          <div className="flex min-w-0 flex-wrap items-center gap-2">
            <h4 className="text-sm font-semibold text-zinc-900 dark:text-zinc-50">{r.title || r.type}</h4>
            <Badge tone={typeTones[r.type] ?? "neutral"} className="font-mono">
              {r.type}
            </Badge>
          </div>
          <Badge tone={status.tone} dot className="shrink-0">
            {status.label}
          </Badge>
        </div>
        {r.purpose && <p className="mt-1 text-sm leading-relaxed text-zinc-500 dark:text-zinc-400">{r.purpose}</p>}
      </header>

      {showFound && (
        <p className="mt-2 text-xs text-amber-800 dark:text-amber-300">
          Derzeit eingetragen:{" "}
          {check.found.map((v, i) => (
            <code key={i} className="break-all font-mono">
              {i > 0 && ", "}
              {v}
            </code>
          ))}
        </p>
      )}

      {dyn ? (
        <p className="mt-3 flex items-start gap-2 rounded-lg bg-emerald-50 px-3 py-2.5 text-sm text-emerald-900 dark:bg-emerald-500/10 dark:text-emerald-200">
          <RefreshCw className="mt-0.5 h-4 w-4 shrink-0 text-emerald-600 dark:text-emerald-300" />
          <span>Nichts eintragen – Postwerk hält diesen Eintrag per DynDNS aktuell.</span>
        </p>
      ) : (
        guide && (
          <>
            {guide.notes.length > 0 && (
              <div className="mt-3 space-y-1.5">
                {guide.notes.map((n, i) => (
                  <NoteLine key={i} note={n} />
                ))}
              </div>
            )}
            <dl className="mt-3 divide-y divide-zinc-100 border-y border-zinc-100 dark:divide-zinc-800/80 dark:border-zinc-800/80">
              {guide.fields.map((f, i) => (
                <FieldRow key={`${f.label}-${i}`} field={f} />
              ))}
            </dl>
            {guide.where && (
              <p className="mt-3 flex items-start gap-1.5 text-xs leading-relaxed text-zinc-500 dark:text-zinc-400">
                <MapPin className="mt-px h-3.5 w-3.5 shrink-0" />
                <span>
                  <span className="font-medium text-zinc-700 dark:text-zinc-300">Wo bei {providerNames[provider]}?</span> {guide.where}
                </span>
              </p>
            )}
          </>
        )
      )}
    </article>
  );
}

function NoteLine({ note }: { note: GuideNote }) {
  const Icon = note.tone === "warning" ? TriangleAlert : Info;
  return (
    <p
      className={cn(
        "flex items-start gap-2 rounded-lg px-3 py-2 text-[13px] leading-relaxed",
        note.tone === "warning"
          ? "bg-amber-50 text-amber-900 dark:bg-amber-500/10 dark:text-amber-200"
          : "bg-indigo-50/70 text-indigo-900 dark:bg-indigo-500/10 dark:text-indigo-200",
      )}
    >
      <Icon
        className={cn(
          "mt-0.5 h-3.5 w-3.5 shrink-0",
          note.tone === "warning" ? "text-amber-500 dark:text-amber-300" : "text-indigo-500 dark:text-indigo-300",
        )}
      />
      <span>{note.text}</span>
    </p>
  );
}

function FieldRow({ field: f }: { field: GuideField }) {
  const empty = f.value === "";
  let display: ReactNode;
  let copy: string | null = f.value;

  if (empty && f.empty === "at") {
    display = (
      <>
        <ValueBox>@</ValueBox>
        <span className="pt-1.5 text-xs text-zinc-400 dark:text-zinc-500">= die Domain selbst</span>
      </>
    );
    copy = "@";
  } else if (empty) {
    display = <span className="py-1 text-sm italic text-zinc-400 dark:text-zinc-500">leer lassen</span>;
    copy = null;
  } else if (f.placeholder) {
    display = (
      <span className="py-1 text-sm italic text-zinc-500 dark:text-zinc-400">
        {f.value.slice(1, -1).replace(/^./, (c) => c.toUpperCase())}
      </span>
    );
    copy = null;
  } else if (f.choice) {
    display = (
      <>
        <ValueBox>{f.value}</ValueBox>
        <span className="pt-1.5 text-xs text-zinc-400 dark:text-zinc-500">auswählen</span>
      </>
    );
    copy = null;
  } else {
    display = <ValueBox grow>{f.value}</ValueBox>;
  }

  return (
    <div className="grid gap-1 py-2 sm:grid-cols-[8.5rem_minmax(0,1fr)] sm:items-start sm:gap-3">
      <dt className="text-xs font-medium text-zinc-500 sm:pt-1.5 dark:text-zinc-400">{f.label}</dt>
      <dd className="flex min-w-0 items-start gap-1.5">
        {display}
        {copy !== null && <CopyButton value={copy} className="ml-auto mt-0.5" />}
      </dd>
    </div>
  );
}

function ValueBox({ children, grow }: { children: ReactNode; grow?: boolean }) {
  return (
    <code
      className={cn(
        "min-w-0 whitespace-pre-wrap break-all rounded-md bg-zinc-50 px-2 py-1 font-mono text-[13px] leading-relaxed text-zinc-800 ring-1 ring-inset ring-zinc-200",
        "dark:bg-zinc-900 dark:text-zinc-200 dark:ring-zinc-800",
        grow && "flex-1",
      )}
    >
      {children}
    </code>
  );
}

function Mono({ children }: { children: ReactNode }) {
  return <code className="break-all font-mono text-[12.5px] font-medium">{children}</code>;
}

function CountPill({ children }: { children: ReactNode }) {
  return (
    <span className="rounded-full bg-zinc-100 px-1.5 text-xs font-medium text-zinc-600 dark:bg-zinc-800 dark:text-zinc-400">{children}</span>
  );
}

// ---------------------------------------------------------------------------
// Expert view
// ---------------------------------------------------------------------------

function ZoneTable({ records }: { records: DnsRecord[] }) {
  return (
    <div className="overflow-hidden rounded-lg border border-zinc-200 dark:border-zinc-800">
      <Table>
        <THead>
          <tr>
            <Th className="w-20">Typ</Th>
            <Th>Name</Th>
            <Th>Wert</Th>
          </tr>
        </THead>
        <TBody>
          {records.map((r, i) => (
            <Tr key={`${r.type}-${r.name}-${i}`}>
              <Td className="align-top">
                <Badge tone={typeTones[r.type] ?? "neutral"} className="font-mono">
                  {r.type}
                </Badge>
              </Td>
              <Td className="align-top">
                <div className="flex items-start gap-1">
                  <code className="break-all font-mono text-[13px] text-zinc-800 dark:text-zinc-200">{r.name}</code>
                  <CopyButton value={r.name} />
                </div>
              </Td>
              <Td className="align-top">
                <div className="flex max-w-xl items-start gap-1">
                  <code className="block max-h-24 min-w-[10rem] flex-1 overflow-y-auto break-all rounded-md bg-zinc-50 px-2 py-1 font-mono text-xs leading-relaxed text-zinc-700 ring-1 ring-inset ring-zinc-200 dark:bg-zinc-950 dark:text-zinc-300 dark:ring-zinc-800">
                    {r.value}
                  </code>
                  <CopyButton value={r.value} />
                </div>
              </Td>
            </Tr>
          ))}
        </TBody>
      </Table>
    </div>
  );
}
