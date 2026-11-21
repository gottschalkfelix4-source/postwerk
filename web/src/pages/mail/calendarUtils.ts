import type { CalendarEvent } from "@/api/types";

const DATE_ONLY = /^\d{4}-\d{2}-\d{2}$/;

/** Parses an API time value; plain dates are local dates (not UTC midnight). */
export function parseEventTime(value: string): Date {
  if (DATE_ONLY.test(value)) {
    const [y, m, d] = value.split("-").map(Number);
    return new Date(y, m - 1, d);
  }
  return new Date(value);
}

/** Formats a local date as YYYY-MM-DD. */
export function dateKey(d: Date): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

/** Formats the local time of a date as HH:MM. */
export function timeKey(d: Date): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

export function addDays(d: Date, n: number): Date {
  return new Date(d.getFullYear(), d.getMonth(), d.getDate() + n);
}

/** Exclusive end of an event; events without end last until their start (timed) or one day (all-day). */
export function eventEnd(e: CalendarEvent): Date {
  const start = parseEventTime(e.start);
  if (e.end) return parseEventTime(e.end);
  return e.allDay ? addDays(start, 1) : start;
}

export function eventCovers(e: CalendarEvent, day: Date): boolean {
  const start = parseEventTime(e.start);
  const end = eventEnd(e);
  const dayStart = new Date(day.getFullYear(), day.getMonth(), day.getDate());
  const dayEnd = addDays(dayStart, 1);
  if (start >= dayEnd) return false;
  return end > dayStart || (end.getTime() === start.getTime() && start >= dayStart);
}

export type RepeatKind = "none" | "DAILY" | "WEEKLY" | "MONTHLY" | "YEARLY" | "custom";

export interface RepeatState {
  kind: RepeatKind;
  /** End of the recurrence: none, a date (UNTIL) or a number of occurrences (COUNT). */
  endMode: "never" | "until" | "count";
  until: string;
  count: number;
}

const SIMPLE = /^FREQ=(DAILY|WEEKLY|MONTHLY|YEARLY)(;(UNTIL=(\d{8})(T\d{6}Z?)?|COUNT=(\d+)))?$/;

/** Maps an RRULE onto the simple editor; anything else is "custom" and kept unchanged. */
export function parseRepeat(rrule: string | null): RepeatState {
  const base: RepeatState = { kind: "none", endMode: "never", until: "", count: 10 };
  if (!rrule) return base;
  const m = SIMPLE.exec(rrule.toUpperCase());
  if (!m) return { ...base, kind: "custom" };
  const out: RepeatState = { ...base, kind: m[1] as RepeatKind };
  if (m[4]) {
    out.endMode = "until";
    out.until = `${m[4].slice(0, 4)}-${m[4].slice(4, 6)}-${m[4].slice(6, 8)}`;
  } else if (m[6]) {
    out.endMode = "count";
    out.count = Number(m[6]);
  }
  return out;
}

/** Builds the RRULE for the simple editor; undefined keeps a custom rule unchanged. */
export function buildRepeat(r: RepeatState, allDay: boolean): string | undefined {
  if (r.kind === "custom") return undefined;
  if (r.kind === "none") return "";
  let rule = `FREQ=${r.kind}`;
  if (r.endMode === "until" && r.until) {
    const compact = r.until.replace(/-/g, "");
    // UNTIL must match the DTSTART value type: a date for all-day events, otherwise UTC end of day.
    rule += allDay ? `;UNTIL=${compact}` : `;UNTIL=${compact}T235959Z`;
  } else if (r.endMode === "count" && r.count > 0) {
    rule += `;COUNT=${Math.floor(r.count)}`;
  }
  return rule;
}

const FREQ_LABEL: Record<string, string> = { DAILY: "Täglich", WEEKLY: "Wöchentlich", MONTHLY: "Monatlich", YEARLY: "Jährlich" };

export function describeRepeat(rrule: string | null): string | null {
  if (!rrule) return null;
  const r = parseRepeat(rrule);
  if (r.kind === "custom" || r.kind === "none") return "Wiederholt sich (benutzerdefiniert)";
  let s = FREQ_LABEL[r.kind];
  if (r.endMode === "until" && r.until) s += ` bis ${parseEventTime(r.until).toLocaleDateString("de-DE")}`;
  if (r.endMode === "count") s += `, ${r.count}-mal`;
  return s;
}

export const CALENDAR_COLORS = ["#4f46e5", "#0ea5e9", "#10b981", "#f59e0b", "#ef4444", "#ec4899", "#8b5cf6", "#64748b"];

/** Reads a file selected in an <input type="file"> as text. */
export function readFileText(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result ?? ""));
    reader.onerror = () => reject(reader.error ?? new Error("Datei konnte nicht gelesen werden"));
    reader.readAsText(file);
  });
}
