import { createContext, useCallback, useContext, useMemo, useRef, useState, type ReactNode } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { File as FileIcon, Paperclip, Save, Send, Trash2, Upload, X } from "lucide-react";
import { contactsApi, mailApi } from "@/api/endpoints";
import type { OutgoingAttachment, SendRequest } from "@/api/types";
import { useMe } from "@/lib/auth";
import { formatBytes } from "@/lib/format";
import { cn, extractEmail, fileToBase64, isEmail } from "@/lib/utils";
import { Button, ChipInput, Kbd, Modal, Textarea, useConfirm, useToast, type ChipSuggestion } from "@/components/ui";
import type { ComposeInit } from "./mailUtils";

interface ComposerApi {
  open: (init?: ComposeInit) => void;
  isOpen: boolean;
}

const ComposerContext = createContext<ComposerApi | null>(null);

export function useComposer(): ComposerApi {
  const ctx = useContext(ComposerContext);
  if (!ctx) throw new Error("useComposer must be used inside ComposerProvider");
  return ctx;
}

interface LocalAttachment extends OutgoingAttachment {
  key: string;
  size: number;
}

interface Draft {
  to: string[];
  cc: string[];
  bcc: string[];
  subject: string;
  text: string;
  attachments: LocalAttachment[];
  inReplyTo?: number;
  draftId?: number;
  mode: "new" | "reply" | "forward";
}

function fromInit(init?: ComposeInit): Draft {
  return {
    to: init?.to ?? [],
    cc: init?.cc ?? [],
    bcc: init?.bcc ?? [],
    subject: init?.subject ?? "",
    text: init?.text ?? "",
    attachments: [],
    inReplyTo: init?.inReplyTo,
    draftId: init?.draftId,
    mode: init?.mode ?? "new",
  };
}

const MAX_TOTAL = 25 * 1024 * 1024;

export function ComposerProvider({ children }: { children: ReactNode }) {
  const [draft, setDraft] = useState<Draft | null>(null);
  const [session, setSession] = useState(0);

  const open = useCallback((init?: ComposeInit) => {
    setDraft(fromInit(init));
    setSession((s) => s + 1);
  }, []);

  const api = useMemo(() => ({ open, isOpen: draft !== null }), [open, draft]);

  return (
    <ComposerContext.Provider value={api}>
      {children}
      {draft && <ComposerPanel key={session} initial={draft} onClose={() => setDraft(null)} />}
    </ComposerContext.Provider>
  );
}

/** Address suggestions from contacts and recently used recipients. */
async function suggestAddresses(q: string, signal: AbortSignal): Promise<ChipSuggestion[]> {
  const list = await contactsApi.suggest(q, 8, signal);
  return list.map((s) => ({
    value: s.email.toLowerCase(),
    label: s.name ? `${s.name} <${s.email}>` : s.email,
    hint: s.source === "contact" ? "Kontakt" : "Zuletzt",
  }));
}

const validateAddress = (v: string) => (isEmail(v) ? null : `„${v}“ ist keine gültige E-Mail-Adresse.`);

