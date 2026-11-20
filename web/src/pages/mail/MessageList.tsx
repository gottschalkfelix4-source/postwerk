import { forwardRef, useEffect, useRef, useState, type ReactNode } from "react";
import {
  Archive,
  ChevronDown,
  Flame,
  FolderInput,
  Inbox,
  Mail,
  MailOpen,
  Paperclip,
  RefreshCw,
  Search,
  ShieldCheck,
  ShieldX,
  Sparkles,
  Star,
  Trash2,
  X,
} from "lucide-react";
import type { BulkAction, Mailbox, MessageSummary } from "@/api/types";
import { formatDateTime, formatShort } from "@/lib/format";
import { cn } from "@/lib/utils";
import {
  Avatar,
  Badge,
  Button,
  Checkbox,
  Dropdown,
  DropdownItem,
  DropdownLabel,
  EmptyState,
  ErrorState,
  Pagination,
  Segmented,
  Skeleton,
  toneFor,
} from "@/components/ui";
import { iconFor, mailboxLabel, type MailParams } from "./mailUtils";

export interface MessageListProps {
  title: string;
  params: MailParams;
  onParams: (patch: Partial<MailParams>) => void;
  items: (MessageSummary & { score?: number })[];
  total: number;
  offset: number;
  limit: number;
  onOffset: (o: number) => void;
  loading: boolean;
  fetching: boolean;
  error: unknown;
  onRetry: () => void;
  selected: Set<number>;
  onSelected: (s: Set<number>) => void;
  activeId: number | null;
  onOpen: (id: number) => void;
  onAction: (ids: number[], action: BulkAction, mailboxId?: number) => void;
  onToggleFlag: (m: MessageSummary) => void;
  mailboxes: Mailbox[];
  currentBox: Mailbox | undefined;
  aiSearch: boolean;
  aiSummaries: boolean;
  categories: string[];
  semantic: boolean;
}

