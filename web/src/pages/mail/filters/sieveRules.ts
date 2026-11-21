// Rule model of the filter editor and its translation to Sieve (RFC 5228).
// The generated script embeds the rules as JSON in a comment, so the editor
// can load them again and detect manual changes by regenerating the script.

export const RULES_SCRIPT = "postwerk-regeln";

export type CondField = "from" | "to" | "subject" | "header" | "size";
export type CondOp = "contains" | "notContains" | "is" | "startsWith" | "over" | "under";
export type ActionType = "fileinto" | "seen" | "flag" | "redirect" | "discard" | "stop";

export interface FilterCondition {
  field: CondField;
  header?: string;
  op: CondOp;
  value: string;
}

export interface FilterAction {
  type: ActionType;
  mailbox?: string;
  address?: string;
  keepCopy?: boolean;
}

export interface FilterRule {
  id: string;
  name: string;
  enabled: boolean;
  match: "all" | "any";
  conditions: FilterCondition[];
  actions: FilterAction[];
}

export const fieldLabels: Record<CondField, string> = {
  from: "Absender",
  to: "Empfänger",
  subject: "Betreff",
  header: "Header",
  size: "Größe",
};

export const textOps: { value: CondOp; label: string }[] = [
  { value: "contains", label: "enthält" },
  { value: "notContains", label: "enthält nicht" },
  { value: "is", label: "ist" },
  { value: "startsWith", label: "beginnt mit" },
];

export const sizeOps: { value: CondOp; label: string }[] = [
  { value: "over", label: "größer als" },
  { value: "under", label: "kleiner als" },
];

export const actionLabels: Record<ActionType, string> = {
  fileinto: "In Ordner verschieben",
  seen: "Als gelesen markieren",
  flag: "Markieren (Stern)",
  redirect: "Weiterleiten an",
  discard: "Löschen",
  stop: "Verarbeitung stoppen",
};

let counter = 0;
export function newId(): string {
  counter += 1;
  return `r${Date.now().toString(36)}${counter}`;
}

export function newRule(): FilterRule {
  return {
    id: newId(),
    name: "",
    enabled: true,
    match: "all",
    conditions: [{ field: "subject", op: "contains", value: "" }],
    actions: [{ type: "fileinto", mailbox: "" }],
  };
}

