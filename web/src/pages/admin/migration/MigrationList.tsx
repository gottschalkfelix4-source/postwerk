import { Link, useNavigate } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { ArrowRightLeft, FolderInput, KeyRound, Plus, RefreshCw, ShieldCheck } from "lucide-react";
import { adminApi } from "@/api/endpoints";
import { formatDateTime, formatNumber, formatRelative } from "@/lib/format";
import {
  Button,
  Card,
  EmptyState,
  ErrorState,
  PageHeader,
  Progress,
  SkeletonRows,
  TBody,
  THead,
  Table,
  Td,
  Th,
  Tr,
} from "@/components/ui";
import { isActive, jobProgress, MigrationStatusBadge, percent } from "./shared";

export default function MigrationListPage() {
  const navigate = useNavigate();
  const q = useQuery({
    queryKey: ["admin", "migrations"],
    queryFn: adminApi.migrations,
    refetchInterval: (query) => (query.state.data?.some((j) => isActive(j.status)) ? 2000 : false),
  });

  const jobs = q.data ?? [];
  const polling = jobs.some((j) => isActive(j.status));

  return (
    <>
      <PageHeader
        title="Migration"
        description="Postfächer per IMAP von einem anderen Mailserver übernehmen."
        actions={
          <>
            <Button icon={RefreshCw} onClick={() => void q.refetch()} loading={q.isFetching && !q.isLoading && !polling}>
              Aktualisieren
            </Button>
            <Button variant="primary" icon={Plus} onClick={() => navigate("/admin/migration/new")}>
              Neue Migration
            </Button>
          </>
        }
      />

      <Card className="overflow-hidden">
        {q.isLoading ? (
          <SkeletonRows rows={4} cols={5} />
        ) : q.error ? (
          <ErrorState error={q.error} onRetry={() => void q.refetch()} />
        ) : jobs.length === 0 ? (
          <div className="px-6 py-14">
            <EmptyState
              icon={ArrowRightLeft}
              title="Noch keine Migration"
              description="Ziehe Postfächer von Gmail, Outlook, GMX, mailcow oder einem anderen IMAP-Server um. Ordner, Gelesen-Status und Datum bleiben erhalten; bereits übernommene Nachrichten werden bei einem erneuten Lauf übersprungen."
              action={
                <Button variant="primary" icon={Plus} onClick={() => navigate("/admin/migration/new")}>
                  Migration starten
                </Button>
              }
              className="py-0"
            />
            <div className="mx-auto mt-10 grid max-w-3xl gap-4 sm:grid-cols-3">
              <Hint icon={KeyRound} title="Zugangsdaten bleiben flüchtig" text="Passwörter der Quelle werden nur im Arbeitsspeicher gehalten und nie gespeichert." />
              <Hint icon={FolderInput} title="Ordner werden zugeordnet" text="Gesendet, Entwürfe & Co. landen automatisch im passenden Ordner." />
              <Hint icon={ShieldCheck} title="Wiederaufnahme möglich" text="Nach einem Neustart lässt sich ein Job mit neu eingegebenen Passwörtern fortsetzen." />
            </div>
          </div>
        ) : (
          <Table>
            <THead>
              <tr>
                <Th>Name</Th>
                <Th>Quelle</Th>
                <Th>Status</Th>
                <Th className="w-56">Fortschritt</Th>
                <Th className="text-right">Postfächer</Th>
                <Th>Erstellt</Th>
              </tr>
            </THead>
            <TBody>
              {jobs.map((job) => {
                const p = jobProgress(job);
                const imported = job.accounts.reduce((n, a) => n + a.imported, 0);
                return (
                  <Tr key={job.id} onClick={() => navigate(`/admin/migration/${job.id}`)}>
                    <Td className="min-w-[12rem]">
                      <Link
                        to={`/admin/migration/${job.id}`}
                        onClick={(e) => e.stopPropagation()}
                        className="font-medium text-zinc-900 hover:text-indigo-600 dark:text-zinc-100 dark:hover:text-indigo-300"
                      >
                        {job.name || `Migration #${job.id}`}
                      </Link>
                    </Td>
                    <Td>
                      <code className="font-mono text-[13px] text-zinc-600 dark:text-zinc-400">
                        {job.source.host}:{job.source.port}
                      </code>
                    </Td>
                    <Td>
                      <MigrationStatusBadge status={job.status} />
                    </Td>
                    <Td>
                      <div className="flex items-center gap-3">
                        <Progress value={p} tone="indigo" className="flex-1" />
                        <span className="w-10 text-right text-xs tabular-nums text-zinc-500 dark:text-zinc-400">{percent(p)}</span>
                      </div>
                      <div className="mt-1 text-xs text-zinc-500 dark:text-zinc-400">{formatNumber(imported)} importiert</div>
                    </Td>
                    <Td className="text-right tabular-nums">{job.accounts.length}</Td>
                    <Td className="whitespace-nowrap text-zinc-500 dark:text-zinc-400" title={formatDateTime(job.createdAt)}>
                      {formatRelative(job.createdAt)}
                    </Td>
                  </Tr>
                );
              })}
            </TBody>
          </Table>
        )}
      </Card>
    </>
  );
}

function Hint({ icon: Icon, title, text }: { icon: typeof KeyRound; title: string; text: string }) {
  return (
    <div className="rounded-lg border border-zinc-200 p-4 text-left dark:border-zinc-800">
      <Icon className="mb-2 h-4 w-4 text-indigo-500" />
      <div className="text-sm font-medium text-zinc-900 dark:text-zinc-100">{title}</div>
      <p className="mt-0.5 text-xs leading-relaxed text-zinc-500 dark:text-zinc-400">{text}</p>
    </div>
  );
}
