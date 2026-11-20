import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import QRCode from "qrcode";
import { KeyRound, Lock, Palmtree, Plus, ShieldCheck, ShieldOff, Smartphone, Trash2 } from "lucide-react";
import { accountApi } from "@/api/endpoints";
import type { CreatedAppPassword, TotpSetup, Vacation } from "@/api/types";
import { useMe } from "@/lib/auth";
import { qk } from "@/lib/queries";
import { formatBytes, formatDate, formatRelative, fromDateTimeInput, toDateInput } from "@/lib/format";
import { useDraft } from "@/lib/hooks";
import {
  Avatar,
  Badge,
  Button,
  Card,
  CardBody,
  CardFooter,
  CardHeader,
  CodeInput,
  CopyButton,
  EmptyState,
  Field,
  Input,
  Modal,
  PageHeader,
  PasswordInput,
  Progress,
  Skeleton,
  SwitchRow,
  Textarea,
  useConfirm,
  useToast,
} from "@/components/ui";

export default function MailSettingsPage() {
  const me = useMe();
  return (
    <div className="mx-auto max-w-4xl">
      <PageHeader title="Konto & Sicherheit" description="Persönliche Einstellungen für dein Postfach." />
      <div className="space-y-6">
        <Card>
          <div className="flex flex-wrap items-center gap-4 p-5">
            <Avatar name={me.name} email={me.email} size="lg" className="h-14 w-14 text-lg" />
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2">
                <h2 className="truncate text-lg font-semibold text-zinc-900 dark:text-zinc-50">{me.name || me.email}</h2>
                <Badge tone={me.role === "admin" ? "indigo" : "neutral"}>{me.role === "admin" ? "Administrator" : "Benutzer"}</Badge>
              </div>
              <div className="text-sm text-zinc-500 dark:text-zinc-400">{me.email}</div>
            </div>
            <div className="w-full sm:w-56">
              <div className="mb-1.5 flex justify-between text-xs text-zinc-500 dark:text-zinc-400">
                <span>Speicher</span>
                <span className="tabular-nums">
                  {formatBytes(me.usedBytes)} / {me.quotaBytes > 0 ? formatBytes(me.quotaBytes) : "∞"}
                </span>
              </div>
              <Progress value={me.quotaBytes > 0 ? me.usedBytes / me.quotaBytes : 0} className="h-2" />
            </div>
          </div>
        </Card>
        <PasswordCard />
        <TotpCard />
        <AppPasswordsCard />
        <VacationCard />
      </div>
    </div>
  );
}

function PasswordCard() {
  const toast = useToast();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [repeat, setRepeat] = useState("");
  const tooShort = next.length > 0 && next.length < 8;
  const mismatch = repeat.length > 0 && next !== repeat;

  const save = useMutation({
    mutationFn: () => accountApi.changePassword(current, next),
    onSuccess: () => {
      toast.success("Passwort geändert");
      setCurrent("");
      setNext("");
      setRepeat("");
    },
    onError: (err) => toast.error(err, "Passwort konnte nicht geändert werden"),
  });

  const valid = current && next.length >= 8 && next === repeat;

  return (
    <Card>
      <CardHeader title="Passwort" description="Gilt für die Weboberfläche. Für Mail-Programme besser App-Passwörter nutzen." icon={<Lock className="h-4 w-4 text-zinc-400" />} />
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (valid) save.mutate();
        }}
      >
        <CardBody className="grid gap-4 sm:grid-cols-3">
          <Field label="Aktuelles Passwort">
            {(id) => <PasswordInput id={id} value={current} onChange={(e) => setCurrent(e.target.value)} autoComplete="current-password" />}
          </Field>
          <Field label="Neues Passwort" error={tooShort ? "Mindestens 8 Zeichen." : null}>
            {(id) => <PasswordInput id={id} value={next} invalid={tooShort} onChange={(e) => setNext(e.target.value)} autoComplete="new-password" />}
          </Field>
          <Field label="Wiederholen" error={mismatch ? "Passwörter stimmen nicht überein." : null}>
            {(id) => <PasswordInput id={id} value={repeat} invalid={mismatch} onChange={(e) => setRepeat(e.target.value)} autoComplete="new-password" />}
          </Field>
        </CardBody>
        <CardFooter>
          <Button type="submit" variant="primary" size="sm" disabled={!valid} loading={save.isPending}>
            Passwort ändern
          </Button>
        </CardFooter>
      </form>
    </Card>
  );
}

