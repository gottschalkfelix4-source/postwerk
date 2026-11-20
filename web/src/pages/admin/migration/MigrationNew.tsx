import { useMemo, useRef, useState, type ChangeEvent, type ReactNode } from "react";
import { Link, useNavigate } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowLeft,
  CircleCheck,
  CircleX,
  FileSpreadsheet,
  FolderInput,
  Info,
  KeyRound,
  ListChecks,
  Lock,
  Mail,
  Play,
  PlugZap,
  Plus,
  Server,
  Settings2,
  Sparkles,
  Trash2,
  TriangleAlert,
  Upload,
  Users,
  WandSparkles,
} from "lucide-react";
import { adminApi } from "@/api/endpoints";
import { errorMessage } from "@/api/client";
import type { Account, ImapSecurity, MigrationAccountInput, MigrationSource, MigrationTestResult } from "@/api/types";
import { formatNumber, pluralize } from "@/lib/format";
import { useAiStatus } from "@/lib/queries";
import { cn, isEmail } from "@/lib/utils";
import {
  Badge,
  Button,
  Checkbox,
  Field,
  Input,
  Modal,
  Notice,
  PageHeader,
  PasswordInput,
  Segmented,
  SwitchRow,
  TBody,
  THead,
  Table,
  Td,
  Textarea,
  Th,
  Tr,
  WizardLayout,
  WizardNav,
  WizardStep,
  useToast,
  type StepItem,
} from "@/components/ui";
import { generatePassword } from "../shared";
import { folderKind, parseMailboxCsv, securityLabels, sourcePresets, targetLabel, type CsvRow } from "./shared";

interface Row {
  key: number;
  targetEmail: string;
  username: string;
  password: string;
  name: string;
  newPassword: string;
}

interface Options {
  includeTrash: boolean;
  includeJunk: boolean;
  sinceEnabled: boolean;
  since: string;
  analyzeWithAI: boolean;
  name: string;
}

const steps: StepItem[] = [
  { id: "source", label: "Quelle", description: "Anbieter und Server" },
  { id: "test", label: "Testanmeldung", description: "Ordner prüfen" },
  { id: "mailboxes", label: "Postfächer", description: "Zuordnung" },
  { id: "options", label: "Optionen" },
  { id: "summary", label: "Zusammenfassung", description: "Prüfen und starten" },
];

const PAGE = 200;

/** All accounts, following pagination (max. 200 per request). */
async function fetchAllAccounts(): Promise<Account[]> {
  const out: Account[] = [];
  for (let offset = 0; offset < 20_000; offset += PAGE) {
    const page = await adminApi.accounts({ limit: PAGE, offset });
    out.push(...page.items);
    if (out.length >= page.total || page.items.length < PAGE) break;
  }
  return out;
}

let rowKey = 1;
const newRow = (patch: Partial<Row> = {}): Row => ({
  key: rowKey++,
  targetEmail: "",
  username: "",
  password: "",
  name: "",
  newPassword: "",
  ...patch,
});

const localPart = (email: string) => email.split("@")[0] ?? email;
const domainPart = (email: string) => (email.split("@")[1] ?? "").toLowerCase();

