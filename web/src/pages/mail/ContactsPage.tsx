import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  BookUser,
  Briefcase,
  Building2,
  Cake,
  Download,
  Mail,
  MoreHorizontal,
  Pencil,
  Phone,
  Plus,
  Search,
  StickyNote,
  Trash2,
  Upload,
  UserPlus,
  X,
} from "lucide-react";
import { addressBooksApi, contactsApi } from "@/api/endpoints";
import type { AddressBook, Contact, ContactEntry, ContactInput } from "@/api/types";
import { qk } from "@/lib/queries";
import { cn } from "@/lib/utils";
import {
  Avatar,
  Button,
  Card,
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
  Select,
  SkeletonRows,
  Textarea,
  useConfirm,
  useToast,
} from "@/components/ui";
import { useComposer } from "./Composer";
import { parseEventTime, readFileText } from "./calendarUtils";

const EMAIL_TYPES = [
  { value: "home", label: "Privat" },
  { value: "work", label: "Arbeit" },
  { value: "other", label: "Sonstige" },
];
const PHONE_TYPES = [
  { value: "cell", label: "Mobil" },
  { value: "home", label: "Privat" },
  { value: "work", label: "Arbeit" },
  { value: "other", label: "Sonstige" },
];

const typeLabel = (list: { value: string; label: string }[], t: string) => list.find((x) => x.value === t)?.label ?? t;

function displayName(c: Pick<Contact, "name" | "firstName" | "lastName" | "organization" | "emails">): string {
  return c.name || [c.firstName, c.lastName].filter(Boolean).join(" ") || c.organization || c.emails[0]?.value || "Ohne Namen";
}

function useDebounced<T>(value: T, ms: number): T {
  const [v, setV] = useState(value);
  useEffect(() => {
    const t = window.setTimeout(() => setV(value), ms);
    return () => window.clearTimeout(t);
  }, [value, ms]);
  return v;
}

