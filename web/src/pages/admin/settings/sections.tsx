import { useState, type ReactNode } from "react";
import { useMutation } from "@tanstack/react-query";
import { CircleCheck, CircleX, Info, PlugZap, Zap } from "lucide-react";
import { adminApi } from "@/api/endpoints";
import type { AiTestResult, Settings, SettingsUpdate } from "@/api/types";
import { useDraft } from "@/lib/hooks";
import { cn, isEmail } from "@/lib/utils";
import { errorMessage } from "@/api/client";
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
} from "@/components/ui";
import type { SectionProps } from "./SettingsPage";

const MASK = "••••";

function Shell({
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

function NumberInput({
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
function SecretInput({ id, value, onChange, placeholder }: { id?: string; value: string; onChange: (v: string) => void; placeholder?: string }) {
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
    </Shell>
  );
}

const ACME_PRESETS = [
  { label: "Let's Encrypt", url: "https://acme-v02.api.letsencrypt.org/directory" },
  { label: "Let's Encrypt (Staging)", url: "https://acme-staging-v02.api.letsencrypt.org/directory" },
  { label: "ZeroSSL", url: "https://acme.zerossl.com/v2/DV90" },
];

export function TlsSection({ settings, save, saving }: SectionProps) {
  const { draft, setDraft, dirty, reset } = useDraft(settings.tls.acme);
  if (!draft) return null;
  const set = (patch: Partial<Settings["tls"]["acme"]>) => setDraft((d) => ({ ...d, ...patch }));
  const status = settings.tls.status;
  const statusTone = /fehl|error|fail|expired|abgelaufen/i.test(status) ? "red" : /ok|valid|gültig|aktiv|active/i.test(status) ? "green" : "neutral";
  return (
    <Shell
      title="TLS / ACME"
      description="Zertifikate für SMTP, IMAP und die Weboberfläche."
      dirty={dirty}
      saving={saving}
      onReset={reset}
      onSave={() => save({ tls: { acme: draft } })}
    >
      <FormSection title="Status" description="Aktueller Zustand der Zertifikate laut Server.">
        <div className="flex items-start gap-3 rounded-lg border border-zinc-200 bg-zinc-50 p-3 dark:border-zinc-800 dark:bg-zinc-950/50">
          <Badge tone={statusTone} dot>
            {statusTone === "green" ? "OK" : statusTone === "red" ? "Problem" : "Info"}
          </Badge>
          <p className="min-w-0 break-words text-sm text-zinc-700 dark:text-zinc-300">{status || "Keine Angabe"}</p>
        </div>
      </FormSection>
      <FormSection title="Automatische Zertifikate" description="Zertifikate werden per ACME (z. B. Let's Encrypt) bezogen und automatisch erneuert.">
        <SwitchRow label="ACME aktivieren" description="Port 80 oder 443 muss für die Validierung von außen erreichbar sein." checked={draft.enabled} onChange={(v) => set({ enabled: v })} />
        <Field label="Kontakt-E-Mail" help="Die Zertifizierungsstelle schickt hierhin Ablaufwarnungen." error={draft.email && !isEmail(draft.email) ? "Ungültige E-Mail-Adresse." : null}>
          {(id) => <Input id={id} type="email" value={draft.email} onChange={(e) => set({ email: e.target.value })} placeholder="admin@beispiel.de" disabled={!draft.enabled} />}
        </Field>
        <Field label="ACME-Verzeichnis" help="Zum Testen die Staging-Umgebung verwenden, um Rate-Limits zu vermeiden.">
          {(id) => (
            <div className="space-y-2">
              <Input id={id} value={draft.directoryUrl} onChange={(e) => set({ directoryUrl: e.target.value })} disabled={!draft.enabled} className="font-mono text-[13px]" />
              <div className="flex flex-wrap gap-1.5">
                {ACME_PRESETS.map((p) => (
                  <button
                    key={p.url}
                    type="button"
                    disabled={!draft.enabled}
                    onClick={() => set({ directoryUrl: p.url })}
                    className={cn(
                      "rounded-md px-2 py-1 text-xs ring-1 ring-inset transition disabled:opacity-50",
                      draft.directoryUrl === p.url
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
        <Field label="Domains" help="Alle Namen, für die ein Zertifikat ausgestellt werden soll (z. B. mail.beispiel.de, autoconfig.beispiel.de).">
          {(id) => <ChipInput id={id} value={draft.domains} onChange={(v) => set({ domains: v })} placeholder="mail.beispiel.de" transform={(v) => v.toLowerCase()} disabled={!draft.enabled} />}
        </Field>
      </FormSection>
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
    const payload: SettingsUpdate["ai"] = { ...draft };
    if (draft.apiKey === settings.ai.apiKey) delete payload.apiKey;
    save({ ai: payload });
  };

  return (
    <Shell
      title="KI"
      description="Anbindung an ein OpenAI-kompatibles Sprachmodell (z. B. Ollama, LM Studio, vLLM)."
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
      <FormSection title="Endpunkt" description="Basis-URL der OpenAI-kompatiblen API inklusive /v1.">
        <Field
          label="Basis-URL"
          help={
            <>
              Für Ollama im selben Docker-Netz z. B. <code className="font-mono">http://ollama:11434/v1</code>, lokal{" "}
              <code className="font-mono">http://localhost:11434/v1</code>.
            </>
          }
        >
          {(id) => <Input id={id} value={draft.baseUrl} onChange={(e) => set({ baseUrl: e.target.value })} placeholder="http://ollama:11434/v1" className="font-mono text-[13px]" />}
        </Field>
        <Field label="API-Schlüssel" help="Für Ollama nicht erforderlich. Gespeicherte Schlüssel werden nicht angezeigt.">
          {(id) => <SecretInput id={id} value={draft.apiKey} onChange={(v) => set({ apiKey: v })} placeholder="sk-…" />}
        </Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Chat-Modell" help="z. B. llama3.1:8b, qwen2.5:7b">
            {(id) => <Input id={id} value={draft.chatModel} onChange={(e) => set({ chatModel: e.target.value })} className="font-mono text-[13px]" />}
          </Field>
          <Field label="Embedding-Modell" help="Für die semantische Suche, z. B. nomic-embed-text">
            {(id) => <Input id={id} value={draft.embeddingModel} onChange={(e) => set({ embeddingModel: e.target.value })} className="font-mono text-[13px]" />}
          </Field>
        </div>
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
    </Shell>
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
          description="PDF-Anhänge passender Nachrichten werden direkt nach dem Empfang übergeben."
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
