import { useState } from "react";
import { Link, NavLink, useLocation } from "react-router-dom";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { BookUser, CalendarDays, Ellipsis, FolderPlus, ListTodo, Pencil, Sparkles, SquarePen, Tag, Trash2, UserCog } from "lucide-react";
import { mailApi } from "@/api/endpoints";
import type { Mailbox } from "@/api/types";
import { useMe } from "@/lib/auth";
import { useAiStatus, useMailboxes, useTags, qk } from "@/lib/queries";
import { formatBytes } from "@/lib/format";
import { cn } from "@/lib/utils";
import { Button, dotClass, Dropdown, DropdownItem, Field, Input, Modal, Progress, Skeleton, toneFor, useConfirm, useToast } from "@/components/ui";
import { navIconClass, navItemClass, SidebarSectionTitle } from "@/components/layout/Sidebar";
import { useComposer } from "./Composer";
import { iconFor, mailboxLabel, mailLink, sortMailboxes, useKnownCategories, useMailParams } from "./mailUtils";

export function MailSidebar() {
  const me = useMe();
  const composer = useComposer();
  const ai = useAiStatus();
  const mailboxes = useMailboxes();
  const tags = useTags();
  const categories = useKnownCategories();
  const [params] = useMailParams();
  const { pathname } = useLocation();
  const onMail = pathname === "/mail";
  const [folderDialog, setFolderDialog] = useState<Mailbox | "new" | null>(null);
  const qc = useQueryClient();
  const toast = useToast();
  const confirm = useConfirm();

  const sorted = sortMailboxes(mailboxes.data ?? []);
  const inboxId = sorted.find((m) => m.role === "inbox")?.id ?? null;
  const activeBox = params.box ?? (params.category || params.tag || params.q ? null : inboxId);

  const removeFolder = useMutation({
    mutationFn: (id: number) => mailApi.deleteMailbox(id),
    onSuccess: () => {
      toast.success("Ordner gelöscht");
      void qc.invalidateQueries({ queryKey: qk.mailboxes });
    },
    onError: (err) => toast.error(err, "Ordner konnte nicht gelöscht werden"),
  });

  return (
    <>
      <div className="px-0.5 pb-1 pt-3">
        <Button variant="primary" icon={SquarePen} className="w-full" onClick={() => composer.open()}>
          Neue Nachricht
        </Button>
      </div>

      <SidebarSectionTitle
        action={
          <button
            type="button"
            onClick={() => setFolderDialog("new")}
            className="rounded p-0.5 text-zinc-400 hover:bg-zinc-100 hover:text-zinc-700 dark:hover:bg-zinc-800 dark:hover:text-zinc-200"
            aria-label="Ordner anlegen"
            title="Ordner anlegen"
          >
            <FolderPlus className="h-3.5 w-3.5" />
          </button>
        }
      >
        Ordner
      </SidebarSectionTitle>
      <div className="space-y-0.5">
        {mailboxes.isLoading
          ? Array.from({ length: 6 }, (_, i) => <Skeleton key={i} className="mx-2.5 my-2 h-4" />)
          : sorted.map((m) => {
              const Icon = iconFor(m);
              const active = onMail && activeBox === m.id && !params.category && !params.tag;
              const count = m.role === "drafts" ? m.total : m.unread;
              return (
                <div key={m.id} className="group/item relative">
                  <Link to={mailLink({ box: m.id })} className={navItemClass(active)}>
                    <Icon className={navIconClass(active)} />
                    <span className="min-w-0 flex-1 truncate">{mailboxLabel(m)}</span>
                    {count > 0 && (
                      <span
                        className={cn(
                          "min-w-[1.5rem] rounded-full px-1.5 text-center text-xs font-semibold tabular-nums",
                          m.role === null && "group-hover/item:opacity-0",
                          active
                            ? "bg-indigo-600 text-white dark:bg-indigo-500"
                            : m.role === "drafts"
                              ? "text-zinc-400"
                              : "bg-zinc-100 text-zinc-700 dark:bg-zinc-800 dark:text-zinc-300",
                        )}
                      >
                        {count}
                      </span>
                    )}
                  </Link>
                  {m.role === null && (
                    <div className="absolute right-1 top-1/2 -translate-y-1/2 opacity-0 transition group-hover/item:opacity-100 focus-within:opacity-100">
                      <Dropdown
                        align="right"
                        trigger={({ toggle }) => (
                          <button
                            type="button"
                            onClick={toggle}
                            className="rounded p-1 text-zinc-400 hover:bg-zinc-200 hover:text-zinc-700 dark:hover:bg-zinc-700 dark:hover:text-zinc-200"
                            aria-label={`Aktionen für ${m.name}`}
                          >
                            <Ellipsis className="h-4 w-4" />
                          </button>
                        )}
                        menuClassName="min-w-[10rem]"
                      >
                        {(close) => (
                          <>
                            <DropdownItem icon={Pencil} onClick={() => { close(); setFolderDialog(m); }}>
                              Umbenennen
                            </DropdownItem>
                            <DropdownItem
                              icon={Trash2}
                              danger
                              onClick={async () => {
                                close();
                                const ok = await confirm({
                                  title: `Ordner „${m.name}“ löschen?`,
                                  message: m.total > 0 ? `Der Ordner enthält ${m.total} Nachrichten.` : undefined,
                                  confirmLabel: "Löschen",
                                  danger: true,
                                });
                                if (ok) removeFolder.mutate(m.id);
                              }}
                            >
                              Löschen
                            </DropdownItem>
                          </>
                        )}
                      </Dropdown>
                    </div>
                  )}
                </div>
              );
            })}
      </div>

      {ai.enabled && ai.features.classify && categories.length > 0 && (
        <>
          <SidebarSectionTitle>
            <span className="inline-flex items-center gap-1">
              <Sparkles className="h-3 w-3" /> Kategorien
            </span>
          </SidebarSectionTitle>
          <div className="space-y-0.5">
            {categories.map((c) => {
              const active = onMail && params.category === c;
              return (
                <Link key={c} to={mailLink({ category: c })} className={navItemClass(active)}>
                  <span className="flex h-[18px] w-[18px] items-center justify-center">
                    <span className={cn("h-2.5 w-2.5 rounded-full", dotClass(toneFor(c)))} />
                  </span>
                  <span className="truncate">{c}</span>
                </Link>
              );
            })}
          </div>
        </>
      )}

      {tags.data && tags.data.length > 0 && (
        <>
          <SidebarSectionTitle>Tags</SidebarSectionTitle>
          <div className="space-y-0.5">
            {tags.data.map((t) => {
              const active = onMail && params.tag === t.name;
              return (
                <Link key={t.name} to={mailLink({ tag: t.name })} className={navItemClass(active)}>
                  <Tag className={navIconClass(active)} />
                  <span className="min-w-0 flex-1 truncate">{t.name}</span>
                  <span className="text-xs tabular-nums text-zinc-400">{t.count}</span>
                </Link>
              );
            })}
          </div>
        </>
      )}

      <SidebarSectionTitle>Organisieren</SidebarSectionTitle>
      <div className="space-y-0.5">
        <NavLink to="/mail/calendar" className={({ isActive }) => navItemClass(isActive)}>
          {({ isActive }) => (
            <>
              <CalendarDays className={navIconClass(isActive)} /> Kalender
            </>
          )}
        </NavLink>
        <NavLink to="/mail/tasks" className={({ isActive }) => navItemClass(isActive)}>
          {({ isActive }) => (
            <>
              <ListTodo className={navIconClass(isActive)} /> Aufgaben
            </>
          )}
        </NavLink>
        <NavLink to="/mail/contacts" className={({ isActive }) => navItemClass(isActive)}>
          {({ isActive }) => (
            <>
              <BookUser className={navIconClass(isActive)} /> Kontakte
            </>
          )}
        </NavLink>
        <NavLink to="/mail/settings" className={({ isActive }) => navItemClass(isActive)}>
          {({ isActive }) => (
            <>
              <UserCog className={navIconClass(isActive)} /> Einstellungen
            </>
          )}
        </NavLink>
      </div>

      <div className="mx-1 mt-6 rounded-lg border border-zinc-200 p-3 dark:border-zinc-800">
        <div className="mb-1.5 flex justify-between text-xs text-zinc-500 dark:text-zinc-400">
          <span>Speicher</span>
          <span className="tabular-nums">
            {formatBytes(me.usedBytes)}
            {me.quotaBytes > 0 && ` / ${formatBytes(me.quotaBytes)}`}
          </span>
        </div>
        <Progress value={me.quotaBytes > 0 ? me.usedBytes / me.quotaBytes : 0} />
      </div>

      <FolderDialog value={folderDialog} onClose={() => setFolderDialog(null)} />
    </>
  );
}

