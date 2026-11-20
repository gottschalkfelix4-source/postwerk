import type { ComponentType, ReactNode } from "react";
import { Check } from "lucide-react";
import { cn } from "@/lib/utils";

export interface StepItem {
  id: string;
  label: string;
  description?: string;
}

type StepState = "done" | "current" | "open";

function stateOf(index: number, current: number, reached: number): StepState {
  if (index === current) return "current";
  return index < current || index < reached ? "done" : "open";
}

function StepCircle({ state, index }: { state: StepState; index: number }) {
  return (
    <span
      className={cn(
        "relative z-10 flex h-7 w-7 shrink-0 items-center justify-center rounded-full text-xs font-semibold transition-colors",
        state === "done" && "bg-indigo-600 text-white dark:bg-indigo-500",
        state === "current" &&
          "bg-white text-indigo-700 ring-2 ring-indigo-500 dark:bg-zinc-900 dark:text-indigo-300 dark:ring-indigo-400",
        state === "open" && "bg-white text-zinc-400 ring-1 ring-inset ring-zinc-300 dark:bg-zinc-900 dark:text-zinc-500 dark:ring-zinc-700",
      )}
    >
      {state === "done" ? <Check className="h-3.5 w-3.5" strokeWidth={3} /> : index + 1}
    </span>
  );
}

/**
 * Vertical list of wizard steps with done/current/open state.
 * Steps up to `reached` can be selected when `onSelect` is given.
 */
export function StepList({
  steps,
  current,
  reached,
  onSelect,
  className,
}: {
  steps: StepItem[];
  current: number;
  reached: number;
  onSelect?: (index: number) => void;
  className?: string;
}) {
  return (
    <ol className={cn("relative", className)}>
      {steps.map((step, i) => {
        const state = stateOf(i, current, reached);
        const clickable = !!onSelect && i !== current && i <= reached;
        const last = i === steps.length - 1;
        return (
          <li key={step.id} className="relative pb-5 last:pb-0">
            {!last && (
              <span
                aria-hidden
                className={cn(
                  "absolute left-[13px] top-7 h-[calc(100%-1.75rem)] w-0.5 rounded-full transition-colors",
                  i < current ? "bg-indigo-500/70 dark:bg-indigo-400/60" : "bg-zinc-200 dark:bg-zinc-800",
                )}
              />
            )}
            <button
              type="button"
              disabled={!clickable}
              onClick={() => clickable && onSelect?.(i)}
              aria-current={state === "current" ? "step" : undefined}
              className={cn(
                "group flex w-full items-start gap-3 rounded-lg text-left",
                clickable ? "cursor-pointer" : "cursor-default",
                "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-indigo-500/50",
              )}
            >
              <StepCircle state={state} index={i} />
              <span className="min-w-0 pt-0.5">
                <span
                  className={cn(
                    "block text-sm font-medium transition-colors",
                    state === "current"
                      ? "text-zinc-900 dark:text-zinc-50"
                      : state === "done"
                        ? "text-zinc-700 group-hover:text-indigo-700 dark:text-zinc-300 dark:group-hover:text-indigo-300"
                        : "text-zinc-500 dark:text-zinc-500",
                  )}
                >
                  {step.label}
                </span>
                {step.description && (
                  <span className="mt-0.5 block text-xs leading-snug text-zinc-500 dark:text-zinc-400">{step.description}</span>
                )}
              </span>
            </button>
          </li>
        );
      })}
    </ol>
  );
}

/** Compact horizontal step indicator for narrow screens. */
export function StepBar({
  steps,
  current,
  reached,
  onSelect,
  className,
}: {
  steps: StepItem[];
  current: number;
  reached: number;
  onSelect?: (index: number) => void;
  className?: string;
}) {
  return (
    <div className={cn("space-y-2", className)}>
      <div className="flex items-baseline justify-between gap-3 text-xs">
        <span className="font-medium text-zinc-500 dark:text-zinc-400">
          Schritt {current + 1} von {steps.length}
        </span>
        <span className="truncate font-semibold text-zinc-900 dark:text-zinc-100">{steps[current]?.label}</span>
      </div>
      <div className="flex gap-1">
        {steps.map((step, i) => {
          const clickable = !!onSelect && i !== current && i <= reached;
          return (
            <button
              key={step.id}
              type="button"
              disabled={!clickable}
              onClick={() => clickable && onSelect?.(i)}
              aria-label={`${i + 1}. ${step.label}`}
              className={cn(
                "h-1.5 flex-1 rounded-full transition-colors",
                i < current || i < reached
                  ? "bg-indigo-500 dark:bg-indigo-400"
                  : i === current
                    ? "bg-indigo-300 dark:bg-indigo-500/60"
                    : "bg-zinc-200 dark:bg-zinc-800",
              )}
            />
          );
        })}
      </div>
    </div>
  );
}

/** Inline notice box used across the wizards. */
export function Notice({
  tone = "info",
  icon: Icon,
  title,
  children,
  className,
}: {
  tone?: "info" | "warning" | "error" | "success";
  icon?: ComponentType<{ className?: string }>;
  title?: ReactNode;
  children?: ReactNode;
  className?: string;
}) {
  const tones = {
    info: "border-indigo-200 bg-indigo-50/60 text-indigo-900 dark:border-indigo-500/25 dark:bg-indigo-500/[0.07] dark:text-indigo-100",
    warning: "border-amber-200 bg-amber-50/70 text-amber-900 dark:border-amber-500/25 dark:bg-amber-500/[0.07] dark:text-amber-100",
    error: "border-red-200 bg-red-50/70 text-red-800 dark:border-red-500/30 dark:bg-red-500/10 dark:text-red-200",
    success: "border-emerald-200 bg-emerald-50/60 text-emerald-900 dark:border-emerald-500/25 dark:bg-emerald-500/[0.07] dark:text-emerald-100",
  };
  const iconTones = {
    info: "text-indigo-500 dark:text-indigo-300",
    warning: "text-amber-500 dark:text-amber-300",
    error: "text-red-500 dark:text-red-300",
    success: "text-emerald-500 dark:text-emerald-300",
  };
  return (
    <div className={cn("flex gap-3 rounded-lg border px-3.5 py-3 text-sm", tones[tone], className)} role={tone === "error" ? "alert" : undefined}>
      {Icon && <Icon className={cn("mt-0.5 h-4 w-4 shrink-0", iconTones[tone])} />}
      <div className="min-w-0 space-y-1 leading-relaxed">
        {title && <div className="font-medium">{title}</div>}
        {children && <div className={cn(title && "opacity-90")}>{children}</div>}
      </div>
    </div>
  );
}
