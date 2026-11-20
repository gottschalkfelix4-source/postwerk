import { useEffect, useState, type ReactNode } from "react";
import { useNavigate } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowRight,
  ArrowRightLeft,
  Bot,
  Bug,
  CircleCheck,
  CircleX,
  Clock,
  FileUp,
  Globe,
  Info,
  ListOrdered,
  Lock,
  PartyPopper,
  PlugZap,
  RotateCcw,
  Save,
  ShieldAlert,
  ShieldCheck,
  TriangleAlert,
  Waypoints,
  Zap,
} from "lucide-react";
import { adminApi } from "@/api/endpoints";
import { errorMessage } from "@/api/client";
import type { AiTestResult, ClamavTestResult, DnsCheck, Settings, SettingsUpdate, TlsMode } from "@/api/types";
import { qk } from "@/lib/queries";
import { cn, deepEqual } from "@/lib/utils";
import {
  Badge,
  Button,
  ChipInput,
  ErrorState,
  Field,
  Input,
  Notice,
  Segmented,
  Skeleton,
  SwitchRow,
  WizardNav,
  WizardStep,
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
  initialWebDraft,
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
  tlsModeLabels,
  tlsUpdate,
  type TlsDraft,
} from "@/pages/admin/tls/CertificateParts";
import { dynIpKeys } from "@/pages/admin/dynip/DynIpParts";
import { AiConnectionFields, AiProviderPicker, AiTestDetails } from "@/pages/admin/ai/AiProviderParts";
import { setupKeys, useSetup, type StepProps } from "./context";
import { dnsSummary, SectionTitle } from "./steps";

const LETS_ENCRYPT = "https://acme-v02.api.letsencrypt.org/directory";

export function useSettings() {
  return useQuery({ queryKey: setupKeys.settings, queryFn: adminApi.settings, staleTime: 60_000 });
}

export function useSaveSettings() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (update: SettingsUpdate) => adminApi.saveSettings(update),
    onSuccess: (s, update) => {
      qc.setQueryData(setupKeys.settings, s);
      void qc.invalidateQueries({ queryKey: qk.aiStatus });
      void qc.invalidateQueries({ queryKey: setupKeys.systemCheck });
      if (update.dyndns) void qc.invalidateQueries({ queryKey: dynIpKeys.dyndnsStatus });
    },
  });
}

export function SettingsGate({ q, children }: { q: ReturnType<typeof useSettings>; children: (s: Settings) => ReactNode }) {
  if (q.isLoading) return <Skeleton className="h-64 rounded-lg" />;
  if (q.error || !q.data) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  return <>{children(q.data)}</>;
}

// ---------------------------------------------------------------------------
// 6. Reverse proxy
// ---------------------------------------------------------------------------

