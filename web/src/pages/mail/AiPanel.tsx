import { useEffect, useState, type ReactNode } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { CalendarPlus, Check, ChevronDown, Flame, ListPlus, MapPin, PenLine, RefreshCw, Sparkles, Wand2 } from "lucide-react";
import { aiApi, calendarApi, tasksApi } from "@/api/endpoints";
import type { ExtractedEvent, ExtractedTask, MessageDetail, MessageSummary, Paged, ReplyTone, SummarizeResult } from "@/api/types";
import { useStoredFlag } from "@/lib/hooks";
import { qk, useAiStatus } from "@/lib/queries";
import { formatDate, formatDateTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import { Badge, Button, Input, Segmented, toneFor, useToast } from "@/components/ui";

export function AiPanel({
  message,
  threadSize,
  onDraft,
}: {
  message: MessageDetail;
  threadSize: number;
  onDraft: (draft: { subject: string; body: string }) => void;
}) {
  const ai = useAiStatus();
  const [open, setOpen] = useStoredFlag("postwerk.mail.aiPanel", true);
  if (!ai.enabled) return null;
  const f = ai.features;
  if (!f.summarize && !f.replyDraft && !f.extractEvents && !f.classify && !f.priority) return null;

  return (
    <section className="overflow-hidden rounded-xl border border-violet-200/70 bg-gradient-to-br from-violet-50/80 via-white to-indigo-50/60 dark:border-violet-500/20 dark:from-violet-500/[0.07] dark:via-zinc-900 dark:to-indigo-500/[0.05]">
      <button
        type="button"
        onClick={() => setOpen(!open)}
        className="flex w-full items-center gap-2 px-4 py-2.5 text-left"
        aria-expanded={open}
      >
        <span className="flex h-6 w-6 items-center justify-center rounded-md bg-violet-600 text-white dark:bg-violet-500">
          <Sparkles className="h-3.5 w-3.5" />
        </span>
        <span className="text-sm font-semibold text-zinc-900 dark:text-zinc-100">KI-Assistent</span>
        {!open && message.ai.summary && (
          <span className="ml-2 hidden min-w-0 flex-1 truncate text-xs text-zinc-500 dark:text-zinc-400 md:block">{message.ai.summary}</span>
        )}
        <ChevronDown className={cn("ml-auto h-4 w-4 text-zinc-400 transition-transform", open && "rotate-180")} />
      </button>
      {open && (
        <div className="grid gap-px border-t border-violet-200/60 bg-violet-200/40 dark:border-violet-500/15 dark:bg-violet-500/10 2xl:grid-cols-2">
          {f.summarize && <SummaryBlock message={message} threadSize={threadSize} />}
          {(f.classify || f.priority) && <ClassificationBlock message={message} />}
          {f.replyDraft && <ReplyBlock message={message} onDraft={onDraft} />}
          {f.extractEvents && <ExtractBlock message={message} />}
        </div>
      )}
    </section>
  );
}

function Block({ title, icon: Icon, children, action }: { title: string; icon: typeof Sparkles; children: ReactNode; action?: ReactNode }) {
  return (
    <div className="bg-white/80 p-4 dark:bg-zinc-900/80">
      <div className="mb-2.5 flex items-center justify-between gap-2">
        <h4 className="flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide text-zinc-500 dark:text-zinc-400">
          <Icon className="h-3.5 w-3.5 text-violet-500" /> {title}
        </h4>
        {action}
      </div>
      {children}
    </div>
  );
}

function SummaryBlock({ message, threadSize }: { message: MessageDetail; threadSize: number }) {
  const toast = useToast();
  const [result, setResult] = useState<(SummarizeResult & { scope: "message" | "thread" }) | null>(null);
  useEffect(() => setResult(null), [message.id]);

  const run = useMutation({
    mutationFn: (scope: "message" | "thread") =>
      (scope === "thread" ? aiApi.summarize({ threadId: message.threadId }) : aiApi.summarize({ messageId: message.id })).then((r) => ({
        ...r,
        scope,
      })),
    onSuccess: setResult,
    onError: (err) => toast.error(err, "Zusammenfassung fehlgeschlagen"),
  });

  const summary = result?.summary ?? message.ai.summary;
  return (
    <Block title="Zusammenfassung" icon={Sparkles}>
      {summary ? (
        <div className="space-y-2">
          {result?.scope === "thread" && <Badge tone="purple">Ganzer Verlauf</Badge>}
          <p className="text-sm leading-relaxed text-zinc-700 dark:text-zinc-300">{summary}</p>
          {result && result.bullets.length > 0 && (
            <ul className="list-disc space-y-1 pl-5 text-sm text-zinc-600 dark:text-zinc-400">
              {result.bullets.map((b, i) => (
                <li key={i}>{b}</li>
              ))}
            </ul>
          )}
        </div>
      ) : (
        <p className="text-sm text-zinc-500 dark:text-zinc-400">Noch keine Zusammenfassung vorhanden.</p>
      )}
      <div className="mt-3 flex flex-wrap gap-2">
        <Button size="xs" icon={Sparkles} loading={run.isPending && run.variables === "message"} disabled={run.isPending} onClick={() => run.mutate("message")}>
          {summary && !result ? "Neu zusammenfassen" : "Nachricht zusammenfassen"}
        </Button>
        {threadSize > 1 && (
          <Button size="xs" icon={Sparkles} loading={run.isPending && run.variables === "thread"} disabled={run.isPending} onClick={() => run.mutate("thread")}>
            Verlauf ({threadSize}) zusammenfassen
          </Button>
        )}
      </div>
    </Block>
  );
}

function ReplyBlock({ message, onDraft }: { message: MessageDetail; onDraft: (d: { subject: string; body: string }) => void }) {
  const toast = useToast();
  const [tone, setTone] = useState<ReplyTone>("friendly");
  const [instruction, setInstruction] = useState("");
  useEffect(() => setInstruction(""), [message.id]);

  const run = useMutation({
    mutationFn: () => aiApi.reply({ messageId: message.id, tone, ...(instruction.trim() ? { instruction: instruction.trim() } : {}) }),
    onSuccess: onDraft,
    onError: (err) => toast.error(err, "Entwurf konnte nicht erstellt werden"),
  });

  return (
    <Block title="Antwort entwerfen" icon={PenLine}>
      <div className="space-y-2.5">
        <Segmented<ReplyTone>
          size="sm"
          value={tone}
          onChange={setTone}
          className="w-full"
          items={[
            { value: "formal", label: "Formell" },
            { value: "friendly", label: "Freundlich" },
            { value: "short", label: "Kurz" },
          ]}
        />
        <Input
          value={instruction}
          onChange={(e) => setInstruction(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && run.mutate()}
          placeholder="Optional: z. B. „Termin am Dienstag zusagen“"
          className="h-8 text-[13px]"
        />
        <Button size="sm" variant="primary" icon={Wand2} loading={run.isPending} onClick={() => run.mutate()} className="w-full bg-violet-600 hover:bg-violet-500 dark:bg-violet-500 dark:hover:bg-violet-400">
          Entwurf erstellen
        </Button>
      </div>
    </Block>
  );
}

function ExtractBlock({ message }: { message: MessageDetail }) {
  const toast = useToast();
  const qc = useQueryClient();
  const [events, setEvents] = useState<ExtractedEvent[]>(message.ai.events);
  const [tasks, setTasks] = useState<ExtractedTask[]>(message.ai.tasks);
  const [added, setAdded] = useState<Set<string>>(new Set());

  useEffect(() => {
    setEvents(message.ai.events);
    setTasks(message.ai.tasks);
    setAdded(new Set());
  }, [message.id, message.ai.events, message.ai.tasks]);

  const extract = useMutation({
    mutationFn: () => aiApi.extract(message.id),
    onSuccess: (r) => {
      setEvents(r.events);
      setTasks(r.tasks);
      if (r.events.length + r.tasks.length === 0) toast.info("Keine Termine oder Aufgaben gefunden");
    },
    onError: (err) => toast.error(err, "Erkennung fehlgeschlagen"),
  });

  const addEvent = useMutation({
    mutationFn: (e: ExtractedEvent) =>
      calendarApi.createEvent({
        title: e.title,
        start: e.start,
        end: e.end,
        location: e.location,
        description: e.description,
        sourceMessageId: message.id,
      }),
    onSuccess: (_, e) => {
      setAdded((s) => new Set(s).add(`e:${e.title}:${e.start}`));
      void qc.invalidateQueries({ queryKey: qk.calendar });
      toast.success("Termin zum Kalender hinzugefügt", e.title);
    },
    onError: (err) => toast.error(err, "Termin konnte nicht angelegt werden"),
  });

  const addTask = useMutation({
    mutationFn: (t: ExtractedTask) => tasksApi.create({ title: t.title, due: t.due, sourceMessageId: message.id }),
    onSuccess: (_, t) => {
      setAdded((s) => new Set(s).add(`t:${t.title}`));
      void qc.invalidateQueries({ queryKey: qk.tasks });
      toast.success("Aufgabe angelegt", t.title);
    },
    onError: (err) => toast.error(err, "Aufgabe konnte nicht angelegt werden"),
  });

  return (
    <Block
      title="Termine & Aufgaben"
      icon={CalendarPlus}
      action={
        <Button size="xs" variant="ghost" icon={RefreshCw} loading={extract.isPending} onClick={() => extract.mutate()}>
          Erkennen
        </Button>
      }
    >
      {events.length === 0 && tasks.length === 0 ? (
        <p className="text-sm text-zinc-500 dark:text-zinc-400">Keine Termine oder Aufgaben erkannt.</p>
      ) : (
        <ul className="space-y-2">
          {events.map((e, i) => {
            const done = added.has(`e:${e.title}:${e.start}`);
            return (
              <li key={`e${i}`} className="flex flex-wrap items-start gap-3 rounded-lg border border-zinc-200 bg-white p-2.5 dark:border-zinc-700 dark:bg-zinc-800/60">
                <CalendarPlus className="mt-0.5 h-4 w-4 shrink-0 text-indigo-500" />
                <div className="min-w-0 flex-1">
                  <div className="truncate text-sm font-medium text-zinc-900 dark:text-zinc-100">{e.title}</div>
                  <div className="text-xs text-zinc-500 dark:text-zinc-400">
                    {formatDateTime(e.start)}
                    {e.end && ` – ${formatDateTime(e.end)}`}
                  </div>
                  {e.location && (
                    <div className="mt-0.5 flex items-center gap-1 text-xs text-zinc-500 dark:text-zinc-400">
                      <MapPin className="h-3 w-3" /> {e.location}
                    </div>
                  )}
                </div>
                <Button
                  size="xs"
                  icon={done ? Check : CalendarPlus}
                  disabled={done || addEvent.isPending}
                  onClick={() => addEvent.mutate(e)}
                >
                  {done ? "Hinzugefügt" : "Zum Kalender"}
                </Button>
              </li>
            );
          })}
          {tasks.map((t, i) => {
            const done = added.has(`t:${t.title}`);
            return (
              <li key={`t${i}`} className="flex flex-wrap items-start gap-3 rounded-lg border border-zinc-200 bg-white p-2.5 dark:border-zinc-700 dark:bg-zinc-800/60">
                <ListPlus className="mt-0.5 h-4 w-4 shrink-0 text-emerald-500" />
                <div className="min-w-0 flex-1">
                  <div className="truncate text-sm font-medium text-zinc-900 dark:text-zinc-100">{t.title}</div>
                  {t.due && <div className="text-xs text-zinc-500 dark:text-zinc-400">Fällig: {formatDate(t.due)}</div>}
                </div>
                <Button size="xs" icon={done ? Check : ListPlus} disabled={done || addTask.isPending} onClick={() => addTask.mutate(t)}>
                  {done ? "Angelegt" : "Als Aufgabe"}
                </Button>
              </li>
            );
          })}
        </ul>
      )}
    </Block>
  );
}

function ClassificationBlock({ message }: { message: MessageDetail }) {
  const toast = useToast();
  const qc = useQueryClient();
  const run = useMutation({
    mutationFn: () => aiApi.reclassify(message.id),
    onSuccess: (s: MessageSummary) => {
      qc.setQueryData<MessageDetail>(qk.message(message.id), (old) =>
        old ? { ...old, category: s.category, priority: s.priority, summary: s.summary, spamScore: s.spamScore, tags: s.tags } : old,
      );
      qc.setQueriesData<Paged<MessageSummary>>({ queryKey: qk.messages }, (old) =>
        old ? { ...old, items: old.items.map((m) => (m.id === s.id ? { ...m, ...s } : m)) } : old,
      );
      void qc.invalidateQueries({ queryKey: ["mail", "thread"] });
      toast.success("Neu bewertet");
    },
    onError: (err) => toast.error(err, "Neubewertung fehlgeschlagen"),
  });

  const prio = { high: { tone: "red" as const, label: "Hoch" }, normal: { tone: "neutral" as const, label: "Normal" }, low: { tone: "blue" as const, label: "Niedrig" } }[
    message.priority
  ];

  return (
    <Block
      title="Einordnung"
      icon={Flame}
      action={
        <Button size="xs" variant="ghost" icon={RefreshCw} loading={run.isPending} onClick={() => run.mutate()}>
          Neu bewerten
        </Button>
      }
    >
      <dl className="grid grid-cols-2 gap-3 text-sm">
        <div>
          <dt className="text-xs text-zinc-500 dark:text-zinc-400">Kategorie</dt>
          <dd className="mt-1">{message.category ? <Badge tone={toneFor(message.category)}>{message.category}</Badge> : <span className="text-zinc-400">—</span>}</dd>
        </div>
        <div>
          <dt className="text-xs text-zinc-500 dark:text-zinc-400">Priorität</dt>
          <dd className="mt-1">
            <Badge tone={prio.tone} icon={message.priority === "high" ? Flame : undefined}>
              {prio.label}
            </Badge>
          </dd>
        </div>
      </dl>
      {message.ai.reasons.length > 0 && (
        <div className="mt-3">
          <div className="mb-1 text-xs text-zinc-500 dark:text-zinc-400">Begründung</div>
          <ul className="space-y-1 text-xs text-zinc-600 dark:text-zinc-400">
            {message.ai.reasons.map((r, i) => (
              <li key={i} className="flex gap-1.5">
                <span className="mt-1.5 h-1 w-1 shrink-0 rounded-full bg-violet-400" />
                {r}
              </li>
            ))}
          </ul>
        </div>
      )}
    </Block>
  );
}
