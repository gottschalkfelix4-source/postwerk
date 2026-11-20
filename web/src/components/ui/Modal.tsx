import { useEffect, useRef, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { X } from "lucide-react";
import { cn } from "@/lib/utils";

export interface ModalProps {
  open: boolean;
  onClose: () => void;
  title?: ReactNode;
  description?: ReactNode;
  icon?: ReactNode;
  children?: ReactNode;
  footer?: ReactNode;
  size?: "sm" | "md" | "lg" | "xl";
  /** Render as a panel sliding in from the right instead of a centered dialog. */
  variant?: "dialog" | "sheet";
  /** Prevent closing via backdrop click (e.g. forms with unsaved input). */
  dismissible?: boolean;
  className?: string;
  bodyClassName?: string;
}

const sizes = {
  sm: "max-w-sm",
  md: "max-w-lg",
  lg: "max-w-2xl",
  xl: "max-w-4xl",
};

let openCount = 0;
const modalStack: symbol[] = [];

export function Modal({
  open,
  onClose,
  title,
  description,
  icon,
  children,
  footer,
  size = "md",
  variant = "dialog",
  dismissible = true,
  className,
  bodyClassName,
}: ModalProps) {
  const panelRef = useRef<HTMLDivElement>(null);
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;

  useEffect(() => {
    if (!open) return;
    openCount++;
    const token = Symbol("modal");
    modalStack.push(token);
    document.body.style.overflow = "hidden";
    document.body.dataset.modalOpen = "true";
    const previous = document.activeElement as HTMLElement | null;
    const t = window.setTimeout(() => {
      const panel = panelRef.current;
      if (!panel) return;
      const target =
        panel.querySelector<HTMLElement>("[data-autofocus]") ??
        panel.querySelector<HTMLElement>("input:not([type=hidden]):not([disabled]), textarea, select") ??
        panel;
      target.focus();
    }, 30);
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape" && modalStack[modalStack.length - 1] === token) {
        e.stopPropagation();
        onCloseRef.current();
      }
    };
    document.addEventListener("keydown", onKey);
    return () => {
      window.clearTimeout(t);
      document.removeEventListener("keydown", onKey);
      const idx = modalStack.indexOf(token);
      if (idx >= 0) modalStack.splice(idx, 1);
      openCount--;
      if (openCount <= 0) {
        openCount = 0;
        document.body.style.overflow = "";
        delete document.body.dataset.modalOpen;
      }
      previous?.focus?.();
    };
  }, [open]);

  if (!open) return null;

  const sheet = variant === "sheet";

  return createPortal(
    <div className={cn("fixed inset-0 z-50 flex", sheet ? "justify-end" : "items-end justify-center p-0 sm:items-center sm:p-4")}>
      <div
        className="absolute inset-0 animate-fade-in bg-zinc-950/40 backdrop-blur-[2px] dark:bg-black/60"
        onClick={() => dismissible && onClose()}
        aria-hidden
      />
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        tabIndex={-1}
        className={cn(
          "relative flex w-full flex-col bg-white shadow-pop outline-none dark:bg-zinc-900 dark:ring-1 dark:ring-zinc-800",
          sheet
            ? "h-full max-w-3xl animate-slide-in-right"
            : cn("max-h-[92vh] animate-scale-in rounded-t-2xl sm:rounded-2xl", sizes[size]),
          className,
        )}
      >
        {(title || description) && (
          <div className="flex items-start gap-3 border-b border-zinc-100 px-5 py-4 dark:border-zinc-800">
            {icon}
            <div className="min-w-0 flex-1">
              {title && <h2 className="text-base font-semibold text-zinc-900 dark:text-zinc-50">{title}</h2>}
              {description && <p className="mt-1 text-sm text-zinc-500 dark:text-zinc-400">{description}</p>}
            </div>
            <button
              type="button"
              onClick={onClose}
              className="-mr-1.5 -mt-1 rounded-md p-1.5 text-zinc-400 hover:bg-zinc-100 hover:text-zinc-700 dark:hover:bg-zinc-800 dark:hover:text-zinc-200"
              aria-label="Schließen"
            >
              <X className="h-4 w-4" />
            </button>
          </div>
        )}
        <div className={cn("min-h-0 flex-1 overflow-y-auto px-5 py-4", bodyClassName)}>{children}</div>
        {footer && (
          <div className="flex flex-wrap items-center justify-end gap-2 border-t border-zinc-100 bg-zinc-50/60 px-5 py-3 dark:border-zinc-800 dark:bg-zinc-900/60 sm:rounded-b-2xl">
            {footer}
          </div>
        )}
      </div>
    </div>,
    document.body,
  );
}
