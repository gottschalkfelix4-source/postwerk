import { useEffect, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Ellipsis, KeyRound, Pencil, Plus, Search, ShieldCheck, Trash2, UserCheck, UserPlus, UserX, Users, Wand2 } from "lucide-react";
import { adminApi } from "@/api/endpoints";
import type { Account, AccountSource, CreateAccountRequest, Role } from "@/api/types";
import { SourceBadge } from "./sso/SourceBadge";
import { useDebounced } from "@/lib/hooks";
import { formatDate } from "@/lib/format";
import { isEmail } from "@/lib/utils";
import {
  Avatar,
  Badge,
  Button,
  Card,
  Checkbox,
  CopyButton,
  Dropdown,
  DropdownItem,
  DropdownSeparator,
  EmptyState,
  ErrorState,
  Field,
  Input,
  Modal,
  PageHeader,
  Pagination,
  PasswordInput,
  Progress,
  Select,
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
import { generatePassword, QuotaInput, quotaLabel } from "./shared";

const LIMIT = 25;

export default function AccountsPage() {
  const [params, setParams] = useSearchParams();
  const [search, setSearch] = useState(params.get("q") ?? "");
  const q = useDebounced(search.trim(), 300);
  const [offset, setOffset] = useState(0);
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [createOpen, setCreateOpen] = useState(false);
  const navigate = useNavigate();
  const qc = useQueryClient();
  const toast = useToast();
  const confirm = useConfirm();

  useEffect(() => {
    setOffset(0);
    setSelected(new Set());
    setParams(q ? { q } : {}, { replace: true });
  }, [q, setParams]);

  useEffect(() => {
    const external = params.get("q") ?? "";
    if (external !== q) setSearch(external);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [params]);

  const list = useQuery({
    queryKey: ["admin", "accounts", q, offset],
    queryFn: () => adminApi.accounts({ q: q || undefined, limit: LIMIT, offset }),
    placeholderData: keepPreviousData,
  });

  const invalidate = () => qc.invalidateQueries({ queryKey: ["admin", "accounts"] });

  const bulk = useMutation({
    mutationFn: async ({ ids, action }: { ids: number[]; action: "disable" | "enable" | "delete" }) => {
      for (const id of ids) {
        if (action === "delete") await adminApi.deleteAccount(id);
        else await adminApi.updateAccount(id, { disabled: action === "disable" });
      }
      return ids.length;
    },
    onSuccess: (count, { action }) => {
      toast.success(
        action === "delete" ? `${count} Konto/Konten gelöscht` : action === "disable" ? `${count} Konto/Konten deaktiviert` : `${count} Konto/Konten aktiviert`,
      );
      setSelected(new Set());
    },
    onError: (err) => toast.error(err, "Aktion fehlgeschlagen"),
    onSettled: invalidate,
  });

  const items = list.data?.items ?? [];
  const allSelected = items.length > 0 && items.every((a) => selected.has(a.id));
  const someSelected = items.some((a) => selected.has(a.id));

  const toggle = (id: number) =>
    setSelected((s) => {
      const n = new Set(s);
      if (n.has(id)) n.delete(id);
      else n.add(id);
      return n;
    });

  const askDelete = async (ids: number[]) => {
    const ok = await confirm({
      title: ids.length === 1 ? "Konto löschen?" : `${ids.length} Konten löschen?`,
      message: "Alle Nachrichten, Kalender und Aufgaben der Konten werden dauerhaft entfernt. Das kann nicht rückgängig gemacht werden.",
      confirmLabel: "Endgültig löschen",
      danger: true,
    });
    if (ok) bulk.mutate({ ids, action: "delete" });
  };

  return (
    <>
      <PageHeader
        title="Konten"
        description="Postfächer und Benutzer dieses Servers verwalten."
        actions={
          <Button variant="primary" icon={UserPlus} onClick={() => setCreateOpen(true)}>
            Konto anlegen
          </Button>
        }
      />

      <Card>
        <TableToolbar>
          <div className="w-full max-w-sm">
            <Input icon={Search} value={search} onChange={(e) => setSearch(e.target.value)} placeholder="Name oder E-Mail suchen …" />
          </div>
          {list.data && (
            <span className="ml-auto text-sm text-zinc-500 dark:text-zinc-400">{list.data.total} Konten</span>
          )}
        </TableToolbar>
        <SelectionBar count={selected.size} onClear={() => setSelected(new Set())}>
          <Button size="sm" icon={UserX} onClick={() => bulk.mutate({ ids: [...selected], action: "disable" })} loading={bulk.isPending}>
            Deaktivieren
          </Button>
          <Button size="sm" icon={UserCheck} onClick={() => bulk.mutate({ ids: [...selected], action: "enable" })} disabled={bulk.isPending}>
            Aktivieren
          </Button>
          <Button size="sm" variant="danger" icon={Trash2} onClick={() => void askDelete([...selected])} disabled={bulk.isPending}>
            Löschen
          </Button>
        </SelectionBar>

        {list.isLoading ? (
          <SkeletonRows rows={6} cols={4} />
        ) : list.error ? (
          <ErrorState error={list.error} onRetry={() => void list.refetch()} />
        ) : items.length === 0 ? (
          <EmptyState
            icon={Users}
            title={q ? "Keine Treffer" : "Noch keine Konten"}
            description={q ? `Kein Konto passt zu „${q}“.` : "Lege das erste Postfach an, um E-Mails zu empfangen."}
            action={
              !q && (
                <Button variant="primary" icon={Plus} onClick={() => setCreateOpen(true)}>
                  Konto anlegen
                </Button>
              )
            }
          />
        ) : (
          <>
            <Table>
              <THead>
                <tr>
                  <Th className="w-10 pr-0">
                    <Checkbox
                      checked={allSelected}
                      indeterminate={!allSelected && someSelected}
                      onChange={() =>
                        setSelected((s) => {
                          const n = new Set(s);
                          if (allSelected) items.forEach((a) => n.delete(a.id));
                          else items.forEach((a) => n.add(a.id));
                          return n;
                        })
                      }
                      aria-label="Alle auswählen"
                    />
                  </Th>
                  <Th>Konto</Th>
                  <Th>Rolle</Th>
                  <Th className="min-w-[12rem]">Speicher</Th>
                  <Th>Status</Th>
                  <Th>Erstellt</Th>
                  <Th className="w-12" />
                </tr>
              </THead>
              <TBody>
                {items.map((a) => (
                  <AccountRow
                    key={a.id}
                    account={a}
                    selected={selected.has(a.id)}
                    onToggle={() => toggle(a.id)}
                    onOpen={() => navigate(`/admin/accounts/${a.id}`)}
                    onDelete={() => void askDelete([a.id])}
                    onToggleDisabled={() => bulk.mutate({ ids: [a.id], action: a.disabled ? "enable" : "disable" })}
                  />
                ))}
              </TBody>
            </Table>
            <Pagination total={list.data?.total ?? 0} limit={LIMIT} offset={offset} onChange={setOffset} className="border-t border-zinc-100 dark:border-zinc-800" />
          </>
        )}
      </Card>

      <CreateAccountDialog
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        onCreated={(a) => {
          void invalidate();
          navigate(`/admin/accounts/${a.id}`);
        }}
      />
    </>
  );
}

function AccountRow({
  account: a,
  selected,
  onToggle,
  onOpen,
  onDelete,
  onToggleDisabled,
}: {
  account: Account;
  selected: boolean;
  onToggle: () => void;
  onOpen: () => void;
  onDelete: () => void;
  onToggleDisabled: () => void;
}) {
  return (
    <Tr selected={selected} onClick={onOpen}>
      <Td className="w-10 pr-0" onClick={(e) => e.stopPropagation()}>
        <Checkbox checked={selected} onChange={onToggle} aria-label={`${a.email} auswählen`} />
      </Td>
      <Td>
        <div className="flex items-center gap-3">
          <Avatar name={a.name} email={a.email} size="sm" />
          <div className="min-w-0">
            <div className="truncate font-medium text-zinc-900 dark:text-zinc-100">{a.name || "—"}</div>
            <div className="truncate text-xs text-zinc-500 dark:text-zinc-400">{a.email}</div>
          </div>
        </div>
      </Td>
      <Td>
        <Badge tone={a.role === "admin" ? "indigo" : "neutral"}>{a.role === "admin" ? "Administrator" : "Benutzer"}</Badge>
      </Td>
      <Td>
        <div className="space-y-1.5">
          <Progress value={a.quotaBytes > 0 ? a.usedBytes / a.quotaBytes : 0} />
          <div className="text-xs text-zinc-500 dark:text-zinc-400">{quotaLabel(a.usedBytes, a.quotaBytes)}</div>
        </div>
      </Td>
      <Td>
        <div className="flex flex-wrap gap-1.5">
          {a.disabled ? <Badge tone="red" dot>Deaktiviert</Badge> : <Badge tone="green" dot>Aktiv</Badge>}
          <SourceBadge source={a.source} />
          {a.totpEnabled && (
            <Badge tone="indigo" icon={ShieldCheck}>
              2FA
            </Badge>
          )}
        </div>
      </Td>
      <Td className="whitespace-nowrap text-zinc-500 dark:text-zinc-400">{formatDate(a.createdAt)}</Td>
      <Td className="w-12 text-right" onClick={(e) => e.stopPropagation()}>
        <Dropdown
          align="right"
          trigger={({ toggle }) => <Button size="icon-sm" variant="ghost" icon={Ellipsis} onClick={toggle} aria-label="Aktionen" />}
        >
          {(close) => (
            <>
              <DropdownItem icon={Pencil} onClick={() => { close(); onOpen(); }}>
                Bearbeiten
              </DropdownItem>
              <DropdownItem icon={a.disabled ? UserCheck : UserX} onClick={() => { close(); onToggleDisabled(); }}>
                {a.disabled ? "Aktivieren" : "Deaktivieren"}
              </DropdownItem>
              <DropdownSeparator />
              <DropdownItem icon={Trash2} danger onClick={() => { close(); onDelete(); }}>
                Löschen
              </DropdownItem>
            </>
          )}
        </Dropdown>
      </Td>
    </Tr>
  );
}

function CreateAccountDialog({ open, onClose, onCreated }: { open: boolean; onClose: () => void; onCreated: (a: Account) => void }) {
  const toast = useToast();
  const domains = useQuery({ queryKey: ["admin", "domains"], queryFn: adminApi.domains, enabled: open });
  const empty: CreateAccountRequest = { email: "", name: "", password: "", role: "user", quotaBytes: 5 * 1024 * 1024 * 1024 };
  const [form, setForm] = useState<CreateAccountRequest>(empty);
  const [touched, setTouched] = useState(false);

  useEffect(() => {
    if (open) {
      setForm(empty);
      setTouched(false);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  const create = useMutation({
    mutationFn: () => adminApi.createAccount({ ...form, email: form.email.trim().toLowerCase(), name: form.name.trim() }),
    onSuccess: (a) => {
      toast.success("Konto angelegt", a.email);
      onClose();
      onCreated(a);
    },
    onError: (err) => toast.error(err, "Konto konnte nicht angelegt werden"),
  });

  const emailError = touched && !isEmail(form.email) ? "Bitte eine gültige E-Mail-Adresse eingeben." : null;
  const domainOfEmail = form.email.split("@")[1]?.toLowerCase();
  const knownDomain = !domainOfEmail || !domains.data || domains.data.some((d) => d.name.toLowerCase() === domainOfEmail);
  // LDAP and SSO accounts have no Postwerk password.
  const external = (form.source ?? "local") !== "local";
  const pwOk = external || form.password.length >= 10;
  const pwError = touched && !pwOk ? "Mindestens 10 Zeichen." : null;

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="Neues Konto"
      description="Legt ein Postfach mit Zugangsdaten an."
      icon={
        <span className="flex h-9 w-9 items-center justify-center rounded-lg bg-indigo-50 text-indigo-600 dark:bg-indigo-500/15 dark:text-indigo-300">
          <UserPlus className="h-[18px] w-[18px]" />
        </span>
      }
      dismissible={false}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>
            Abbrechen
          </Button>
          <Button
            variant="primary"
            loading={create.isPending}
            onClick={() => {
              setTouched(true);
              if (isEmail(form.email) && pwOk) create.mutate();
            }}
          >
            Konto anlegen
          </Button>
        </>
      }
    >
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          setTouched(true);
          if (isEmail(form.email) && pwOk) create.mutate();
        }}
      >
        <Field label="Name">
          {(id) => <Input id={id} value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="Erika Mustermann" />}
        </Field>
        <Field
          label="E-Mail-Adresse"
          required
          error={emailError ?? (!knownDomain ? `Die Domain „${domainOfEmail}“ ist nicht angelegt.` : null)}
          help={domains.data?.length ? `Verfügbare Domains: ${domains.data.map((d) => d.name).join(", ")}` : undefined}
        >
          {(id) => (
            <Input
              id={id}
              type="email"
              value={form.email}
              invalid={!!emailError || !knownDomain}
              onChange={(e) => setForm({ ...form, email: e.target.value })}
              placeholder="erika@beispiel.de"
            />
          )}
        </Field>
        <Field label="Anmeldung" help={external ? "Kein Postwerk-Passwort: die Anmeldung prüft das Verzeichnis bzw. der Identitätsanbieter." : undefined}>
          {(id) => (
            <Select
              id={id}
              value={form.source ?? "local"}
              onChange={(e) => setForm({ ...form, source: e.target.value as AccountSource, password: "" })}
              options={[
                { value: "local", label: "Postwerk-Passwort" },
                { value: "ldap", label: "LDAP-Verzeichnis" },
                { value: "oidc", label: "Nur Single Sign-on" },
              ]}
            />
          )}
        </Field>
        {!external && (
        <Field label="Passwort" required error={pwError} help="Mindestens 10 Zeichen. Der Benutzer kann es später selbst ändern.">
          {(id) => (
            <div className="flex gap-2">
              <div className="flex-1">
                <PasswordInput id={id} value={form.password} invalid={!!pwError} onChange={(e) => setForm({ ...form, password: e.target.value })} autoComplete="new-password" />
              </div>
              <Button icon={Wand2} onClick={() => setForm({ ...form, password: generatePassword() })} title="Passwort generieren">
                Generieren
              </Button>
              {form.password && <CopyButton value={form.password} className="px-2.5" />}
            </div>
          )}
        </Field>
        )}
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Rolle">
            {(id) => (
              <Select
                id={id}
                value={form.role}
                onChange={(e) => setForm({ ...form, role: e.target.value as Role })}
                options={[
                  { value: "user", label: "Benutzer" },
                  { value: "admin", label: "Administrator" },
                ]}
              />
            )}
          </Field>
          <Field label="Speicherkontingent" help="0 = unbegrenzt">
            {(id) => <QuotaInput id={id} value={form.quotaBytes} onChange={(v) => setForm({ ...form, quotaBytes: v })} />}
          </Field>
        </div>
        <button type="submit" className="hidden" />
      </form>
      <p className="mt-4 flex items-center gap-2 text-xs text-zinc-500 dark:text-zinc-400">
        <KeyRound className="h-3.5 w-3.5" /> Für IMAP/SMTP kann der Benutzer später eigene App-Passwörter erzeugen.
      </p>
    </Modal>
  );
}
