import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Archive,
  ArrowLeft,
  Bug,
  Download,
  Ellipsis,
  FileArchive,
  FileImage,
  FileText,
  File as FileIcon,
  FileSpreadsheet,
  FileUp,
  Flame,
  Forward,
  ImageOff,
  Mail,
  MailOpen,
  Reply,
  ReplyAll,
  ShieldCheck,
  ShieldX,
  Star,
  Trash2,
  TriangleAlert,
  Code,
  UserX,
} from "lucide-react";
import { dmsApi, mailApi } from "@/api/endpoints";
import type { Address, Attachment, AuthResults, BulkAction, Mailbox, MessageDetail } from "@/api/types";
import { qk, useAiStatus } from "@/lib/queries";
import { formatBytes, formatDateTime, formatShort } from "@/lib/format";
import { cn } from "@/lib/utils";
import {
  Avatar,
  Badge,
  Button,
  Dropdown,
  DropdownItem,
  DropdownSeparator,
  EmptyState,
  ErrorState,
  Kbd,
  Skeleton,
  SkeletonText,
  toneFor,
  useConfirm,
  useToast,
  type BadgeTone,
} from "@/components/ui";
import { AiPanel } from "./AiPanel";
import { MoveMenu } from "./MessageList";
import { sanitizeEmail } from "./sanitize";

export interface ReaderActions {
  onBack: () => void;
  onAction: (ids: number[], action: BulkAction, mailboxId?: number) => void;
  onToggleFlag: (m: MessageDetail) => void;
  onReply: (m: MessageDetail, all: boolean) => void;
  onForward: (m: MessageDetail) => void;
  onAiDraft: (m: MessageDetail, draft: { subject: string; body: string }) => void;
}

export function MessageReader({
  messageId,
  mailboxes,
  ...actions
}: { messageId: number | null; mailboxes: Mailbox[] } & ReaderActions) {
  if (messageId === null) {
    return (
      <div className="flex h-full flex-1 items-center justify-center">
        <EmptyState
          icon={MailOpen}
          title="Keine Nachricht ausgewählt"
          description={
            <span className="mt-2 inline-flex flex-wrap items-center justify-center gap-x-3 gap-y-1.5 text-xs">
              <span className="inline-flex items-center gap-1">
                <Kbd>j</Kbd>/<Kbd>k</Kbd> navigieren
              </span>
              <span className="inline-flex items-center gap-1">
                <Kbd>c</Kbd> neu
              </span>
              <span className="inline-flex items-center gap-1">
                <Kbd>r</Kbd> antworten
              </span>
              <span className="inline-flex items-center gap-1">
                <Kbd>e</Kbd> archivieren
              </span>
              <span className="inline-flex items-center gap-1">
                <Kbd>#</Kbd> löschen
              </span>
              <span className="inline-flex items-center gap-1">
                <Kbd>/</Kbd> suchen
              </span>
            </span>
          }
        />
      </div>
    );
  }
  return <ReaderContent key={messageId} messageId={messageId} mailboxes={mailboxes} {...actions} />;
}

