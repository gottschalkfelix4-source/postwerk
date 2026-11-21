import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowDown,
  ArrowUp,
  CircleAlert,
  CircleCheck,
  FlaskConical,
  Info,
  Plus,
  RotateCcw,
  Save,
  Trash2,
  X,
} from "lucide-react";
import { isApiError, errorMessage } from "@/api/client";
import { mailApi, sieveApi } from "@/api/endpoints";
import type { SieveAction, SieveTestResult } from "@/api/types";
import { useMailboxes } from "@/lib/queries";
import { formatRelative } from "@/lib/format";
import {
  Badge,
  Button,
  Card,
  CardBody,
  CardFooter,
  CardHeader,
  Checkbox,
  EmptyState,
  Input,
  Notice,
  PageHeader,
  Segmented,
  Select,
  Skeleton,
  Switch,
  Tabs,
  Textarea,
  useConfirm,
  useToast,
} from "@/components/ui";
import { cn } from "@/lib/utils";
import {
  RULES_SCRIPT,
  actionLabels,
  errorPosition,
  fieldLabels,
  generateScript,
  newRule,
  parseScript,
  ruleProblem,
  sizeOps,
  textOps,
  type ActionType,
  type CondField,
  type CondOp,
  type FilterAction,
  type FilterCondition,
  type FilterRule,
} from "./filters/sieveRules";

type Tab = "rules" | "expert" | "test";

const listKey = ["sieve", "list"] as const;
const scriptKey = (name: string) => ["sieve", "script", name] as const;

/** Loads a script; a missing script yields null. */
async function loadScript(name: string) {
  try {
    return await sieveApi.get(name);
  } catch (err) {
    if (isApiError(err, "not_found")) return null;
    throw err;
  }
}

