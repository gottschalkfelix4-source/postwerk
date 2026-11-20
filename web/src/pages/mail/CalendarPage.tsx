import { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CalendarDays, ChevronLeft, ChevronRight, Clock, Mail, MapPin, Plus, Trash2 } from "lucide-react";
import { calendarApi } from "@/api/endpoints";
import type { CalendarEvent } from "@/api/types";
import { qk } from "@/lib/queries";
import { formatDateTime, formatTime, fromDateTimeInput, isSameDay, toDateInput } from "@/lib/format";
import { cn } from "@/lib/utils";
import { Button, Card, ErrorState, Field, Input, Modal, PageHeader, Switch, Textarea, useConfirm, useToast } from "@/components/ui";
import { mailLink } from "./mailUtils";

const WEEKDAYS = ["Mo", "Di", "Mi", "Do", "Fr", "Sa", "So"];

function startOfGrid(month: Date): Date {
  const first = new Date(month.getFullYear(), month.getMonth(), 1);
  const offset = (first.getDay() + 6) % 7; // Monday = 0
  return new Date(first.getFullYear(), first.getMonth(), 1 - offset);
}

function addDays(d: Date, n: number): Date {
  return new Date(d.getFullYear(), d.getMonth(), d.getDate() + n);
}

function eventCovers(e: CalendarEvent, day: Date): boolean {
  const start = new Date(e.start);
  const end = e.end ? new Date(e.end) : start;
  const dayStart = new Date(day.getFullYear(), day.getMonth(), day.getDate());
  const dayEnd = addDays(dayStart, 1);
  return start < dayEnd && (end > dayStart || isSameDay(start, day));
}

function isAllDay(e: CalendarEvent): boolean {
  const s = new Date(e.start);
  return s.getHours() === 0 && s.getMinutes() === 0 && (!e.end || new Date(e.end).getHours() === 0);
}

