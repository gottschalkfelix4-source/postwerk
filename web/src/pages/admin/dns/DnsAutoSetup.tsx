import { Fragment, useMemo, useState, type ChangeEvent, type FormEvent, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowLeft,
  ArrowRight,
  CircleCheck,
  CircleX,
  Clock,
  ExternalLink,
  Hand,
  Info,
  LocateFixed,
  Lock,
  RefreshCw,
  ShieldCheck,
  Sparkles,
  TriangleAlert,
  Wand2,
} from "lucide-react";
import { adminApi } from "@/api/endpoints";
import { isApiError } from "@/api/client";
import type {
  CheckStatus,
  DnsApplyResult,
  DnsChange,
  DnsChangeAction,
  DnsCheck,
  DnsPlan,
  DnsPlanRequest,
  DnsProviderInfo,
} from "@/api/types";
import { pluralize } from "@/lib/format";
import { cn } from "@/lib/utils";
import {
  Badge,
  Button,
  Checkbox,
  ConfirmDialog,
  CopyButton,
  ErrorState,
  Field,
  Input,
  Modal,
  Notice,
  PasswordInput,
  Skeleton,
  StepBar,
  TBody,
  THead,
  Table,
  Td,
  Th,
  Tr,
  useToast,
  type BadgeTone,
  type StepItem,
} from "@/components/ui";
import { setupKeys } from "@/pages/setup/context";

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

export interface DnsAutoSetupProps {
  open: boolean;
  onClose: () => void;
  domain: string;
  /** Called after changes were applied; the caller should reload its DNS check. */
  onApplied?: () => void;
  /** Pre-fills the public IPv4 used for the A record of the host name. */
  defaultIpv4?: string | null;
}

/**
 * Dialog that writes the recommended DNS records of a domain directly at the
 * DNS provider: choose provider → credentials → preview → apply. All state
 * (including credentials) lives only while the dialog is open.
 */
export function DnsAutoSetup(props: DnsAutoSetupProps) {
  if (!props.open) return null;
  return <DnsAutoSetupDialog {...props} />;
}

/** Prominent call-to-action card that opens {@link DnsAutoSetup}. */
export function DnsAutoSetupCard({
  domain,
  onApplied,
  defaultIpv4,
  className,
}: Omit<DnsAutoSetupProps, "open" | "onClose"> & { className?: string }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <div
        className={cn(
          "flex flex-col gap-4 rounded-xl border border-indigo-200 bg-gradient-to-br from-indigo-50 via-white to-white p-4 sm:flex-row sm:items-center",
          "dark:border-indigo-500/30 dark:from-indigo-500/10 dark:via-zinc-900 dark:to-zinc-900",
          className,
        )}
      >
        <span className="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl bg-indigo-600 text-white shadow-sm dark:bg-indigo-500">
          <Wand2 className="h-5 w-5" />
        </span>
        <div className="min-w-0 flex-1">
          <h3 className="text-sm font-semibold text-zinc-900 dark:text-zinc-50">Automatisch beim DNS-Anbieter eintragen</h3>
          <p className="mt-0.5 text-sm leading-relaxed text-zinc-600 dark:text-zinc-400">
            Postwerk setzt die Einträge für <span className="font-medium text-zinc-800 dark:text-zinc-200">{domain}</span> über die
            Schnittstelle deines DNS-Anbieters – mit Vorschau, bevor etwas geändert wird.
          </p>
        </div>
        <Button variant="primary" icon={Sparkles} onClick={() => setOpen(true)} className="self-start sm:self-center">
          Einrichten
        </Button>
      </div>
      <DnsAutoSetup open={open} onClose={() => setOpen(false)} domain={domain} onApplied={onApplied} defaultIpv4={defaultIpv4} />
    </>
  );
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

export const dnsProvidersKey = ["admin", "dns-providers"] as const;

const steps: StepItem[] = [
  { id: "provider", label: "Anbieter" },
  { id: "credentials", label: "Zugangsdaten" },
  { id: "preview", label: "Vorschau" },
  { id: "result", label: "Ergebnis" },
];

const providerTones: Record<string, string> = {
  ionos: "bg-blue-50 text-blue-700 dark:bg-blue-500/15 dark:text-blue-300",
  strato: "bg-orange-50 text-orange-600 dark:bg-orange-500/15 dark:text-orange-300",
  hetzner: "bg-red-50 text-red-600 dark:bg-red-500/15 dark:text-red-300",
  cloudflare: "bg-amber-50 text-amber-600 dark:bg-amber-500/15 dark:text-amber-300",
  netcup: "bg-teal-50 text-teal-700 dark:bg-teal-500/15 dark:text-teal-300",
  inwx: "bg-sky-50 text-sky-700 dark:bg-sky-500/15 dark:text-sky-300",
  desec: "bg-emerald-50 text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-300",
};
const fallbackTone = "bg-indigo-50 text-indigo-700 dark:bg-indigo-500/15 dark:text-indigo-300";

const actionMeta: Record<DnsChangeAction, { label: string; tone: BadgeTone }> = {
  create: { label: "Neu", tone: "blue" },
  update: { label: "Ändern", tone: "amber" },
  unchanged: { label: "Unverändert", tone: "neutral" },
  manual: { label: "Manuell", tone: "neutral" },
};

