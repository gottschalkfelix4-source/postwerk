import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CircleCheck, CircleX, FolderOpen, HardDrive, Info, Network, PlugZap, ShieldAlert, Zap, type LucideIcon } from "lucide-react";
import { adminApi } from "@/api/endpoints";
import type { BackupSettings, BackupTarget, BackupTargetType, BackupTestResult } from "@/api/types";
import { useDraft } from "@/lib/hooks";
import { cn } from "@/lib/utils";
import {
  Badge,
  Button,
  Card,
  CardBody,
  CardHeader,
  ErrorState,
  Field,
  FormSection,
  Input,
  Notice,
  SaveBar,
  Select,
  Skeleton,
  SwitchRow,
  useToast,
} from "@/components/ui";
import { NumberInput, SecretInput } from "@/pages/admin/settings/sections";

const MASK = "••••";
// Must match minBackupPassword on the server.
const MIN_PASSWORD = 12;

const targetMeta: Record<BackupTargetType, { name: string; text: string; icon: LucideIcon }> = {
  local: { name: "Lokal", text: "Ordner im Container, z. B. ein eingebundenes Laufwerk.", icon: HardDrive },
  webdav: { name: "WebDAV", text: "Nextcloud, ownCloud, Synology, QNAP, Hetzner Storage Box …", icon: FolderOpen },
  smb: { name: "SMB-Freigabe", text: "Windows-Freigabe, NAS, Unraid, Fritzbox (SMB 2/3).", icon: Network },
};

const weekdays = ["Sonntag", "Montag", "Dienstag", "Mittwoch", "Donnerstag", "Freitag", "Samstag"];

function TargetPicker({ value, onChange }: { value: BackupTargetType; onChange: (t: BackupTargetType) => void }) {
  return (
    <div className="grid gap-2.5 sm:grid-cols-3" role="radiogroup" aria-label="Speicherziel">
      {(Object.keys(targetMeta) as BackupTargetType[]).map((t) => {
        const meta = targetMeta[t];
        const active = t === value;
        const Icon = meta.icon;
        return (
          <button
            key={t}
            type="button"
            role="radio"
            aria-checked={active}
            onClick={() => onChange(t)}
            className={cn(
              "flex min-w-0 flex-col gap-1.5 rounded-xl border p-3 text-left transition",
              "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-indigo-500/50",
              active
                ? "border-indigo-400 bg-indigo-50/60 ring-1 ring-indigo-400 dark:border-indigo-500/60 dark:bg-indigo-500/10 dark:ring-indigo-500/60"
                : "border-zinc-200 hover:border-zinc-300 hover:bg-zinc-50 dark:border-zinc-800 dark:hover:border-zinc-700 dark:hover:bg-zinc-800/50",
            )}
          >
            <span className="flex items-center gap-2 text-sm font-medium text-zinc-900 dark:text-zinc-100">
              <Icon className="h-4 w-4 text-indigo-500" /> {meta.name}
            </span>
            <span className="text-xs leading-relaxed text-zinc-500 dark:text-zinc-400">{meta.text}</span>
          </button>
        );
      })}
    </div>
  );
}

