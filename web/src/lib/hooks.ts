import { useEffect, useRef, useState } from "react";
import { deepEqual, storageGet, storageSet } from "./utils";

export function useDebounced<T>(value: T, delay = 300): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const t = window.setTimeout(() => setDebounced(value), delay);
    return () => window.clearTimeout(t);
  }, [value, delay]);
  return debounced;
}

/** Boolean preference persisted in localStorage. */
export function useStoredFlag(key: string, initial: boolean): [boolean, (v: boolean) => void] {
  const [value, setValue] = useState<boolean>(() => {
    const raw = storageGet(key);
    return raw === null ? initial : raw === "1";
  });
  const set = (v: boolean) => {
    setValue(v);
    storageSet(key, v ? "1" : "0");
  };
  return [value, set];
}

/**
 * Local editable copy of server data. Re-syncs whenever the source changes
 * and reports whether the draft differs from it.
 */
export function useDraft<T>(source: T | undefined): {
  draft: T | undefined;
  setDraft: (updater: (prev: T) => T) => void;
  dirty: boolean;
  reset: () => void;
} {
  const [draft, setDraftState] = useState<T | undefined>(source);
  const lastSource = useRef(source);

  useEffect(() => {
    if (!deepEqual(lastSource.current, source) || draft === undefined) {
      lastSource.current = source;
      setDraftState(source);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [source]);

  const setDraft = (updater: (prev: T) => T) =>
    setDraftState((prev) => (prev === undefined ? prev : updater(prev)));

  return {
    draft,
    setDraft,
    dirty: draft !== undefined && source !== undefined && !deepEqual(draft, source),
    reset: () => setDraftState(source),
  };
}

export function useMediaQuery(query: string): boolean {
  const [matches, setMatches] = useState(() => window.matchMedia(query).matches);
  useEffect(() => {
    const mq = window.matchMedia(query);
    const onChange = () => setMatches(mq.matches);
    onChange();
    mq.addEventListener("change", onChange);
    return () => mq.removeEventListener("change", onChange);
  }, [query]);
  return matches;
}

export function useClickOutside<T extends HTMLElement>(onOutside: () => void, active = true) {
  const ref = useRef<T>(null);
  useEffect(() => {
    if (!active) return;
    const handler = (e: MouseEvent | TouchEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) onOutside();
    };
    document.addEventListener("mousedown", handler);
    document.addEventListener("touchstart", handler);
    return () => {
      document.removeEventListener("mousedown", handler);
      document.removeEventListener("touchstart", handler);
    };
  }, [onOutside, active]);
  return ref;
}
