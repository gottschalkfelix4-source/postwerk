import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Archive,
  CalendarClock,
  Download,
  HardDrive,
  History,
  Lock,
  Play,
  RefreshCw,
  RotateCcw,

  Trash2,
  TriangleAlert,
  Upload,
} from "lucide-react";
import { adminApi } from "@/api/endpoints";
import { errorMessage } from "@/api/client";
import type { BackupArchive, BackupJob, BackupRun } from "@/api/types";
import { formatBytes, formatDateTime, formatDuration, formatRelative } from "@/lib/format";
import {
  Badge,
  Button,
  Card,
  CardHeader,
  EmptyState,
  Field,
  Modal,
  Notice,
  PageHeader,
  PasswordInput,
  Input,
  Progress,
  SkeletonRows,
  StatTile,
  TBody,
  THead,
  Table,
  Tabs,
  Td,
  Th,
  Tr,
  useConfirm,
  useToast,
} from "@/components/ui";
import { BackupSettingsForm } from "./BackupSettingsForm";

type Tab = "archives" | "settings";

const keys = {
  status: ["admin", "backups", "status"] as const,
  archives: ["admin", "backups", "archives"] as const,
};

/** "postwerk-20261121-033000…" → ISO date in the server's local time. */
function archiveDate(a: BackupArchive): string {
  const m = /^postwerk-(\d{4})(\d{2})(\d{2})-(\d{2})(\d{2})(\d{2})/.exec(a.name);
  if (!m) return a.modified;
  return new Date(+m[1], +m[2] - 1, +m[3], +m[4], +m[5], +m[6]).toISOString();
}

function JobPanel({ job }: { job: BackupJob }) {
  const fraction = job.total > 0 ? job.done / job.total : 0;
  const restarting = job.kind === "restore" && job.phase === "Neustart";
  return (
    <Card className="p-5">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-2 text-sm font-semibold text-zinc-900 dark:text-zinc-100">
          <RefreshCw className="h-4 w-4 animate-spin text-indigo-500" />
          {job.kind === "restore" ? "Wiederherstellung läuft" : "Sicherung läuft"} – {job.phase}
        </div>
        <span className="text-xs text-zinc-500 dark:text-zinc-400">seit {formatRelative(job.startedAt)}</span>
      </div>
      {job.archive && <p className="mt-1 font-mono text-xs text-zinc-500 dark:text-zinc-400">{job.archive}</p>}
      {!restarting && (
        <div className="mt-3 flex items-center gap-3">
          <Progress value={fraction} tone="indigo" className="flex-1" />
          <span className="w-40 text-right text-xs tabular-nums text-zinc-500 dark:text-zinc-400">
            {formatBytes(job.done)}
            {job.total > 0 && ` / ${formatBytes(job.total)}`}
          </span>
        </div>
      )}
      {restarting && (
        <Notice tone="info" className="mt-3" title="Server startet neu">
          Die Daten werden eingespielt. Die Seite lädt sich neu, sobald der Server wieder erreichbar ist – danach bitte neu anmelden.
        </Notice>
      )}
    </Card>
  );
}

function RunStatus({ run }: { run: BackupRun }) {
  if (run.status === "running") return <Badge tone="indigo" dot>Läuft</Badge>;
  if (run.status === "success") return <Badge tone="green" dot>Erfolgreich</Badge>;
  return <Badge tone="red" dot>Fehlgeschlagen</Badge>;
}

function runSeconds(run: BackupRun): number | null {
  if (!run.finishedAt) return null;
  return Math.max(0, Math.round((new Date(run.finishedAt).getTime() - new Date(run.startedAt).getTime()) / 1000));
}

/** Waits for the server after a restore and reloads the page. */
function useReloadAfterRestart(active: boolean) {
  useEffect(() => {
    if (!active) return;
    let stop = false;
    let seenDown = false;
    const poll = async () => {
      while (!stop) {
        await new Promise((r) => setTimeout(r, 2000));
        try {
          const res = await fetch("/healthz", { cache: "no-store" });
          if (res.ok && seenDown) {
            window.location.href = "/login";
            return;
          }
          if (!res.ok) seenDown = true;
        } catch {
          seenDown = true;
        }
      }
    };
    void poll();
    // Fallback: a quick restart may not be noticed as "down".
    const t = window.setTimeout(() => {
      window.location.href = "/login";
    }, 45_000);
    return () => {
      stop = true;
      window.clearTimeout(t);
    };
  }, [active]);
}

