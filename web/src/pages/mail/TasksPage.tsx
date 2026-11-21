import { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CalendarClock, CircleCheckBig, ListTodo, Mail, Plus, Trash2 } from "lucide-react";
import { calendarApi, tasksApi } from "@/api/endpoints";
import type { Task } from "@/api/types";
import { qk } from "@/lib/queries";
import { formatDate, fromDateTimeInput, isSameDay } from "@/lib/format";
import { cn } from "@/lib/utils";
import { Badge, Button, Card, Checkbox, EmptyState, ErrorState, Input, PageHeader, Segmented, Select, SkeletonRows, useToast } from "@/components/ui";
import { mailLink } from "./mailUtils";

type Filter = "open" | "done" | "all";

function dueInfo(due: string | null, done: boolean): { label: string; tone: "red" | "amber" | "neutral" } | null {
  if (!due) return null;
  const d = new Date(due);
  if (Number.isNaN(d.getTime())) return { label: due, tone: "neutral" };
  const now = new Date();
  const today = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  const tomorrow = new Date(today.getFullYear(), today.getMonth(), today.getDate() + 1);
  if (isSameDay(d, now)) return { label: "Heute", tone: done ? "neutral" : "amber" };
  if (isSameDay(d, tomorrow)) return { label: "Morgen", tone: "neutral" };
  if (d < today) return { label: `Überfällig · ${formatDate(due)}`, tone: done ? "neutral" : "red" };
  return { label: formatDate(due), tone: "neutral" };
}