export default function FiltersPage() {
  const [tab, setTab] = useState<Tab>("rules");
  const [drafts, setDrafts] = useState<{ rules?: string; expert?: string }>({});
  const setRulesDraft = useCallback((s: string) => setDrafts((d) => (d.rules === s ? d : { ...d, rules: s })), []);
  const setExpertDraft = useCallback((s: string) => setDrafts((d) => (d.expert === s ? d : { ...d, expert: s })), []);
  const list = useQuery({ queryKey: listKey, queryFn: sieveApi.list });
  const active = list.data?.active ?? null;

  return (
    <div className="mx-auto max-w-4xl">
      <PageHeader
        title="Filterregeln"
        description="Eingehende Nachrichten automatisch sortieren, markieren oder weiterleiten (Sieve)."
        actions={
          active ? (
            <Badge tone="green" dot>
              Aktiv: {active}
            </Badge>
          ) : list.data ? (
            <Badge tone="neutral">Keine Regeln aktiv</Badge>
          ) : undefined
        }
      />
      <Tabs
        className="mb-6"
        value={tab}
        onChange={setTab}
        items={[
          { value: "rules", label: "Regeln" },
          { value: "expert", label: "Experten-Modus" },
          { value: "test", label: "Testen" },
        ]}
      />
      {tab === "rules" && <RulesEditor active={active} onDraft={setRulesDraft} />}
      {tab === "expert" && <ExpertEditor scripts={list.data?.scripts.map((s) => s.name) ?? []} active={active} onDraft={setExpertDraft} />}
      {tab === "test" && <TestPanel active={active} drafts={drafts} />}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Rule builder
// ---------------------------------------------------------------------------

function RulesEditor({ active, onDraft }: { active: string | null; onDraft: (script: string) => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const confirm = useConfirm();
  const script = useQuery({ queryKey: scriptKey(RULES_SCRIPT), queryFn: () => loadScript(RULES_SCRIPT) });
  const mailboxes = useMailboxes();
  const [rules, setRules] = useState<FilterRule[] | null>(null);
  const [manual, setManual] = useState(false);
  const [dirty, setDirty] = useState(false);

  const reset = () => {
    if (script.data === undefined) return;
    const parsed = script.data ? parseScript(script.data.content) : { rules: [], manual: false };
    setRules(parsed.rules);
    setManual(parsed.manual);
    setDirty(false);
  };
  useEffect(reset, [script.data]); // eslint-disable-line react-hooks/exhaustive-deps

  const generated = useMemo(() => (rules ? generateScript(rules) : ""), [rules]);
  useEffect(() => {
    if (dirty) onDraft(generated);
  }, [dirty, generated, onDraft]);

  const update = (fn: (r: FilterRule[]) => FilterRule[]) => {
    setRules((r) => fn(r ?? []));
    setDirty(true);
  };
  const updateRule = (id: string, patch: Partial<FilterRule>) => update((rs) => rs.map((r) => (r.id === id ? { ...r, ...patch } : r)));
  const move = (index: number, delta: number) =>
    update((rs) => {
      const next = [...rs];
      const [r] = next.splice(index, 1);
      next.splice(index + delta, 0, r);
      return next;
    });

  const save = useMutation({
    mutationFn: () => sieveApi.put(RULES_SCRIPT, generated, true),
    onSuccess: (sc) => {
      qc.setQueryData(scriptKey(RULES_SCRIPT), sc);
      qc.invalidateQueries({ queryKey: listKey });
      toast.success("Filterregeln gespeichert und aktiviert");
    },
    onError: (err) => toast.error(err, "Speichern fehlgeschlagen"),
  });

  const folderOptions = (mailboxes.data ?? [])
    .filter((m) => m.role !== "inbox")
    .map((m) => ({ value: m.name, label: m.name }));
  const problems = (rules ?? []).map(ruleProblem);
  const invalid = problems.some((p, i) => p !== null && rules?.[i].enabled);

  if (script.isLoading || rules === null) {
    return (
      <Card>
        <div className="space-y-3 p-5">
          <Skeleton className="h-6 w-1/3" />
          <Skeleton className="h-28" />
        </div>
      </Card>
    );
  }

  return (
    <div className="space-y-4">
      {manual && (
        <Notice tone="warning" icon={CircleAlert} title="Manuell bearbeitetes Skript">
          Das Skript „{RULES_SCRIPT}“ wurde im Experten-Modus oder per ManageSieve geändert und kann hier nicht vollständig
          dargestellt werden. Speichern im Regel-Editor überschreibt die manuellen Änderungen.
          <div className="mt-2">
            <Button
              size="xs"
              variant="secondary"
              icon={RotateCcw}
              onClick={async () => {
                const ok = await confirm({
                  title: "Regel-Editor neu beginnen?",
                  message: "Die manuellen Änderungen gehen beim nächsten Speichern verloren.",
                  confirmLabel: "Neu beginnen",
                  danger: true,
                });
                if (ok) {
                  setManual(false);
                  setDirty(true);
                }
              }}
            >
              Neu beginnen
            </Button>
          </div>
        </Notice>
      )}
      {active && active !== RULES_SCRIPT && (
        <Notice tone="info" icon={Info}>
          Derzeit ist das Skript „{active}“ aktiv. Beim Speichern wird stattdessen „{RULES_SCRIPT}“ aktiviert.
        </Notice>
      )}

      {!manual && rules.length === 0 && (
        <Card>
          <EmptyState
            icon={FlaskConical}
            title="Noch keine Regeln"
            description="Lege fest, was mit eingehenden Nachrichten passieren soll – etwa Rechnungen in einen Ordner verschieben."
            action={
              <Button variant="primary" size="sm" icon={Plus} onClick={() => update((rs) => [...rs, newRule()])}>
                Regel hinzufügen
              </Button>
            }
          />
        </Card>
      )}

      {!manual &&
        rules.map((rule, i) => (
          <RuleCard
            key={rule.id}
            rule={rule}
            index={i}
            count={rules.length}
            problem={problems[i]}
            folders={folderOptions}
            onChange={(patch) => updateRule(rule.id, patch)}
            onMove={(d) => move(i, d)}
            onRemove={() => update((rs) => rs.filter((r) => r.id !== rule.id))}
          />
        ))}

      <div className="flex flex-wrap items-center justify-between gap-2">
        {!manual && rules.length > 0 ? (
          <Button size="sm" variant="secondary" icon={Plus} onClick={() => update((rs) => [...rs, newRule()])}>
            Regel hinzufügen
          </Button>
        ) : (
          <span />
        )}
        <div className="flex gap-2">
          <Button size="sm" variant="ghost" disabled={!dirty} onClick={reset}>
            Verwerfen
          </Button>
          <Button
            size="sm"
            variant="primary"
            icon={Save}
            disabled={(!dirty && script.data?.active) || invalid || manual}
            loading={save.isPending}
            onClick={() => save.mutate()}
          >
            Speichern & aktivieren
          </Button>
        </div>
      </div>
      {invalid && <p className="text-right text-xs text-red-600 dark:text-red-400">Bitte die markierten Regeln vervollständigen.</p>}
    </div>
  );
}

function RuleCard({
  rule,
  index,
  count,
  problem,
  folders,
  onChange,
  onMove,
  onRemove,
}: {
  rule: FilterRule;
  index: number;
  count: number;
  problem: string | null;
  folders: { value: string; label: string }[];
  onChange: (patch: Partial<FilterRule>) => void;
  onMove: (delta: number) => void;
  onRemove: () => void;
}) {
  const setCondition = (i: number, patch: Partial<FilterCondition>) =>
    onChange({ conditions: rule.conditions.map((c, j) => (j === i ? { ...c, ...patch } : c)) });
  const setAction = (i: number, patch: Partial<FilterAction>) =>
    onChange({ actions: rule.actions.map((a, j) => (j === i ? { ...a, ...patch } : a)) });

  return (
    <Card className={cn(!rule.enabled && "opacity-70")}>
      <div className="flex flex-wrap items-center gap-3 border-b border-zinc-100 px-5 py-3 dark:border-zinc-800">
        <span className="text-xs font-medium tabular-nums text-zinc-400">#{index + 1}</span>
        <Input
          className="min-w-[12rem] flex-1"
          value={rule.name}
          placeholder="Name der Regel (optional)"
          onChange={(e) => onChange({ name: e.target.value })}
        />
        <div className="flex items-center gap-2">
          <Switch size="sm" checked={rule.enabled} onChange={(v) => onChange({ enabled: v })} aria-label="Regel aktiv" />
          <span className="text-xs text-zinc-500 dark:text-zinc-400">{rule.enabled ? "Aktiv" : "Aus"}</span>
        </div>
        <div className="flex items-center gap-1">
          <Button size="icon-sm" variant="ghost" icon={ArrowUp} disabled={index === 0} onClick={() => onMove(-1)} aria-label="Nach oben" />
          <Button size="icon-sm" variant="ghost" icon={ArrowDown} disabled={index === count - 1} onClick={() => onMove(1)} aria-label="Nach unten" />
          <Button size="icon-sm" variant="danger-ghost" icon={Trash2} onClick={onRemove} aria-label="Regel löschen" />
        </div>
      </div>
      <CardBody className="space-y-5">
        <section className="space-y-2">
          <div className="flex flex-wrap items-center gap-2 text-sm font-medium text-zinc-800 dark:text-zinc-200">
            Wenn
            {rule.conditions.length > 1 ? (
              <Segmented
                size="sm"
                value={rule.match}
                onChange={(v) => onChange({ match: v })}
                items={[
                  { value: "all", label: "alle" },
                  { value: "any", label: "eine" },
                ]}
              />
            ) : null}
            {rule.conditions.length > 1 ? "der Bedingungen zutreffen" : rule.conditions.length === 0 ? "– immer" : null}
          </div>
          {rule.conditions.map((c, i) => (
            <ConditionRow key={i} cond={c} onChange={(p) => setCondition(i, p)} onRemove={() => onChange({ conditions: rule.conditions.filter((_, j) => j !== i) })} />
          ))}
          <Button
            size="xs"
            variant="ghost"
            icon={Plus}
            onClick={() => onChange({ conditions: [...rule.conditions, { field: "from", op: "contains", value: "" }] })}
          >
            Bedingung
          </Button>
        </section>
        <section className="space-y-2">
          <div className="text-sm font-medium text-zinc-800 dark:text-zinc-200">dann</div>
          {rule.actions.map((a, i) => (
            <ActionRow
              key={i}
              action={a}
              folders={folders}
              onChange={(p) => setAction(i, p)}
              onRemove={() => onChange({ actions: rule.actions.filter((_, j) => j !== i) })}
            />
          ))}
          <Button size="xs" variant="ghost" icon={Plus} onClick={() => onChange({ actions: [...rule.actions, { type: "seen" }] })}>
            Aktion
          </Button>
        </section>
        {problem && rule.enabled && <p className="text-xs text-red-600 dark:text-red-400">{problem}</p>}
      </CardBody>
    </Card>
  );
}

function ConditionRow({ cond, onChange, onRemove }: { cond: FilterCondition; onChange: (p: Partial<FilterCondition>) => void; onRemove: () => void }) {
  const isSize = cond.field === "size";
  return (
    <div className="flex flex-wrap items-center gap-2">
      <Select
        size="sm"
        className="w-36"
        value={cond.field}
        options={(Object.keys(fieldLabels) as CondField[]).map((f) => ({ value: f, label: fieldLabels[f] }))}
        onChange={(e) => {
          const field = e.target.value as CondField;
          const toSize = field === "size";
          onChange({
            field,
            op: toSize ? "over" : isSize ? "contains" : cond.op,
            value: toSize !== isSize ? "" : cond.value,
          });
        }}
      />
      {cond.field === "header" && (
        <Input className="w-40" value={cond.header ?? ""} placeholder="z. B. List-Id" onChange={(e) => onChange({ header: e.target.value })} />
      )}
      <Select
        size="sm"
        className="w-36"
        value={cond.op}
        options={isSize ? sizeOps : textOps}
        onChange={(e) => onChange({ op: e.target.value as CondOp })}
      />
      <div className="flex min-w-[10rem] flex-1 items-center gap-2">
        <Input
          className="flex-1"
          value={cond.value}
          inputMode={isSize ? "numeric" : undefined}
          placeholder={isSize ? "Größe" : "Wert"}
          onChange={(e) => onChange({ value: e.target.value })}
        />
        {isSize && <span className="text-xs text-zinc-500">KB</span>}
      </div>
      <Button size="icon-sm" variant="ghost" icon={X} onClick={onRemove} aria-label="Bedingung entfernen" />
    </div>
  );
}

function ActionRow({
  action,
  folders,
  onChange,
  onRemove,
}: {
  action: FilterAction;
  folders: { value: string; label: string }[];
  onChange: (p: Partial<FilterAction>) => void;
  onRemove: () => void;
}) {
  const options = folders.some((f) => f.value === action.mailbox) || !action.mailbox ? folders : [...folders, { value: action.mailbox, label: `${action.mailbox} (fehlt)` }];
  return (
    <div className="flex flex-wrap items-center gap-2">
      <Select
        size="sm"
        className="w-52"
        value={action.type}
        options={(Object.keys(actionLabels) as ActionType[]).map((t) => ({ value: t, label: actionLabels[t] }))}
        onChange={(e) => onChange({ type: e.target.value as ActionType })}
      />
      {action.type === "fileinto" && (
        <Select
          size="sm"
          className="min-w-[12rem] flex-1"
          value={action.mailbox ?? ""}
          placeholder="Ordner wählen …"
          options={options}
          onChange={(e) => onChange({ mailbox: e.target.value })}
        />
      )}
      {action.type === "redirect" && (
        <>
          <Input
            className="min-w-[12rem] flex-1"
            type="email"
            value={action.address ?? ""}
            placeholder="adresse@example.com"
            onChange={(e) => onChange({ address: e.target.value })}
          />
          <label className="flex items-center gap-1.5 text-xs text-zinc-600 dark:text-zinc-400">
            <Checkbox checked={!!action.keepCopy} onChange={(e) => onChange({ keepCopy: e.target.checked })} />
            Kopie behalten
          </label>
        </>
      )}
      {action.type !== "fileinto" && action.type !== "redirect" && <span className="flex-1" />}
      <Button size="icon-sm" variant="ghost" icon={X} onClick={onRemove} aria-label="Aktion entfernen" />
    </div>
  );
}

// ---------------------------------------------------------------------------
// Expert mode
// ---------------------------------------------------------------------------

function ExpertEditor({ scripts, active, onDraft }: { scripts: string[]; active: string | null; onDraft: (s: string) => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const confirm = useConfirm();
  const [name, setName] = useState(active ?? RULES_SCRIPT);
  const [newName, setNewName] = useState("");
  const script = useQuery({ queryKey: scriptKey(name), queryFn: () => loadScript(name) });
  const [content, setContent] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [valid, setValid] = useState(false);
  const gutter = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (script.data !== undefined) {
      setContent(script.data?.content ?? "");
      setError(null);
      setValid(false);
    }
  }, [script.data]);

  const text = content ?? "";
  const dirty = script.data !== undefined && text !== (script.data?.content ?? "");
  useEffect(() => {
    if (dirty) onDraft(text);
  }, [dirty, text, onDraft]);

  const errPos = error ? errorPosition(error) : null;
  const lineCount = Math.max(text.split("\n").length, 12);

  const onFail = (err: unknown, fallback: string) => {
    if (isApiError(err, "invalid_script") || isApiError(err, "invalid")) {
      setError(errorMessage(err));
      setValid(false);
    } else {
      toast.error(err, fallback);
    }
  };

  const check = useMutation({
    mutationFn: () => sieveApi.check(text),
    onSuccess: () => {
      setError(null);
      setValid(true);
    },
    onError: (err) => onFail(err, "Prüfen fehlgeschlagen"),
  });
  const save = useMutation({
    mutationFn: (activate: boolean) => sieveApi.put(name, text, activate),
    onSuccess: (sc, activate) => {
      qc.setQueryData(scriptKey(name), sc);
      qc.invalidateQueries({ queryKey: listKey });
      setError(null);
      setValid(true);
      toast.success(activate ? "Skript gespeichert und aktiviert" : "Skript gespeichert");
    },
    onError: (err) => onFail(err, "Speichern fehlgeschlagen"),
  });
  const deactivate = useMutation({
    mutationFn: sieveApi.deactivate,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["sieve"] });
      toast.success("Filter deaktiviert");
    },
    onError: (err) => toast.error(err, "Deaktivieren fehlgeschlagen"),
  });
  const remove = useMutation({
    mutationFn: () => sieveApi.remove(name),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["sieve"] });
      toast.success("Skript gelöscht");
      setName(active ?? RULES_SCRIPT);
    },
    onError: (err) => toast.error(err, "Löschen fehlgeschlagen"),
  });

  const names = Array.from(new Set([...scripts, name, RULES_SCRIPT])).sort();
  const isActive = active === name;
  const exists = !!script.data;

  return (
    <Card>
      <CardHeader
        title="Sieve-Skript bearbeiten"
        description="Direkte Bearbeitung nach RFC 5228. Skripte lassen sich auch per ManageSieve (Port 4190) aus Mailprogrammen verwalten."
        actions={isActive ? <Badge tone="green" dot>Aktiv</Badge> : exists ? <Badge tone="neutral">Inaktiv</Badge> : <Badge tone="amber">Neu</Badge>}
      />
      <CardBody className="space-y-4">
        <div className="flex flex-wrap items-end gap-2">
          <div className="w-60">
            <label className="mb-1.5 block text-xs font-medium text-zinc-600 dark:text-zinc-400">Skript</label>
            <Select size="sm" value={name} options={names.map((n) => ({ value: n, label: n }))} onChange={(e) => setName(e.target.value)} />
          </div>
          <div className="flex items-end gap-2">
            <Input className="w-48" value={newName} placeholder="Neues Skript …" onChange={(e) => setNewName(e.target.value)} />
            <Button
              size="sm"
              variant="secondary"
              icon={Plus}
              disabled={newName.trim() === ""}
              onClick={() => {
                setName(newName.trim());
                setNewName("");
              }}
            >
              Anlegen
            </Button>
          </div>
        </div>
        {name === RULES_SCRIPT && (
          <Notice tone="info" icon={Info}>
            Dieses Skript erzeugt der Regel-Editor. Nach manuellen Änderungen kann er es nicht mehr darstellen.
          </Notice>
        )}
        {script.isLoading || content === null ? (
          <Skeleton className="h-72" />
        ) : (
          <div className="flex overflow-hidden rounded-lg border border-zinc-200 bg-white focus-within:border-indigo-500 focus-within:ring-2 focus-within:ring-indigo-500/20 dark:border-zinc-700 dark:bg-zinc-950">
            <div
              ref={gutter}
              aria-hidden
              className="select-none overflow-hidden border-r border-zinc-100 bg-zinc-50 py-2 text-right font-mono text-xs leading-5 text-zinc-400 dark:border-zinc-800 dark:bg-zinc-900"
              style={{ maxHeight: "28rem" }}
            >
              {Array.from({ length: lineCount }, (_, i) => (
                <div key={i} className={cn("px-2", errPos?.line === i + 1 && "bg-red-100 font-semibold text-red-600 dark:bg-red-500/20 dark:text-red-300")}>
                  {i + 1}
                </div>
              ))}
            </div>
            <textarea
              value={text}
              spellCheck={false}
              wrap="off"
              onScroll={(e) => {
                if (gutter.current) gutter.current.scrollTop = e.currentTarget.scrollTop;
              }}
              onChange={(e) => {
                setContent(e.target.value);
                setValid(false);
              }}
              className="min-h-[18rem] flex-1 resize-y bg-transparent px-3 py-2 font-mono text-xs leading-5 text-zinc-900 outline-none dark:text-zinc-100"
              style={{ maxHeight: "28rem" }}
              placeholder={'require ["fileinto"];\nif header :contains "subject" "Rechnung" {\n  fileinto "Rechnungen";\n}'}
            />
          </div>
        )}
        {error && (
          <Notice tone="error" icon={CircleAlert} title="Das Skript enthält einen Fehler">
            {error}
          </Notice>
        )}
        {valid && !error && (
          <Notice tone="success" icon={CircleCheck}>
            Das Skript ist gültig.
          </Notice>
        )}
      </CardBody>
      <CardFooter className="justify-between">
        <div className="flex gap-2">
          {isActive && (
            <Button size="sm" variant="ghost" loading={deactivate.isPending} onClick={() => deactivate.mutate()}>
              Deaktivieren
            </Button>
          )}
          {exists && !isActive && (
            <Button
              size="sm"
              variant="danger-ghost"
              icon={Trash2}
              loading={remove.isPending}
              onClick={async () => {
                const ok = await confirm({ title: `Skript „${name}“ löschen?`, confirmLabel: "Löschen", danger: true });
                if (ok) remove.mutate();
              }}
            >
              Löschen
            </Button>
          )}
        </div>
        <div className="flex gap-2">
          <Button size="sm" variant="ghost" loading={check.isPending} onClick={() => check.mutate()}>
            Prüfen
          </Button>
          <Button size="sm" variant="secondary" disabled={!dirty && exists} loading={save.isPending && !save.variables} onClick={() => save.mutate(false)}>
            Speichern
          </Button>
          <Button size="sm" variant="primary" icon={Save} disabled={!dirty && isActive} loading={save.isPending && !!save.variables} onClick={() => save.mutate(true)}>
            Speichern & aktivieren
          </Button>
        </div>
      </CardFooter>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Dry run
// ---------------------------------------------------------------------------

type Source = "active" | "rules" | "expert";

function flagLabel(f: string): string {
  switch (f.toLowerCase()) {
    case "\\seen":
      return "gelesen";
    case "\\flagged":
      return "markiert";
    case "\\answered":
      return "beantwortet";
    case "\\deleted":
      return "gelöscht";
    case "\\draft":
      return "Entwurf";
  }
  return f;
}

function describe(a: SieveAction): string {
  const flags = a.flags?.length ? ` – ${a.flags.map(flagLabel).join(", ")}` : "";
  switch (a.type) {
    case "keep":
      return `Im Posteingang ablegen${a.implicit ? " (keine Regel hat gegriffen oder die Verarbeitung lief weiter)" : ""}${flags}`;
    case "fileinto":
      return `In Ordner „${a.mailbox}“ ablegen${a.copy ? " (Kopie)" : ""}${a.create ? ", Ordner wird bei Bedarf angelegt" : ""}${flags}`;
    case "redirect":
      return `Weiterleiten an ${a.address}${a.copy ? " (Kopie bleibt erhalten)" : ""}`;
    case "discard":
      return "Verwerfen – die Nachricht wird nicht abgelegt";
    case "reject":
    case "ereject":
      return `Ablehnen: „${a.reason ?? ""}“`;
    case "vacation":
      return `Abwesenheitsantwort senden${a.vacation?.subject ? ` (Betreff „${a.vacation.subject}“)` : ""}`;
  }
  return a.type;
}

function TestPanel({ active, drafts }: { active: string | null; drafts: { rules?: string; expert?: string } }) {
  const toast = useToast();
  const mailboxes = useMailboxes();
  const inbox = mailboxes.data?.find((m) => m.role === "inbox");
  const messages = useQuery({
    queryKey: ["sieve", "test-messages", inbox?.id],
    queryFn: () => mailApi.messages({ mailbox: inbox?.id, limit: 25 }),
    enabled: !!inbox,
  });
  const sources: { value: Source; label: string }[] = [
    ...(active ? [{ value: "active" as const, label: "Aktives Skript" }] : []),
    ...(drafts.rules !== undefined ? [{ value: "rules" as const, label: "Entwurf Regel-Editor" }] : []),
    ...(drafts.expert !== undefined ? [{ value: "expert" as const, label: "Entwurf Experten-Modus" }] : []),
  ];
  const [chosen, setSource] = useState<Source>(sources[0]?.value ?? "active");
  const source = sources.some((s) => s.value === chosen) ? chosen : (sources[0]?.value ?? "active");
  const [mode, setMode] = useState<"message" | "raw">("message");
  const [selected, setSelected] = useState<number | null>(null);
  const [raw, setRaw] = useState("");
  const [result, setResult] = useState<SieveTestResult | null>(null);
  const [error, setError] = useState<string | null>(null);

  const run = useMutation({
    mutationFn: () => {
      const content = source === "rules" ? drafts.rules : source === "expert" ? drafts.expert : undefined;
      return sieveApi.test({ content, messageId: mode === "message" ? selected ?? undefined : undefined, raw: mode === "raw" ? raw : undefined });
    },
    onSuccess: (r) => {
      setResult(r);
      setError(null);
    },
    onError: (err) => {
      setResult(null);
      if (isApiError(err, "invalid_script") || isApiError(err, "invalid")) setError(errorMessage(err));
      else toast.error(err, "Test fehlgeschlagen");
    },
  });

  if (sources.length === 0) {
    return (
      <Card>
        <EmptyState
          icon={FlaskConical}
          title="Nichts zu testen"
          description="Aktiviere ein Skript oder bearbeite die Regeln, um sie mit einer Nachricht auszuprobieren."
        />
      </Card>
    );
  }
  const items = messages.data?.items ?? [];
  const canRun = mode === "message" ? selected !== null : raw.trim() !== "";

  return (
    <Card>
      <CardHeader title="Regeln testen" description="Probelauf ohne Wirkung: Es wird nichts verschoben, weitergeleitet oder gelöscht." />
      <CardBody className="space-y-4">
        <div className="flex flex-wrap items-center gap-3">
          <Segmented size="sm" value={source} onChange={setSource} items={sources} />
          <Segmented
            size="sm"
            value={mode}
            onChange={setMode}
            items={[
              { value: "message", label: "Nachricht aus dem Posteingang" },
              { value: "raw", label: "Rohnachricht" },
            ]}
          />
        </div>
        {mode === "message" ? (
          <div className="max-h-72 overflow-y-auto rounded-lg border border-zinc-200 dark:border-zinc-800">
            {messages.isLoading ? (
              <div className="space-y-2 p-3">
                <Skeleton className="h-8" />
                <Skeleton className="h-8" />
              </div>
            ) : items.length === 0 ? (
              <p className="p-4 text-sm text-zinc-500">Der Posteingang ist leer.</p>
            ) : (
              <ul className="divide-y divide-zinc-100 dark:divide-zinc-800">
                {items.map((m) => (
                  <li key={m.id}>
                    <button
                      type="button"
                      onClick={() => setSelected(m.id)}
                      className={cn(
                        "flex w-full items-center gap-3 px-3 py-2 text-left text-sm hover:bg-zinc-50 dark:hover:bg-zinc-800/60",
                        selected === m.id && "bg-indigo-50 dark:bg-indigo-500/10",
                      )}
                    >
                      <span className="w-40 shrink-0 truncate text-zinc-600 dark:text-zinc-400">{m.from.name || m.from.email}</span>
                      <span className="min-w-0 flex-1 truncate text-zinc-900 dark:text-zinc-100">{m.subject || "(kein Betreff)"}</span>
                      <span className="shrink-0 text-xs text-zinc-400">{formatRelative(m.receivedAt)}</span>
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </div>
        ) : (
          <Textarea
            rows={8}
            className="font-mono text-xs"
            value={raw}
            onChange={(e) => setRaw(e.target.value)}
            placeholder={"From: absender@example.com\nTo: ich@example.com\nSubject: Rechnung März\n\nText der Nachricht"}
          />
        )}
        {error && (
          <Notice tone="error" icon={CircleAlert} title="Test nicht möglich">
            {error}
          </Notice>
        )}
        {result && (
          <div className="space-y-2">
            {result.error && (
              <Notice tone="warning" icon={CircleAlert} title="Laufzeitfehler">
                {result.error} – die Nachricht würde deshalb im Posteingang abgelegt.
              </Notice>
            )}
            <div className="text-sm font-medium text-zinc-800 dark:text-zinc-200">Ergebnis{result.script ? ` (Skript „${result.script}“)` : ""}</div>
            <ol className="space-y-1.5">
              {result.actions.map((a, i) => (
                <li key={i}className="flex items-start gap-2 rounded-lg bg-zinc-50 px-3 py-2 text-sm text-zinc-800 dark:bg-zinc-800/60 dark:text-zinc-200">
                  <Badge tone={a.type === "discard" || a.type === "reject" || a.type === "ereject" ? "red" : a.type === "redirect" ? "blue" : "indigo"}>
                    {i + 1}
                  </Badge>
                  <span>{describe(a)}</span>
                </li>
              ))}
            </ol>
          </div>
        )}
      </CardBody>
      <CardFooter>
        <Button size="sm" variant="primary" icon={FlaskConical} disabled={!canRun} loading={run.isPending} onClick={() => run.mutate()}>
          Testen
        </Button>
      </CardFooter>
    </Card>
  );
}