function TotpCard() {
  const me = useMe();
  const qc = useQueryClient();
  const toast = useToast();
  const [setup, setSetup] = useState<TotpSetup | null>(null);
  const [qr, setQr] = useState<string | null>(null);
  const [code, setCode] = useState("");
  const [disableOpen, setDisableOpen] = useState(false);
  const [password, setPassword] = useState("");

  useEffect(() => {
    if (!setup) {
      setQr(null);
      return;
    }
    QRCode.toDataURL(setup.otpauthUrl, { width: 220, margin: 1, errorCorrectionLevel: "M" })
      .then(setQr)
      .catch(() => setQr(null));
  }, [setup]);

  const start = useMutation({
    mutationFn: accountApi.totpSetup,
    onSuccess: (s) => {
      setSetup(s);
      setCode("");
    },
    onError: (err) => toast.error(err, "Einrichtung konnte nicht gestartet werden"),
  });

  const enable = useMutation({
    mutationFn: (c: string) => accountApi.totpEnable(c),
    onSuccess: () => {
      toast.success("Zwei-Faktor-Anmeldung aktiviert");
      setSetup(null);
      void qc.invalidateQueries({ queryKey: qk.me });
    },
    onError: (err) => {
      setCode("");
      toast.error(err, "Code ungültig");
    },
  });

  const disable = useMutation({
    mutationFn: () => accountApi.totpDisable(password),
    onSuccess: () => {
      toast.success("Zwei-Faktor-Anmeldung deaktiviert");
      setDisableOpen(false);
      setPassword("");
      void qc.invalidateQueries({ queryKey: qk.me });
    },
    onError: (err) => toast.error(err, "Deaktivieren fehlgeschlagen"),
  });

  const secretFromUrl = setup?.secret ?? "";

  return (
    <Card>
      <CardHeader
        title="Zwei-Faktor-Anmeldung"
        description="Schützt dein Konto zusätzlich mit Einmalcodes aus einer Authenticator-App."
        icon={<ShieldCheck className="h-4 w-4 text-zinc-400" />}
        actions={me.totpEnabled ? <Badge tone="green" dot>Aktiv</Badge> : <Badge>Nicht eingerichtet</Badge>}
      />
      <CardBody>
        {me.totpEnabled ? (
          <div className="flex flex-wrap items-center justify-between gap-4">
            <p className="text-sm text-zinc-600 dark:text-zinc-400">Bei der Anmeldung wird zusätzlich ein 6-stelliger Code abgefragt.</p>
            <Button variant="danger-ghost" icon={ShieldOff} onClick={() => setDisableOpen(true)}>
              Deaktivieren
            </Button>
          </div>
        ) : setup ? (
          <div className="grid gap-6 md:grid-cols-[auto_1fr]">
            <div className="flex flex-col items-center gap-2">
              <div className="rounded-xl border border-zinc-200 bg-white p-2 dark:border-zinc-700">
                {qr ? <img src={qr} alt="QR-Code für die Authenticator-App" className="h-[200px] w-[200px]" /> : <Skeleton className="h-[200px] w-[200px]" />}
              </div>
            </div>
            <div className="space-y-4">
              <ol className="list-decimal space-y-1.5 pl-5 text-sm text-zinc-600 dark:text-zinc-400">
                <li>Scanne den QR-Code mit einer Authenticator-App (z. B. Aegis, 2FAS, Google Authenticator).</li>
                <li>Gib den angezeigten 6-stelligen Code ein, um die Einrichtung abzuschließen.</li>
              </ol>
              <div>
                <div className="mb-1 text-xs text-zinc-500 dark:text-zinc-400">Oder Schlüssel manuell eingeben:</div>
                <div className="flex items-center gap-1 rounded-lg bg-zinc-50 px-3 py-2 ring-1 ring-inset ring-zinc-200 dark:bg-zinc-950 dark:ring-zinc-800">
                  <code className="flex-1 break-all font-mono text-sm tracking-wider text-zinc-800 dark:text-zinc-200">{secretFromUrl}</code>
                  <CopyButton value={secretFromUrl} />
                </div>
              </div>
              <div className="flex flex-wrap items-center gap-3">
                <CodeInput value={code} onChange={setCode} onComplete={(c) => enable.mutate(c)} disabled={enable.isPending} autoFocus />
              </div>
              <div className="flex gap-2">
                <Button variant="primary" disabled={code.length !== 6} loading={enable.isPending} onClick={() => enable.mutate(code)}>
                  Aktivieren
                </Button>
                <Button variant="ghost" onClick={() => setSetup(null)}>
                  Abbrechen
                </Button>
              </div>
            </div>
          </div>
        ) : (
          <div className="flex flex-wrap items-center justify-between gap-4">
            <div className="flex items-center gap-3 text-sm text-zinc-600 dark:text-zinc-400">
              <Smartphone className="h-8 w-8 shrink-0 text-zinc-300 dark:text-zinc-600" />
              Empfohlen: Ohne zweiten Faktor reicht ein gestohlenes Passwort für den Zugriff auf dein Postfach.
            </div>
            <Button variant="primary" icon={ShieldCheck} loading={start.isPending} onClick={() => start.mutate()}>
              Einrichten
            </Button>
          </div>
        )}
      </CardBody>

      <Modal
        open={disableOpen}
        onClose={() => setDisableOpen(false)}
        size="sm"
        title="2FA deaktivieren"
        description="Zur Bestätigung bitte dein Passwort eingeben."
        footer={
          <>
            <Button onClick={() => setDisableOpen(false)}>Abbrechen</Button>
            <Button variant="danger" disabled={!password} loading={disable.isPending} onClick={() => disable.mutate()}>
              Deaktivieren
            </Button>
          </>
        }
      >
        <form
          onSubmit={(e) => {
            e.preventDefault();
            if (password) disable.mutate();
          }}
        >
          <Field label="Passwort">{(id) => <PasswordInput id={id} value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" />}</Field>
        </form>
      </Modal>
    </Card>
  );
}

function AppPasswordsCard() {
  const qc = useQueryClient();
  const toast = useToast();
  const confirm = useConfirm();
  const [createOpen, setCreateOpen] = useState(false);
  const [name, setName] = useState("");
  const [created, setCreated] = useState<CreatedAppPassword | null>(null);
  const key = ["account", "app-passwords"];
  const list = useQuery({ queryKey: key, queryFn: accountApi.appPasswords });

  const create = useMutation({
    mutationFn: () => accountApi.createAppPassword(name.trim()),
    onSuccess: (p) => {
      setCreateOpen(false);
      setName("");
      setCreated(p);
      void qc.invalidateQueries({ queryKey: key });
    },
    onError: (err) => toast.error(err, "App-Passwort konnte nicht erstellt werden"),
  });

  const revoke = useMutation({
    mutationFn: (id: number) => accountApi.deleteAppPassword(id),
    onSuccess: () => {
      toast.success("App-Passwort widerrufen");
      void qc.invalidateQueries({ queryKey: key });
    },
    onError: (err) => toast.error(err, "Widerrufen fehlgeschlagen"),
  });

  return (
    <Card>
      <CardHeader
        title="App-Passwörter"
        description="Eigene Passwörter für IMAP, SMTP, CalDAV oder Smartphones – einzeln widerrufbar."
        icon={<KeyRound className="h-4 w-4 text-zinc-400" />}
        actions={
          <Button size="sm" icon={Plus} onClick={() => setCreateOpen(true)}>
            Neues App-Passwort
          </Button>
        }
      />
      {list.isLoading ? (
        <div className="space-y-3 p-5">
          <Skeleton className="h-5 w-1/2" />
          <Skeleton className="h-5 w-1/3" />
        </div>
      ) : !list.data || list.data.length === 0 ? (
        <EmptyState compact icon={KeyRound} title="Keine App-Passwörter" description="Lege für jedes Gerät ein eigenes Passwort an." />
      ) : (
        <ul className="divide-y divide-zinc-100 dark:divide-zinc-800">
          {list.data.map((p) => (
            <li key={p.id} className="flex items-center gap-4 px-5 py-3">
              <span className="flex h-9 w-9 items-center justify-center rounded-lg bg-zinc-100 text-zinc-500 dark:bg-zinc-800 dark:text-zinc-400">
                <Smartphone className="h-4 w-4" />
              </span>
              <div className="min-w-0 flex-1">
                <div className="truncate text-sm font-medium text-zinc-900 dark:text-zinc-100">{p.name}</div>
                <div className="text-xs text-zinc-500 dark:text-zinc-400">
                  Erstellt am {formatDate(p.createdAt)} · {p.lastUsedAt ? `zuletzt genutzt ${formatRelative(p.lastUsedAt)}` : "noch nie genutzt"}
                </div>
              </div>
              <Button
                size="sm"
                variant="danger-ghost"
                icon={Trash2}
                onClick={async () => {
                  const ok = await confirm({
                    title: "App-Passwort widerrufen?",
                    message: `Geräte, die „${p.name}“ verwenden, können sich danach nicht mehr anmelden.`,
                    confirmLabel: "Widerrufen",
                    danger: true,
                  });
                  if (ok) revoke.mutate(p.id);
                }}
              >
                Widerrufen
              </Button>
            </li>
          ))}
        </ul>
      )}

      <Modal
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        size="sm"
        title="Neues App-Passwort"
        footer={
          <>
            <Button onClick={() => setCreateOpen(false)}>Abbrechen</Button>
            <Button variant="primary" disabled={!name.trim()} loading={create.isPending} onClick={() => create.mutate()}>
              Erstellen
            </Button>
          </>
        }
      >
        <form
          onSubmit={(e) => {
            e.preventDefault();
            if (name.trim()) create.mutate();
          }}
        >
          <Field label="Bezeichnung" help="Damit du später erkennst, wofür das Passwort gedacht ist.">
            {(id) => <Input id={id} value={name} onChange={(e) => setName(e.target.value)} placeholder="z. B. Thunderbird Laptop" />}
          </Field>
        </form>
      </Modal>

      <Modal
        open={created !== null}
        onClose={() => setCreated(null)}
        size="md"
        title="App-Passwort erstellt"
        description="Kopiere das Passwort jetzt – es wird nur dieses eine Mal angezeigt."
        footer={
          <Button variant="primary" onClick={() => setCreated(null)}>
            Fertig
          </Button>
        }
      >
        {created && (
          <div className="space-y-4">
            <div className="text-sm text-zinc-600 dark:text-zinc-400">
              Für <span className="font-medium text-zinc-900 dark:text-zinc-100">{created.name}</span>
            </div>
            <div className="flex items-center gap-2 rounded-xl bg-zinc-950 px-4 py-3 dark:bg-black">
              <code className="flex-1 select-all break-all font-mono text-lg tracking-wider text-emerald-300">{created.password}</code>
              <CopyButton value={created.password} label="Kopieren" className="text-zinc-300 hover:bg-zinc-800 hover:text-white" />
            </div>
            <p className="text-xs text-zinc-500 dark:text-zinc-400">
              Benutzername ist deine E-Mail-Adresse. Verwende das Passwort anstelle deines Kontopassworts in IMAP-/SMTP-Programmen.
            </p>
          </div>
        )}
      </Modal>
    </Card>
  );
}

function VacationCard() {
  const qc = useQueryClient();
  const toast = useToast();
  const key = ["account", "vacation"];
  const q = useQuery({ queryKey: key, queryFn: accountApi.vacation });
  const { draft, setDraft, dirty, reset } = useDraft<Vacation>(q.data);

  const save = useMutation({
    mutationFn: (v: Vacation) => accountApi.saveVacation(v),
    onSuccess: (v) => {
      qc.setQueryData(key, v);
      toast.success(v.enabled ? "Abwesenheitsnotiz aktiviert" : "Abwesenheitsnotiz gespeichert");
    },
    onError: (err) => toast.error(err, "Speichern fehlgeschlagen"),
  });

  return (
    <Card>
      <CardHeader
        title="Abwesenheitsnotiz"
        description="Automatische Antwort, solange du nicht erreichbar bist."
        icon={<Palmtree className="h-4 w-4 text-zinc-400" />}
        actions={q.data?.enabled ? <Badge tone="amber" dot>Aktiv</Badge> : undefined}
      />
      {q.isLoading || !draft ? (
        <div className="space-y-3 p-5">
          <Skeleton className="h-6 w-1/3" />
          <Skeleton className="h-24" />
        </div>
      ) : (
        <>
          <CardBody className="space-y-4">
            <SwitchRow
              label="Abwesenheitsnotiz aktivieren"
              description="Jeder Absender erhält höchstens einmal pro Tag eine automatische Antwort."
              checked={draft.enabled}
              onChange={(v) => setDraft((d) => ({ ...d, enabled: v }))}
            />
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="Von" help="Optional – leer bedeutet ab sofort.">
                {(id) => (
                  <Input
                    id={id}
                    type="date"
                    value={toDateInput(draft.from)}
                    onChange={(e) => setDraft((d) => ({ ...d, from: e.target.value ? fromDateTimeInput(e.target.value, "00:00") : null }))}
                  />
                )}
              </Field>
              <Field label="Bis" help="Optional – leer bedeutet bis zum Deaktivieren.">
                {(id) => (
                  <Input
                    id={id}
                    type="date"
                    value={toDateInput(draft.until)}
                    min={toDateInput(draft.from)}
                    onChange={(e) => setDraft((d) => ({ ...d, until: e.target.value ? fromDateTimeInput(e.target.value, "23:59") : null }))}
                  />
                )}
              </Field>
            </div>
            <Field label="Betreff">
              {(id) => <Input id={id} value={draft.subject} onChange={(e) => setDraft((d) => ({ ...d, subject: e.target.value }))} placeholder="Abwesend bis …" />}
            </Field>
            <Field label="Nachricht">
              {(id) => (
                <Textarea
                  id={id}
                  rows={6}
                  value={draft.body}
                  onChange={(e) => setDraft((d) => ({ ...d, body: e.target.value }))}
                  placeholder="Vielen Dank für deine Nachricht. Ich bin bis … nicht erreichbar …"
                />
              )}
            </Field>
          </CardBody>
          <CardFooter>
            <Button size="sm" variant="ghost" disabled={!dirty} onClick={reset}>
              Verwerfen
            </Button>
            <Button size="sm" variant="primary" disabled={!dirty} loading={save.isPending} onClick={() => save.mutate(draft)}>
              Speichern
            </Button>
          </CardFooter>
        </>
      )}
    </Card>
  );
}
