import { useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronRight, Globe, Plus, Search, Trash2 } from "lucide-react";
import { adminApi } from "@/api/endpoints";
import { formatDate } from "@/lib/format";
import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  Field,
  Input,
  Modal,
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

const DOMAIN_RE = /^(?=.{1,253}$)(?!-)([a-z0-9-]{1,63}\.)+[a-z]{2,63}$/i;

export default function DomainsPage() {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const toast = useToast();
  const confirm = useConfirm();
  const [search, setSearch] = useState("");
  const [createOpen, setCreateOpen] = useState(false);

  const q = useQuery({ queryKey: ["admin", "domains"], queryFn: adminApi.domains });

  const remove = useMutation({
    mutationFn: adminApi.deleteDomain,
    onSuccess: (_, name) => {
      toast.success("Domain gelöscht", name);
      void qc.invalidateQueries({ queryKey: ["admin", "domains"] });
    },
    onError: (err) => toast.error(err, "Löschen fehlgeschlagen"),
  });

  const items = useMemo(() => {
    const s = search.trim().toLowerCase();
    return (q.data ?? []).filter((d) => !s || d.name.toLowerCase().includes(s));
  }, [q.data, search]);

  return (
    <>
      <PageHeader
        title="Domains"
        description="Domains, für die dieser Server E-Mails annimmt."
        actions={
          <Button variant="primary" icon={Plus} onClick={() => setCreateOpen(true)}>
            Domain hinzufügen
          </Button>
        }
      />
      <Card>
        <TableToolbar>
          <div className="w-full max-w-sm">
            <Input icon={Search} value={search} onChange={(e) => setSearch(e.target.value)} placeholder="Domain suchen …" />
          </div>
        </TableToolbar>
        {q.isLoading ? (
          <SkeletonRows rows={4} cols={4} />
        ) : q.error ? (
          <ErrorState error={q.error} onRetry={() => void q.refetch()} />
        ) : items.length === 0 ? (
          <EmptyState
            icon={Globe}
            title={search ? "Keine Treffer" : "Noch keine Domains"}
            description={search ? undefined : "Füge deine erste Domain hinzu. Postwerk erzeugt dabei automatisch einen DKIM-Schlüssel."}
            action={
              !search && (
                <Button variant="primary" icon={Plus} onClick={() => setCreateOpen(true)}>
                  Domain hinzufügen
                </Button>
              )
            }
          />
        ) : (
          <Table>
            <THead>
              <tr>
                <Th>Domain</Th>
                <Th>Konten</Th>
                <Th>Aliase</Th>
                <Th>Catch-all</Th>
                <Th>DKIM-Selektor</Th>
                <Th>Erstellt</Th>
                <Th className="w-20" />
              </tr>
            </THead>
            <TBody>
              {items.map((d) => (
                <Tr key={d.name} onClick={() => navigate(`/admin/domains/${encodeURIComponent(d.name)}`)}>
                  <Td>
                    <div className="flex items-center gap-3">
                      <span className="flex h-8 w-8 items-center justify-center rounded-lg bg-zinc-100 text-zinc-500 dark:bg-zinc-800 dark:text-zinc-400">
                        <Globe className="h-4 w-4" />
                      </span>
                      <span className="font-medium text-zinc-900 dark:text-zinc-100">{d.name}</span>
                    </div>
                  </Td>
                  <Td className="tabular-nums">{d.accountCount}</Td>
                  <Td className="tabular-nums">{d.aliasCount}</Td>
                  <Td>{d.catchAll ? <Badge tone="amber">{d.catchAll}</Badge> : <span className="text-zinc-400">—</span>}</Td>
                  <Td>
                    <code className="rounded bg-zinc-100 px-1.5 py-0.5 font-mono text-xs dark:bg-zinc-800">{d.dkimSelector}</code>
                  </Td>
                  <Td className="whitespace-nowrap text-zinc-500 dark:text-zinc-400">{formatDate(d.createdAt)}</Td>
                  <Td className="text-right" onClick={(e) => e.stopPropagation()}>
                    <div className="flex justify-end gap-1">
                      <Button
                        size="icon-sm"
                        variant="danger-ghost"
                        icon={Trash2}
                        aria-label={`${d.name} löschen`}
                        onClick={async () => {
                          const ok = await confirm({
                            title: `${d.name} löschen?`,
                            message:
                              d.accountCount > 0
                                ? `Zu dieser Domain gehören noch ${d.accountCount} Konten. Der Server lehnt das Löschen ggf. ab.`
                                : "Die Domain und ihr DKIM-Schlüssel werden entfernt.",
                            confirmLabel: "Löschen",
                            danger: true,
                          });
                          if (ok) remove.mutate(d.name);
                        }}
                      />
                      <Button
                        size="icon-sm"
                        variant="ghost"
                        icon={ChevronRight}
                        aria-label="Details"
                        onClick={() => navigate(`/admin/domains/${encodeURIComponent(d.name)}`)}
                      />
                    </div>
                  </Td>
                </Tr>
              ))}
            </TBody>
          </Table>
        )}
      </Card>
      <CreateDomainDialog
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        onCreated={(name) => {
          void qc.invalidateQueries({ queryKey: ["admin", "domains"] });
          navigate(`/admin/domains/${encodeURIComponent(name)}`);
        }}
      />
    </>
  );
}

function CreateDomainDialog({ open, onClose, onCreated }: { open: boolean; onClose: () => void; onCreated: (name: string) => void }) {
  const toast = useToast();
  const [name, setName] = useState("");
  const [touched, setTouched] = useState(false);
  const valid = DOMAIN_RE.test(name.trim());

  const create = useMutation({
    mutationFn: () => adminApi.createDomain(name.trim().toLowerCase()),
    onSuccess: (d) => {
      toast.success("Domain hinzugefügt", "Trage jetzt die empfohlenen DNS-Einträge ein.");
      setName("");
      setTouched(false);
      onClose();
      onCreated(d.name);
    },
    onError: (err) => toast.error(err, "Domain konnte nicht angelegt werden"),
  });

  const submit = () => {
    setTouched(true);
    if (valid) create.mutate();
  };

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="Domain hinzufügen"
      description="Ein DKIM-Schlüssel wird automatisch erzeugt."
      size="sm"
      footer={
        <>
          <Button onClick={onClose}>Abbrechen</Button>
          <Button variant="primary" loading={create.isPending} onClick={submit}>
            Hinzufügen
          </Button>
        </>
      }
    >
      <form
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        <Field label="Domainname" error={touched && !valid ? "Bitte einen gültigen Domainnamen eingeben (z. B. beispiel.de)." : null}>
          {(id) => (
            <Input id={id} value={name} onChange={(e) => setName(e.target.value)} placeholder="beispiel.de" invalid={touched && !valid} icon={Globe} />
          )}
        </Field>
      </form>
    </Modal>
  );
}