export default function MigrationNewPage() {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const toast = useToast();

  const [current, setCurrent] = useState(0);
  const [reached, setReached] = useState(0);
  const [direction, setDirection] = useState<"forward" | "back">("forward");

  const [presetId, setPresetId] = useState<string | null>(null);
  const [source, setSource] = useState<MigrationSource>({ host: "", port: 993, security: "tls" });
  const [testUser, setTestUser] = useState("");
  const [testPass, setTestPass] = useState("");
  const [test, setTest] = useState<MigrationTestResult | null>(null);
  const [excluded, setExcluded] = useState<Set<string>>(new Set());
  const [rows, setRows] = useState<Row[]>([]);
  const [options, setOptions] = useState<Options>({
    includeTrash: false,
    includeJunk: false,
    sinceEnabled: false,
    since: "",
    analyzeWithAI: false,
    name: "",
  });

  const accounts = useQuery({ queryKey: ["admin", "accounts", "all"], queryFn: fetchAllAccounts, staleTime: 30_000 });
  const domains = useQuery({ queryKey: ["admin", "domains"], queryFn: adminApi.domains });
  const existing = useMemo(() => new Set((accounts.data ?? []).map((a) => a.email.toLowerCase())), [accounts.data]);
  const localDomains = useMemo(() => (domains.data ? new Set(domains.data.map((d) => d.name.toLowerCase())) : null), [domains.data]);

  const testMutation = useMutation({
    mutationFn: () => adminApi.testMigration({ source: { ...source, host: source.host.trim() }, username: testUser.trim(), password: testPass }),
    onSuccess: (res) => {
      setTest(res);
      setExcluded(new Set());
    },
    onError: (err) => setTest({ ok: false, folders: [], error: errorMessage(err) }),
  });

  const invalidateTest = () => {
    if (test) setTest(null);
  };

  const go = (index: number) => {
    const target = Math.max(0, Math.min(steps.length - 1, index));
    if (target === 2 && rows.length === 0) {
      setRows([newRow({ targetEmail: isEmail(testUser) ? testUser.trim().toLowerCase() : "", username: testUser.trim(), password: testPass })]);
    }
    setDirection(target < current ? "back" : "forward");
    setCurrent(target);
    setReached((r) => Math.max(r, target));
  };

  // --- validation ----------------------------------------------------------

  const hostValid = /^[a-z0-9.-]+$/i.test(source.host.trim()) && source.host.includes(".");
  const portValid = Number.isInteger(source.port) && source.port > 0 && source.port < 65536;

  const rowIssues = useMemo(() => {
    const counts = new Map<string, number>();
    rows.forEach((r) => {
      const k = r.targetEmail.trim().toLowerCase();
      if (k) counts.set(k, (counts.get(k) ?? 0) + 1);
    });
    return rows.map((r) => {
      const email = r.targetEmail.trim().toLowerCase();
      const issues: string[] = [];
      if (!isEmail(email)) issues.push("Gültige Zieladresse eingeben.");
      else if ((counts.get(email) ?? 0) > 1) issues.push("Zieladresse ist doppelt.");
      else if (!existing.has(email) && localDomains && !localDomains.has(domainPart(email))) {
        issues.push(`Domain „${domainPart(email)}“ ist auf diesem Server nicht angelegt.`);
      }
      if (!r.username.trim()) issues.push("Quell-Benutzer fehlt.");
      if (!r.password) issues.push("Quell-Passwort fehlt.");
      if (isEmail(email) && !existing.has(email) && r.newPassword.length < 10) issues.push("Neues Passwort (min. 10 Zeichen) für das Zielkonto.");
      return issues;
    });
  }, [rows, existing, localDomains]);

  const rowsValid = rows.length > 0 && rowIssues.every((i) => i.length === 0) && !accounts.isLoading;
  const sinceValid = !options.sinceEnabled || /^\d{4}-\d{2}-\d{2}$/.test(options.since);
  const defaultName = source.host.trim() ? `Migration von ${source.host.trim()}` : "Migration";

  const canEnter = (index: number): boolean => {
    if (index >= 1 && !(hostValid && portValid)) return false;
    if (index >= 2 && !test?.ok) return false;
    if (index >= 3 && !rowsValid) return false;
    if (index >= 4 && !sinceValid) return false;
    return true;
  };

  // --- start ---------------------------------------------------------------

  const create = useMutation({
    mutationFn: () => {
      const accountsInput: MigrationAccountInput[] = rows.map((r) => {
        const email = r.targetEmail.trim().toLowerCase();
        const base: MigrationAccountInput = { targetEmail: email, username: r.username.trim(), password: r.password };
        if (!existing.has(email)) base.createAccount = { name: r.name.trim() || localPart(email), password: r.newPassword };
        return base;
      });
      return adminApi.createMigration({
        name: options.name.trim() || defaultName,
        source: { ...source, host: source.host.trim() },
        accounts: accountsInput,
        options: {
          includeTrash: options.includeTrash,
          includeJunk: options.includeJunk,
          excludeFolders: [...excluded],
          since: options.sinceEnabled ? options.since : null,
          analyzeWithAI: options.analyzeWithAI,
        },
      });
    },
    onSuccess: (job) => {
      qc.setQueryData(["admin", "migration", job.id], job);
      void qc.invalidateQueries({ queryKey: ["admin", "migrations"] });
      void qc.invalidateQueries({ queryKey: ["admin", "accounts"] });
      toast.success("Migration gestartet", job.name);
      navigate(`/admin/migration/${job.id}`, { replace: true });
    },
    onError: (err) => toast.error(err, "Migration konnte nicht gestartet werden"),
  });

  const back = current > 0 ? () => go(current - 1) : undefined;

  return (
    <>
      <Link
        to="/admin/migration"
        className="mb-4 inline-flex items-center gap-1.5 text-sm text-zinc-500 hover:text-zinc-800 dark:text-zinc-400 dark:hover:text-zinc-200"
      >
        <ArrowLeft className="h-4 w-4" /> Alle Migrationen
      </Link>
      <PageHeader title="Neue Migration" description="Postfächer von einem anderen IMAP-Server übernehmen." />

      <WizardLayout
        steps={steps}
        current={current}
        reached={reached}
        direction={direction}
        breakpoint="xl"
        onSelect={(i) => canEnter(i) && go(i)}
        aside={
          <div className="mt-8 rounded-lg border border-zinc-200 p-3 text-xs leading-relaxed text-zinc-500 dark:border-zinc-800 dark:text-zinc-400">
            <Lock className="mb-1.5 h-3.5 w-3.5 text-zinc-400" />
            Zugangsdaten der Quelle werden nur im Arbeitsspeicher gehalten und nie gespeichert.
          </div>
        }
      >
        {current === 0 && (
          <WizardStep
            icon={Server}
            title="Quelle"
            description="Von welchem Anbieter oder Server sollen die Postfächer kommen?"
            footer={<WizardNav onNext={() => go(1)} nextDisabled={!hostValid || !portValid} />}
          >
            <SourceStep
              presetId={presetId}
              source={source}
              onPreset={(id) => {
                setPresetId(id);
                const p = sourcePresets.find((x) => x.id === id);
                if (p) setSource({ host: p.host, port: p.port, security: p.security });
                else setSource((s) => ({ ...s, host: "" }));
                invalidateTest();
              }}
              onChange={(s) => {
                setSource(s);
                invalidateTest();
              }}
              hostInvalid={!!source.host && !hostValid}
              portInvalid={!portValid}
            />
          </WizardStep>
        )}

        {current === 1 && (
          <WizardStep
            icon={KeyRound}
            title="Testanmeldung"
            description={`Mit einem Postfach auf ${source.host} anmelden und die Ordner abrufen.`}
            footer={<WizardNav onBack={back} onNext={() => go(2)} nextDisabled={!test?.ok} />}
          >
            <TestStep
              source={source}
              user={testUser}
              pass={testPass}
              onUser={(v) => {
                setTestUser(v);
                invalidateTest();
              }}
              onPass={(v) => {
                setTestPass(v);
                invalidateTest();
              }}
              hint={sourcePresets.find((p) => p.id === presetId)?.hint}
              result={test}
              pending={testMutation.isPending}
              onTest={() => testMutation.mutate()}
              excluded={excluded}
              setExcluded={setExcluded}
              options={options}
              setOptions={setOptions}
            />
          </WizardStep>
        )}

        {current === 2 && (
          <WizardStep
            icon={Users}
            title="Postfächer"
            description="Welche Quell-Postfächer sollen in welche Konten übernommen werden?"
            footer={<WizardNav onBack={back} onNext={() => go(3)} nextDisabled={!rowsValid} />}
          >
            <MailboxStep
              rows={rows}
              setRows={setRows}
              issues={rowIssues}
              existing={existing}
              accountsLoading={accounts.isLoading}
              accountsError={accounts.error}
            />
          </WizardStep>
        )}

        {current === 3 && (
          <WizardStep
            icon={Settings2}
            title="Optionen"
            description="Feinsteuerung für den Import."
            footer={<WizardNav onBack={back} onNext={() => go(4)} nextDisabled={!sinceValid} />}
          >
            <OptionsStep options={options} setOptions={setOptions} defaultName={defaultName} sinceValid={sinceValid} />
          </WizardStep>
        )}

        {current === 4 && (
          <WizardStep
            icon={ListChecks}
            title="Zusammenfassung"
            description="Bitte prüfen – die Migration startet sofort."
            footer={
              <WizardNav onBack={back} onNext={() => create.mutate()} nextLabel="Migration starten" nextIcon={Play} loading={create.isPending} />
            }
          >
            <SummaryStep
              source={source}
              rows={rows}
              existing={existing}
              excluded={excluded}
              options={options}
              name={options.name.trim() || defaultName}
              test={test}
            />
          </WizardStep>
        )}
      </WizardLayout>
    </>
  );
}

