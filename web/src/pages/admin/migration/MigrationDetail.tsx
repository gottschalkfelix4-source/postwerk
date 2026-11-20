import { useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowLeft,
  ArrowRightLeft,
  CircleAlert,
  CircleSlash,
  Copy,
  Download,
  Lock,
  RotateCcw,
  ScrollText,
  SkipForward,
  Trash2,
  Users,
} from "lucide-react";
import { adminApi } from "@/api/endpoints";
import type { LogEntry, MigrationJob } from "@/api/types";
import { formatDateTime, formatDuration, formatNumber } from "@/lib/format";
import { cn, copyToClipboard } from "@/lib/utils";
import {
  Button,
  Card,
  CardHeader,
  EmptyState,
  ErrorState,
  Field,
  Modal,
  Notice,
  PasswordInput,
  Progress,
  Skeleton,
  StatTile,
  TBody,
  THead,
  Table,
  Td,
  Th,
  Tr,
  useConfirm,
  useToast,
} from "@/components/ui";
import { levelTextClass } from "../shared";
import { accountProgress, canRestart, isActive, jobProgress, MigrationStatusBadge, percent, securityLabels } from "./shared";

function formatLogTime(value: string): string {
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return value;
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${pad(d.getDate())}.${pad(d.getMonth() + 1)}. ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

export default function MigrationDetailPage() {
  const { id: idParam } = useParams();
  const id = Number(idParam);
  const navigate = useNavigate();
  const qc = useQueryClient();
  const toast = useToast();
  const confirm = useConfirm();
  const [restartOpen, setRestartOpen] = useState(false);

  const q = useQuery({
    queryKey: ["admin", "migration", id],
    queryFn: () => adminApi.migration(id),
    enabled: Number.isFinite(id),
    refetchInterval: (query) => (query.state.data && isActive(query.state.data.status) ? 2000 : false),
  });
  const job = q.data;

  const refresh = (j?: MigrationJob) => {
    if (j) qc.setQueryData(["admin", "migration", id], j);
    void qc.invalidateQueries({ queryKey: ["admin", "migration", id] });
    void qc.invalidateQueries({ queryKey: ["admin", "migrations"] });
  };

  const cancel = useMutation({
    mutationFn: () => adminApi.cancelMigration(id),
    onSuccess: () => {
      toast.success("Migration wird abgebrochen");
      refresh();
    },
    onError: (err) => toast.error(err, "Abbrechen fehlgeschlagen"),
  });

  const remove = useMutation({
    mutationFn: () => adminApi.deleteMigration(id),
    onSuccess: () => {
      toast.success("Migration gelöscht");
      qc.removeQueries({ queryKey: ["admin", "migration", id] });
      void qc.invalidateQueries({ queryKey: ["admin", "migrations"] });
      navigate("/admin/migration", { replace: true });
    },
    onError: (err) => toast.error(err, "Löschen fehlgeschlagen"),
  });

  return (
    <>
      <Link
        to="/admin/migration"
        className="mb-4 inline-flex items-center gap-1.5 text-sm text-zinc-500 hover:text-zinc-800 dark:text-zinc-400 dark:hover:text-zinc-200"
      >
        <ArrowLeft className="h-4 w-4" /> Alle Migrationen
      </Link>

      {q.isLoading ? (
        <div className="space-y-6">
          <Skeleton className="h-28 rounded-xl" />
          <Skeleton className="h-64 rounded-xl" />
        </div>
      ) : q.error || !job ? (
        <Card>
          {q.error ? (
            <ErrorState error={q.error} onRetry={() => void q.refetch()} />
          ) : (
            <EmptyState icon={ArrowRightLeft} title="Migration nicht gefunden" />
          )}
        </Card>
      ) : (
        <>
          <Header
            job={job}
            onCancel={async () => {
              const ok = await confirm({
                title: "Migration abbrechen?",
                message: "Bereits übernommene Nachrichten bleiben erhalten. Du kannst die Migration später erneut starten.",
                confirmLabel: "Abbrechen",
                cancelLabel: "Weiterlaufen lassen",
                danger: true,
              });
              if (ok) cancel.mutate();
            }}
            cancelling={cancel.isPending}
            onRestart={() => setRestartOpen(true)}
            onDelete={async () => {
              const ok = await confirm({
                title: "Migration löschen?",
                message: "Der Job und sein Protokoll werden entfernt. Importierte Nachrichten bleiben in den Postfächern.",
                confirmLabel: "Löschen",
                danger: true,
              });
              if (ok) remove.mutate();
            }}
            deleting={remove.isPending}
          />
          <AccountsTable job={job} />
          <LogCard log={job.log} live={isActive(job.status)} />
          <RestartDialog
            job={job}
            open={restartOpen}
            onClose={() => setRestartOpen(false)}
            onRestarted={(j) => {
              setRestartOpen(false);
              toast.success("Migration neu gestartet", "Bereits übernommene Nachrichten werden übersprungen.");
              refresh(j);
            }}
          />
        </>
      )}
    </>
  );
}

function Header({
  job,
  onCancel,
  cancelling,
  onRestart,
  onDelete,
  deleting,
}: {
  job: MigrationJob;
  onCancel: () => void;
  cancelling: boolean;
  onRestart: () => void;
  onDelete: () => void;
  deleting: boolean;
}) {
  const p = jobProgress(job);
  const sum = (key: "imported" | "skipped" | "failed") => job.accounts.reduce((n, a) => n + a[key], 0);
  const messagesDone = job.accounts.reduce((n, a) => n + a.messagesDone, 0);
  const messagesTotal = job.accounts.reduce((n, a) => n + a.messagesTotal, 0);
  const active = isActive(job.status);
  const elapsed = job.startedAt
    ? ((job.finishedAt ? new Date(job.finishedAt).getTime() : Date.now()) - new Date(job.startedAt).getTime()) / 1000
    : null;

  return (
    <div className="mb-6 space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="flex min-w-0 items-center gap-4">
          <span className="flex h-12 w-12 shrink-0 items-center justify-center rounded-xl bg-indigo-50 text-indigo-600 dark:bg-indigo-500/15 dark:text-indigo-300">
            <ArrowRightLeft className="h-6 w-6" />
          </span>
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2">
              <h1 className="truncate text-2xl font-semibold tracking-tight text-zinc-900 dark:text-zinc-50">{job.name || `Migration #${job.id}`}</h1>
              <MigrationStatusBadge status={job.status} />
            </div>
            <p className="mt-0.5 text-sm text-zinc-500 dark:text-zinc-400">
              <code className="font-mono text-[13px]">
                {job.source.host}:{job.source.port}
              </code>{" "}
              · {securityLabels[job.source.security]} · erstellt {formatDateTime(job.createdAt)}
            </p>
          </div>
        </div>
        <div className="flex flex-wrap gap-2">
          {active && (
            <Button variant="danger-ghost" icon={CircleSlash} onClick={onCancel} loading={cancelling}>
              Abbrechen
            </Button>
          )}
          {canRestart(job.status) && (
            <Button icon={RotateCcw} onClick={onRestart}>
              Erneut starten
            </Button>
          )}
          {!active && (
            <Button variant="danger-ghost" icon={Trash2} onClick={onDelete} loading={deleting}>
              Löschen
            </Button>
          )}
        </div>
      </div>

      {job.status === "interrupted" && (
        <Notice tone="warning" icon={Lock} title="Migration wurde unterbrochen">
          Der Server wurde während der Migration neu gestartet. Da Zugangsdaten nie gespeichert werden, musst du die Passwörter der Quelle
          erneut eingeben, um fortzufahren.
        </Notice>
      )}

      <Card className="p-5">
        <div className="mb-2 flex flex-wrap items-baseline justify-between gap-2">
          <span className="text-sm font-medium text-zinc-700 dark:text-zinc-300">Gesamtfortschritt</span>
          <span className="text-sm tabular-nums text-zinc-500 dark:text-zinc-400">
            {messagesTotal > 0 && `${formatNumber(messagesDone)} von ${formatNumber(messagesTotal)} Nachrichten · `}
            <span className="font-semibold text-zinc-900 dark:text-zinc-100">{percent(p)}</span>
          </span>
        </div>
        <Progress value={p} tone="indigo" className={cn("h-2.5", active && "[&>div]:animate-pulse")} />
        <div className="mt-3 flex flex-wrap gap-x-6 gap-y-1 text-xs text-zinc-500 dark:text-zinc-400">
          <span>Gestartet: {formatDateTime(job.startedAt)}</span>
          {job.finishedAt && <span>Beendet: {formatDateTime(job.finishedAt)}</span>}
          {elapsed !== null && elapsed >= 0 && <span>Dauer: {formatDuration(elapsed)}</span>}
        </div>
      </Card>

      <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        <StatTile label="Postfächer" value={job.accounts.length} icon={<Users className="h-4 w-4 text-zinc-400" />} />
        <StatTile label="Importiert" value={formatNumber(sum("imported"))} icon={<Download className="h-4 w-4 text-emerald-500" />} />
        <StatTile label="Übersprungen" value={formatNumber(sum("skipped"))} hint="Bereits vorhanden" icon={<SkipForward className="h-4 w-4 text-zinc-400" />} />
        <StatTile
          label="Fehlerhaft"
          value={formatNumber(sum("failed"))}
          icon={<CircleAlert className={cn("h-4 w-4", sum("failed") > 0 ? "text-red-500" : "text-zinc-400")} />}
        />
      </div>
    </div>
  );
}

function AccountsTable({ job }: { job: MigrationJob }) {
  return (
    <Card className="mb-6 overflow-hidden">
      <CardHeader title="Postfächer" description="Fortschritt je Postfach" />
      <Table>
        <THead>
          <tr>
            <Th>Postfach</Th>
            <Th>Status</Th>
            <Th>Ordner</Th>
            <Th className="w-56">Nachrichten</Th>
            <Th className="text-right">Importiert</Th>
            <Th className="text-right">Übersprungen</Th>
            <Th className="text-right">Fehlerhaft</Th>
          </tr>
        </THead>
        <TBody>
          {job.accounts.map((a) => {
            const p = accountProgress(a);
            return (
              <Tr key={a.targetEmail} className="hover:bg-transparent dark:hover:bg-transparent">
                <Td className="align-top">
                  <div className="font-medium text-zinc-900 dark:text-zinc-100">{a.targetEmail}</div>
                  <div className="font-mono text-xs text-zinc-500 dark:text-zinc-400">← {a.username}</div>
                  {a.error && (
                    <div className="mt-1.5 flex max-w-sm items-start gap-1.5 text-xs text-red-600 dark:text-red-400">
                      <CircleAlert className="mt-px h-3.5 w-3.5 shrink-0" />
                      <span className="break-words">{a.error}</span>
                    </div>
                  )}
                </Td>
                <Td className="align-top">
                  <MigrationStatusBadge status={a.status} />
                </Td>
                <Td className="align-top">
                  <div className="tabular-nums">
                    {a.foldersDone} / {a.foldersTotal}
                  </div>
                  {a.currentFolder && a.status === "running" && (
                    <div className="max-w-[12rem] truncate font-mono text-xs text-indigo-600 dark:text-indigo-300" title={a.currentFolder}>
                      {a.currentFolder}
                    </div>
                  )}
                </Td>
                <Td className="align-top">
                  <div className="flex items-center gap-2">
                    <Progress value={p} tone="indigo" className="flex-1" />
                    <span className="w-10 text-right text-xs tabular-nums text-zinc-500 dark:text-zinc-400">{percent(p)}</span>
                  </div>
                  <div className="mt-1 text-xs tabular-nums text-zinc-500 dark:text-zinc-400">
                    {formatNumber(a.messagesDone)} / {formatNumber(a.messagesTotal)}
                  </div>
                </Td>
                <Td className="align-top text-right tabular-nums">{formatNumber(a.imported)}</Td>
                <Td className="align-top text-right tabular-nums text-zinc-500 dark:text-zinc-400">{formatNumber(a.skipped)}</Td>
                <Td className={cn("align-top text-right tabular-nums", a.failed > 0 && "font-medium text-red-600 dark:text-red-400")}>
                  {formatNumber(a.failed)}
                </Td>
              </Tr>
            );
          })}
        </TBody>
      </Table>
    </Card>
  );
}

function LogCard({ log, live }: { log: LogEntry[]; live: boolean }) {
  const toast = useToast();
  const text = log.map((e) => `${e.time} ${e.level.toUpperCase()} ${e.component} ${e.message}`).join("\n");
  return (
    <Card className="overflow-hidden">
      <CardHeader
        title={
          <span className="inline-flex items-center gap-2">
            Protokoll
            {live && <span className="h-2 w-2 animate-pulse rounded-full bg-emerald-500" aria-label="Live" />}
          </span>
        }
        description="Die letzten 100 Einträge, neueste zuerst."
        actions={
          log.length > 0 ? (
            <Button
              size="sm"
              variant="ghost"
              icon={Copy}
              onClick={async () => {
                if (await copyToClipboard(text)) toast.success("Protokoll kopiert");
              }}
            >
              Kopieren
            </Button>
          ) : undefined
        }
      />
      {log.length === 0 ? (
        <EmptyState compact icon={ScrollText} title="Noch keine Einträge" />
      ) : (
        <div className="max-h-[28rem] overflow-auto bg-zinc-50/60 font-mono text-[12.5px] leading-6 dark:bg-zinc-950/60">
          {log.map((e, i) => (
            <div
              key={`${e.time}-${i}`}
              className={cn(
                "flex gap-3 border-b border-zinc-100 px-4 py-1 hover:bg-white dark:border-zinc-900 dark:hover:bg-zinc-900",
                e.level === "error" && "bg-red-50/50 dark:bg-red-500/[0.05]",
                e.level === "warn" && "bg-amber-50/40 dark:bg-amber-500/[0.04]",
              )}
            >
              <span className="shrink-0 text-zinc-400 dark:text-zinc-500" title={e.time}>
                {formatLogTime(e.time)}
              </span>
              <span className={cn("w-12 shrink-0 font-semibold uppercase", levelTextClass[e.level])}>{e.level}</span>
              <span className="min-w-0 flex-1 whitespace-pre-wrap break-words text-zinc-800 dark:text-zinc-200">
                {e.message}
                {e.fields &&
                  Object.entries(e.fields).map(([k, v]) => (
                    <span key={k} className="ml-2 text-zinc-500 dark:text-zinc-400">
                      <span className="text-sky-700 dark:text-sky-400">{k}</span>={v}
                    </span>
                  ))}
              </span>
            </div>
          ))}
        </div>
      )}
    </Card>
  );
}

function RestartDialog({
  job,
  open,
  onClose,
  onRestarted,
}: {
  job: MigrationJob;
  open: boolean;
  onClose: () => void;
  onRestarted: (job: MigrationJob) => void;
}) {
  const toast = useToast();
  const [passwords, setPasswords] = useState<Record<string, string>>({});

  useEffect(() => {
    if (open) setPasswords({});
  }, [open]);

  // Finished mailboxes are optional: without a password they are left out.
  const needsPassword = (status: string) => status !== "done";
  const missing = job.accounts.some((a) => needsPassword(a.status) && !passwords[a.targetEmail]);

  const restart = useMutation({
    mutationFn: () =>
      adminApi.restartMigration(job.id, {
        accounts: job.accounts
          .filter((a) => !!passwords[a.targetEmail])
          .map((a) => ({ targetEmail: a.targetEmail, password: passwords[a.targetEmail] })),
      }),
    onSuccess: onRestarted,
    onError: (err) => toast.error(err, "Neustart fehlgeschlagen"),
  });

  const first = job.accounts.find((a) => passwords[a.targetEmail]);

  return (
    <Modal
      open={open}
      onClose={onClose}
      size="lg"
      dismissible={!restart.isPending}
      title="Migration erneut starten"
      description="Zugangsdaten der Quelle werden nicht gespeichert. Bitte gib die Passwörter erneut ein."
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={restart.isPending}>
            Abbrechen
          </Button>
          <Button variant="primary" icon={RotateCcw} disabled={missing} loading={restart.isPending} onClick={() => restart.mutate()}>
            Erneut starten
          </Button>
        </>
      }
    >
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          if (!missing) restart.mutate();
        }}
      >
        {job.accounts.map((a) => (
          <Field
            key={a.targetEmail}
            label={
              <span className="flex flex-wrap items-center gap-2">
                {a.targetEmail}
                <span className="font-mono text-xs font-normal text-zinc-500 dark:text-zinc-400">← {a.username}</span>
                {!needsPassword(a.status) && <span className="text-xs font-normal text-emerald-600 dark:text-emerald-400">abgeschlossen · optional</span>}
              </span>
            }
          >
            {(id) => (
              <PasswordInput
                id={id}
                value={passwords[a.targetEmail] ?? ""}
                onChange={(e) => setPasswords((p) => ({ ...p, [a.targetEmail]: e.target.value }))}
                autoComplete="off"
                placeholder="Passwort / App-Passwort der Quelle"
              />
            )}
          </Field>
        ))}
        {job.accounts.length > 1 && first && (
          <button
            type="button"
            className="text-xs font-medium text-indigo-600 hover:underline dark:text-indigo-300"
            onClick={() => setPasswords(Object.fromEntries(job.accounts.map((a) => [a.targetEmail, passwords[first.targetEmail]])))}
          >
            Passwort von {first.targetEmail} für alle übernehmen
          </button>
        )}
        <button type="submit" className="hidden" aria-hidden tabIndex={-1} />
      </form>
    </Modal>
  );
}
