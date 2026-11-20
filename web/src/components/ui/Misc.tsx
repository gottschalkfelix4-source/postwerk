import { useState, type ReactNode } from "react";
import { Check, ChevronLeft, ChevronRight, Copy } from "lucide-react";
import { cn, copyToClipboard, hashString, initials } from "@/lib/utils";
import { formatNumber } from "@/lib/format";
import { Button } from "./Button";

export function Pagination({
  total,
  limit,
  offset,
  onChange,
  className,
}: {
  total: number;
  limit: number;
  offset: number;
  onChange: (offset: number) => void;
  className?: string;
}) {
  const from = total === 0 ? 0 : offset + 1;
  const to = Math.min(total, offset + limit);
  const page = Math.floor(offset / limit) + 1;
  const pages = Math.max(1, Math.ceil(total / limit));
  return (
    <div className={cn("flex items-center justify-between gap-3 px-4 py-3 text-sm text-zinc-500 dark:text-zinc-400", className)}>
      <span>
        {formatNumber(from)}–{formatNumber(to)} von {formatNumber(total)}
      </span>
      <div className="flex items-center gap-2">
        <span className="hidden text-xs sm:inline">
          Seite {page} von {pages}
        </span>
        <Button
          size="icon-sm"
          variant="secondary"
          icon={ChevronLeft}
          disabled={offset === 0}
          onClick={() => onChange(Math.max(0, offset - limit))}
          aria-label="Vorherige Seite"
        />
        <Button
          size="icon-sm"
          variant="secondary"
          icon={ChevronRight}
          disabled={offset + limit >= total}
          onClick={() => onChange(offset + limit)}
          aria-label="Nächste Seite"
        />
      </div>
    </div>
  );
}

export function Progress({ value, className, tone }: { value: number; className?: string; tone?: "auto" | "indigo" }) {
  const pct = Math.max(0, Math.min(1, value)) * 100;
  const color =
    tone === "indigo"
      ? "bg-indigo-500"
      : pct >= 90
        ? "bg-red-500"
        : pct >= 75
          ? "bg-amber-500"
          : "bg-indigo-500";
  return (
    <div className={cn("h-1.5 w-full overflow-hidden rounded-full bg-zinc-200 dark:bg-zinc-800", className)}>
      <div className={cn("h-full rounded-full transition-all", color)} style={{ width: `${pct}%` }} />
    </div>
  );
}