const typeTones: Record<string, BadgeTone> = {
  MX: "indigo",
  TXT: "teal",
  CNAME: "purple",
  A: "blue",
  AAAA: "blue",
  TLSA: "amber",
  SRV: "pink",
};

const IPV4_RE = /^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$/;
const IPV6_RE = /^[0-9a-f:.]+$/i;

function isIpv4(v: string): boolean {
  return IPV4_RE.test(v);
}

function isIpv6(v: string): boolean {
  return v.includes(":") && IPV6_RE.test(v);
}

function selectable(c: DnsChange): boolean {
  return c.action === "create" || c.action === "update";
}

function replacesMx(c: DnsChange): boolean {
  return c.type.toUpperCase() === "MX" && c.action === "update";
}

function ProviderIcon({ provider, className }: { provider: DnsProviderInfo; className?: string }) {
  return (
    <span
      className={cn(
        "flex h-8 w-8 shrink-0 items-center justify-center rounded-lg text-sm font-bold",
        providerTones[provider.id] ?? fallbackTone,
        className,
      )}
      aria-hidden
    >
      {provider.name.charAt(0).toUpperCase()}
    </span>
  );
}

function OfficialBadge({ provider }: { provider: DnsProviderInfo }) {
  return provider.official ? (
    <Badge tone="green" icon={ShieldCheck} title="Anbindung über die offizielle Schnittstelle des Anbieters">
      Offizielle API
    </Badge>
  ) : (
    <Badge
      tone="amber"
      icon={TriangleAlert}
      title={provider.notes ?? "Inoffizielle Anbindung – sie kann sich jederzeit ändern."}
    >
      Inoffiziell
    </Badge>
  );
}

function ProviderNotes({ provider }: { provider: DnsProviderInfo }) {
  if (!provider.official) {
    return (
      <Notice tone="warning" icon={TriangleAlert} title={`${provider.name}: inoffizielle Anbindung`}>
        {provider.notes ??
          "Der Anbieter bietet keine offizielle Schnittstelle. Die Anbindung kann jederzeit nicht mehr funktionieren."}
      </Notice>
    );
  }
  if (provider.notes) {
    return (
      <Notice tone="info" icon={Info} title={`Hinweis zu ${provider.name}`}>
        {provider.notes}
      </Notice>
    );
  }
  return null;
}

function RequestError({ error, fallbackTitle }: { error: unknown; fallbackTitle: string }) {
  if (!error) return null;
  const message = error instanceof Error ? error.message : "Unbekannter Fehler.";
  const title = isApiError(error, "dns_provider_error")
    ? "Der DNS-Anbieter meldet einen Fehler"
    : isApiError(error, "invalid")
      ? "Angaben unvollständig oder ungültig"
      : fallbackTitle;
  return (
    <Notice tone="error" icon={CircleX} title={title}>
      <span className="break-words">{message}</span>
    </Notice>
  );
}

/** Monospace DNS value; long values are truncated and can be expanded. */
function DnsValue({ value, variant = "plain", copy }: { value: string; variant?: "plain" | "old" | "new"; copy?: boolean }) {
  const [expanded, setExpanded] = useState(false);
  const limit = 72;
  const long = value.length > limit;
  const shown = long && !expanded ? `${value.slice(0, limit)}…` : value;
  return (
    <div className="flex min-w-0 items-start gap-1">
      <div
        className={cn(
          "min-w-0 flex-1 rounded-md px-2 py-1 font-mono text-xs leading-relaxed ring-1 ring-inset",
          variant === "old" &&
            "bg-red-50/70 text-red-800 line-through decoration-red-400/60 ring-red-200 dark:bg-red-500/[0.07] dark:text-red-300 dark:ring-red-500/25",
          variant === "new" &&
            "bg-emerald-50/70 text-emerald-900 ring-emerald-200 dark:bg-emerald-500/[0.07] dark:text-emerald-200 dark:ring-emerald-500/25",
          variant === "plain" && "bg-zinc-50 text-zinc-700 ring-zinc-200 dark:bg-zinc-950 dark:text-zinc-300 dark:ring-zinc-800",
        )}
      >
        <code className="break-all">{shown}</code>
        {long && (
          <button
            type="button"
            onClick={() => setExpanded((v) => !v)}
            className="ml-1.5 whitespace-nowrap font-sans text-[11px] font-medium text-indigo-600 no-underline hover:underline dark:text-indigo-300"
          >
            {expanded ? "weniger" : "alles anzeigen"}
          </button>
        )}
      </div>
      {copy && <CopyButton value={value} />}
    </div>
  );
}

const checkLabels: Record<CheckStatus, string> = {
  ok: "OK",
  missing: "Fehlt",
  mismatch: "Abweichend",
  warning: "Warnung",
  error: "Fehler",
  info: "Info",
};

function checkTone(status: CheckStatus, required: boolean): BadgeTone {
  if (status === "ok") return "green";
  if (status === "missing" || status === "mismatch") return required ? "red" : "amber";
  if (status === "warning") return "amber";
  if (status === "error") return "red";
  return "neutral";
}

// ---------------------------------------------------------------------------
// Dialog
// ---------------------------------------------------------------------------