export default function ContactsPage() {
  const qc = useQueryClient();
  const toast = useToast();
  const confirm = useConfirm();
  const composer = useComposer();
  const [search, setSearch] = useState("");
  const q = useDebounced(search.trim(), 250);
  const [book, setBook] = useState<number | "all">("all");
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [editing, setEditing] = useState<Contact | "new" | null>(null);
  const [bookDialog, setBookDialog] = useState<AddressBook | "new" | null>(null);
  const fileRef = useRef<HTMLInputElement>(null);

  const books = useQuery({ queryKey: qk.addressBooks, queryFn: addressBooksApi.list });
  const bookId = book === "all" ? undefined : book;
  const contacts = useQuery({
    queryKey: [...qk.contacts, q, bookId ?? 0],
    queryFn: ({ signal }) => contactsApi.list(q || undefined, bookId, signal),
    placeholderData: (prev) => prev,
  });

  const list = useMemo(
    () => [...(contacts.data ?? [])].sort((a, b) => displayName(a).localeCompare(displayName(b), "de", { sensitivity: "base" })),
    [contacts.data],
  );
  const selected = list.find((c) => c.id === selectedId) ?? null;
  const currentBook = books.data?.find((b) => b.id === bookId);

  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: qk.contacts });
    void qc.invalidateQueries({ queryKey: qk.addressBooks });
  };

  const remove = useMutation({
    mutationFn: (id: number) => contactsApi.remove(id),
    onSuccess: () => {
      toast.success("Kontakt gelöscht");
      setSelectedId(null);
      invalidate();
    },
    onError: (err) => toast.error(err, "Löschen fehlgeschlagen"),
  });

  const importVcf = useMutation({
    mutationFn: async (file: File) => contactsApi.importVcf(await readFileText(file), bookId),
    onSuccess: (r) => {
      toast.success("Import abgeschlossen", `${r.imported} neu, ${r.updated} aktualisiert, ${r.skipped} übersprungen`);
      invalidate();
    },
    onError: (err) => toast.error(err, "Import fehlgeschlagen"),
  });

  const removeBook = useMutation({
    mutationFn: (id: number) => addressBooksApi.remove(id),
    onSuccess: () => {
      toast.success("Adressbuch gelöscht");
      setBook("all");
      invalidate();
    },
    onError: (err) => toast.error(err, "Adressbuch konnte nicht gelöscht werden"),
  });

  const total = books.data?.reduce((s, b) => s + b.count, 0);

  return (
    <>
      <PageHeader
        title="Kontakte"
        description={
          total !== undefined ? `${total} ${total === 1 ? "Kontakt" : "Kontakte"} – per CardDAV auch auf Smartphone und Desktop.` : "Dein Adressbuch."
        }
        actions={
          <>
            <Dropdown
              align="right"
              trigger={({ toggle }) => <Button icon={MoreHorizontal} onClick={toggle} aria-label="Weitere Aktionen" />}
            >
              {(close) => (
                <>
                  <DropdownItem
                    icon={Upload}
                    onClick={() => {
                      close();
                      fileRef.current?.click();
                    }}
                  >
                    vCard importieren …
                  </DropdownItem>
                  <DropdownItem
                    icon={Download}
                    onClick={() => {
                      close();
                      window.location.href = contactsApi.exportUrl(bookId);
                    }}
                  >
                    {currentBook ? `„${currentBook.name}“ exportieren` : "Alle exportieren (.vcf)"}
                  </DropdownItem>
                  <DropdownSeparator />
                  <DropdownItem
                    icon={Plus}
                    onClick={() => {
                      close();
                      setBookDialog("new");
                    }}
                  >
                    Neues Adressbuch
                  </DropdownItem>
                  {currentBook && (
                    <>
                      <DropdownItem
                        icon={Pencil}
                        onClick={() => {
                          close();
                          setBookDialog(currentBook);
                        }}
                      >
                        Adressbuch umbenennen
                      </DropdownItem>
                      <DropdownItem
                        icon={Trash2}
                        danger
                        disabled={(books.data?.length ?? 0) <= 1}
                        onClick={async () => {
                          close();
                          const ok = await confirm({
                            title: "Adressbuch löschen?",
                            message: `„${currentBook.name}“ wird mit allen ${currentBook.count} Kontakten gelöscht – auch auf synchronisierten Geräten.`,
                            confirmLabel: "Löschen",
                            danger: true,
                          });
                          if (ok) removeBook.mutate(currentBook.id);
                        }}
                      >
                        Adressbuch löschen
                      </DropdownItem>
                    </>
                  )}
                </>
              )}
            </Dropdown>
            <Button variant="primary" icon={UserPlus} onClick={() => setEditing("new")}>
              Kontakt anlegen
            </Button>
          </>
        }
      />
      <input
        ref={fileRef}
        type="file"
        accept=".vcf,text/vcard,text/x-vcard"
        className="hidden"
        onChange={(e) => {
          const file = e.target.files?.[0];
          e.target.value = "";
          if (file) importVcf.mutate(file);
        }}
      />

      <div className="grid gap-4 lg:grid-cols-[minmax(0,22rem)_minmax(0,1fr)]">
        <Card className="flex min-h-[24rem] flex-col overflow-hidden">
          <div className="space-y-2 border-b border-zinc-100 p-3 dark:border-zinc-800">
            <Input
              icon={Search}
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="Name, E-Mail, Telefon …"
              aria-label="Kontakte durchsuchen"
              trailing={
                search ? (
                  <button type="button" onClick={() => setSearch("")} className="rounded p-1 text-zinc-400 hover:text-zinc-700" aria-label="Suche leeren">
                    <X className="h-3.5 w-3.5" />
                  </button>
                ) : undefined
              }
            />
            {(books.data?.length ?? 0) > 1 && (
              <Select
                size="sm"
                value={String(book)}
                onChange={(e) => setBook(e.target.value === "all" ? "all" : Number(e.target.value))}
                options={[
                  { value: "all", label: "Alle Adressbücher" },
                  ...(books.data ?? []).map((b) => ({ value: String(b.id), label: `${b.name} (${b.count})` })),
                ]}
                aria-label="Adressbuch"
              />
            )}
          </div>
          {contacts.isLoading ? (
            <SkeletonRows rows={6} cols={1} />
          ) : contacts.error ? (
            <ErrorState error={contacts.error} onRetry={() => void contacts.refetch()} />
          ) : list.length === 0 ? (
            <EmptyState
              icon={BookUser}
              compact
              title={q ? "Keine Treffer" : "Noch keine Kontakte"}
              description={q ? "Kein Kontakt passt zur Suche." : "Lege Kontakte an, importiere eine vCard-Datei oder synchronisiere dein Adressbuch per CardDAV."}
              action={
                !q && (
                  <Button size="sm" icon={UserPlus} onClick={() => setEditing("new")}>
                    Kontakt anlegen
                  </Button>
                )
              }
            />
          ) : (
            <ul className={cn("flex-1 divide-y divide-zinc-100 overflow-y-auto dark:divide-zinc-800", contacts.isFetching && "opacity-70")}>
              {list.map((c) => {
                const name = displayName(c);
                const sub = c.emails[0]?.value || c.phones[0]?.value || c.organization;
                return (
                  <li key={c.id}>
                    <button
                      type="button"
                      onClick={() => setSelectedId(c.id)}
                      className={cn(
                        "flex w-full items-center gap-3 px-3 py-2.5 text-left transition-colors",
                        c.id === selectedId ? "bg-indigo-50/70 dark:bg-indigo-500/10" : "hover:bg-zinc-50 dark:hover:bg-zinc-800/40",
                      )}
                    >
                      <Avatar name={name} email={c.emails[0]?.value} size="sm" />
                      <span className="min-w-0 flex-1">
                        <span className="block truncate text-sm font-medium text-zinc-900 dark:text-zinc-100">{name}</span>
                        {sub && <span className="block truncate text-xs text-zinc-500 dark:text-zinc-400">{sub}</span>}
                      </span>
                      {c.organization && sub !== c.organization && (
                        <span className="hidden max-w-[7rem] truncate text-xs text-zinc-400 xl:block">{c.organization}</span>
                      )}
                    </button>
                  </li>
                );
              })}
            </ul>
          )}
        </Card>

        <Card className="min-h-[24rem]">
          {selected ? (
            <ContactDetail
              contact={selected}
              book={books.data?.find((b) => b.id === selected.addressBookId)}
              showBook={(books.data?.length ?? 0) > 1}
              onEdit={() => setEditing(selected)}
              onWrite={(email) => composer.open({ to: [email] })}
              onDelete={async () => {
                const ok = await confirm({ title: "Kontakt löschen?", message: displayName(selected), confirmLabel: "Löschen", danger: true });
                if (ok) remove.mutate(selected.id);
              }}
            />
          ) : (
            <EmptyState icon={BookUser} title="Kein Kontakt ausgewählt" description="Wähle links einen Kontakt aus, um die Details zu sehen." />
          )}
        </Card>
      </div>

      <ContactEditDialog
        contact={editing}
        books={books.data ?? []}
        defaultBook={bookId}
        onClose={() => setEditing(null)}
        onSaved={(c) => {
          setSelectedId(c.id);
          invalidate();
        }}
      />
      <AddressBookDialog book={bookDialog} onClose={() => setBookDialog(null)} onSaved={invalidate} />
    </>
  );
}