export function ProxyStep({ onBack, onNext }: StepProps) {
  const ctx = useSetup();
  const toast = useToast();
  const q = useSettings();
  const detect = useProxyDetect();
  const save = useSaveSettings();
  const guide = useGuideInput(detect.data);
  const [draft, setDraft] = useState<WebSettings | null>(null);
  const hostname = q.data?.server.hostname || ctx.status.hostname;

  useEffect(() => {
    if (!draft && q.data && !detect.isLoading) setDraft(initialWebDraft(q.data, detect.data, hostname));
  }, [draft, q.data, detect.isLoading, detect.data, hostname]);

  const set = (patch: Partial<WebSettings>) => setDraft((d) => (d ? { ...d, ...patch } : d));
  const touchCookies = useCookieFollowsUrl(draft?.publicUrl, (v) => set({ secureCookies: v }));

  const saved = q.data ? webOf(q.data.web) : null;
  // "No" only switches the proxy mode off and leaves the other values alone.
  const update: SettingsUpdate | null = !draft || !saved
    ? null
    : draft.behindProxy
      ? deepEqual(draft, saved)
        ? null
        : { web: draft }
      : saved.behindProxy
        ? { web: { behindProxy: false } }
        : null;
  const dirty = update !== null;
  const urlInvalid = !!draft?.behindProxy && !isHttpUrl(draft.publicUrl);

  const persist = (after: (saved: boolean) => void) => {
    if (!update) return after(false);
    save.mutate(update, {
      onSuccess: (s) => {
        if (draft?.behindProxy) setDraft(webOf(s.web));
        after(true);
      },
      onError: (err) => toast.error(err, "Speichern fehlgeschlagen"),
    });
  };

  const next = () =>
    persist((didSave) => {
      if (didSave) toast.success("Proxy-Einstellungen gespeichert", "Sie gelten ab sofort.");
      onNext();
    });

  const recheck = () => persist(() => void detect.refetch());

  const setBehind = (v: boolean) =>
    setDraft((d) => (d ? (v ? withProxySuggestions(d, detect.data, hostname) : { ...d, behindProxy: false }) : d));

  return (
    <WizardStep
      icon={Waypoints}
      title="Reverse-Proxy"
      description="Weboberfläche hinter Nginx Proxy Manager, Traefik, Caddy & Co."
      footer={
        <WizardNav
          onBack={onBack}
          onNext={next}
          nextLabel={dirty ? "Speichern & weiter" : "Weiter"}
          loading={save.isPending}
          nextDisabled={!draft || urlInvalid}
        />
      }
    >
      <SettingsGate q={q}>
        {() =>
          !draft ? (
            <Skeleton className="h-64 rounded-lg" />
          ) : (
            <div className="space-y-6">
              <div className="space-y-3">
                <div className="flex flex-wrap items-center justify-between gap-3">
                  <h3 className="text-sm font-semibold text-zinc-900 dark:text-zinc-100">Läuft Postwerk hinter einem Reverse-Proxy?</h3>
                  <Segmented
                    items={[
                      { value: "no", label: "Nein" },
                      { value: "yes", label: "Ja" },
                    ]}
                    value={draft.behindProxy ? "yes" : "no"}
                    onChange={(v) => setBehind(v === "yes")}
                    className="w-40"
                  />
                </div>
                <DetectNotice detect={detect.data} loading={detect.isLoading} suggested={!!detect.data?.viaProxy && draft.behindProxy} />
              </div>

              {!draft.behindProxy ? (
                <Notice tone="info" icon={Info} title="Ohne Reverse-Proxy">
                  Du erreichst die Weboberfläche direkt über die Portfreigabe von Postwerk. Postwerk wertet dann keine
                  X-Forwarded-Header aus, damit niemand seine IP-Adresse fälschen kann. Das lässt sich jederzeit unter Einstellungen →
                  Web &amp; Proxy ändern.
                </Notice>
              ) : (
                <div className="animate-fade-in space-y-6">
                  <section className="space-y-4">
                    <SectionTitle icon={Waypoints} title="Welchen Proxy verwendest du?" text="Die Anleitung unten passt sich deiner Auswahl an." />
                    <ProxyKindPicker value={guide.kind} onChange={guide.setKind} />
                  </section>

                  <div className="border-t border-zinc-100 dark:border-zinc-800" />

                  <section className="space-y-4">
                    <SectionTitle icon={Globe} title="Adressen" text="Wie der Browser Postwerk erreicht und wohin der Proxy weiterleitet." />
                    <div className="grid gap-4 lg:grid-cols-2">
                      <PublicUrlField value={draft.publicUrl} onChange={(v) => set({ publicUrl: v })} hostname={hostname} />
                      <UpstreamField value={guide.upstream} onChange={guide.setUpstream} suggestion={guide.suggestion} />
                    </div>
                    <TrustedProxiesField
                      value={draft.trustedProxies}
                      onChange={(v) => set({ trustedProxies: v })}
                      suggestion={detect.data?.suggestedTrustedProxy}
                    />
                    <SecureCookiesRow
                      checked={draft.secureCookies}
                      onChange={(v) => {
                        touchCookies();
                        set({ secureCookies: v });
                      }}
                    />
                  </section>

                  <div className="border-t border-zinc-100 dark:border-zinc-800" />

                  <section className="space-y-4">
                    <SectionTitle icon={ListOrdered} title="Anleitung" text="Schritt für Schritt – mit Konfiguration zum Kopieren." />
                    <ProxyGuidePanel
                      kind={guide.kind}
                      upstream={guide.upstream}
                      hostname={hostname}
                      certificateAction={
                        <Button variant="primary" iconRight={ArrowRight} onClick={next} loading={save.isPending} disabled={urlInvalid}>
                          Weiter zur Zertifikatsauswahl
                        </Button>
                      }
                    />
                  </section>

                  <ProxyCheckPanel
                    detect={detect.data}
                    loading={detect.isFetching || save.isPending}
                    error={detect.error}
                    onRecheck={recheck}
                    note={dirty ? "Ungespeicherte Änderungen werden vor der Prüfung gespeichert." : undefined}
                  />
                </div>
              )}
            </div>
          )
        }
      </SettingsGate>
    </WizardStep>
  );
}