function DnsAutoSetupDialog({ onClose, domain, onApplied, defaultIpv4 }: DnsAutoSetupProps) {
  const qc = useQueryClient();
  const toast = useToast();

  const [step, setStep] = useState(0);
  const [providerId, setProviderId] = useState<string | null>(null);
  const [credentials, setCredentials] = useState<Record<string, string>>({});
  const [ipv4, setIpv4] = useState(defaultIpv4 ?? "");
  const [ipv6, setIpv6] = useState("");
  const [detectedIpv6, setDetectedIpv6] = useState<string | null>(null);
  const [touched, setTouched] = useState(false);
  const [plan, setPlan] = useState<DnsPlan | null>(null);
  const [planRequest, setPlanRequest] = useState<DnsPlanRequest | null>(null);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [result, setResult] = useState<DnsApplyResult | null>(null);
  const [confirmMx, setConfirmMx] = useState(false);
  const [checkOn, setCheckOn] = useState(false);

  const providers = useQuery({ queryKey: dnsProvidersKey, queryFn: adminApi.dnsProviders, staleTime: 10 * 60_000 });
  const provider = providers.data?.find((p) => p.id === providerId) ?? null;

  const planMut = useMutation({
    mutationFn: (req: DnsPlanRequest) => adminApi.domainDnsPlan(domain, req),
    onSuccess: (p, req) => {
      setPlan(p);
      setPlanRequest(req);
      setSelected(new Set(p.changes.filter(selectable).map((c) => c.key)));
      applyMut.reset();
      setStep(2);
    },
  });

  const applyMut = useMutation({
    mutationFn: (keys: string[]) => adminApi.domainDnsApply(domain, { ...planRequest!, keys }),
    onSuccess: (r) => {
      setResult(r);
      setStep(3);
      void qc.invalidateQueries({ queryKey: setupKeys.dnsCheck(domain) });
      onApplied?.();
    },
  });

  const ipMut = useMutation({
    mutationFn: adminApi.publicIp,
    onSuccess: (r) => {
      if (r.ipv4) setIpv4(r.ipv4);
      else toast.info("Keine öffentliche IPv4 gefunden", "Bitte trage die Adresse von Hand ein.");
      setDetectedIpv6(r.ipv6);
    },
    onError: (err) => toast.error(err, "Adresse konnte nicht ermittelt werden"),
  });

  const check = useQuery({
    queryKey: setupKeys.dnsCheck(domain),
    queryFn: () => adminApi.domainDnsCheck(domain),
    enabled: checkOn,
    staleTime: 0,
  });

  const busy = planMut.isPending || applyMut.isPending;
  const close = () => {
    if (!busy) onClose();
  };

  const chooseProvider = (id: string) => {
    if (id === providerId) return;
    setProviderId(id);
    setCredentials({});
    setTouched(false);
    setPlan(null);
    setPlanRequest(null);
    planMut.reset();
  };

  // Validation of the credentials step.
  const fieldErrors = useMemo(() => {
    const errors: Record<string, string> = {};
    for (const f of provider?.fields ?? []) {
      if (f.required && !(credentials[f.key] ?? "").trim()) errors[f.key] = "Pflichtfeld";
    }
    return errors;
  }, [provider, credentials]);
  const ipv4Invalid = ipv4.trim() !== "" && !isIpv4(ipv4.trim());
  const ipv6Invalid = ipv6.trim() !== "" && !isIpv6(ipv6.trim());
  const credentialsValid = Object.keys(fieldErrors).length === 0 && !ipv4Invalid && !ipv6Invalid;

  const loadPlan = (e?: FormEvent) => {
    e?.preventDefault();
    if (!provider) return;
    setTouched(true);
    if (!credentialsValid) return;
    const creds: Record<string, string> = {};
    for (const f of provider.fields) {
      const raw = credentials[f.key] ?? "";
      // Passwords are sent as typed (minus pasted line breaks); text fields are trimmed.
      const value = f.type === "password" ? raw.replace(/[\r\n]+/g, "") : raw.trim();
      if (value !== "") creds[f.key] = value;
    }
    planMut.mutate({
      provider: provider.id,
      credentials: creds,
      ipv4: ipv4.trim() || null,
      ipv6: ipv6.trim() || null,
    });
  };

  const changes = plan?.changes ?? [];
  const selectedChanges = changes.filter((c) => selectable(c) && selected.has(c.key));
  const mxReplacements = selectedChanges.filter(replacesMx);

  const startApply = () => {
    if (selectedChanges.length === 0 || !planRequest) return;
    if (mxReplacements.length > 0) setConfirmMx(true);
    else applyMut.mutate(selectedChanges.map((c) => c.key));
  };

  // ---- footer -------------------------------------------------------------

  let footer: ReactNode;
  if (step === 0) {
    footer = (
      <>
        <Button variant="ghost" onClick={close}>
          Abbrechen
        </Button>
        <Button variant="primary" iconRight={ArrowRight} disabled={!provider} onClick={() => setStep(1)}>
          Weiter
        </Button>
      </>
    );
  } else if (step === 1) {
    footer = (
      <>
        <Button variant="ghost" icon={ArrowLeft} onClick={() => setStep(0)} disabled={busy} className="mr-auto">
          Zurück
        </Button>
        <Button
          variant="primary"
          type="submit"
          form="dns-auto-credentials"
          iconRight={ArrowRight}
          loading={planMut.isPending}
          disabled={touched && !credentialsValid}
        >
          Vorschau laden
        </Button>
      </>
    );
  } else if (step === 2) {
    footer = (
      <>
        <Button variant="ghost" icon={ArrowLeft} onClick={() => setStep(1)} disabled={busy} className="mr-auto">
          Zurück
        </Button>
        <Button icon={RefreshCw} onClick={() => planRequest && planMut.mutate(planRequest)} loading={planMut.isPending} disabled={applyMut.isPending}>
          Neu laden
        </Button>
        <Button
          variant="primary"
          icon={Wand2}
          onClick={startApply}
          loading={applyMut.isPending}
          disabled={selectedChanges.length === 0 || planMut.isPending}
        >
          {selectedChanges.length === 0
            ? "Nichts ausgewählt"
            : `${pluralize(selectedChanges.length, "Änderung", "Änderungen")} anwenden`}
        </Button>
      </>
    );
  } else {
    footer = (
      <>
        <Button variant="ghost" onClick={close} className="mr-auto">
          Schließen
        </Button>
        <Button
          variant="primary"
          icon={RefreshCw}
          loading={check.isFetching}
          onClick={() => (checkOn ? void check.refetch() : setCheckOn(true))}
        >
          DNS jetzt prüfen
        </Button>
      </>
    );
  }

  return (
    <>
      <Modal
        open
        onClose={close}
        size="xl"
        dismissible={step === 0 || step === 3}
        icon={
          <span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-indigo-50 text-indigo-600 dark:bg-indigo-500/15 dark:text-indigo-300">
            <Wand2 className="h-[18px] w-[18px]" />
          </span>
        }
        title="DNS automatisch einrichten"
        description={
          <>
            Einträge für <span className="font-medium text-zinc-700 dark:text-zinc-200">{domain}</span> direkt beim DNS-Anbieter setzen.
          </>
        }
        // Keyed per step: otherwise React reuses the "Weiter" button as the
        // submit button of the next step and the same click submits the form.
        footer={<Fragment key={step}>{footer}</Fragment>}
        bodyClassName="space-y-5"
      >
        <StepBar
          steps={steps}
          current={step}
          reached={step}
          onSelect={step < 3 && !busy ? (i) => setStep(i) : undefined}
        />

        {step === 0 && (
          <ProviderStep
            domain={domain}
            providers={providers.data}
            loading={providers.isLoading}
            error={providers.error}
            onRetry={() => void providers.refetch()}
            selectedId={providerId}
            onSelect={chooseProvider}
            onConfirm={(id) => {
              chooseProvider(id);
              setStep(1);
            }}
            selectedProvider={provider}
          />
        )}

        {step === 1 && provider && (
          <form id="dns-auto-credentials" onSubmit={loadPlan} className="space-y-5" autoComplete="off" noValidate>
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div className="flex min-w-0 items-center gap-3">
                <ProviderIcon provider={provider} />
                <div className="min-w-0">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="text-sm font-semibold text-zinc-900 dark:text-zinc-100">{provider.name}</span>
                    <OfficialBadge provider={provider} />
                  </div>
                  {provider.supports.length > 0 && (
                    <p className="mt-0.5 text-xs text-zinc-500 dark:text-zinc-400">Unterstützte Typen: {provider.supports.join(", ")}</p>
                  )}
                </div>
              </div>
              {provider.docsUrl && (
                <a
                  href={provider.docsUrl}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="inline-flex items-center gap-1.5 text-sm font-medium text-indigo-600 hover:underline dark:text-indigo-300"
                >
                  Wo bekomme ich den Schlüssel?
                  <ExternalLink className="h-3.5 w-3.5" />
                </a>
              )}
            </div>

            {!provider.official && <ProviderNotes provider={provider} />}

            <div className="grid gap-4 sm:grid-cols-2">
              {provider.fields.map((f) => {
                const error = touched ? fieldErrors[f.key] : undefined;
                const common = {
                  value: credentials[f.key] ?? "",
                  onChange: (e: ChangeEvent<HTMLInputElement>) => setCredentials((c) => ({ ...c, [f.key]: e.target.value })),
                  placeholder: f.placeholder ?? undefined,
                  invalid: !!error,
                  spellCheck: false,
                  className: "font-mono text-[13px]",
                };
                return (
                  <Field key={f.key} label={f.label} required={f.required} help={f.help} error={error}>
                    {(id) =>
                      f.type === "password" ? (
                        <PasswordInput id={id} autoComplete="new-password" {...common} />
                      ) : (
                        <Input id={id} autoComplete="off" {...common} />
                      )
                    }
                  </Field>
                );
              })}
            </div>

            <p className="flex items-center gap-2 text-xs text-zinc-500 dark:text-zinc-400">
              <Lock className="h-3.5 w-3.5 shrink-0" />
              Zugangsdaten werden nicht gespeichert – Postwerk verwendet sie nur für die Vorschau und das Anwenden.
            </p>

            <div className="grid gap-4 border-t border-zinc-100 pt-5 dark:border-zinc-800 sm:grid-cols-2">
              <Field
                label="Öffentliche IPv4"
                error={touched && ipv4Invalid ? "Bitte eine gültige IPv4-Adresse eingeben." : null}
                help={
                  <>
                    Für den A-Eintrag des Mailserver-Hostnamens. Leer lassen, wenn du ihn selbst setzt. „Ermitteln“ fragt die Adresse
                    über <span className="font-mono">api.ipify.org</span> ab.
                  </>
                }
              >
                {(id) => (
                  <div className="flex gap-2">
                    <div className="min-w-0 flex-1">
                      <Input
                        id={id}
                        value={ipv4}
                        onChange={(e) => setIpv4(e.target.value)}
                        placeholder="203.0.113.10"
                        invalid={touched && ipv4Invalid}
                        spellCheck={false}
                        className="font-mono text-[13px]"
                      />
                    </div>
                    <Button icon={LocateFixed} onClick={() => ipMut.mutate()} loading={ipMut.isPending}>
                      Ermitteln
                    </Button>
                  </div>
                )}
              </Field>
              <Field
                label="Öffentliche IPv6 (optional)"
                error={touched && ipv6Invalid ? "Bitte eine gültige IPv6-Adresse eingeben." : null}
                help={
                  detectedIpv6 && detectedIpv6 !== ipv6.trim() ? (
                    <>
                      Erkannt: <span className="font-mono">{detectedIpv6}</span> –{" "}
                      <button
                        type="button"
                        onClick={() => setIpv6(detectedIpv6)}
                        className="font-medium text-indigo-600 hover:underline dark:text-indigo-300"
                      >
                        übernehmen
                      </button>
                    </>
                  ) : (
                    "Nur ausfüllen, wenn der Server per IPv6 erreichbar ist (AAAA-Eintrag)."
                  )
                }
              >
                {(id) => (
                  <Input
                    id={id}
                    value={ipv6}
                    onChange={(e) => setIpv6(e.target.value)}
                    placeholder="2001:db8::10"
                    invalid={touched && ipv6Invalid}
                    spellCheck={false}
                    className="font-mono text-[13px]"
                  />
                )}
              </Field>
            </div>

            <RequestError error={planMut.error} fallbackTitle="Vorschau fehlgeschlagen" />
          </form>
        )}

        {step === 2 && plan && (
          <PreviewStep
            plan={plan}
            domain={domain}
            providerName={provider?.name ?? plan.provider}
            selected={selected}
            onSelectedChange={setSelected}
            refreshing={planMut.isPending}
            planError={planMut.error}
            applyError={applyMut.error}
          />
        )}

        {step === 3 && result && (
          <ResultStep
            result={result}
            plan={plan}
            providerName={provider?.name ?? plan?.provider ?? ""}
            checkOn={checkOn}
            checks={check.data}
            checkLoading={check.isLoading}
            checkFetching={check.isFetching}
            checkError={check.error}
          />
        )}
      </Modal>

      <ConfirmDialog
        open={confirmMx}
        danger
        title="MX-Einträge ersetzen?"
        confirmLabel="Ersetzen und anwenden"
        onCancel={() => setConfirmMx(false)}
        onConfirm={() => {
          setConfirmMx(false);
          applyMut.mutate(selectedChanges.map((c) => c.key));
        }}
        message={
          <div className="space-y-2">
            <p>
              Danach werden alle eingehenden Mails für <span className="font-medium">{domain}</span> an Postwerk zugestellt. Postfächer beim
              bisherigen Anbieter erhalten keine neuen Nachrichten mehr.
            </p>
            {mxReplacements.some((c) => c.current.length > 0) && (
              <div className="space-y-1">
                <div className="text-xs font-medium text-zinc-500 dark:text-zinc-400">Wird ersetzt:</div>
                {mxReplacements.flatMap((c) =>
                  c.current.map((v) => (
                    <code key={`${c.key}-${v}`} className="block break-all font-mono text-xs text-red-700 line-through dark:text-red-300">
                      {v}
                    </code>
                  )),
                )}
              </div>
            )}
          </div>
        }
      />
    </>
  );
}