// ---- detail ----

function DetailRow({ icon: Icon, label, children }: { icon: typeof Mail; label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-start gap-3 py-2.5">
      <Icon className="mt-0.5 h-4 w-4 shrink-0 text-zinc-400" />
      <div className="min-w-0 flex-1">
        <div className="text-[11px] font-semibold uppercase tracking-wide text-zinc-400 dark:text-zinc-500">{label}</div>
        <div className="mt-0.5 text-sm text-zinc-800 dark:text-zinc-200">{children}</div>
      </div>
    </div>
  );
}

function ContactDetail({
  contact: c,
  book,
  showBook,
  onEdit,
  onWrite,
  onDelete,
}: {
  contact: Contact;
  book?: AddressBook;
  showBook: boolean;
  onEdit: () => void;
  onWrite: (email: string) => void;
  onDelete: () => void;
}) {
  const name = displayName(c);
  const birthday = c.birthday ? parseEventTime(c.birthday).toLocaleDateString("de-DE", { dateStyle: "long" }) : null;
  return (
    <div>
      <div className="flex flex-wrap items-center gap-4 border-b border-zinc-100 p-5 dark:border-zinc-800">
        <Avatar name={name} email={c.emails[0]?.value} size="lg" />
        <div className="min-w-0 flex-1">
          <h2 className="truncate text-lg font-semibold text-zinc-900 dark:text-zinc-50">{name}</h2>
          {(c.title || c.organization) && (
            <p className="truncate text-sm text-zinc-500 dark:text-zinc-400">{[c.title, c.organization].filter(Boolean).join(" · ")}</p>
          )}
          {showBook && book && <p className="mt-0.5 text-xs text-zinc-400">{book.name}</p>}
        </div>
        <div className="flex gap-2">
          {c.emails[0] && (
            <Button size="sm" icon={Mail} onClick={() => onWrite(c.emails[0].value)}>
              Mail schreiben
            </Button>
          )}
          <Button size="sm" icon={Pencil} onClick={onEdit}>
            Bearbeiten
          </Button>
          <Button size="icon-sm" variant="danger-ghost" icon={Trash2} onClick={onDelete} aria-label="Kontakt löschen" className="h-8 w-8" />
        </div>
      </div>
      <div className="divide-y divide-zinc-100 px-5 dark:divide-zinc-800">
        {c.emails.length > 0 && (
          <DetailRow icon={Mail} label="E-Mail">
            <ul className="space-y-1">
              {c.emails.map((e, i) => (
                <li key={i} className="flex items-center gap-2">
                  <button type="button" onClick={() => onWrite(e.value)} className="truncate text-indigo-600 hover:underline dark:text-indigo-400">
                    {e.value}
                  </button>
                  <span className="text-xs text-zinc-400">{typeLabel(EMAIL_TYPES, e.type)}</span>
                  <CopyButton value={e.value} />
                </li>
              ))}
            </ul>
          </DetailRow>
        )}
        {c.phones.length > 0 && (
          <DetailRow icon={Phone} label="Telefon">
            <ul className="space-y-1">
              {c.phones.map((p, i) => (
                <li key={i} className="flex items-center gap-2">
                  <a href={`tel:${p.value.replace(/[^\d+]/g, "")}`} className="truncate hover:underline">
                    {p.value}
                  </a>
                  <span className="text-xs text-zinc-400">{typeLabel(PHONE_TYPES, p.type)}</span>
                  <CopyButton value={p.value} />
                </li>
              ))}
            </ul>
          </DetailRow>
        )}
        {c.organization && (
          <DetailRow icon={Building2} label="Organisation">
            {c.organization}
          </DetailRow>
        )}
        {c.title && (
          <DetailRow icon={Briefcase} label="Position">
            {c.title}
          </DetailRow>
        )}
        {birthday && (
          <DetailRow icon={Cake} label="Geburtstag">
            {birthday}
          </DetailRow>
        )}
        {c.notes && (
          <DetailRow icon={StickyNote} label="Notizen">
            <p className="whitespace-pre-wrap">{c.notes}</p>
          </DetailRow>
        )}
        {!c.emails.length && !c.phones.length && !c.organization && !c.title && !birthday && !c.notes && (
          <p className="py-6 text-sm text-zinc-500 dark:text-zinc-400">Keine weiteren Angaben.</p>
        )}
      </div>
    </div>
  );
}