function RestoreDialog({ archive, onClose }: { archive: BackupArchive | null; onClose: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [password, setPassword] = useState("");
  const [confirmText, setConfirmText] = useState("");
  useEffect(() => {
    setPassword("");
    setConfirmText("");
  }, [archive]);
  const restore = useMutation({
    mutationFn: () => adminApi.restoreBackup(archive!.name, password),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: keys.status });
      toast.info("Wiederherstellung gestartet", "Der Server startet danach automatisch neu.");
      onClose();
    },
  });
  const ready = confirmText.trim().toUpperCase() === "WIEDERHERSTELLEN";
  return (
    <Modal
      open={archive !== null}
      onClose={onClose}
      title="Sicherung wiederherstellen?"
      icon={<TriangleAlert className="h-5 w-5 text-amber-500" />}
      description={archive ? `${archive.name} vom ${formatDateTime(archiveDate(archive))}` : undefined}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Abbrechen
          </Button>
          <Button variant="danger" icon={RotateCcw} disabled={!ready} loading={restore.isPending} onClick={() => restore.mutate()}>
            Wiederherstellen und neu starten
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <Notice tone="warning" title="Der aktuelle Stand wird ersetzt">
          Alle Konten, Mails, Einstellungen und Zertifikate werden auf den Stand der Sicherung zurückgesetzt. Mails, die seitdem
          angekommen sind, fehlen danach. Der bisherige Stand bleibt einmalig im Datenverzeichnis unter <code className="font-mono">pre-restore-…</code>{" "}
          erhalten. Der Server startet dafür neu (etwa 10–30 Sekunden).
        </Notice>
        {archive?.encrypted && (
          <Field label="Backup-Passwort" help="Leer lassen, wenn das Archiv mit dem aktuell eingestellten Passwort erstellt wurde.">
            {(id) => <PasswordInput id={id} value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="off" />}
          </Field>
        )}
        <Field label={<>Zur Bestätigung <strong>WIEDERHERSTELLEN</strong> eingeben</>}>
          {(id) => <Input id={id} value={confirmText} onChange={(e) => setConfirmText(e.target.value)} autoComplete="off" />}
        </Field>
        {restore.error && <Notice tone="error">{errorMessage(restore.error)}</Notice>}
      </div>
    </Modal>
  );
}