const headerNameRe = /^[!-9;-~]+$/;
const emailRe = /^[^\s@<>"]+@[^\s@<>"]+\.[^\s@<>"]+$/;

/** German problem description for a rule, or null when it is complete. */
export function ruleProblem(rule: FilterRule): string | null {
  for (const c of rule.conditions) {
    if (c.field === "size") {
      if (!/^\d+$/.test(c.value.trim())) return "Die Größe muss eine ganze Zahl (KB) sein.";
      continue;
    }
    if (c.field === "header" && !headerNameRe.test((c.header ?? "").trim())) return "Bitte einen gültigen Header-Namen angeben.";
    if (c.value === "") return "Bitte für jede Bedingung einen Wert angeben.";
  }
  if (rule.actions.length === 0) return "Jede Regel braucht mindestens eine Aktion.";
  for (const a of rule.actions) {
    if (a.type === "fileinto" && !a.mailbox) return "Bitte einen Zielordner wählen.";
    if (a.type === "redirect" && !emailRe.test((a.address ?? "").trim())) return "Bitte eine gültige Weiterleitungsadresse angeben.";
  }
  return null;
}

/** Sieve quoted string. */
export function quote(s: string): string {
  return `"${s.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"`;
}

function wildcardPrefix(s: string): string {
  return s.replace(/[\\*?]/g, (m) => `\\${m}`) + "*";
}

function conditionTest(c: FilterCondition): string {
  if (c.field === "size") {
    return `size :${c.op === "under" ? "under" : "over"} ${parseInt(c.value, 10) || 0}K`;
  }
  const headers =
    c.field === "from" ? '"from"' : c.field === "to" ? '["to", "cc"]' : c.field === "subject" ? '"subject"' : quote((c.header ?? "").trim().toLowerCase());
  const addressField = c.field === "from" || c.field === "to";
  switch (c.op) {
    case "is":
      return addressField ? `address :is ${headers} ${quote(c.value)}` : `header :is ${headers} ${quote(c.value)}`;
    case "startsWith":
      return `${addressField ? "address" : "header"} :matches ${headers} ${quote(wildcardPrefix(c.value))}`;
    case "notContains":
      return `not header :contains ${headers} ${quote(c.value)}`;
    default:
      return `header :contains ${headers} ${quote(c.value)}`;
  }
}

function actionOrder(t: ActionType): number {
  return t === "seen" || t === "flag" ? 0 : t === "stop" ? 2 : 1;
}

/** Generates the Sieve script for the rules. */
export function generateScript(rules: FilterRule[]): string {
  const requires = new Set<string>();
  const blocks: string[] = [];
  rules.forEach((rule, i) => {
    if (!rule.enabled || rule.actions.length === 0) return;
    const tests = rule.conditions.map(conditionTest);
    let test = "true";
    if (tests.length === 1) test = tests[0];
    else if (tests.length > 1) test = `${rule.match === "any" ? "anyof" : "allof"} (${tests.join(", ")})`;
    const lines: string[] = [];
    const actions = [...rule.actions].sort((a, b) => actionOrder(a.type) - actionOrder(b.type));
    for (const a of actions) {
      switch (a.type) {
        case "fileinto":
          requires.add("fileinto");
          lines.push(`fileinto ${quote(a.mailbox ?? "")};`);
          break;
        case "seen":
          requires.add("imap4flags");
          lines.push('addflag "\\\\Seen";');
          break;
        case "flag":
          requires.add("imap4flags");
          lines.push('addflag "\\\\Flagged";');
          break;
        case "redirect":
          if (a.keepCopy) {
            requires.add("copy");
            lines.push(`redirect :copy ${quote((a.address ?? "").trim())};`);
          } else {
            lines.push(`redirect ${quote((a.address ?? "").trim())};`);
          }
          break;
        case "discard":
          lines.push("discard;");
          break;
        case "stop":
          lines.push("stop;");
          break;
      }
    }
    const title = rule.name.replace(/[\r\n]+/g, " ").trim();
    blocks.push(`# Regel ${i + 1}${title ? `: ${title}` : ""}\nif ${test} {\n${lines.map((l) => `  ${l}`).join("\n")}\n}\n`);
  });
  const order = ["fileinto", "imap4flags", "copy"].filter((r) => requires.has(r));
  const head = [
    "# Postwerk-Filterregeln – erzeugt vom Regel-Editor der Weboberfläche.",
    "# Manuelle Änderungen kann der Regel-Editor nicht darstellen.",
    `# rules: ${JSON.stringify({ v: 1, rules: rules.map(({ id: _id, ...r }) => r) })}`,
  ];
  if (order.length > 0) head.push(`require [${order.map(quote).join(", ")}];`);
  return `${head.join("\n")}\n${blocks.length ? "\n" + blocks.join("\n") : ""}`;
}

export interface ParsedRules {
  rules: FilterRule[];
  /** The script was changed outside the rule editor. */
  manual: boolean;
}

/** Reads the rules embedded in a generated script. */
export function parseScript(script: string): ParsedRules {
  const normalized = script.replace(/\r\n/g, "\n");
  const m = normalized.match(/^# rules: (.*)$/m);
  if (!m) return { rules: [], manual: normalized.trim() !== "" };
  try {
    const data = JSON.parse(m[1]) as { v?: number; rules?: Omit<FilterRule, "id">[] };
    if (data.v !== 1 || !Array.isArray(data.rules)) return { rules: [], manual: true };
    const rules: FilterRule[] = data.rules.map((r) => ({
      id: newId(),
      name: String(r.name ?? ""),
      enabled: r.enabled !== false,
      match: r.match === "any" ? "any" : "all",
      conditions: Array.isArray(r.conditions) ? r.conditions : [],
      actions: Array.isArray(r.actions) ? r.actions : [],
    }));
    return { rules, manual: generateScript(rules).trimEnd() !== normalized.trimEnd() };
  } catch {
    return { rules: [], manual: true };
  }
}

/** Line and column of a server-side script error ("Zeile 3, Spalte 5: …"). */
export function errorPosition(message: string): { line: number; column: number } | null {
  const m = message.match(/Zeile (\d+), Spalte (\d+)/);
  return m ? { line: Number(m[1]), column: Number(m[2]) } : null;
}
