import { useEffect, useState } from "react";
import type { LogEntry } from "@/api/types";
import { Badge, Input, Select, type BadgeTone } from "@/components/ui";
import { formatBytes } from "@/lib/format";
import { cn } from "@/lib/utils";

const levelTones: Record<LogEntry["level"], BadgeTone> = {
  debug: "neutral",
  info: "blue",
  warn: "amber",
  error: "red",
};

const levelLabels: Record<LogEntry["level"], string> = {
  debug: "DEBUG",
  info: "INFO",
  warn: "WARN",
  error: "ERROR",
};

export function LevelBadge({ level, className }: { level: LogEntry["level"]; className?: string }) {
  return (
    <Badge tone={levelTones[level] ?? "neutral"} className={cn("w-14 justify-center font-mono text-[10px] tracking-wide", className)}>
      {levelLabels[level] ?? String(level).toUpperCase()}
    </Badge>
  );
}

export const levelTextClass: Record<LogEntry["level"], string> = {
  debug: "text-zinc-400 dark:text-zinc-500",
  info: "text-sky-600 dark:text-sky-400",
  warn: "text-amber-600 dark:text-amber-400",
  error: "text-red-600 dark:text-red-400",
};

const MB = 1024 * 1024;
const GB = 1024 * MB;

/** Quota editor in MB/GB; 0 means unlimited. */
export function QuotaInput({ id, value, onChange }: { id?: string; value: number; onChange: (bytes: number) => void }) {
  const initialUnit = value > 0 && value % GB === 0 ? "GB" : value > 0 ? "MB" : "GB";
  const [unit, setUnit] = useState<"MB" | "GB">(initialUnit);
  const [text, setText] = useState(() => (value > 0 ? String(value / (initialUnit === "GB" ? GB : MB)) : "0"));

  useEffect(() => {
    const current = Number(text.replace(",", ".")) * (unit === "GB" ? GB : MB);
    if (Math.round(current) !== value) {
      const u = value > 0 && value % GB === 0 ? "GB" : value > 0 ? "MB" : unit;
      setUnit(u);
      setText(value > 0 ? String(+(value / (u === "GB" ? GB : MB)).toFixed(2)) : "0");
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [value]);

  const emit = (t: string, u: "MB" | "GB") => {
    const n = Number(t.replace(",", "."));
    onChange(Number.isFinite(n) && n > 0 ? Math.round(n * (u === "GB" ? GB : MB)) : 0);
  };

  return (
    <div className="flex gap-2">
      <Input
        id={id}
        type="number"
        min={0}
        step="any"
        value={text}
        onChange={(e) => {
          setText(e.target.value);
          emit(e.target.value, unit);
        }}
        className="w-32"
      />
      <Select
        value={unit}
        onChange={(e) => {
          const u = e.target.value as "MB" | "GB";
          setUnit(u);
          emit(text, u);
        }}
        options={[
          { value: "MB", label: "MB" },
          { value: "GB", label: "GB" },
        ]}
        className="w-24"
      />
    </div>
  );
}

export function quotaLabel(used: number, quota: number): string {
  return quota > 0 ? `${formatBytes(used)} von ${formatBytes(quota)}` : `${formatBytes(used)} · unbegrenzt`;
}

export function generatePassword(length = 20): string {
  const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789-_!.";
  const bytes = new Uint32Array(length);
  crypto.getRandomValues(bytes);
  return Array.from(bytes, (b) => alphabet[b % alphabet.length]).join("");
}
