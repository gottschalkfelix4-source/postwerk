import { useCallback, useMemo, useSyncExternalStore } from "react";
import { useAiStatus } from "@/lib/queries";
import { useSearchParams } from "react-router-dom";
import { Archive, FileText, Folder, Inbox, Send, ShieldX, Trash2, type LucideIcon } from "lucide-react";
import type { Address, Mailbox, MessageDetail } from "@/api/types";
import { formatDateTime } from "@/lib/format";
import { storageGet, storageSet } from "@/lib/utils";

export const mailboxIcons: Record<NonNullable<Mailbox["role"]> | "none", LucideIcon> = {
  inbox: Inbox,
  drafts: FileText,
  sent: Send,
  archive: Archive,
  junk: ShieldX,
  trash: Trash2,
  none: Folder,
};

export const mailboxRoleLabels: Record<NonNullable<Mailbox["role"]>, string> = {
  inbox: "Posteingang",
  drafts: "Entwürfe",
  sent: "Gesendet",
  archive: "Archiv",
  junk: "Spam",
  trash: "Papierkorb",
};

const roleOrder: Record<string, number> = { inbox: 0, drafts: 1, sent: 2, archive: 3, junk: 4, trash: 5 };

export function sortMailboxes(list: Mailbox[]): Mailbox[] {
  return [...list].sort((a, b) => {
    const ra = a.role ? roleOrder[a.role] : 99;
    const rb = b.role ? roleOrder[b.role] : 99;
    if (ra !== rb) return ra - rb;
    return a.name.localeCompare(b.name, "de");
  });
}

export function mailboxLabel(m: Mailbox): string {
  return m.role ? mailboxRoleLabels[m.role] : m.name;
}

export function iconFor(m: Mailbox): LucideIcon {
  return mailboxIcons[m.role ?? "none"];
}

// ---------------------------------------------------------------------------
// URL state of the mail view

export interface MailParams {
  box: number | null;
  category: string | null;
  tag: string | null;
  q: string;
  mode: "classic" | "semantic";
  msg: number | null;
  unread: boolean;
  important: boolean;
}

export function useMailParams(): [MailParams, (patch: Partial<MailParams>, replace?: boolean) => void] {
  const [params, setParams] = useSearchParams();

  const state = useMemo<MailParams>(() => {
    const num = (k: string) => {
      const v = params.get(k);
      const n = v ? Number(v) : NaN;
      return Number.isFinite(n) ? n : null;
    };
    return {
      box: num("box"),
      category: params.get("category"),
      tag: params.get("tag"),
      q: params.get("q") ?? "",
      mode: params.get("mode") === "semantic" ? "semantic" : "classic",
      msg: num("msg"),
      unread: params.get("unread") === "1",
      important: params.get("important") === "1",
    };
  }, [params]);

  const update = useCallback(
    (patch: Partial<MailParams>, replace = false) => {
      setParams(
        (prev) => {
          const next = new URLSearchParams(prev);
          for (const [k, v] of Object.entries(patch)) {
            if (v === null || v === undefined || v === "" || v === false || (k === "mode" && v === "classic")) next.delete(k);
            else next.set(k, v === true ? "1" : String(v));
          }
          return next;
        },
        { replace },
      );
    },
    [setParams],
  );

  return [state, update];
}

export function mailLink(p: Partial<MailParams>): string {
  const s = new URLSearchParams();
  for (const [k, v] of Object.entries(p)) {
    if (v === null || v === undefined || v === "" || v === false) continue;
    s.set(k, v === true ? "1" : String(v));
  }
  const qs = s.toString();
  return qs ? `/mail?${qs}` : "/mail";
}

// ---------------------------------------------------------------------------
// Categories seen in messages (users have no endpoint listing AI categories)

const CAT_KEY = "postwerk.mail.categories";
let catCache: string[] = (() => {
  try {
    const raw = storageGet(CAT_KEY);
    const v = raw ? (JSON.parse(raw) as unknown) : [];
    return Array.isArray(v) ? [...new Set(v.filter((x): x is string => typeof x === "string"))] : [];
  } catch {
    return [];
  }
})();
const catListeners = new Set<() => void>();

