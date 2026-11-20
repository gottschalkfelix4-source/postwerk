import type { ImapSecurity, MigrationAccountProgress, MigrationJob, MigrationStatus, SourceFolder } from "@/api/types";
import { Badge, type BadgeTone } from "@/components/ui";
import { cn } from "@/lib/utils";

// ---------------------------------------------------------------------------
// Status
// ---------------------------------------------------------------------------

const statusLabels: Record<MigrationStatus, string> = {
  pending: "Wartend",
  running: "Läuft",
  done: "Abgeschlossen",
  failed: "Fehlgeschlagen",
  cancelled: "Abgebrochen",
  interrupted: "Unterbrochen",
};

const statusTones: Record<MigrationStatus, BadgeTone> = {
  pending: "blue",
  running: "indigo",
  done: "green",
  failed: "red",
  cancelled: "neutral",
  interrupted: "amber",
};

export function MigrationStatusBadge({ status, className }: { status: MigrationStatus; className?: string }) {
  return (
    <Badge tone={statusTones[status] ?? "neutral"} dot className={cn(status === "running" && "[&>span:first-child]:animate-pulse", className)}>
      {statusLabels[status] ?? status}
    </Badge>
  );
}

export const isActive = (status: MigrationStatus) => status === "running" || status === "pending";
export const isFinished = (status: MigrationStatus) => !isActive(status);
export const canRestart = (status: MigrationStatus) => status === "interrupted" || status === "failed" || status === "cancelled";

/** Overall progress of a job as fraction 0..1 (messages if known, else folders). */
export function jobProgress(job: Pick<MigrationJob, "accounts" | "status">): number {
  if (job.status === "done") return 1;
  return accountsProgress(job.accounts);
}

export function accountsProgress(accounts: MigrationAccountProgress[]): number {
  const total = accounts.reduce((n, a) => n + a.messagesTotal, 0);
  if (total > 0) return accounts.reduce((n, a) => n + a.messagesDone, 0) / total;
  const folders = accounts.reduce((n, a) => n + a.foldersTotal, 0);
  if (folders > 0) return accounts.reduce((n, a) => n + a.foldersDone, 0) / folders;
  return 0;
}

export function accountProgress(a: MigrationAccountProgress): number {
  if (a.status === "done") return 1;
  if (a.messagesTotal > 0) return a.messagesDone / a.messagesTotal;
  if (a.foldersTotal > 0) return a.foldersDone / a.foldersTotal;
  return 0;
}

export function percent(fraction: number): string {
  return `${Math.round(Math.max(0, Math.min(1, fraction)) * 100)} %`;
}

export const securityLabels: Record<ImapSecurity, string> = {
  tls: "SSL/TLS",
  starttls: "STARTTLS",
  none: "Unverschlüsselt",
};

// ---------------------------------------------------------------------------
// Source presets
// ---------------------------------------------------------------------------

export interface SourcePreset {
  id: string;
  name: string;
  host: string;
  port: number;
  security: ImapSecurity;
  /** Brand-neutral accent for the tile initial. */
  tone: string;
  hint?: string;
}

export const sourcePresets: SourcePreset[] = [
  {
    id: "gmail",
    name: "Gmail",
    host: "imap.gmail.com",
    port: 993,
    security: "tls",
    tone: "bg-red-50 text-red-600 dark:bg-red-500/15 dark:text-red-300",
    hint: "Gmail verlangt ein App-Passwort (Google-Konto → Sicherheit → Bestätigung in zwei Schritten → App-Passwörter). IMAP muss in den Gmail-Einstellungen aktiviert sein.",
  },
  {
    id: "outlook",
    name: "Outlook / Microsoft 365",
    host: "outlook.office365.com",
    port: 993,
    security: "tls",
    tone: "bg-sky-50 text-sky-600 dark:bg-sky-500/15 dark:text-sky-300",
    hint: "Bei Microsoft-Konten mit Zwei-Faktor-Anmeldung ein App-Passwort verwenden. In Microsoft 365 muss IMAP für das Postfach freigeschaltet sein.",
  },
  {
    id: "icloud",
    name: "iCloud",
    host: "imap.mail.me.com",
    port: 993,
    security: "tls",
    tone: "bg-zinc-100 text-zinc-600 dark:bg-zinc-800 dark:text-zinc-300",
    hint: "iCloud verlangt ein app-spezifisches Passwort (account.apple.com → Anmeldung und Sicherheit). Benutzername ist die iCloud-Adresse.",
  },
  {
    id: "gmx",
    name: "GMX",
    host: "imap.gmx.net",
    port: 993,
    security: "tls",
    tone: "bg-blue-50 text-blue-700 dark:bg-blue-500/15 dark:text-blue-300",
    hint: "IMAP-Zugriff muss im GMX-Postfach unter Einstellungen → POP3/IMAP aktiviert sein.",
  },
  {
    id: "webde",
    name: "WEB.DE",
    host: "imap.web.de",
    port: 993,
    security: "tls",
    tone: "bg-amber-50 text-amber-700 dark:bg-amber-500/15 dark:text-amber-300",
    hint: "IMAP-Zugriff muss im WEB.DE-Postfach unter Einstellungen → POP3/IMAP aktiviert sein.",
  },
  {
    id: "ionos",
    name: "IONOS",
    host: "imap.ionos.de",
    port: 993,
    security: "tls",
    tone: "bg-indigo-50 text-indigo-700 dark:bg-indigo-500/15 dark:text-indigo-300",
  },
  {
    id: "strato",
    name: "Strato",
    host: "imap.strato.de",
    port: 993,
    security: "tls",
    tone: "bg-orange-50 text-orange-700 dark:bg-orange-500/15 dark:text-orange-300",
  },
  {
    id: "tonline",
    name: "T-Online",
    host: "secureimap.t-online.de",
    port: 993,
    security: "tls",
    tone: "bg-pink-50 text-pink-700 dark:bg-pink-500/15 dark:text-pink-300",
    hint: "T-Online verwendet ein separates E-Mail-Passwort, nicht das Passwort des Kundencenters.",
  },
];