export default function TasksPage() {
  const qc = useQueryClient();
  const toast = useToast();
  const [filter, setFilter] = useState<Filter>("open");
  const [title, setTitle] = useState("");
  const [due, setDue] = useState("");

  const [calendarId, setCalendarId] = useState("");

  const q = useQuery({ queryKey: qk.tasks, queryFn: tasksApi.list });
  const calendars = useQuery({ queryKey: qk.calendars, queryFn: calendarApi.calendars });
  const taskCalendars = (calendars.data ?? []).filter((c) => c.components.includes("VTODO"));
  const calendarName = (id: number) => taskCalendars.find((c) => c.id === id)?.name;

  const create = useMutation({
    mutationFn: () =>
      tasksApi.create({
        title: title.trim(),
        due: due ? fromDateTimeInput(due, "12:00") : null,
        ...(calendarId ? { calendarId: Number(calendarId) } : {}),
      }),
    onSuccess: (t) => {
      qc.setQueryData<Task[]>(qk.tasks, (old) => (old ? [t, ...old] : [t]));
      setTitle("");
      setDue("");
    },
    onError: (err) => toast.error(err, "Aufgabe konnte nicht angelegt werden"),
  });

  const update = useMutation({
    mutationFn: ({ id, done }: { id: number; done: boolean }) => tasksApi.update(id, { done }),
    onMutate: ({ id, done }) => {
      qc.setQueryData<Task[]>(qk.tasks, (old) => old?.map((t) => (t.id === id ? { ...t, done } : t)));
    },
    onSuccess: (t) => qc.setQueryData<Task[]>(qk.tasks, (old) => old?.map((x) => (x.id === t.id ? t : x))),
    onError: (err) => {
      toast.error(err, "Aktualisieren fehlgeschlagen");
      void qc.invalidateQueries({ queryKey: qk.tasks });
    },
  });

  const remove = useMutation({
    mutationFn: (id: number) => tasksApi.remove(id),
    onMutate: (id) => qc.setQueryData<Task[]>(qk.tasks, (old) => old?.filter((t) => t.id !== id)),
    onError: (err) => {
      toast.error(err, "Löschen fehlgeschlagen");
      void qc.invalidateQueries({ queryKey: qk.tasks });
    },
  });

  const all = q.data ?? [];
  const openCount = all.filter((t) => !t.done).length;
  const items = useMemo(() => {
    const list = all.filter((t) => (filter === "all" ? true : filter === "done" ? t.done : !t.done));
    return list.sort((a, b) => {
      if (a.done !== b.done) return a.done ? 1 : -1;
      if (a.due && b.due) return a.due.localeCompare(b.due);
      if (a.due) return -1;
      if (b.due) return 1;
      return b.createdAt.localeCompare(a.createdAt);
    });
  }, [all, filter]);

  return (
    <div className="mx-auto max-w-3xl">
      <PageHeader title="Aufgaben" description={`${openCount} offene Aufgaben`} />
      <Card>
        <form
          className="flex flex-wrap gap-2 border-b border-zinc-100 p-4 dark:border-zinc-800"
          onSubmit={(e) => {
            e.preventDefault();
            if (title.trim()) create.mutate();
          }}
        >
          <div className="min-w-[12rem] flex-1">
            <Input icon={Plus} value={title} onChange={(e) => setTitle(e.target.value)} placeholder="Neue Aufgabe …" />
          </div>
          <Input type="date" value={due} onChange={(e) => setDue(e.target.value)} className="w-40" aria-label="Fällig am" />
          {taskCalendars.length > 1 && (
            <Select
              value={calendarId || String(taskCalendars[0].id)}
              onChange={(e) => setCalendarId(e.target.value)}
              options={taskCalendars.map((c) => ({ value: String(c.id), label: c.name }))}
              className="w-40"
              aria-label="Kalender"
            />
          )}
          <Button type="submit" variant="primary" disabled={!title.trim()} loading={create.isPending}>
            Hinzufügen
          </Button>
        </form>
        <div className="flex items-center justify-between border-b border-zinc-100 px-4 py-2.5 dark:border-zinc-800">
          <Segmented<Filter>
            size="sm"
            value={filter}
            onChange={setFilter}
            items={[
              { value: "open", label: "Offen" },
              { value: "done", label: "Erledigt" },
              { value: "all", label: "Alle" },
            ]}
          />
        </div>
        {q.isLoading ? (
          <SkeletonRows rows={5} cols={2} />
        ) : q.error ? (
          <ErrorState error={q.error} onRetry={() => void q.refetch()} />
        ) : items.length === 0 ? (
          <EmptyState
            icon={filter === "done" ? ListTodo : CircleCheckBig}
            title={filter === "done" ? "Noch nichts erledigt" : "Alles erledigt"}
            description={filter === "open" ? "Keine offenen Aufgaben. Aufgaben lassen sich auch direkt aus E-Mails anlegen." : undefined}
          />
        ) : (
          <ul className="divide-y divide-zinc-100 dark:divide-zinc-800">
            {items.map((t) => {
              const info = dueInfo(t.due, t.done);
              return (
                <li key={t.id} className="group flex items-center gap-3 px-4 py-3 hover:bg-zinc-50/70 dark:hover:bg-zinc-800/30">
                  <Checkbox
                    checked={t.done}
                    onChange={() => update.mutate({ id: t.id, done: !t.done })}
                    className="h-[18px] w-[18px] rounded-full"
                    aria-label={t.done ? "Als offen markieren" : "Als erledigt markieren"}
                  />
                  <div className="min-w-0 flex-1">
                    <div className={cn("text-sm", t.done ? "text-zinc-400 line-through" : "text-zinc-900 dark:text-zinc-100")}>{t.title}</div>
                    <div className="mt-1 flex flex-wrap items-center gap-2">
                      {info && (
                        <Badge tone={info.tone} icon={CalendarClock}>
                          {info.label}
                        </Badge>
                      )}
                      {taskCalendars.length > 1 && calendarName(t.calendarId) && (
                        <span className="text-xs text-zinc-400">{calendarName(t.calendarId)}</span>
                      )}
                      {t.sourceMessageId !== null && (
                        <Link
                          to={mailLink({ msg: t.sourceMessageId })}
                          className="inline-flex items-center gap-1 text-xs text-indigo-600 hover:text-indigo-500 dark:text-indigo-400"
                        >
                          <Mail className="h-3 w-3" /> Quell-Nachricht
                        </Link>
                      )}
                    </div>
                  </div>
                  <Button
                    size="icon-sm"
                    variant="danger-ghost"
                    icon={Trash2}
                    onClick={() => remove.mutate(t.id)}
                    aria-label="Aufgabe löschen"
                    className="opacity-0 group-hover:opacity-100 focus:opacity-100"
                  />
                </li>
              );
            })}
          </ul>
        )}
      </Card>
    </div>
  );
}
