import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { aiApi, mailApi } from "@/api/endpoints";
import type { BulkAction, BulkRequest, MessageDetail, MessageSummary, Paged } from "@/api/types";
import { useMe } from "@/lib/auth";
import { qk, useAiStatus, useMailboxes } from "@/lib/queries";
import { cn, isTypingTarget } from "@/lib/utils";
import { useToast } from "@/components/ui";
import { useComposer } from "./Composer";
import { MessageList } from "./MessageList";
import { MessageReader } from "./MessageReader";
import {
  forwardInit,
  mailboxLabel,
  plainBody,
  rememberCategories,
  replyInit,
  sortMailboxes,
  useKnownCategories,
  useMailParams,
} from "./mailUtils";

const LIMIT = 50;

const actionMessages: Record<BulkAction, (n: number) => string | null> = {
  read: () => null,
  unread: () => null,
  flag: () => null,
  unflag: () => null,
  move: (n) => (n === 1 ? "Nachricht verschoben" : `${n} Nachrichten verschoben`),
  delete: (n) => (n === 1 ? "Nachricht gelöscht" : `${n} Nachrichten gelöscht`),
  spam: (n) => (n === 1 ? "Als Spam markiert" : `${n} Nachrichten als Spam markiert`),
  notspam: (n) => (n === 1 ? "Als „kein Spam“ markiert" : `${n} Nachrichten als „kein Spam“ markiert`),
};

const removingActions: BulkAction[] = ["move", "delete", "spam", "notspam"];

