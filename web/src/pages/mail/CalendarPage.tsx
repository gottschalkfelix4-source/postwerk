import { useEffect, useMemo, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  CalendarDays,
  ChevronLeft,
  ChevronRight,
  Clock,
  Download,
  Mail,
  MapPin,
  MoreHorizontal,
  Pencil,
  Plus,
  Repeat,
  Trash2,
  Upload,
} from "lucide-react";
import { calendarApi } from "@/api/endpoints";
import type { Calendar, CalendarEvent, EventInput } from "@/api/types";
import { qk } from "@/lib/queries";
import { formatTime, isSameDay } from "@/lib/format";
import { cn, storageGet, storageSet } from "@/lib/utils";
import {
  Button,
  Card,
  Checkbox,
  Dropdown,
  DropdownItem,
  DropdownSeparator,
  ErrorState,
  Field,
  Input,
  Modal,
  PageHeader,
  Select,
  Switch,
  Textarea,
  useConfirm,
  useToast,
} from "@/components/ui";
import { mailLink } from "./mailUtils";
import {
  CALENDAR_COLORS,
  addDays,
  buildRepeat,
  dateKey,
  describeRepeat,
  eventCovers,
  eventEnd,
  parseEventTime,
  parseRepeat,
  readFileText,
  timeKey,
  type RepeatState,
} from "./calendarUtils";

const WEEKDAYS = ["Mo", "Di", "Mi", "Do", "Fr", "Sa", "So"];
const HIDDEN_KEY = "postwerk.calendar.hidden";
const FALLBACK_COLOR = "#4f46e5";

function startOfGrid(month: Date): Date {
  const first = new Date(month.getFullYear(), month.getMonth(), 1);
  const offset = (first.getDay() + 6) % 7; // Monday = 0
  return new Date(first.getFullYear(), first.getMonth(), 1 - offset);
}

function loadHidden(): number[] {
  try {
    const v = JSON.parse(storageGet(HIDDEN_KEY) ?? "[]");
    return Array.isArray(v) ? v.filter((x): x is number => typeof x === "number") : [];
  } catch {
    return [];
  }
}

const eventKey = (e: CalendarEvent) => `${e.id}:${e.start}`;

function formatEventRange(e: CalendarEvent): string {
  const start = parseEventTime(e.start);
  if (e.allDay) {
    const last = addDays(eventEnd(e), -1);
    const fmt = (d: Date) => d.toLocaleDateString("de-DE", { dateStyle: "medium" });
    return last > start ? `${fmt(start)} – ${fmt(last)} (ganztägig)` : `${fmt(start)} (ganztägig)`;
  }
  const s = start.toLocaleString("de-DE", { dateStyle: "medium", timeStyle: "short" });
  if (!e.end) return s;
  const end = parseEventTime(e.end);
  const t = isSameDay(start, end)
    ? end.toLocaleTimeString("de-DE", { hour: "2-digit", minute: "2-digit" })
    : end.toLocaleString("de-DE", { dateStyle: "medium", timeStyle: "short" });
  return `${s} – ${t}`;
}