function ReaderContent({
  messageId,
  mailboxes,
  onBack,
  onAction,
  onToggleFlag,
  onReply,
  onForward,
  onAiDraft,
}: { messageId: number; mailboxes: Mailbox[] } & ReaderActions) {
  const detail = useQuery({ queryKey: qk.message(messageId), queryFn: () => mailApi.message(messageId) });
  const threadId = detail.data?.threadId;
  const thread = useQuery({
    queryKey: qk.thread(threadId ?? -1),
    queryFn: () => mailApi.thread(threadId!),
    enabled: threadId !== undefined,
  });
  const toast = useToast();
  const qc = useQueryClient();
  const confirm = useConfirm();

  if (detail.isLoading) {
    return (
      <div className="flex-1 space-y-6 p-6">
        <Skeleton className="h-7 w-2/3" />
        <div className="flex gap-3">
          <Skeleton className="h-10 w-10 rounded-full" />
          <div className="flex-1 space-y-2">
            <Skeleton className="h-4 w-1/3" />
            <Skeleton className="h-3 w-1/4" />
          </div>
        </div>
        <SkeletonText lines={8} />
      </div>
    );
  }
  if (detail.error || !detail.data) {
    return (
      <div className="flex flex-1 items-center justify-center">
        <ErrorState error={detail.error} onRetry={() => void detail.refetch()} />
      </div>
    );
  }

  const m = detail.data;
  const box = mailboxes.find((b) => b.id === m.mailboxId);
  const archive = mailboxes.find((b) => b.role === "archive");
  const inJunk = box?.role === "junk";
  const messages = thread.data && thread.data.length > 0 ? thread.data : [m];

  const doUnsubscribe = useMutation({
    mutationFn: (id: number) => mailApi.unsubscribe(id),
    onSuccess: () => {
      toast.success("Abgemeldet", "Der Absender wurde in „Newsletter“ einsortiert.");
      void qc.invalidateQueries({ queryKey: qk.messages });
    },
    onError: (err) => toast.error(err, "Abmelden fehlgeschlagen"),
  });

  return (
    <div className="flex h-full min-w-0 flex-1 flex-col">
      {/* Toolbar */}
      <div className="flex h-12 shrink-0 items-center gap-1 border-b border-zinc-200 bg-white px-2 dark:border-zinc-800 dark:bg-zinc-900 sm:px-3">
        <Button size="icon-sm" variant="ghost" icon={ArrowLeft} onClick={onBack} aria-label="Zurück zur Liste" className="md:hidden" />
        {archive && box?.id !== archive.id && (
          <Button size="sm" variant="ghost" icon={Archive} onClick={() => onAction([m.id], "move", archive.id)} title="Archivieren (e)">
            <span className="hidden xl:inline">Archivieren</span>
          </Button>
        )}
        {inJunk ? (
          <Button size="sm" variant="ghost" icon={ShieldCheck} onClick={() => onAction([m.id], "notspam")}>
            <span className="hidden xl:inline">Kein Spam</span>
          </Button>
        ) : (
          <Button size="sm" variant="ghost" icon={ShieldX} onClick={() => onAction([m.id], "spam")} title="Als Spam markieren">
            <span className="hidden xl:inline">Spam</span>
          </Button>
        )}
        <Button size="sm" variant="ghost" icon={Trash2} onClick={() => onAction([m.id], "delete")} title="Löschen (#)">
          <span className="hidden xl:inline">Löschen</span>
        </Button>
        <MoveMenu mailboxes={mailboxes} currentId={m.mailboxId} onMove={(id) => onAction([m.id], "move", id)} />
        <Button
          size="icon-sm"
          variant="ghost"
          icon={Star}
          onClick={() => onToggleFlag(m)}
          aria-label={m.flagged ? "Markierung entfernen" : "Markieren"}
          className={cn(m.flagged && "text-amber-500 [&>svg]:fill-current")}
        />
        <Dropdown
          trigger={({ toggle }) => <Button size="icon-sm" variant="ghost" icon={Ellipsis} onClick={toggle} aria-label="Weitere Aktionen" />}
        >
          {(close) => (
            <>
              <DropdownItem icon={Mail} onClick={() => { close(); onAction([m.id], "unread"); }}>
                Als ungelesen markieren
              </DropdownItem>
              <DropdownSeparator />
              <DropdownItem icon={Code} onClick={() => { close(); window.open(mailApi.rawUrl(m.id), "_blank", "noopener"); }}>
                Original anzeigen
              </DropdownItem>
              {m.unsubscribe?.header && (
                <DropdownItem
                  icon={UserX}
                  onClick={() => {
                    close();
                    void confirm({
                      title: "Von Newsletter abmelden?",
                      message:
                        "Postwerk ruft den Ein-Klick-Abmelde-Link des Absenders auf und sortiert ihn in den Ordner „Newsletter“ ein.",
                      confirmLabel: "Abmelden",
                    }).then((ok) => ok && doUnsubscribe.mutate(m.id));
                  }}
                >
                  Abmelden (Newsletter)
                </DropdownItem>
              )}
            </>
          )}
        </Dropdown>
        <div className="ml-auto flex items-center gap-1">
          <Button size="sm" variant="ghost" icon={Reply} onClick={() => onReply(m, false)} title="Antworten (r)">
            <span className="hidden lg:inline">Antworten</span>
          </Button>
          <Button size="sm" variant="ghost" icon={ReplyAll} onClick={() => onReply(m, true)} title="Allen antworten">
            <span className="hidden 2xl:inline">Allen antworten</span>
          </Button>
          <Button size="sm" variant="ghost" icon={Forward} onClick={() => onForward(m)} title="Weiterleiten">
            <span className="hidden 2xl:inline">Weiterleiten</span>
          </Button>
        </div>
      </div>

      {/* Content */}
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto max-w-4xl space-y-4 px-4 py-5 sm:px-6">
          <div>
            <h1 className="text-xl font-semibold leading-snug tracking-tight text-zinc-900 dark:text-zinc-50">{m.subject || "(kein Betreff)"}</h1>
            <div className="mt-2 flex flex-wrap gap-1.5">
              {box && <Badge>{box.role ? { inbox: "Posteingang", sent: "Gesendet", drafts: "Entwürfe", trash: "Papierkorb", junk: "Spam", archive: "Archiv" }[box.role] : box.name}</Badge>}
              {m.priority === "high" && (
                <Badge tone="red" icon={Flame}>
                  Hohe Priorität
                </Badge>
              )}
              {m.aiSuspicious && (
                <Badge tone="amber" icon={TriangleAlert} title="Die Nachricht enthält möglicherweise versteckte Anweisungen an die KI. Automatische Ablage wurde übersprungen.">
                  Verdacht auf Prompt-Injection
                </Badge>
              )}
              {m.category && <Badge tone={toneFor(m.category)}>{m.category}</Badge>}
              {m.tags.map((t) => (
                <Badge key={t}>#{t}</Badge>
              ))}
              {messages.length > 1 && <Badge tone="indigo">{messages.length} Nachrichten im Verlauf</Badge>}
            </div>
          </div>

          <AiPanel message={m} threadSize={messages.length} onDraft={(d) => onAiDraft(m, d)} />

          <ThreadView messages={messages} selectedId={m.id} onReply={onReply} onForward={onForward} />
        </div>
      </div>
    </div>
  );
}