export default function MailPage() {
  const me = useMe();
  const [p, update] = useMailParams();
  const mailboxes = useMailboxes();
  const ai = useAiStatus();
  const composer = useComposer();
  const qc = useQueryClient();
  const toast = useToast();
  const categories = useKnownCategories();
  const searchRef = useRef<HTMLInputElement>(null);

  const sorted = useMemo(() => sortMailboxes(mailboxes.data ?? []), [mailboxes.data]);
  const inbox = sorted.find((m) => m.role === "inbox");
  const filterMode = !!(p.category || p.tag || p.q);
  const mailboxId = p.box ?? (filterMode ? undefined : inbox?.id);
  const currentBox = sorted.find((m) => m.id === mailboxId);
  const semanticAvailable = ai.enabled && ai.features.semanticSearch;
  const semantic = p.mode === "semantic" && !!p.q && semanticAvailable;

  const [offset, setOffset] = useState(0);
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const filterKey = JSON.stringify([mailboxId, p.q, p.tag, p.category, p.unread, p.mode]);
  useEffect(() => {
    setOffset(0);
    setSelected(new Set());
  }, [filterKey]);

  const ready = mailboxId !== undefined || filterMode || (!mailboxes.isLoading && sorted.length === 0) || mailboxes.isError;

  const list = useQuery({
    queryKey: [...qk.messages, { mailboxId, q: p.q, tag: p.tag, category: p.category, unread: p.unread, offset, semantic }],
    queryFn: async ({ signal }): Promise<Paged<MessageSummary & { score?: number }>> => {
      if (semantic) {
        const res = await aiApi.search(p.q, LIMIT, signal);
        return { total: res.items.length, items: res.items };
      }
      return mailApi.messages(
        {
          mailbox: mailboxId,
          q: p.q || undefined,
          tag: p.tag ?? undefined,
          category: p.category ?? undefined,
          unread: p.unread || undefined,
          limit: LIMIT,
          offset,
        },
        signal,
      );
    },
    enabled: ready,
    placeholderData: keepPreviousData,
    refetchInterval: 60_000,
  });

  useEffect(() => {
    if (list.data) rememberCategories(list.data.items.map((m) => m.category));
  }, [list.data]);

  const items = useMemo(() => {
    const raw = list.data?.items ?? [];
    return p.important ? raw.filter((m) => m.flagged || m.priority === "high") : raw;
  }, [list.data, p.important]);

  const title = p.q
    ? semantic
      ? "Semantische Suche"
      : "Suchergebnisse"
    : p.category
      ? p.category
      : p.tag
        ? `#${p.tag}`
        : currentBox
          ? mailboxLabel(currentBox)
          : "Nachrichten";

  // --- cache helpers -------------------------------------------------------

  const patchCached = useCallback(
    (ids: number[], patch: Partial<MessageSummary>) => {
      qc.setQueriesData<Paged<MessageSummary>>({ queryKey: qk.messages }, (old) =>
        old ? { ...old, items: old.items.map((m) => (ids.includes(m.id) ? { ...m, ...patch } : m)) } : old,
      );
      for (const id of ids) {
        qc.setQueryData<MessageDetail>(qk.message(id), (old) => (old ? { ...old, ...patch } : old));
      }
      qc.setQueriesData<MessageDetail[]>({ queryKey: ["mail", "thread"] }, (old) =>
        old ? old.map((m) => (ids.includes(m.id) ? { ...m, ...patch } : m)) : old,
      );
    },
    [qc],
  );

  const refreshAll = useCallback(() => {
    void qc.invalidateQueries({ queryKey: qk.messages });
    void qc.invalidateQueries({ queryKey: qk.mailboxes });
    void qc.invalidateQueries({ queryKey: qk.tags });
  }, [qc]);

  // --- mark as seen when opened -------------------------------------------

  const markedSeen = useRef(new Set<number>());
  const detailForOpen = useQuery({
    queryKey: qk.message(p.msg ?? -1),
    queryFn: () => mailApi.message(p.msg!),
    enabled: p.msg !== null,
  });
  useEffect(() => {
    const d = detailForOpen.data;
    if (!d || d.seen || markedSeen.current.has(d.id)) return;
    markedSeen.current.add(d.id);
    patchCached([d.id], { seen: true });
    mailApi
      .updateMessage(d.id, { seen: true })
      .then(() => void qc.invalidateQueries({ queryKey: qk.mailboxes }))
      .catch(() => markedSeen.current.delete(d.id));
  }, [detailForOpen.data, patchCached, qc]);

  // --- actions --------------------------------------------------------------

  const bulk = useMutation({
    mutationFn: (req: BulkRequest) => mailApi.bulk(req),
    onMutate: (req) => {
      if (req.action === "read" || req.action === "unread") patchCached(req.ids, { seen: req.action === "read" });
      if (req.action === "flag" || req.action === "unflag") patchCached(req.ids, { flagged: req.action === "flag" });
    },
    onSuccess: (res, req) => {
      const msg = actionMessages[req.action](res?.count ?? req.ids.length);
      if (msg) toast.success(msg);
    },
    onError: (err) => toast.error(err, "Aktion fehlgeschlagen"),
    onSettled: (_, __, req) => {
      refreshAll();
      if (removingActions.includes(req.action)) {
        req.ids.forEach((id) => qc.removeQueries({ queryKey: qk.message(id) }));
        void qc.invalidateQueries({ queryKey: ["mail", "thread"] });
      }
    },
  });

  const runAction = useCallback(
    (ids: number[], action: BulkAction, targetMailbox?: number) => {
      if (ids.length === 0) return;
      if (removingActions.includes(action) && p.msg !== null && ids.includes(p.msg)) {
        const idx = items.findIndex((m) => m.id === p.msg);
        const rest = items.filter((m) => !ids.includes(m.id));
        const next = rest[Math.min(Math.max(idx, 0), rest.length - 1)];
        update({ msg: next?.id ?? null }, true);
      }
      if (removingActions.includes(action)) setSelected((s) => new Set([...s].filter((id) => !ids.includes(id))));
      bulk.mutate({ ids, action, ...(targetMailbox !== undefined ? { mailboxId: targetMailbox } : {}) });
    },
    [bulk, items, p.msg, update],
  );

  const toggleFlag = useCallback(
    (m: MessageSummary) => {
      patchCached([m.id], { flagged: !m.flagged });
      mailApi.updateMessage(m.id, { flagged: !m.flagged }).catch((err) => {
        patchCached([m.id], { flagged: m.flagged });
        toast.error(err, "Markierung fehlgeschlagen");
      });
    },
    [patchCached, toast],
  );

  const archiveBox = sorted.find((m) => m.role === "archive");

  const openMessage = useCallback(
    (id: number) => {
      if (currentBox?.role !== "drafts") {
        update({ msg: id });
        return;
      }
      // Drafts open in the composer instead of the reader.
      qc.fetchQuery({ queryKey: qk.message(id), queryFn: () => mailApi.message(id) })
        .then((d) =>
          composer.open({
            to: d.to.map((a) => a.email),
            cc: d.cc.map((a) => a.email),
            subject: d.subject,
            text: plainBody(d),
            draftId: d.id,
          }),
        )
        .catch((err) => toast.error(err, "Entwurf konnte nicht geöffnet werden"));
    },
    [currentBox, update, qc, composer, toast],
  );

  const reply = useCallback((m: MessageDetail, all: boolean) => composer.open(replyInit(m, all, me.email)), [composer, me.email]);
  const forward = useCallback((m: MessageDetail) => composer.open(forwardInit(m)), [composer]);
  const aiDraft = useCallback(
    (m: MessageDetail, d: { subject: string; body: string }) => {
      const base = replyInit(m, false, me.email);
      composer.open({ ...base, subject: d.subject || base.subject, text: `${d.body}${base.text ?? ""}` });
      toast.info("Automatisch erstellter Entwurf", "Kann Fehler enthalten – bitte vor dem Senden prüfen.");
    },
    [composer, me.email, toast],
  );

  // --- keyboard shortcuts ---------------------------------------------------

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.ctrlKey || e.metaKey || e.altKey) return;
      if (isTypingTarget(e.target) || composer.isOpen || document.body.dataset.modalOpen) return;
      const idx = items.findIndex((m) => m.id === p.msg);
      const current = p.msg !== null ? qc.getQueryData<MessageDetail>(qk.message(p.msg)) : undefined;
      switch (e.key) {
        case "c":
          e.preventDefault();
          composer.open();
          break;
        case "r":
          if (current) {
            e.preventDefault();
            reply(current, false);
          }
          break;
        case "j": {
          e.preventDefault();
          const next = items[Math.min(items.length - 1, idx + 1)];
          if (next) update({ msg: next.id }, true);
          break;
        }
        case "k": {
          e.preventDefault();
          const prev = items[Math.max(0, idx === -1 ? 0 : idx - 1)];
          if (prev) update({ msg: prev.id }, true);
          break;
        }
        case "e": {
          const ids = selected.size ? [...selected] : p.msg !== null ? [p.msg] : [];
          if (ids.length === 0) break;
          e.preventDefault();
          if (!archiveBox) toast.error("Kein Archiv-Ordner", "Es gibt keinen Ordner mit der Rolle „Archiv“.");
          else runAction(ids, "move", archiveBox.id);
          break;
        }
        case "#": {
          const ids = selected.size ? [...selected] : p.msg !== null ? [p.msg] : [];
          if (ids.length === 0) break;
          e.preventDefault();
          runAction(ids, "delete");
          break;
        }
        case "/":
          e.preventDefault();
          searchRef.current?.focus();
          break;
        case "Escape":
          if (selected.size) setSelected(new Set());
          break;
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [items, p.msg, selected, composer, reply, runAction, archiveBox, update, qc, toast]);

  const aiCategories = ai.enabled && ai.features.classify ? categories : [];

  return (
    <div className="flex min-w-0 flex-1">
      <section
        className={cn(
          "flex w-full min-w-0 flex-col border-r border-zinc-200 bg-white dark:border-zinc-800 dark:bg-zinc-900 md:w-[320px] md:shrink-0 lg:w-[340px] xl:w-[380px] 2xl:w-[420px]",
          p.msg !== null && "hidden md:flex",
        )}
      >
        <MessageList
          ref={searchRef}
          title={title}
          params={p}
          onParams={(patch) => update(patch)}
          items={items}
          total={list.data?.total ?? 0}
          offset={offset}
          limit={LIMIT}
          onOffset={setOffset}
          loading={!ready || list.isLoading}
          fetching={list.isFetching}
          error={list.error}
          onRetry={() => {
            void list.refetch();
            void mailboxes.refetch();
          }}
          selected={selected}
          onSelected={setSelected}
          activeId={p.msg}
          onOpen={openMessage}
          onAction={runAction}
          onToggleFlag={toggleFlag}
          mailboxes={sorted}
          currentBox={currentBox}
          aiSearch={semanticAvailable}
          aiSummaries={ai.enabled && ai.features.summarize}
          categories={aiCategories}
          semantic={semantic}
        />
      </section>
      <section className={cn("min-w-0 flex-1 bg-zinc-50 dark:bg-zinc-950", p.msg === null ? "hidden md:flex" : "flex")}>
        <MessageReader
          messageId={p.msg}
          mailboxes={sorted}
          onBack={() => update({ msg: null })}
          onAction={runAction}
          onToggleFlag={toggleFlag}
          onReply={reply}
          onForward={forward}
          onAiDraft={aiDraft}
        />
      </section>
    </div>
  );
}
