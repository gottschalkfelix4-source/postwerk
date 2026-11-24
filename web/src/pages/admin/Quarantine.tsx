import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Inbox, RefreshCw, Search, ShieldCheck, Trash2, TriangleAlert } from "lucide-react";
import { adminApi, type QuarantineItem } from "@/api/endpoints";
import { formatDateTime } from "@/lib/format";
import {
  Badge,
  Button,
  Card,
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
  useConfirm,
  useToast,
} from "@/components/ui";

/** Admin overview of all messages sitting in the junk folders (quarantine). */
export default function QuarantinePage() {
  const qc = useQueryClient();
  const toast = useToast();
  const confirm = useConfirm();
  const [search, setSearch] = useState("");

  const q = useQuery({ queryKey: ["admin", "quarantine"], queryFn: adminApi.quarantine, refetchInterval: 60_000 });
  const invalidate = () => qc.invalidateQueries({ queryKey: ["admin", "quarantine"] });

  const release = useMutation({
    mutationFn: async (item: QuarantineItem) => {
      await adminApi.quarantineRelease(item.id);
      return item;
    },
    onSuccess: (item) => toast.success("Freigegeben", `„${item.subject || "(ohne Betreff)"}“ liegt wieder im Posteingang von ${item.account}.`),
    onError: (err) => toast.error(err, "Freigeben fehlgeschlagen"),
    onSettled: invalidate,
  });

  const remove = useMutation({
    mutationFn: async (item: QuarantineItem) => {
      await adminApi.quarantineDelete(item.id);
      return item;
    },
    onSuccess: (item) => toast.success("Als Spam bestätigt", `„${item.subject || "(ohne Betreff)"}“ wurde gelöscht und trainiert den Spamfilter.`),
    onError: (err) => toast.error(err, "Löschen fehlgeschlagen"),
    onSettled: invalidate,
  });

  const items = useMemo(() => {
    const s = search.trim().toLowerCase();
    return (q.data?.items ?? []).filter(
      (i) => !s || i.subject.toLowerCase().includes(s) || i.from.email.toLowerCase().includes(s) || i.account.toLowerCase().includes(s),
    );
  }, [q.data, search]);

  const askDelete = async (item: QuarantineItem) => {
    const ok = await confirm({
      title: "Als Spam bestätigen und löschen?",
      message: `„${item.subject || "(ohne Betreff)"}“ von ${item.from.email} wird gelöscht. Damit trainiert Postwerk den Spamfilter mit dieser Nachricht.`,
      confirmLabel: "Löschen",
      danger: true,
    });
    if (ok) remove.mutate(item);
  };

  return (
    <>
      <PageHeader
        title="Quarantäne"
        description="Nachrichten aller Konten, die als Spam einsortiert wurden – freigeben (nicht Spam) oder als Spam bestätigen und löschen."
        actions={
          <Button icon={RefreshCw} onClick={() => void q.refetch()} loading={q.isFetching && !q.isLoading}>
            Aktualisieren
          </Button>
        }
      />
      <Card>
        <TableToolbar>
          <div className="w-full max-w-sm">
            <Input icon={Search} value={search} onChange={(e) => setSearch(e.target.value)} placeholder="Betreff, Absender oder Konto …" />
          </div>
          {q.data && (
            <div className="ml-auto flex gap-2">
              <Badge tone="neutral">{q.data.items.length} in Quarantäne</Badge>
            </div>
          )}
        </TableToolbar>
        {q.isLoading ? (
          <SkeletonRows cols={5} rows={6} />
        ) : q.isError ? (
          <ErrorState error={q.error} onRetry={() => void q.refetch()} />
        ) : items.length === 0 ? (
          <EmptyState
            icon={Inbox}
            title={search ? "Keine Treffer" : "Nichts in Quarantäne"}
            description="Hier liegen Nachrichten, die der Spamfilter aussortiert hat."
          />
        ) : (
          <Table>
            <THead>
              <Tr>
                <Th>Betreff</Th>
                <Th>Absender</Th>
                <Th>Konto</Th>
                <Th>Empfangen</Th>
                <Th className="text-right">Aktion</Th>
              </Tr>
            </THead>
            <TBody>
              {items.map((i) => (
                <Tr key={i.id}>
                  <Td className="max-w-[24rem] truncate font-medium text-zinc-900 dark:text-zinc-100">{i.subject || "(ohne Betreff)"}</Td>
                  <Td className="max-w-[16rem] truncate">{i.from.name ? `${i.from.name} <${i.from.email}>` : i.from.email}</Td>
                  <Td>{i.account}</Td>
                  <Td className="whitespace-nowrap">{formatDateTime(i.receivedAt)}</Td>
                  <Td className="text-right">
                    <div className="flex justify-end gap-1.5">
                      <Button
                        size="sm"
                        variant="ghost"
                        icon={ShieldCheck}
                        title="In den Posteingang verschieben und als „kein Spam“ trainieren"
                        loading={release.isPending && release.variables?.id === i.id}
                        onClick={() => release.mutate(i)}
                      >
                        Freigeben
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        icon={Trash2}
                        title="Als Spam bestätigen und löschen"
                        loading={remove.isPending && remove.variables?.id === i.id}
                        onClick={() => void askDelete(i)}
                        className="text-red-600 hover:text-red-700 dark:text-red-400 dark:hover:text-red-300"
                      >
                        Löschen
                      </Button>
                    </div>
                  </Td>
                </Tr>
              ))}
            </TBody>
          </Table>
        )}
        {q.data && q.data.items.length > 0 && items.length === 0 && !search && (
          <div className="flex items-center gap-2 px-5 py-3 text-xs text-zinc-500 dark:text-zinc-400">
            <TriangleAlert className="h-3.5 w-3.5" /> Die Liste ist auf {q.data.limit} Einträge begrenzt.
          </div>
        )}
      </Card>
    </>
  );
}