export const MessageList = forwardRef<HTMLInputElement, MessageListProps>(function MessageList(props, searchRef) {
  const {
    title,
    params,
    onParams,
    items,
    total,
    offset,
    limit,
    onOffset,
    loading,
    fetching,
    error,
    onRetry,
    selected,
    onSelected,
    activeId,
    onOpen,
    onAction,
    onToggleFlag,
    mailboxes,
    currentBox,
    aiSearch,
    aiSummaries,
    categories,
    semantic,
  } = props;

  const [search, setSearch] = useState(params.q);
  const lastQ = useRef(params.q);
  useEffect(() => {
    if (params.q !== lastQ.current) {
      lastQ.current = params.q;
      setSearch(params.q);
    }
  }, [params.q]);

  // Classic search applies while typing; semantic search waits for Enter.
  useEffect(() => {
    if (params.mode === "semantic") return;
    const t = window.setTimeout(() => {
      if (search.trim() !== params.q) {
        lastQ.current = search.trim();
        onParams({ q: search.trim(), msg: null });
      }
    }, 350);
    return () => window.clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [search, params.mode]);

  const ids = items.map((m) => m.id);
  const allSelected = ids.length > 0 && ids.every((id) => selected.has(id));
  const selectedIds = [...selected];
  const inJunk = currentBox?.role === "junk";
  const archive = mailboxes.find((m) => m.role === "archive");

  const toggle = (id: number) => {
    const n = new Set(selected);
    if (n.has(id)) n.delete(id);
    else n.add(id);
    onSelected(n);
  };

  return (
    <div className="flex h-full min-h-0 flex-col">
      {/* Header */}
      <div className="space-y-3 border-b border-zinc-200 px-4 pb-3 pt-4 dark:border-zinc-800">
        <div className="flex items-center justify-between gap-2">
          <div className="min-w-0">
            <h1 className="truncate text-lg font-semibold text-zinc-900 dark:text-zinc-50">{title}</h1>
            <p className="text-xs text-zinc-500 dark:text-zinc-400">
              {loading ? "Wird geladen …" : semantic ? `${total} relevante Treffer` : `${total} Nachrichten`}
              {currentBox && currentBox.unread > 0 && !semantic && ` · ${currentBox.unread} ungelesen`}
            </p>
          </div>
          <Button size="icon-sm" variant="ghost" icon={RefreshCw} onClick={onRetry} aria-label="Aktualisieren" className={cn(fetching && "[&>svg]:animate-spin")} />
        </div>

        <div className="space-y-2">
          <div className="relative">
            {params.mode === "semantic" ? (
              <Sparkles className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-violet-500" />
            ) : (
              <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-zinc-400" />
            )}
            <input
              ref={searchRef}
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  lastQ.current = search.trim();
                  onParams({ q: search.trim(), msg: null });
                } else if (e.key === "Escape") {
                  setSearch("");
                  lastQ.current = "";
                  onParams({ q: "" });
                  (e.target as HTMLInputElement).blur();
                }
              }}
              placeholder={params.mode === "semantic" ? "Frag nach Inhalten … (Enter)" : "Nachrichten durchsuchen …"}
              className={cn(
                "h-9 w-full rounded-lg border bg-zinc-50 pl-9 pr-8 text-sm text-zinc-900 placeholder:text-zinc-400 transition focus:bg-white focus:outline-none focus:ring-2 dark:bg-zinc-800/60 dark:text-zinc-100 dark:focus:bg-zinc-900",
                params.mode === "semantic"
                  ? "border-violet-300 focus:border-violet-500 focus:ring-violet-500/25 dark:border-violet-500/40"
                  : "border-zinc-200 focus:border-indigo-500 focus:ring-indigo-500/25 dark:border-zinc-700",
              )}
            />
            {search && (
              <button
                type="button"
                onClick={() => {
                  setSearch("");
                  lastQ.current = "";
                  onParams({ q: "" });
                }}
                className="absolute right-2 top-1/2 -translate-y-1/2 rounded p-0.5 text-zinc-400 hover:text-zinc-700 dark:hover:text-zinc-200"
                aria-label="Suche leeren"
              >
                <X className="h-4 w-4" />
              </button>
            )}
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <Segmented
              size="sm"
              value={params.mode}
              onChange={(m) => onParams({ mode: m, msg: null })}
              disabled={!aiSearch && params.mode === "classic"}
              items={[
                { value: "classic", label: "Klassisch" },
                {
                  value: "semantic",
                  label: (
                    <span className="inline-flex items-center gap-1" title={aiSearch ? undefined : "KI-Suche ist nicht verfügbar"}>
                      <Sparkles className="h-3 w-3" /> Semantisch (KI)
                    </span>
                  ),
                },
              ]}
            />
            <div className="flex flex-wrap gap-1.5">
              <FilterChip active={params.unread} onClick={() => onParams({ unread: !params.unread })}>
                Ungelesen
              </FilterChip>
              <FilterChip active={params.important} onClick={() => onParams({ important: !params.important })}>
                Wichtig
              </FilterChip>
              {categories.length > 0 && (
                <Dropdown
                  trigger={({ toggle }) => (
                    <FilterChip active={!!params.category} onClick={toggle}>
                      {params.category ?? "Kategorie"} <ChevronDown className="h-3 w-3" />
                    </FilterChip>
                  )}
                  menuClassName="min-w-[11rem]"
                >
                  {(close) => (
                    <>
                      <DropdownLabel>KI-Kategorie</DropdownLabel>
                      <DropdownItem selected={!params.category} onClick={() => { close(); onParams({ category: null }); }}>
                        Alle
                      </DropdownItem>
                      {categories.map((c) => (
                        <DropdownItem key={c} selected={params.category === c} onClick={() => { close(); onParams({ category: c, msg: null }); }}>
                          {c}
                        </DropdownItem>
                      ))}
                    </>
                  )}
                </Dropdown>
              )}
            </div>
          </div>
        </div>
      </div>

      {/* Selection bar */}
      {items.length > 0 && (
        <div
          className={cn(
            "flex h-11 shrink-0 items-center gap-1 border-b px-3",
            selected.size > 0
              ? "border-indigo-100 bg-indigo-50/70 dark:border-indigo-500/20 dark:bg-indigo-500/10"
              : "border-zinc-100 dark:border-zinc-800",
          )}
        >
          <Checkbox
            checked={allSelected}
            indeterminate={!allSelected && selected.size > 0}
            onChange={() => onSelected(allSelected ? new Set() : new Set(ids))}
            aria-label="Alle auswählen"
            className="mx-1"
          />
          {selected.size === 0 ? (
            <span className="ml-1 text-xs text-zinc-500 dark:text-zinc-400">Alle auswählen</span>
          ) : (
            <>
              <span className="ml-1 mr-1 text-xs font-medium text-indigo-700 dark:text-indigo-300">{selected.size}</span>
              <Button size="icon-sm" variant="ghost" icon={MailOpen} title="Als gelesen markieren" aria-label="Als gelesen markieren" onClick={() => onAction(selectedIds, "read")} />
              <Button size="icon-sm" variant="ghost" icon={Mail} title="Als ungelesen markieren" aria-label="Als ungelesen markieren" onClick={() => onAction(selectedIds, "unread")} />
              <Button size="icon-sm" variant="ghost" icon={Star} title="Markieren" aria-label="Markieren" onClick={() => onAction(selectedIds, "flag")} />
              {archive && currentBox?.id !== archive.id && (
                <Button size="icon-sm" variant="ghost" icon={Archive} title="Archivieren" aria-label="Archivieren" onClick={() => onAction(selectedIds, "move", archive.id)} />
              )}
              <MoveMenu mailboxes={mailboxes} currentId={currentBox?.id} onMove={(id) => onAction(selectedIds, "move", id)} />
              {inJunk ? (
                <Button size="icon-sm" variant="ghost" icon={ShieldCheck} title="Kein Spam" aria-label="Kein Spam" onClick={() => onAction(selectedIds, "notspam")} />
              ) : (
                <Button size="icon-sm" variant="ghost" icon={ShieldX} title="Als Spam markieren" aria-label="Als Spam markieren" onClick={() => onAction(selectedIds, "spam")} />
              )}
              <Button size="icon-sm" variant="danger-ghost" icon={Trash2} title="Löschen" aria-label="Löschen" onClick={() => onAction(selectedIds, "delete")} />
            </>
          )}
        </div>
      )}

      {/* List */}
      <div className="min-h-0 flex-1 overflow-y-auto">
        {loading ? (
          <ListSkeleton />
        ) : error ? (
          <ErrorState error={error} onRetry={onRetry} />
        ) : items.length === 0 ? (
          <EmptyState
            icon={params.q ? Search : Inbox}
            title={params.q ? "Keine Treffer" : params.unread || params.important ? "Nichts gefunden" : "Keine Nachrichten"}
            description={params.q ? `Für „${params.q}“ wurde nichts gefunden.` : params.unread ? "Alles gelesen. Sehr gut!" : "Dieser Ordner ist leer."}
          />
        ) : (
          <ul role="listbox" aria-label="Nachrichten">
            {items.map((m) => (
              <MessageRow
                key={m.id}
                m={m}
                active={activeId === m.id}
                checked={selected.has(m.id)}
                selectionMode={selected.size > 0}
                onCheck={() => toggle(m.id)}
                onOpen={() => onOpen(m.id)}
                onFlag={() => onToggleFlag(m)}
                showSummary={aiSummaries}
              />
            ))}
          </ul>
        )}
      </div>
      {!semantic && total > limit && (
        <Pagination total={total} limit={limit} offset={offset} onChange={onOffset} className="shrink-0 border-t border-zinc-200 px-3 py-2 text-xs dark:border-zinc-800" />
      )}
    </div>
  );
});

