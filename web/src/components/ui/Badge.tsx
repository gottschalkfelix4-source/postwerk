import type { ReactNode } from "react";
import type { LucideIcon } from "lucide-react";
import { cn, hashString } from "@/lib/utils";

export type BadgeTone = "neutral" | "indigo" | "green" | "red" | "amber" | "blue" | "purple" | "pink" | "teal" | "orange";

const tones: Record<BadgeTone, string> = {
  neutral: "bg-zinc-100 text-zinc-700 ring-zinc-500/15 dark:bg-zinc-800 dark:text-zinc-300 dark:ring-zinc-400/20",
  indigo: "bg-indigo-50 text-indigo-700 ring-indigo-600/15 dark:bg-indigo-500/15 dark:text-indigo-300 dark:ring-indigo-400/25",
  green: "bg-emerald-50 text-emerald-700 ring-emerald-600/15 dark:bg-emerald-500/15 dark:text-emerald-300 dark:ring-emerald-400/25",
  red: "bg-red-50 text-red-700 ring-red-600/15 dark:bg-red-500/15 dark:text-red-300 dark:ring-red-400/25",
  amber: "bg-amber-50 text-amber-800 ring-amber-600/20 dark:bg-amber-500/15 dark:text-amber-300 dark:ring-amber-400/25",
  blue: "bg-sky-50 text-sky-700 ring-sky-600/15 dark:bg-sky-500/15 dark:text-sky-300 dark:ring-sky-400/25",
  purple: "bg-violet-50 text-violet-700 ring-violet-600/15 dark:bg-violet-500/15 dark:text-violet-300 dark:ring-violet-400/25",
  pink: "bg-pink-50 text-pink-700 ring-pink-600/15 dark:bg-pink-500/15 dark:text-pink-300 dark:ring-pink-400/25",
  teal: "bg-teal-50 text-teal-700 ring-teal-600/15 dark:bg-teal-500/15 dark:text-teal-300 dark:ring-teal-400/25",
  orange: "bg-orange-50 text-orange-700 ring-orange-600/15 dark:bg-orange-500/15 dark:text-orange-300 dark:ring-orange-400/25",
};

const dots: Record<BadgeTone, string> = {
  neutral: "bg-zinc-400",
  indigo: "bg-indigo-500",
  green: "bg-emerald-500",
  red: "bg-red-500",
  amber: "bg-amber-500",
  blue: "bg-sky-500",
  purple: "bg-violet-500",
  pink: "bg-pink-500",
  teal: "bg-teal-500",
  orange: "bg-orange-500",
};

export function Badge({
  tone = "neutral",
  children,
  dot,
  icon: Icon,
  className,
  title,
}: {
  tone?: BadgeTone;
  children: ReactNode;
  dot?: boolean;
  icon?: LucideIcon;
  className?: string;
  title?: string;
}) {
  return (
    <span
      title={title}
      className={cn(
        "inline-flex max-w-full items-center gap-1.5 whitespace-nowrap rounded-md px-1.5 py-0.5 text-xs font-medium ring-1 ring-inset",
        tones[tone],
        className,
      )}
    >
      {dot && <span className={cn("h-1.5 w-1.5 shrink-0 rounded-full", dots[tone])} />}
      {Icon && <Icon className="h-3 w-3 shrink-0" />}
      <span className="truncate">{children}</span>
    </span>
  );
}

const categoryTones: BadgeTone[] = ["indigo", "teal", "amber", "purple", "blue", "pink", "orange", "green"];

/** Stable color for a free-form label like an AI category or tag. */
export function toneFor(label: string): BadgeTone {
  return categoryTones[hashString(label.toLowerCase()) % categoryTones.length];
}

export function dotClass(tone: BadgeTone): string {
  return dots[tone];
}
