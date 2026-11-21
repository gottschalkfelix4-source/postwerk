import { useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { FileKey, KeyRound, Lock, LockOpen, RefreshCw, ShieldCheck, TriangleAlert, Upload } from "lucide-react";
import { atRestApi, atRestKeys, type ConversionJob, type EncryptionKind, type KeyInfo, type MailEncryptionConfig } from "@/api/atrest";
import { errorMessage } from "@/api/client";
import { formatDate, formatNumber } from "@/lib/format";
import { qk } from "@/lib/queries";
import {
  Badge,
  Button,
  Card,
  CardBody,
  CardHeader,
  Checkbox,
  Field,
  Notice,
  Progress,
  Segmented,
  Skeleton,
  SwitchRow,
  Textarea,
  useConfirm,
  useToast,
} from "@/components/ui";

const kindLabel: Record<EncryptionKind, string> = { pgp: "OpenPGP", smime: "S/MIME" };

/** Consequences of encrypted storage, shown before enabling it. */
function Consequences() {
  return (
    <Notice tone="warning" icon={TriangleAlert} title="Was das bedeutet">
      <ul className="list-disc space-y-0.5 pl-4">
        <li>Nur Programme mit deinem privaten Schlüssel können verschlüsselte Nachrichten lesen – z. B. Thunderbird (OpenPGP), Apple Mail/iOS oder Outlook (S/MIME).</li>
        <li>Die Weboberfläche zeigt nur noch Absender, Empfänger, Datum und – falls nicht verborgen – den Betreff.</li>
        <li>Volltextsuche, Vorschau, KI-Funktionen (Zusammenfassung, Einsortierung, Termine) und die Paperless-Übergabe funktionieren für verschlüsselte Nachrichten nicht.</li>
        <li>Geht der private Schlüssel verloren, sind die Nachrichten unwiederbringlich verloren – auch für den Administrator.</li>
        <li>Entwürfe werden nicht verschlüsselt. Abschalten wirkt nur für neue Nachrichten; bereits verschlüsselte bleiben verschlüsselt.</li>
      </ul>
    </Notice>
  );
}

function KeyDetails({ info, matches }: { info: Pick<KeyInfo, "kind" | "fingerprint" | "description" | "algorithm" | "expires"> & { emails?: string[] }; matches?: boolean }) {
  const expired = info.expires ? new Date(info.expires).getTime() < Date.now() : false;
  return (
    <div className="space-y-2 rounded-lg border border-zinc-200 p-3 text-sm dark:border-zinc-800">
      <div className="flex flex-wrap items-center gap-2">
        <FileKey className="h-4 w-4 text-zinc-400" />
        <span className="font-medium text-zinc-900 dark:text-zinc-100">{info.description || "Ohne Namen"}</span>
        <Badge tone="indigo">{kindLabel[info.kind]}</Badge>
        {info.algorithm && <Badge tone="neutral">{info.algorithm}</Badge>}
        {expired && <Badge tone="amber">abgelaufen</Badge>}
      </div>
      <div className="break-all font-mono text-xs text-zinc-500 dark:text-zinc-400">{info.fingerprint.replace(/(.{4})/g, "$1 ").trim()}</div>
      <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-zinc-500 dark:text-zinc-400">
        {info.emails && info.emails.length > 0 && <span>Adressen: {info.emails.join(", ")}</span>}
        <span>Gültig bis: {info.expires ? formatDate(info.expires) : "unbegrenzt"}</span>
      </div>
      {matches === false && (
        <p className="text-xs text-amber-700 dark:text-amber-300">Der Schlüssel nennt weder deine Adresse noch einen deiner Aliasse – bitte prüfen, ob es der richtige ist.</p>
      )}
      {expired && (
        <p className="text-xs text-amber-700 dark:text-amber-300">
          Der Schlüssel ist abgelaufen. Nachrichten werden trotzdem weiter damit verschlüsselt – lade bald einen verlängerten Schlüssel hoch.
        </p>
      )}
    </div>
  );
}

function JobProgress({ job }: { job: ConversionJob }) {
  const fraction = job.total > 0 ? job.done / job.total : job.running ? 0 : 1;
  return (
    <div className="space-y-1.5">
      <div className="flex justify-between text-xs text-zinc-500 dark:text-zinc-400">
        <span>
          {job.running ? "Bestehende Nachrichten werden verschlüsselt …" : job.error ? `Abgebrochen: ${job.error}` : "Bestehende Nachrichten verschlüsselt"}
        </span>
        <span className="tabular-nums">
          {formatNumber(job.done)} / {formatNumber(job.total)}
        </span>
      </div>
      <Progress value={fraction} tone="indigo" className="h-2" />
      {!job.running && (
        <div className="flex flex-wrap gap-2 pt-1">
          <Badge tone="green">{formatNumber(job.converted)} verschlüsselt</Badge>
          <Badge tone="neutral">{formatNumber(job.skipped)} waren schon verschlüsselt</Badge>
          {job.failed > 0 && <Badge tone="red">{formatNumber(job.failed)} fehlgeschlagen</Badge>}
        </div>
      )}
    </div>
  );
}

/** Reads a key file; DER certificates are converted to PEM. */
async function readKeyFile(file: File): Promise<string> {
  const buf = new Uint8Array(await file.arrayBuffer());
  const text = new TextDecoder().decode(buf);
  if (text.includes("-----BEGIN")) return text;
  if (/\.(cer|crt|der)$/i.test(file.name) || buf[0] === 0x30) {
    let bin = "";
    buf.forEach((b) => (bin += String.fromCharCode(b)));
    const b64 = btoa(bin).replace(/(.{64})/g, "$1\n");
    return `-----BEGIN CERTIFICATE-----\n${b64}\n-----END CERTIFICATE-----\n`;
  }
  throw new Error("Binäre OpenPGP-Schlüssel bitte im ASCII-Format exportieren (z. B. gpg --armor --export).");
}

function KeyForm({ current, onDone, onCancel }: { current: MailEncryptionConfig | null; onDone: () => void; onCancel?: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const fileRef = useRef<HTMLInputElement>(null);
  const [kind, setKind] = useState<EncryptionKind>(current?.kind ?? "pgp");
  const [key, setKey] = useState("");
  const [hideSubject, setHideSubject] = useState(current?.hideSubject ?? false);
  const [checked, setChecked] = useState<{ key: KeyInfo; matchesAccount: boolean } | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [ack, setAck] = useState(!!current);

  const inspect = useMutation({
    mutationFn: () => atRestApi.inspectKey(kind, key),
    onSuccess: (r) => {
      setChecked(r);
      setError(null);
    },
    onError: (err) => {
      setChecked(null);
      setError(errorMessage(err));
    },
  });
  const save = useMutation({
    mutationFn: () => atRestApi.saveMailEncryption({ kind, publicKey: key, hideSubject }),
    onSuccess: (state) => {
      qc.setQueryData(atRestKeys.mailEncryption, state);
      toast.success("Verschlüsselung aktiviert", "Neue Nachrichten werden ab jetzt verschlüsselt gespeichert.");
      onDone();
    },
    onError: (err) => setError(errorMessage(err)),
  });

  return (
    <div className="space-y-4">
      <Segmented
        value={kind}
        onChange={(v) => {
          setKind(v);
          setChecked(null);
        }}
        items={[
          { value: "pgp", label: "OpenPGP" },
          { value: "smime", label: "S/MIME" },
        ]}
      />
      <Field
        label={kind === "pgp" ? "Öffentlicher OpenPGP-Schlüssel" : "S/MIME-Zertifikat"}
        help={
          kind === "pgp"
            ? "ASCII-Format („-----BEGIN PGP PUBLIC KEY BLOCK-----“). Thunderbird: Konten-Einstellungen → Ende-zu-Ende-Verschlüsselung → Schlüssel exportieren."
            : "Zertifikat (PEM oder .cer/.crt) mit RSA-Schlüssel – nur das Zertifikat, nicht die .p12-Datei mit dem privaten Schlüssel."
        }
      >
        {(id) => (
          <Textarea
            id={id}
            rows={6}
            value={key}
            onChange={(e) => {
              setKey(e.target.value);
              setChecked(null);
            }}
            placeholder={kind === "pgp" ? "-----BEGIN PGP PUBLIC KEY BLOCK-----" : "-----BEGIN CERTIFICATE-----"}
            className="font-mono text-xs"
          />
        )}
      </Field>
      <div className="flex flex-wrap gap-2">
        <input
          ref={fileRef}
          type="file"
          accept=".asc,.pem,.cer,.crt,.der,.txt,.pub,.key"
          className="hidden"
          onChange={(e) => {
            const f = e.target.files?.[0];
            e.target.value = "";
            if (!f) return;
            readKeyFile(f)
              .then((t) => {
                setKey(t);
                setChecked(null);
                setError(null);
              })
              .catch((err) => setError(errorMessage(err)));
          }}
        />
        <Button size="sm" variant="secondary" icon={Upload} onClick={() => fileRef.current?.click()}>
          Datei wählen
        </Button>
        <Button size="sm" variant="secondary" icon={KeyRound} disabled={!key.trim()} loading={inspect.isPending} onClick={() => inspect.mutate()}>
          Schlüssel prüfen
        </Button>
      </div>
      {error && <Notice tone="error">{error}</Notice>}
      {checked && <KeyDetails info={checked.key} matches={checked.matchesAccount} />}
      <SwitchRow
        label="Betreff verbergen"
        description="Der Betreff steht dann nur im verschlüsselten Teil; in der Liste erscheint „...“. Mail-Programme mit deinem Schlüssel zeigen den echten Betreff nach dem Entschlüsseln."
        checked={hideSubject}
        onChange={setHideSubject}
      />
      {!current && (
        <>
          <Consequences />
          <label className="flex cursor-pointer items-start gap-2.5 text-sm text-zinc-700 dark:text-zinc-300">
            <Checkbox checked={ack} onChange={(e) => setAck(e.target.checked)} className="mt-0.5" />
            <span>Ich habe den passenden privaten Schlüssel sicher aufbewahrt und in meinem Mail-Programm eingerichtet.</span>
          </label>
        </>
      )}
      <div className="flex flex-wrap gap-2">
        <Button variant="primary" icon={Lock} disabled={!checked || !ack} loading={save.isPending} onClick={() => save.mutate()}>
          {current ? "Schlüssel ersetzen" : "Verschlüsselung aktivieren"}
        </Button>
        {onCancel && (
          <Button variant="ghost" onClick={onCancel}>
            Abbrechen
          </Button>
        )}
      </div>
      {!checked && key.trim() && <p className="text-xs text-zinc-500 dark:text-zinc-400">Bitte den Schlüssel zuerst prüfen.</p>}
    </div>
  );
}

/** Webmail settings: encrypt stored mail with the user's own public key. */
export function AtRestCard() {
  const qc = useQueryClient();
  const toast = useToast();
  const confirm = useConfirm();
  const [replacing, setReplacing] = useState(false);
  const q = useQuery({
    queryKey: atRestKeys.mailEncryption,
    queryFn: atRestApi.mailEncryption,
    refetchInterval: (query) => (query.state.data?.job?.running ? 1500 : false),
  });
  const cfg = q.data?.config ?? null;
  const job = q.data?.job ?? null;

  const update = useMutation({
    mutationFn: (hideSubject: boolean) => atRestApi.saveMailEncryption({ hideSubject }),
    onSuccess: (state) => {
      qc.setQueryData(atRestKeys.mailEncryption, state);
      toast.success("Gespeichert");
    },
    onError: (err) => toast.error(err, "Speichern fehlgeschlagen"),
  });
  const disable = useMutation({
    mutationFn: atRestApi.disableMailEncryption,
    onSuccess: (state) => {
      qc.setQueryData(atRestKeys.mailEncryption, state);
      toast.success("Verschlüsselung deaktiviert", "Neue Nachrichten werden wieder unverschlüsselt gespeichert.");
    },
    onError: (err) => toast.error(err, "Deaktivieren fehlgeschlagen"),
  });
  const encryptAll = useMutation({
    mutationFn: atRestApi.encryptExisting,
    onSuccess: (state) => qc.setQueryData(atRestKeys.mailEncryption, state),
    onError: (err) => toast.error(err, "Start fehlgeschlagen"),
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: qk.messages });
    },
  });

  const onEncryptAll = async () => {
    const ok = await confirm({
      title: "Bestehende Nachrichten verschlüsseln?",
      message: `Alle ${formatNumber(q.data?.messages ?? 0)} gespeicherten Nachrichten (außer Entwürfen) werden mit deinem Schlüssel verschlüsselt. Das lässt sich nicht rückgängig machen – Vorschau, Suche und KI-Ergebnisse dieser Nachrichten werden entfernt. Mail-Programme laden die Nachrichten neu.`,
      confirmLabel: "Verschlüsseln",
      danger: true,
    });
    if (ok) encryptAll.mutate();
  };
  const onDisable = async () => {
    const ok = await confirm({
      title: "Verschlüsselung deaktivieren?",
      message: "Neue Nachrichten werden wieder unverschlüsselt gespeichert. Bereits verschlüsselte Nachrichten bleiben verschlüsselt und sind weiterhin nur mit deinem privaten Schlüssel lesbar.",
      confirmLabel: "Deaktivieren",
      danger: true,
    });
    if (ok) disable.mutate();
  };

  return (
    <Card id="encryption">
      <CardHeader
        title="Verschlüsselung im Ruhezustand"
        description="Neue Nachrichten werden mit deinem öffentlichen Schlüssel verschlüsselt, bevor sie auf dem Server gespeichert werden."
        icon={<ShieldCheck className="h-4 w-4 text-zinc-400" />}
        actions={cfg ? <Badge tone="green" dot>Aktiv</Badge> : <Badge tone="neutral">Aus</Badge>}
      />
      <CardBody className="space-y-4">
        {q.isLoading ? (
          <Skeleton className="h-32 rounded-lg" />
        ) : !cfg || replacing ? (
          <KeyForm current={cfg} onDone={() => setReplacing(false)} onCancel={replacing ? () => setReplacing(false) : undefined} />
        ) : (
          <>
            <KeyDetails info={cfg} />
            <SwitchRow
              label="Betreff verbergen"
              description="Gilt für neu gespeicherte Nachrichten. In der Liste erscheint „...“ statt des Betreffs."
              checked={cfg.hideSubject}
              onChange={(v) => update.mutate(v)}
              disabled={update.isPending}
            />
            {job && <JobProgress job={job} />}
            <div className="flex flex-wrap gap-2 border-t border-zinc-100 pt-4 dark:border-zinc-800">
              <Button
                variant="secondary"
                icon={job?.running ? RefreshCw : Lock}
                disabled={job?.running}
                loading={encryptAll.isPending}
                onClick={() => void onEncryptAll()}
              >
                Bestehende Nachrichten verschlüsseln
              </Button>
              <Button variant="ghost" icon={KeyRound} onClick={() => setReplacing(true)}>
                Schlüssel ersetzen
              </Button>
              <Button variant="danger-ghost" icon={LockOpen} loading={disable.isPending} onClick={() => void onDisable()}>
                Deaktivieren
              </Button>
            </div>
            <Consequences />
          </>
        )}
      </CardBody>
    </Card>
  );
}
