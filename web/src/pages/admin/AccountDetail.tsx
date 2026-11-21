import { useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, AtSign, KeyRound, ShieldCheck, ShieldOff, Trash2, UserX, Wand2 } from "lucide-react";
import { adminApi } from "@/api/endpoints";
import type { Account, AccountSource, Role, UpdateAccountRequest } from "@/api/types";
import { SourceBadge } from "./sso/SourceBadge";
import { formatBytes, formatDate } from "@/lib/format";
import {
  Avatar,
  Badge,
  Button,
  Card,
  CardBody,
  CardHeader,
  CopyButton,
  EmptyState,
  ErrorState,
  Field,
  FormSection,
  Input,
  PasswordInput,
  Progress,
  SaveBar,
  Select,
  Skeleton,
  SwitchRow,
  useConfirm,
  useToast,
} from "@/components/ui";
import { generatePassword, QuotaInput } from "./shared";

interface FormState {
  name: string;
  role: Role;
  quotaBytes: number;
  disabled: boolean;
  password: string;
  source: AccountSource;
}

function toForm(a: Account): FormState {
  return { name: a.name, role: a.role, quotaBytes: a.quotaBytes, disabled: a.disabled, password: "", source: a.source ?? "local" };
}

export default function AccountDetailPage() {
  const { id: idParam } = useParams();
  const id = Number(idParam);
  const navigate = useNavigate();
  const qc = useQueryClient();
  const toast = useToast();
  const confirm = useConfirm();

  const q = useQuery({ queryKey: ["admin", "account", id], queryFn: () => adminApi.account(id), enabled: Number.isFinite(id) });
  const [form, setForm] = useState<FormState | null>(null);

  useEffect(() => {
    if (q.data) setForm(toForm(q.data));
  }, [q.data]);

  const onSaved = (a: Account) => {
    qc.setQueryData(["admin", "account", id], a);
    void qc.invalidateQueries({ queryKey: ["admin", "accounts"] });
  };

  const save = useMutation({
    mutationFn: (body: UpdateAccountRequest) => adminApi.updateAccount(id, body),
    onSuccess: (a) => {
      onSaved(a);
      toast.success("Änderungen gespeichert");
    },
    onError: (err) => toast.error(err, "Speichern fehlgeschlagen"),
  });

  const resetTotp = useMutation({
    mutationFn: () => adminApi.updateAccount(id, { resetTotp: true }),
    onSuccess: (a) => {
      onSaved(a);
      toast.success("Zwei-Faktor-Anmeldung zurückgesetzt");
    },
    onError: (err) => toast.error(err, "Zurücksetzen fehlgeschlagen"),
  });

  const remove = useMutation({
    mutationFn: () => adminApi.deleteAccount(id),
    onSuccess: () => {
      toast.success("Konto gelöscht");
      void qc.invalidateQueries({ queryKey: ["admin", "accounts"] });
      navigate("/admin/accounts", { replace: true });
    },
    onError: (err) => toast.error(err, "Löschen fehlgeschlagen"),
  });

  if (q.isLoading || (!form && q.data)) {
    return (
      <div className="space-y-6">
        <Skeleton className="h-28 rounded-xl" />
        <Skeleton className="h-96 rounded-xl" />
      </div>
    );
  }
  if (q.error || !q.data || !form) {
    return (
      <Card>
        <ErrorState error={q.error ?? new Error("Konto nicht gefunden.")} onRetry={() => void q.refetch()} />
      </Card>
    );
  }

  const a = q.data;
  const original = toForm(a);
  const dirty = JSON.stringify(original) !== JSON.stringify(form);
  const toLocal = form.source === "local" && original.source !== "local";
  const pwError =
    form.password && form.password.length < 10 ? "Mindestens 10 Zeichen." : toLocal && !form.password ? "Für ein lokales Konto bitte ein Passwort festlegen." : null;

  const submit = () => {
    if (pwError) return;
    const body: UpdateAccountRequest = {};
    if (form.name !== original.name) body.name = form.name.trim();
    if (form.role !== original.role) body.role = form.role;
    if (form.quotaBytes !== original.quotaBytes) body.quotaBytes = form.quotaBytes;
    if (form.disabled !== original.disabled) body.disabled = form.disabled;
    if (form.password && form.source === "local") body.password = form.password;
    if (form.source !== original.source) body.source = form.source;
    save.mutate(body);
  };

  return (
    <>
      <Link to="/admin/accounts" className="mb-4 inline-flex items-center gap-1.5 text-sm text-zinc-500 hover:text-zinc-800 dark:text-zinc-400 dark:hover:text-zinc-200">
        <ArrowLeft className="h-4 w-4" /> Alle Konten
      </Link>

      <Card className="mb-6">
        <div className="flex flex-wrap items-center gap-5 p-5">
          <Avatar name={a.name} email={a.email} size="lg" className="h-14 w-14 text-lg" />
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-center gap-2">
              <h1 className="truncate text-xl font-semibold text-zinc-900 dark:text-zinc-50">{a.name || a.email}</h1>
              <Badge tone={a.role === "admin" ? "indigo" : "neutral"}>{a.role === "admin" ? "Administrator" : "Benutzer"}</Badge>
              {a.disabled ? <Badge tone="red" dot>Deaktiviert</Badge> : <Badge tone="green" dot>Aktiv</Badge>}
              {a.totpEnabled && <Badge tone="indigo" icon={ShieldCheck}>2FA aktiv</Badge>}
              <SourceBadge source={a.source} />
            </div>
            <div className="mt-1 flex items-center gap-1 text-sm text-zinc-500 dark:text-zinc-400">
              {a.email} <CopyButton value={a.email} />
            </div>
            <div className="mt-0.5 text-xs text-zinc-400">Angelegt am {formatDate(a.createdAt)} · ID {a.id}</div>
          </div>
          <div className="w-full sm:w-64">
            <div className="mb-1.5 flex justify-between text-xs text-zinc-500 dark:text-zinc-400">
              <span>Speicher</span>
              <span className="tabular-nums">
                {formatBytes(a.usedBytes)} / {a.quotaBytes > 0 ? formatBytes(a.quotaBytes) : "∞"}
              </span>
            </div>
            <Progress value={a.quotaBytes > 0 ? a.usedBytes / a.quotaBytes : 0} className="h-2" />
            {a.quotaBytes > 0 && (
              <div className="mt-1 text-right text-xs text-zinc-400">{Math.round((a.usedBytes / a.quotaBytes) * 100)} % belegt</div>
            )}
          </div>
        </div>
      </Card>

      <div className="grid gap-6 xl:grid-cols-3">
        <div className="xl:col-span-2">
          <Card>
            <CardHeader title="Kontodaten" description="Änderungen werden erst mit „Speichern“ übernommen." />
            <CardBody className="divide-y divide-zinc-100 dark:divide-zinc-800">
              <FormSection title="Profil" description="Anzeigename und Berechtigungen.">
                <Field label="Name">
                  {(id) => <Input id={id} value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />}
                </Field>
                <Field label="E-Mail-Adresse" help="Die Adresse kann nicht geändert werden. Lege bei Bedarf einen Alias an.">
                  {(id) => <Input id={id} value={a.email} disabled />}
                </Field>
                <Field label="Rolle" help="Administratoren haben Zugriff auf die gesamte Verwaltung.">
                  {(id) => (
                    <Select
                      id={id}
                      value={form.role}
                      onChange={(e) => setForm({ ...form, role: e.target.value as Role })}
                      options={[
                        { value: "user", label: "Benutzer" },
                        { value: "admin", label: "Administrator" },
                      ]}
                      className="max-w-xs"
                    />
                  )}
                </Field>
              </FormSection>
              <FormSection title="Speicher" description="Maximaler Speicherplatz für alle Nachrichten.">
                <Field label="Kontingent" help="0 = unbegrenzt">
                  {(id) => <QuotaInput id={id} value={form.quotaBytes} onChange={(v) => setForm({ ...form, quotaBytes: v })} />}
                </Field>
              </FormSection>
              <FormSection title="Zugang" description="Passwort neu setzen oder das Konto sperren.">
                <Field
                  label="Anmeldung"
                  help={
                    form.source === "ldap"
                      ? "Das Passwort wird im LDAP-Verzeichnis geprüft und dort geändert."
                      : form.source === "oidc"
                        ? "Kein Postwerk-Passwort: Weboberfläche per Single Sign-on, Mail-Programme mit App-Passwörtern."
                        : undefined
                  }
                >
                  {(id) => (
                    <Select
                      id={id}
                      value={form.source}
                      onChange={(e) => setForm({ ...form, source: e.target.value as AccountSource, password: "" })}
                      options={[
                        { value: "local", label: "Postwerk-Passwort" },
                        { value: "ldap", label: "LDAP-Verzeichnis" },
                        { value: "oidc", label: "Nur Single Sign-on" },
                      ]}
                      className="max-w-xs"
                    />
                  )}
                </Field>
                {form.source !== original.source && original.source === "local" && (
                  <p className="text-xs text-amber-700 dark:text-amber-300">
                    Beim Speichern wird das bisherige Passwort entfernt; Sitzungen und App-Passwörter werden beendet.
                  </p>
                )}
                {form.source === "local" && (
                <Field label="Neues Passwort" error={pwError} help={toLocal ? "Pflicht beim Wechsel auf ein Postwerk-Passwort." : "Leer lassen, um das Passwort nicht zu ändern."}>
                  {(id) => (
                    <div className="flex gap-2">
                      <div className="flex-1">
                        <PasswordInput
                          id={id}
                          value={form.password}
                          invalid={!!pwError}
                          autoComplete="new-password"
                          onChange={(e) => setForm({ ...form, password: e.target.value })}
                        />
                      </div>
                      <Button icon={Wand2} onClick={() => setForm({ ...form, password: generatePassword() })}>
                        Generieren
                      </Button>
                      {form.password && <CopyButton value={form.password} className="px-2.5" />}
                    </div>
                  )}
                </Field>
                )}
                <SwitchRow
                  label="Konto deaktivieren"
                  description="Deaktivierte Konten können sich nicht anmelden; eingehende Nachrichten werden abgelehnt."
                  checked={form.disabled}
                  onChange={(v) => setForm({ ...form, disabled: v })}
                />
              </FormSection>
            </CardBody>
          </Card>
          <SaveBar dirty={dirty} saving={save.isPending} onSave={submit} onReset={() => setForm(original)} />
        </div>

        <div className="space-y-6">
          <Card>
            <CardHeader title="Aliase" description="Adressen, die an dieses Konto zustellen." icon={<AtSign className="h-4 w-4 text-zinc-400" />} />
            {a.aliases.length === 0 ? (
              <EmptyState compact title="Keine Aliase" description="Aliase werden unter Verzeichnis › Aliase verwaltet." />
            ) : (
              <ul className="divide-y divide-zinc-100 dark:divide-zinc-800">
                {a.aliases.map((al) => (
                  <li key={al} className="flex items-center justify-between gap-2 px-5 py-2.5 text-sm">
                    <span className="truncate font-mono text-[13px] text-zinc-700 dark:text-zinc-300">{al}</span>
                    <CopyButton value={al} />
                  </li>
                ))}
              </ul>
            )}
            <div className="border-t border-zinc-100 px-5 py-3 dark:border-zinc-800">
              <Link to="/admin/aliases" className="text-sm font-medium text-indigo-600 hover:text-indigo-500 dark:text-indigo-400">
                Aliase verwalten →
              </Link>
            </div>
          </Card>

          <Card>
            <CardHeader title="Sicherheit" icon={<KeyRound className="h-4 w-4 text-zinc-400" />} />
            <CardBody className="space-y-4">
              <div className="flex items-start justify-between gap-4">
                <div>
                  <div className="text-sm font-medium text-zinc-900 dark:text-zinc-100">Zwei-Faktor-Anmeldung</div>
                  <div className="text-xs text-zinc-500 dark:text-zinc-400">
                    {a.totpEnabled ? "Aktiv. Zurücksetzen, wenn das Gerät verloren ging." : "Nicht eingerichtet."}
                  </div>
                </div>
                <Button
                  size="sm"
                  icon={ShieldOff}
                  disabled={!a.totpEnabled}
                  loading={resetTotp.isPending}
                  onClick={async () => {
                    const ok = await confirm({
                      title: "2FA zurücksetzen?",
                      message: `Die Zwei-Faktor-Anmeldung für ${a.email} wird entfernt. Der Benutzer kann sie danach neu einrichten.`,
                      confirmLabel: "Zurücksetzen",
                      danger: true,
                    });
                    if (ok) resetTotp.mutate();
                  }}
                >
                  Zurücksetzen
                </Button>
              </div>
              <div className="flex items-start justify-between gap-4 border-t border-zinc-100 pt-4 dark:border-zinc-800">
                <div>
                  <div className="text-sm font-medium text-zinc-900 dark:text-zinc-100">Sofort sperren</div>
                  <div className="text-xs text-zinc-500 dark:text-zinc-400">Deaktiviert das Konto ohne weitere Änderungen.</div>
                </div>
                <Button
                  size="sm"
                  icon={UserX}
                  disabled={a.disabled || save.isPending}
                  onClick={() => save.mutate({ disabled: true })}
                >
                  Sperren
                </Button>
              </div>
            </CardBody>
          </Card>

          <Card className="border-red-200 dark:border-red-500/30">
            <CardHeader title="Gefahrenzone" />
            <CardBody className="flex items-start justify-between gap-4">
              <p className="text-sm text-zinc-500 dark:text-zinc-400">Löscht das Konto inklusive aller Nachrichten dauerhaft.</p>
              <Button
                size="sm"
                variant="danger"
                icon={Trash2}
                loading={remove.isPending}
                onClick={async () => {
                  const ok = await confirm({
                    title: "Konto endgültig löschen?",
                    message: `${a.email} und alle zugehörigen Daten werden unwiderruflich entfernt.`,
                    confirmLabel: "Endgültig löschen",
                    danger: true,
                  });
                  if (ok) remove.mutate();
                }}
              >
                Löschen
              </Button>
            </CardBody>
          </Card>
        </div>
      </div>
    </>
  );
}
