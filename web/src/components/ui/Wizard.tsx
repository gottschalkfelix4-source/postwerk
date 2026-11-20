import type { ReactNode } from "react";
import { ArrowLeft, ArrowRight, type LucideIcon } from "lucide-react";
import { cn } from "@/lib/utils";
import { Button } from "./Button";
import { Card } from "./Card";
import { StepBar, StepList, type StepItem } from "./Stepper";

/** Step list on the left (compact bar on mobile), animated step content on the right. */
export function WizardLayout({
  steps,
  current,
  reached,
  direction = "forward",
  onSelect,
  aside,
  children,
  className,
  breakpoint = "lg",
}: {
  steps: StepItem[];
  current: number;
  reached: number;
  direction?: "forward" | "back";
  onSelect?: (index: number) => void;
  /** Extra content below the step list on wide screens. */
  aside?: ReactNode;
  children: ReactNode;
  className?: string;
  /** Width from which the vertical step list replaces the compact bar. */
  breakpoint?: "lg" | "xl";
}) {
  const xl = breakpoint === "xl";
  return (
    <div className={cn("grid gap-5", xl ? "xl:grid-cols-[15rem_minmax(0,1fr)] xl:gap-8" : "lg:grid-cols-[15rem_minmax(0,1fr)] lg:gap-8", className)}>
      <div className="min-w-0">
        <StepBar steps={steps} current={current} reached={reached} onSelect={onSelect} className={xl ? "xl:hidden" : "lg:hidden"} />
        <div className={cn("sticky top-6 hidden", xl ? "xl:block" : "lg:block")}>
          <StepList steps={steps} current={current} reached={reached} onSelect={onSelect} />
          {aside}
        </div>
      </div>
      <div key={current} className={cn("min-w-0", direction === "back" ? "animate-step-in-back" : "animate-step-in")}>
        {children}
      </div>
    </div>
  );
}

/** Card frame of a single wizard step. */
export function WizardStep({
  title,
  description,
  icon: Icon,
  children,
  footer,
  className,
}: {
  title: ReactNode;
  description?: ReactNode;
  icon?: LucideIcon;
  children: ReactNode;
  footer?: ReactNode;
  className?: string;
}) {
  return (
    <Card className={cn("overflow-hidden", className)}>
      <div className="flex items-start gap-4 border-b border-zinc-100 px-5 py-5 dark:border-zinc-800 sm:px-6">
        {Icon && (
          <span className="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl bg-indigo-50 text-indigo-600 dark:bg-indigo-500/15 dark:text-indigo-300">
            <Icon className="h-5 w-5" />
          </span>
        )}
        <div className="min-w-0">
          <h2 className="text-lg font-semibold tracking-tight text-zinc-900 dark:text-zinc-50">{title}</h2>
          {description && <p className="mt-0.5 text-sm text-zinc-500 dark:text-zinc-400">{description}</p>}
        </div>
      </div>
      <div className="px-5 py-5 sm:px-6">{children}</div>
      {footer && (
        <div className="border-t border-zinc-100 bg-zinc-50/70 px-5 py-3 dark:border-zinc-800 dark:bg-zinc-900/40 sm:px-6">{footer}</div>
      )}
    </Card>
  );
}

/** Back / next buttons at the bottom of a wizard step. */
export function WizardNav({
  onBack,
  onNext,
  nextLabel = "Weiter",
  backLabel = "Zurück",
  nextDisabled,
  loading,
  nextIcon = ArrowRight,
  extra,
  form,
}: {
  onBack?: () => void;
  onNext?: () => void;
  nextLabel?: string;
  backLabel?: string;
  nextDisabled?: boolean;
  loading?: boolean;
  nextIcon?: LucideIcon | null;
  /** Additional controls shown left of the next button. */
  extra?: ReactNode;
  /** Id of a form the next button submits (enables submit on Enter inside that form). */
  form?: string;
}) {
  return (
    <div className="flex flex-wrap items-center justify-between gap-2">
      <div>
        {onBack && (
          <Button variant="ghost" icon={ArrowLeft} onClick={onBack} disabled={loading}>
            {backLabel}
          </Button>
        )}
      </div>
      <div className="flex flex-wrap items-center gap-2">
        {extra}
        {(onNext || form) && (
          <Button
            type={form ? "submit" : "button"}
            form={form}
            variant="primary"
            iconRight={nextIcon ?? undefined}
            onClick={form ? undefined : onNext}
            disabled={nextDisabled}
            loading={loading}
          >
            {nextLabel}
          </Button>
        )}
      </div>
    </div>
  );
}
