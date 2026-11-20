import { useState, type FormEvent, type ReactNode } from "react";
import { useNavigate } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowRightLeft,
  Bot,
  Bug,
  CircleCheck,
  CircleX,
  Globe,
  Info,
  KeyRound,
  Lock,
  Mail,
  Network,
  RefreshCw,
  Rocket,
  Send,
  ServerCog,
  ShieldCheck,
  Terminal,
  TriangleAlert,
  User,
  type LucideIcon,
} from "lucide-react";
import { adminApi, setupApi } from "@/api/endpoints";
import { errorMessage, isApiError } from "@/api/client";
import type { CheckStatus, DnsCheck, DnsRecord } from "@/api/types";
import { qk } from "@/lib/queries";
import { domainFromHostname, passwordStrength } from "@/lib/setup";
import { cn, isEmail } from "@/lib/utils";
import {
  Badge,
  Button,
  CopyButton,
  EmptyState,
  ErrorState,
  Field,
  Input,
  Notice,
  PasswordInput,
  Select,
  Skeleton,
  SkeletonRows,
  TBody,
  THead,
  Table,
  Td,
  Th,
  Tr,
  WizardNav,
  WizardStep,
  useToast,
  type BadgeTone,
} from "@/components/ui";
import { DnsAutoSetupCard } from "@/pages/admin/dns/DnsAutoSetup";
import { setupKeys, useSetup, type StepProps } from "./context";

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