// ---------------------------------------------------------------------------
// Step 1: provider
// ---------------------------------------------------------------------------

function ProviderStep({
  domain,
  providers,
  loading,
  error,
  onRetry,
  selectedId,
  onSelect,
  onConfirm,
  selectedProvider,
}: {
  domain: string;
  providers: DnsProviderInfo[] | undefined;
  loading: boolean;
  error: unknown;
  onRetry: () => void;
  selectedId: string | null;
  onSelect: (id: string) => void;
  onConfirm: (id: string) => void;
  selectedProvider: DnsProviderInfo | null;
}) {
  if (loading) {
    return (
      <div className="grid gap-2.5 sm:grid-cols-2 lg:grid-cols-3">
        {Array.from({ length: 6 }, (_, i) => (
          <Skeleton key={i} className="h-[92px] rounded-xl" />
        ))}
      </div>
    );
  }
  if (error || !providers) return <ErrorState error={error} onRetry={onRetry} />;

  return (
    <div className="space-y-4">
      <p className="text-sm text-zinc-600 dark:text-zinc-400">
        Bei welchem Anbieter liegt die DNS-Zone von <span className="font-medium text-zinc-900 dark:text-zinc-100">{domain}</span>?
      </p>
      <div className="grid gap-2.5 sm:grid-cols-2 lg:grid-cols-3">
        {providers.map((p) => {
          const active = p.id === selectedId;
          return (
            <button
              key={p.id}
              type="button"
              onClick={() => onSelect(p.id)}
              onDoubleClick={() => onConfirm(p.id)}
              aria-pressed={active}
              className={cn(
                "flex min-w-0 flex-col gap-2 rounded-xl border p-3 text-left transition",
                "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-indigo-500/50",
                active
                  ? "border-indigo-400 bg-indigo-50/60 ring-1 ring-indigo-400 dark:border-indigo-500/60 dark:bg-indigo-500/10 dark:ring-indigo-500/60"
                  : "border-zinc-200 hover:border-zinc-300 hover:bg-zinc-50 dark:border-zinc-800 dark:hover:border-zinc-700 dark:hover:bg-zinc-800/50",
              )}
            >
              <span className="flex w-full items-center justify-between gap-2">
                <span className="flex min-w-0 items-center gap-2.5">
                  <ProviderIcon provider={p} />
                  <span className="truncate text-sm font-medium text-zinc-900 dark:text-zinc-100">{p.name}</span>
                </span>
                <OfficialBadge provider={p} />
              </span>
              <span className="line-clamp-2 text-xs leading-relaxed text-zinc-500 dark:text-zinc-400">{p.description}</span>
            </button>
          );
        })}
      </div>
      {selectedProvider && <ProviderNotes provider={selectedProvider} />}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Step 3: preview
// ---------------------------------------------------------------------------

function PreviewStep({
  plan,
  domain,
  providerName,
  selected,
  onSelectedChange,
  refreshing,
  planError,
  applyError,
}: {
  plan: DnsPlan;
  domain: string;
  providerName: string;
  selected: Set<string>;
  onSelectedChange: (s: Set<string>) => void;
  refreshing: boolean;
  planError: unknown;
  applyError: unknown;
}) {
  const changes = plan.changes;
  const count = (a: DnsChangeAction) => changes.filter((c) => c.action === a).length;
  const counts = { create: count("create"), update: count("update"), unchanged: count("unchanged"), manual: count("manual") };
  const selectableKeys = changes.filter(selectable).map((c) => c.key);
  const selectedCount = selectableKeys.filter((k) => selected.has(k)).length;
  const allSelected = selectableKeys.length > 0 && selectedCount === selectableKeys.length;

  const toggle = (key: string, on: boolean) => {
    const next = new Set(selected);
    if (on) next.add(key);
    else next.delete(key);
    onSelectedChange(next);
  };

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="text-sm text-zinc-600 dark:text-zinc-400">
          Zone <code className="font-mono text-[13px] text-zinc-900 dark:text-zinc-100">{plan.zone}</code> bei{" "}
          <span className="font-medium text-zinc-900 dark:text-zinc-100">{providerName}</span>
        </div>
        <div className="flex flex-wrap items-center gap-1.5">
          <Badge tone="blue">{counts.create} neu</Badge>
          <Badge tone="amber">{counts.update} ändern</Badge>
          <Badge tone="neutral">{counts.unchanged} unverändert</Badge>
          <Badge tone="neutral">{counts.manual} manuell</Badge>
        </div>
      </div>

      <RequestError error={planError} fallbackTitle="Vorschau fehlgeschlagen" />
      <RequestError error={applyError} fallbackTitle="Anwenden fehlgeschlagen" />

      {selectableKeys.length === 0 && counts.manual === 0 && (
        <Notice tone="success" icon={CircleCheck} title="Alles bereits eingetragen">
          Alle empfohlenen Einträge sind beim Anbieter schon korrekt gesetzt.
        </Notice>
      )}

      <div className={cn("overflow-hidden rounded-lg border border-zinc-200 transition-opacity dark:border-zinc-800", refreshing && "opacity-60")}>
        <Table>
          <THead>
            <tr>
              <Th className="w-10 pr-0">
                <Checkbox
                  aria-label="Alle auswählen"
                  checked={allSelected}
                  indeterminate={selectedCount > 0 && !allSelected}
                  disabled={selectableKeys.length === 0}
                  onChange={(e) => onSelectedChange(e.target.checked ? new Set(selectableKeys) : new Set())}
                />
              </Th>
              <Th className="w-28">Aktion</Th>
              <Th className="w-16">Typ</Th>
              <Th className="min-w-[11rem]">Name</Th>
              <Th className="min-w-[18rem]">Aktuell / Neu</Th>
            </tr>
          </THead>
          <TBody>
            {changes.map((c) => (
              <ChangeRow
                key={c.key}
                change={c}
                domain={domain}
                checked={selectable(c) && selected.has(c.key)}
                onToggle={(on) => toggle(c.key, on)}
              />
            ))}
          </TBody>
        </Table>
      </div>

      {counts.manual > 0 && (
        <Notice tone="info" icon={Hand} title={`${pluralize(counts.manual, "Eintrag", "Einträge")} von Hand setzen`}>
          {counts.manual === 1
            ? `${providerName} kann diesen Eintrag nicht über die Schnittstelle setzen. Trage ihn selbst im Kundenbereich des Anbieters ein – der Wert steht in der Spalte „Neu“ und lässt sich kopieren.`
            : `${providerName} kann diese Einträge nicht über die Schnittstelle setzen. Trage sie selbst im Kundenbereich des Anbieters ein – die Werte stehen in der Spalte „Neu“ und lassen sich kopieren.`}
        </Notice>
      )}
    </div>
  );
}

function ChangeRow({
  change: c,
  domain,
  checked,
  onToggle,
}: {
  change: DnsChange;
  domain: string;
  checked: boolean;
  onToggle: (on: boolean) => void;
}) {
  const meta = actionMeta[c.action] ?? { label: c.action, tone: "neutral" as BadgeTone };
  const canSelect = selectable(c);
  const showCurrent = c.current.length > 0 && c.action !== "unchanged";
  const mxReplace = replacesMx(c);
  const mxNote = mxReplace && !(c.warning ?? "").includes("Postwerk");

  return (
    <Tr selected={checked} className={cn(!canSelect && "bg-zinc-50/40 dark:bg-zinc-900/30")}>
      <Td className="w-10 pr-0 align-top">
        <Checkbox
          aria-label={`${c.type} ${c.name} übernehmen`}
          checked={checked}
          disabled={!canSelect}
          onChange={(e) => onToggle(e.target.checked)}
          className="mt-0.5 disabled:cursor-not-allowed disabled:opacity-40"
        />
      </Td>
      <Td className="align-top">
        <Badge tone={meta.tone} icon={c.action === "manual" ? Hand : undefined} title={c.reason ?? undefined}>
          {meta.label}
        </Badge>
      </Td>
      <Td className="align-top">
        <Badge tone={typeTones[c.type.toUpperCase()] ?? "neutral"} className="font-mono">
          {c.type}
        </Badge>
      </Td>
      <Td className="align-top">
        <code className="break-all font-mono text-[13px] text-zinc-800 dark:text-zinc-200">{c.name}</code>
        <div className="mt-0.5 text-xs text-zinc-500 dark:text-zinc-400">
          {c.purpose}
          {!c.required && " · optional"}
        </div>
      </Td>
      <Td className="align-top">
        <div className="max-w-xl space-y-1.5">
          {showCurrent && (
            <div className="grid grid-cols-[3.25rem_minmax(0,1fr)] items-start gap-x-2 gap-y-1">
              <span className="pt-1 text-[11px] font-medium uppercase tracking-wide text-zinc-400">Aktuell</span>
              <div className="space-y-1">
                {c.current.map((v, i) => (
                  <DnsValue key={i} value={v} variant={c.action === "update" ? "old" : "plain"} />
                ))}
              </div>
            </div>
          )}
          <div className="grid grid-cols-[3.25rem_minmax(0,1fr)] items-start gap-x-2">
            <span className="pt-1 text-[11px] font-medium uppercase tracking-wide text-zinc-400">
              {c.action === "unchanged" ? "Wert" : "Neu"}
            </span>
            <DnsValue
              value={c.desired}
              variant={c.action === "create" || c.action === "update" ? "new" : "plain"}
              copy={c.action === "manual"}
            />
          </div>
          {c.warning && (
            <p className="flex items-start gap-1.5 text-xs font-medium text-amber-700 dark:text-amber-300">
              <TriangleAlert className="mt-px h-3.5 w-3.5 shrink-0 text-amber-500" />
              <span>
                {c.warning}
                {mxNote && ` – eingehende Mails für ${domain} gehen danach an Postwerk.`}
              </span>
            </p>
          )}
          {!c.warning && mxReplace && (
            <p className="flex items-start gap-1.5 text-xs font-medium text-amber-700 dark:text-amber-300">
              <TriangleAlert className="mt-px h-3.5 w-3.5 shrink-0 text-amber-500" />
              <span>Bestehende MX-Einträge werden ersetzt – eingehende Mails für {domain} gehen danach an Postwerk.</span>
            </p>
          )}
          {c.reason && (
            <p className="flex items-start gap-1.5 text-xs text-zinc-500 dark:text-zinc-400">
              <Info className="mt-px h-3.5 w-3.5 shrink-0" />
              <span>{c.reason}</span>
            </p>
          )}
        </div>
      </Td>
    </Tr>
  );
}

// ---------------------------------------------------------------------------
// Step 4: result
// ---------------------------------------------------------------------------

function ResultStep({
  result,
  plan,
  providerName,
  checkOn,
  checks,
  checkLoading,
  checkFetching,
  checkError,
}: {
  result: DnsApplyResult;
  plan: DnsPlan | null;
  providerName: string;
  checkOn: boolean;
  checks: DnsCheck[] | undefined;
  checkLoading: boolean;
  checkFetching: boolean;
  checkError: unknown;
}) {
  const byKey = new Map((plan?.changes ?? []).map((c) => [c.key, c]));
  const failed = result.failed.length;
  const applied = result.applied.length;

  return (
    <div className="space-y-4">
      {failed === 0 ? (
        <Notice tone="success" icon={CircleCheck} title={`${pluralize(applied, "Änderung", "Änderungen")} bei ${providerName} angewendet`}>
          {result.manual.length > 0
            ? "Einige Einträge musst du noch von Hand setzen (siehe unten)."
            : "Postwerk hat alle ausgewählten Einträge gesetzt."}
        </Notice>
      ) : (
        <Notice
          tone={applied > 0 ? "warning" : "error"}
          icon={applied > 0 ? TriangleAlert : CircleX}
          title={applied > 0 ? `${applied} angewendet, ${failed} fehlgeschlagen` : "Keine Änderung konnte angewendet werden"}
        >
          Die Fehlermeldungen des Anbieters stehen bei den betroffenen Einträgen.
        </Notice>
      )}

      {applied > 0 && (
        <ResultGroup title="Angewendet">
          {result.applied.map((key) => (
            <ResultItem key={key} icon={<CircleCheck className="h-4 w-4 text-emerald-500" />} change={byKey.get(key)} fallback={key} />
          ))}
        </ResultGroup>
      )}

      {failed > 0 && (
        <ResultGroup title="Fehlgeschlagen">
          {result.failed.map((f) => (
            <ResultItem key={f.key} icon={<CircleX className="h-4 w-4 text-red-500" />} change={byKey.get(f.key)} fallback={f.key}>
              <p className="mt-1 break-words text-xs text-red-600 dark:text-red-400">{f.error}</p>
            </ResultItem>
          ))}
        </ResultGroup>
      )}

      {result.manual.length > 0 && (
        <ResultGroup
          title="Von Hand eintragen"
          hint="Diese Werte trägst du selbst beim Anbieter ein. Du findest sie auch in der Tabelle der DNS-Einträge zum Kopieren."
        >
          {result.manual.map((key) => {
            const c = byKey.get(key);
            return (
              <ResultItem key={key} icon={<Hand className="h-4 w-4 text-zinc-400" />} change={c} fallback={key}>
                {c && (
                  <div className="mt-1.5">
                    <DnsValue value={c.desired} copy />
                  </div>
                )}
                {c?.reason && <p className="mt-1 text-xs text-zinc-500 dark:text-zinc-400">{c.reason}</p>}
              </ResultItem>
            );
          })}
        </ResultGroup>
      )}

      <Notice tone="info" icon={Clock}>
        DNS-Änderungen können einige Minuten brauchen, bis sie überall sichtbar sind. Prüfe die Einträge danach erneut.
      </Notice>

      {checkOn && (
        <ResultGroup title="DNS-Prüfung">
          {checkLoading ? (
            <div className="space-y-2 p-3">
              <Skeleton className="h-5 w-full" />
              <Skeleton className="h-5 w-4/5" />
              <Skeleton className="h-5 w-3/5" />
            </div>
          ) : checkError ? (
            <div className="p-3">
              <RequestError error={checkError} fallbackTitle="Prüfung fehlgeschlagen" />
            </div>
          ) : (
            <CheckList checks={checks ?? []} fetching={checkFetching} />
          )}
        </ResultGroup>
      )}
    </div>
  );
}

function ResultGroup({ title, hint, children }: { title: string; hint?: string; children: ReactNode }) {
  return (
    <section>
      <h3 className="text-xs font-semibold uppercase tracking-wide text-zinc-500 dark:text-zinc-400">{title}</h3>
      {hint && <p className="mt-0.5 text-xs text-zinc-500 dark:text-zinc-400">{hint}</p>}
      <div className="mt-2 divide-y divide-zinc-100 overflow-hidden rounded-lg border border-zinc-200 dark:divide-zinc-800 dark:border-zinc-800">
        {children}
      </div>
    </section>
  );
}

function ResultItem({
  icon,
  change,
  fallback,
  children,
}: {
  icon: ReactNode;
  change: DnsChange | undefined;
  fallback: string;
  children?: ReactNode;
}) {
  return (
    <div className="flex items-start gap-3 px-3 py-2.5">
      <span className="mt-0.5 shrink-0">{icon}</span>
      <div className="min-w-0 flex-1">
        {change ? (
          <div className="flex flex-wrap items-center gap-2">
            <Badge tone={typeTones[change.type.toUpperCase()] ?? "neutral"} className="font-mono">
              {change.type}
            </Badge>
            <code className="break-all font-mono text-[13px] text-zinc-800 dark:text-zinc-200">{change.name}</code>
            <span className="text-xs text-zinc-500 dark:text-zinc-400">{change.purpose}</span>
          </div>
        ) : (
          <code className="font-mono text-[13px] text-zinc-800 dark:text-zinc-200">{fallback}</code>
        )}
        {children}
      </div>
    </div>
  );
}

function CheckList({ checks, fetching }: { checks: DnsCheck[]; fetching: boolean }) {
  if (checks.length === 0) {
    return <p className="px-3 py-2.5 text-sm text-zinc-500 dark:text-zinc-400">Keine Einträge zu prüfen.</p>;
  }
  const required = checks.filter((c) => c.required);
  const ok = required.filter((c) => c.status === "ok").length;
  return (
    <div className={cn("transition-opacity", fetching && "opacity-60")}>
      <div className="flex items-center justify-between gap-2 bg-zinc-50/80 px-3 py-2 dark:bg-zinc-900/60">
        {ok === required.length ? (
          <Badge tone="green" icon={CircleCheck}>
            Alle Pflichteinträge korrekt
          </Badge>
        ) : (
          <Badge tone="amber" icon={TriangleAlert}>
            {ok} von {required.length} Pflichteinträgen korrekt
          </Badge>
        )}
        {fetching && <span className="text-xs text-zinc-500 dark:text-zinc-400">Prüfe …</span>}
      </div>
      {checks.map((c, i) => (
        <div key={`${c.type}-${c.name}-${i}`} className="flex items-center gap-3 border-t border-zinc-100 px-3 py-2 dark:border-zinc-800">
          <Badge tone={checkTone(c.status, c.required)} dot className="w-24 justify-start">
            {checkLabels[c.status] ?? c.status}
          </Badge>
          <Badge tone={typeTones[c.type] ?? "neutral"} className="font-mono">
            {c.type}
          </Badge>
          <code className="min-w-0 flex-1 truncate font-mono text-xs text-zinc-700 dark:text-zinc-300" title={c.name}>
            {c.name}
          </code>
          <span className="hidden shrink-0 text-xs text-zinc-500 dark:text-zinc-400 sm:inline">
            {c.purpose}
            {!c.required && " · optional"}
          </span>
        </div>
      ))}
    </div>
  );
}