function ThreadView({
  messages,
  selectedId,
  onReply,
  onForward,
}: {
  messages: MessageDetail[];
  selectedId: number;
  onReply: (m: MessageDetail, all: boolean) => void;
  onForward: (m: MessageDetail) => void;
}) {
  const lastId = messages[messages.length - 1]?.id;
  const [expanded, setExpanded] = useState<Set<number>>(() => new Set([selectedId, lastId]));
  const [showAll, setShowAll] = useState(messages.length <= 4);
  const selectedRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (messages.length > 1 && selectedId !== lastId) selectedRef.current?.scrollIntoView({ block: "start" });
  }, [messages.length]);

  const toggle = (id: number) =>
    setExpanded((s) => {
      const n = new Set(s);
      if (n.has(id)) n.delete(id);
      else n.add(id);
      return n;
    });

  // Collapse long middle sections like most mail clients do.
  const hiddenCount = showAll ? 0 : Math.max(0, messages.length - 3);
  const visible = showAll ? messages : [messages[0], ...messages.slice(messages.length - 2)];

  return (
    <div className="space-y-3">
      {visible.map((msg, idx) => (
        <div key={msg.id} ref={msg.id === selectedId ? selectedRef : undefined}>
          {idx === 1 && hiddenCount > 0 && (
            <button
              type="button"
              onClick={() => setShowAll(true)}
              className="mb-3 flex w-full items-center gap-3 text-xs font-medium text-zinc-500 hover:text-indigo-600 dark:text-zinc-400"
            >
              <span className="h-px flex-1 bg-zinc-200 dark:bg-zinc-800" />
              {hiddenCount} weitere Nachrichten anzeigen
              <span className="h-px flex-1 bg-zinc-200 dark:bg-zinc-800" />
            </button>
          )}
          <MessageCard
            m={msg}
            expanded={expanded.has(msg.id) || messages.length === 1}
            collapsible={messages.length > 1}
            onToggle={() => toggle(msg.id)}
            onReply={onReply}
            onForward={onForward}
          />
        </div>
      ))}
    </div>
  );
}

