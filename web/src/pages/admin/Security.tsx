import { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { LockOpen, Search, Settings, ShieldCheck } from "lucide-react";
import { adminApi } from "@/api/endpoints";
import { formatDateTime, formatRelative } from "@/lib/format";
import {
  Badge,
  Button,
  Card,
  CardBody,
  CardHeader,
  EmptyState,
  ErrorState,
  Input,
  PageHeader,
  SkeletonRows,
  TBody,
  THead,
  Table,
  TableToolbar,
  Td,
  Th,
  Tr,
  useToast,
} from "@/components/ui";

export default function SecurityPage() {
  const qc = useQueryClient();
  const toast = useToast();
  const [search, setSearch] = useState("");
  const bans = useQuery({ queryKey: ["admin", "bans"], queryFn: adminApi.bans, refetchInterval: 30_000 });
  const settings = useQuery({ queryKey: ["admin", "settings"], queryFn: adminApi.settings, staleTime: 60_000 });

  const unban = useMutation({
    mutationFn: adminApi.unban,
    onSuccess: (_, ip) => {
      toast.success("IP entsperrt", ip);
      void qc.invalidateQueries({ queryKey: ["admin", "bans"] });
    },
    onError: (err) => toast.error(err, "Entsperren fehlgeschlagen"),
  });

  const items = useMemo(() => {
    const s = search.trim().toLowerCase();
    return (bans.data ?? []).filter((b) => !s || b.ip.includes(s) || b.reason.toLowerCase().includes(s));
  }, [bans.data, search]);

  const bf = settings.data?.security.bruteForce;

  return (
    <>
      <PageHeader title="Sicherheit" description="Automatisch gesperrte IP-Adressen nach fehlgeschlagenen Anmeldungen." />

      <div className="grid gap-6 xl:grid-cols-3">
        <Card className="xl:col-span-2">
          <CardHeader title="Gesperrte IP-Adressen" description="Sperren laufen automatisch ab; du kannst sie hier auch vorzeitig aufheben." />
          <TableToolbar>
            <div className="w-full max-w-xs">
              <Input icon={Search} value={search} onChange={(e) => setSearch(e.target.value)} placeholder="IP oder Grund …" />
            </div>
            {bans.data && <Badge tone={bans.data.length ? "amber" : "green"} className="ml-auto">{bans.data.length} aktiv</Badge>}
          </TableToolbar>
          {bans.isLoading ? (
            <SkeletonRows rows={4} cols={4} />
          ) : bans.error ? (
            <ErrorState error={bans.error} onRetry={() => void bans.refetch()} />
          ) : items.length === 0 ? (
            <EmptyState icon={ShieldCheck} title={search ? "Keine Treffer" : "Keine gesperrten Adressen"} description={search ? undefined : "Derzeit ist keine IP-Adresse gesperrt."} />
          ) : (
            <Table>
              <THead>
                <tr>
                  <Th>IP-Adresse</Th>
                  <Th>Grund</Th>
                  <Th>Fehlversuche</Th>
                  <Th>Gesperrt bis</Th>
                  <Th className="w-32" />
                </tr>
              </THead>
              <TBody>
                {items.map((b) => (
                  <Tr key={b.ip}>
                    <Td className="font-mono text-[13px] font-medium text-zinc-900 dark:text-zinc-100">{b.ip}</Td>
                    <Td>{b.reason}</Td>
                    <Td>
                      <Badge tone="red">{b.failures}</Badge>
                    </Td>
                    <Td className="whitespace-nowrap" title={formatDateTime(b.until)}>
                      <span className="text-zinc-700 dark:text-zinc-300">{formatRelative(b.until)}</span>
                    </Td>
                    <Td className="text-right">
                      <Button
                        size="sm"
                        icon={LockOpen}
                        loading={unban.isPending && unban.variables === b.ip}
                        onClick={() => unban.mutate(b.ip)}
                      >
                        Entsperren
                      </Button>
                    </Td>
                  </Tr>
                ))}
              </TBody>
            </Table>
          )}
        </Card>

        <Card className="h-fit">
          <CardHeader title="Brute-Force-Schutz" />
          <CardBody>
            {bf ? (
              <dl className="space-y-3 text-sm">
                <div className="flex justify-between gap-4">
                  <dt className="text-zinc-500 dark:text-zinc-400">Max. Fehlversuche</dt>
                  <dd className="font-medium tabular-nums text-zinc-900 dark:text-zinc-100">{bf.maxAttempts}</dd>
                </div>
                <div className="flex justify-between gap-4">
                  <dt className="text-zinc-500 dark:text-zinc-400">Zeitfenster</dt>
                  <dd className="font-medium tabular-nums text-zinc-900 dark:text-zinc-100">{bf.windowSeconds} s</dd>
                </div>
                <div className="flex justify-between gap-4">
                  <dt className="text-zinc-500 dark:text-zinc-400">Sperrdauer</dt>
                  <dd className="font-medium tabular-nums text-zinc-900 dark:text-zinc-100">{bf.banSeconds} s</dd>
                </div>
              </dl>
            ) : (
              <p className="text-sm text-zinc-500">Einstellungen werden geladen …</p>
            )}
            <Link to="/admin/settings/security" className="mt-5 block">
              <span className="inline-flex items-center gap-1.5 text-sm font-medium text-indigo-600 hover:text-indigo-500 dark:text-indigo-400">
                <Settings className="h-4 w-4" /> Einstellungen anpassen
              </span>
            </Link>
          </CardBody>
        </Card>
      </div>
    </>
  );
}