export function rememberCategories(names: (string | null)[]): void {
  const add = [...new Set(names)].filter((n): n is string => !!n && !catCache.includes(n));
  if (add.length === 0) return;
  catCache = [...catCache, ...add].sort((a, b) => a.localeCompare(b, "de")).slice(0, 30);
  storageSet(CAT_KEY, JSON.stringify(catCache));
  catListeners.forEach((l) => l());
}

/** Configured AI categories merged with categories seen in messages. */
export function useKnownCategories(): string[] {
  const seen = useSyncExternalStore(
    (cb) => {
      catListeners.add(cb);
      return () => catListeners.delete(cb);
    },
    () => catCache,
  );
  const configured = useAiStatus().categories;
  return useMemo(() => [...new Set([...configured, ...seen])], [configured, seen]);
}

// ---------------------------------------------------------------------------
// Reply / forward helpers

export function addressLabel(a: Address): string {
  return a.name ? `${a.name} <${a.email}>` : a.email;
}

export function htmlToText(html: string): string {
  try {
    const doc = new DOMParser().parseFromString(html, "text/html");
    doc.querySelectorAll("style,script,head").forEach((n) => n.remove());
    doc.querySelectorAll("br").forEach((n) => n.replaceWith("\n"));
    doc.querySelectorAll("p,div,tr,li,h1,h2,h3,h4,blockquote").forEach((n) => n.append("\n"));
    return (doc.body.textContent ?? "").replace(/\n{3,}/g, "\n\n").trim();
  } catch {
    return "";
  }
}

export function plainBody(m: MessageDetail): string {
  if (m.textBody && m.textBody.trim()) return m.textBody;
  return m.htmlBody ? htmlToText(m.htmlBody) : "";
}

function prefixed(subject: string, prefix: "Re" | "Fwd"): string {
  const re = prefix === "Re" ? /^\s*(re|aw|antw)\s*:/i : /^\s*(fwd?|wg)\s*:/i;
  return re.test(subject) ? subject : `${prefix}: ${subject}`;
}

export interface ComposeInit {
  to?: string[];
  cc?: string[];
  bcc?: string[];
  subject?: string;
  text?: string;
  inReplyTo?: number;
  draftId?: number;
  mode?: "new" | "reply" | "forward";
}

function quoteOf(m: MessageDetail): string {
  const body = plainBody(m)
    .split("\n")
    .map((l) => `> ${l}`)
    .join("\n");
  return `\n\nAm ${formatDateTime(m.receivedAt)} schrieb ${m.from.name || m.from.email}:\n${body}`;
}

export function replyInit(m: MessageDetail, all: boolean, myEmail: string): ComposeInit {
  const me = myEmail.toLowerCase();
  const primary = (m.replyTo.length ? m.replyTo : [m.from]).map((a) => a.email);
  let cc: string[] = [];
  if (all) {
    const seen = new Set(primary.map((e) => e.toLowerCase()));
    seen.add(me);
    cc = [...m.to, ...m.cc]
      .map((a) => a.email)
      .filter((e) => {
        const k = e.toLowerCase();
        if (seen.has(k)) return false;
        seen.add(k);
        return true;
      });
  }
  const to = primary.filter((e) => e.toLowerCase() !== me);
  return {
    to: to.length ? to : primary,
    cc,
    subject: prefixed(m.subject, "Re"),
    text: quoteOf(m),
    inReplyTo: m.id,
    mode: "reply",
  };
}

export function forwardInit(m: MessageDetail): ComposeInit {
  const header = [
    "---------- Weitergeleitete Nachricht ----------",
    `Von: ${addressLabel(m.from)}`,
    `Datum: ${formatDateTime(m.receivedAt)}`,
    `Betreff: ${m.subject}`,
    `An: ${m.to.map(addressLabel).join(", ")}`,
    ...(m.cc.length ? [`Cc: ${m.cc.map(addressLabel).join(", ")}`] : []),
  ].join("\n");
  return {
    subject: prefixed(m.subject, "Fwd"),
    text: `\n\n${header}\n\n${plainBody(m)}`,
    mode: "forward",
  };
}
