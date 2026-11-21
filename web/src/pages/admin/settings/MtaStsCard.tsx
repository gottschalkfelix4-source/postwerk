import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { CircleCheck, CircleX, RefreshCw, ShieldCheck, TriangleAlert } from "lucide-react";
import { mailAuthApi } from "@/api/mailauth";
import type { MtaStsMode } from "@/api/types";
import { Button, Card, CardBody, CardFooter, CardHeader, CopyButton, Field, Notice, Select, Skeleton } from "@/components/ui";
import { NumberInput } from "./sections";
import type { SectionProps } from "./SettingsPage";

const modes: { value: MtaStsMode; label: string; help: string }[] = [
  {
    value: "none",
    label: "Aus (none)",
    help: "Andere Server ignorieren die Richtlinie. Zum Abschalten: Absender vergessen eine früher veröffentlichte Richtlinie, sobald sie diese Fassung abgerufen haben.",
  },
  {
    value: "testing",
    label: "Testen (testing)",
    help: "Empfohlen zum Start. Andere Server liefern auch bei Zertifikatsproblemen weiter aus und melden Fehler nur per TLS-Bericht (siehe Berichte).",
  },
  {
    value: "enforce",
    label: "Erzwingen (enforce)",
    help: "Andere Server liefern nur noch verschlüsselt mit gültigem Zertifikat an deinen MX aus. Ist das Zertifikat abgelaufen oder ungültig, bleiben eingehende Mails beim Absender liegen.",
  },
];

const DAY = 86400;

/** MTA-STS policy of our own domains (settings.mailauth.mtaSts), shown on the TLS page. */
export function MtaStsCard({ settings, save, saving }: SectionProps) {
  const qc = useQueryClient();
  const current = settings.mailauth?.mtaSts ?? { mode: "testing" as MtaStsMode, maxAge: 7 * DAY };
  const [mode, setMode] = useState<MtaStsMode>(current.mode);
  const [days, setDays] = useState(Math.round(current.maxAge / DAY));
  const first = useRef(true);
  useEffect(() => {
    setMode(current.mode);
    setDays(Math.round(current.maxAge / DAY));
    if (first.current) {
      first.current = false;
      return;
    }
    // Saved: the policy text and the TXT id follow the new settings.
    void qc.invalidateQueries({ queryKey: ["admin", "mta-sts"] });
    void qc.invalidateQueries({ queryKey: ["admin", "domain-dns"] });
  }, [current.mode, current.maxAge, qc]);

  const status = useQuery({ queryKey: ["admin", "mta-sts"], queryFn: mailAuthApi.mtaStsStatus, staleTime: 60_000 });
  const dirty = mode !== current.mode || days * DAY !== current.maxAge;
  const notReady = mode === "enforce" && status.data && !status.data.enforceReady;
  const help = modes.find((m) => m.value === mode)?.help;

  const onSave = () => save({ mailauth: { mtaSts: { mode, maxAge: days * DAY } } });

  return (
    <Card className="mt-6">
      <CardHeader
        icon={<ShieldCheck className="h-5 w-5 text-zinc-400" />}
        title="MTA-STS für eigene Domains"
        description="Mit MTA-STS teilst du anderen Mailservern mit, dass Mails an deine Domains nur verschlüsselt und mit gültigem Zertifikat zugestellt werden sollen. Dazu gehören der TXT-Eintrag _mta-sts und der CNAME mta-sts in den DNS-Einträgen der Domain."
      />
      <CardBody className="space-y-4">
        <Field label="Modus" help={help}>
          {(id) => (
            <Select
              id={id}
              value={mode}
              onChange={(e) => setMode(e.target.value as MtaStsMode)}
              options={modes.map((m) => ({ value: m.value, label: m.label }))}
              className="max-w-xs"
            />
          )}
        </Field>
        <Field label="Gültigkeit (max_age)" help="So lange merken sich andere Server die Richtlinie. Üblich sind 7 Tage; im Modus „Erzwingen“ auch länger (bis 365 Tage).">
          {(id) => <NumberInput id={id} value={days} min={1} max={365} step={1} suffix="Tage" onChange={(v) => setDays(Math.max(1, Math.min(365, Math.round(v))))} />}
        </Field>

        {notReady && (
          <Notice tone="warning" icon={TriangleAlert} title="Zertifikate noch nicht bereit für „Erzwingen“">
            Nicht für alle benötigten Namen ist ein gültiges öffentliches Zertifikat vorhanden (siehe unten). Mit „Erzwingen“ würden andere Server dann keine Mails mehr an dich zustellen. Erst die Zertifikate einrichten (Let's Encrypt), dann umstellen.
          </Notice>
        )}

        <div className="rounded-lg border border-zinc-200 dark:border-zinc-800">
          <div className="flex items-center justify-between gap-2 border-b border-zinc-200 px-3 py-2 dark:border-zinc-800">
            <span className="text-sm font-medium text-zinc-800 dark:text-zinc-200">Zertifikate</span>
            <Button size="sm" variant="ghost" icon={RefreshCw} loading={status.isFetching} onClick={() => void status.refetch()}>
              Prüfen
            </Button>
          </div>
          <div className="space-y-2 p-3">
            {status.isLoading ? (
              <Skeleton className="h-12 rounded-md" />
            ) : status.error ? (
              <p className="text-xs text-red-600 dark:text-red-400">Prüfung fehlgeschlagen.</p>
            ) : (
              status.data?.checks.map((c) => (
                <div key={c.host} className="flex items-start gap-2 text-xs">
                  {c.ok ? (
                    <CircleCheck className="mt-px h-3.5 w-3.5 shrink-0 text-emerald-500" />
                  ) : (
                    <CircleX className="mt-px h-3.5 w-3.5 shrink-0 text-amber-500" />
                  )}
                  <div className="min-w-0">
                    <code className="break-all font-mono text-zinc-800 dark:text-zinc-200">{c.host}</code>
                    <span className="text-zinc-500 dark:text-zinc-400"> · {c.purpose === "mx" ? "Mailserver (MX)" : "Richtlinie per HTTPS"}</span>
                    <div className="text-zinc-500 dark:text-zinc-400">{c.detail}</div>
                  </div>
                </div>
              ))
            )}
          </div>
        </div>

        {status.data && (
          <div className="space-y-1.5 rounded-md bg-zinc-50 p-3 text-xs dark:bg-zinc-800/50">
            <div className="flex items-center justify-between gap-2">
              <span className="text-zinc-500 dark:text-zinc-400">Veröffentlichte Richtlinie</span>
              <CopyButton value={`v=STSv1; id=${status.data.id}`} label="TXT-Wert kopieren" />
            </div>
            <pre className="whitespace-pre-wrap break-all font-mono text-zinc-800 dark:text-zinc-200">{status.data.policy.replace(/\r/g, "")}</pre>
            <p className="text-zinc-500 dark:text-zinc-400">
              TXT-Eintrag <code className="font-mono">_mta-sts</code>: <code className="font-mono">v=STSv1; id={status.data.id}</code>. Die id ändert sich mit jeder Änderung der Richtlinie – nach dem Speichern den TXT-Eintrag aktualisieren (oder „Automatisch einrichten“ nutzen).
            </p>
          </div>
        )}
      </CardBody>
      <CardFooter>
        <Button variant={notReady ? "danger" : "primary"} size="sm" disabled={!dirty} loading={saving} onClick={onSave}>
          {notReady ? "Trotzdem speichern" : "Speichern"}
        </Button>
      </CardFooter>
    </Card>
  );
}