function FilterChip({ active, onClick, children }: { active: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      className={cn(
        "inline-flex h-7 items-center gap-1 rounded-full border px-2.5 text-xs font-medium transition",
        active
          ? "border-indigo-200 bg-indigo-50 text-indigo-700 dark:border-indigo-500/40 dark:bg-indigo-500/15 dark:text-indigo-300"
          : "border-zinc-200 text-zinc-600 hover:bg-zinc-50 dark:border-zinc-700 dark:text-zinc-400 dark:hover:bg-zinc-800",
      )}
    >
      {children}
    </button>
  );
}

export function MoveMenu({
  mailboxes,
  currentId,
  onMove,
  label,
}: {
  mailboxes: Mailbox[];
  currentId?: number;
  onMove: (id: number) => void;
  label?: string;
}) {
  return (
    <Dropdown
      trigger={({ toggle }) =>
        label ? (
          <Button size="sm" variant="ghost" icon={FolderInput} onClick={toggle}>
            {label}
          </Button>
        ) : (
          <Button size="icon-sm" variant="ghost" icon={FolderInput} onClick={toggle} title="Verschieben" aria-label="Verschieben" />
        )
      }
      menuClassName="max-h-80 overflow-y-auto"
    >
      {(close) => (
        <>
          <DropdownLabel>Verschieben nach</DropdownLabel>
          {mailboxes
            .filter((m) => m.id !== currentId && m.role !== "drafts")
            .map((m) => (
              <DropdownItem
                key={m.id}
                icon={iconFor(m)}
                onClick={() => {
                  close();
                  onMove(m.id);
                }}
              >
                {mailboxLabel(m)}
              </DropdownItem>
            ))}
        </>
      )}
    </Dropdown>
  );
}