function TestPanel({ result, pending }: { result: BackupTestResult | null; pending: boolean }) {
  if (pending) {
    return (
      <div className="flex items-center gap-3 rounded-xl border border-zinc-200 bg-zinc-50 p-4 text-sm text-zinc-600 dark:border-zinc-800 dark:bg-zinc-950/50 dark:text-zinc-400">
        <Zap className="h-4 w-4 animate-pulse text-indigo-500" /> Speicherziel wird getestet (Datei schreiben, auflisten, löschen) …
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
          {result.ok ? "Speicherziel funktioniert" : "Test fehlgeschlagen"}
        </span>
        {result.ok && <Badge tone="green">{result.latencyMs} ms</Badge>}
      </div>
      {result.target && <p className="mt-1 break-all font-mono text-xs text-zinc-500 dark:text-zinc-400">{result.target}</p>}
      {result.ok && (
        <p className="mt-2 text-sm text-zinc-600 dark:text-zinc-300">
          Schreiben, Auflisten und Löschen klappen. {result.archives > 0 ? `${result.archives} vorhandene Sicherung(en) gefunden.` : "Noch keine Sicherungen vorhanden."}
        </p>
      )}
      {result.error && <p className="mt-2 break-words font-mono text-xs text-red-700 dark:text-red-300">{result.error}</p>}
    </div>
  );
}

export function BackupSettingsForm() {
  const qc = useQueryClient();
  const toast = useToast();
  const q = useQuery({ queryKey: ["admin", "settings"], queryFn: adminApi.settings, staleTime: 60_000 });
  const { draft, setDraft, dirty, reset } = useDraft(q.data?.backup);
  const [test, setTest] = useState<BackupTestResult | null>(null);

  const save = useMutation({
    mutationFn: (backup: BackupSettings) => adminApi.saveSettings({ backup }),
    onSuccess: (s) => {
      qc.setQueryData(["admin", "settings"], s);
      void qc.invalidateQueries({ queryKey: ["admin", "backups"] });
      toast.success("Backup-Einstellungen gespeichert");
    },
    onError: (e) => toast.error(e),
  });
  const testMut = useMutation({
    mutationFn: adminApi.testBackupTarget,
    onMutate: () => setTest(null),
    onSuccess: setTest,
    onError: (e) => toast.error(e),
  });

  if (q.isLoading) return <Skeleton className="h-96 rounded-xl" />;
  if (q.error || !draft) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;

  const set = (fn: (d: BackupSettings) => BackupSettings) => setDraft(fn);
  const t = draft.target;
  const setTarget = (patch: Partial<BackupSettings["target"]>) => set((d) => ({ ...d, target: { ...d.target, ...patch } }));
  const setSchedule = (patch: Partial<BackupSettings["schedule"]>) => set((d) => ({ ...d, schedule: { ...d.schedule, ...patch } }));

  const passwordMissing = draft.encrypt && draft.password !== MASK && [...draft.password].length < MIN_PASSWORD;
  const targetInvalid = backupTargetInvalid(t);
  const valid = !passwordMissing && !targetInvalid;

  return (
    <>
      <Card>
        <CardHeader title="Backup-Einstellungen" description="Wohin gesichert wird, wie oft und wie viele Archive aufgehoben werden." />
        <CardBody className="divide-y divide-zinc-100 dark:divide-zinc-800">
          <FormSection
            title="Speicherziel"
            description="Ein Archiv enthält Datenbank, alle Mails, Kalender, Einstellungen, DKIM-Schlüssel und Zertifikate."
          >
            <BackupTargetFields
              target={t}
              onChange={(patch) => {
                setTarget(patch);
                if (patch.type) setTest(null);
              }}
            />

            <div className="space-y-3">
              <Button icon={PlugZap} onClick={() => testMut.mutate(draft.target)} loading={testMut.isPending} disabled={targetInvalid}>
                Speicherziel testen
              </Button>
              <TestPanel result={test} pending={testMut.isPending} />
            </div>
          </FormSection>

          <FormSection title="Zeitplan" description="Automatische Sicherung im Hintergrund. Der Server bleibt dabei erreichbar.">
            <SwitchRow label="Automatisch sichern" checked={draft.schedule.enabled} onChange={(v) => setSchedule({ enabled: v })} />
            <div className="grid gap-4 sm:grid-cols-3">
              <Field label="Häufigkeit">
                {(id) => (
                  <Select
                    id={id}
                    value={draft.schedule.frequency}
                    disabled={!draft.schedule.enabled}
                    onChange={(e) => setSchedule({ frequency: e.target.value as BackupSettings["schedule"]["frequency"] })}
                    options={[
                      { value: "hourly", label: "Stündlich" },
                      { value: "daily", label: "Täglich" },
                      { value: "weekly", label: "Wöchentlich" },
                    ]}
                  />
                )}
              </Field>
              {draft.schedule.frequency === "weekly" && (
                <Field label="Wochentag">
                  {(id) => (
                    <Select
                      id={id}
                      value={String(draft.schedule.weekday)}
                      disabled={!draft.schedule.enabled}
                      onChange={(e) => setSchedule({ weekday: Number(e.target.value) })}
                      options={weekdays.map((w, i) => ({ value: String(i), label: w }))}
                    />
                  )}
                </Field>
              )}
              {draft.schedule.frequency !== "hourly" && (
                <Field label="Uhrzeit">
                  {(id) => (
                    <Input
                      id={id}
                      type="time"
                      value={draft.schedule.time}
                      disabled={!draft.schedule.enabled}
                      onChange={(e) => setSchedule({ time: e.target.value })}
                      className="w-36 tabular-nums"
                    />
                  )}
                </Field>
              )}
            </div>
          </FormSection>

          <FormSection title="Aufbewahrung" description="Nach jeder erfolgreichen Sicherung werden die ältesten Archive auf dem Ziel gelöscht.">
            <Field label="Sicherungen behalten" help="0 = alle behalten.">
              {(id) => <NumberInput id={id} value={draft.keep} min={0} max={1000} suffix="Archive" onChange={(v) => set((d) => ({ ...d, keep: v }))} />}
            </Field>
          </FormSection>

          <FormSection title="Verschlüsselung" description="AES-256 mit einem Passwort. Empfohlen, sobald Archive auf einem fremden Speicher liegen.">
            <SwitchRow label="Archive verschlüsseln" checked={draft.encrypt} onChange={(v) => set((d) => ({ ...d, encrypt: v }))} />
            {!draft.encrypt && t.type !== "local" && (
              <Notice tone="warning" icon={ShieldAlert} title="Unverschlüsselt auf fremdem Speicher">
                Die Archive enthalten alle Mails, Passwort-Hashes und Schlüssel dieses Servers. Wer Zugriff auf das{" "}
                {t.type === "webdav" ? "WebDAV-Ziel" : "SMB-Share"} hat, kann sie lesen. Verschlüsselung einschalten wird dringend empfohlen.
              </Notice>
            )}
            {draft.encrypt && (
              <>
                <Field label="Backup-Passwort" required error={passwordMissing ? `Mindestens ${MIN_PASSWORD} Zeichen` : undefined}>
                  {(id) => <SecretInput id={id} value={draft.password} onChange={(v) => set((d) => ({ ...d, password: v }))} />}
                </Field>
                <Notice tone="warning" icon={ShieldAlert} title="Passwort sicher aufbewahren">
                  Ohne dieses Passwort lässt sich kein verschlüsseltes Archiv wiederherstellen – auch nicht von uns. Notiere es außerhalb dieses Servers,
                  z. B. im Passwortmanager. Bereits vorhandene Archive behalten ihr altes Passwort.
                </Notice>
              </>
            )}
          </FormSection>
        </CardBody>
      </Card>
      {dirty && test && (
        <p className="mt-3 flex items-center gap-1.5 text-xs text-zinc-500 dark:text-zinc-400">
          <Info className="h-3.5 w-3.5" /> Der Test nutzt die eingegebenen Werte; gespeichert wird erst mit „Speichern“.
        </p>
      )}
      <SaveBar dirty={dirty} saving={save.isPending} onSave={() => valid && save.mutate(draft)} onReset={reset} />
    </>
  );
}

/** Storage target type and its connection fields (also used by the setup assistant). */
export function BackupTargetFields({ target: t, onChange }: { target: BackupTarget; onChange: (patch: Partial<BackupTarget>) => void }) {
  const setTarget = onChange;
  const setWebdav = (patch: Partial<BackupTarget["webdav"]>) => setTarget({ webdav: { ...t.webdav, ...patch } });
  const setSmb = (patch: Partial<BackupTarget["smb"]>) => setTarget({ smb: { ...t.smb, ...patch } });
  const { webdavInvalid } = targetProblems(t);
  return (
    <>
      <TargetPicker value={t.type} onChange={(type) => setTarget({ type })} />

      {t.type === "local" && (
        <Field
          label="Ordner"
          help="Pfad im Container. Leer = /data/backups (liegt dann auf demselben Datenträger – für echten Schutz ein anderes Laufwerk einbinden)."
        >
          {(id) => (
            <Input id={id} value={t.localPath} onChange={(e) => setTarget({ localPath: e.target.value })} placeholder="/data/backups" className="font-mono text-[13px]" />
          )}
        </Field>
      )}

      {t.type === "webdav" && (
        <>
          <Field
            label="WebDAV-URL"
            required
            error={webdavInvalid && t.webdav.url ? "Mit http:// oder https:// beginnen" : undefined}
            help="Nextcloud: https://cloud.example.de/remote.php/dav/files/BENUTZER/Postwerk – der Ordner wird bei Bedarf angelegt."
          >
            {(id) => (
              <Input
                id={id}
                value={t.webdav.url}
                onChange={(e) => setWebdav({ url: e.target.value })}
                placeholder="https://cloud.example.de/remote.php/dav/files/felix/Postwerk"
                className="font-mono text-[13px]"
              />
            )}
          </Field>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Benutzername">
              {(id) => <Input id={id} value={t.webdav.username} onChange={(e) => setWebdav({ username: e.target.value })} autoComplete="off" />}
            </Field>
            <Field label="Passwort" help="Bei Nextcloud ein App-Passwort verwenden.">
              {(id) => <SecretInput id={id} value={t.webdav.password} onChange={(v) => setWebdav({ password: v })} />}
            </Field>
          </div>
          <SwitchRow
            label="Zertifikat nicht prüfen"
            description="Nur für NAS-Geräte mit selbstsigniertem Zertifikat im eigenen Netz."
            checked={t.webdav.skipTlsVerify}
            onChange={(v) => setWebdav({ skipTlsVerify: v })}
          />
        </>
      )}

      {t.type === "smb" && (
        <>
          <div className="grid gap-4 sm:grid-cols-[1fr_8rem]">
            <Field label="Server" required help="Hostname oder IP, z. B. 192.168.1.10 oder nas.local">
              {(id) => <Input id={id} value={t.smb.host} onChange={(e) => setSmb({ host: e.target.value })} placeholder="192.168.1.10" className="font-mono text-[13px]" />}
            </Field>
            <Field label="Port">
              {(id) => <NumberInput id={id} value={t.smb.port} min={1} max={65535} onChange={(v) => setSmb({ port: v })} />}
            </Field>
          </div>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Freigabe" required help="Name der Freigabe, z. B. backups">
              {(id) => <Input id={id} value={t.smb.share} onChange={(e) => setSmb({ share: e.target.value })} placeholder="backups" className="font-mono text-[13px]" />}
            </Field>
            <Field label="Unterordner" help="Optional, wird bei Bedarf angelegt.">
              {(id) => <Input id={id} value={t.smb.path} onChange={(e) => setSmb({ path: e.target.value })} placeholder="postwerk" className="font-mono text-[13px]" />}
            </Field>
          </div>
          <div className="grid gap-4 sm:grid-cols-3">
            <Field label="Benutzername" required>
              {(id) => <Input id={id} value={t.smb.username} onChange={(e) => setSmb({ username: e.target.value })} autoComplete="off" />}
            </Field>
            <Field label="Passwort">
              {(id) => <SecretInput id={id} value={t.smb.password} onChange={(v) => setSmb({ password: v })} />}
            </Field>
            <Field label="Domäne" help="Meist leer lassen.">
              {(id) => <Input id={id} value={t.smb.domain} onChange={(e) => setSmb({ domain: e.target.value })} placeholder="WORKGROUP" />}
            </Field>
          </div>
        </>
      )}
    </>
  );
}

function targetProblems(t: BackupTarget) {
  return {
    webdavInvalid: t.type === "webdav" && !/^https?:\/\/\S+/.test(t.webdav.url.trim()),
    smbInvalid: t.type === "smb" && (!t.smb.host.trim() || !t.smb.share.trim() || !t.smb.username.trim()),
  };
}

/** Required connection fields are missing or malformed. */
export function backupTargetInvalid(t: BackupTarget): boolean {
  const p = targetProblems(t);
  return p.webdavInvalid || p.smbInvalid;
}
