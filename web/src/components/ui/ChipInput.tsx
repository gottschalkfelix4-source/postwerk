import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import { X } from "lucide-react";
import { cn } from "@/lib/utils";

export interface ChipInputProps {
  value: string[];
  onChange: (value: string[]) => void;
  placeholder?: string;
  /** Return an error message for invalid entries, or null when valid. */
  validate?: (entry: string) => string | null;
  /** Normalize an entry before it is added. */
  transform?: (entry: string) => string;
  id?: string;
  disabled?: boolean;
  className?: string;
  /** Optional suggestions offered below the field. */
  suggestions?: string[];
  autoFocus?: boolean;
  /** Optional autocomplete source queried while typing (debounced). */
  suggest?: (query: string, signal: AbortSignal) => Promise<ChipSuggestion[]>;
}

export interface ChipSuggestion {
  /** Value added as chip when picked. */
  value: string;
  label: string;
  hint?: string;
}

export function ChipInput({
  value,
  onChange,
  placeholder,
  validate,
  transform,
  id,
  disabled,
  className,
  suggestions,
  autoFocus,
  suggest,
}: ChipInputProps) {
  const [text, setText] = useState("");
  const [error, setError] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const [options, setOptions] = useState<ChipSuggestion[]>([]);
  const [active, setActive] = useState(-1);
  const [focused, setFocused] = useState(false);

  useEffect(() => {
    const q = text.trim();
    if (!suggest || !focused || q.length < 1) {
      setOptions([]);
      return;
    }
    const ctrl = new AbortController();
    const t = window.setTimeout(() => {
      suggest(q, ctrl.signal)
        .then((list) => {
          const taken = new Set(value.map((v) => v.toLowerCase()));
          setOptions(list.filter((o) => !taken.has(o.value.toLowerCase())));
          setActive(-1);
        })
        .catch(() => {
          // Suggestions are optional; errors (including aborts) are ignored.
        });
    }, 150);
    return () => {
      window.clearTimeout(t);
      ctrl.abort();
    };
  }, [text, suggest, focused, value]);

  const showOptions = options.length > 0 && focused;

  const commit = (raw: string): boolean => {
    const parts = raw
      .split(/[,;\n]/)
      .map((p) => (transform ? transform(p.trim()) : p.trim()))
      .filter(Boolean);
    if (parts.length === 0) return true;
    const next = [...value];
    for (const p of parts) {
      const err = validate?.(p) ?? null;
      if (err) {
        setError(err);
        setText(p);
        if (next.length !== value.length) onChange(next);
        return false;
      }
      if (!next.some((v) => v.toLowerCase() === p.toLowerCase())) next.push(p);
    }
    setError(null);
    setText("");
    onChange(next);
    return true;
  };

  const pick = (o: ChipSuggestion) => {
    setOptions([]);
    setActive(-1);
    commit(o.value);
  };

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (showOptions) {
      if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        e.preventDefault();
        const n = options.length;
        setActive((i) => (e.key === "ArrowDown" ? (i + 1) % n : (i - 1 + n) % n));
        return;
      }
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        setOptions([]);
        return;
      }
      if ((e.key === "Enter" || e.key === "Tab") && active >= 0) {
        e.preventDefault();
        pick(options[active]);
        return;
      }
    }
    if (e.key === "Enter" || e.key === "," || e.key === ";" || (e.key === "Tab" && text.trim())) {
      if (text.trim()) {
        e.preventDefault();
        commit(text);
      } else if (e.key !== "Tab" && e.key !== "Enter") {
        e.preventDefault();
      }
    } else if (e.key === "Backspace" && !text && value.length > 0) {
      onChange(value.slice(0, -1));
    }
  };

  const remaining = suggestions?.filter((s) => !value.some((v) => v.toLowerCase() === s.toLowerCase())) ?? [];

  return (
    <div className={cn(suggest && "relative", className)}>
      <div
        onClick={() => inputRef.current?.focus()}
        className={cn(
          "flex min-h-9 w-full cursor-text flex-wrap items-center gap-1.5 rounded-lg border bg-white px-2 py-1.5 shadow-sm transition",
          "focus-within:border-indigo-500 focus-within:ring-2 focus-within:ring-indigo-500/30 dark:bg-zinc-900 dark:focus-within:border-indigo-400",
          error ? "border-red-400 dark:border-red-500/70" : "border-zinc-300 dark:border-zinc-700",
          disabled && "pointer-events-none opacity-60",
        )}
      >
        {value.map((v) => (
          <span
            key={v}
            className="inline-flex max-w-full items-center gap-1 rounded-md bg-zinc-100 py-0.5 pl-2 pr-1 text-[13px] text-zinc-800 ring-1 ring-inset ring-zinc-200 dark:bg-zinc-800 dark:text-zinc-200 dark:ring-zinc-700"
          >
            <span className="truncate">{v}</span>
            <button
              type="button"
              onClick={(e) => {
                e.stopPropagation();
                onChange(value.filter((x) => x !== v));
              }}
              className="rounded p-0.5 text-zinc-400 hover:bg-zinc-200 hover:text-zinc-700 dark:hover:bg-zinc-700 dark:hover:text-zinc-100"
              aria-label={`${v} entfernen`}
            >
              <X className="h-3 w-3" />
            </button>
          </span>
        ))}
        <input
          ref={inputRef}
          id={id}
          value={text}
          autoFocus={autoFocus}
          disabled={disabled}
          onChange={(e) => {
            setText(e.target.value);
            if (error) setError(null);
          }}
          onKeyDown={onKeyDown}
          onFocus={() => setFocused(true)}
          onBlur={() => {
            setFocused(false);
            if (text.trim()) commit(text);
          }}
          role={suggest ? "combobox" : undefined}
          aria-expanded={suggest ? showOptions : undefined}
          aria-autocomplete={suggest ? "list" : undefined}
          onPaste={(e) => {
            const data = e.clipboardData.getData("text");
            if (/[,;\n]/.test(data)) {
              e.preventDefault();
              commit(data);
            }
          }}
          placeholder={value.length === 0 ? placeholder : undefined}
          className="min-w-[8rem] flex-1 border-0 bg-transparent p-0.5 text-sm text-zinc-900 placeholder:text-zinc-400 focus:outline-none focus:ring-0 dark:text-zinc-100 dark:placeholder:text-zinc-500"
        />
      </div>
      {showOptions && (
        <ul
          role="listbox"
          className="absolute left-0 right-0 top-full z-40 mt-1 max-h-64 overflow-auto rounded-xl border border-zinc-200 bg-white p-1 shadow-pop dark:border-zinc-700 dark:bg-zinc-800"
        >
          {options.map((o, i) => (
            <li
              key={o.value}
              role="option"
              aria-selected={i === active}
              // mousedown keeps the focus in the input, so blur does not commit the typed text first
              onMouseDown={(e) => {
                e.preventDefault();
                pick(o);
              }}
              onMouseEnter={() => setActive(i)}
              className={cn(
                "flex cursor-pointer items-center gap-2 rounded-lg px-2.5 py-1.5 text-sm",
                i === active ? "bg-zinc-100 dark:bg-zinc-700/70" : "hover:bg-zinc-50 dark:hover:bg-zinc-700/40",
              )}
            >
              <span className="min-w-0 flex-1 truncate text-zinc-800 dark:text-zinc-100">{o.label}</span>
              {o.hint && <span className="shrink-0 text-xs text-zinc-400">{o.hint}</span>}
            </li>
          ))}
        </ul>
      )}
      {error && <p className="mt-1.5 text-xs text-red-600 dark:text-red-400">{error}</p>}
      {remaining.length > 0 && (
        <div className="mt-2 flex flex-wrap gap-1.5">
          {remaining.map((s) => (
            <button
              key={s}
              type="button"
              onClick={() => onChange([...value, s])}
              className="rounded-md border border-dashed border-zinc-300 px-2 py-0.5 text-xs text-zinc-500 hover:border-indigo-400 hover:text-indigo-600 dark:border-zinc-700 dark:text-zinc-400 dark:hover:border-indigo-400 dark:hover:text-indigo-300"
            >
              + {s}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
