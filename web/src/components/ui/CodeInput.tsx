import { useEffect, useRef } from "react";
import { cn } from "@/lib/utils";

/** Six separate digit boxes for TOTP codes, with paste support. */
export function CodeInput({
  value,
  onChange,
  onComplete,
  length = 6,
  disabled,
  invalid,
  autoFocus,
}: {
  value: string;
  onChange: (v: string) => void;
  onComplete?: (v: string) => void;
  length?: number;
  disabled?: boolean;
  invalid?: boolean;
  autoFocus?: boolean;
}) {
  const refs = useRef<(HTMLInputElement | null)[]>([]);

  useEffect(() => {
    if (autoFocus) refs.current[0]?.focus();
  }, [autoFocus]);

  const setDigits = (digits: string, focusIndex: number) => {
    const next = digits.replace(/\D/g, "").slice(0, length);
    onChange(next);
    refs.current[Math.min(focusIndex, length - 1)]?.focus();
    if (next.length === length) onComplete?.(next);
  };

  return (
    <div className="flex justify-center gap-2" role="group" aria-label="Bestätigungscode">
      {Array.from({ length }, (_, i) => (
        <input
          key={i}
          ref={(el) => {
            refs.current[i] = el;
          }}
          inputMode="numeric"
          autoComplete={i === 0 ? "one-time-code" : "off"}
          maxLength={1}
          disabled={disabled}
          value={value[i] ?? ""}
          aria-label={`Ziffer ${i + 1}`}
          onChange={(e) => {
            const d = e.target.value.replace(/\D/g, "");
            if (!d) return;
            if (d.length > 1) {
              setDigits(value.slice(0, i) + d, i + d.length);
              return;
            }
            const chars = value.padEnd(length, " ").split("");
            chars[i] = d;
            setDigits(chars.join("").replace(/\s+$/, "").replace(/\s/g, ""), i + 1);
          }}
          onKeyDown={(e) => {
            if (e.key === "Backspace") {
              e.preventDefault();
              if (value[i]) {
                onChange(value.slice(0, i) + value.slice(i + 1));
              } else if (i > 0) {
                onChange(value.slice(0, i - 1) + value.slice(i));
                refs.current[i - 1]?.focus();
              }
            } else if (e.key === "ArrowLeft" && i > 0) {
              refs.current[i - 1]?.focus();
            } else if (e.key === "ArrowRight" && i < length - 1) {
              refs.current[i + 1]?.focus();
            }
          }}
          onPaste={(e) => {
            e.preventDefault();
            setDigits(e.clipboardData.getData("text"), length - 1);
          }}
          onFocus={(e) => e.target.select()}
          className={cn(
            "h-12 w-11 rounded-lg border bg-white text-center font-mono text-xl font-semibold text-zinc-900 shadow-sm transition",
            "focus:border-indigo-500 focus:outline-none focus:ring-2 focus:ring-indigo-500/30 dark:bg-zinc-900 dark:text-zinc-50",
            invalid ? "border-red-400 dark:border-red-500/70" : "border-zinc-300 dark:border-zinc-700",
          )}
        />
      ))}
    </div>
  );
}
