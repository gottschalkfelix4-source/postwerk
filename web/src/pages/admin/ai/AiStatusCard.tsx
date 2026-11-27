import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Activity, CirclePause, RefreshCw, RotateCcw, TriangleAlert } from "lucide-react";
import { adminApi } from "@/api/endpoints";
import { errorMessage } from "@/api/client";
import type { AiWorkerStatus } from "@/api/types";
import { keptLabel } from "@/lib/aiKeep";
import { formatDateTime, formatRelative } from "@/lib/format";
import { Badge, Button, Card, CardBody, CardHeader, Notice, Spinner, useToast } from "@/components/ui";

const STATUS_KEY = ["admin", "ai", "status"] as const;

function Stat({ label, value, hint }: { label: string; value: number | string; hint?: string }) {
  return (
    <div className="rounded-xl border border-zinc-200 p-3 dark:border-zinc-800">
      <div className="text-xs text-zinc-500 dark:text-zinc-400">{label}</div>
      <div className="mt-1 text-xl font-semibold tabular-nums text-zinc-900 dark:text-zinc-100">{value}</div>
      {hint && <div className="mt-0.5 text-xs text-zinc-500 dark:text-zinc-400">{hint}</div>}
    </div>
  );
}

function StateBadge({ s }: { s: AiWorkerStatus }) {
  if (!s.enabled) return <Badge tone="neutral">Ausgeschaltet</Badge>;
  if (s.worker?.pausedUntil) return <Badge tone="amber" dot>Pausiert</Badge>;
  if (s.queue.due > 0) return <Badge tone="blue" dot>Arbeitet</Badge>;
  return <Badge tone="green" dot>Bereit</Badge>;
}