// ---------------------------------------------------------------------------
// Step 1: source
// ---------------------------------------------------------------------------

function SourceStep({
  presetId,
  source,
  onPreset,
  onChange,
  hostInvalid,
  portInvalid,
}: {
  presetId: string | null;
  source: MigrationSource;
  onPreset: (id: string | null) => void;
  onChange: (s: MigrationSource) => void;
  hostInvalid: boolean;
  portInvalid: boolean;
}) {
  const preset = sourcePresets.find((p) => p.id === presetId);
  const setSecurity = (security: ImapSecurity) => {
    const defaults = [993, 143];
    const port = defaults.includes(source.port) ? (security === "tls" ? 993 : 143) : source.port;
    onChange({ ...source, security, port });
  };

  return (
    <div className="space-y-6">
      <div className="grid grid-cols-2 gap-2.5 sm:grid-cols-3 xl:grid-cols-5">
        {sourcePresets.map((p) => (
          <PresetTile key={p.id} active={presetId === p.id} onClick={() => onPreset(p.id)} title={p.name} subtitle={p.host}>
            <span className={cn("flex h-8 w-8 items-center justify-center rounded-lg text-sm font-bold", p.tone)}>{p.name[0]}</span>
          </PresetTile>
        ))}
        <PresetTile active={presetId === "custom"} onClick={() => onPreset("custom")} title="Eigener Server" subtitle="mailcow, Dovecot …">
          <span className="flex h-8 w-8 items-center justify-center rounded-lg bg-zinc-100 text-zinc-500 dark:bg-zinc-800 dark:text-zinc-400">
            <Server className="h-4 w-4" />
          </span>
        </PresetTile>
      </div>

      {preset?.hint && (
        <Notice tone="info" icon={Info} title={preset.name}>
          {preset.hint}
        </Notice>
      )}

      <div className="grid gap-4 border-t border-zinc-100 pt-5 dark:border-zinc-800 sm:grid-cols-[1fr_8rem]">
        <Field label="IMAP-Server" required error={hostInvalid ? "Bitte einen gültigen Hostnamen eingeben." : null}>
          {(id) => (
            <Input
              id={id}
              icon={Server}
              value={source.host}
              onChange={(e) => onChange({ ...source, host: e.target.value })}
              placeholder="imap.beispiel.de"
              invalid={hostInvalid}
              autoComplete="off"
              spellCheck={false}
              className="font-mono text-[13px]"
            />
          )}
        </Field>
        <Field label="Port" required error={portInvalid ? "1–65535" : null}>
          {(id) => (
            <Input
              id={id}
              type="number"
              min={1}
              max={65535}
              value={Number.isFinite(source.port) ? String(source.port) : ""}
              onChange={(e) => onChange({ ...source, port: e.target.value === "" ? NaN : Number(e.target.value) })}
              invalid={portInvalid}
              className="tabular-nums"
            />
          )}
        </Field>
      </div>
      <Field label="Verschlüsselung" help={source.security === "none" ? "Passwörter werden unverschlüsselt übertragen – nur im lokalen Netz verwenden." : undefined}>
        {() => (
          <Segmented<ImapSecurity>
            value={source.security}
            onChange={setSecurity}
            items={[
              { value: "tls", label: securityLabels.tls },
              { value: "starttls", label: securityLabels.starttls },
              { value: "none", label: securityLabels.none },
            ]}
          />
        )}
      </Field>
    </div>
  );
}