// ---- edit dialog ----

const emptyInput = (book?: number): ContactInput => ({
  ...(book ? { addressBookId: book } : {}),
  name: "",
  firstName: "",
  lastName: "",
  emails: [{ type: "home", value: "" }],
  phones: [{ type: "cell", value: "" }],
  organization: "",
  title: "",
  notes: "",
  birthday: null,
});

function toInput(c: Contact): ContactInput {
  return {
    addressBookId: c.addressBookId,
    name: c.name,
    firstName: c.firstName,
    lastName: c.lastName,
    emails: c.emails.length ? c.emails.map((e) => ({ ...e })) : [{ type: "home", value: "" }],
    phones: c.phones.length ? c.phones.map((p) => ({ ...p })) : [{ type: "cell", value: "" }],
    organization: c.organization,
    title: c.title,
    notes: c.notes,
    birthday: c.birthday,
  };
}

function EntryList({
  label,
  entries,
  types,
  inputType,
  placeholder,
  onChange,
}: {
  label: string;
  entries: ContactEntry[];
  types: { value: string; label: string }[];
  inputType: string;
  placeholder: string;
  onChange: (entries: ContactEntry[]) => void;
}) {
  const update = (i: number, patch: Partial<ContactEntry>) => onChange(entries.map((e, j) => (j === i ? { ...e, ...patch } : e)));
  return (
    <Field label={label}>
      {() => (
        <div className="space-y-2">
          {entries.map((e, i) => (
            <div key={i} className="flex gap-2">
              <Select size="md" value={e.type} onChange={(ev) => update(i, { type: ev.target.value })} options={types} className="w-32 shrink-0" aria-label="Art" />
              <div className="min-w-0 flex-1">
                <Input type={inputType} value={e.value} onChange={(ev) => update(i, { value: ev.target.value })} placeholder={placeholder} />
              </div>
              <Button
                size="icon"
                variant="ghost"
                icon={X}
                onClick={() => onChange(entries.length > 1 ? entries.filter((_, j) => j !== i) : [{ type: types[0].value, value: "" }])}
                aria-label="Eintrag entfernen"
              />
            </div>
          ))}
          <Button size="xs" variant="ghost" icon={Plus} onClick={() => onChange([...entries, { type: types[0].value, value: "" }])}>
            Weitere hinzufügen
          </Button>
        </div>
      )}
    </Field>
  );
}