export default function CalendarPage() {
  const [month, setMonth] = useState(() => {
    const now = new Date();
    return new Date(now.getFullYear(), now.getMonth(), 1);
  });
  const [createFor, setCreateFor] = useState<Date | null>(null);
  const [selected, setSelected] = useState<CalendarEvent | null>(null);

  const gridStart = startOfGrid(month);
  const days = useMemo(() => Array.from({ length: 42 }, (_, i) => addDays(gridStart, i)), [gridStart.getTime()]); // eslint-disable-line react-hooks/exhaustive-deps
  const from = days[0].toISOString();
  const to = addDays(days[41], 1).toISOString();

  const q = useQuery({ queryKey: [...qk.calendar, from, to], queryFn: () => calendarApi.events(from, to) });
  const today = new Date();

  const byDay = useMemo(() => {
    const events = [...(q.data ?? [])].sort((a, b) => a.start.localeCompare(b.start));
    return days.map((d) => events.filter((e) => eventCovers(e, d)));
  }, [q.data, days]);

  const monthLabel = month.toLocaleDateString("de-DE", { month: "long", year: "numeric" });

  return (
    <>
      <PageHeader
        title="Kalender"
        description="Termine aus deinem Postfach und eigene Einträge."
        actions={
          <Button variant="primary" icon={Plus} onClick={() => setCreateFor(new Date())}>
            Termin anlegen
          </Button>
        }
      />
      <Card className="overflow-hidden">
        <div className="flex flex-wrap items-center justify-between gap-3 border-b border-zinc-100 px-4 py-3 dark:border-zinc-800">
          <div className="flex items-center gap-2">
            <Button size="icon-sm" icon={ChevronLeft} onClick={() => setMonth(new Date(month.getFullYear(), month.getMonth() - 1, 1))} aria-label="Vorheriger Monat" />
            <Button size="icon-sm" icon={ChevronRight} onClick={() => setMonth(new Date(month.getFullYear(), month.getMonth() + 1, 1))} aria-label="Nächster Monat" />
            <h2 className="ml-2 text-lg font-semibold capitalize text-zinc-900 dark:text-zinc-50">{monthLabel}</h2>
          </div>
          <Button size="sm" onClick={() => setMonth(new Date(today.getFullYear(), today.getMonth(), 1))}>
            Heute
          </Button>
        </div>
        {q.error ? (
          <ErrorState error={q.error} onRetry={() => void q.refetch()} />
        ) : (
          <div className={cn("transition-opacity", q.isFetching && "opacity-70")}>
            <div className="grid grid-cols-7 border-b border-zinc-100 bg-zinc-50/70 dark:border-zinc-800 dark:bg-zinc-900/60">
              {WEEKDAYS.map((d) => (
                <div key={d} className="px-2 py-2 text-center text-xs font-semibold uppercase tracking-wide text-zinc-500 dark:text-zinc-400">
                  {d}
                </div>
              ))}
            </div>
            <div className="grid grid-cols-7">
              {days.map((d, i) => {
                const inMonth = d.getMonth() === month.getMonth();
                const isToday = isSameDay(d, today);
                const events = byDay[i];
                return (
                  <div
                    key={i}
                    onDoubleClick={() => setCreateFor(d)}
                    className={cn(
                      "group relative min-h-[6.5rem] border-b border-r border-zinc-100 p-1.5 dark:border-zinc-800 sm:min-h-[7.5rem]",
                      i % 7 === 6 && "border-r-0",
                      !inMonth && "bg-zinc-50/60 dark:bg-zinc-950/40",
                    )}
                  >
                    <div className="mb-1 flex items-center justify-between">
                      <span
                        className={cn(
                          "flex h-6 min-w-[1.5rem] items-center justify-center rounded-full px-1 text-xs font-medium",
                          isToday
                            ? "bg-indigo-600 text-white dark:bg-indigo-500"
                            : inMonth
                              ? "text-zinc-700 dark:text-zinc-300"
                              : "text-zinc-400 dark:text-zinc-600",
                        )}
                      >
                        {d.getDate()}
                      </span>
                      <button
                        type="button"
                        onClick={() => setCreateFor(d)}
                        className="rounded p-0.5 text-zinc-400 opacity-0 transition hover:bg-zinc-100 hover:text-zinc-700 group-hover:opacity-100 dark:hover:bg-zinc-800"
                        aria-label="Termin an diesem Tag anlegen"
                      >
                        <Plus className="h-3.5 w-3.5" />
                      </button>
                    </div>
                    <div className="space-y-1">
                      {events.slice(0, 3).map((e) => (
                        <button
                          key={e.id}
                          type="button"
                          onClick={() => setSelected(e)}
                          className="flex w-full items-center gap-1 truncate rounded-md bg-indigo-50 px-1.5 py-0.5 text-left text-[11px] font-medium text-indigo-800 ring-1 ring-inset ring-indigo-100 hover:bg-indigo-100 dark:bg-indigo-500/15 dark:text-indigo-200 dark:ring-indigo-500/20 dark:hover:bg-indigo-500/25"
                          title={e.title}
                        >
                          {!isAllDay(e) && isSameDay(new Date(e.start), d) && (
                            <span className="shrink-0 tabular-nums text-indigo-500 dark:text-indigo-300">{formatTime(e.start)}</span>
                          )}
                          <span className="truncate">{e.title}</span>
                        </button>
                      ))}
                      {events.length > 3 && (
                        <button type="button" onClick={() => setSelected(events[3])} className="px-1.5 text-[11px] text-zinc-500 hover:text-indigo-600">
                          +{events.length - 3} weitere
                        </button>
                      )}
                    </div>
                  </div>
                );
              })}
            </div>
          </div>
        )}
      </Card>

      <CreateEventDialog day={createFor} onClose={() => setCreateFor(null)} />
      <EventDetailDialog event={selected} onClose={() => setSelected(null)} />
    </>
  );
}

