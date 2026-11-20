import type { HTMLAttributes, ReactNode, TdHTMLAttributes, ThHTMLAttributes } from "react";
import { cn } from "@/lib/utils";

export function Table({ className, children }: { className?: string; children: ReactNode }) {
  return (
    <div className={cn("overflow-x-auto", className)}>
      <table className="min-w-full border-separate border-spacing-0 text-sm">{children}</table>
    </div>
  );
}

export function THead({ children }: { children: ReactNode }) {
  return <thead className="bg-zinc-50/80 dark:bg-zinc-900/60">{children}</thead>;
}

export function TBody({ children }: { children: ReactNode }) {
  return <tbody className="[&>tr:last-child>td]:border-b-0">{children}</tbody>;
}

export function Tr({ className, selected, ...rest }: HTMLAttributes<HTMLTableRowElement> & { selected?: boolean }) {
  return (
    <tr
      className={cn(
        "group transition-colors",
        selected ? "bg-indigo-50/60 dark:bg-indigo-500/[0.07]" : "hover:bg-zinc-50/80 dark:hover:bg-zinc-800/40",
        rest.onClick && "cursor-pointer",
        className,
      )}
      {...rest}
    />
  );
}

export function Th({ className, ...rest }: ThHTMLAttributes<HTMLTableCellElement>) {
  return (
    <th
      className={cn(
        "whitespace-nowrap border-b border-zinc-200 px-4 py-2.5 text-left text-xs font-semibold uppercase tracking-wide text-zinc-500 dark:border-zinc-800 dark:text-zinc-400",
        className,
      )}
      {...rest}
    />
  );
}

export function Td({ className, ...rest }: TdHTMLAttributes<HTMLTableCellElement>) {
  return (
    <td
      className={cn("border-b border-zinc-100 px-4 py-3 align-middle text-zinc-700 dark:border-zinc-800/80 dark:text-zinc-300", className)}
      {...rest}
    />
  );
}

/** Search/filter row above a table, and the selection action bar. */
export function TableToolbar({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <div
      className={cn(
        "flex flex-wrap items-center gap-2 border-b border-zinc-100 px-4 py-3 dark:border-zinc-800",
        className,
      )}
    >
      {children}
    </div>
  );
}

export function SelectionBar({ count, children, onClear }: { count: number; children: ReactNode; onClear: () => void }) {
  if (count === 0) return null;
  return (
    <div className="flex flex-wrap items-center gap-2 border-b border-indigo-100 bg-indigo-50/70 px-4 py-2 text-sm dark:border-indigo-500/20 dark:bg-indigo-500/10">
      <span className="font-medium text-indigo-700 dark:text-indigo-300">{count} ausgewählt</span>
      <button type="button" onClick={onClear} className="text-xs text-indigo-600 hover:underline dark:text-indigo-300">
        Auswahl aufheben
      </button>
      <div className="ml-auto flex flex-wrap items-center gap-1.5">{children}</div>
    </div>
  );
}