function ContactEditDialog({
  contact,
  books,
  defaultBook,
  onClose,
  onSaved,
}: {
  contact: Contact | "new" | null;
  books: AddressBook[];
  defaultBook?: number;
  onClose: () => void;
  onSaved: (c: Contact) => void;
}) {
  const toast = useToast();
  const [f, setF] = useState<ContactInput>(() => emptyInput(defaultBook));
  const [last, setLast] = useState<Contact | "new" | null>(null);
  if (contact !== last) {
    setLast(contact);
    if (contact === "new") setF(emptyInput(defaultBook ?? books[0]?.id));
    else if (contact) setF(toInput(contact));
  }
  const set = <K extends keyof ContactInput>(k: K, v: ContactInput[K]) => setF((prev) => ({ ...prev, [k]: v }));

  const save = useMutation({
    mutationFn: () => {
      const body: ContactInput = {
        ...f,
        name: f.name.trim(),
        firstName: f.firstName.trim(),
        lastName: f.lastName.trim(),
        organization: f.organization.trim(),
        title: f.title.trim(),
        notes: f.notes.trim(),
        emails: f.emails.map((e) => ({ ...e, value: e.value.trim() })).filter((e) => e.value),
        phones: f.phones.map((p) => ({ ...p, value: p.value.trim() })).filter((p) => p.value),
        birthday: f.birthday || null,
      };
      return contact && contact !== "new" ? contactsApi.update(contact.id, body) : contactsApi.create(body);
    },
    onSuccess: (c) => {
      toast.success(contact === "new" ? "Kontakt angelegt" : "Kontakt gespeichert");
      onSaved(c);
      onClose();
    },
    onError: (err) => toast.error(err, "Speichern fehlgeschlagen"),
  });

  const canSave = !!(f.name.trim() || f.firstName.trim() || f.lastName.trim() || f.organization.trim() || f.emails.some((e) => e.value.trim()));

  return (
    <Modal
      open={contact !== null}
      onClose={onClose}
      dismissible={false}
      size="lg"
      title={contact === "new" ? "Neuer Kontakt" : "Kontakt bearbeiten"}
      icon={
        <span className="flex h-9 w-9 items-center justify-center rounded-lg bg-indigo-50 text-indigo-600 dark:bg-indigo-500/15 dark:text-indigo-300">
          <BookUser className="h-[18px] w-[18px]" />
        </span>
      }
      footer={
        <>
          <Button onClick={onClose}>Abbrechen</Button>
          <Button variant="primary" disabled={!canSave} loading={save.isPending} onClick={() => save.mutate()}>
            Speichern
          </Button>
        </>
      }
    >
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          if (canSave) save.mutate();
        }}
      >
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Vorname">{(id) => <Input id={id} value={f.firstName} onChange={(e) => set("firstName", e.target.value)} autoFocus />}</Field>
          <Field label="Nachname">{(id) => <Input id={id} value={f.lastName} onChange={(e) => set("lastName", e.target.value)} />}</Field>
        </div>
        <Field label="Anzeigename" help="Leer lassen, um ihn aus Vor- und Nachname zu bilden.">
          {(id) => (
            <Input
              id={id}
              value={f.name}
              onChange={(e) => set("name", e.target.value)}
              placeholder={[f.firstName, f.lastName].filter(Boolean).join(" ") || "z. B. Erika Mustermann"}
            />
          )}
        </Field>
        <EntryList label="E-Mail-Adressen" entries={f.emails} types={EMAIL_TYPES} inputType="email" placeholder="name@beispiel.de" onChange={(v) => set("emails", v)} />
        <EntryList label="Telefonnummern" entries={f.phones} types={PHONE_TYPES} inputType="tel" placeholder="+49 …" onChange={(v) => set("phones", v)} />
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Organisation">{(id) => <Input id={id} value={f.organization} onChange={(e) => set("organization", e.target.value)} />}</Field>
          <Field label="Position">{(id) => <Input id={id} value={f.title} onChange={(e) => set("title", e.target.value)} />}</Field>
        </div>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Geburtstag">
            {(id) => <Input id={id} type="date" value={f.birthday ?? ""} onChange={(e) => set("birthday", e.target.value || null)} />}
          </Field>
          {books.length > 1 && (
            <Field label="Adressbuch">
              {(id) => (
                <Select
                  id={id}
                  value={String(f.addressBookId ?? books[0]?.id ?? "")}
                  onChange={(e) => set("addressBookId", Number(e.target.value))}
                  options={books.map((b) => ({ value: String(b.id), label: b.name }))}
                />
              )}
            </Field>
          )}
        </div>
        <Field label="Notizen">{(id) => <Textarea id={id} rows={3} value={f.notes} onChange={(e) => set("notes", e.target.value)} />}</Field>
        <button type="submit" className="hidden" />
      </form>
    </Modal>
  );
}