function CreateEventDialog({ day, onClose }: { day: Date | null; onClose: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [title, setTitle] = useState("");
  const [date, setDate] = useState("");
  const [allDay, setAllDay] = useState(false);
  const [startTime, setStartTime] = useState("09:00");
  const [endDate, setEndDate] = useState("");
  const [endTime, setEndTime] = useState("10:00");
  const [location, setLocation] = useState("");
  const [description, setDescription] = useState("");
  const [lastDay, setLastDay] = useState<Date | null>(null);

  if (day !== lastDay) {
    setLastDay(day);
    if (day) {
      const iso = toDateInput(day.toISOString());
      setTitle("");
      setDate(iso);
      setEndDate(iso);
      setAllDay(false);
      setStartTime("09:00");
      setEndTime("10:00");
      setLocation("");
      setDescription("");
    }
  }

  const create = useMutation({
    mutationFn: () => {
      const start = fromDateTimeInput(date, allDay ? "00:00" : startTime);
      const end = allDay
        ? endDate && endDate !== date
          ? fromDateTimeInput(endDate, "00:00")
          : null
        : fromDateTimeInput(endDate || date, endTime);
      if (!start) throw new Error("Bitte ein gültiges Datum angeben.");
      return calendarApi.createEvent({
        title: title.trim(),
        start,
        end,
        location: location.trim() || null,
        description: description.trim() || null,
        sourceMessageId: null,
      });
    },
    onSuccess: () => {
      toast.success("Termin angelegt");
      void qc.invalidateQueries({ queryKey: qk.calendar });
      onClose();
    },
    onError: (err) => toast.error(err, "Termin konnte nicht angelegt werden"),
  });

  return (
    <Modal
      open={day !== null}
      onClose={onClose}
      title="Neuer Termin"
      icon={
        <span className="flex h-9 w-9 items-center justify-center rounded-lg bg-indigo-50 text-indigo-600 dark:bg-indigo-500/15 dark:text-indigo-300">
          <CalendarDays className="h-[18px] w-[18px]" />
        </span>
      }
      footer={
        <>
          <Button onClick={onClose}>Abbrechen</Button>
          <Button variant="primary" disabled={!title.trim() || !date} loading={create.isPending} onClick={() => create.mutate()}>
            Anlegen
          </Button>
        </>
      }
    >
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          if (title.trim() && date) create.mutate();
        }}
      >
        <Field label="Titel">{(id) => <Input id={id} value={title} onChange={(e) => setTitle(e.target.value)} placeholder="z. B. Abstimmung mit Team" />}</Field>
        <label className="flex items-center gap-2 text-sm text-zinc-700 dark:text-zinc-300">
          <Switch size="sm" checked={allDay} onChange={setAllDay} aria-label="Ganztägig" /> Ganztägig
        </label>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Beginn">
            {(id) => (
              <div className="flex gap-2">
                <Input id={id} type="date" value={date} onChange={(e) => setDate(e.target.value)} />
                {!allDay && <Input type="time" value={startTime} onChange={(e) => setStartTime(e.target.value)} className="w-28" />}
              </div>
            )}
          </Field>
          <Field label="Ende">
            {(id) => (
              <div className="flex gap-2">
                <Input id={id} type="date" value={endDate} onChange={(e) => setEndDate(e.target.value)} min={date} />
                {!allDay && <Input type="time" value={endTime} onChange={(e) => setEndTime(e.target.value)} className="w-28" />}
              </div>
            )}
          </Field>
        </div>
        <Field label="Ort">{(id) => <Input id={id} icon={MapPin} value={location} onChange={(e) => setLocation(e.target.value)} placeholder="Optional" />}</Field>
        <Field label="Beschreibung">
          {(id) => <Textarea id={id} rows={3} value={description} onChange={(e) => setDescription(e.target.value)} placeholder="Optional" />}
        </Field>
        <button type="submit" className="hidden" />
      </form>
    </Modal>
  );
}

function EventDetailDialog({ event, onClose }: { event: CalendarEvent | null; onClose: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const confirm = useConfirm();
  const remove = useMutation({
    mutationFn: (id: number) => calendarApi.deleteEvent(id),
    onSuccess: () => {
      toast.success("Termin gelöscht");
      void qc.invalidateQueries({ queryKey: qk.calendar });
      onClose();
    },
    onError: (err) => toast.error(err, "Löschen fehlgeschlagen"),
  });

  return (
    <Modal
      open={event !== null}
      onClose={onClose}
      size="sm"
      title={event?.title}
      footer={
        event && (
          <>
            <Button
              variant="danger-ghost"
              icon={Trash2}
              loading={remove.isPending}
              className="mr-auto"
              onClick={async () => {
                const ok = await confirm({ title: "Termin löschen?", message: event.title, confirmLabel: "Löschen", danger: true });
                if (ok) remove.mutate(event.id);
              }}
            >
              Löschen
            </Button>
            <Button onClick={onClose}>Schließen</Button>
          </>
        )
      }
    >
      {event && (
        <div className="space-y-3 text-sm">
          <div className="flex items-start gap-2.5 text-zinc-700 dark:text-zinc-300">
            <Clock className="mt-0.5 h-4 w-4 shrink-0 text-zinc-400" />
            <span>
              {formatDateTime(event.start)}
              {event.end && ` – ${formatDateTime(event.end)}`}
            </span>
          </div>
          {event.location && (
            <div className="flex items-start gap-2.5 text-zinc-700 dark:text-zinc-300">
              <MapPin className="mt-0.5 h-4 w-4 shrink-0 text-zinc-400" />
              <span>{event.location}</span>
            </div>
          )}
          {event.description && <p className="whitespace-pre-wrap rounded-lg bg-zinc-50 p-3 text-zinc-700 dark:bg-zinc-800/60 dark:text-zinc-300">{event.description}</p>}
          {event.sourceMessageId !== null && (
            <Link to={mailLink({ msg: event.sourceMessageId })} onClick={onClose} className="inline-flex items-center gap-1.5 font-medium text-indigo-600 hover:text-indigo-500 dark:text-indigo-400">
              <Mail className="h-4 w-4" /> Zur Quell-Nachricht
            </Link>
          )}
        </div>
      )}
    </Modal>
  );
}