function authTone(v: string): BadgeTone {
  const s = v.toLowerCase();
  if (s === "pass") return "green";
  if (s === "fail" || s === "permerror" || s === "softfail") return "red";
  if (s === "temperror" || s === "neutral") return "amber";
  return "neutral";
}

function AuthBadges({ auth }: { auth: AuthResults }) {
  const entries: [string, string][] = [
    ["SPF", auth.spf],
    ["DKIM", auth.dkim],
    ["DMARC", auth.dmarc],
  ];
  return (
    <div className="flex flex-wrap gap-1">
      {entries.map(([k, v]) => (
        <Badge key={k} tone={authTone(v || "none")} title={`${k}: ${v || "none"}`} className="text-[10px]">
          {k} {v || "none"}
        </Badge>
      ))}
    </div>
  );
}

function AddressList({ label, list }: { label: string; list: Address[] }) {
  if (list.length === 0) return null;
  return (
    <div className="truncate">
      <span className="text-zinc-400">{label} </span>
      {list.map((a, i) => (
        <span key={`${a.email}-${i}`} title={a.email}>
          {i > 0 && ", "}
          {a.name || a.email}
        </span>
      ))}
    </div>
  );
}

function MessageCard({
  m,
  expanded,
  collapsible,
  onToggle,
  onReply,
  onForward,
}: {
  m: MessageDetail;
  expanded: boolean;
  collapsible: boolean;
  onToggle: () => void;
  onReply: (m: MessageDetail, all: boolean) => void;
  onForward: (m: MessageDetail) => void;
}) {
  if (!expanded) {
    return (
      <button
        type="button"
        onClick={onToggle}
        className="flex w-full items-center gap-3 rounded-xl border border-zinc-200 bg-white px-4 py-3 text-left shadow-card transition hover:border-zinc-300 dark:border-zinc-800 dark:bg-zinc-900 dark:hover:border-zinc-700"
      >
        <Avatar name={m.from.name} email={m.from.email} size="sm" />
        <span className={cn("w-40 shrink-0 truncate text-sm", m.seen ? "text-zinc-700 dark:text-zinc-300" : "font-semibold text-zinc-900 dark:text-zinc-50")}>
          {m.from.name || m.from.email}
        </span>
        <span className="min-w-0 flex-1 truncate text-sm text-zinc-500 dark:text-zinc-400">{m.preview}</span>
        <span className="shrink-0 text-xs text-zinc-400">{formatShort(m.receivedAt)}</span>
      </button>
    );
  }

  return (
    <article className="overflow-hidden rounded-xl border border-zinc-200 bg-white shadow-card dark:border-zinc-800 dark:bg-zinc-900">
      <header
        className={cn("flex flex-wrap items-start gap-3 px-4 py-3.5 sm:px-5", collapsible && "cursor-pointer")}
        onClick={collapsible ? onToggle : undefined}
      >
        <Avatar name={m.from.name} email={m.from.email} size="md" />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-baseline gap-x-2">
            <span className="font-semibold text-zinc-900 dark:text-zinc-50">{m.from.name || m.from.email}</span>
            {m.from.name && <span className="truncate text-xs text-zinc-500 dark:text-zinc-400">&lt;{m.from.email}&gt;</span>}
          </div>
          <div className="mt-0.5 space-y-0.5 text-xs text-zinc-600 dark:text-zinc-400">
            <AddressList label="an" list={m.to} />
            <AddressList label="cc" list={m.cc} />
          </div>
        </div>
        <div className="flex flex-col items-end gap-1.5" onClick={(e) => e.stopPropagation()}>
          <div className="flex items-center gap-1">
            <time className="text-xs text-zinc-500 dark:text-zinc-400" dateTime={m.receivedAt}>
              {formatDateTime(m.receivedAt)}
            </time>
            {collapsible && (
              <>
                <Button size="icon-sm" variant="ghost" icon={Reply} onClick={() => onReply(m, false)} aria-label="Antworten" />
                <Button size="icon-sm" variant="ghost" icon={Forward} onClick={() => onForward(m)} aria-label="Weiterleiten" />
              </>
            )}
          </div>
          <div className="flex flex-wrap justify-end gap-1">
            <AuthBadges auth={m.auth} />
            <Badge tone={m.spamScore >= 5 ? "red" : m.spamScore >= 2 ? "amber" : "neutral"} title="Spam-Score" className="text-[10px]">
              Score {m.spamScore.toFixed(1)}
            </Badge>
          </div>
        </div>
      </header>

      {m.virus && (
        <div className="mx-4 mb-3 flex items-start gap-3 rounded-lg border border-red-200 bg-red-50 px-3 py-2.5 text-sm text-red-800 dark:border-red-500/30 dark:bg-red-500/10 dark:text-red-200 sm:mx-5">
          <Bug className="mt-0.5 h-4 w-4 shrink-0" />
          <div>
            <div className="font-semibold">Virus erkannt: {m.virus}</div>
            <div className="text-xs opacity-80">Öffne keine Anhänge und klicke keine Links in dieser Nachricht.</div>
          </div>
        </div>
      )}
      {!m.virus && m.spamScore >= 5 && (
        <div className="mx-4 mb-3 flex items-center gap-2 rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-800 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200 sm:mx-5">
          <TriangleAlert className="h-4 w-4 shrink-0" /> Diese Nachricht wurde als wahrscheinlicher Spam eingestuft.
        </div>
      )}

      <div className="border-t border-zinc-100 dark:border-zinc-800">
        {m.htmlBody ? (
          <HtmlBody html={m.htmlBody} />
        ) : (
          <div className="whitespace-pre-wrap break-words px-4 py-4 text-[14px] leading-relaxed text-zinc-800 dark:text-zinc-200 sm:px-5">
            {m.textBody || <span className="italic text-zinc-400">(kein Inhalt)</span>}
          </div>
        )}
      </div>

      {m.attachments.length > 0 && <Attachments message={m} />}
    </article>
  );
}