export function CopyButton({ value, className, label }: { value: string; className?: string; label?: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      onClick={async (e) => {
        e.stopPropagation();
        if (await copyToClipboard(value)) {
          setCopied(true);
          window.setTimeout(() => setCopied(false), 1500);
        }
      }}
      className={cn(
        "inline-flex shrink-0 items-center gap-1.5 rounded-md px-1.5 py-1 text-xs font-medium transition-colors",
        copied
          ? "text-emerald-600 dark:text-emerald-400"
          : "text-zinc-400 hover:bg-zinc-100 hover:text-zinc-700 dark:hover:bg-zinc-800 dark:hover:text-zinc-200",
        className,
      )}
      title={copied ? "Kopiert" : "In die Zwischenablage kopieren"}
      aria-label="Kopieren"
    >
      {copied ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
      {label && <span>{copied ? "Kopiert" : label}</span>}
    </button>
  );
}

const avatarColors = [
  "bg-indigo-100 text-indigo-700 dark:bg-indigo-500/20 dark:text-indigo-300",
  "bg-emerald-100 text-emerald-700 dark:bg-emerald-500/20 dark:text-emerald-300",
  "bg-amber-100 text-amber-800 dark:bg-amber-500/20 dark:text-amber-300",
  "bg-sky-100 text-sky-700 dark:bg-sky-500/20 dark:text-sky-300",
  "bg-violet-100 text-violet-700 dark:bg-violet-500/20 dark:text-violet-300",
  "bg-pink-100 text-pink-700 dark:bg-pink-500/20 dark:text-pink-300",
  "bg-teal-100 text-teal-700 dark:bg-teal-500/20 dark:text-teal-300",
  "bg-orange-100 text-orange-700 dark:bg-orange-500/20 dark:text-orange-300",
];

export function Avatar({
  name,
  email,
  size = "md",
  className,
}: {
  name?: string;
  email?: string;
  size?: "xs" | "sm" | "md" | "lg";
  className?: string;
}) {
  const key = (email || name || "?").toLowerCase();
  const sizes = { xs: "h-6 w-6 text-[10px]", sm: "h-8 w-8 text-xs", md: "h-9 w-9 text-sm", lg: "h-11 w-11 text-base" };
  return (
    <span
      className={cn(
        "inline-flex shrink-0 select-none items-center justify-center rounded-full font-semibold",
        avatarColors[hashString(key) % avatarColors.length],
        sizes[size],
        className,
      )}
      aria-hidden
    >
      {initials(name || email || "?")}
    </span>
  );
}

export function PageHeader({
  title,
  description,
  actions,
  className,
}: {
  title: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("mb-6 flex flex-wrap items-end justify-between gap-4", className)}>
      <div className="min-w-0">
        <h1 className="text-xl font-semibold tracking-tight text-zinc-900 dark:text-zinc-50 sm:text-2xl">{title}</h1>
        {description && <p className="mt-1 text-sm text-zinc-500 dark:text-zinc-400">{description}</p>}
      </div>
      {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
    </div>
  );
}

/** Sticky save bar shown at the bottom right of forms with unsaved changes. */
export function SaveBar({
  dirty,
  saving,
  onSave,
  onReset,
  label = "Speichern",
}: {
  dirty: boolean;
  saving?: boolean;
  onSave: () => void;
  onReset: () => void;
  label?: string;
}) {
  return (
    <div className={cn("z-10 -mx-1 mt-6 flex justify-end px-1 pb-4 pt-2", dirty && "sticky bottom-0")}>
      <div
        className={cn(
          "flex items-center gap-3 rounded-xl border bg-white/95 py-2 pl-4 pr-2 shadow-pop backdrop-blur transition dark:bg-zinc-900/95",
          dirty ? "border-indigo-200 dark:border-indigo-500/30" : "border-zinc-200 dark:border-zinc-800",
        )}
      >
        <span className="text-sm text-zinc-500 dark:text-zinc-400">
          {dirty ? "Ungespeicherte Änderungen" : "Alle Änderungen gespeichert"}
        </span>
        <Button size="sm" variant="ghost" onClick={onReset} disabled={!dirty || saving}>
          Verwerfen
        </Button>
        <Button size="sm" variant="primary" onClick={onSave} disabled={!dirty} loading={saving}>
          {label}
        </Button>
      </div>
    </div>
  );
}

export function Kbd({ children }: { children: ReactNode }) {
  return (
    <kbd className="inline-flex h-5 min-w-[1.25rem] items-center justify-center rounded border border-zinc-200 bg-zinc-50 px-1 font-sans text-[11px] font-medium text-zinc-500 dark:border-zinc-700 dark:bg-zinc-800 dark:text-zinc-400">
      {children}
    </kbd>
  );
}

/** Small metric tile used on the dashboard and detail pages. */
export function StatTile({
  label,
  value,
  hint,
  icon,
  className,
}: {
  label: ReactNode;
  value: ReactNode;
  hint?: ReactNode;
  icon?: ReactNode;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "rounded-xl border border-zinc-200 bg-white p-4 shadow-card dark:border-zinc-800 dark:bg-zinc-900",
        className,
      )}
    >
      <div className="flex items-center justify-between gap-2">
        <span className="text-xs font-medium uppercase tracking-wide text-zinc-500 dark:text-zinc-400">{label}</span>
        {icon}
      </div>
      <div className="mt-2 text-2xl font-semibold tabular-nums tracking-tight text-zinc-900 dark:text-zinc-50">{value}</div>
      {hint && <div className="mt-1 text-xs text-zinc-500 dark:text-zinc-400">{hint}</div>}
    </div>
  );
}