// ---------------------------------------------------------------------------
// 7. TLS
// ---------------------------------------------------------------------------

export function TlsStep({ onBack, onNext }: StepProps) {
  const ctx = useSetup();
  const toast = useToast();
  const qc = useQueryClient();
  const q = useSettings();
  const save = useSaveSettings();
  const [draft, setDraft] = useState<TlsDraft | null>(null);

  useEffect(() => {
    if (q.data && !draft) setDraft(tlsDraftOf(q.data.tls));
  }, [q.data, draft]);

  const source = q.data ? tlsDraftOf(q.data.tls) : null;
  const dirty = !!source && !!draft && !deepEqual(draft, source);
  const errors = draft ? tlsDraftErrors(draft) : null;
  const valid = !!draft && tlsDraftValid(draft);
  const behindProxy = !!q.data?.web?.behindProxy;
  const hostname = q.data?.server.hostname || ctx.status.hostname;

  const setAcme = (patch: Partial<TlsDraft["acme"]>) => setDraft((d) => (d ? { ...d, acme: { ...d.acme, ...patch } } : d));

  const setMode = (mode: TlsMode) =>
    setDraft((d) => {
      if (!d) return d;
      const acme = { ...d.acme, enabled: mode === "acme" };
      if (mode === "acme") {
        if (!acme.email && ctx.me) acme.email = ctx.me.email;
        if (acme.domains.length === 0 && hostname) acme.domains = [hostname];
        if (!acme.directoryUrl) acme.directoryUrl = LETS_ENCRYPT;
      }
      const httpPort = mode === "acme" && d.httpPort === "open" ? "auto-close" : d.httpPort;
      return { ...d, mode, acme, httpPort };
    });

  const persist = (after?: () => void) => {
    if (!draft) return;
    save.mutate(tlsUpdate(draft), {
      onSuccess: (s) => {
        setDraft(tlsDraftOf(s.tls));
        // The certificate is loaded right away; fetch the settings again for a fresh status.
        void qc.invalidateQueries({ queryKey: setupKeys.settings });
        toast.success("TLS-Einstellungen gespeichert", s.tls.status || undefined);
        after?.();
      },
      onError: (err) => toast.error(err, "Speichern fehlgeschlagen"),
    });
  };

  const next = () => (dirty ? persist(onNext) : onNext());

  return (
    <WizardStep
      icon={Lock}
      title="TLS-Zertifikate"
      description="Verschlüsselung für SMTP, IMAP und POP3 – Änderungen wirken ohne Neustart."
      footer={
        <WizardNav
          onBack={onBack}
          onNext={next}
          nextLabel={dirty ? "Speichern & weiter" : "Weiter"}
          loading={save.isPending}
          nextDisabled={!draft || (dirty && !valid)}
          extra={
            dirty ? (
              <Button icon={Save} onClick={() => persist()} disabled={!valid || save.isPending}>
                Speichern
              </Button>
            ) : undefined
          }
        />
      }
    >
      <SettingsGate q={q}>
        {(s) =>
          draft &&
          errors && (
            <div className="space-y-5">
              <TlsStatusBox status={s.tls.status} />

              <div className="space-y-3">
                <h3 className="text-sm font-semibold text-zinc-900 dark:text-zinc-100">Woher kommt das Zertifikat?</h3>
                <TlsModePicker value={draft.mode} onChange={setMode} recommended={behindProxy ? "files" : "acme"} />
              </div>

              {draft.mode === "files" && (
                <div className="animate-fade-in">
                  <CertificateFilesPanel
                    value={draft.files}
                    onChange={(files) => setDraft((d) => (d ? { ...d, files } : d))}
                    hostname={hostname}
                  />
                </div>
              )}

              {draft.mode === "acme" && (
                <div className="animate-fade-in space-y-4">
                  <Field
                    label="Kontakt-E-Mail"
                    required
                    help="Die Zertifizierungsstelle schickt hierhin Ablaufwarnungen."
                    error={draft.acme.email && errors.email ? "Ungültige E-Mail-Adresse." : null}
                  >
                    {(id) => (
                      <Input
                        id={id}
                        type="email"
                        value={draft.acme.email}
                        onChange={(e) => setAcme({ email: e.target.value })}
                        placeholder="admin@beispiel.de"
                        invalid={!!draft.acme.email && errors.email}
                        className="max-w-md"
                      />
                    )}
                  </Field>
                  <Field
                    label="Domains im Zertifikat"
                    help="Der Hostname ist Pflicht. Zusätzliche Namen, z. B. autoconfig.beispiel.de oder mta-sts.beispiel.de, mit Enter hinzufügen."
                    error={errors.domains ? "Mindestens ein Name ist erforderlich." : null}
                  >
                    {(id) => (
                      <ChipInput
                        id={id}
                        value={draft.acme.domains}
                        onChange={(v) => setAcme({ domains: v })}
                        placeholder={hostname || "mail.beispiel.de"}
                        transform={(v) => v.toLowerCase()}
                        suggestions={suggestTlsNames(hostname, ctx.domain).filter((n) => !draft.acme.domains.includes(n))}
                      />
                    )}
                  </Field>
                  <AcmeNotice behindProxy={behindProxy} />
                  <HttpPortPicker
                    value={draft.httpPort}
                    onChange={(v) => setDraft((d) => (d ? { ...d, httpPort: v } : d))}
                    status={q.data?.tls.httpStatus}
                  />
                  <IssueCertificateButton
                    disabledReason={dirty ? "Erst speichern, dann das Zertifikat holen." : q.data?.tls.mode !== "acme" ? "Erst „Let’s Encrypt direkt“ speichern." : null}
                  />
                </div>
              )}

              {draft.mode === "selfsigned" && <SelfSignedNotice />}
            </div>
          )
        }
      </SettingsGate>
    </WizardStep>
  );
}