function HtmlBody({ html }: { html: string }) {
  const [allowRemote, setAllowRemote] = useState(false);
  const { doc, blocked } = useMemo(() => sanitizeEmail(html, allowRemote), [html, allowRemote]);
  const frameRef = useRef<HTMLIFrameElement>(null);
  const [height, setHeight] = useState(240);

  const observerRef = useRef<ResizeObserver | null>(null);

  useEffect(() => () => observerRef.current?.disconnect(), []);

  const onLoad = () => {
    const frame = frameRef.current;
    const d = frame?.contentDocument;
    if (!frame || !d?.documentElement) return;
    const measure = () => setHeight(Math.max(80, Math.ceil(d.documentElement.scrollHeight)));
    measure();
    observerRef.current?.disconnect();
    if ("ResizeObserver" in window && d.body) {
      observerRef.current = new ResizeObserver(measure);
      observerRef.current.observe(d.body);
    }
  };
  return (
    <div>
      {blocked > 0 && !allowRemote && (
        <div className="flex flex-wrap items-center gap-3 border-b border-zinc-100 bg-zinc-50 px-4 py-2 text-xs text-zinc-600 dark:border-zinc-800 dark:bg-zinc-950/40 dark:text-zinc-400 sm:px-5">
          <ImageOff className="h-4 w-4 shrink-0" />
          <span className="flex-1">Externe Inhalte wurden zum Schutz deiner Privatsphäre blockiert.</span>
          <Button size="xs" onClick={() => setAllowRemote(true)}>
            Bilder laden
          </Button>
        </div>
      )}
      <div className="bg-white">
        <iframe
          ref={frameRef}
          title="Nachrichteninhalt"
          srcDoc={doc}
          sandbox="allow-popups allow-popups-to-escape-sandbox"
          referrerPolicy="no-referrer"
          onLoad={onLoad}
          className="block w-full border-0"
          style={{ height }}
        />
      </div>
    </div>
  );
}

