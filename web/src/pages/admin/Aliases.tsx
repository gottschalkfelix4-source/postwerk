import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowRight, AtSign, Pencil, Plus, Search, Trash2 } from "lucide-react";
import { adminApi } from "@/api/endpoints";
import { mailAuthApi } from "@/api/mailauth";
import { PostmasterCard, SystemAliasRows } from "./SystemAliases";
import type { Alias } from "@/api/types";
import { formatDate } from "@/lib/format";
import { isEmail } from "@/lib/utils";
import {
  Button,
  Card,
  Checkbox,
  ChipInput,
  EmptyState,
  ErrorState,
  Field,
  Input,
  Modal,
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

export default function AliasesPage() {
  const qc = useQueryClient();
  const toast = useToast();
  const confirm = useConfirm();
  const [search, setSearch] = useState("");
  const [editing, setEditing] = useState<Alias | "new" | null>(null);
  const [selected, setSelected] = useState<Set<number>>(new Set());

  const q = useQuery({ queryKey: ["admin", "aliases"], queryFn: adminApi.aliases });
  const system = useQuery({ queryKey: ["admin", "aliases", "system"], queryFn: mailAuthApi.systemAliases });

  const remove = useMutation({
    mutationFn: async (ids: number[]) => {
      for (const id of ids) await adminApi.deleteAlias(id);
      return ids.length;
    },
    onSuccess: (n) => {
      toast.success(n === 1 ? "Alias gelöscht" : `${n} Aliase gelöscht`);
      setSelected(new Set());
    },
    onError: (err) => toast.error(err, "Löschen fehlgeschlagen"),
    onSettled: () => qc.invalidateQueries({ queryKey: ["admin", "aliases"] }),
  });

  const items = useMemo(() => {
    const s = search.trim().toLowerCase();
    return (q.data ?? []).filter(
      (a) => !s || a.address.toLowerCase().includes(s) || a.targets.some((t) => t.toLowerCase().includes(s)),
    );
  }, [q.data, search]);

  const systemItems = useMemo(() => {
    const s = search.trim().toLowerCase();
    return (system.data ?? []).filter((a) => !s || a.address.includes(s) || a.targets.some((t) => t.includes(s)));
  }, [system.data, search]);

  const askDelete = async (ids: number[]) => {
    const ok = await confirm({
      title: ids.length === 1 ? "Alias löschen?" : `${ids.length} Aliase löschen?`,
      message: "Nachrichten an diese Adressen werden danach abgelehnt.",
      confirmLabel: "Löschen",
      danger: true,
    });
    if (ok) remove.mutate(ids);
  };

  const allSelected = items.length > 0 && items.every((a) => selected.has(a.id));

  return (
    <>
      <PageHeader
        title="Aliase"
        description="Zusätzliche Adressen, die an ein oder mehrere Ziele weiterleiten."
        actions={
          <Button variant="primary" icon={Plus} onClick={() => setEditing("new")}>
            Alias anlegen
          </Button>
        }
      />
      <Card>
        <TableToolbar>
          <div className="w-full max-w-sm">
            <Input icon={Search} value={search} onChange={(e) => setSearch(e.target.value)} placeholder="Adresse oder Ziel suchen …" />
          </div>
        </TableToolbar>
        <SelectionBar count={selected.size} onClear={() => setSelected(new Set())}>
          <Button size="sm" variant="danger" icon={Trash2} loading={remove.isPending} onClick={() => void askDelete([...selected])}>
            Löschen
          </Button>
        </SelectionBar>
        {q.isLoading ? (
          <SkeletonRows rows={5} cols={3} />
        ) : q.error ? (
          <ErrorState error={q.error} onRetry={() => void q.refetch()} />
        ) : items.length === 0 && systemItems.length === 0 ? (
          <EmptyState
            icon={AtSign}
            title={search ? "Keine Treffer" : "Noch keine Aliase"}
            description={search ? undefined : "Mit Aliasen leitest du z. B. info@ oder rechnungen@ an bestehende Postfächer weiter."}
            action={
              !search && (
                <Button variant="primary" icon={Plus} onClick={() => setEditing("new")}>
                  Alias anlegen
                </Button>
              )
            }
          />
        ) : (
          <Table>
            <THead>
              <tr>
                <Th className="w-10 pr-0">
                  <Checkbox
                    checked={allSelected}
                    indeterminate={!allSelected && items.some((a) => selected.has(a.id))}
                    onChange={() => setSelected(allSelected ? new Set() : new Set(items.map((a) => a.id)))}
                    aria-label="Alle auswählen"
                  />
                </Th>
                <Th>Adresse</Th>
                <Th className="w-8" />
                <Th>Ziele</Th>
                <Th>Erstellt</Th>
                <Th className="w-24" />
              </tr>
            </THead>
            <TBody>
              {items.map((a) => (
                <Tr key={a.id} selected={selected.has(a.id)} onClick={() => setEditing(a)}>
                  <Td className="w-10 pr-0" onClick={(e) => e.stopPropagation()}>
                    <Checkbox
                      checked={selected.has(a.id)}
                      onChange={() =>
                        setSelected((s) => {
                          const n = new Set(s);
                          if (n.has(a.id)) n.delete(a.id);
                          else n.add(a.id);
                          return n;
                        })
                      }
                      aria-label={`${a.address} auswählen`}
                    />
                  </Td>
                  <Td className="font-medium text-zinc-900 dark:text-zinc-100">{a.address}</Td>
                  <Td className="px-0 text-zinc-300 dark:text-zinc-600">
                    <ArrowRight className="h-4 w-4" />
                  </Td>
                  <Td>
                    <div className="flex flex-wrap gap-1.5">
                      {a.targets.map((t) => (
                        <span key={t} className="rounded-md bg-zinc-100 px-2 py-0.5 text-xs text-zinc-700 ring-1 ring-inset ring-zinc-200 dark:bg-zinc-800 dark:text-zinc-300 dark:ring-zinc-700">
                          {t}
                        </span>
                      ))}
                    </div>
                  </Td>
                  <Td className="whitespace-nowrap text-zinc-500 dark:text-zinc-400">{formatDate(a.createdAt)}</Td>
                  <Td className="text-right" onClick={(e) => e.stopPropagation()}>
                    <div className="flex justify-end gap-1">
                      <Button size="icon-sm" variant="ghost" icon={Pencil} onClick={() => setEditing(a)} aria-label="Bearbeiten" />
                      <Button size="icon-sm" variant="danger-ghost" icon={Trash2} onClick={() => void askDelete([a.id])} aria-label="Löschen" />
                    </div>
                  </Td>
                </Tr>
              ))}
              <SystemAliasRows items={systemItems} />
            </TBody>
          </Table>
        )}
      </Card>
      <PostmasterCard />
      <AliasDialog
        alias={editing}
        onClose={() => setEditing(null)}
        onSaved={() => void qc.invalidateQueries({ queryKey: ["admin", "aliases"] })}
      />
    </>
  );
}

function AliasDialog({ alias, onClose, onSaved }: { alias: Alias | "new" | null; onClose: () => void; onSaved: () => void }) {
  const toast = useToast();
  const isNew = alias === "new";
  const [address, setAddress] = useState("");
  const [targets, setTargets] = useState<string[]>([]);
  const [touched, setTouched] = useState(false);

  useEffect(() => {
    if (alias === null) return;
    setAddress(alias === "new" ? "" : alias.address);
    setTargets(alias === "new" ? [] : alias.targets);
    setTouched(false);
  }, [alias]);

  const save = useMutation({
    mutationFn: () =>
      isNew || alias === null
        ? adminApi.createAlias(address.trim().toLowerCase(), targets)
        : adminApi.updateAlias(alias.id, targets),
    onSuccess: () => {
      toast.success(isNew ? "Alias angelegt" : "Alias gespeichert");
      onSaved();
      onClose();
    },
    onError: (err) => toast.error(err, "Speichern fehlgeschlagen"),
  });

  const addressInvalid = isNew && !isEmail(address);
  const targetsInvalid = targets.length === 0;

  const submit = () => {
    setTouched(true);
    if (!addressInvalid && !targetsInvalid) save.mutate();
  };

  return (
    <Modal
      open={alias !== null}
      onClose={onClose}
      title={isNew ? "Neuer Alias" : "Alias bearbeiten"}
      description="Eingehende Nachrichten an die Adresse werden an alle Ziele zugestellt."
      dismissible={false}
      footer={
        <>
          <Button onClick={onClose}>Abbrechen</Button>
          <Button variant="primary" loading={save.isPending} onClick={submit}>
            {isNew ? "Anlegen" : "Speichern"}
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <Field
          label="Alias-Adresse"
          error={touched && addressInvalid ? "Bitte eine gültige E-Mail-Adresse eingeben." : null}
          help={!isNew ? "Die Adresse kann nachträglich nicht geändert werden." : "Die Domain muss auf diesem Server angelegt sein."}
        >
          {(id) => (
            <Input
              id={id}
              icon={AtSign}
              value={address}
              disabled={!isNew}
              invalid={touched && addressInvalid}
              onChange={(e) => setAddress(e.target.value)}
              placeholder="info@beispiel.de"
            />
          )}
        </Field>
        <Field
          label="Ziele"
          error={touched && targetsInvalid ? "Mindestens ein Ziel angeben." : null}
          help="Mehrere Adressen mit Enter oder Komma trennen. Externe Adressen sind möglich."
        >
          {(id) => (
            <ChipInput
              id={id}
              value={targets}
              onChange={setTargets}
              placeholder="ziel@beispiel.de"
              transform={(v) => v.toLowerCase()}
              validate={(v) => (isEmail(v) ? null : `„${v}“ ist keine gültige E-Mail-Adresse.`)}
            />
          )}
        </Field>
      </div>
    </Modal>
  );
}