function suggestTlsNames(hostname: string, domain: string): string[] {
  const out = new Set<string>();
  if (hostname) out.add(hostname);
  if (domain) {
    out.add(`autoconfig.${domain}`);
    out.add(`mta-sts.${domain}`);
  }
  return [...out];
}

// ---------------------------------------------------------------------------
// 8. Protection & AI
// ---------------------------------------------------------------------------

interface ProtectionDraft {
  antivirus: Settings["antivirus"];
  spam: Settings["spam"];
  ai: Settings["ai"];
}

export function ProtectionStep({ onBack, onNext }: StepProps) {
  const toast = useToast();
  const q = useSettings();
  const save = useSaveSettings();
  const [draft, setDraft] = useState<ProtectionDraft | null>(null);
  const [clamResult, setClamResult] = useState<ClamavTestResult | null>(null);
  const [aiResult, setAiResult] = useState<AiTestResult | null>(null);

  useEffect(() => {
    if (q.data && !draft) setDraft({ antivirus: q.data.antivirus, spam: q.data.spam, ai: q.data.ai });
  }, [q.data, draft]);

  const clamTest = useMutation({
    mutationFn: (address: string) => adminApi.testClamav(address),
    onSuccess: setClamResult,
    onError: (err) => setClamResult({ ok: false, error: errorMessage(err) }),
  });

  const aiPayload = (d: ProtectionDraft): SettingsUpdate["ai"] => {
    const payload: SettingsUpdate["ai"] = { ...d.ai };
    if (q.data && d.ai.apiKey === q.data.ai.apiKey) delete payload.apiKey;
    if (q.data && d.ai.embeddingApiKey === q.data.ai.embeddingApiKey) delete payload.embeddingApiKey;
    return payload;
  };

  const aiTest = useMutation({
    mutationFn: async () => {
      // The test endpoint checks the stored configuration, so save AI first.
      if (draft && q.data && !deepEqual(draft.ai, q.data.ai)) await save.mutateAsync({ ai: aiPayload(draft) });
      return adminApi.testAi();
    },
    onSuccess: setAiResult,
    onError: (err) => setAiResult({ ok: false, latencyMs: 0, models: [], error: errorMessage(err) }),
  });

  if (!draft || !q.data) {
    return (
      <WizardStep icon={ShieldCheck} title="Schutz & KI" footer={<WizardNav onBack={onBack} onNext={onNext} nextDisabled />}>
        <SettingsGate q={q}>{() => <Skeleton className="h-64 rounded-lg" />}</SettingsGate>
      </WizardStep>
    );
  }

  const s = q.data;
  const update: SettingsUpdate = {};
  if (!deepEqual(draft.antivirus, s.antivirus)) update.antivirus = draft.antivirus;
  if (!deepEqual(draft.spam, s.spam)) update.spam = draft.spam;
  if (!deepEqual(draft.ai, s.ai)) update.ai = aiPayload(draft);
  const dirty = Object.keys(update).length > 0;

  const setAv = (patch: Partial<Settings["antivirus"]>) => setDraft((d) => (d ? { ...d, antivirus: { ...d.antivirus, ...patch } } : d));
  const setAi = (patch: Partial<Settings["ai"]>) => setDraft((d) => (d ? { ...d, ai: { ...d.ai, ...patch } } : d));
  const setSpam = (fn: (spam: Settings["spam"]) => Settings["spam"]) => setDraft((d) => (d ? { ...d, spam: fn(d.spam) } : d));

  const next = () => {
    if (!dirty) return onNext();
    save.mutate(update, {
      onSuccess: () => {
        toast.success("Einstellungen gespeichert");
        onNext();
      },
      onError: (err) => toast.error(err, "Speichern fehlgeschlagen"),
    });
  };

  const pickModel = (m: string) => (/embed/i.test(m) ? setAi({ embeddingModel: m }) : setAi({ chatModel: m }));

  return (
    <WizardStep
      icon={ShieldCheck}
      title="Schutz & KI"
      description="Optional – alles lässt sich später unter Einstellungen ändern."
      footer={
        <WizardNav
          onBack={onBack}
          onNext={next}
          nextLabel={dirty ? "Speichern & weiter" : "Weiter"}
          loading={save.isPending && !aiTest.isPending}
          extra={
            dirty ? (
              <Button variant="ghost" icon={RotateCcw} onClick={() => setDraft({ antivirus: s.antivirus, spam: s.spam, ai: s.ai })}>
                Verwerfen
              </Button>
            ) : undefined
          }
        />
      }
    >
      <div className="space-y-6">
        <section className="space-y-4">
          <SectionTitle icon={Bug} title="Virenscan" text="Eingehende Nachrichten mit ClamAV prüfen. Benötigt einen erreichbaren clamd-Dienst." />
          <SwitchRow label="Virenscan aktivieren" checked={draft.antivirus.enabled} onChange={(v) => setAv({ enabled: v })} />
          <Field label="clamd-Adresse" help="z. B. tcp://clamav:3310 oder unix:///run/clamav/clamd.sock">
            {(id) => (
              <div className="flex flex-wrap gap-2">
                <Input
                  id={id}
                  value={draft.antivirus.clamdAddress}
                  onChange={(e) => {
                    setAv({ clamdAddress: e.target.value });
                    setClamResult(null);
                  }}
                  placeholder="tcp://clamav:3310"
                  className="min-w-[16rem] flex-1 font-mono text-[13px]"
                />
                <Button icon={PlugZap} loading={clamTest.isPending} disabled={!draft.antivirus.clamdAddress.trim()} onClick={() => clamTest.mutate(draft.antivirus.clamdAddress.trim())}>
                  Verbindung testen
                </Button>
              </div>
            )}
          </Field>
          {clamResult && (
            <Notice tone={clamResult.ok ? "success" : "error"} icon={clamResult.ok ? CircleCheck : CircleX} title={clamResult.ok ? "clamd ist erreichbar" : "Keine Verbindung zu clamd"}>
              {clamResult.error && <span className="break-words font-mono text-xs">{clamResult.error}</span>}
            </Notice>
          )}
        </section>

        <div className="border-t border-zinc-100 dark:border-zinc-800" />

        <section className="space-y-4">
          <SectionTitle icon={ShieldAlert} title="Spamfilter" text="Zusätzliche Prüfungen für eingehende Verbindungen." />
          <SwitchRow
            label="Greylisting"
            description="Unbekannte Absender werden beim ersten Versuch kurz abgewiesen. Echte Mailserver versuchen es später erneut – Spam-Bots meist nicht."
            checked={draft.spam.greylisting.enabled}
            onChange={(v) => setSpam((sp) => ({ ...sp, greylisting: { ...sp.greylisting, enabled: v } }))}
          />
          <SwitchRow
            label="DNS-Blocklisten"
            description={
              draft.spam.dnsbl.servers.length > 0
                ? `IP-Adressen einliefernder Server gegen ${draft.spam.dnsbl.servers.join(", ")} prüfen.`
                : "IP-Adressen einliefernder Server gegen öffentliche Blocklisten prüfen (Listen unter Einstellungen → Spamfilter)."
            }
            checked={draft.spam.dnsbl.enabled}
            onChange={(v) => setSpam((sp) => ({ ...sp, dnsbl: { ...sp.dnsbl, enabled: v } }))}
          />
        </section>

        <div className="border-t border-zinc-100 dark:border-zinc-800" />

        <section className="space-y-4">
          <SectionTitle icon={Bot} title="KI" text="Lokal über Ollama oder ein externer Dienst mit OpenAI- oder Anthropic-kompatibler API." />
          <SwitchRow label="KI-Funktionen aktivieren" description="Zusammenfassungen, Kategorien, Antwortentwürfe und semantische Suche." checked={draft.ai.enabled} onChange={(v) => setAi({ enabled: v })} />
          {draft.ai.enabled && (
            <div className="animate-fade-in space-y-4">
              <AiProviderPicker value={draft.ai} onChange={setAi} />
              <AiConnectionFields value={draft.ai} onChange={setAi} />
              <div className="flex flex-wrap items-center gap-3">
                <Button icon={PlugZap} loading={aiTest.isPending} disabled={!draft.ai.baseUrl.trim()} onClick={() => aiTest.mutate()}>
                  Verbindung testen
                </Button>
                {!deepEqual(draft.ai, s.ai) && <span className="text-xs text-zinc-500 dark:text-zinc-400">Die KI-Einstellungen werden vor dem Test gespeichert.</span>}
              </div>
              {aiTest.isPending ? (
                <div className="flex items-center gap-3 rounded-lg border border-zinc-200 bg-zinc-50 p-3 text-sm text-zinc-600 dark:border-zinc-800 dark:bg-zinc-950/50 dark:text-zinc-400">
                  <Zap className="h-4 w-4 animate-pulse text-indigo-500" /> Verbindung wird getestet …
                </div>
              ) : aiResult ? (
                <AiResult result={aiResult} chatModel={draft.ai.chatModel} embeddingModel={draft.ai.embeddingModel} onUse={pickModel} />
              ) : null}
            </div>
          )}
        </section>
      </div>
    </WizardStep>
  );
}