// ---------------------------------------------------------------------------
// Folders
// ---------------------------------------------------------------------------

const roleLabels: Record<string, string> = {
  inbox: "Posteingang",
  sent: "Gesendet",
  drafts: "Entwürfe",
  trash: "Papierkorb",
  junk: "Spam",
  archive: "Archiv",
};

export function targetLabel(target: string): string {
  return roleLabels[target.toLowerCase()] ?? target;
}

export function folderKind(f: SourceFolder): "trash" | "junk" | null {
  const use = (f.specialUse ?? "").toLowerCase();
  const target = f.target.toLowerCase();
  if (use === "\\trash" || target === "trash") return "trash";
  if (use === "\\junk" || target === "junk") return "junk";
  return null;
}

// ---------------------------------------------------------------------------
// CSV import of mailboxes
// ---------------------------------------------------------------------------

export interface CsvRow {
  targetEmail: string;
  username: string;
  password: string;
  name: string;
  newPassword: string;
}

export interface CsvResult {
  rows: CsvRow[];
  errors: string[];
}

/** Split one CSV line, honoring double quotes. */
function splitCsvLine(line: string, delimiter: string): string[] {
  const out: string[] = [];
  let cur = "";
  let quoted = false;
  for (let i = 0; i < line.length; i++) {
    const ch = line[i];
    if (quoted) {
      if (ch === '"' && line[i + 1] === '"') {
        cur += '"';
        i++;
      } else if (ch === '"') {
        quoted = false;
      } else {
        cur += ch;
      }
    } else if (ch === '"' && cur.trim() === "") {
      quoted = true;
      cur = "";
    } else if (ch === delimiter) {
      out.push(cur.trim());
      cur = "";
    } else {
      cur += ch;
    }
  }
  out.push(cur.trim());
  return out;
}

const EMAIL_RE = /^[^\s@<>()]+@[^\s@<>()]+\.[^\s@<>()]+$/;

/**
 * Parse "zieladresse;quellbenutzer;quellpasswort;name;neues_passwort".
 * Comma or semicolon, header row optional, empty lines and "#" comments ignored.
 */
export function parseMailboxCsv(text: string): CsvResult {
  const rows: CsvRow[] = [];
  const errors: string[] = [];
  const lines = text.replace(/^﻿/, "").split(/\r?\n/);
  lines.forEach((raw, idx) => {
    const line = raw.trim();
    if (!line || line.startsWith("#")) return;
    const unquoted = line.replace(/"(?:[^"]|"")*"/g, "");
    const delimiter = unquoted.includes(";") ? ";" : ",";
    const cells = splitCsvLine(line, delimiter);
    const [target = "", user = "", pass = "", name = "", newPass = ""] = cells;
    if (rows.length === 0 && errors.length === 0 && !EMAIL_RE.test(target) && /ziel|target|mail|adresse/i.test(target)) {
      return; // header row
    }
    if (!EMAIL_RE.test(target)) {
      errors.push(`Zeile ${idx + 1}: „${target || "(leer)"}“ ist keine gültige Zieladresse.`);
      return;
    }
    if (!pass) {
      errors.push(`Zeile ${idx + 1}: Quell-Passwort fehlt.`);
      return;
    }
    rows.push({ targetEmail: target.toLowerCase(), username: user || target, password: pass, name, newPassword: newPass });
  });
  return { rows, errors };
}