/** Live state of the background analysis: pause, queue, failures and why mail stayed in the inbox. */
export function AiStatusCard() {
  const qc = useQueryClient();
  const toast = useToast();
  const q = useQuery({ queryKey: STATUS_KEY, queryFn: adminApi.aiWorkerStatus, refetchInterval: 15_000 });
  const retry = useMutation({
    mutationFn: adminApi.aiRetry,
    onSuccess: (r) => {
      toast.success(r.reset > 0 ? `${r.reset} Nachrichten werden erneut analysiert.` : "Analyse fortgesetzt.");
      void qc.invalidateQueries({ queryKey: STATUS_KEY });
    },
    onError: (err) => toast.error(errorMessage(err)),
  });

  const s = q.data;
  const w = s?.worker;
  const keptEntries = Object.entries(w?.kept ?? {}).filter(([, n]) => n > 0).sort((a, b) => b[1] - a[1]);
  const keptTotal = keptEntries.reduce((sum, [, n]) => sum + n, 0);
  const canRetry = !!s && (s.queue.waiting > 0 || !!w?.pausedUntil);

  return (
    <Card className="mb-6">
      <CardHeader
        icon={<Activity className="h-5 w-5 text-zinc-400" />}
        title={
          <span className="flex items-center gap-2">
            Status der Analyse {s && <StateBadge s={s} />}
          </span>
        }
        description={
          w ? `Zähler seit dem Serverstart ${formatRelative(w.since)} (${formatDateTime(w.since)}).` : "Was die KI im Hintergrund gerade tut."
        }
        actions={
          <div className="flex gap-2">
            <Button size="sm" variant="ghost" icon={RefreshCw} loading={q.isFetching} onClick={() => void q.refetch()}>
              Aktualisieren
            </Button>
            <Button size="sm" variant="secondary" icon={RotateCcw} disabled={!canRetry} loading={retry.isPending} onClick={() => retry.mutate()}>
              Jetzt erneut versuchen
            </Button>
          </div>
        }
      />
      <CardBody className="space-y-5">
        {q.isLoading ? (
          <div className="flex items-center gap-2 text-sm text-zinc-500 dark:text-zinc-400">
            <Spinner className="h-4 w-4" /> Status wird geladen …
          </div>
        ) : q.error || !s ? (
          <Notice tone="error" icon={TriangleAlert}>
            {errorMessage(q.error)}
          </Notice>
        ) : (
          <>
            {w?.pausedUntil && (
              <Notice tone="warning" icon={CirclePause} title={`Analyse pausiert bis ${formatDateTime(w.pausedUntil)}`}>
                <span className="block break-words font-mono text-xs font-normal">{w.pauseReason}</span>
                <span className="mt-1 block text-xs">
                  Während der Pause bleiben neue Mails unsortiert im Posteingang. Speichern der KI-Einstellungen oder „Jetzt erneut versuchen“ beendet die Pause.
                </span>
              </Notice>
            )}
            {s.enabled && !s.autoFile && (
              <Notice tone="info">Automatisches Einsortieren ist ausgeschaltet – analysierte Mails bleiben im Posteingang.</Notice>
            )}

            <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
              <Stat label="Wartet auf Analyse" value={s.queue.due} />
              <Stat label="Fehlgeschlagen, Wiederholung geplant" value={s.queue.waiting} />
              <Stat
                label="Analysiert (letzte 24 h)"
                value={s.queue.analyzed24h}
                hint={`von ${s.queue.received24h} empfangenen`}
              />
              <Stat label="Analysiert insgesamt" value={s.queue.analyzed} />
            </div>

            {w && (
              <div>
                <div className="mb-2 text-sm font-medium text-zinc-900 dark:text-zinc-100">
                  Seit Serverstart: {w.analyzed} analysiert, {w.filed} einsortiert, {w.junk} als Spam verschoben, {keptTotal} im Posteingang geblieben
                  {w.failed > 0 && `, ${w.failed} Fehler`}
                </div>
                {keptEntries.length > 0 ? (
                  <ul className="divide-y divide-zinc-100 rounded-xl border border-zinc-200 text-sm dark:divide-zinc-800 dark:border-zinc-800">
                    {keptEntries.map(([reason, n]) => (
                      <li key={reason} className="flex items-center justify-between gap-3 px-3 py-2">
                        <span className="text-zinc-700 dark:text-zinc-300">{keptLabel(reason, s.mode)}</span>
                        <Badge tone="neutral">{n}</Badge>
                      </li>
                    ))}
                  </ul>
                ) : (
                  <p className="text-xs text-zinc-500 dark:text-zinc-400">Noch keine Mail ist nach der Analyse im Posteingang geblieben.</p>
                )}
                {w.lastAnalyzedAt && (
                  <p className="mt-2 text-xs text-zinc-500 dark:text-zinc-400">Letzte Analyse {formatRelative(w.lastAnalyzedAt)}.</p>
                )}
                {w.lastError && w.lastErrorAt && !(w.pausedUntil && w.lastError === w.pauseReason) && (
                  <p className="mt-1 break-words text-xs text-red-700 dark:text-red-300">
                    Letzter Fehler {formatRelative(w.lastErrorAt)}: <span className="font-mono">{w.lastError}</span>
                  </p>
                )}
              </div>
            )}

            {s.failures.length > 0 && (
              <div>
                <div className="mb-2 text-sm font-medium text-zinc-900 dark:text-zinc-100">Fehlgeschlagene Nachrichten</div>
                <ul className="divide-y divide-zinc-100 rounded-xl border border-zinc-200 text-sm dark:divide-zinc-800 dark:border-zinc-800">
                  {s.failures.map((f) => (
                    <li key={f.id} className="space-y-1 px-3 py-2">
                      <div className="flex flex-wrap items-center justify-between gap-2">
                        <span className="min-w-0 truncate font-medium text-zinc-800 dark:text-zinc-200" title={f.subject}>
                          {f.subject || "(ohne Betreff)"}
                        </span>
                        <span className="flex shrink-0 items-center gap-2 text-xs text-zinc-500 dark:text-zinc-400">
                          <Badge tone="red">{f.attempts}× fehlgeschlagen</Badge>
                          {f.nextAttemptAt && <>nächster Versuch {formatRelative(f.nextAttemptAt)}</>}
                        </span>
                      </div>
                      <div className="truncate text-xs text-zinc-500 dark:text-zinc-400">
                        {f.from} → {f.account} · {formatDateTime(f.receivedAt)}
                      </div>
                      <div className="line-clamp-2 break-words font-mono text-xs text-red-700 dark:text-red-300" title={f.lastError}>
                        {f.lastError}
                      </div>
                    </li>
                  ))}
                </ul>
              </div>
            )}
          </>
        )}
      </CardBody>
    </Card>
  );
}