export default function CalendarPage() {
  const [month, setMonth] = useState(() => {
    const now = new Date();
    return new Date(now.getFullYear(), now.getMonth(), 1);
  });
  const [editor, setEditor] = useState<{ day?: Date; event?: CalendarEvent } | null>(null);
  const [selected, setSelected] = useState<CalendarEvent | null>(null);
  const [hidden, setHidden] = useState<number[]>(loadHidden);

  const gridStart = startOfGrid(month);
  const days = useMemo(() => Array.from({ length: 42 }, (_, i) => addDays(gridStart, i)), [gridStart.getTime()]); // eslint-disable-line react-hooks/exhaustive-deps
  const from = days[0].toISOString();
  const to = addDays(days[41], 1).toISOString();

  const calendars = useQuery({ queryKey: qk.calendars, queryFn: calendarApi.calendars });
  const q = useQuery({ queryKey: [...qk.calendar, from, to], queryFn: () => calendarApi.events(from, to) });
  const today = new Date();

  const eventCalendars = useMemo(() => (calendars.data ?? []).filter((c) => c.components.includes("VEVENT")), [calendars.data]);
  const colorOf = useMemo(() => {
    const m = new Map<number, string>();
    for (const c of calendars.data ?? []) m.set(c.id, c.color || FALLBACK_COLOR);
    return (id: number) => m.get(id) ?? FALLBACK_COLOR;
  }, [calendars.data]);

  const toggleHidden = (id: number) => {
    setHidden((prev) => {
      const next = prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id];
      storageSet(HIDDEN_KEY, JSON.stringify(next));
      return next;
    });
  };

  const byDay = useMemo(() => {
    const events = (q.data ?? [])
      .filter((e) => !hidden.includes(e.calendarId))
      .sort((a, b) => {
        if (a.allDay !== b.allDay) return a.allDay ? -1 : 1;
        return parseEventTime(a.start).getTime() - parseEventTime(b.start).getTime();
      });
    return days.map((d) => events.filter((e) => eventCovers(e, d)));
  }, [q.data, days, hidden]);

  const monthLabel = month.toLocaleDateString("de-DE", { month: "long", year: "numeric" });

  return (
    <>
      <PageHeader
        title="Kalender"
        description="Termine aus deinem Postfach und eigene Einträge – per CalDAV auch auf Smartphone und Desktop."
        actions={
          <Button variant="primary" icon={Plus} onClick={() => setEditor({ day: new Date() })} disabled={eventCalendars.length === 0}>
            Termin anlegen
          </Button>
        }
      />
      <div className="grid gap-4 lg:grid-cols-[15rem_minmax(0,1fr)]">
        <CalendarList calendars={calendars.data ?? []} loading={calendars.isLoading} hidden={hidden} onToggle={toggleHidden} />
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
                      onDoubleClick={() => eventCalendars.length > 0 && setEditor({ day: d })}
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
                        {eventCalendars.length > 0 && (
                          <button
                            type="button"
                            onClick={() => setEditor({ day: d })}
                            className="rounded p-0.5 text-zinc-400 opacity-0 transition hover:bg-zinc-100 hover:text-zinc-700 group-hover:opacity-100 dark:hover:bg-zinc-800"
                            aria-label="Termin an diesem Tag anlegen"
                          >
                            <Plus className="h-3.5 w-3.5" />
                          </button>
                        )}
                      </div>
                      <div className="space-y-1">
                        {events.slice(0, 3).map((e) => {
                          const color = colorOf(e.calendarId);
                          return (
                            <button
                              key={eventKey(e)}
                              type="button"
                              onClick={() => setSelected(e)}
                              style={{ backgroundColor: `${color}1f`, borderLeftColor: color }}
                              className="flex w-full items-center gap-1 truncate rounded-md border-l-[3px] px-1.5 py-0.5 text-left text-[11px] font-medium text-zinc-800 hover:brightness-95 dark:text-zinc-100 dark:hover:brightness-125"
                              title={e.title}
                            >
                              {!e.allDay && isSameDay(parseEventTime(e.start), d) && (
                                <span className="shrink-0 tabular-nums opacity-70">{formatTime(e.start)}</span>
                              )}
                              {e.rrule && <Repeat className="h-3 w-3 shrink-0 opacity-60" />}
                              <span className="truncate">{e.title}</span>
                            </button>
                          );
                        })}
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
      </div>

      <EventEditorDialog
        open={editor !== null}
        day={editor?.day}
        event={editor?.event}
        calendars={eventCalendars}
        visibleDefault={eventCalendars.find((c) => !hidden.includes(c.id))?.id}
        onClose={() => setEditor(null)}
      />
      <EventDetailDialog
        event={selected}
        calendar={calendars.data?.find((c) => c.id === selected?.calendarId)}
        onClose={() => setSelected(null)}
        onEdit={(e) => {
          setSelected(null);
          setEditor({ event: e });
        }}
      />
    </>
  );
}

