import { createContext, useCallback, useContext, useMemo, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { CircleCheck, CircleX, Info, X } from "lucide-react";
import { cn } from "@/lib/utils";
import { errorMessage } from "@/api/client";

type ToastKind = "success" | "error" | "info";

interface ToastItem {
  id: number;
  kind: ToastKind;
  title: string;
  description?: string;
  action?: { label: string; onClick: () => void };
}

export interface ToastOptions {
  /** Optional action button inside the toast (e.g. Undo Send). */
  action?: { label: string; onClick: () => void };
}

interface ToastApi {
  success: (title: string, description?: string | ToastOptions) => void;
  error: (titleOrError: unknown, description?: string) => void;
  info: (title: string, description?: string | ToastOptions) => void;
}

const ToastContext = createContext<ToastApi | null>(null);

const icons = {
  success: <CircleCheck className="h-5 w-5 text-emerald-500" />,
  error: <CircleX className="h-5 w-5 text-red-500" />,
  info: <Info className="h-5 w-5 text-indigo-500" />,
};

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<ToastItem[]>([]);
  const nextId = useRef(1);

  const dismiss = useCallback((id: number) => {
    setToasts((list) => list.filter((t) => t.id !== id));
  }, []);

  const push = useCallback(
    (kind: ToastKind, title: string, description?: string | ToastOptions) => {
      const opts: ToastOptions = typeof description === "object" ? description : {};
      const text = typeof description === "string" ? description : undefined;
      const id = nextId.current++;
      setToasts((list) => [...list.slice(-4), { id, kind, title, description: text, action: opts.action }]);
      window.setTimeout(() => dismiss(id), kind === "error" ? 7000 : opts.action ? 10000 : 4000);
    },
    [dismiss],
  );

  const api = useMemo<ToastApi>(
    () => ({
      success: (title, description) => push("success", title, description),
      info: (title, description) => push("info", title, description),
      error: (titleOrError, description) => {
        if (typeof titleOrError === "string") push("error", titleOrError, description);
        else push("error", description ?? "Fehler", errorMessage(titleOrError));
      },
    }),
    [push],
  );

  return (
    <ToastContext.Provider value={api}>
      {children}
      {createPortal(
        <div className="pointer-events-none fixed inset-x-0 bottom-0 z-[60] flex flex-col items-center gap-2 p-4 sm:items-end">
          {toasts.map((t) => (
            <div
              key={t.id}
              role={t.kind === "error" ? "alert" : "status"}
              className={cn(
                "pointer-events-auto flex w-full max-w-sm animate-toast-in items-start gap-3 rounded-xl border bg-white p-3.5 shadow-pop",
                "border-zinc-200 dark:border-zinc-700 dark:bg-zinc-800",
              )}
            >
              <div className="mt-px">{icons[t.kind]}</div>
              <div className="min-w-0 flex-1">
                <p className="text-sm font-medium text-zinc-900 dark:text-zinc-50">{t.title}</p>
                {t.description && <p className="mt-0.5 break-words text-sm text-zinc-500 dark:text-zinc-400">{t.description}</p>}
                {t.action && (
                  <button
                    type="button"
                    onClick={() => {
                      t.action?.onClick();
                      dismiss(t.id);
                    }}
                    className="mt-1.5 rounded-lg border border-zinc-200 px-2.5 py-1 text-xs font-medium text-zinc-700 hover:bg-zinc-100 dark:border-zinc-600 dark:text-zinc-200 dark:hover:bg-zinc-700"
                  >
                    {t.action.label}
                  </button>
                )}
              </div>
              <button
                type="button"
                onClick={() => dismiss(t.id)}
                className="rounded-md p-1 text-zinc-400 hover:bg-zinc-100 hover:text-zinc-600 dark:hover:bg-zinc-700 dark:hover:text-zinc-200"
                aria-label="Schließen"
              >
                <X className="h-4 w-4" />
              </button>
            </div>
          ))}
        </div>,
        document.body,
      )}
    </ToastContext.Provider>
  );
}

export function useToast(): ToastApi {
  const ctx = useContext(ToastContext);
  if (!ctx) throw new Error("useToast must be used inside ToastProvider");
  return ctx;
}
