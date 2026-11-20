import type { ReactNode } from "react";
import type { LucideIcon } from "lucide-react";
import { CircleAlert } from "lucide-react";
import { cn } from "@/lib/utils";
import { errorMessage } from "@/api/client";
import { Button } from "./Button";

export function EmptyState({
  icon: Icon,
  title,
  description,
  action,
  className,
  compact,
}: {
  icon?: LucideIcon;
  title: ReactNode;
  description?: ReactNode;
  action?: ReactNode;
  className?: string;
  compact?: boolean;
}) {
  return (
    <div className={cn("flex flex-col items-center justify-center text-center", compact ? "px-4 py-8" : "px-6 py-16", className)}>
      {Icon && (
        <div className="mb-4 flex h-12 w-12 items-center justify-center rounded-2xl bg-zinc-100 text-zinc-400 ring-1 ring-inset ring-zinc-200 dark:bg-zinc-800/80 dark:text-zinc-500 dark:ring-zinc-700">
          <Icon className="h-6 w-6" />
        </div>
      )}
      <h3 className="text-sm font-semibold text-zinc-900 dark:text-zinc-100">{title}</h3>
      {description && <p className="mt-1 max-w-sm text-sm text-zinc-500 dark:text-zinc-400">{description}</p>}
      {action && <div className="mt-5">{action}</div>}
    </div>
  );
}

export function ErrorState({ error, onRetry, className }: { error: unknown; onRetry?: () => void; className?: string }) {
  return (
    <EmptyState
      icon={CircleAlert}
      title="Daten konnten nicht geladen werden"
      description={errorMessage(error)}
      className={className}
      action={
        onRetry ? (
          <Button size="sm" onClick={onRetry}>
            Erneut versuchen
          </Button>
        ) : undefined
      }
    />
  );
}