function PresetTile({
  active,
  onClick,
  title,
  subtitle,
  children,
}: {
  active: boolean;
  onClick: () => void;
  title: string;
  subtitle: string;
  children: ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      className={cn(
        "flex min-w-0 flex-col items-start gap-2 rounded-xl border p-3 text-left transition",
        "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-indigo-500/50",
        active
          ? "border-indigo-400 bg-indigo-50/60 ring-1 ring-indigo-400 dark:border-indigo-500/60 dark:bg-indigo-500/10 dark:ring-indigo-500/60"
          : "border-zinc-200 hover:border-zinc-300 hover:bg-zinc-50 dark:border-zinc-800 dark:hover:border-zinc-700 dark:hover:bg-zinc-800/50",
      )}
    >
      {children}
      <span className="w-full min-w-0">
        <span className="block truncate text-sm font-medium text-zinc-900 dark:text-zinc-100">{title}</span>
        <span className="block truncate font-mono text-[11px] text-zinc-500 dark:text-zinc-400">{subtitle}</span>
      </span>
    </button>
  );
}

// ---------------------------------------------------------------------------
// Step 2: test login and folders
// ---------------------------------------------------------------------------

function TestStep({
  source,
  user,
  pass,
  onUser,
  onPass,
  hint,
  result,
  pending,
  onTest,
  excluded,
  setExcluded,
  options,
  setOptions,
}: {
  source: MigrationSource;
  user: string;
  pass: string;
  onUser: (v: string) => void;
  onPass: (v: string) => void;
  hint?: string;
  result: MigrationTestResult | null;
  pending: boolean;
  onTest: () => void;
  excluded: Set<string>;
  setExcluded: (s: Set<string>) => void;
  options: Options;
  setOptions: (fn: (o: Options) => Options) => void;
}) {
  const folders = result?.folders ?? [];
  const included = (name: string, kind: "trash" | "junk" | null) =>
    kind === "trash" ? options.includeTrash : kind === "junk" ? options.includeJunk : !excluded.has(name);

  const toggle = (name: string, kind: "trash" | "junk" | null, on: boolean) => {
    if (kind === "trash") setOptions((o) => ({ ...o, includeTrash: on }));
    else if (kind === "junk") setOptions((o) => ({ ...o, includeJunk: on }));
    else {
      const next = new Set(excluded);
      if (on) next.delete(name);
      else next.add(name);
      setExcluded(next);
    }
  };

  const selected = folders.filter((f) => included(f.name, folderKind(f)));
  const selectedMessages = selected.reduce((n, f) => n + f.messages, 0);

  return (
    <div className="space-y-5">
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (user.trim() && pass) onTest();
        }}
        className="grid gap-4 sm:grid-cols-[1fr_1fr_auto] sm:items-end"
      >
        <Field label="Benutzer auf der Quelle">
          {(id) => <Input id={id} icon={Mail} value={user} onChange={(e) => onUser(e.target.value)} placeholder="name@anbieter.de" autoComplete="off" autoFocus />}
        </Field>
        <Field label="Passwort / App-Passwort">
          {(id) => <PasswordInput id={id} icon={Lock} value={pass} onChange={(e) => onPass(e.target.value)} autoComplete="off" />}
        </Field>
        <Button type="submit" icon={PlugZap} loading={pending} disabled={!user.trim() || !pass}>
          Verbindung testen
        </Button>
      </form>
      {hint && !result && (
        <p className="flex items-start gap-1.5 text-xs text-zinc-500 dark:text-zinc-400">
          <Info className="mt-px h-3.5 w-3.5 shrink-0" />
          {hint}
        </p>
      )}

      {result && !result.ok && (
        <Notice tone="error" icon={CircleX} title="Anmeldung fehlgeschlagen">
          <span className="break-words">{result.error || "Der Server hat die Anmeldung abgelehnt."}</span>
          <span className="mt-1 block text-xs opacity-80">
            Prüfe Benutzername, Passwort und Verschlüsselung ({securityLabels[source.security]}, Port {source.port}).
          </span>
        </Notice>
      )}

      {result?.ok && (
        <div className="animate-fade-in space-y-3">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div className="flex items-center gap-2 text-sm font-medium text-emerald-700 dark:text-emerald-300">
              <CircleCheck className="h-4 w-4" /> Anmeldung erfolgreich – {pluralize(folders.length, "Ordner", "Ordner")} gefunden
            </div>
            <span className="text-xs text-zinc-500 dark:text-zinc-400">
              {selected.length} ausgewählt · {formatNumber(selectedMessages)} Nachrichten
            </span>
          </div>
          <div className="overflow-hidden rounded-lg border border-zinc-200 dark:border-zinc-800">
            <Table className="max-h-[26rem] overflow-y-auto">
              <THead>
                <tr>
                  <Th className="w-10" />
                  <Th>Ordner</Th>
                  <Th className="text-right">Nachrichten</Th>
                  <Th>Ziel</Th>
                </tr>
              </THead>
              <TBody>
                {folders.map((f) => {
                  const kind = folderKind(f);
                  const on = included(f.name, kind);
                  return (
                    <Tr key={f.name} className={cn(!on && "opacity-60")} onClick={() => toggle(f.name, kind, !on)}>
                      <Td className="py-2">
                        <Checkbox checked={on} onChange={(e) => toggle(f.name, kind, e.target.checked)} onClick={(e) => e.stopPropagation()} aria-label={`${f.name} übernehmen`} />
                      </Td>
                      <Td className="py-2">
                        <div className="flex flex-wrap items-center gap-2">
                          <span className="break-all font-mono text-[13px] text-zinc-800 dark:text-zinc-200">{f.name}</span>
                          {f.specialUse && <Badge className="font-mono text-[10px]">{f.specialUse}</Badge>}
                        </div>
                      </Td>
                      <Td className="py-2 text-right tabular-nums">{formatNumber(f.messages)}</Td>
                      <Td className="py-2 text-zinc-500 dark:text-zinc-400">
                        {on ? (
                          <span className="inline-flex items-center gap-1.5">
                            <FolderInput className="h-3.5 w-3.5 text-indigo-500" /> {targetLabel(f.target)}
                          </span>
                        ) : (
                          <span className="text-xs italic">wird übersprungen</span>
                        )}
                      </Td>
                    </Tr>
                  );
                })}
              </TBody>
            </Table>
          </div>
          <p className="text-xs text-zinc-500 dark:text-zinc-400">
            Die Auswahl gilt für alle Postfächer dieser Migration. Papierkorb und Spam sind standardmäßig abgewählt.
          </p>
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Step 3: mailboxes
// ---------------------------------------------------------------------------

function MailboxStep({
  rows,
  setRows,
  issues,
  existing,
  accountsLoading,
  accountsError,
}: {
  rows: Row[];
  setRows: (fn: (rows: Row[]) => Row[]) => void;
  issues: string[][];
  existing: Set<string>;
  accountsLoading: boolean;
  accountsError: unknown;
}) {
  const [csvOpen, setCsvOpen] = useState(false);
  const update = (key: number, patch: Partial<Row>) => setRows((rs) => rs.map((r) => (r.key === key ? { ...r, ...patch } : r)));
  const remove = (key: number) => setRows((rs) => rs.filter((r) => r.key !== key));

  const importRows = (csv: CsvRow[]) => {
    setRows((rs) => {
      const kept = rs.filter((r) => r.targetEmail.trim() || r.username.trim() || r.password);
      const byEmail = new Map(kept.map((r) => [r.targetEmail.trim().toLowerCase(), r]));
      for (const c of csv) {
        const prev = byEmail.get(c.targetEmail);
        byEmail.set(c.targetEmail, newRow({ ...c, key: prev?.key ?? rowKey++ }));
      }
      return [...byEmail.values()];
    });
  };

  const newCount = rows.filter((r) => isEmail(r.targetEmail) && !existing.has(r.targetEmail.trim().toLowerCase())).length;

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="text-sm text-zinc-500 dark:text-zinc-400">
          {pluralize(rows.length, "Postfach", "Postfächer")}
          {newCount > 0 && ` · ${newCount} ${newCount === 1 ? "Konto wird" : "Konten werden"} neu angelegt`}
        </div>
        <div className="flex gap-2">
          <Button size="sm" icon={FileSpreadsheet} onClick={() => setCsvOpen(true)}>
            CSV-Import
          </Button>
          <Button size="sm" icon={Plus} onClick={() => setRows((rs) => [...rs, newRow()])}>
            Zeile hinzufügen
          </Button>
        </div>
      </div>

      {accountsError ? (
        <Notice tone="warning" icon={TriangleAlert}>
          Bestehende Konten konnten nicht geladen werden ({errorMessage(accountsError)}). Alle Zielkonten werden als neu behandelt.
        </Notice>
      ) : null}

      <div className="overflow-hidden rounded-lg border border-zinc-200 dark:border-zinc-800">
        <div className="hidden grid-cols-[minmax(0,1.3fr)_minmax(0,1fr)_minmax(0,1fr)_2.25rem] gap-3 border-b border-zinc-200 bg-zinc-50/80 px-4 py-2.5 text-xs font-semibold uppercase tracking-wide text-zinc-500 dark:border-zinc-800 dark:bg-zinc-900/60 dark:text-zinc-400 md:grid">
          <span>Zielkonto</span>
          <span>Quell-Benutzer</span>
          <span>Quell-Passwort</span>
          <span />
        </div>
        <div className="divide-y divide-zinc-100 dark:divide-zinc-800">
          {rows.map((r, i) => {
            const email = r.targetEmail.trim().toLowerCase();
            const valid = isEmail(email);
            const exists = valid && existing.has(email);
            const rowIssues = issues[i] ?? [];
            const touched = !!(r.targetEmail || r.username || r.password);
            return (
              <div key={r.key} className="space-y-2 px-4 py-3">
                <div className="grid gap-2 md:grid-cols-[minmax(0,1.3fr)_minmax(0,1fr)_minmax(0,1fr)_2.25rem] md:gap-3">
                  <Input
                    value={r.targetEmail}
                    onChange={(e) => update(r.key, { targetEmail: e.target.value })}
                    placeholder="Zielkonto, z. B. name@deine-domain.de"
                    invalid={touched && !valid}
                    aria-label="Zielkonto"
                    autoComplete="off"
                  />
                  <Input value={r.username} onChange={(e) => update(r.key, { username: e.target.value })} placeholder="Login auf der Quelle" aria-label="Quell-Benutzer" autoComplete="off" />
                  <PasswordInput value={r.password} onChange={(e) => update(r.key, { password: e.target.value })} placeholder="Quell-Passwort" aria-label="Quell-Passwort" autoComplete="off" />
                  <Button size="icon" variant="ghost" icon={Trash2} onClick={() => remove(r.key)} aria-label="Zeile entfernen" className="justify-self-end" />
                </div>
                {valid && (
                  <div className="flex flex-wrap items-center gap-2">
                    {accountsLoading ? (
                      <Badge>prüfe …</Badge>
                    ) : exists ? (
                      <Badge tone="green" dot>
                        existiert
                      </Badge>
                    ) : (
                      <>
                        <Badge tone="indigo" dot>
                          wird angelegt
                        </Badge>
                        <Input
                          value={r.name}
                          onChange={(e) => update(r.key, { name: e.target.value })}
                          placeholder={`Name (${localPart(email)})`}
                          aria-label="Name des neuen Kontos"
                          className="h-8 w-48 text-[13px]"
                        />
                        <div className="w-60">
                          <Input
                            value={r.newPassword}
                            onChange={(e) => update(r.key, { newPassword: e.target.value })}
                            placeholder="Neues Passwort"
                            aria-label="Passwort des neuen Kontos"
                            invalid={r.newPassword.length > 0 && r.newPassword.length < 10}
                            autoComplete="new-password"
                            className="h-8 font-mono text-[13px]"
                            trailing={
                              <button
                                type="button"
                                onClick={() => update(r.key, { newPassword: generatePassword() })}
                                className="rounded-md p-1 text-zinc-400 hover:bg-zinc-100 hover:text-zinc-700 dark:hover:bg-zinc-800 dark:hover:text-zinc-200"
                                title="Passwort generieren"
                                aria-label="Passwort generieren"
                              >
                                <WandSparkles className="h-4 w-4" />
                              </button>
                            }
                          />
                        </div>
                      </>
                    )}
                  </div>
                )}
                {touched && rowIssues.length > 0 && <p className="text-xs text-red-600 dark:text-red-400">{rowIssues[0]}</p>}
              </div>
            );
          })}
          {rows.length === 0 && (
            <div className="px-4 py-8 text-center text-sm text-zinc-500 dark:text-zinc-400">
              Noch keine Postfächer. Füge eine Zeile hinzu oder importiere eine CSV-Datei.
            </div>
          )}
        </div>
      </div>

      <CsvModal open={csvOpen} onClose={() => setCsvOpen(false)} onImport={importRows} />
    </div>
  );
}

const CSV_EXAMPLE = `zieladresse;quellbenutzer;quellpasswort;name;neues_passwort
anna@beispiel.de;anna@altanbieter.de;geheim1;Anna Schmidt;NeuesPasswort1
ben@beispiel.de;ben;geheim2;;`;

function CsvModal({ open, onClose, onImport }: { open: boolean; onClose: () => void; onImport: (rows: CsvRow[]) => void }) {
  const [text, setText] = useState("");
  const fileRef = useRef<HTMLInputElement>(null);
  const parsed = useMemo(() => parseMailboxCsv(text), [text]);

  const onFile = async (e: ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    e.target.value = "";
    if (!file) return;
    setText(await file.text());
  };

  return (
    <Modal
      open={open}
      onClose={onClose}
      size="lg"
      title="Postfächer aus CSV importieren"
      description="Eine Zeile pro Postfach. Trennzeichen Semikolon oder Komma, Kopfzeile optional."
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Abbrechen
          </Button>
          <Button
            variant="primary"
            disabled={parsed.rows.length === 0}
            onClick={() => {
              onImport(parsed.rows);
              setText("");
              onClose();
            }}
          >
            {parsed.rows.length > 0 ? `${pluralize(parsed.rows.length, "Postfach", "Postfächer")} übernehmen` : "Übernehmen"}
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <div className="rounded-lg bg-zinc-50 px-3 py-2 font-mono text-xs text-zinc-600 ring-1 ring-inset ring-zinc-200 dark:bg-zinc-950 dark:text-zinc-400 dark:ring-zinc-800">
          zieladresse;quellbenutzer;quellpasswort;name;neues_passwort
        </div>
        <p className="text-xs text-zinc-500 dark:text-zinc-400">
          Ohne Quell-Benutzer wird die Zieladresse verwendet. Name und neues Passwort sind nur für Konten nötig, die noch nicht existieren.
        </p>
        <Textarea
          value={text}
          onChange={(e) => setText(e.target.value)}
          rows={9}
          placeholder={CSV_EXAMPLE}
          spellCheck={false}
          className="font-mono text-xs"
          aria-label="CSV-Inhalt"
        />
        <div className="flex flex-wrap items-center justify-between gap-2">
          <input ref={fileRef} type="file" accept=".csv,.txt,text/csv,text/plain" className="hidden" onChange={(e) => void onFile(e)} />
          <Button size="sm" icon={Upload} onClick={() => fileRef.current?.click()}>
            Datei wählen
          </Button>
          {text.trim() && (
            <span className="text-xs text-zinc-500 dark:text-zinc-400">
              {pluralize(parsed.rows.length, "Postfach", "Postfächer")} erkannt
              {parsed.errors.length > 0 && `, ${pluralize(parsed.errors.length, "Fehler", "Fehler")}`}
            </span>
          )}
        </div>
        {parsed.errors.length > 0 && (
          <Notice tone="warning" icon={TriangleAlert} title="Diese Zeilen werden ignoriert">
            <ul className="max-h-32 space-y-0.5 overflow-y-auto text-xs">
              {parsed.errors.map((e) => (
                <li key={e}>{e}</li>
              ))}
            </ul>
          </Notice>
        )}
      </div>
    </Modal>
  );
}

// ---------------------------------------------------------------------------
// Step 4: options
// ---------------------------------------------------------------------------

function OptionsStep({
  options,
  setOptions,
  defaultName,
  sinceValid,
}: {
  options: Options;
  setOptions: (fn: (o: Options) => Options) => void;
  defaultName: string;
  sinceValid: boolean;
}) {
  const ai = useAiStatus();
  const set = (patch: Partial<Options>) => setOptions((o) => ({ ...o, ...patch }));
  return (
    <div className="space-y-5">
      <Field label="Name der Migration">
        {(id) => <Input id={id} value={options.name} onChange={(e) => set({ name: e.target.value })} placeholder={defaultName} className="max-w-md" />}
      </Field>
      <div className="space-y-4 border-t border-zinc-100 pt-5 dark:border-zinc-800">
        <SwitchRow label="Papierkorb mitnehmen" description="Gelöschte Nachrichten der Quelle ebenfalls übernehmen." checked={options.includeTrash} onChange={(v) => set({ includeTrash: v })} />
        <SwitchRow label="Spam mitnehmen" description="Nachrichten aus dem Spam-Ordner der Quelle übernehmen." checked={options.includeJunk} onChange={(v) => set({ includeJunk: v })} />
        <SwitchRow
          label="Nur Nachrichten ab einem Datum"
          description="Ältere Nachrichten bleiben auf der Quelle."
          checked={options.sinceEnabled}
          onChange={(v) => set({ sinceEnabled: v })}
        />
        {options.sinceEnabled && (
          <Field label="Ab Datum" error={!sinceValid ? "Bitte ein Datum wählen." : null} className="animate-fade-in">
            {(id) => <Input id={id} type="date" value={options.since} onChange={(e) => set({ since: e.target.value })} className="w-48" invalid={!sinceValid} />}
          </Field>
        )}
      </div>
      <div className="space-y-3 border-t border-zinc-100 pt-5 dark:border-zinc-800">
        <SwitchRow
          label={
            <span className="inline-flex items-center gap-1.5">
              <Sparkles className="h-4 w-4 text-indigo-500" /> Importierte Mails durch KI analysieren
            </span>
          }
          description={
            ai.enabled
              ? "Kategorien, Prioritäten, Zusammenfassungen und Suchindex für die übernommenen Nachrichten erzeugen."
              : "Die KI ist auf diesem Server deaktiviert (Einstellungen → KI)."
          }
          checked={options.analyzeWithAI}
          onChange={(v) => set({ analyzeWithAI: v })}
          disabled={!ai.enabled && !options.analyzeWithAI}
        />
        {options.analyzeWithAI && (
          <Notice tone="warning" icon={TriangleAlert}>
            Bei vielen Nachrichten kann die Analyse sehr lange dauern und das Sprachmodell stark auslasten. Der Import selbst wird
            dadurch nicht verzögert.
          </Notice>
        )}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Step 5: summary
// ---------------------------------------------------------------------------

function SummaryStep({
  source,
  rows,
  existing,
  excluded,
  options,
  name,
  test,
}: {
  source: MigrationSource;
  rows: Row[];
  existing: Set<string>;
  excluded: Set<string>;
  options: Options;
  name: string;
  test: MigrationTestResult | null;
}) {
  const newCount = rows.filter((r) => !existing.has(r.targetEmail.trim().toLowerCase())).length;
  const skipped = [...excluded];
  if (!options.includeTrash) skipped.push("Papierkorb");
  if (!options.includeJunk) skipped.push("Spam");
  const messages = test?.folders.reduce((n, f) => n + f.messages, 0) ?? 0;

  return (
    <div className="space-y-5">
      <dl className="grid gap-x-8 gap-y-3 sm:grid-cols-2">
        <Item label="Name">{name}</Item>
        <Item label="Quelle">
          <code className="font-mono text-[13px]">
            {source.host}:{source.port}
          </code>{" "}
          <Badge className="ml-1">{securityLabels[source.security]}</Badge>
        </Item>
        <Item label="Postfächer">
          {rows.length}
          {newCount > 0 && <span className="text-zinc-500 dark:text-zinc-400"> ({newCount} neu)</span>}
        </Item>
        <Item label="Übersprungen">{skipped.length > 0 ? skipped.join(", ") : "Nichts"}</Item>
        <Item label="Zeitraum">{options.sinceEnabled && options.since ? `ab ${options.since.split("-").reverse().join(".")}` : "Alle Nachrichten"}</Item>
        <Item label="KI-Analyse">{options.analyzeWithAI ? "Ja" : "Nein"}</Item>
      </dl>

      <div className="overflow-hidden rounded-lg border border-zinc-200 dark:border-zinc-800">
        <Table className="max-h-72 overflow-y-auto">
          <THead>
            <tr>
              <Th>Zielkonto</Th>
              <Th>Quell-Benutzer</Th>
              <Th>Konto</Th>
            </tr>
          </THead>
          <TBody>
            {rows.map((r) => {
              const email = r.targetEmail.trim().toLowerCase();
              return (
                <Tr key={r.key}>
                  <Td className="py-2 font-medium text-zinc-900 dark:text-zinc-100">{email}</Td>
                  <Td className="py-2 font-mono text-[13px]">{r.username}</Td>
                  <Td className="py-2">
                    {existing.has(email) ? (
                      <Badge tone="green" dot>
                        existiert
                      </Badge>
                    ) : (
                      <Badge tone="indigo" dot>
                        wird angelegt
                      </Badge>
                    )}
                  </Td>
                </Tr>
              );
            })}
          </TBody>
        </Table>
      </div>

      {messages > 0 && (
        <p className="text-xs text-zinc-500 dark:text-zinc-400">
          Das Testpostfach enthält {formatNumber(messages)} Nachrichten. Bereits vorhandene Nachrichten (gleiche Message-ID) werden übersprungen.
        </p>
      )}

      <Notice tone="info" icon={Lock}>
        Die Passwörter der Quelle werden nur im Arbeitsspeicher gehalten. Wird der Server während der Migration neu gestartet, kannst du
        den Job mit erneut eingegebenen Passwörtern fortsetzen.
      </Notice>
    </div>
  );
}

function Item({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="text-xs font-medium uppercase tracking-wide text-zinc-500 dark:text-zinc-400">{label}</dt>
      <dd className="mt-0.5 break-words text-sm text-zinc-900 dark:text-zinc-100">{children}</dd>
    </div>
  );
}