// ---- calendar list ----

function CalendarList({
  calendars,
  loading,
  hidden,
  onToggle,
}: {
  calendars: Calendar[];
  loading: boolean;
  hidden: number[];
  onToggle: (id: number) => void;
}) {
  const qc = useQueryClient();
  const toast = useToast();
  const confirm = useConfirm();
  const [edit, setEdit] = useState<Calendar | "new" | null>(null);
  const fileRef = useRef<HTMLInputElement>(null);
  const [importTarget, setImportTarget] = useState<number | null>(null);

  const remove = useMutation({
    mutationFn: (id: number) => calendarApi.deleteCalendar(id),
    onSuccess: () => {
      toast.success("Kalender gelöscht");
      void qc.invalidateQueries({ queryKey: qk.calendars });
      void qc.invalidateQueries({ queryKey: qk.calendar });
      void qc.invalidateQueries({ queryKey: qk.tasks });
    },
    onError: (err) => toast.error(err, "Kalender konnte nicht gelöscht werden"),
  });

  const importIcs = useMutation({
    mutationFn: async ({ id, file }: { id: number; file: File }) => calendarApi.importIcs(id, await readFileText(file)),
    onSuccess: (r) => {
      toast.success("Import abgeschlossen", `${r.imported} neu, ${r.updated} aktualisiert, ${r.skipped} übersprungen`);
      void qc.invalidateQueries({ queryKey: qk.calendar });
      void qc.invalidateQueries({ queryKey: qk.tasks });
    },
    onError: (err) => toast.error(err, "Import fehlgeschlagen"),
  });

  return (
    <Card className="h-fit">
      <div className="flex items-center justify-between border-b border-zinc-100 px-4 py-3 dark:border-zinc-800">
        <h2 className="text-sm font-semibold text-zinc-900 dark:text-zinc-100">Meine Kalender</h2>
        <Button size="icon-sm" variant="ghost" icon={Plus} onClick={() => setEdit("new")} aria-label="Kalender anlegen" title="Kalender anlegen" />
      </div>
      <ul className="p-1.5">
        {loading && <li className="px-2.5 py-2 text-sm text-zinc-400">Wird geladen …</li>}
        {calendars.map((c) => (
          <li key={c.id} className="group flex items-center gap-2.5 rounded-lg px-2.5 py-1.5 hover:bg-zinc-50 dark:hover:bg-zinc-800/50">
            <Checkbox
              checked={!hidden.includes(c.id)}
              onChange={() => onToggle(c.id)}
              style={{ accentColor: c.color || FALLBACK_COLOR }}
              aria-label={`${c.name} anzeigen`}
            />
            <span className="h-2.5 w-2.5 shrink-0 rounded-full" style={{ backgroundColor: c.color || FALLBACK_COLOR }} />
            <span className="min-w-0 flex-1 truncate text-sm text-zinc-700 dark:text-zinc-200" title={c.name}>
              {c.name}
              {!c.components.includes("VEVENT") && <span className="ml-1 text-xs text-zinc-400">(nur Aufgaben)</span>}
            </span>
            <Dropdown
              align="right"
              trigger={({ toggle }) => (
                <button
                  type="button"
                  onClick={toggle}
                  className="rounded p-0.5 text-zinc-400 opacity-0 hover:bg-zinc-200 hover:text-zinc-700 focus:opacity-100 group-hover:opacity-100 dark:hover:bg-zinc-700 dark:hover:text-zinc-200"
                  aria-label={`Aktionen für ${c.name}`}
                >
                  <MoreHorizontal className="h-4 w-4" />
                </button>
              )}
            >
              {(close) => (
                <>
                  <DropdownItem
                    icon={Pencil}
                    onClick={() => {
                      close();
                      setEdit(c);
                    }}
                  >
                    Bearbeiten
                  </DropdownItem>
                  <DropdownItem
                    icon={Download}
                    onClick={() => {
                      close();
                      window.location.href = calendarApi.exportUrl(c.id);
                    }}
                  >
                    Als ICS exportieren
                  </DropdownItem>
                  <DropdownItem
                    icon={Upload}
                    onClick={() => {
                      close();
                      setImportTarget(c.id);
                      fileRef.current?.click();
                    }}
                  >
                    ICS importieren …
                  </DropdownItem>
                  <DropdownSeparator />
                  <DropdownItem
                    icon={Trash2}
                    danger
                    disabled={calendars.length <= 1}
                    onClick={async () => {
                      close();
                      const ok = await confirm({
                        title: "Kalender löschen?",
                        message: `„${c.name}“ wird mit allen Terminen und Aufgaben gelöscht – auch auf synchronisierten Geräten.`,
                        confirmLabel: "Löschen",
                        danger: true,
                      });
                      if (ok) remove.mutate(c.id);
                    }}
                  >
                    Löschen
                  </DropdownItem>
                </>
              )}
            </Dropdown>
          </li>
        ))}
      </ul>
      <input
        ref={fileRef}
        type="file"
        accept=".ics,text/calendar"
        className="hidden"
        onChange={(e) => {
          const file = e.target.files?.[0];
          e.target.value = "";
          if (file && importTarget !== null) importIcs.mutate({ id: importTarget, file });
        }}
      />
      <CalendarEditDialog calendar={edit} onClose={() => setEdit(null)} />
    </Card>
  );
}

