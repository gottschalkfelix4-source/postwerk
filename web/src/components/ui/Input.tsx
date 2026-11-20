import { forwardRef, useId, useState, type InputHTMLAttributes, type ReactNode, type TextareaHTMLAttributes } from "react";
import { Eye, EyeOff, type LucideIcon } from "lucide-react";
import { cn } from "@/lib/utils";

export const fieldBase =
  "block w-full rounded-lg border bg-white text-sm text-zinc-900 shadow-sm transition placeholder:text-zinc-400 " +
  "focus:outline-none focus:ring-2 focus:ring-indigo-500/30 focus:border-indigo-500 " +
  "disabled:cursor-not-allowed disabled:bg-zinc-50 disabled:text-zinc-500 " +
  "dark:bg-zinc-900 dark:text-zinc-100 dark:placeholder:text-zinc-500 dark:focus:border-indigo-400 dark:disabled:bg-zinc-800/50";

export const fieldBorder = (invalid?: boolean) =>
  invalid
    ? "border-red-400 focus:border-red-500 focus:ring-red-500/25 dark:border-red-500/70"
    : "border-zinc-300 dark:border-zinc-700";

export interface InputProps extends InputHTMLAttributes<HTMLInputElement> {
  icon?: LucideIcon;
  invalid?: boolean;
  trailing?: ReactNode;
}

export const Input = forwardRef<HTMLInputElement, InputProps>(function Input(
  { icon: Icon, invalid, trailing, className, ...rest },
  ref,
) {
  return (
    <div className="relative">
      {Icon && (
        <Icon className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-zinc-400" aria-hidden />
      )}
      <input
        ref={ref}
        className={cn(fieldBase, fieldBorder(invalid), "h-9 px-3", Icon && "pl-9", trailing ? "pr-10" : "", className)}
        aria-invalid={invalid || undefined}
        {...rest}
      />
      {trailing && <div className="absolute inset-y-0 right-1 flex items-center">{trailing}</div>}
    </div>
  );
});

export const PasswordInput = forwardRef<HTMLInputElement, Omit<InputProps, "type" | "trailing">>(function PasswordInput(
  props,
  ref,
) {
  const [visible, setVisible] = useState(false);
  return (
    <Input
      ref={ref}
      type={visible ? "text" : "password"}
      trailing={
        <button
          type="button"
          tabIndex={-1}
          onClick={() => setVisible((v) => !v)}
          className="rounded-md p-1.5 text-zinc-400 hover:bg-zinc-100 hover:text-zinc-700 dark:hover:bg-zinc-800 dark:hover:text-zinc-200"
          aria-label={visible ? "Passwort verbergen" : "Passwort anzeigen"}
        >
          {visible ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
        </button>
      }
      {...props}
    />
  );
});

export interface TextareaProps extends TextareaHTMLAttributes<HTMLTextAreaElement> {
  invalid?: boolean;
}

export const Textarea = forwardRef<HTMLTextAreaElement, TextareaProps>(function Textarea(
  { invalid, className, ...rest },
  ref,
) {
  return <textarea ref={ref} className={cn(fieldBase, fieldBorder(invalid), "px-3 py-2", className)} {...rest} />;
});

export interface FieldProps {
  label?: ReactNode;
  help?: ReactNode;
  error?: ReactNode;
  required?: boolean;
  className?: string;
  /** Render label and control side by side on wide screens. */
  inline?: boolean;
  children: (id: string) => ReactNode;
}

/** Labelled form row with help text and error message. */
export function Field({ label, help, error, required, className, inline, children }: FieldProps) {
  const id = useId();
  return (
    <div className={cn(inline ? "grid gap-2 sm:grid-cols-[minmax(0,14rem)_1fr] sm:gap-6" : "space-y-1.5", className)}>
      {label && (
        <label htmlFor={id} className={cn("block text-sm font-medium text-zinc-800 dark:text-zinc-200", inline && "sm:pt-2")}>
          {label}
          {required && <span className="ml-0.5 text-red-500">*</span>}
        </label>
      )}
      <div className="space-y-1.5">
        {children(id)}
        {error ? (
          <p className="text-xs text-red-600 dark:text-red-400">{error}</p>
        ) : help ? (
          <p className="text-xs leading-relaxed text-zinc-500 dark:text-zinc-400">{help}</p>
        ) : null}
      </div>
    </div>
  );
}

export function Label({ children, htmlFor, className }: { children: ReactNode; htmlFor?: string; className?: string }) {
  return (
    <label htmlFor={htmlFor} className={cn("block text-sm font-medium text-zinc-800 dark:text-zinc-200", className)}>
      {children}
    </label>
  );
}