function FolderDialog({ value, onClose }: { value: Mailbox | "new" | null; onClose: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const isNew = value === "new";
  const [name, setName] = useState("");
  const [lastValue, setLastValue] = useState<typeof value>(null);
  if (value !== lastValue) {
    setLastValue(value);
    setName(value && value !== "new" ? value.name : "");
  }

  const save = useMutation({
    mutationFn: () =>
      isNew || value === null ? mailApi.createMailbox(name.trim()) : mailApi.renameMailbox(value.id, name.trim()),
    onSuccess: () => {
      toast.success(isNew ? "Ordner angelegt" : "Ordner umbenannt");
      void qc.invalidateQueries({ queryKey: qk.mailboxes });
      onClose();
    },
    onError: (err) => toast.error(err, "Speichern fehlgeschlagen"),
  });

  return (
    <Modal
      open={value !== null}
      onClose={onClose}
      size="sm"
      title={isNew ? "Neuer Ordner" : "Ordner umbenennen"}
      footer={
        <>
          <Button onClick={onClose}>Abbrechen</Button>
          <Button variant="primary" disabled={!name.trim()} loading={save.isPending} onClick={() => save.mutate()}>
            {isNew ? "Anlegen" : "Speichern"}
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
        <Field label="Name">{(id) => <Input id={id} value={name} onChange={(e) => setName(e.target.value)} placeholder="z. B. Projekte" />}</Field>
      </form>
    </Modal>
  );
}