function AiResult({
  result,
  chatModel,
  embeddingModel,
  onUse,
}: {
  result: AiTestResult;
  chatModel: string;
  embeddingModel: string;
  onUse: (model: string) => void;
}) {
  return (
    <div
      className={cn(
        "rounded-lg border p-3",
        result.ok
          ? "border-emerald-200 bg-emerald-50/60 dark:border-emerald-500/30 dark:bg-emerald-500/[0.07]"
          : "border-red-200 bg-red-50/60 dark:border-red-500/30 dark:bg-red-500/[0.07]",
      )}
    >
      <div className="flex flex-wrap items-center gap-2">
        {result.ok ? <CircleCheck className="h-4 w-4 text-emerald-600" /> : <CircleX className="h-4 w-4 text-red-600" />}
        <span className="text-sm font-semibold text-zinc-900 dark:text-zinc-100">{result.ok ? "Verbindung erfolgreich" : "Verbindung fehlgeschlagen"}</span>
        {result.ok && <Badge tone="green">{result.latencyMs} ms</Badge>}
      </div>
      {result.error && <p className="mt-2 break-words font-mono text-xs text-red-700 dark:text-red-300">{result.error}</p>}
      <AiTestDetails result={result} />
      {result.models.length > 0 && (
        <div className="mt-3">
          <div className="mb-1.5 text-xs text-zinc-500 dark:text-zinc-400">
            Verfügbare Modelle – klicken zum Übernehmen (Modelle mit „embed“ im Namen werden als Embedding-Modell gesetzt):
          </div>
          <div className="flex flex-wrap gap-1.5">
            {result.models.map((m) => {
              const active = m === chatModel || m === embeddingModel;
              return (
                <button
                  key={m}
                  type="button"
                  onClick={() => onUse(m)}
                  className={cn(
                    "rounded-md px-2 py-0.5 font-mono text-xs ring-1 ring-inset transition",
                    active
                      ? "bg-indigo-50 text-indigo-700 ring-indigo-300 dark:bg-indigo-500/15 dark:text-indigo-300 dark:ring-indigo-500/40"
                      : "bg-white text-zinc-700 ring-zinc-200 hover:ring-indigo-400 dark:bg-zinc-900 dark:text-zinc-300 dark:ring-zinc-700",
                  )}
                >
                  {m}
                </button>
              );
            })}
          </div>
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// 9. Data import
// ---------------------------------------------------------------------------

export function DataStep({ onBack, onNext }: StepProps) {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const toast = useToast();
  const complete = useMutation({
    mutationFn: adminApi.completeSetup,
    onSettled: (_data, err) => {
      if (err) toast.error(err, "Einrichtung konnte nicht abgeschlossen werden");
      void qc.invalidateQueries({ queryKey: qk.setupStatus });
      navigate("/admin/migration/new");
    },
  });

  return (
    <WizardStep
      icon={ArrowRightLeft}
      title="Daten übernehmen"
      description="Bestehende Postfächer mitnehmen – jetzt oder später."
      footer={<WizardNav onBack={onBack} onNext={onNext} />}
    >
      <div className="grid gap-4 md:grid-cols-2">
        <div className="flex flex-col rounded-xl border border-indigo-200 bg-indigo-50/50 p-5 dark:border-indigo-500/30 dark:bg-indigo-500/[0.06]">
          <span className="mb-3 flex h-10 w-10 items-center justify-center rounded-xl bg-indigo-600 text-white dark:bg-indigo-500">
            <ArrowRightLeft className="h-5 w-5" />
          </span>
          <h3 className="text-base font-semibold text-zinc-900 dark:text-zinc-50">Von einem anderen Mailserver umziehen</h3>
          <p className="mt-1 flex-1 text-sm text-zinc-600 dark:text-zinc-300">
            Postfächer per IMAP übernehmen – von Gmail, Outlook, GMX, mailcow, Dovecot und vielen mehr. Ordner, Gelesen-Status
            und Datum bleiben erhalten.
          </p>
          <p className="mt-2 text-xs text-zinc-500 dark:text-zinc-400">Die Einrichtung wird dabei abgeschlossen.</p>
          <Button variant="primary" iconRight={ArrowRight} className="mt-4 self-start" loading={complete.isPending} onClick={() => complete.mutate()}>
            Migrationsassistent öffnen
          </Button>
        </div>
        <div className="flex flex-col rounded-xl border border-zinc-200 p-5 dark:border-zinc-800">
          <span className="mb-3 flex h-10 w-10 items-center justify-center rounded-xl bg-zinc-100 text-zinc-500 dark:bg-zinc-800 dark:text-zinc-400">
            <Clock className="h-5 w-5" />
          </span>
          <h3 className="text-base font-semibold text-zinc-900 dark:text-zinc-50">Später</h3>
          <p className="mt-1 flex-1 text-sm text-zinc-600 dark:text-zinc-300">
            Die Migration findest du jederzeit in der Verwaltung unter <strong className="font-medium">Daten → Migration</strong>.
          </p>
          <p className="mt-2 flex items-start gap-1.5 text-xs text-zinc-500 dark:text-zinc-400">
            <FileUp className="mt-px h-3.5 w-3.5 shrink-0" />
            Benutzer können außerdem selbst .mbox- und .eml-Dateien im Webmail unter „Konto & Sicherheit“ importieren.
          </p>
          <Button className="mt-4 self-start" onClick={onNext}>
            Später
          </Button>
        </div>
      </div>
    </WizardStep>
  );
}

// ---------------------------------------------------------------------------
// 10. Done
// ---------------------------------------------------------------------------

function SummaryRow({ label, children, ok }: { label: string; children: ReactNode; ok?: boolean | null }) {
  return (
    <div className="flex items-start justify-between gap-4 py-2.5">
      <dt className="text-sm text-zinc-500 dark:text-zinc-400">{label}</dt>
      <dd className="flex min-w-0 items-center gap-2 text-right text-sm font-medium text-zinc-900 dark:text-zinc-100">
        <span className="min-w-0 break-words">{children}</span>
        {ok === true && <CircleCheck className="h-4 w-4 shrink-0 text-emerald-500" />}
        {ok === false && <TriangleAlert className="h-4 w-4 shrink-0 text-amber-500" />}
      </dd>
    </div>
  );
}

export function DoneStep({ onBack }: StepProps) {
  const ctx = useSetup();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const toast = useToast();
  const settings = useSettings();
  const domains = useQuery({ queryKey: setupKeys.domains, queryFn: adminApi.domains });
  const domain = ctx.domain || domains.data?.[0]?.name || "";
  const dns = qc.getQueryData<DnsCheck[]>(setupKeys.dnsCheck(domain));
  const summary = dns ? dnsSummary(dns) : null;

  const complete = useMutation({
    mutationFn: adminApi.completeSetup,
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.setupStatus });
      toast.success("Einrichtung abgeschlossen", "Viel Freude mit Postwerk!");
      navigate("/admin", { replace: true });
    },
    onError: (err) => toast.error(err, "Einrichtung konnte nicht abgeschlossen werden"),
  });

  const s = settings.data;
  const tls = s ? tlsDraftOf(s.tls) : null;

  return (
    <WizardStep
      icon={PartyPopper}
      title="Geschafft!"
      description="Dein Mailserver ist eingerichtet. Hier die Zusammenfassung."
      footer={<WizardNav onBack={onBack} onNext={() => complete.mutate()} nextLabel="Zur Verwaltung" loading={complete.isPending} />}
    >
      <dl className="divide-y divide-zinc-100 dark:divide-zinc-800">
        <SummaryRow label="Hostname">{s?.server.hostname || ctx.status.hostname || "–"}</SummaryRow>
        <SummaryRow label="Domains">{domains.data?.map((d) => d.name).join(", ") || "–"}</SummaryRow>
        {ctx.me && <SummaryRow label="Administrator">{ctx.me.email}</SummaryRow>}
        <SummaryRow label="DNS" ok={summary ? summary.problems === 0 : null}>
          {summary
            ? summary.problems === 0
              ? "Alle Pflichteinträge korrekt"
              : `${summary.ok} von ${summary.required} Pflichteinträgen korrekt`
            : "Nicht geprüft"}
        </SummaryRow>
        <SummaryRow label="Internetanschluss">
          {s
            ? s.dyndns?.enabled || s.relay?.enabled
              ? `Dynamische IP (${[s.dyndns?.enabled && "DynDNS", s.relay?.enabled && `Smarthost ${s.relay.host}`].filter(Boolean).join(", ")})`
              : "Feste IP"
            : "–"}
        </SummaryRow>
        <SummaryRow label="Reverse-Proxy">
          {s ? (s.web?.behindProxy ? `Ja${s.web.publicUrl ? ` (${s.web.publicUrl})` : ""}` : "Nein") : "–"}
        </SummaryRow>
        <SummaryRow label="TLS" ok={tls ? tls.mode !== "selfsigned" : null}>
          {tls
            ? tls.mode === "acme"
              ? `${tlsModeLabels.acme} (${tls.acme.domains.join(", ")})`
              : tls.mode === "files"
                ? `${tlsModeLabels.files}${tls.files.certPath ? ` (${tls.files.certPath})` : ""}`
                : tlsModeLabels.selfsigned
            : "–"}
        </SummaryRow>
        <SummaryRow label="Virenscan">{s ? (s.antivirus.enabled ? "Aktiv" : "Aus") : "–"}</SummaryRow>
        <SummaryRow label="Spamfilter">
          {s
            ? [s.spam.greylisting.enabled && "Greylisting", s.spam.dnsbl.enabled && "DNS-Blocklisten"].filter(Boolean).join(", ") ||
              "Nur Basisfilter"
            : "–"}
        </SummaryRow>
        <SummaryRow label="KI">{s ? (s.ai.enabled ? `Aktiv (${s.ai.chatModel || "kein Modell"})` : "Aus") : "–"}</SummaryRow>
      </dl>

      <div className="mt-5 rounded-lg border border-zinc-200 p-4 text-sm dark:border-zinc-800">
        <div className="mb-2 font-medium text-zinc-900 dark:text-zinc-100">Nächste Schritte</div>
        <ul className="list-disc space-y-1 pl-5 text-zinc-600 dark:text-zinc-300">
          <li>Konten und Aliase für deine Benutzer anlegen.</li>
          <li>Für Mail-Programme und Smartphones App-Passwörter verwenden.</li>
          <li>Zwei-Faktor-Anmeldung für dein Admin-Konto aktivieren.</li>
        </ul>
      </div>
    </WizardStep>
  );
}
