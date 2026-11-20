import { useEffect, useState, type ReactNode } from "react";
import { CircleCheck, Info, PlugZap, RefreshCw, Router, Save, Send, type LucideIcon } from "lucide-react";
import { errorMessage } from "@/api/client";
import type { RelayTestResult, SettingsUpdate } from "@/api/types";
import { domainFromHostname } from "@/lib/setup";
import { Button, Notice, Segmented, Skeleton, SwitchRow, WizardNav, WizardStep, useToast } from "@/components/ui";
import {
  DynamicIpNotice,
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
import { useSetup, type StepProps } from "./context";
import { SettingsGate, useSaveSettings, useSettings } from "./settingsSteps";

type Mode = "static" | "dynamic";

/** Bordered sub-card with its own actions inside a wizard step. */
function ServiceCard({
  icon: Icon,
  title,
  text,
  children,
  footer,
}: {
  icon: LucideIcon;
  title: string;
  text: ReactNode;
  children: ReactNode;
  footer?: ReactNode;
}) {
  return (
    <section className="overflow-hidden rounded-xl border border-zinc-200 dark:border-zinc-800">
      <div className="flex items-start gap-3 border-b border-zinc-100 px-4 py-4 dark:border-zinc-800 sm:px-5">
        <span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-indigo-50 text-indigo-600 dark:bg-indigo-500/15 dark:text-indigo-300">
          <Icon className="h-[18px] w-[18px]" />
        </span>
        <div className="min-w-0">
          <h3 className="text-[15px] font-semibold text-zinc-900 dark:text-zinc-50">{title}</h3>
          <p className="mt-0.5 text-sm leading-relaxed text-zinc-500 dark:text-zinc-400">{text}</p>
        </div>
      </div>
      <div className="space-y-5 px-4 py-5 sm:px-5">{children}</div>
      {footer && (
        <div className="flex flex-wrap items-center justify-end gap-2 border-t border-zinc-100 bg-zinc-50/70 px-4 py-3 dark:border-zinc-800 dark:bg-zinc-900/40 sm:px-5">
          {footer}
        </div>
      )}
    </section>
  );
}

// ---------------------------------------------------------------------------
// 5. Internet connection (fixed or dynamic IP)
// ---------------------------------------------------------------------------

export function ConnectionStep({ onBack, onNext }: StepProps) {
  const ctx = useSetup();
  const toast = useToast();
  const q = useSettings();
  const save = useSaveSettings();
  const ddPresets = useDynDNSPresets();
  const relayPresets = useRelayPresets();
  const [mode, setMode] = useState<Mode | null>(null);
  const [dd, setDd] = useState<DynDNSSettings | null>(null);
  const [relay, setRelay] = useState<RelaySettings | null>(null);
  const [ddErrors, setDdErrors] = useState(false);
  const [relayErrors, setRelayErrors] = useState(false);
  const [relayResult, setRelayResult] = useState<RelayTestResult | null>(null);

  const hostname = q.data?.server.hostname || ctx.status.hostname;
  const domain = ctx.domain || domainFromHostname(hostname);
  const savedDd = q.data ? dyndnsOf(q.data.dyndns) : null;
  const savedRelay = q.data ? relayOf(q.data.relay) : null;
  const envOverride = !!relayPresets.data?.envOverride;
  const status = useDynDNSStatus(!!savedDd?.enabled);

  useEffect(() => {
    if (!q.data || mode !== null) return;
    const d = dyndnsOf(q.data.dyndns);
    const r = relayOf(q.data.relay);
    setDd(d);
    setRelay(r);
    setMode(d.enabled || r.enabled ? "dynamic" : "static");
  }, [q.data, mode]);

  const ddChange = dd && savedDd ? dyndnsChange(dd, savedDd) : null;
  const relayUpdate = relay && savedRelay && !envOverride ? relayChange(relay, savedRelay) : null;

  // Pending changes for "Weiter": a fixed IP only switches DynDNS off and leaves the smarthost alone.
  const update: SettingsUpdate = {};
  if (mode === "static") {
    if (savedDd?.enabled) update.dyndns = { enabled: false };
  } else {
    if (ddChange) update.dyndns = ddChange;
    if (relayUpdate) update.relay = relayUpdate;
  }
  const dirty = Object.keys(update).length > 0;

  const chooseMode = (m: Mode) => {
    setMode(m);
    // Nothing configured yet: suggest DynDNS and smarthost with STRATO preselected.
    if (m === "dynamic" && savedDd && savedRelay && !savedDd.enabled && !savedRelay.enabled) {
      setDd((d) => (d && !d.enabled ? withDynDNSSuggestions(d, hostname, ddPresets.data) : d));
      if (!envOverride) setRelay((r) => (r && !r.enabled ? withRelaySuggestions(r, relayPresets.data?.presets) : r));
    }
  };

  const persistDd = async () => {
    if (!dd || !savedDd) return;
    const change = dyndnsChange(dd, savedDd);
    if (!change) return;
    const s = await save.mutateAsync({ dyndns: change });
    setDd(dyndnsOf(s.dyndns));
  };

  const persistRelay = async () => {
    if (!relay || !savedRelay || envOverride) return;
    const change = relayChange(relay, savedRelay);
    if (!change) return;
    const s = await save.mutateAsync({ relay: change });
    setRelay(relayOf(s.relay));
  };

  const ddUpdate = useDynDNSUpdate(persistDd);
  const relayTest = useRelayTest(persistRelay);

  const checkDd = () => {
    if (dd && dyndnsValid(dd)) return true;
    setDdErrors(true);
    toast.error("Bitte die markierten Felder ausfüllen.", "DynDNS unvollständig");
    return false;
  };

  const checkRelay = () => {
    if (envOverride || (relay && relayValid(relay))) return true;
    setRelayErrors(true);
    toast.error("Bitte die markierten Felder ausfüllen.", "Smarthost unvollständig");
    return false;
  };

  const saveDd = () => {
    if (!checkDd()) return;
    persistDd()
      .then(() => toast.success("DynDNS gespeichert"))
      .catch((err) => toast.error(err, "Speichern fehlgeschlagen"));
  };

  const saveRelay = () => {
    if (!checkRelay()) return;
    persistRelay()
      .then(() => toast.success("Smarthost gespeichert"))
      .catch((err) => toast.error(err, "Speichern fehlgeschlagen"));
  };

  const runDdUpdate = () => {
    if (!checkDd()) return;
    ddUpdate.mutate(undefined, { onError: (err) => toast.error(err, "Aktualisierung fehlgeschlagen") });
  };

  const runRelayTest = () => {
    if (!checkRelay()) return;
    setRelayResult(null);
    relayTest.mutate(undefined, {
      onSuccess: setRelayResult,
      onError: (err) => setRelayResult({ ok: false, detail: errorMessage(err) }),
    });
  };

  const next = () => {
    if (!dirty) return onNext();
    if (update.dyndns && update.dyndns.enabled !== false && !checkDd()) return;
    if (update.relay && update.relay.enabled !== false && !checkRelay()) return;
    save.mutate(update, {
      onSuccess: (s) => {
        setDd(dyndnsOf(s.dyndns));
        setRelay(relayOf(s.relay));
        toast.success("Einstellungen gespeichert");
        onNext();
      },
      onError: (err) => toast.error(err, "Speichern fehlgeschlagen"),
    });
  };

  const ddDirty = ddChange !== null;
  const relayDirty = relayUpdate !== null;
  const busy = save.isPending || ddUpdate.isPending || relayTest.isPending;

  return (
    <WizardStep
      icon={Router}
      title="Internetanschluss"
      description="Feste oder wechselnde IP-Adresse – davon hängt ab, wie Postwerk erreichbar bleibt und Mails verschickt."
      footer={
        <WizardNav
          onBack={onBack}
          onNext={next}
          nextLabel={dirty ? "Speichern & weiter" : "Weiter"}
          loading={save.isPending && !ddUpdate.isPending && !relayTest.isPending}
          nextDisabled={!mode || ddUpdate.isPending || relayTest.isPending}
        />
      }
    >
      <SettingsGate q={q}>
        {() =>
          !mode || !dd || !relay || !savedDd || !savedRelay ? (
            <Skeleton className="h-64 rounded-lg" />
          ) : (
            <div className="space-y-6">
              <div className="space-y-3">
                <h3 className="text-sm font-semibold text-zinc-900 dark:text-zinc-100">Hat dein Internetanschluss eine feste IP-Adresse?</h3>
                <Segmented
                  items={[
                    { value: "static", label: "Feste IP" },
                    {
                      value: "dynamic",
                      label: (
                        <>
                          Dynamische IP<span className="hidden sm:inline">(wechselt, z. B. DSL/Kabel zu Hause)</span>
                        </>
                      ),
                    },
                  ]}
                  value={mode}
                  onChange={chooseMode}
                  className="w-full sm:w-auto"
                />
                <p className="text-xs leading-relaxed text-zinc-500 dark:text-zinc-400">
                  Das steht in deinem Vertrag oder in der Übersicht deines Routers: Eine feste IP wird ausdrücklich genannt und meist extra
                  berechnet. Privatkundenanschlüsse (DSL, Kabel, Glasfaser) sind fast immer dynamisch – die IP wechselt z. B. bei einer
                  Neueinwahl des Routers.
                </p>
              </div>

              {mode === "static" ? (
                <Notice tone="success" icon={CircleCheck} title="Feste IP – hier ist nichts weiter nötig">
                  Lass beim Anbieter den Reverse-DNS-Eintrag (PTR) deiner IP auf{" "}
                  <code className="font-mono">{hostname || "deinen Hostnamen"}</code> setzen – große Mailanbieter prüfen ihn.
                  {savedDd.enabled && " Die DynDNS-Aktualisierung wird beim Weiter ausgeschaltet."}
                  {savedRelay.enabled && " Der eingerichtete Smarthost bleibt unverändert (Einstellungen → Smarthost)."}
                </Notice>
              ) : (
                <div className="animate-fade-in space-y-5">
                  <ServiceCard
                    icon={RefreshCw}
                    title="DynDNS – Hostname aktuell halten"
                    text={
                      <>
                        Postwerk meldet jede neue IP an deinen DNS-Anbieter, damit{" "}
                        <code className="font-mono">{hostname || "dein Hostname"}</code> immer auf deinen Anschluss zeigt.
                      </>
                    }
                    footer={
                      <>
                        {ddDirty && (
                          <Button icon={Save} onClick={saveDd} disabled={busy}>
                            Speichern
                          </Button>
                        )}
                        <Button
                          variant={ddDirty ? "secondary" : "primary"}
                          icon={RefreshCw}
                          onClick={runDdUpdate}
                          loading={ddUpdate.isPending}
                          disabled={!dd.enabled || (busy && !ddUpdate.isPending)}
                        >
                          Jetzt aktualisieren
                        </Button>
                      </>
                    }
                  >
                    <SwitchRow
                      label="DynDNS über Postwerk aktualisieren"
                      description={routerDynDNSHint}
                      checked={dd.enabled}
                      onChange={(v) => setDd((d) => (d ? (v ? withDynDNSSuggestions(d, hostname, ddPresets.data) : { ...d, enabled: false }) : d))}
                    />
                    {dd.enabled && (
                      <div className="animate-fade-in">
                        <DynDNSFields
                          value={dd}
                          onChange={setDd}
                          presets={ddPresets.data}
                          presetsLoading={ddPresets.isLoading}
                          hostname={hostname}
                          showErrors={ddErrors}
                          afterPresets={dd.preset === "strato" ? <StratoPrepNotice hostname={hostname} domain={domain} /> : undefined}
                        />
                      </div>
                    )}
                    {(savedDd.enabled || ddUpdate.data) && (
                      <DynDNSStatusPanel
                        status={status.data}
                        loading={status.isFetching}
                        error={status.error}
                        note={ddDirty ? "Ungespeicherte Änderungen werden vor dem Aktualisieren gespeichert." : undefined}
                      />
                    )}
                  </ServiceCard>

                  <ServiceCard
                    icon={Send}
                    title="Smarthost – Mails zuverlässig verschicken"
                    text="Gmail, Outlook & Co. lehnen Mails von Privatkunden-IPs meist ab. Deshalb verschickt Postwerk ausgehende Mails über den Server eines Anbieters, den Smarthost."
                    footer={
                      <>
                        {relayDirty && (
                          <Button icon={Save} onClick={saveRelay} disabled={busy}>
                            Speichern
                          </Button>
                        )}
                        <Button
                          variant={relayDirty ? "secondary" : "primary"}
                          icon={PlugZap}
                          onClick={runRelayTest}
                          loading={relayTest.isPending}
                          disabled={(!relay.enabled && !envOverride) || (busy && !relayTest.isPending)}
                        >
                          Verbindung testen
                        </Button>
                      </>
                    }
                  >
                    {envOverride && <RelayEnvNotice />}
                    <SwitchRow
                      label="Ausgehende Mails über einen Smarthost senden"
                      description="Empfohlen bei dynamischer IP."
                      checked={relay.enabled}
                      disabled={envOverride}
                      onChange={(v) => setRelay((r) => (r ? (v ? withRelaySuggestions(r, relayPresets.data?.presets) : { ...r, enabled: false }) : r))}
                    />
                    {(relay.enabled || envOverride) && (
                      <div className="animate-fade-in">
                        <RelayFields
                          value={relay}
                          onChange={(r) => {
                            setRelay(r);
                            setRelayResult(null);
                          }}
                          presets={relayPresets.data?.presets}
                          presetsLoading={relayPresets.isLoading}
                          readOnly={envOverride}
                          showErrors={relayErrors}
                        />
                      </div>
                    )}
                    {(relayResult || relayTest.isPending) && <RelayTestPanel result={relayResult} pending={relayTest.isPending} />}
                    {relayDirty && !relayTest.isPending && (
                      <p className="flex items-center gap-1.5 text-xs text-zinc-500 dark:text-zinc-400">
                        <Info className="h-3.5 w-3.5" /> Ungespeicherte Änderungen werden vor dem Test gespeichert.
                      </p>
                    )}
                  </ServiceCard>

                  <DynamicIpNotice />
                </div>
              )}
            </div>
          )
        }
      </SettingsGate>
    </WizardStep>
  );
}
