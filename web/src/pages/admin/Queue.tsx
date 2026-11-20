import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CircleCheck, RefreshCw, RotateCw, Search, Trash2, TriangleAlert } from "lucide-react";
import { adminApi } from "@/api/endpoints";
import type { QueueItem } from "@/api/types";
import { formatBytes, formatDateTime, formatRelative } from "@/lib/format";
import {
  Badge,
  Button,
  Card,
  Checkbox,
  EmptyState,
  ErrorState,
  Input,
  PageHeader,
  SelectionBar,
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

export default function QueuePage() {
  const qc = useQueryClient();
  const toast = useToast();
  const confirm = useConfirm();
  const [search, setSearch] = useState("");
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [expanded, setExpanded] = useState<number | null>(null);

  const q = useQuery({ queryKey: ["admin", "queue"], queryFn: adminApi.queue, refetchInterval: 15_000 });
  const invalidate = () => qc.invalidateQueries({ queryKey: ["admin", "queue"] });

  const retry = useMutation({
    mutationFn: async (ids: number[]) => {
      for (const id of ids) await adminApi.retryQueueItem(id);
      return ids.length;
    },
    onSuccess: (n) => {
      toast.success(n === 1 ? "Zustellung wird erneut versucht" : `${n} Nachrichten werden erneut zugestellt`);
      setSelected(new Set());
    },
    onError: (err) => toast.error(err, "Erneuter Versuch fehlgeschlagen"),
    onSettled: invalidate,
  });

  const remove = useMutation({
    mutationFn: async (ids: number[]) => {
      for (const id of ids) await adminApi.deleteQueueItem(id);
      return ids.length;
    },
    onSuccess: (n) => {
      toast.success(n === 1 ? "Nachricht aus der Warteschlange entfernt" : `${n} Nachrichten entfernt`);
      setSelected(new Set());
    },
    onError: (err) => toast.error(err, "Entfernen fehlgeschlagen"),
    onSettled: invalidate,
  });

  const items = useMemo(() => {
    const s = search.trim().toLowerCase();
    return (q.data ?? []).filter(
      (i) => !s || i.from.toLowerCase().includes(s) || i.to.some((t) => t.toLowerCase().includes(s)) || (i.lastError ?? "").toLowerCase().includes(s),
    );
  }, [q.data, search]);

  const askDelete = async (ids: number[]) => {
    const ok = await confirm({
      title: ids.length === 1 ? "Nachricht verwerfen?" : `${ids.length} Nachrichten verwerfen?`,
      message: "Die Nachrichten werden nicht zugestellt und ohne Unzustellbarkeitsbericht entfernt.",
      confirmLabel: "Verwerfen",
      danger: true,
    });
    if (ok) remove.mutate(ids);
  };

  const allSelected = items.length > 0 && items.every((i) => selected.has(i.id));
  const failing = (q.data ?? []).filter((i) => i.lastError).length;

  return (
    <>
      <PageHeader
        title="Warteschlange"
        description="Ausgehende Nachrichten, die noch zugestellt werden."
        actions={
          <Button icon={RefreshCw} onClick={() => void q.refetch()} loading={q.isFetching && !q.isLoading}>
            Aktualisieren
          </Button>
        }
      />
      <Card>
        <TableToolbar>
          <div className="w-full max-w-sm">
            <Input icon={Search} value={search} onChange={(e) => setSearch(e.target.value)} placeholder="Absender, Empfänger oder Fehler …" />
          </div>
          {q.data && (
            <div className="ml-auto flex gap-2">
              <Badge tone="neutral">{q.data.length} in Warteschlange</Badge>
              {failing > 0 && (
                <Badge tone="amber" icon={TriangleAlert}>
                  {failing} mit Fehler
                </Badge>
              )}
            </div>
          )}
        </TableToolbar>
        <SelectionBar count={selected.size} onClear={() => setSelected(new Set())}>
          <Button size="sm" icon={RotateCw} loading={retry.isPending} onClick={() => retry.mutate([...selected])}>
            Erneut versuchen
          </Button>
          <Button size="sm" variant="danger" icon={Trash2} disabled={remove.isPending} onClick={() => void askDelete([...selected])}>
            Verwerfen
          </Button>
        </SelectionBar>
        {q.isLoading ? (
          <SkeletonRows rows={5} cols={5} />
        ) : q.error ? (
          <ErrorState error={q.error} onRetry={() => void q.refetch()} />
        ) : items.length === 0 ? (
          <EmptyState
            icon={CircleCheck}
            title={search ? "Keine Treffer" : "Warteschlange ist leer"}
            description={search ? undefined : "Alle ausgehenden Nachrichten wurden zugestellt."}
          />
        ) : (
          <Table>
            <THead>
              <tr>
                <Th className="w-10 pr-0">
                  <Checkbox
                    checked={allSelected}
                    indeterminate={!allSelected && items.some((i) => selected.has(i.id))}
                    onChange={() => setSelected(allSelected ? new Set() : new Set(items.map((i) => i.id)))}
                    aria-label="Alle auswählen"
                  />
                </Th>
                <Th>Absender</Th>
                <Th>Empfänger</Th>
                <Th>Größe</Th>
                <Th>Versuche</Th>
                <Th>Nächster Versuch</Th>
                <Th>Letzter Fehler</Th>
                <Th className="w-24" />
              </tr>
            </THead>
            <TBody>
              {items.map((i) => (
                <QueueRow
                  key={i.id}
                  item={i}
                  selected={selected.has(i.id)}
                  expanded={expanded === i.id}
                  onExpand={() => setExpanded(expanded === i.id ? null : i.id)}
                  onToggle={() =>
                    setSelected((s) => {
                      const n = new Set(s);
                      if (n.has(i.id)) n.delete(i.id);
                      else n.add(i.id);
                      return n;
                    })
                  }
                  onRetry={() => retry.mutate([i.id])}
                  onDelete={() => void askDelete([i.id])}
                />
              ))}
            </TBody>
          </Table>
        )}
      </Card>
    </>
  );
}

function QueueRow({
  item: i,
  selected,
  expanded,
  onExpand,
  onToggle,
  onRetry,
  onDelete,
}: {
  item: QueueItem;
  selected: boolean;
  expanded: boolean;
  onExpand: () => void;
  onToggle: () => void;
  onRetry: () => void;
  onDelete: () => void;
}) {
  return (
    <>
      <Tr selected={selected} onClick={onExpand}>
        <Td className="w-10 pr-0" onClick={(e) => e.stopPropagation()}>
          <Checkbox checked={selected} onChange={onToggle} aria-label="Auswählen" />
        </Td>
        <Td className="max-w-[14rem] truncate font-medium text-zinc-900 dark:text-zinc-100">{i.from || "<>"}</Td>
        <Td className="max-w-[16rem]">
          <span className="truncate">{i.to[0]}</span>
          {i.to.length > 1 && <span className="ml-1.5 text-xs text-zinc-400">+{i.to.length - 1}</span>}
        </Td>
        <Td className="whitespace-nowrap tabular-nums">{formatBytes(i.size)}</Td>
        <Td>
          <Badge tone={i.attempts >= 5 ? "red" : i.attempts >= 2 ? "amber" : "neutral"}>{i.attempts}</Badge>
        </Td>
        <Td className="whitespace-nowrap text-zinc-500 dark:text-zinc-400" title={formatDateTime(i.nextAttempt)}>
          {formatRelative(i.nextAttempt)}
        </Td>
        <Td className="max-w-[22rem]">
          {i.lastError ? (
            <span className="line-clamp-1 font-mono text-xs text-red-600 dark:text-red-400" title={i.lastError}>
              {i.lastError}
            </span>
          ) : (
            <span className="text-zinc-400">—</span>
          )}
        </Td>
        <Td className="text-right" onClick={(e) => e.stopPropagation()}>
          <div className="flex justify-end gap-1">
            <Button size="icon-sm" variant="ghost" icon={RotateCw} onClick={onRetry} aria-label="Erneut versuchen" title="Erneut versuchen" />
            <Button size="icon-sm" variant="danger-ghost" icon={Trash2} onClick={onDelete} aria-label="Verwerfen" title="Verwerfen" />
          </div>
        </Td>
      </Tr>
      {expanded && (
        <tr>
          <td colSpan={8} className="border-b border-zinc-100 bg-zinc-50/70 px-6 py-4 text-sm dark:border-zinc-800 dark:bg-zinc-950/40">
            <dl className="grid gap-x-8 gap-y-3 sm:grid-cols-2 lg:grid-cols-4">
              <div>
                <dt className="text-xs text-zinc-500">ID</dt>
                <dd className="font-mono">{i.id}</dd>
              </div>
              <div>
                <dt className="text-xs text-zinc-500">Eingereiht</dt>
                <dd>{formatDateTime(i.createdAt)}</dd>
              </div>
              <div>
                <dt className="text-xs text-zinc-500">Nächster Versuch</dt>
                <dd>{formatDateTime(i.nextAttempt)}</dd>
              </div>
              <div>
                <dt className="text-xs text-zinc-500">Empfänger</dt>
                <dd className="break-all">{i.to.join(", ")}</dd>
              </div>
            </dl>
            {i.lastError && (
              <pre className="mt-4 whitespace-pre-wrap break-all rounded-lg bg-red-50 p-3 font-mono text-xs text-red-700 ring-1 ring-inset ring-red-200 dark:bg-red-500/10 dark:text-red-300 dark:ring-red-500/20">
                {i.lastError}
              </pre>
            )}
          </td>
        </tr>
      )}
    </>
  );
}
