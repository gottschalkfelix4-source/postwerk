import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CircleX, Download, FileArchive, PackageOpen } from "lucide-react";
import { atRestApi, atRestKeys, type DataExport } from "@/api/atrest";
import { formatBytes, formatDateTime, formatNumber, formatRelative } from "@/lib/format";
import { Badge, Button, Card, CardBody, CardHeader, Notice, Progress, Skeleton, useToast } from "@/components/ui";

function StatusBadge({ e }: { e: DataExport }) {
  switch (e.status) {
    case "done":
      return <Badge tone="green" dot>Bereit</Badge>;
    case "failed":
      return <Badge tone="red" dot>Fehlgeschlagen</Badge>;
    case "expired":
      return <Badge tone="neutral">Abgelaufen</Badge>;
    default:
      return <Badge tone="indigo" dot>Wird erstellt</Badge>;
  }
}

function ExportRow({ e, downloadUrl }: { e: DataExport; downloadUrl: string }) {
  const busy = e.status === "pending" || e.status === "running";
  return (
    <li className="space-y-2 py-3 first:pt-0 last:pb-0">
      <div className="flex flex-wrap items-center gap-3">
        <FileArchive className="h-5 w-5 shrink-0 text-zinc-400" />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2 text-sm font-medium text-zinc-900 dark:text-zinc-100">
            <span title={formatDateTime(e.createdAt)}>Export vom {formatDateTime(e.createdAt)}</span>
            <StatusBadge e={e} />
          </div>
          <div className="text-xs text-zinc-500 dark:text-zinc-400">
            {e.status === "done" && (
              <>
                {formatBytes(e.size)} · Download bis {e.expiresAt ? formatDateTime(e.expiresAt) : "–"}
              </>
            )}
            {busy && `${e.phase || "Wird vorbereitet"} …`}
            {e.status === "expired" && "Die Datei wurde nach Ablauf der Frist gelöscht."}
            {e.status === "failed" && <span className="text-red-600 dark:text-red-400">{e.error}</span>}
          </div>
        </div>
        {e.status === "done" && (
          <a
            href={downloadUrl}
            download
            className="inline-flex h-8 items-center gap-1.5 rounded-md border border-zinc-200 bg-white px-3 text-sm font-medium text-zinc-800 shadow-sm hover:bg-zinc-50 dark:border-zinc-700 dark:bg-zinc-800/80 dark:text-zinc-100 dark:hover:bg-zinc-700/80"
          >
            <Download className="h-4 w-4" />
            Herunterladen
          </a>
        )}
      </div>
      {busy && (
        <div className="flex items-center gap-3 pl-8">
          <Progress value={e.total > 0 ? e.done / e.total : 0} tone="indigo" className="h-1.5 flex-1" />
          <span className="text-xs tabular-nums text-zinc-500 dark:text-zinc-400">
            {formatNumber(e.done)} / {formatNumber(e.total)} Nachrichten
          </span>
        </div>
      )}
    </li>
  );
}

/** Export list with start button, for the own account or (admin) any account. */
export function DataExportPanel({ accountId }: { accountId?: number }) {
  const qc = useQueryClient();
  const toast = useToast();
  const admin = accountId !== undefined;
  const key = admin ? atRestKeys.accountExports(accountId) : atRestKeys.exports;
  const q = useQuery({
    queryKey: key,
    queryFn: () => (admin ? atRestApi.accountExports(accountId) : atRestApi.exports()),
    refetchInterval: (query) => (query.state.data?.some((e) => e.status === "pending" || e.status === "running") ? 1500 : false),
  });
  const start = useMutation({
    mutationFn: () => (admin ? atRestApi.startAccountExport(accountId) : atRestApi.startExport()),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: key });
      toast.info("Export gestartet", "Die ZIP-Datei wird im Hintergrund erstellt. Du kannst die Seite verlassen.");
    },
    onError: (err) => toast.error(err, "Export nicht möglich"),
  });
  const list = q.data ?? [];
  const running = list.some((e) => e.status === "pending" || e.status === "running");
  const recent = !admin && list.some((e) => e.status !== "failed" && Date.now() - new Date(e.createdAt).getTime() < 3600_000);

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-3">
        <Button variant="primary" icon={PackageOpen} loading={start.isPending} disabled={running || recent} onClick={() => start.mutate()}>
          {admin ? "Daten dieses Kontos exportieren" : "Meine Daten exportieren"}
        </Button>
        {recent && !running && (
          <span className="text-xs text-zinc-500 dark:text-zinc-400">
            Ein neuer Export ist {formatRelative(new Date(new Date(list[0].createdAt).getTime() + 3600_000).toISOString())} wieder möglich (einmal pro Stunde).
          </span>
        )}
      </div>
      {q.isLoading ? (
        <Skeleton className="h-14 rounded-lg" />
      ) : q.error ? (
        <Notice tone="error" icon={CircleX}>
          Exporte konnten nicht geladen werden.
        </Notice>
      ) : list.length > 0 ? (
        <ul className="divide-y divide-zinc-100 dark:divide-zinc-800">
          {list.map((e) => (
            <ExportRow key={e.id} e={e} downloadUrl={admin ? atRestApi.adminExportDownloadUrl(e.id) : atRestApi.exportDownloadUrl(e.id)} />
          ))}
        </ul>
      ) : null}
    </div>
  );
}

/** Account settings: GDPR self-service export. */
export function DataExportCard() {
  return (
    <Card id="export">
      <CardHeader
        title="Meine Daten exportieren"
        description="Alle deine Daten als ZIP-Datei (DSGVO Art. 15/20): Mails als mbox pro Ordner, Kalender und Aufgaben als .ics, Kontodaten, App-Passwort-Namen und Sitzungen."
        icon={<PackageOpen className="h-4 w-4 text-zinc-400" />}
      />
      <CardBody className="space-y-4">
        <DataExportPanel />
        <p className="text-xs leading-relaxed text-zinc-500 dark:text-zinc-400">
          Der Export wird im Hintergrund erstellt und steht 24 Stunden zum Download bereit, danach wird er automatisch gelöscht. Passwörter und andere
          Geheimnisse sind nicht enthalten. Verschlüsselt gespeicherte Nachrichten werden so exportiert, wie sie gespeichert sind.
        </p>
      </CardBody>
    </Card>
  );
}
