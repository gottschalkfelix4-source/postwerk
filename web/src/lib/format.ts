const LOCALE = "de-DE";

export function formatBytes(bytes: number, digits = 1): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  const i = Math.min(units.length - 1, Math.floor(Math.log(bytes) / Math.log(1024)));
  const value = bytes / Math.pow(1024, i);
  return `${value.toLocaleString(LOCALE, { maximumFractionDigits: i === 0 ? 0 : digits })} ${units[i]}`;
}

export function formatNumber(n: number): string {
  return n.toLocaleString(LOCALE);
}

function toDate(value: string | Date): Date | null {
  const d = value instanceof Date ? value : new Date(value);
  return Number.isNaN(d.getTime()) ? null : d;
}

export function formatDateTime(value: string | null | undefined): string {
  if (!value) return "–";
  const d = toDate(value);
  if (!d) return value;
  return d.toLocaleString(LOCALE, { dateStyle: "medium", timeStyle: "short" });
}

export function formatDate(value: string | null | undefined): string {
  if (!value) return "–";
  const d = toDate(value);
  if (!d) return value;
  return d.toLocaleDateString(LOCALE, { day: "2-digit", month: "2-digit", year: "numeric" });
}

export function formatTime(value: string): string {
  const d = toDate(value);
  if (!d) return value;
  return d.toLocaleTimeString(LOCALE, { hour: "2-digit", minute: "2-digit" });
}

export function isSameDay(a: Date, b: Date): boolean {
  return a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate();
}

/** Short timestamp for message lists: time today, "Gestern", day/month this year, otherwise date. */
export function formatShort(value: string): string {
  const d = toDate(value);
  if (!d) return value;
  const now = new Date();
  if (isSameDay(d, now)) return formatTime(value);
  const yesterday = new Date(now);
  yesterday.setDate(now.getDate() - 1);
  if (isSameDay(d, yesterday)) return "Gestern";
  const diffDays = (now.getTime() - d.getTime()) / 86_400_000;
  if (diffDays < 6 && diffDays > 0) return d.toLocaleDateString(LOCALE, { weekday: "short" });
  if (d.getFullYear() === now.getFullYear()) {
    return d.toLocaleDateString(LOCALE, { day: "numeric", month: "short" });
  }
  return d.toLocaleDateString(LOCALE, { day: "2-digit", month: "2-digit", year: "2-digit" });
}

const rtf = new Intl.RelativeTimeFormat(LOCALE, { numeric: "auto" });

export function formatRelative(value: string): string {
  const d = toDate(value);
  if (!d) return value;
  const diff = (d.getTime() - Date.now()) / 1000;
  const abs = Math.abs(diff);
  if (abs < 45) return diff < 0 ? "gerade eben" : "gleich";
  if (abs < 3600) return rtf.format(Math.round(diff / 60), "minute");
  if (abs < 86_400) return rtf.format(Math.round(diff / 3600), "hour");
  if (abs < 86_400 * 30) return rtf.format(Math.round(diff / 86_400), "day");
  if (abs < 86_400 * 365) return rtf.format(Math.round(diff / (86_400 * 30)), "month");
  return rtf.format(Math.round(diff / (86_400 * 365)), "year");
}

export function formatDuration(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return "–";
  const d = Math.floor(seconds / 86_400);
  const h = Math.floor((seconds % 86_400) / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  if (d > 0) return `${d} T ${h} Std`;
  if (h > 0) return `${h} Std ${m} Min`;
  if (m > 0) return `${m} Min`;
  return `${Math.floor(seconds)} s`;
}

/** Hour label for chart axes; tolerates both ISO timestamps and plain labels. */
export function formatHourLabel(value: string): string {
  const d = toDate(value);
  if (!d || !/\d{4}-\d{2}-\d{2}/.test(value)) return value;
  return d.toLocaleTimeString(LOCALE, { hour: "2-digit", minute: "2-digit" });
}

/** Value for <input type="date"> from an RFC 3339 string. */
export function toDateInput(value: string | null | undefined): string {
  if (!value) return "";
  const d = toDate(value);
  if (!d) return "";
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

/** Value for <input type="time"> from an RFC 3339 string. */
export function toTimeInput(value: string | null | undefined): string {
  if (!value) return "";
  const d = toDate(value);
  if (!d) return "";
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** Combine local date ("YYYY-MM-DD") and optional time ("HH:MM") into an RFC 3339 UTC string. */
export function fromDateTimeInput(date: string, time?: string): string | null {
  if (!date) return null;
  const [y, m, d] = date.split("-").map(Number);
  const [hh, mm] = (time || "00:00").split(":").map(Number);
  const dt = new Date(y, (m ?? 1) - 1, d ?? 1, hh ?? 0, mm ?? 0, 0, 0);
  if (Number.isNaN(dt.getTime())) return null;
  return dt.toISOString();
}

export function pluralize(n: number, one: string, many: string): string {
  return `${formatNumber(n)} ${n === 1 ? one : many}`;
}