function AddressBookDialog({ book, onClose, onSaved }: { book: AddressBook | "new" | null; onClose: () => void; onSaved: () => void }) {
  const toast = useToast();
  const [name, setName] = useState("");
  const [last, setLast] = useState<AddressBook | "new" | null>(null);
  if (book !== last) {
    setLast(book);
    setName(book && book !== "new" ? book.name : "");
  }
  const save = useMutation({
    mutationFn: () => (book && book !== "new" ? addressBooksApi.update(book.id, name.trim()) : addressBooksApi.create(name.trim())),
    onSuccess: () => {
      toast.success(book === "new" ? "Adressbuch angelegt" : "Adressbuch gespeichert");
      onSaved();
      onClose();
    },
    onError: (err) => toast.error(err, "Speichern fehlgeschlagen"),
  });
  return (
    <Modal
      open={book !== null}
      onClose={onClose}
      size="sm"
      title={book === "new" ? "Neues Adressbuch" : "Adressbuch umbenennen"}
      footer={
        <>
          <Button onClick={onClose}>Abbrechen</Button>
          <Button variant="primary" disabled={!name.trim()} loading={save.isPending} onClick={() => save.mutate()}>
            Speichern
          </Button>
        </>
      }
    >
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (name.trim()) save.mutate();
        }}
      >
        <Field label="Name">{(id) => <Input id={id} value={name} onChange={(e) => setName(e.target.value)} placeholder="z. B. Arbeit" autoFocus />}</Field>
      </form>
    </Modal>
  );
}