function CalendarEditDialog({ calendar, onClose }: { calendar: Calendar | "new" | null; onClose: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [name, setName] = useState("");
  const [color, setColor] = useState(CALENDAR_COLORS[0]);
  const [tasks, setTasks] = useState(true);
  const [last, setLast] = useState<Calendar | "new" | null>(null);

  if (calendar !== last) {
    setLast(calendar);
    if (calendar === "new") {
      setName("");
      setColor(CALENDAR_COLORS[1]);
      setTasks(true);
    } else if (calendar) {
      setName(calendar.name);
      setColor(calendar.color || FALLBACK_COLOR);
    }
  }

  const save = useMutation({
    mutationFn: () =>
      calendar === "new"
        ? calendarApi.createCalendar({ name: name.trim(), color, components: tasks ? ["VEVENT", "VTODO"] : ["VEVENT"] })
        : calendarApi.updateCalendar((calendar as Calendar).id, { name: name.trim(), color }),
    onSuccess: () => {
      toast.success(calendar === "new" ? "Kalender angelegt" : "Kalender gespeichert");
      void qc.invalidateQueries({ queryKey: qk.calendars });
      onClose();
    },
    onError: (err) => toast.error(err, "Speichern fehlgeschlagen"),
  });

  return (
    <Modal
      open={calendar !== null}
      onClose={onClose}
      size="sm"
      title={calendar === "new" ? "Neuer Kalender" : "Kalender bearbeiten"}
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
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          if (name.trim()) save.mutate();
        }}
      >
        <Field label="Name">{(id) => <Input id={id} value={name} onChange={(e) => setName(e.target.value)} placeholder="z. B. Familie" autoFocus />}</Field>
        <Field label="Farbe">
          {() => (
            <div className="flex flex-wrap gap-2">
              {CALENDAR_COLORS.map((c) => (
                <button
                  key={c}
                  type="button"
                  onClick={() => setColor(c)}
                  style={{ backgroundColor: c }}
                  className={cn(
                    "h-7 w-7 rounded-full ring-offset-2 transition dark:ring-offset-zinc-900",
                    color.toLowerCase() === c ? "ring-2 ring-zinc-900 dark:ring-zinc-100" : "hover:scale-110",
                  )}
                  aria-label={`Farbe ${c}`}
                  aria-pressed={color.toLowerCase() === c}
                />
              ))}
            </div>
          )}
        </Field>
        {calendar === "new" && (
          <label className="flex items-center gap-2 text-sm text-zinc-700 dark:text-zinc-300">
            <Switch size="sm" checked={tasks} onChange={setTasks} aria-label="Auch Aufgaben" /> Auch Aufgaben in diesem Kalender
          </label>
        )}
        <button type="submit" className="hidden" />
      </form>
    </Modal>
  );
}