function ArchivesTab({ onOpenSettings }: { onOpenSettings: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const confirm = useConfirm();
  const fileRef = useRef<HTMLInputElement>(null);
  const [restoreTarget, setRestoreTarget] = useState<BackupArchive | null>(null);
  const [upload, setUpload] = useState<number | null>(null);

  const status = useQuery({
    queryKey: keys.status,
    queryFn: adminApi.backupStatus,
    refetchInterval: (q) => (q.state.data?.status.job ? 1500 : 30_000),
    // During the restart after a restore the server is briefly unreachable.
    retry: true,
  });
  const job = status.data?.status.job ?? null;
  const archives = useQuery({ queryKey: keys.archives, queryFn: adminApi.backupArchives });

  // Refresh the archive list once a job finishes.
  const hadJob = useRef(false);
  useEffect(() => {
    if (job) hadJob.current = true;
    else if (hadJob.current) {
      hadJob.current = false;
      void qc.invalidateQueries({ queryKey: keys.archives });
    }
  }, [job, qc]);
  useReloadAfterRestart(job?.kind === "restore" && job.phase === "Neustart");

  const start = useMutation({
    mutationFn: adminApi.startBackup,
    onSuccess: () => void qc.invalidateQueries({ queryKey: keys.status }),
    onError: (e) => toast.error(e),
  });
  const remove = useMutation({
    mutationFn: adminApi.deleteBackup,
    onSuccess: () => {
      toast.success("Sicherung gelöscht");
      void qc.invalidateQueries({ queryKey: keys.archives });
    },
    onError: (e) => toast.error(e),
  });

  const onUpload = async (file: File) => {
    setUpload(0);
    try {
      const res = await adminApi.uploadBackup(file, { onProgress: setUpload });
      toast.success("Archiv hochgeladen", res.name);
      void qc.invalidateQueries({ queryKey: keys.archives });
    } catch (e) {
      toast.error(e);
    } finally {
      setUpload(null);
      if (fileRef.current) fileRef.current.value = "";
    }
  };

  const onDelete = async (a: BackupArchive) => {
    const ok = await confirm({
      title: "Sicherung löschen?",
      message: `${a.name} wird endgültig vom Speicherziel gelöscht.`,
      confirmLabel: "Löschen",
      danger: true,
    });
    if (ok) remove.mutate(a.name);
  };

  const list = archives.data?.archives ?? [];
  const runs = status.data?.runs ?? [];
  const lastOk = runs.find((r) => r.status === "success" && r.trigger !== "restore");
  const last = status.data?.status.last;
  const totalSize = list.reduce((n, a) => n + a.size, 0);

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center justify-end gap-2">
        <input
          ref={fileRef}
          type="file"
          accept=".gz,.enc,application/gzip,application/octet-stream"
          className="hidden"
          onChange={(e) => {
            const f = e.target.files?.[0];
            if (f) void onUpload(f);
          }}
        />
        <Button icon={Upload} onClick={() => fileRef.current?.click()} loading={upload !== null} disabled={upload !== null}>
          {upload !== null ? `Hochladen … ${Math.round(upload * 100)} %` : "Archiv hochladen"}
        </Button>
        <Button variant="primary" icon={Play} onClick={() => start.mutate()} loading={start.isPending} disabled={!!job}>
          Jetzt sichern
        </Button>
      </div>

      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <StatTile
          label="Letzte Sicherung"
          value={lastOk ? formatRelative(lastOk.startedAt) : "–"}
          hint={lastOk ? formatDateTime(lastOk.startedAt) : "Noch keine erfolgreiche Sicherung"}
          icon={<History className="h-4 w-4 text-zinc-400" />}
        />
        <StatTile
          label="Nächste Sicherung"
          value={status.data?.status.nextRun ? formatDateTime(status.data.status.nextRun) : "Aus"}
          hint={
            status.data?.status.nextRun ? (
              "laut Zeitplan"
            ) : (
              <button type="button" onClick={onOpenSettings} className="text-indigo-600 hover:underline dark:text-indigo-300">
                Zeitplan einrichten
              </button>
            )
          }
          icon={<CalendarClock className="h-4 w-4 text-zinc-400" />}
        />
        <StatTile
          label="Archive"
          value={list.length}
          hint={list.length > 0 ? `${formatBytes(totalSize)} insgesamt` : "auf dem Speicherziel"}
          icon={<Archive className="h-4 w-4 text-zinc-400" />}
        />
        <StatTile
          label="Speicherziel"
          value={<span className="block truncate text-base font-medium" title={status.data?.status.target}>{status.data?.status.target || "–"}</span>}
          hint={
            <button type="button" onClick={onOpenSettings} className="text-indigo-600 hover:underline dark:text-indigo-300">
              Ändern
            </button>
          }
          icon={<HardDrive className="h-4 w-4 text-zinc-400" />}
        />
      </div>

      {job && <JobPanel job={job} />}
      {!job && last && !last.ok && (
        <Notice tone="error" title={last.kind === "restore" ? "Wiederherstellung fehlgeschlagen" : "Letzte Sicherung fehlgeschlagen"}>
          <span className="break-words">{last.error}</span>
        </Notice>
      )}

      <Card className="overflow-hidden">
        <CardHeader
          title="Sicherungen"
          description="Archive auf dem Speicherziel, neueste zuerst."
          actions={
            <Button size="sm" icon={RefreshCw} onClick={() => void archives.refetch()} loading={archives.isFetching && !archives.isLoading}>
              Aktualisieren
            </Button>
          }
        />
        {archives.isLoading ? (
          <SkeletonRows rows={3} cols={4} />
        ) : archives.data?.error ? (
          <div className="p-5">
            <Notice tone="error" title="Speicherziel nicht erreichbar">
              <span className="break-words">{archives.data.error}</span>{" "}
              <button type="button" onClick={onOpenSettings} className="font-medium underline">
                Einstellungen prüfen
              </button>
            </Notice>
          </div>
        ) : list.length === 0 ? (
          <EmptyState
            icon={Archive}
            title="Noch keine Sicherung"
            description="Lege die erste Sicherung an oder richte einen Zeitplan ein. Archive anderer Postwerk-Server kannst du hochladen und hier wiederherstellen."
            action={
              <Button variant="primary" icon={Play} onClick={() => start.mutate()} disabled={!!job}>
                Jetzt sichern
              </Button>
            }
          />
        ) : (
          <Table>
            <THead>
              <tr>
                <Th>Archiv</Th>
                <Th>Erstellt</Th>
                <Th className="text-right">Größe</Th>
                <Th className="text-right">Aktionen</Th>
              </tr>
            </THead>
            <TBody>
              {list.map((a) => (
                <Tr key={a.name}>
                  <Td className="min-w-[16rem]">
                    <div className="flex items-center gap-2">
                      {a.encrypted ? (
                        <Lock className="h-4 w-4 shrink-0 text-emerald-600" aria-label="verschlüsselt" />
                      ) : (
                        <Archive className="h-4 w-4 shrink-0 text-zinc-400" />
                      )}
                      <span className="font-mono text-[13px] text-zinc-800 dark:text-zinc-200">{a.name}</span>
                      {a.encrypted && <Badge tone="green">verschlüsselt</Badge>}
                    </div>
                  </Td>
                  <Td className="whitespace-nowrap text-zinc-500 dark:text-zinc-400" title={formatDateTime(archiveDate(a))}>
                    {formatRelative(archiveDate(a))}
                  </Td>
                  <Td className="whitespace-nowrap text-right tabular-nums">{formatBytes(a.size)}</Td>
                  <Td className="whitespace-nowrap text-right">
                    <div className="inline-flex items-center gap-1">
                      <a
                        href={adminApi.backupDownloadUrl(a.name)}
                        download={a.name}
                        title="Herunterladen"
                        aria-label="Herunterladen"
                        className="inline-flex h-7 w-7 items-center justify-center rounded-md text-zinc-500 hover:bg-zinc-100 hover:text-zinc-900 dark:text-zinc-400 dark:hover:bg-zinc-800 dark:hover:text-zinc-100"
                      >
                        <Download className="h-4 w-4" />
                      </a>
                      <Button size="icon-sm" variant="ghost" icon={RotateCcw} title="Wiederherstellen" aria-label="Wiederherstellen" disabled={!!job} onClick={() => setRestoreTarget(a)} />
                      <Button size="icon-sm" variant="danger-ghost" icon={Trash2} title="Löschen" aria-label="Löschen" disabled={!!job} onClick={() => void onDelete(a)} />
                    </div>
                  </Td>
                </Tr>
              ))}
            </TBody>
          </Table>
        )}
      </Card>

      <Card className="overflow-hidden">
        <CardHeader title="Verlauf" description="Die letzten Sicherungen und Wiederherstellungen." />
        {runs.length === 0 ? (
          <p className="px-5 py-6 text-sm text-zinc-500 dark:text-zinc-400">Noch keine Läufe.</p>
        ) : (
          <Table>
            <THead>
              <tr>
                <Th>Start</Th>
                <Th>Auslöser</Th>
                <Th>Status</Th>
                <Th>Archiv</Th>
                <Th className="text-right">Größe</Th>
                <Th className="text-right">Dauer</Th>
              </tr>
            </THead>
            <TBody>
              {runs.map((r) => {
                const secs = runSeconds(r);
                return (
                  <Tr key={r.id}>
                    <Td className="whitespace-nowrap" title={formatDateTime(r.startedAt)}>
                      {formatDateTime(r.startedAt)}
                    </Td>
                    <Td>{r.trigger === "schedule" ? "Zeitplan" : r.trigger === "restore" ? "Wiederherstellung" : "Manuell"}</Td>
                    <Td>
                      <RunStatus run={r} />
                    </Td>
                    <Td className="max-w-[28rem]">
                      {r.error ? (
                        <span className="break-words text-xs text-red-600 dark:text-red-400">{r.error}</span>
                      ) : (
                        <span className="font-mono text-xs text-zinc-500 dark:text-zinc-400">{r.archive}</span>
                      )}
                    </Td>
                    <Td className="whitespace-nowrap text-right tabular-nums">{r.size > 0 ? formatBytes(r.size) : "–"}</Td>
                    <Td className="whitespace-nowrap text-right tabular-nums">{secs !== null ? formatDuration(secs) : "–"}</Td>
                  </Tr>
                );
              })}
            </TBody>
          </Table>
        )}
      </Card>

      <RestoreDialog archive={restoreTarget} onClose={() => setRestoreTarget(null)} />
    </div>
  );
}

export default function BackupPage() {
  const [tab, setTab] = useState<Tab>("archives");
  return (
    <>
      <PageHeader
        title="Backup"
        description="Sicherungen des gesamten Systems anlegen, verwalten und wiederherstellen – lokal, per WebDAV oder auf einer SMB-Freigabe."
      />
      <Tabs
        className="mb-6"
        value={tab}
        onChange={setTab}
        items={[
          { value: "archives", label: "Sicherungen" },
          { value: "settings", label: "Einstellungen" },
        ]}
      />
      {tab === "archives" ? <ArchivesTab onOpenSettings={() => setTab("settings")} /> : <BackupSettingsForm />}
    </>
  );
}