function attachmentIcon(a: Attachment) {
  const t = a.contentType.toLowerCase();
  if (t.startsWith("image/")) return FileImage;
  if (t.includes("pdf") || t.startsWith("text/") || t.includes("word") || t.includes("document")) return FileText;
  if (t.includes("sheet") || t.includes("excel") || t.includes("csv")) return FileSpreadsheet;
  if (t.includes("zip") || t.includes("compressed") || t.includes("tar")) return FileArchive;
  return FileIcon;
}

function Attachments({ message }: { message: MessageDetail }) {
  const { dmsEnabled } = useAiStatus();
  const toast = useToast();
  const [sent, setSent] = useState<Set<number>>(new Set());
  const forward = useMutation({
    mutationFn: (attId: number) => dmsApi.forward(message.id, attId),
    onSuccess: (_, attId) => {
      setSent((s) => new Set(s).add(attId));
      toast.success("An Paperless übergeben");
    },
    onError: (err) => toast.error(err, "Übergabe an Paperless fehlgeschlagen"),
  });
  const total = message.attachments.reduce((s, a) => s + a.size, 0);

  return (
    <div className="border-t border-zinc-100 px-4 py-4 dark:border-zinc-800 sm:px-5">
      <div className="mb-3 flex items-center gap-2 text-xs font-medium text-zinc-500 dark:text-zinc-400">
        {message.attachments.length} {message.attachments.length === 1 ? "Anhang" : "Anhänge"} · {formatBytes(total)}
      </div>
      <div className="grid gap-2.5 sm:grid-cols-2 xl:grid-cols-3">
        {message.attachments.map((a) => {
          const Icon = attachmentIcon(a);
          const url = mailApi.attachmentUrl(message.id, a.id);
          return (
            <div
              key={a.id}
              className="group flex items-center gap-3 rounded-lg border border-zinc-200 bg-zinc-50/60 p-2.5 transition hover:border-zinc-300 hover:bg-white dark:border-zinc-700 dark:bg-zinc-800/40 dark:hover:border-zinc-600 dark:hover:bg-zinc-800"
            >
              <span className="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-white text-indigo-500 ring-1 ring-zinc-200 dark:bg-zinc-900 dark:ring-zinc-700">
                <Icon className="h-5 w-5" />
              </span>
              <div className="min-w-0 flex-1">
                <div className="truncate text-sm font-medium text-zinc-800 dark:text-zinc-100" title={a.filename}>
                  {a.filename}
                </div>
                <div className="text-xs text-zinc-500 dark:text-zinc-400">{formatBytes(a.size)}</div>
              </div>
              <div className="flex shrink-0 gap-0.5">
                <a
                  href={url}
                  download={a.filename}
                  className="rounded-md p-1.5 text-zinc-500 hover:bg-zinc-100 hover:text-zinc-900 dark:text-zinc-400 dark:hover:bg-zinc-700 dark:hover:text-zinc-100"
                  title="Herunterladen"
                  aria-label={`${a.filename} herunterladen`}
                >
                  <Download className="h-4 w-4" />
                </a>
                {dmsEnabled && <button
                  type="button"
                  disabled={sent.has(a.id) || (forward.isPending && forward.variables === a.id)}
                  onClick={() => forward.mutate(a.id)}
                  className="rounded-md p-1.5 text-zinc-500 hover:bg-zinc-100 hover:text-zinc-900 disabled:text-emerald-500 dark:text-zinc-400 dark:hover:bg-zinc-700 dark:hover:text-zinc-100"
                  title={sent.has(a.id) ? "An Paperless gesendet" : "An Paperless senden"}
                  aria-label="An Paperless senden"
                >
                  <FileUp className="h-4 w-4" />
                </button>}
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
}