// ---- event editor ----

interface EditorState {
  calendarId: string;
  title: string;
  allDay: boolean;
  startDate: string;
  startTime: string;
  endDate: string;
  endTime: string;
  location: string;
  description: string;
  repeat: RepeatState;
}

function initialState(day: Date | undefined, event: CalendarEvent | undefined, defaultCalendar: number | undefined): EditorState {
  if (event) {
    const start = parseEventTime(event.start);
    const end = eventEnd(event);
    // All-day ends are exclusive in the API; the form shows the last day.
    const shownEnd = event.allDay ? addDays(end, -1) : end;
    return {
      calendarId: String(event.calendarId),
      title: event.title,
      allDay: event.allDay,
      startDate: dateKey(start),
      startTime: timeKey(start),
      endDate: dateKey(shownEnd < start ? start : shownEnd),
      endTime: event.end ? timeKey(end) : timeKey(new Date(start.getTime() + 3600_000)),
      location: event.location ?? "",
      description: event.description ?? "",
      repeat: parseRepeat(event.rrule),
    };
  }
  const d = day ?? new Date();
  return {
    calendarId: defaultCalendar ? String(defaultCalendar) : "",
    title: "",
    allDay: false,
    startDate: dateKey(d),
    startTime: "09:00",
    endDate: dateKey(d),
    endTime: "10:00",
    location: "",
    description: "",
    repeat: parseRepeat(null),
  };
}

function toLocalISO(date: string, time: string): string | null {
  const [y, m, d] = date.split("-").map(Number);
  const [hh, mm] = (time || "00:00").split(":").map(Number);
  const dt = new Date(y, (m ?? 1) - 1, d ?? 1, hh ?? 0, mm ?? 0, 0, 0);
  return Number.isNaN(dt.getTime()) ? null : dt.toISOString();
}