function ComposerPanel({ initial, onClose }: { initial: Draft; onClose: () => void }) {
  const me = useMe();
  const toast = useToast();
  const confirm = useConfirm();
  const qc = useQueryClient();
  const [d, setD] = useState<Draft>(initial);
  const [showCc, setShowCc] = useState(initial.cc.length > 0 || initial.bcc.length > 0);
  const [dragging, setDragging] = useState(false);
  const [savedSnapshot, setSavedSnapshot] = useState(() => JSON.stringify(initial));
  const fileRef = useRef<HTMLInputElement>(null);
  const dragDepth = useRef(0);

  const dirty = JSON.stringify(d) !== savedSnapshot;
  const totalSize = d.attachments.reduce((s, a) => s + a.size, 0);

  const payload = (): SendRequest => ({
    to: d.to,
    ...(d.cc.length ? { cc: d.cc } : {}),
    ...(d.bcc.length ? { bcc: d.bcc } : {}),
    subject: d.subject,
    text: d.text,
    ...(d.inReplyTo !== undefined ? { inReplyTo: d.inReplyTo } : {}),
    ...(d.attachments.length
      ? { attachments: d.attachments.map(({ filename, contentType, dataBase64 }) => ({ filename, contentType, dataBase64 })) }
      : {}),
  });

  const refreshMail = () => {
    void qc.invalidateQueries({ queryKey: ["mail", "messages"] });
    void qc.invalidateQueries({ queryKey: ["mail", "mailboxes"] });
  };

  const send = useMutation({
    mutationFn: () => mailApi.send({ ...payload(), ...(d.draftId !== undefined ? { draftId: d.draftId } : {}) }),
    onSuccess: () => {
      toast.success("Nachricht gesendet");
      refreshMail();
      onClose();
    },
    onError: (err) => toast.error(err, "Senden fehlgeschlagen"),
  });

  const saveDraft = useMutation({
    mutationFn: () => mailApi.saveDraft({ ...payload(), ...(d.draftId !== undefined ? { draftId: d.draftId } : {}) }),
    onSuccess: (res) => {
      const next = { ...d, draftId: res.id };
      setD(next);
      setSavedSnapshot(JSON.stringify(next));
      toast.success("Entwurf gespeichert");
      refreshMail();
    },
    onError: (err) => toast.error(err, "Entwurf konnte nicht gespeichert werden"),
  });

  const trySend = () => {
    if (d.to.length + d.cc.length + d.bcc.length === 0) {
      toast.error("Kein Empfänger", "Bitte mindestens einen Empfänger angeben.");
      return;
    }
    if (totalSize > MAX_TOTAL) {
      toast.error("Anhänge zu groß", `Insgesamt höchstens ${formatBytes(MAX_TOTAL)}.`);
      return;
    }
    if (!d.subject.trim()) {
      void confirm({ title: "Ohne Betreff senden?", message: "Die Nachricht hat keinen Betreff.", confirmLabel: "Trotzdem senden" }).then(
        (ok) => ok && send.mutate(),
      );
      return;
    }
    send.mutate();
  };

  const requestClose = async () => {
    if (!dirty) return onClose();
    const ok = await confirm({
      title: "Nachricht verwerfen?",
      message: "Nicht gespeicherte Änderungen gehen verloren. Speichere vorher einen Entwurf, um sie zu behalten.",
      confirmLabel: "Verwerfen",
      danger: true,
    });
    if (ok) onClose();
  };

  const addFiles = async (files: FileList | File[]) => {
    const list = Array.from(files);
    try {
      const added: LocalAttachment[] = [];
      for (const f of list) {
        added.push({
          key: `${f.name}-${f.size}-${Math.random().toString(36).slice(2)}`,
          filename: f.name,
          contentType: f.type || "application/octet-stream",
          dataBase64: await fileToBase64(f),
          size: f.size,
        });
      }
      setD((prev) => ({ ...prev, attachments: [...prev.attachments, ...added] }));
    } catch (err) {
      toast.error(err, "Datei konnte nicht gelesen werden");
    }
  };

  const title = d.mode === "reply" ? "Antworten" : d.mode === "forward" ? "Weiterleiten" : "Neue Nachricht";

  return (
    <Modal open variant="sheet" onClose={() => void requestClose()} dismissible={false} className="max-w-2xl" bodyClassName="p-0">
      <div
        className="relative flex h-full flex-col"
        onDragEnter={(e) => {
          if (!e.dataTransfer.types.includes("Files")) return;
          e.preventDefault();
          dragDepth.current++;
          setDragging(true);
        }}
        onDragOver={(e) => {
          if (e.dataTransfer.types.includes("Files")) e.preventDefault();
        }}
        onDragLeave={() => {
          dragDepth.current = Math.max(0, dragDepth.current - 1);
          if (dragDepth.current === 0) setDragging(false);
        }}
        onDrop={(e) => {
          e.preventDefault();
          dragDepth.current = 0;
          setDragging(false);
          if (e.dataTransfer.files.length) void addFiles(e.dataTransfer.files);
        }}
        onKeyDown={(e) => {
          if ((e.ctrlKey || e.metaKey) && e.key === "Enter") {
            e.preventDefault();
            trySend();
          }
        }}
      >
        <div className="flex items-center justify-between border-b border-zinc-200 px-5 py-3.5 dark:border-zinc-800">
          <div>
            <h2 className="text-base font-semibold text-zinc-900 dark:text-zinc-50">{title}</h2>
            <p className="text-xs text-zinc-500 dark:text-zinc-400">
              Von {me.name ? `${me.name} <${me.email}>` : me.email}
              {d.draftId !== undefined && " · Entwurf gespeichert"}
            </p>
          </div>
          <button
            type="button"
            onClick={() => void requestClose()}
            className="rounded-md p-1.5 text-zinc-400 hover:bg-zinc-100 hover:text-zinc-700 dark:hover:bg-zinc-800 dark:hover:text-zinc-200"
            aria-label="Schließen"
          >
            <X className="h-5 w-5" />
          </button>
        </div>

        <div className="space-y-0 divide-y divide-zinc-100 border-b border-zinc-200 dark:divide-zinc-800 dark:border-zinc-800">
          <AddressRow label="An" value={d.to} onChange={(v) => setD({ ...d, to: v })} autoFocus={d.mode !== "reply" && d.to.length === 0}>
            {!showCc && (
              <button type="button" onClick={() => setShowCc(true)} className="shrink-0 px-2 text-xs font-medium text-zinc-500 hover:text-indigo-600 dark:text-zinc-400">
                Cc/Bcc
              </button>
            )}
          </AddressRow>
          {showCc && (
            <>
              <AddressRow label="Cc" value={d.cc} onChange={(v) => setD({ ...d, cc: v })} />
              <AddressRow label="Bcc" value={d.bcc} onChange={(v) => setD({ ...d, bcc: v })} />
            </>
          )}
          <div className="flex items-center gap-3 px-5 py-1.5">
            <span className="w-10 shrink-0 text-sm text-zinc-500 dark:text-zinc-400">Betreff</span>
            <input
              value={d.subject}
              onChange={(e) => setD({ ...d, subject: e.target.value })}
              className="h-9 min-w-0 flex-1 border-0 bg-transparent text-sm font-medium text-zinc-900 placeholder:text-zinc-400 focus:outline-none focus:ring-0 dark:text-zinc-100"
              placeholder="Worum geht es?"
            />
          </div>
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto">
          <Textarea
            value={d.text}
            onChange={(e) => setD({ ...d, text: e.target.value })}
            data-autofocus={d.mode === "reply" || d.to.length > 0 ? true : undefined}
            onFocus={(e) => {
              if (d.mode === "reply" && e.target.selectionStart === e.target.value.length && e.target.value.startsWith("\n")) {
                e.target.setSelectionRange(0, 0);
              }
            }}
            className="h-full min-h-[18rem] resize-none rounded-none border-0 px-5 py-4 text-[14px] leading-relaxed shadow-none focus:ring-0 dark:bg-transparent"
            placeholder="Nachricht schreiben …"
          />
        </div>

        {d.attachments.length > 0 && (
          <div className="border-t border-zinc-100 px-5 py-3 dark:border-zinc-800">
            <div className="mb-2 flex items-center justify-between text-xs text-zinc-500 dark:text-zinc-400">
              <span>
                {d.attachments.length} {d.attachments.length === 1 ? "Anhang" : "Anhänge"}
              </span>
              <span className={cn(totalSize > MAX_TOTAL && "font-medium text-red-600")}>
                {formatBytes(totalSize)} / {formatBytes(MAX_TOTAL)}
              </span>
            </div>
            <div className="flex flex-wrap gap-2">
              {d.attachments.map((a) => (
                <span
                  key={a.key}
                  className="inline-flex max-w-[16rem] items-center gap-2 rounded-lg border border-zinc-200 bg-zinc-50 py-1.5 pl-2.5 pr-1.5 text-sm dark:border-zinc-700 dark:bg-zinc-800"
                >
                  <FileIcon className="h-4 w-4 shrink-0 text-zinc-400" />
                  <span className="truncate text-zinc-700 dark:text-zinc-200">{a.filename}</span>
                  <span className="shrink-0 text-xs text-zinc-400">{formatBytes(a.size)}</span>
                  <button
                    type="button"
                    onClick={() => setD({ ...d, attachments: d.attachments.filter((x) => x.key !== a.key) })}
                    className="rounded p-0.5 text-zinc-400 hover:bg-zinc-200 hover:text-zinc-700 dark:hover:bg-zinc-700"
                    aria-label={`${a.filename} entfernen`}
                  >
                    <X className="h-3.5 w-3.5" />
                  </button>
                </span>
              ))}
            </div>
          </div>
        )}

        <div className="flex flex-wrap items-center gap-2 border-t border-zinc-200 bg-zinc-50/70 px-5 py-3 dark:border-zinc-800 dark:bg-zinc-900/60">
          <Button variant="primary" icon={Send} onClick={trySend} loading={send.isPending}>
            Senden
          </Button>
          <span className="hidden items-center gap-1 text-xs text-zinc-400 sm:flex">
            <Kbd>Strg</Kbd>+<Kbd>Enter</Kbd>
          </span>
          <div className="ml-auto flex items-center gap-1">
            <input
              ref={fileRef}
              type="file"
              multiple
              hidden
              onChange={(e) => {
                if (e.target.files?.length) void addFiles(e.target.files);
                e.target.value = "";
              }}
            />
            <Button variant="ghost" icon={Paperclip} onClick={() => fileRef.current?.click()}>
              <span className="hidden sm:inline">Anhängen</span>
            </Button>
            <Button variant="ghost" icon={Save} onClick={() => saveDraft.mutate()} loading={saveDraft.isPending}>
              <span className="hidden sm:inline">Entwurf speichern</span>
            </Button>
            <Button variant="danger-ghost" size="icon" icon={Trash2} onClick={() => void requestClose()} aria-label="Verwerfen" title="Verwerfen" />
          </div>
        </div>

        {dragging && (
          <div className="pointer-events-none absolute inset-3 z-10 flex flex-col items-center justify-center gap-2 rounded-2xl border-2 border-dashed border-indigo-400 bg-indigo-50/90 text-indigo-700 dark:bg-indigo-500/15 dark:text-indigo-200">
            <Upload className="h-8 w-8" />
            <span className="text-sm font-medium">Dateien hier ablegen, um sie anzuhängen</span>
          </div>
        )}
      </div>
    </Modal>
  );
}

function AddressRow({
  label,
  value,
  onChange,
  autoFocus,
  children,
}: {
  label: string;
  value: string[];
  onChange: (v: string[]) => void;
  autoFocus?: boolean;
  children?: ReactNode;
}) {
  return (
    <div className="flex items-start gap-3 px-5 py-1.5">
      <span className="w-10 shrink-0 pt-2 text-sm text-zinc-500 dark:text-zinc-400">{label}</span>
      <ChipInput
        value={value}
        onChange={onChange}
        transform={(v) => extractEmail(v).toLowerCase()}
        validate={validateAddress}
        placeholder="name@beispiel.de"
        autoFocus={autoFocus}
        suggest={suggestAddresses}
        className="min-w-0 flex-1 [&>div]:border-0 [&>div]:bg-transparent [&>div]:px-0 [&>div]:shadow-none [&>div]:focus-within:ring-0 dark:[&>div]:bg-transparent"
      />
      {children && <div className="pt-1.5">{children}</div>}
    </div>
  );
}