function MessageRow({
  m,
  active,
  checked,
  selectionMode,
  onCheck,
  onOpen,
  onFlag,
  showSummary,
}: {
  m: MessageSummary & { score?: number };
  active: boolean;
  checked: boolean;
  selectionMode: boolean;
  onCheck: () => void;
  onOpen: () => void;
  onFlag: () => void;
  showSummary: boolean;
}) {
  const ref = useRef<HTMLLIElement>(null);
  useEffect(() => {
    if (active) ref.current?.scrollIntoView({ block: "nearest" });
  }, [active]);

  const unread = !m.seen;
  const fromLabel = m.from.name || m.from.email || "(unbekannt)";

  return (
    <li
      ref={ref}
      role="option"
      aria-selected={active}
      onClick={onOpen}
      className={cn(
        "group relative flex cursor-pointer gap-3 border-b border-zinc-100 px-3 py-3 transition-colors dark:border-zinc-800/80",
        active
          ? "bg-indigo-50/80 dark:bg-indigo-500/10"
          : checked
            ? "bg-indigo-50/40 dark:bg-indigo-500/5"
            : "hover:bg-zinc-50 dark:hover:bg-zinc-800/40",
      )}
    >
      {active && <span className="absolute inset-y-0 left-0 w-0.5 bg-indigo-600 dark:bg-indigo-400" />}
      {unread && !active && <span className="absolute left-1 top-1/2 h-1.5 w-1.5 -translate-y-1/2 rounded-full bg-indigo-500" aria-label="Ungelesen" />}

      <div className="relative mt-0.5 h-9 w-9 shrink-0" onClick={(e) => e.stopPropagation()}>
        <div className={cn("transition-opacity", (checked || selectionMode) ? "opacity-0" : "group-hover:opacity-0")}>
          <Avatar name={m.from.name} email={m.from.email} />
        </div>
        <label
          className={cn(
            "absolute inset-0 flex cursor-pointer items-center justify-center transition-opacity",
            checked || selectionMode ? "opacity-100" : "opacity-0 group-hover:opacity-100",
          )}
        >
          <Checkbox checked={checked} onChange={onCheck} aria-label="Auswählen" className="h-[18px] w-[18px]" />
        </label>
      </div>

      <div className="min-w-0 flex-1">
        <div className="flex items-baseline gap-2">
          <span className={cn("min-w-0 flex-1 truncate text-sm", unread ? "font-semibold text-zinc-900 dark:text-zinc-50" : "text-zinc-700 dark:text-zinc-300")}>
            {fromLabel}
          </span>
          {m.hasAttachments && <Paperclip className="h-3.5 w-3.5 shrink-0 self-center text-zinc-400" aria-label="Anhang" />}
          <time
            dateTime={m.receivedAt}
            title={formatDateTime(m.receivedAt)}
            className={cn("shrink-0 text-xs tabular-nums", unread ? "font-semibold text-indigo-600 dark:text-indigo-400" : "text-zinc-400")}
          >
            {formatShort(m.receivedAt)}
          </time>
        </div>
        <div className="mt-0.5 flex items-center gap-1.5">
          {m.priority === "high" && <Flame className="h-3.5 w-3.5 shrink-0 text-red-500" aria-label="Hohe Priorität" />}
          <span className={cn("min-w-0 flex-1 truncate text-[13px]", unread ? "font-semibold text-zinc-900 dark:text-zinc-100" : "text-zinc-800 dark:text-zinc-200")}>
            {m.subject || "(kein Betreff)"}
          </span>
          <button
            type="button"
            onClick={(e) => {
              e.stopPropagation();
              onFlag();
            }}
            className={cn(
              "shrink-0 rounded p-0.5 transition",
              m.flagged ? "text-amber-400" : "text-zinc-300 opacity-0 hover:text-amber-400 group-hover:opacity-100 dark:text-zinc-600",
            )}
            aria-label={m.flagged ? "Markierung entfernen" : "Markieren"}
          >
            <Star className="h-4 w-4" fill={m.flagged ? "currentColor" : "none"} />
          </button>
        </div>
        {showSummary && m.summary ? (
          <p className="mt-0.5 flex items-start gap-1 text-xs text-violet-700 dark:text-violet-300">
            <Sparkles className="mt-0.5 h-3 w-3 shrink-0" />
            <span className="line-clamp-1">{m.summary}</span>
          </p>
        ) : (
          <p className="mt-0.5 line-clamp-1 text-xs text-zinc-500 dark:text-zinc-400">{m.preview}</p>
        )}
        {(m.category || m.tags.length > 0 || m.score !== undefined || m.spamScore >= 5) && (
          <div className="mt-1.5 flex flex-wrap gap-1">
            {m.category && <Badge tone={toneFor(m.category)}>{m.category}</Badge>}
            {m.tags.slice(0, 3).map((t) => (
              <Badge key={t}>#{t}</Badge>
            ))}
            {m.spamScore >= 5 && <Badge tone="red">Spam {m.spamScore.toFixed(1)}</Badge>}
            {m.score !== undefined && <Badge tone="purple">{Math.round(m.score * 100)} % Relevanz</Badge>}
          </div>
        )}
      </div>
    </li>
  );
}

function ListSkeleton() {
  return (
    <div>
      {Array.from({ length: 9 }, (_, i) => (
        <div key={i} className="flex gap-3 border-b border-zinc-100 px-3 py-3.5 dark:border-zinc-800">
          <Skeleton className="h-9 w-9 rounded-full" />
          <div className="flex-1 space-y-2">
            <div className="flex justify-between gap-4">
              <Skeleton className="h-3.5 w-1/3" />
              <Skeleton className="h-3 w-10" />
            </div>
            <Skeleton className="h-3.5 w-3/4" />
            <Skeleton className="h-3 w-full" />
          </div>
        </div>
      ))}
    </div>
  );
}