function EventEditorDialog({
  open,
  day,
  event,
  calendars,
  visibleDefault,
  onClose,
}: {
  open: boolean;
  day?: Date;
  event?: CalendarEvent;
  calendars: Calendar[];
  visibleDefault?: number;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const toast = useToast();
  const [s, setS] = useState<EditorState>(() => initialState(day, event, visibleDefault ?? calendars[0]?.id));
  const set = <K extends keyof EditorState>(k: K, v: EditorState[K]) => setS((prev) => ({ ...prev, [k]: v }));

  useEffect(() => {
    if (open) setS(initialState(day, event, visibleDefault ?? calendars[0]?.id));
    // Reset the form whenever the dialog opens for a new target.
  }, [open, day, event]); // eslint-disable-line react-hooks/exhaustive-deps

  const save = useMutation({
    mutationFn: () => {
      let body: EventInput;
      if (s.allDay) {
        const endIncl = s.endDate && s.endDate >= s.startDate ? s.endDate : s.startDate;
        body = {
          title: s.title.trim(),
          allDay: true,
          start: s.startDate,
          end: dateKey(addDays(parseEventTime(endIncl), 1)),
        };
      } else {
        const start = toLocalISO(s.startDate, s.startTime);
        const end = toLocalISO(s.endDate || s.startDate, s.endTime);
        if (!start) throw new Error("Bitte ein gültiges Datum angeben.");
        if (end && end < start) throw new Error("Das Ende liegt vor dem Beginn.");
        body = { title: s.title.trim(), allDay: false, start, end };
      }
      body.location = s.location.trim() || null;
      body.description = s.description.trim() || null;
      if (s.calendarId) body.calendarId = Number(s.calendarId);
      const rrule = buildRepeat(s.repeat, s.allDay);
      if (rrule !== undefined) {
        if (rrule !== "" || event) body.rrule = rrule;
      }
      // Editing an occurrence moves the whole series by the same offset.
      // Timed instances are sent as UTC instants so the server computes the
      // offset in the same time base as `start`.
      if (event) {
        const instance = event.instance ? (event.allDay ? event.instance : parseEventTime(event.instance).toISOString()) : null;
        return calendarApi.updateEvent(event.id, instance ? { ...body, instance } : body);
      }
      return calendarApi.createEvent({ ...body, sourceMessageId: null });
    },
    onSuccess: () => {
      toast.success(event ? "Termin gespeichert" : "Termin angelegt");
      void qc.invalidateQueries({ queryKey: qk.calendar });
      onClose();
    },
    onError: (err) => toast.error(err, "Termin konnte nicht gespeichert werden"),
  });

  const canSave = s.title.trim() !== "" && s.startDate !== "";
  const repeatOptions = [
    { value: "none", label: "Keine Wiederholung" },
    { value: "DAILY", label: "Täglich" },
    { value: "WEEKLY", label: "Wöchentlich" },
    { value: "MONTHLY", label: "Monatlich" },
    { value: "YEARLY", label: "Jährlich" },
    ...(s.repeat.kind === "custom" ? [{ value: "custom", label: "Benutzerdefiniert (unverändert)" }] : []),
  ];

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={event ? "Termin bearbeiten" : "Neuer Termin"}
      description={event?.rrule ? "Änderungen gelten für alle Termine der Serie." : undefined}
      icon={
        <span className="flex h-9 w-9 items-center justify-center rounded-lg bg-indigo-50 text-indigo-600 dark:bg-indigo-500/15 dark:text-indigo-300">
          <CalendarDays className="h-[18px] w-[18px]" />
        </span>
      }
      footer={
        <>
          <Button onClick={onClose}>Abbrechen</Button>
          <Button variant="primary" disabled={!canSave} loading={save.isPending} onClick={() => save.mutate()}>
            {event ? "Speichern" : "Anlegen"}
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
        <Field label="Titel">{(id) => <Input id={id} value={s.title} onChange={(e) => set("title", e.target.value)} placeholder="z. B. Abstimmung mit Team" />}</Field>
        {calendars.length > 1 && (
          <Field label="Kalender">
            {(id) => (
              <Select id={id} value={s.calendarId} onChange={(e) => set("calendarId", e.target.value)} options={calendars.map((c) => ({ value: String(c.id), label: c.name }))} />
            )}
          </Field>
        )}
        <label className="flex items-center gap-2 text-sm text-zinc-700 dark:text-zinc-300">
          <Switch size="sm" checked={s.allDay} onChange={(v) => set("allDay", v)} aria-label="Ganztägig" /> Ganztägig
        </label>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Beginn">
            {(id) => (
              <div className="flex gap-2">
                <Input
                  id={id}
                  type="date"
                  value={s.startDate}
                  onChange={(e) => {
                    const v = e.target.value;
                    setS((prev) => ({ ...prev, startDate: v, endDate: !prev.endDate || prev.endDate < v ? v : prev.endDate }));
                  }}
                />
                {!s.allDay && <Input type="time" value={s.startTime} onChange={(e) => set("startTime", e.target.value)} className="w-28" />}
              </div>
            )}
          </Field>
          <Field label="Ende">
            {(id) => (
              <div className="flex gap-2">
                <Input id={id} type="date" value={s.endDate} onChange={(e) => set("endDate", e.target.value)} min={s.startDate} />
                {!s.allDay && <Input type="time" value={s.endTime} onChange={(e) => set("endTime", e.target.value)} className="w-28" />}
              </div>
            )}
          </Field>
        </div>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Wiederholung">
            {(id) => (
              <Select
                id={id}
                value={s.repeat.kind}
                onChange={(e) => set("repeat", { ...s.repeat, kind: e.target.value as RepeatState["kind"] })}
                options={repeatOptions}
              />
            )}
          </Field>
          {s.repeat.kind !== "none" && s.repeat.kind !== "custom" && (
            <Field label="Ende der Wiederholung">
              {(id) => (
                <div className="flex gap-2">
                  <Select
                    id={id}
                    value={s.repeat.endMode}
                    onChange={(e) => set("repeat", { ...s.repeat, endMode: e.target.value as RepeatState["endMode"] })}
                    options={[
                      { value: "never", label: "Nie" },
                      { value: "until", label: "Am" },
                      { value: "count", label: "Nach" },
                    ]}
                    className="w-24 shrink-0"
                  />
                  {s.repeat.endMode === "until" && (
                    <Input type="date" value={s.repeat.until} min={s.startDate} onChange={(e) => set("repeat", { ...s.repeat, until: e.target.value })} />
                  )}
                  {s.repeat.endMode === "count" && (
                    <Input
                      type="number"
                      min={1}
                      max={999}
                      value={s.repeat.count}
                      onChange={(e) => set("repeat", { ...s.repeat, count: Number(e.target.value) })}
                      trailing={<span className="text-xs text-zinc-400">mal</span>}
                    />
                  )}
                </div>
              )}
            </Field>
          )}
        </div>
        <Field label="Ort">{(id) => <Input id={id} icon={MapPin} value={s.location} onChange={(e) => set("location", e.target.value)} placeholder="Optional" />}</Field>
        <Field label="Beschreibung">
          {(id) => <Textarea id={id} rows={3} value={s.description} onChange={(e) => set("description", e.target.value)} placeholder="Optional" />}
        </Field>
        <button type="submit" className="hidden" />
      </form>
    </Modal>
  );
}

// ---- event details ----

function EventDetailDialog({
  event,
  calendar,
  onClose,
  onEdit,
}: {
  event: CalendarEvent | null;
  calendar?: Calendar;
  onClose: () => void;
  onEdit: (e: CalendarEvent) => void;
}) {
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
  const repeat = describeRepeat(event?.rrule ?? null);

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
                const ok = await confirm({
                  title: event.rrule ? "Terminserie löschen?" : "Termin löschen?",
                  message: event.rrule ? `„${event.title}“ wiederholt sich – es werden alle Termine der Serie gelöscht.` : event.title,
                  confirmLabel: "Löschen",
                  danger: true,
                });
                if (ok) remove.mutate(event.id);
              }}
            >
              Löschen
            </Button>
            <Button icon={Pencil} onClick={() => onEdit(event)}>
              Bearbeiten
            </Button>
          </>
        )
      }
    >
      {event && (
        <div className="space-y-3 text-sm">
          {calendar && (
            <div className="flex items-center gap-2.5 text-zinc-700 dark:text-zinc-300">
              <span className="ml-1 mr-0.5 h-2.5 w-2.5 shrink-0 rounded-full" style={{ backgroundColor: calendar.color || FALLBACK_COLOR }} />
              <span>{calendar.name}</span>
            </div>
          )}
          <div className="flex items-start gap-2.5 text-zinc-700 dark:text-zinc-300">
            <Clock className="mt-0.5 h-4 w-4 shrink-0 text-zinc-400" />
            <span>{formatEventRange(event)}</span>
          </div>
          {repeat && (
            <div className="flex items-start gap-2.5 text-zinc-700 dark:text-zinc-300">
              <Repeat className="mt-0.5 h-4 w-4 shrink-0 text-zinc-400" />
              <span>{repeat}</span>
            </div>
          )}
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
