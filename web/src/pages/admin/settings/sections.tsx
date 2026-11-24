import { useMemo, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { useMutation } from "@tanstack/react-query";
import { ArrowRight, CircleCheck, CircleX, Info, PlugZap, RefreshCw, Zap } from "lucide-react";
import { adminApi } from "@/api/endpoints";
import type { AiTestResult, RelayTestResult, Settings, SettingsUpdate, TlsMode } from "@/api/types";
import { useDraft } from "@/lib/hooks";
import { domainFromHostname } from "@/lib/setup";
import { cn } from "@/lib/utils";
import { errorMessage } from "@/api/client";
import { AiConnectionFields, AiProviderPicker, AiTestDetails } from "@/pages/admin/ai/AiProviderParts";
import {
  Badge,
  Button,
  Card,
  CardBody,
  CardHeader,
  ChipInput,
  Field,
  FormSection,
  Input,
  PasswordInput,
  SaveBar,
  Select,
  SwitchRow,
  useToast,
} from "@/components/ui";
import {
  DetectNotice,
  ProxyCheckPanel,
  ProxyGuidePanel,
  ProxyKindPicker,
  PublicUrlField,
  SecureCookiesRow,
  TrustedProxiesField,
  UpstreamField,
  isHttpUrl,
  useCookieFollowsUrl,
  useGuideInput,
  useProxyDetect,
  webOf,
  withProxySuggestions,
  type WebSettings,
} from "@/pages/admin/proxy/ProxyParts";
import {
  AcmeNotice,
  HttpPortPicker,
  IssueCertificateButton,
  CertificateFilesPanel,
  SelfSignedNotice,
  TlsModePicker,
  TlsStatusBox,
  tlsDraftErrors,
  tlsDraftOf,
  tlsDraftValid,
  tlsUpdate,
} from "@/pages/admin/tls/CertificateParts";
import {
  DynDNSAdvancedFields,
  DynDNSFields,
  DynDNSStatusPanel,
  RelayEnvNotice,
  RelayFields,
  RelayTestPanel,
  StratoPrepNotice,
  dyndnsChange,
  dyndnsOf,
  dyndnsValid,
  relayChange,
  relayOf,
  relayValid,
  routerDynDNSHint,
  useDynDNSPresets,
  useDynDNSStatus,
  useDynDNSUpdate,
  useRelayPresets,
  useRelayTest,
  withDynDNSSuggestions,
  withRelaySuggestions,
  type DynDNSSettings,
  type RelaySettings,
} from "@/pages/admin/dynip/DynIpParts";
import type { SectionProps } from "./SettingsPage";

const MASK = "••••";

export function Shell({
  title,
  description,
  dirty,
  saving,
  onSave,
  onReset,
  children,
  actions,
}: {
  title: string;
  description: string;
  dirty: boolean;
  saving: boolean;
  onSave: () => void;
  onReset: () => void;
  children: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <>
      <Card>
        <CardHeader title={title} description={description} actions={actions} />
        <CardBody className="divide-y divide-zinc-100 dark:divide-zinc-800">{children}</CardBody>
      </Card>
      <SaveBar dirty={dirty} saving={saving} onSave={onSave} onReset={onReset} />
    </>
  );
}

export function NumberInput({
  id,
  value,
  onChange,
  min,
  max,
  step,
  suffix,
  className,
}: {
  id?: string;
  value: number;
  onChange: (v: number) => void;
  min?: number;
  max?: number;
  step?: number;
  suffix?: string;
  className?: string;
}) {
  return (
    <div className={cn("flex items-center gap-2", className)}>
      <Input
        id={id}
        type="number"
        value={Number.isFinite(value) ? String(value) : ""}
        min={min}
        max={max}
        step={step}
        onChange={(e) => onChange(e.target.value === "" ? 0 : Number(e.target.value))}
        className="w-36 tabular-nums"
      />
      {suffix && <span className="text-sm text-zinc-500 dark:text-zinc-400">{suffix}</span>}
    </div>
  );
}

/** Password-style input for masked secrets: shows a hint when a value is stored. */
export function SecretInput({ id, value, onChange, placeholder }: { id?: string; value: string; onChange: (v: string) => void; placeholder?: string }) {
  const masked = value === MASK;
  return (
    <PasswordInput
      id={id}
      value={masked ? "" : value}
      placeholder={masked ? "•••••••• (gespeichert – zum Ändern neu eingeben)" : placeholder}
      autoComplete="new-password"
      onChange={(e) => onChange(e.target.value === "" && masked ? MASK : e.target.value)}
    />
  );
}

// ---------------------------------------------------------------------------

export function ServerSection({ settings, save, saving }: SectionProps) {
  const { draft, setDraft, dirty, reset } = useDraft(settings.server);
  if (!draft) return null;
  const mb = draft.maxMessageSize / (1024 * 1024);
  const retention = retentionOf(draft);
  return (
    <Shell
      title="Server"
      description="Grundlegende Angaben zu diesem Mailserver."
      dirty={dirty}
      saving={saving}
      onReset={reset}
      onSave={() => save({ server: draft })}
    >
      <FormSection title="Identität" description="Der Hostname wird im SMTP-Banner, in HELO/EHLO und für TLS-Zertifikate verwendet.">
        <Field label="Hostname" help="Vollqualifizierter Name, auf den der MX-Eintrag zeigt, z. B. mail.beispiel.de.">
          {(id) => <Input id={id} value={draft.hostname} onChange={(e) => setDraft((d) => ({ ...d, hostname: e.target.value }))} placeholder="mail.beispiel.de" />}
        </Field>
      </FormSection>
      <FormSection title="Limits" description="Größere Nachrichten werden bereits beim Empfang abgelehnt.">
        <Field label="Maximale Nachrichtengröße" help="Inklusive Anhänge und Kodierung (Base64 vergrößert Anhänge um ca. 33 %).">
          {(id) => (
            <NumberInput
              id={id}
              value={Math.round(mb * 10) / 10}
              min={1}
              step={1}
              suffix="MB"
              onChange={(v) => setDraft((d) => ({ ...d, maxMessageSize: Math.round(v * 1024 * 1024) }))}
            />
          )}
        </Field>
      </FormSection>
      <FormSection
        title="Aufbewahrung"
        description="Papierkorb und Spam-Ordner aller Konten werden automatisch geleert. Gezählt wird ab dem Zeitpunkt, an dem eine Nachricht im Ordner gelandet ist. 0 = nie automatisch leeren."
      >
        <Field label="Papierkorb leeren nach" help="Nachrichten im Papierkorb werden nach dieser Zeit endgültig gelöscht.">
          {(id) => (
            <NumberInput
              id={id}
              value={retention.trashDays}
              min={0}
              max={3650}
              step={1}
              suffix="Tagen"
              onChange={(v) => setDraft((d) => ({ ...d, retention: { ...retentionOf(d), trashDays: Math.max(0, Math.round(v)) } }))}
            />
          )}
        </Field>
        <Field label="Spam-Ordner leeren nach" help="Nachrichten im Spam-Ordner werden nach dieser Zeit endgültig gelöscht.">
          {(id) => (
            <NumberInput
              id={id}
              value={retention.junkDays}
              min={0}
              max={3650}
              step={1}
              suffix="Tagen"
              onChange={(v) => setDraft((d) => ({ ...d, retention: { ...retentionOf(d), junkDays: Math.max(0, Math.round(v)) } }))}
            />
          )}
        </Field>
      </FormSection>
    </Shell>
  );
}

/** Retention values with defaults for settings saved by older versions. */
function retentionOf(s: Settings["server"]): NonNullable<Settings["server"]["retention"]> {
  return s.retention ?? { trashDays: 30, junkDays: 30 };
}

export function WebSection({ settings, save, saving }: SectionProps) {
  const toast = useToast();
  const source = useMemo(() => webOf(settings.web), [settings.web]);
  const { draft, setDraft, dirty, reset } = useDraft(source);
  const detect = useProxyDetect();
  const guide = useGuideInput(detect.data);
  const hostname = settings.server.hostname;
  const touchCookies = useCookieFollowsUrl(draft?.publicUrl, (v) => setDraft((d) => ({ ...d, secureCookies: v })));
  if (!draft) return null;
  const set = (patch: Partial<WebSettings>) => setDraft((d) => ({ ...d, ...patch }));
  const urlInvalid = (draft.behindProxy || !!draft.publicUrl.trim()) && !isHttpUrl(draft.publicUrl);

  return (
    <Shell
      title="Web & Proxy"
      description="Öffentliche Adresse der Weboberfläche und Betrieb hinter einem Reverse-Proxy. Änderungen wirken sofort."
      dirty={dirty}
      saving={saving}
      onReset={reset}
      onSave={() =>
        urlInvalid ? toast.error("Bitte eine gültige öffentliche URL mit http:// oder https:// angeben.", "Nicht gespeichert") : save({ web: draft })
      }
    >
      <FormSection title="Reverse-Proxy" description="Nur die Weboberfläche läuft über den Proxy – die Mail-Ports werden direkt an Postwerk weitergeleitet.">
        <SwitchRow
          label="Postwerk läuft hinter einem Reverse-Proxy"
          description="Postwerk wertet dann X-Forwarded-For und X-Forwarded-Proto vertrauenswürdiger Proxys aus."
          checked={draft.behindProxy}
          onChange={(v) => setDraft((d) => (v ? withProxySuggestions(d, detect.data, hostname) : { ...d, behindProxy: false }))}
        />
        <DetectNotice detect={detect.data} loading={detect.isLoading} />
        {draft.behindProxy && (
          <TrustedProxiesField value={draft.trustedProxies} onChange={(v) => set({ trustedProxies: v })} suggestion={detect.data?.suggestedTrustedProxy} />
        )}
      </FormSection>
      <FormSection title="Adresse & Cookies" description="Unter welcher Adresse die Weboberfläche erreichbar ist.">
        <PublicUrlField value={draft.publicUrl} onChange={(v) => set({ publicUrl: v })} hostname={hostname} />
        <SecureCookiesRow
          checked={draft.secureCookies}
          onChange={(v) => {
            touchCookies();
            set({ secureCookies: v });
          }}
        />
      </FormSection>
      <FormSection title="Senden" description="Wartezeit vor dem Senden von Webmail-Nachrichten, damit ein versehentlicher Versand rückgängig gemacht werden kann (optional, standardmäßig aus).">
        <Field label="Senden rückgängig machen" help="0 = aus. Der Versand wird für diese Sekundenzahl verzögert; in der Wartezeit bietet das Webmail „Rückgängig“ an.">
          {(id) => (
            <NumberInput
              id={id}
              value={draft.undoSendSeconds}
              min={0}
              max={300}
              step={5}
              suffix="Sekunden"
              onChange={(v) => set({ undoSendSeconds: v })}
            />
          )}
        </Field>
      </FormSection>
      {draft.behindProxy && (
        <FormSection title="Anleitung" description="Konfiguration für deinen Proxy. Proxy-Typ und Zieladresse werden nur in diesem Browser gemerkt.">
          <ProxyKindPicker value={guide.kind} onChange={guide.setKind} compact />
          <UpstreamField value={guide.upstream} onChange={guide.setUpstream} suggestion={guide.suggestion} />
          <ProxyGuidePanel
            kind={guide.kind}
            upstream={guide.upstream}
            hostname={hostname}
            certificateAction={
              <Link
                to="/admin/settings/tls"
                className="inline-flex items-center gap-1.5 text-sm font-medium text-indigo-600 hover:underline dark:text-indigo-300"
              >
                Zur Zertifikatsauswahl <ArrowRight className="h-3.5 w-3.5" />
              </Link>
            }
          />
        </FormSection>
      )}
      {draft.behindProxy && (
        <FormSection title="Verbindung prüfen" description="Zeigt, wie Postwerk deine aktuelle Anfrage sieht.">
          <ProxyCheckPanel
            detect={detect.data}
            loading={detect.isFetching}
            error={detect.error}
            onRecheck={() => void detect.refetch()}
            note={dirty ? "Die Prüfung verwendet die gespeicherte Konfiguration – speichere zuerst, um Änderungen zu prüfen." : undefined}
          />
        </FormSection>
      )}
    </Shell>
  );
}

export function DynDNSSection({ settings, save, saving }: SectionProps) {
  const toast = useToast();
  const source = useMemo(() => dyndnsOf(settings.dyndns), [settings.dyndns]);
  const { draft, setDraft, reset } = useDraft(source);
  const [showErrors, setShowErrors] = useState(false);
  const presets = useDynDNSPresets();
  const status = useDynDNSStatus(source.enabled);
  const update = useDynDNSUpdate();
  if (!draft) return null;
  const change = dyndnsChange(draft, source);
  const hostname = settings.server.hostname;
  const set = (v: DynDNSSettings) => setDraft(() => v);

  const onSave = () => {
    if (!dyndnsValid(draft)) {
      setShowErrors(true);
      toast.error("Bitte die markierten Felder ausfüllen.", "Nicht gespeichert");
      return;
    }
    if (change) save({ dyndns: change });
  };

  return (
    <Shell
      title="DynDNS"
      description="Hält den A-Record des Mailserver-Hostnamens bei wechselnder IP aktuell. Postwerk meldet nur, wenn sich die IP ändert."
      dirty={change !== null}
      saving={saving}
      onReset={() => {
        reset();
        setShowErrors(false);
      }}
      onSave={onSave}
      actions={
        <Button
          icon={RefreshCw}
          loading={update.isPending}
          disabled={!source.enabled || change !== null}
          onClick={() => update.mutate(undefined, { onError: (err) => toast.error(err, "Aktualisierung fehlgeschlagen") })}
        >
          Jetzt aktualisieren
        </Button>
      }
    >
      <FormSection title="Allgemein" description={routerDynDNSHint}>
        <SwitchRow
          label="DynDNS über Postwerk aktualisieren"
          description="Für Internetanschlüsse mit wechselnder IP-Adresse."
          checked={draft.enabled}
          onChange={(v) => setDraft((d) => (v ? withDynDNSSuggestions(d, hostname, presets.data) : { ...d, enabled: false }))}
        />
      </FormSection>
      {draft.enabled && (
        <FormSection title="Anbieter & Zugang" description="dyndns2-Dienst, die zu aktualisierenden Hostnamen und die Zugangsdaten.">
          <DynDNSFields
            value={draft}
            onChange={set}
            presets={presets.data}
            presetsLoading={presets.isLoading}
            hostname={hostname}
            showErrors={showErrors}
            compact
            advanced="none"
            afterPresets={draft.preset === "strato" ? <StratoPrepNotice hostname={hostname} domain={domainFromHostname(hostname)} /> : undefined}
          />
        </FormSection>
      )}
      {draft.enabled && (
        <FormSection title="Erweitert" description="Dienst zur Ermittlung der öffentlichen IP und Prüfintervall.">
          <DynDNSAdvancedFields value={draft} onChange={set} showErrors={showErrors} />
        </FormSection>
      )}
      {(source.enabled || update.data) && (
        <FormSection title="Status" description="Öffentliche IP und A-Records laut DNS.">
          <DynDNSStatusPanel
            status={status.data}
            loading={status.isFetching}
            error={status.error}
            note={change ? "„Jetzt aktualisieren“ verwendet die gespeicherte Konfiguration – speichere zuerst." : undefined}
          />
        </FormSection>
      )}
    </Shell>
  );
}

export function RelaySection({ settings, save, saving }: SectionProps) {
  const toast = useToast();
  const source = useMemo(() => relayOf(settings.relay), [settings.relay]);
  const { draft, setDraft, reset } = useDraft(source);
  const [showErrors, setShowErrors] = useState(false);
  const [result, setResult] = useState<RelayTestResult | null>(null);
  const presets = useRelayPresets();
  const test = useRelayTest();
  if (!draft) return null;
  const envOverride = !!presets.data?.envOverride;
  const change = envOverride ? null : relayChange(draft, source);
  const set = (v: RelaySettings) => setDraft(() => v);

  const onSave = () => {
    if (!relayValid(draft)) {
      setShowErrors(true);
      toast.error("Bitte die markierten Felder ausfüllen.", "Nicht gespeichert");
      return;
    }
    if (change) save({ relay: change });
  };

  return (
    <Shell
      title="Smarthost"
      description="Ausgehende Mails über den SMTP-Server eines Anbieters verschicken – bei dynamischer IP nötig, weil große Mailanbieter Privatkunden-IPs ablehnen."
      dirty={change !== null}
      saving={saving}
      onReset={() => {
        reset();
        setShowErrors(false);
      }}
      onSave={onSave}
      actions={
        <Button
          icon={PlugZap}
          loading={test.isPending}
          disabled={!source.enabled && !envOverride}
          onClick={() => {
            setResult(null);
            test.mutate(undefined, { onSuccess: setResult, onError: (err) => setResult({ ok: false, detail: errorMessage(err) }) });
          }}
        >
          Verbindung testen
        </Button>
      }
    >
      {(result || test.isPending) && (
        <div className="space-y-2 pb-6">
          <RelayTestPanel result={result} pending={test.isPending} />
          {change && !test.isPending && (
            <p className="flex items-center gap-1.5 text-xs text-zinc-500 dark:text-zinc-400">
              <Info className="h-3.5 w-3.5" /> Der Test verwendet die gespeicherte Konfiguration. Speichere zuerst, um Änderungen zu testen.
            </p>
          )}
        </div>
      )}
      <FormSection title="Allgemein" description="Ohne Smarthost stellt Postwerk ausgehende Mails direkt beim Server des Empfängers zu.">
        {envOverride && <RelayEnvNotice />}
        <SwitchRow
          label="Ausgehende Mails über einen Smarthost senden"
          description="Empfohlen bei dynamischer IP."
          checked={draft.enabled}
          disabled={envOverride}
          onChange={(v) => setDraft((d) => (v ? withRelaySuggestions(d, presets.data?.presets) : { ...d, enabled: false }))}
        />
      </FormSection>
      {(draft.enabled || envOverride) && (
        <FormSection title="Server & Zugang" description="Anbieter wählen oder eigenen SMTP-Server eintragen.">
          <RelayFields
            value={draft}
            onChange={set}
            presets={presets.data?.presets}
            presetsLoading={presets.isLoading}
            readOnly={envOverride}
            showErrors={showErrors}
            compact
          />
        </FormSection>
      )}
    </Shell>
  );
}

const ACME_PRESETS = [
  { label: "Let's Encrypt", url: "https://acme-v02.api.letsencrypt.org/directory" },
  { label: "Let's Encrypt (Staging)", url: "https://acme-staging-v02.api.letsencrypt.org/directory" },
  { label: "ZeroSSL", url: "https://acme.zerossl.com/v2/DV90" },
];

export function TlsSection({ settings, save, saving }: SectionProps) {
  const toast = useToast();
  const source = useMemo(() => tlsDraftOf(settings.tls), [settings.tls]);
  const { draft, setDraft, dirty, reset } = useDraft(source);
  if (!draft) return null;
  const setAcme = (patch: Partial<Settings["tls"]["acme"]>) => setDraft((d) => ({ ...d, acme: { ...d.acme, ...patch } }));
  const setMode = (mode: TlsMode) =>
    setDraft((d) => {
      const acme = { ...d.acme, enabled: mode === "acme" };
      if (mode === "acme") {
        if (acme.domains.length === 0 && settings.server.hostname) acme.domains = [settings.server.hostname];
        if (!acme.directoryUrl) acme.directoryUrl = ACME_PRESETS[0].url;
      }
      const httpPort = mode === "acme" && d.httpPort === "open" ? "auto-close" : d.httpPort;
      return { ...d, mode, acme, httpPort };
    });
  const errors = tlsDraftErrors(draft);
  const behindProxy = !!settings.web?.behindProxy;

  return (
    <Shell
      title="TLS / Zertifikate"
      description="Zertifikat für SMTP, IMAP und POP3. Änderungen wirken ohne Neustart."
      dirty={dirty}
      saving={saving}
      onReset={reset}
      onSave={() =>
        tlsDraftValid(draft)
          ? save(tlsUpdate(draft))
          : toast.error(
              draft.mode === "files" ? "Bitte Zertifikat und Schlüssel angeben." : "Bitte E-Mail-Adresse und mindestens eine Domain angeben.",
              "Nicht gespeichert",
            )
      }
    >
      <FormSection title="Status" description="Aktueller Zustand des Zertifikats laut Server.">
        <TlsStatusBox status={settings.tls.status} />
      </FormSection>
      <FormSection title="Zertifikatsquelle" description="Woher Postwerk das Zertifikat für die Mail-Ports bezieht.">
        <TlsModePicker value={draft.mode} onChange={setMode} recommended={behindProxy ? "files" : "acme"} stacked />
      </FormSection>
      {draft.mode === "files" && (
        <FormSection
          title="Zertifikat vom Proxy"
          description={
            <>
              Der Zertifikatsordner des Proxys muss schreibgeschützt nach <code className="font-mono">/certs</code> eingebunden sein.
            </>
          }
        >
          <CertificateFilesPanel value={draft.files} onChange={(files) => setDraft((d) => ({ ...d, files }))} hostname={settings.server.hostname} />
        </FormSection>
      )}
      {draft.mode === "acme" && (
        <FormSection title="ACME" description="Zertifikate werden per ACME (z. B. Let's Encrypt) bezogen und automatisch erneuert.">
          <Field label="Kontakt-E-Mail" help="Die Zertifizierungsstelle schickt hierhin Ablaufwarnungen." error={draft.acme.email && errors.email ? "Ungültige E-Mail-Adresse." : null}>
            {(id) => <Input id={id} type="email" value={draft.acme.email} onChange={(e) => setAcme({ email: e.target.value })} placeholder="admin@beispiel.de" />}
          </Field>
          <Field label="ACME-Verzeichnis" help="Zum Testen die Staging-Umgebung verwenden, um Rate-Limits zu vermeiden.">
            {(id) => (
              <div className="space-y-2">
                <Input id={id} value={draft.acme.directoryUrl} onChange={(e) => setAcme({ directoryUrl: e.target.value })} className="font-mono text-[13px]" />
                <div className="flex flex-wrap gap-1.5">
                  {ACME_PRESETS.map((p) => (
                    <button
                      key={p.url}
                      type="button"
                      onClick={() => setAcme({ directoryUrl: p.url })}
                      className={cn(
                        "rounded-md px-2 py-1 text-xs ring-1 ring-inset transition disabled:opacity-50",
                        draft.acme.directoryUrl === p.url
                          ? "bg-indigo-50 text-indigo-700 ring-indigo-200 dark:bg-indigo-500/15 dark:text-indigo-300 dark:ring-indigo-500/30"
                          : "text-zinc-600 ring-zinc-200 hover:bg-zinc-50 dark:text-zinc-400 dark:ring-zinc-700 dark:hover:bg-zinc-800",
                      )}
                    >
                      {p.label}
                    </button>
                  ))}
                </div>
              </div>
            )}
          </Field>
          <Field
            label="Domains"
            help="Alle Namen, für die ein Zertifikat ausgestellt werden soll (z. B. mail.beispiel.de, autoconfig.beispiel.de)."
            error={errors.domains ? "Mindestens ein Name ist erforderlich." : null}
          >
            {(id) => <ChipInput id={id} value={draft.acme.domains} onChange={(v) => setAcme({ domains: v })} placeholder="mail.beispiel.de" transform={(v) => v.toLowerCase()} />}
          </Field>
          <AcmeNotice behindProxy={behindProxy} />
          <HttpPortPicker value={draft.httpPort} onChange={(v) => setDraft((d) => ({ ...d, httpPort: v }))} status={settings.tls.httpStatus} />
          <IssueCertificateButton
            disabledReason={dirty ? "Erst speichern, dann das Zertifikat holen." : settings.tls.mode !== "acme" ? "Erst „Let’s Encrypt direkt“ speichern." : null}
          />
        </FormSection>
      )}
      {draft.mode === "selfsigned" && (
        <FormSection title="Selbstsigniert" description="Postwerk erzeugt sich selbst ein Zertifikat.">
          <SelfSignedNotice />
        </FormSection>
      )}
    </Shell>
  );
}

export function SpamSection({ settings, save, saving }: SectionProps) {
  const { draft, setDraft, dirty, reset } = useDraft(settings.spam);
  if (!draft) return null;
  const thresholdError = draft.rejectThreshold <= draft.junkThreshold ? "Der Ablehnungs-Schwellwert sollte über dem Junk-Schwellwert liegen." : null;
  return (
    <Shell
      title="Spamfilter"
      description="Bewertung eingehender Nachrichten und Schwellwerte."
      dirty={dirty}
      saving={saving}
      onReset={reset}
      onSave={() => save({ spam: draft })}
    >
      <FormSection title="Allgemein" description="Jede Nachricht erhält einen Spam-Score. Ab den Schwellwerten wird sie in Junk verschoben bzw. abgelehnt.">
        <SwitchRow label="Spamfilter aktivieren" checked={draft.enabled} onChange={(v) => setDraft((d) => ({ ...d, enabled: v }))} />
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Junk ab Score" help="Nachricht landet im Junk-Ordner.">
            {(id) => <NumberInput id={id} value={draft.junkThreshold} step={0.5} onChange={(v) => setDraft((d) => ({ ...d, junkThreshold: v }))} />}
          </Field>
          <Field label="Ablehnen ab Score" help="Annahme wird mit 5xx verweigert." error={thresholdError}>
            {(id) => <NumberInput id={id} value={draft.rejectThreshold} step={0.5} onChange={(v) => setDraft((d) => ({ ...d, rejectThreshold: v }))} />}
          </Field>
        </div>
      </FormSection>
      <FormSection title="Greylisting" description="Unbekannte Absender werden beim ersten Versuch temporär abgewiesen. Echte Mailserver versuchen es später erneut.">
        <SwitchRow
          label="Greylisting aktivieren"
          checked={draft.greylisting.enabled}
          onChange={(v) => setDraft((d) => ({ ...d, greylisting: { ...d.greylisting, enabled: v } }))}
        />
        <Field label="Verzögerung">
          {(id) => (
            <NumberInput
              id={id}
              value={draft.greylisting.delaySeconds}
              min={0}
              suffix="Sekunden"
              onChange={(v) => setDraft((d) => ({ ...d, greylisting: { ...d.greylisting, delaySeconds: v } }))}
            />
          )}
        </Field>
      </FormSection>
      <FormSection title="DNS-Blocklisten" description="IP-Adressen einliefernder Server werden gegen öffentliche Blocklisten geprüft.">
        <SwitchRow label="DNSBL-Prüfung aktivieren" checked={draft.dnsbl.enabled} onChange={(v) => setDraft((d) => ({ ...d, dnsbl: { ...d.dnsbl, enabled: v } }))} />
        <Field label="Server" help="Hostnamen der Blocklisten, z. B. zen.spamhaus.org.">
          {(id) => (
            <ChipInput
              id={id}
              value={draft.dnsbl.servers}
              onChange={(v) => setDraft((d) => ({ ...d, dnsbl: { ...d.dnsbl, servers: v } }))}
              placeholder="zen.spamhaus.org"
              transform={(v) => v.toLowerCase()}
              suggestions={["zen.spamhaus.org", "bl.spamcop.net", "b.barracudacentral.org"]}
            />
          )}
        </Field>
      </FormSection>
      <FormSection title="Bayes-Filter" description="Lernt aus als Spam/kein Spam markierten Nachrichten.">
        <SwitchRow label="Bayes-Filter aktivieren" checked={draft.bayes.enabled} onChange={(v) => setDraft((d) => ({ ...d, bayes: { ...d.bayes, enabled: v } }))} />
        <SwitchRow
          label="Automatisch lernen"
          description="Eindeutige Nachrichten (sehr hoher bzw. niedriger Score) automatisch zum Training verwenden."
          checked={draft.bayes.autoLearn}
          onChange={(v) => setDraft((d) => ({ ...d, bayes: { ...d.bayes, autoLearn: v } }))}
        />
        <Field label="Mindestanzahl Tokens" help="Erst ab dieser Menge gelernter Tokens fließt das Bayes-Ergebnis in den Score ein.">
          {(id) => <NumberInput id={id} value={draft.bayes.minTokens} min={0} onChange={(v) => setDraft((d) => ({ ...d, bayes: { ...d.bayes, minTokens: v } }))} />}
        </Field>
      </FormSection>
    </Shell>
  );
}

export function AntivirusSection({ settings, save, saving }: SectionProps) {
  const { draft, setDraft, dirty, reset } = useDraft(settings.antivirus);
  if (!draft) return null;
  return (
    <Shell
      title="Virenscan"
      description="Prüfung eingehender Nachrichten mit ClamAV."
      dirty={dirty}
      saving={saving}
      onReset={reset}
      onSave={() => save({ antivirus: draft })}
    >
      <FormSection title="ClamAV" description="Postwerk verbindet sich per TCP oder Unix-Socket mit clamd.">
        <SwitchRow label="Virenscan aktivieren" checked={draft.enabled} onChange={(v) => setDraft((d) => ({ ...d, enabled: v }))} />
        <Field label="clamd-Adresse" help="z. B. tcp://clamav:3310 oder unix:///run/clamav/clamd.sock">
          {(id) => (
            <Input
              id={id}
              value={draft.clamdAddress}
              onChange={(e) => setDraft((d) => ({ ...d, clamdAddress: e.target.value }))}
              placeholder="tcp://clamav:3310"
              className="font-mono text-[13px]"
              disabled={!draft.enabled}
            />
          )}
        </Field>
        <Field label="Aktion bei Fund" help="Quarantäne stellt die Nachricht mit Warnhinweis zu, Ablehnen verweigert die Annahme.">
          {(id) => (
            <Select
              id={id}
              value={draft.action}
              disabled={!draft.enabled}
              onChange={(e) => setDraft((d) => ({ ...d, action: e.target.value as Settings["antivirus"]["action"] }))}
              options={[
                { value: "reject", label: "Ablehnen" },
                { value: "quarantine", label: "Quarantäne" },
              ]}
              className="max-w-xs"
            />
          )}
        </Field>
      </FormSection>
    </Shell>
  );
}

const featureLabels: { key: keyof Settings["ai"]["features"]; label: string; description: string }[] = [
  { key: "spamCheck", label: "Spam-Bewertung", description: "Die KI fließt als zusätzliches Signal in den Spam-Score ein." },
  { key: "classify", label: "Kategorisierung", description: "Eingehende Nachrichten einer der Kategorien zuordnen." },
  { key: "priority", label: "Priorität", description: "Wichtige Nachrichten erkennen und hervorheben." },
  { key: "summarize", label: "Zusammenfassungen", description: "Kurze Zusammenfassungen von Nachrichten und Threads." },
  { key: "replyDraft", label: "Antwortentwürfe", description: "Antwortvorschläge im Webmail erzeugen." },
  { key: "semanticSearch", label: "Semantische Suche", description: "Bedeutungsbasierte Suche über Embeddings." },
  { key: "extractEvents", label: "Termine & Aufgaben", description: "Termine und Aufgaben aus Nachrichten erkennen." },
];

export function AiSection({ settings, save, saving }: SectionProps) {
  const { draft, setDraft, dirty, reset } = useDraft(settings.ai);
  const [result, setResult] = useState<AiTestResult | null>(null);
  const test = useMutation({
    mutationFn: adminApi.testAi,
    onSuccess: setResult,
    onError: (err) => setResult({ ok: false, latencyMs: 0, models: [], error: errorMessage(err) }),
  });
  if (!draft) return null;

  const set = (patch: Partial<Settings["ai"]>) => setDraft((d) => ({ ...d, ...patch }));

  const onSave = () => {
    // Send every category explicitly ("" = not filed): saved maps are merged.
    const categoryFolders: Record<string, string> = {};
    for (const k of Object.keys(draft.autoFile.categoryFolders)) categoryFolders[k] = "";
    for (const c of draft.categories) categoryFolders[c] = (draft.autoFile.categoryFolders[c] ?? "").trim();
    const payload: SettingsUpdate["ai"] = { ...draft, autoFile: { ...draft.autoFile, categoryFolders } };
    if (draft.apiKey === settings.ai.apiKey) delete payload.apiKey;
    if (draft.embeddingApiKey === settings.ai.embeddingApiKey) delete payload.embeddingApiKey;
    save({ ai: payload });
  };

  return (
    <Shell
      title="KI"
      description="Lokales Modell über Ollama, ein externer Dienst mit OpenAI- oder Anthropic-kompatibler API oder ein ChatGPT-Abo."
      dirty={dirty}
      saving={saving}
      onReset={reset}
      onSave={onSave}
      actions={
        <Button icon={PlugZap} loading={test.isPending} onClick={() => test.mutate()}>
          Verbindung testen
        </Button>
      }
    >
      {(result || test.isPending) && (
        <div className="pb-6">
          <TestResult result={result} pending={test.isPending} onUseModel={(m) => set({ chatModel: m })} dirty={dirty} />
        </div>
      )}
      <FormSection title="Allgemein" description="Ist die KI deaktiviert, blendet das Webmail alle KI-Funktionen aus.">
        <SwitchRow label="KI-Funktionen aktivieren" checked={draft.enabled} onChange={(v) => set({ enabled: v })} />
      </FormSection>
      <FormSection title="Anbieter" description="Wähle, wo das Sprachmodell läuft.">
        <AiProviderPicker value={draft} onChange={set} />
      </FormSection>
      <FormSection title="Verbindung" description="Adresse, Schlüssel und Modelle des gewählten Anbieters.">
        <AiConnectionFields value={draft} onChange={set} />
        <Field label="Zeitlimit" help="Anfragen, die länger dauern, werden abgebrochen.">
          {(id) => <NumberInput id={id} value={draft.timeoutSeconds} min={1} suffix="Sekunden" onChange={(v) => set({ timeoutSeconds: v })} />}
        </Field>
      </FormSection>
      <FormSection title="Funktionen" description="Einzelne Funktionen lassen sich gezielt abschalten, z. B. um Rechenzeit zu sparen.">
        <div className="space-y-4">
          {featureLabels.map((f) => (
            <SwitchRow
              key={f.key}
              label={f.label}
              description={f.description}
              checked={draft.features[f.key]}
              disabled={!draft.enabled}
              onChange={(v) => setDraft((d) => ({ ...d, features: { ...d.features, [f.key]: v } }))}
            />
          ))}
        </div>
      </FormSection>
      <FormSection title="Kategorien" description="Die KI ordnet jede Nachricht genau einer dieser Kategorien zu.">
        <Field label="Kategorien" help="Mit Enter oder Komma hinzufügen.">
          {(id) => (
            <ChipInput
              id={id}
              value={draft.categories}
              onChange={(v) => set({ categories: v })}
              placeholder="z. B. Rechnung"
              suggestions={["Rechnung", "Newsletter", "Support", "Privat", "Arbeit", "Versand"]}
            />
          )}
        </Field>
      </FormSection>
      <AutoFileSection
        value={draft.autoFile}
        categories={draft.categories}
        aiEnabled={draft.enabled}
        classify={draft.features.classify}
        onChange={(autoFile) => set({ autoFile })}
      />
      <FormSection
        title="Tagesbriefing"
        description="Optional: jeden Tag eine Zusammenfassung aller neuen Nachrichten als Mail pro Konto. Standardmäßig aus."
      >
        <SwitchRow
          label="Tagesbriefing aktivieren"
          description="Nutzt die Zusammenfassungs-Funktion; pro Tag und Konto entsteht genau eine Mail."
          checked={draft.briefing.enabled}
          onChange={(v) => set({ briefing: { ...draft.briefing, enabled: v } })}
        />
        {draft.briefing.enabled && (
          <Field label="Uhrzeit" help="Ortszeit des Servers, Format HH:MM.">
            {(id) => (
              <Input
                id={id}
                type="time"
                value={draft.briefing.time}
                onChange={(e) => set({ briefing: { ...draft.briefing, time: e.target.value } })}
                className="w-40"
              />
            )}
          </Field>
        )}
      </FormSection>
    </Shell>
  );
}

function AutoFileSection({
  value,
  categories,
  aiEnabled,
  classify,
  onChange,
}: {
  value: Settings["ai"]["autoFile"];
  categories: string[];
  aiEnabled: boolean;
  classify: boolean;
  onChange: (v: Settings["ai"]["autoFile"]) => void;
}) {
  const set = (patch: Partial<Settings["ai"]["autoFile"]>) => onChange({ ...value, ...patch });
  const disabled = !aiEnabled || !value.enabled;
  return (
    <FormSection
      title="In Ordner einsortieren"
      description="Die KI verschiebt passende Nachrichten nach der Analyse aus dem Posteingang in einen Ordner – auch in Mail-Apps per IMAP sichtbar."
    >
      <SwitchRow
        label="Automatisch einsortieren"
        description="Nur Nachrichten, die noch im Posteingang liegen. Spam und Phishing landen weiterhin im Spam-Ordner."
        checked={value.enabled}
        disabled={!aiEnabled}
        onChange={(v) => set({ enabled: v })}
      />
      <Field label="Zielordner">
        {() => (
          <div className={cn("grid gap-2.5 sm:grid-cols-2", disabled && "pointer-events-none opacity-50")} role="radiogroup" aria-label="Zielordner">
            {(
              [
                { mode: "categories", title: "Nach Kategorie", text: "Jede Kategorie bekommt einen festen Ordner. Fehlende Ordner werden pro Konto angelegt." },
                { mode: "folders", title: "Eigene Ordner der Benutzer", text: "Die KI wählt aus den Ordnern, die jeder Benutzer selbst angelegt hat. Es werden keine neuen Ordner erstellt." },
              ] as const
            ).map((o) => (
              <button
                key={o.mode}
                type="button"
                role="radio"
                aria-checked={value.mode === o.mode}
                onClick={() => set({ mode: o.mode })}
                className={cn(
                  "flex flex-col gap-1 rounded-xl border p-3 text-left transition",
                  value.mode === o.mode
                    ? "border-indigo-400 bg-indigo-50/60 ring-1 ring-indigo-400 dark:border-indigo-500/60 dark:bg-indigo-500/10 dark:ring-indigo-500/60"
                    : "border-zinc-200 hover:border-zinc-300 hover:bg-zinc-50 dark:border-zinc-800 dark:hover:border-zinc-700 dark:hover:bg-zinc-800/50",
                )}
              >
                <span className="text-sm font-medium text-zinc-900 dark:text-zinc-100">{o.title}</span>
                <span className="text-xs leading-relaxed text-zinc-500 dark:text-zinc-400">{o.text}</span>
              </button>
            ))}
          </div>
        )}
      </Field>
      {value.mode === "categories" && (
        <Field label="Ordner je Kategorie" help="Leer = bleibt im Posteingang. Unterordner mit „/“, z. B. Finanzen/Rechnungen.">
          {() =>
            categories.length === 0 ? (
              <p className="text-sm text-zinc-500 dark:text-zinc-400">Lege oben zuerst Kategorien an.</p>
            ) : (
              <div className="space-y-2">
                {!classify && (
                  <p className="flex items-center gap-1.5 text-xs text-amber-700 dark:text-amber-300">
                    <Info className="h-3.5 w-3.5" /> Dafür muss die Funktion „Kategorisierung“ eingeschaltet sein.
                  </p>
                )}
                {categories.map((c) => (
                  <div key={c} className="grid grid-cols-[8rem_auto_1fr] items-center gap-3 sm:grid-cols-[10rem_auto_1fr]">
                    <Badge tone="indigo" className="justify-self-start">{c}</Badge>
                    <ArrowRight className="h-4 w-4 text-zinc-400" />
                    <Input
                      value={value.categoryFolders[c] ?? ""}
                      disabled={disabled}
                      placeholder="bleibt im Posteingang"
                      aria-label={`Ordner für ${c}`}
                      onChange={(e) => set({ categoryFolders: { ...value.categoryFolders, [c]: e.target.value } })}
                    />
                  </div>
                ))}
              </div>
            )
          }
        </Field>
      )}
      <SwitchRow
        label="Wichtige Nachrichten im Posteingang lassen"
        description="Als dringend erkannte Nachrichten (Fristen, Mahnungen, persönliche Bitten) werden nicht verschoben."
        checked={value.keepImportant}
        disabled={disabled}
        onChange={(v) => set({ keepImportant: v })}
      />
    </FormSection>
  );
}

function TestResult({
  result,
  pending,
  onUseModel,
  dirty,
}: {
  result: AiTestResult | null;
  pending: boolean;
  onUseModel: (m: string) => void;
  dirty: boolean;
}) {
  if (pending) {
    return (
      <div className="flex items-center gap-3 rounded-xl border border-zinc-200 bg-zinc-50 p-4 text-sm text-zinc-600 dark:border-zinc-800 dark:bg-zinc-950/50 dark:text-zinc-400">
        <Zap className="h-4 w-4 animate-pulse text-indigo-500" /> Verbindung wird getestet …
      </div>
    );
  }
  if (!result) return null;
  return (
    <div
      className={cn(
        "rounded-xl border p-4",
        result.ok
          ? "border-emerald-200 bg-emerald-50/60 dark:border-emerald-500/30 dark:bg-emerald-500/[0.07]"
          : "border-red-200 bg-red-50/60 dark:border-red-500/30 dark:bg-red-500/[0.07]",
      )}
    >
      <div className="flex flex-wrap items-center gap-2">
        {result.ok ? <CircleCheck className="h-5 w-5 text-emerald-600" /> : <CircleX className="h-5 w-5 text-red-600" />}
        <span className="text-sm font-semibold text-zinc-900 dark:text-zinc-100">
          {result.ok ? "Verbindung erfolgreich" : "Verbindung fehlgeschlagen"}
        </span>
        {result.ok && <Badge tone="green">{result.latencyMs} ms</Badge>}
      </div>
      {result.error && <p className="mt-2 break-words font-mono text-xs text-red-700 dark:text-red-300">{result.error}</p>}
      <AiTestDetails result={result} />
      {result.models.length > 0 && (
        <div className="mt-3">
          <div className="mb-1.5 text-xs text-zinc-500 dark:text-zinc-400">Verfügbare Modelle (klicken, um als Chat-Modell zu übernehmen):</div>
          <div className="flex flex-wrap gap-1.5">
            {result.models.map((m) => (
              <button
                key={m}
                type="button"
                onClick={() => onUseModel(m)}
                className="rounded-md bg-white px-2 py-0.5 font-mono text-xs text-zinc-700 ring-1 ring-inset ring-zinc-200 hover:ring-indigo-400 dark:bg-zinc-900 dark:text-zinc-300 dark:ring-zinc-700"
              >
                {m}
              </button>
            ))}
          </div>
        </div>
      )}
      {dirty && (
        <p className="mt-3 flex items-center gap-1.5 text-xs text-zinc-500 dark:text-zinc-400">
          <Info className="h-3.5 w-3.5" /> Der Test verwendet die gespeicherte Konfiguration. Speichere zuerst, um Änderungen zu testen.
        </p>
      )}
    </div>
  );
}

export function DmsSection({ settings, save, saving }: SectionProps) {
  const { draft, setDraft, dirty, reset } = useDraft(settings.dms.paperless);
  if (!draft) return null;
  const set = (patch: Partial<Settings["dms"]["paperless"]>) => setDraft((d) => ({ ...d, ...patch }));
  const onSave = () => {
    const paperless: NonNullable<NonNullable<SettingsUpdate["dms"]>["paperless"]> = { ...draft };
    if (draft.token === settings.dms.paperless.token) delete paperless.token;
    save({ dms: { paperless } });
  };
  return (
    <Shell
      title="DMS / Paperless"
      description="Anhänge an Paperless-ngx übergeben, manuell oder automatisch."
      dirty={dirty}
      saving={saving}
      onReset={reset}
      onSave={onSave}
    >
      <FormSection title="Verbindung" description="Benötigt ein API-Token eines Paperless-Benutzers mit Upload-Rechten.">
        <SwitchRow label="Paperless-Anbindung aktivieren" checked={draft.enabled} onChange={(v) => set({ enabled: v })} />
        <Field label="Paperless-URL" help="z. B. http://paperless:8000">
          {(id) => <Input id={id} value={draft.url} onChange={(e) => set({ url: e.target.value })} placeholder="http://paperless:8000" className="font-mono text-[13px]" disabled={!draft.enabled} />}
        </Field>
        <Field label="API-Token" help="In Paperless unter „Mein Profil“ → „API-Token“ erzeugen.">
          {(id) => <SecretInput id={id} value={draft.token} onChange={(v) => set({ token: v })} />}
        </Field>
      </FormSection>
      <FormSection title="Automatik" description="Anhänge von Nachrichten bestimmter KI-Kategorien automatisch weiterleiten.">
        <SwitchRow
          label="Automatisch weiterleiten"
          description="PDF-Anhänge passender Nachrichten werden direkt nach dem Empfang übergeben – nur für die unten genannten Konten und nur, wenn DKIM oder DMARC den Absender bestätigen."
          checked={draft.autoForward}
          disabled={!draft.enabled}
          onChange={(v) => set({ autoForward: v })}
        />
        <Field label="Kategorien" help="Nur Nachrichten dieser KI-Kategorien werden automatisch übergeben.">
          {(id) => (
            <ChipInput
              id={id}
              value={draft.categories}
              onChange={(v) => set({ categories: v })}
              placeholder="z. B. Rechnung"
              suggestions={settings.ai.categories}
              disabled={!draft.enabled || !draft.autoForward}
            />
          )}
        </Field>
        <Field
          label="Konten"
          help="Paperless ist ein gemeinsames Archiv: Nur Mails dieser Postfächer werden automatisch übergeben. Ohne Eintrag wird nichts automatisch weitergeleitet."
        >
          {(id) => (
            <ChipInput
              id={id}
              value={draft.accounts ?? []}
              onChange={(v) => set({ accounts: v })}
              placeholder="z. B. buchhaltung@beispiel.de"
              disabled={!draft.enabled || !draft.autoForward}
            />
          )}
        </Field>
      </FormSection>
    </Shell>
  );
}

export function SecuritySection({ settings, save, saving }: SectionProps) {
  const { draft, setDraft, dirty, reset } = useDraft(settings.security.bruteForce);
  if (!draft) return null;
  return (
    <Shell
      title="Sicherheit"
      description="Schutz vor Passwort-Rateangriffen auf SMTP, IMAP und Weboberfläche."
      dirty={dirty}
      saving={saving}
      onReset={reset}
      onSave={() => save({ security: { bruteForce: draft } })}
    >
      <FormSection title="Brute-Force-Schutz" description="Nach zu vielen Fehlversuchen innerhalb des Zeitfensters wird die IP-Adresse gesperrt.">
        <Field label="Max. Fehlversuche">
          {(id) => <NumberInput id={id} value={draft.maxAttempts} min={1} onChange={(v) => setDraft((d) => ({ ...d, maxAttempts: v }))} />}
        </Field>
        <Field label="Zeitfenster" help="Zeitraum, in dem Fehlversuche gezählt werden.">
          {(id) => <NumberInput id={id} value={draft.windowSeconds} min={1} suffix="Sekunden" onChange={(v) => setDraft((d) => ({ ...d, windowSeconds: v }))} />}
        </Field>
        <Field label="Sperrdauer">
          {(id) => <NumberInput id={id} value={draft.banSeconds} min={1} suffix="Sekunden" onChange={(v) => setDraft((d) => ({ ...d, banSeconds: v }))} />}
        </Field>
      </FormSection>
    </Shell>
  );
}