const HOST_RE = /^(?=.{1,253}$)([a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$/i;

export function isHostname(value: string): boolean {
  return HOST_RE.test(value.trim().replace(/\.$/, ""));
}

const checkLabels: Record<CheckStatus, string> = {
  ok: "OK",
  missing: "Fehlt",
  mismatch: "Abweichend",
  warning: "Warnung",
  error: "Fehler",
  info: "Info",
};

export function checkTone(status: CheckStatus, required = true): BadgeTone {
  switch (status) {
    case "ok":
      return "green";
    case "missing":
    case "mismatch":
      return required ? "red" : "amber";
    case "warning":
      return "amber";
    case "error":
      return "red";
    default:
      return "neutral";
  }
}

export function CheckBadge({ status, required = true, label }: { status: CheckStatus; required?: boolean; label?: string }) {
  return (
    <Badge tone={checkTone(status, required)} dot>
      {label ?? checkLabels[status] ?? status}
    </Badge>
  );
}

export function CommandBox({ command }: { command: string }) {
  return (
    <div className="flex items-center gap-2 rounded-lg bg-zinc-900 px-3 py-2 font-mono text-[12.5px] text-zinc-100 ring-1 ring-inset ring-zinc-800 dark:bg-zinc-950">
      <Terminal className="h-3.5 w-3.5 shrink-0 text-zinc-500" />
      <code className="min-w-0 flex-1 overflow-x-auto whitespace-nowrap scrollbar-none">{command}</code>
      <CopyButton value={command} className="text-zinc-400 hover:bg-zinc-800 hover:text-zinc-100 dark:hover:bg-zinc-800" />
    </div>
  );
}

// ---------------------------------------------------------------------------
// 1. Welcome
// ---------------------------------------------------------------------------

const overview: { icon: LucideIcon; title: string; text: string }[] = [
  { icon: ServerCog, title: "Server & Administrator", text: "Hostname, erste Domain und dein Admin-Konto." },
  { icon: Globe, title: "DNS-Einträge", text: "MX, SPF, DKIM und DMARC live prüfen." },
  { icon: Network, title: "System-Check", text: "Reverse-DNS, Port 25 und Dienste." },
  { icon: Lock, title: "Reverse-Proxy & TLS", text: "Proxy-Anleitung und Zertifikat für die Mail-Ports." },
  { icon: ShieldCheck, title: "Schutz & KI", text: "Virenscan, Spamfilter und KI-Funktionen." },
  { icon: ArrowRightLeft, title: "Daten übernehmen", text: "Postfächer von einem anderen Server umziehen." },
];

export function WelcomeStep({ onNext }: StepProps) {
  const ctx = useSetup();
  const needsToken = ctx.initialFlow && !ctx.initialized;
  const [touched, setTouched] = useState(false);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setTouched(true);
    if (!needsToken || ctx.token.trim()) onNext();
  };

  return (
    <WizardStep
      icon={Rocket}
      title="Willkommen bei Postwerk"
      description={
        needsToken
          ? "In wenigen Minuten ist dein Mailserver startklar."
          : "Prüfe DNS, Zertifikate und Schutzfunktionen deines Servers."
      }
      footer={
        <WizardNav
          form="setup-welcome"
          nextLabel={needsToken ? "Los geht’s" : "Weiter"}
          extra={
            !needsToken ? (
              <Button variant="ghost" onClick={ctx.skip}>
                Später einrichten
              </Button>
            ) : undefined
          }
        />
      }
    >
      <form id="setup-welcome" onSubmit={submit} className="space-y-6" noValidate>
        <p className="text-sm leading-relaxed text-zinc-600 dark:text-zinc-300">
          Der Assistent führt dich durch alle Schritte, die ein Mailserver für zuverlässige Zustellung braucht. Optionale
          Schritte kannst du überspringen und später in den Einstellungen nachholen.
        </p>

        <div className="grid gap-3 sm:grid-cols-2">
          {overview.map((o) => (
            <div key={o.title} className="flex gap-3 rounded-lg border border-zinc-200 p-3 dark:border-zinc-800">
              <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-zinc-100 text-zinc-500 dark:bg-zinc-800 dark:text-zinc-400">
                <o.icon className="h-4 w-4" />
              </span>
              <div className="min-w-0">
                <div className="text-sm font-medium text-zinc-900 dark:text-zinc-100">{o.title}</div>
                <div className="text-xs text-zinc-500 dark:text-zinc-400">{o.text}</div>
              </div>
            </div>
          ))}
        </div>

        {needsToken ? (
          <div className="space-y-3 rounded-xl border border-zinc-200 bg-zinc-50/70 p-4 dark:border-zinc-800 dark:bg-zinc-950/40">
            <Field
              label="Setup-Token"
              required
              error={ctx.tokenError ?? (touched && !ctx.token.trim() ? "Bitte den Setup-Token eingeben." : null)}
              help="Der Token schützt die Ersteinrichtung. Er wurde beim ersten Start des Servers erzeugt."
            >
              {(id) => (
                <Input
                  id={id}
                  icon={KeyRound}
                  value={ctx.token}
                  onChange={(e) => ctx.setToken(e.target.value)}
                  invalid={!!ctx.tokenError || (touched && !ctx.token.trim())}
                  placeholder="z. B. 7f3c9a…"
                  autoComplete="off"
                  spellCheck={false}
                  autoFocus
                  className="font-mono text-[13px]"
                />
              )}
            </Field>
            <div className="space-y-2 text-xs text-zinc-500 dark:text-zinc-400">
              <p>So findest du den Token – im Container-Log:</p>
              <CommandBox command="docker logs postwerk | grep -i setup" />
              <p>
                oder in der Datei <code className="font-mono text-zinc-700 dark:text-zinc-300">/data/setup-token.txt</code> im
                Datenverzeichnis:
              </p>
              <CommandBox command="docker exec postwerk cat /data/setup-token.txt" />
            </div>
          </div>
        ) : (
          <Notice tone="info" icon={Info}>
            Dein Server ist bereits in Betrieb. Du kannst den Assistenten jederzeit mit „Später einrichten“ verlassen; er
            erscheint dann bei der nächsten Anmeldung erneut, bis du ihn abschließt.
          </Notice>
        )}
      </form>
    </WizardStep>
  );
}

// ---------------------------------------------------------------------------
// 2a. Server & admin (initial mode)
// ---------------------------------------------------------------------------

function StrengthMeter({ password }: { password: string }) {
  const { score, label } = passwordStrength(password);
  if (!password) return null;
  const colors = ["bg-red-500", "bg-red-500", "bg-amber-500", "bg-emerald-500", "bg-emerald-600"];
  return (
    <div className="flex items-center gap-3">
      <div className="flex flex-1 gap-1">
        {[0, 1, 2, 3].map((i) => (
          <span
            key={i}
            className={cn("h-1 flex-1 rounded-full transition-colors", i < Math.max(1, score) ? colors[score] : "bg-zinc-200 dark:bg-zinc-800")}
          />
        ))}
      </div>
      <span className="w-24 text-right text-xs text-zinc-500 dark:text-zinc-400">{label}</span>
    </div>
  );
}

export function ServerAdminStep({ onBack, onNext }: StepProps) {
  const ctx = useSetup();
  const qc = useQueryClient();
  const toast = useToast();
  const navigate = useNavigate();
  const f = ctx.initForm;
  const [touched, setTouched] = useState(false);
  const [alreadyDone, setAlreadyDone] = useState(false);

  const set = (patch: Partial<typeof f>) => {
    const next = { ...f, ...patch };
    if (patch.hostname !== undefined && !next.domainTouched) next.domain = domainFromHostname(patch.hostname);
    if ((patch.hostname !== undefined || patch.domain !== undefined) && !next.emailTouched) {
      next.adminEmail = next.domain ? `admin@${next.domain}` : "";
    }
    ctx.setInitForm(next);
  };

  const errors = {
    hostname: !isHostname(f.hostname) ? "Bitte einen vollqualifizierten Hostnamen eingeben, z. B. mail.beispiel.de." : null,
    domain: !isHostname(f.domain) ? "Bitte eine gültige Domain eingeben, z. B. beispiel.de." : null,
    adminName: !f.adminName.trim() ? "Bitte einen Namen eingeben." : null,
    adminEmail: !isEmail(f.adminEmail) ? "Bitte eine gültige E-Mail-Adresse eingeben." : null,
    password: f.password.length < 10 ? "Mindestens 10 Zeichen." : null,
    repeat: f.repeat !== f.password ? "Die Passwörter stimmen nicht überein." : null,
  };
  const valid = Object.values(errors).every((e) => !e);
  const emailOtherDomain =
    isEmail(f.adminEmail) && isHostname(f.domain) && !f.adminEmail.trim().toLowerCase().endsWith(`@${f.domain.trim().toLowerCase()}`);

  const init = useMutation({
    mutationFn: () =>
      setupApi.initialize({
        token: ctx.token.trim(),
        hostname: f.hostname.trim().toLowerCase().replace(/\.$/, ""),
        domain: f.domain.trim().toLowerCase().replace(/\.$/, ""),
        adminEmail: f.adminEmail.trim().toLowerCase(),
        adminName: f.adminName.trim(),
        adminPassword: f.password,
      }),
    onSuccess: (me) => {
      qc.setQueryData(qk.me, me);
      void qc.invalidateQueries({ queryKey: qk.setupStatus });
      ctx.setInitForm({ ...f, password: "", repeat: "" });
      ctx.markInitialized(f.domain.trim().toLowerCase());
      toast.success("Administrator angelegt", `Du bist jetzt als ${me.email} angemeldet.`);
      onNext();
    },
    onError: (err) => {
      if (isApiError(err, "invalid_token")) {
        ctx.setTokenError("Der Setup-Token ist ungültig. Bitte prüfe ihn im Container-Log.");
        ctx.goTo(0);
      } else if (isApiError(err, "already_initialized")) {
        setAlreadyDone(true);
      }
    },
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setTouched(true);
    if (valid && !init.isPending) init.mutate();
  };

  const show = (key: keyof typeof errors, dirty: boolean) => (touched || dirty ? errors[key] : null);

  return (
    <WizardStep
      icon={ServerCog}
      title="Server & Administrator"
      description="Grunddaten deines Mailservers und das erste Admin-Konto."
      footer={
        <WizardNav onBack={onBack} form="setup-init" nextLabel="Einrichten" loading={init.isPending} nextDisabled={alreadyDone} />
      }
    >
      <form id="setup-init" onSubmit={submit} className="space-y-6" noValidate>
        {alreadyDone ? (
          <Notice tone="error" icon={TriangleAlert} title="Postwerk ist bereits eingerichtet">
            <p>Auf diesem Server existiert schon ein Administrator. Melde dich mit diesem Konto an, um fortzufahren.</p>
            <Button
              size="sm"
              variant="secondary"
              className="mt-2"
              onClick={() => {
                void qc.invalidateQueries({ queryKey: qk.setupStatus });
                navigate("/login", { replace: true });
              }}
            >
              Zur Anmeldung
            </Button>
          </Notice>
        ) : init.error && !isApiError(init.error, "invalid_token") ? (
          <Notice tone="error" icon={CircleX} title="Einrichtung fehlgeschlagen">
            {errorMessage(init.error)}
          </Notice>
        ) : null}

        <section className="space-y-4">
          <SectionTitle icon={Globe} title="Server" text="Der Hostname muss per A/AAAA-Record auf diesen Server zeigen; der MX-Eintrag der Domain zeigt auf ihn." />
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Hostname" required error={show("hostname", f.hostname !== ctx.status.hostname && !!f.hostname)}>
              {(id) => (
                <Input
                  id={id}
                  value={f.hostname}
                  onChange={(e) => set({ hostname: e.target.value })}
                  placeholder="mail.beispiel.de"
                  autoComplete="off"
                  invalid={!!show("hostname", false)}
                  autoFocus
                />
              )}
            </Field>
            <Field label="Erste Domain" required error={show("domain", f.domainTouched && !!f.domain)} help="Für diese Domain werden Postfächer angelegt und ein DKIM-Schlüssel erzeugt.">
              {(id) => (
                <Input
                  id={id}
                  value={f.domain}
                  onChange={(e) => set({ domain: e.target.value, domainTouched: true })}
                  placeholder="beispiel.de"
                  autoComplete="off"
                  invalid={!!show("domain", false)}
                />
              )}
            </Field>
          </div>
        </section>

        <div className="border-t border-zinc-100 dark:border-zinc-800" />

        <section className="space-y-4">
          <SectionTitle icon={User} title="Administrator" text="Mit diesem Konto meldest du dich an der Verwaltung und im Webmail an." />
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Name" required error={show("adminName", false)}>
              {(id) => (
                <Input id={id} value={f.adminName} onChange={(e) => set({ adminName: e.target.value })} placeholder="Max Mustermann" autoComplete="name" invalid={!!show("adminName", false)} />
              )}
            </Field>
            <Field
              label="E-Mail-Adresse"
              required
              error={show("adminEmail", f.emailTouched && !!f.adminEmail)}
              help={emailOtherDomain ? "Die Adresse liegt nicht in der ersten Domain – das ist erlaubt, aber ungewöhnlich." : undefined}
            >
              {(id) => (
                <Input
                  id={id}
                  type="email"
                  icon={Mail}
                  value={f.adminEmail}
                  onChange={(e) => set({ adminEmail: e.target.value, emailTouched: true })}
                  placeholder="admin@beispiel.de"
                  autoComplete="username"
                  invalid={!!show("adminEmail", false)}
                />
              )}
            </Field>
            <Field label="Passwort" required error={show("password", f.password.length > 0)}>
              {(id) => (
                <div className="space-y-2">
                  <PasswordInput id={id} icon={Lock} value={f.password} onChange={(e) => set({ password: e.target.value })} autoComplete="new-password" invalid={!!show("password", f.password.length > 0)} />
                  <StrengthMeter password={f.password} />
                </div>
              )}
            </Field>
            <Field label="Passwort wiederholen" required error={show("repeat", f.repeat.length > 0)}>
              {(id) => (
                <PasswordInput id={id} icon={Lock} value={f.repeat} onChange={(e) => set({ repeat: e.target.value })} autoComplete="new-password" invalid={!!show("repeat", f.repeat.length > 0)} />
              )}
            </Field>
          </div>
        </section>
      </form>
    </WizardStep>
  );
}

export function SectionTitle({ icon: Icon, title, text }: { icon: LucideIcon; title: string; text?: string }) {
  return (
    <div className="flex items-start gap-2.5">
      <Icon className="mt-0.5 h-4 w-4 shrink-0 text-zinc-400" />
      <div>
        <h3 className="text-sm font-semibold text-zinc-900 dark:text-zinc-100">{title}</h3>
        {text && <p className="mt-0.5 text-xs leading-relaxed text-zinc-500 dark:text-zinc-400">{text}</p>}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// 2b. Server (configure mode): host name via the settings API
// ---------------------------------------------------------------------------

export function ServerStep({ onBack, onNext }: StepProps) {
  const qc = useQueryClient();
  const toast = useToast();
  const settings = useQuery({ queryKey: setupKeys.settings, queryFn: adminApi.settings, staleTime: 60_000 });
  const domains = useQuery({ queryKey: setupKeys.domains, queryFn: adminApi.domains });
  const [hostname, setHostname] = useState<string | null>(null);
  const value = hostname ?? settings.data?.server.hostname ?? "";
  const dirty = !!settings.data && value.trim() !== settings.data.server.hostname;
  const invalid = !isHostname(value);

  const save = useMutation({
    mutationFn: () => adminApi.saveSettings({ server: { ...settings.data!.server, hostname: value.trim().toLowerCase() } }),
    onSuccess: (s) => {
      qc.setQueryData(setupKeys.settings, s);
      void qc.invalidateQueries({ queryKey: qk.setupStatus });
      toast.success("Hostname gespeichert");
      onNext();
    },
    onError: (err) => toast.error(err, "Speichern fehlgeschlagen"),
  });

  return (
    <WizardStep
      icon={ServerCog}
      title="Server"
      description="Der Name, unter dem dein Mailserver im Internet erreichbar ist."
      footer={
        <WizardNav
          onBack={onBack}
          onNext={() => (dirty ? save.mutate() : onNext())}
          nextLabel={dirty ? "Speichern & weiter" : "Weiter"}
          nextDisabled={!settings.data || invalid}
          loading={save.isPending}
        />
      }
    >
      {settings.isLoading ? (
        <Skeleton className="h-24 rounded-lg" />
      ) : settings.error ? (
        <ErrorState error={settings.error} onRetry={() => void settings.refetch()} />
      ) : (
        <div className="space-y-6">
          <Field
            label="Hostname"
            error={invalid && value ? "Bitte einen vollqualifizierten Hostnamen eingeben." : null}
            help="Wird im SMTP-Banner, in HELO/EHLO und für TLS-Zertifikate verwendet. Der MX-Eintrag deiner Domains zeigt auf diesen Namen."
          >
            {(id) => (
              <Input id={id} value={value} onChange={(e) => setHostname(e.target.value)} placeholder="mail.beispiel.de" invalid={invalid && !!value} className="max-w-md" />
            )}
          </Field>
          <div>
            <div className="mb-2 text-sm font-medium text-zinc-800 dark:text-zinc-200">Domains auf diesem Server</div>
            {domains.isLoading ? (
              <Skeleton className="h-6 w-48" />
            ) : domains.data && domains.data.length > 0 ? (
              <div className="flex flex-wrap gap-1.5">
                {domains.data.map((d) => (
                  <Badge key={d.name} tone="indigo">
                    {d.name}
                  </Badge>
                ))}
              </div>
            ) : (
              <p className="text-sm text-zinc-500 dark:text-zinc-400">Noch keine Domain angelegt. Domains verwaltest du unter Verwaltung → Domains.</p>
            )}
          </div>
        </div>
      )}
    </WizardStep>
  );
}

// ---------------------------------------------------------------------------
// 3. DNS
// ---------------------------------------------------------------------------

const typeTones: Record<DnsRecord["type"], BadgeTone> = {
  MX: "indigo",
  TXT: "teal",
  CNAME: "purple",
  A: "blue",
  AAAA: "blue",
  TLSA: "amber",
  SRV: "pink",
};

export function dnsSummary(checks: DnsCheck[] | undefined): { ok: number; required: number; problems: number } {
  const list = checks ?? [];
  const required = list.filter((c) => c.required);
  return {
    ok: required.filter((c) => c.status === "ok").length,
    required: required.length,
    problems: required.filter((c) => c.status !== "ok" && c.status !== "info").length,
  };
}

export function DnsStep({ onBack, onNext }: StepProps) {
  const ctx = useSetup();
  const domains = useQuery({ queryKey: setupKeys.domains, queryFn: adminApi.domains });
  const list = domains.data ?? [];
  const selected = list.some((d) => d.name === ctx.domain) ? ctx.domain : (list[0]?.name ?? "");
  const check = useQuery({
    queryKey: setupKeys.dnsCheck(selected),
    queryFn: () => adminApi.domainDnsCheck(selected),
    enabled: !!selected,
    staleTime: 0,
  });
  const summary = dnsSummary(check.data);
  // Only read a system check that is already cached; it is not fetched here.
  const systemCheck = useQuery({ queryKey: setupKeys.systemCheck, queryFn: adminApi.systemCheck, enabled: false });
  const defaultIpv4 = systemCheck.data?.hostnameIps.find((ip) => /^\d{1,3}(\.\d{1,3}){3}$/.test(ip)) ?? null;

  return (
    <WizardStep
      icon={Globe}
      title="DNS-Einträge"
      description="Diese Einträge braucht deine Domain für Zustellung, SPF, DKIM und DMARC."
      footer={
        <WizardNav
          onBack={onBack}
          onNext={onNext}
          extra={
            <Button icon={RefreshCw} onClick={() => void check.refetch()} loading={check.isFetching} disabled={!selected}>
              Erneut prüfen
            </Button>
          }
        />
      }
    >
      {domains.isLoading ? (
        <SkeletonRows rows={4} cols={3} />
      ) : domains.error ? (
        <ErrorState error={domains.error} onRetry={() => void domains.refetch()} />
      ) : list.length === 0 ? (
        <EmptyState compact icon={Globe} title="Keine Domain angelegt" description="Lege unter Verwaltung → Domains eine Domain an, um ihre DNS-Einträge zu prüfen." />
      ) : (
        <div className="space-y-4">
          <div className="flex flex-wrap items-center justify-between gap-3">
            {list.length > 1 ? (
              <Select
                value={selected}
                onChange={(e) => ctx.setDomain(e.target.value)}
                options={list.map((d) => ({ value: d.name, label: d.name }))}
                className="w-full max-w-xs"
                aria-label="Domain"
              />
            ) : (
              <div className="flex items-center gap-2 text-sm font-medium text-zinc-900 dark:text-zinc-100">
                <Globe className="h-4 w-4 text-zinc-400" /> {selected}
              </div>
            )}
            {check.data && (
              <div className="flex items-center gap-2 text-sm">
                {summary.problems === 0 ? (
                  <Badge tone="green" icon={CircleCheck}>
                    Alle Pflichteinträge korrekt
                  </Badge>
                ) : (
                  <Badge tone="red" icon={CircleX}>
                    {summary.ok} von {summary.required} Pflichteinträgen korrekt
                  </Badge>
                )}
              </div>
            )}
          </div>

          <DnsAutoSetupCard key={selected} domain={selected} defaultIpv4={defaultIpv4} onApplied={() => void check.refetch()} />

          <div className="overflow-hidden rounded-lg border border-zinc-200 dark:border-zinc-800">
            {check.isLoading ? (
              <SkeletonRows rows={5} cols={4} />
            ) : check.error ? (
              <ErrorState error={check.error} onRetry={() => void check.refetch()} />
            ) : !check.data || check.data.length === 0 ? (
              <EmptyState compact title="Keine Einträge" />
            ) : (
              <Table>
                <THead>
                  <tr>
                    <Th className="w-28">Status</Th>
                    <Th className="w-20">Typ</Th>
                    <Th className="min-w-[12rem]">Name</Th>
                    <Th className="min-w-[16rem]">Erwarteter Wert</Th>
                    <Th className="min-w-[11rem]">Gefunden</Th>
                  </tr>
                </THead>
                <TBody>
                  {check.data.map((r, i) => (
                    <Tr key={`${r.type}-${r.name}-${i}`}>
                      <Td className="align-top">
                        <CheckBadge status={r.status} required={r.required} />
                      </Td>
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
                        <div className="mt-0.5 text-xs text-zinc-500 dark:text-zinc-400">
                          {r.purpose}
                          {!r.required && " · optional"}
                        </div>
                      </Td>
                      <Td className="align-top">
                        <div className="flex max-w-md items-start gap-1">
                          <code className="block max-h-24 min-w-[10rem] flex-1 overflow-y-auto break-all rounded-md bg-zinc-50 px-2 py-1 font-mono text-xs leading-relaxed text-zinc-700 ring-1 ring-inset ring-zinc-200 dark:bg-zinc-950 dark:text-zinc-300 dark:ring-zinc-800">
                            {r.expected}
                          </code>
                          <CopyButton value={r.expected} />
                        </div>
                      </Td>
                      <Td className="max-w-xs align-top">
                        {r.found.length === 0 ? (
                          <span className="text-xs italic text-zinc-400 dark:text-zinc-500">nicht gefunden</span>
                        ) : (
                          <div className="space-y-1">
                            {r.found.map((v, j) => (
                              <code key={j} className="block break-all font-mono text-xs text-zinc-600 dark:text-zinc-400">
                                {v}
                              </code>
                            ))}
                          </div>
                        )}
                      </Td>
                    </Tr>
                  ))}
                </TBody>
              </Table>
            )}
          </div>

          <Notice tone="info" icon={Info}>
            DNS-Änderungen können je nach TTL einige Minuten bis Stunden brauchen, bis sie überall sichtbar sind. Du kannst
            trotzdem fortfahren und die Prüfung später unter Verwaltung → Domains wiederholen.
          </Notice>
        </div>
      )}
    </WizardStep>
  );
}

// ---------------------------------------------------------------------------
// 4. System check
// ---------------------------------------------------------------------------

function CheckCard({
  icon: Icon,
  title,
  status,
  label,
  children,
}: {
  icon: LucideIcon;
  title: string;
  status: CheckStatus;
  label?: string;
  children: ReactNode;
}) {
  const tone = checkTone(status);
  const ring =
    tone === "red"
      ? "border-red-200 dark:border-red-500/30"
      : tone === "amber"
        ? "border-amber-200 dark:border-amber-500/30"
        : "border-zinc-200 dark:border-zinc-800";
  return (
    <div className={cn("rounded-xl border bg-white p-4 dark:bg-zinc-900", ring)}>
      <div className="mb-2 flex items-center justify-between gap-2">
        <div className="flex items-center gap-2 text-sm font-semibold text-zinc-900 dark:text-zinc-100">
          <Icon className="h-4 w-4 text-zinc-400" /> {title}
        </div>
        <CheckBadge status={status} label={label} />
      </div>
      <div className="space-y-2 text-sm text-zinc-600 dark:text-zinc-300">{children}</div>
    </div>
  );
}

function tlsStatusOf(status: string): CheckStatus {
  if (/fehl|error|fail|expired|abgelaufen/i.test(status)) return "error";
  if (/ok|valid|gültig|aktiv|active/i.test(status)) return "ok";
  return "info";
}

export function SystemStep({ onBack, onNext }: StepProps) {
  const q = useQuery({ queryKey: setupKeys.systemCheck, queryFn: adminApi.systemCheck, staleTime: 0 });
  const c = q.data;

  return (
    <WizardStep
      icon={Network}
      title="System-Check"
      description="Netzwerk und Dienste aus Sicht des Servers."
      footer={
        <WizardNav
          onBack={onBack}
          onNext={onNext}
          extra={
            <Button icon={RefreshCw} onClick={() => void q.refetch()} loading={q.isFetching}>
              Erneut prüfen
            </Button>
          }
        />
      }
    >
      {q.isLoading ? (
        <div className="grid gap-4 sm:grid-cols-2">
          {Array.from({ length: 6 }, (_, i) => (
            <Skeleton key={i} className="h-28 rounded-xl" />
          ))}
        </div>
      ) : q.error || !c ? (
        <ErrorState error={q.error} onRetry={() => void q.refetch()} />
      ) : (
        <div className="grid gap-4 sm:grid-cols-2">
          <CheckCard icon={Globe} title="Hostname" status={c.hostnameIps.length > 0 ? "ok" : "error"}>
            <code className="font-mono text-[13px] text-zinc-800 dark:text-zinc-200">{c.hostname}</code>
            {c.hostnameIps.length > 0 ? (
              <div className="flex flex-wrap gap-1.5">
                {c.hostnameIps.map((ip) => (
                  <Badge key={ip} className="font-mono">
                    {ip}
                  </Badge>
                ))}
              </div>
            ) : (
              <p className="text-xs text-red-600 dark:text-red-400">Der Hostname löst nicht auf. Lege einen A- bzw. AAAA-Eintrag an.</p>
            )}
          </CheckCard>

          <CheckCard
            icon={Network}
            title="Reverse-DNS (PTR)"
            status={c.ptr.length === 0 ? "warning" : c.ptr.every((p) => p.matches) ? "ok" : "warning"}
          >
            {c.ptr.length === 0 ? (
              <p className="text-xs text-zinc-500 dark:text-zinc-400">Keine IP-Adressen zum Prüfen gefunden.</p>
            ) : (
              <ul className="space-y-1.5">
                {c.ptr.map((p) => (
                  <li key={p.ip} className="flex items-start gap-2 text-xs">
                    {p.matches ? (
                      <CircleCheck className="mt-px h-3.5 w-3.5 shrink-0 text-emerald-500" />
                    ) : (
                      <TriangleAlert className="mt-px h-3.5 w-3.5 shrink-0 text-amber-500" />
                    )}
                    <span className="min-w-0 break-all">
                      <code className="font-mono">{p.ip}</code> →{" "}
                      <code className="font-mono">{p.names.length > 0 ? p.names.join(", ") : "kein PTR"}</code>
                    </span>
                  </li>
                ))}
              </ul>
            )}
            {c.ptr.some((p) => !p.matches) && (
              <p className="text-xs text-zinc-500 dark:text-zinc-400">
                Der PTR-Eintrag wird beim Hoster bzw. Provider gesetzt und sollte auf <code className="font-mono">{c.hostname}</code> zeigen. Viele
                Empfänger lehnen Mails sonst ab.
              </p>
            )}
          </CheckCard>

          <CheckCard icon={Send} title="Ausgehender Port 25" status={c.port25Outbound} label={c.port25Outbound === "error" ? "Gesperrt" : undefined}>
            {c.port25Detail && <p className="text-xs">{c.port25Detail}</p>}
            {c.port25Outbound === "error" && (
              <div className="space-y-2 text-xs">
                <p className="text-zinc-500 dark:text-zinc-400">
                  Dein Provider blockiert ausgehende Verbindungen auf Port 25. Versende über einen Smarthost, indem du diese
                  Umgebungsvariablen am Container setzt:
                </p>
                <CommandBox command="POSTWERK_RELAY_HOST=smtp.anbieter.de" />
                <CommandBox command="POSTWERK_RELAY_USER=benutzer" />
                <CommandBox command="POSTWERK_RELAY_PASSWORD=geheim" />
              </div>
            )}
          </CheckCard>

          <CheckCard icon={Lock} title="TLS" status={tlsStatusOf(c.tls)}>
            <p className="break-words text-xs">{c.tls || "Keine Angabe"}</p>
          </CheckCard>

          <CheckCard icon={Bug} title="Virenscan (ClamAV)" status={c.clamav} label={c.clamav === "info" ? "Deaktiviert" : undefined}>
            <p className="break-words text-xs">{c.clamavDetail || "Keine Angabe"}</p>
          </CheckCard>

          <CheckCard icon={Bot} title="KI" status={c.ai} label={c.ai === "info" ? "Deaktiviert" : undefined}>
            <p className="break-words text-xs">{c.aiDetail || "Keine Angabe"}</p>
          </CheckCard>
        </div>
      )}
    </WizardStep>
  );
}
