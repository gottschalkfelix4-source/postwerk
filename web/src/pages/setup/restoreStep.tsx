import { useEffect, useRef, useState, type FormEvent } from "react";
import { useNavigate } from "react-router-dom";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { ArchiveRestore, CircleCheck, FileArchive, HardDrive, KeyRound, Search, Upload, type LucideIcon } from "lucide-react";
import { setupApi } from "@/api/endpoints";
import { errorMessage, isApiError } from "@/api/client";
import type { BackupArchive, BackupManifest, BackupStatus, BackupTarget } from "@/api/types";
import { formatBytes, formatDateTime } from "@/lib/format";
import { qk } from "@/lib/queries";
import { cn } from "@/lib/utils";
import { Badge, Button, Field, Input, Notice, PasswordInput, Progress, WizardNav, WizardStep } from "@/components/ui";
import { BackupTargetFields, backupTargetInvalid } from "@/pages/admin/backup/BackupSettingsForm";
import { useSetup } from "./context";

type Source = "upload" | "target";
type Phase = "form" | "working" | "key" | "restarting" | "done" | "failed";

const emptyTarget: BackupTarget = {
  type: "local",
  localPath: "",
  webdav: { url: "", username: "", password: "", skipTlsVerify: false },
  smb: { host: "", port: 445, share: "", path: "", username: "", password: "", domain: "" },
};

const sources: Record<Source, { title: string; text: string; icon: LucideIcon }> = {
  upload: { title: "Datei hochladen", text: "Ein heruntergeladenes Archiv (.tar.gz oder .tar.gz.enc) von diesem Rechner.", icon: Upload },
  target: { title: "Vom Sicherungsziel laden", text: "Ordner im Container, WebDAV (z. B. Nextcloud) oder SMB-Freigabe.", icon: HardDrive },
};

const keyCodes = ["master_key_required", "master_key_mismatch"];

function isKeyError(err: unknown): boolean {
  return keyCodes.some((c) => isApiError(err, c));
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

/**
 * Restore a backup on a fresh server instead of setting it up from scratch.
 * The archive is unpacked and checked on the server, which then restarts
 * with the old data; afterwards the old accounts log in as before.
 */
export function RestoreFlow({ onCancel }: { onCancel: () => void }) {
  const ctx = useSetup();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [source, setSource] = useState<Source>("upload");
  const [file, setFile] = useState<File | null>(null);
  const [target, setTarget] = useState<BackupTarget>(emptyTarget);
  const [archives, setArchives] = useState<BackupArchive[] | null>(null);
  const [listError, setListError] = useState<string | null>(null);
  const [selected, setSelected] = useState<string>("");
  const [password, setPassword] = useState("");
  const [masterKey, setMasterKey] = useState("");
  const [showKey, setShowKey] = useState(false);
  const [phase, setPhase] = useState<Phase>("form");
  const [progress, setProgress] = useState<{ label: string; value: number | null } | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [manifest, setManifest] = useState<BackupManifest | null>(null);
  const [fingerprint, setFingerprint] = useState<string | null>(null);
  const fileRef = useRef<HTMLInputElement>(null);
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);

  const token = ctx.token.trim();
  const selectedArchive = archives?.find((a) => a.name === selected);
  const encrypted = source === "upload" ? !!file && file.name.endsWith(".enc") : !!selectedArchive?.encrypted;

  const fail = (err: unknown) => {
    if (isApiError(err, "invalid_token")) {
      ctx.setTokenError("Der Setup-Token ist ungültig.");
      setPhase("form");
      return;
    }
    if (isKeyError(err)) {
      setError(errorMessage(err));
      setPhase("key");
      setShowKey(true);
      return;
    }
    setError(errorMessage(err));
    setPhase("form");
  };

  const list = useMutation({
    mutationFn: () => setupApi.restoreArchives(token, target),
    onMutate: () => {
      setArchives(null);
      setListError(null);
      setSelected("");
    },
    onSuccess: (res) => {
      setArchives(res.archives);
      setListError(res.error ?? null);
      if (res.archives.length > 0) setSelected(res.archives[0].name);
    },
    onError: (err) => {
      if (isApiError(err, "invalid_token")) ctx.setTokenError("Der Setup-Token ist ungültig.");
      else setListError(errorMessage(err));
    },
  });

  // Waits until the server is back with the restored data.
  const awaitRestart = async () => {
    setPhase("restarting");
    const deadline = Date.now() + 5 * 60_000;
    await sleep(2000);
    while (alive.current && Date.now() < deadline) {
      try {
        const st = await setupApi.status();
        if (st.mode !== "initial") {
          void qc.invalidateQueries({ queryKey: qk.setupStatus });
          setPhase("done");
          return;
        }
        if (st.restoreError) {
          setError(st.restoreError);
          setPhase("failed");
          return;
        }
      } catch {
        // Restarting: not reachable for a moment.
      }
      await sleep(2000);
    }
    if (alive.current) {
      setError("Der Server meldet sich nicht zurück. Bitte das Container-Protokoll prüfen und die Seite neu laden.");
      setPhase("failed");
    }
  };

  // Follows a restore from a backup target (runs in the background on the server).
  const followJob = async () => {
    while (alive.current) {
      await sleep(1000);
      let st: BackupStatus;
      try {
        st = await setupApi.restoreStatus(token);
      } catch (err) {
        if (isApiError(err, "invalid_token") || isApiError(err, "rate_limited")) {
          fail(err);
          return;
        }
        // Unreachable or "already initialized": the server is restarting
        // or already back with the restored data.
        void awaitRestart();
        return;
      }
      const job = st.job;
      if (job) {
        if (job.phase === "Neustart") {
          void awaitRestart();
          return;
        }
        setProgress({ label: job.phase, value: job.total > 0 ? job.done / job.total : null });
        continue;
      }
      const last = st.last;
      if (last && last.kind === "restore" && !last.ok) {
        setError(last.error ?? "Wiederherstellung fehlgeschlagen");
        if (last.code) {
          setFingerprint(last.keyFingerprint ?? null);
          setShowKey(true);
          setPhase("key");
        } else {
          setPhase("form");
        }
        return;
      }
      // Finished between two polls: the restart follows.
      void awaitRestart();
      return;
    }
  };

  const start = async () => {
    setError(null);
    ctx.setTokenError(null);
    setPhase("working");
    try {
      if (source === "upload") {
        if (!file) return;
        setProgress({ label: "Archiv hochladen und prüfen", value: 0 });
        const res = await setupApi.restoreUpload(
          file,
          { token, password, masterKey: masterKey.trim() },
          {
            onProgress: (p) =>
              setProgress(p >= 1 ? { label: "Archiv entpacken und prüfen", value: null } : { label: "Archiv hochladen und prüfen", value: p }),
          },
        );
        setManifest(res.manifest);
        void awaitRestart();
      } else {
        setProgress({ label: "Archiv laden", value: null });
        await setupApi.restoreStart({ token, target, name: selected, password, masterKey: masterKey.trim() });
        void followJob();
      }
    } catch (err) {
      if (isKeyError(err)) setFingerprint(null);
      fail(err);
    }
  };

  const resume = async () => {
    setError(null);
    setPhase("working");
    setProgress({ label: "Master-Schlüssel prüfen", value: null });
    try {
      const res = await setupApi.restoreResume(token, masterKey.trim());
      setManifest(res.manifest);
      void awaitRestart();
    } catch (err) {
      fail(err);
    }
  };

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (phase === "key") void resume();
    else void start();
  };

  if (phase === "done") {
    return (
      <WizardStep
        icon={CircleCheck}
        title="Wiederherstellung abgeschlossen"
        description="Der Server läuft wieder mit dem Stand der Sicherung."
        footer={<WizardNav onNext={() => navigate("/login", { replace: true })} nextLabel="Zur Anmeldung" />}
      >
        <div className="space-y-4">
          <Notice tone="success" icon={CircleCheck}>
            Konten, Mails, Domains mit DKIM-Schlüsseln, Filterregeln, Kalender, Kontakte und alle Einstellungen sind zurück. Melde dich
            mit deinem bisherigen Admin-Konto an.
          </Notice>
          {manifest && <ManifestSummary manifest={manifest} />}
          <p className="text-sm text-zinc-600 dark:text-zinc-300">
            Hat sich die IP-Adresse des Servers geändert, passe den A-Eintrag und den Reverse-DNS-Eintrag an. Unter Administration → DNS
            prüfst du danach, ob alles stimmt.
          </p>
        </div>
      </WizardStep>
    );
  }

  const busy = phase === "working" || phase === "restarting";
  const canStart =
    !!token &&
    (source === "upload" ? !!file : !!selected) &&
    (!encrypted || password.length > 0) &&
    (source === "upload" || !backupTargetInvalid(target));

  return (
    <WizardStep
      icon={ArchiveRestore}
      title="Aus Backup wiederherstellen"
      description="Spielt eine Postwerk-Sicherung ein. Danach ist der Server auf dem Stand der Sicherung – inklusive Konten und Einstellungen."
      footer={
        <WizardNav
          form="setup-restore"
          onBack={busy ? undefined : onCancel}
          backLabel="Neu einrichten"
          nextLabel={phase === "key" ? "Mit Schlüssel fortsetzen" : "Wiederherstellen"}
          nextIcon={ArchiveRestore}
          nextDisabled={busy || (phase === "key" ? !masterKey.trim() : !canStart)}
          loading={busy}
        />
      }
    >
      <form id="setup-restore" onSubmit={submit} className="space-y-6" noValidate>
        {phase === "failed" && error && (
          <Notice tone="error" title="Wiederherstellung fehlgeschlagen">
            <p>{error}</p>
            <p className="mt-2">
              Der Server läuft mit den vorherigen (leeren) Daten weiter. Beim Neustart wurde ein neuer Setup-Token erzeugt – bitte erneut
              anzeigen lassen und eintragen.
            </p>
          </Notice>
        )}
        {busy && progress && (
          <div className="space-y-2 rounded-xl border border-indigo-200 bg-indigo-50/60 p-4 dark:border-indigo-500/25 dark:bg-indigo-500/[0.07]">
            <div className="text-sm font-medium text-zinc-900 dark:text-zinc-100">
              {phase === "restarting" ? "Server startet mit den wiederhergestellten Daten neu …" : `${progress.label} …`}
            </div>
            {phase !== "restarting" && progress.value !== null ? (
              <Progress value={progress.value} tone="indigo" />
            ) : (
              <Progress value={1} tone="indigo" className="animate-pulse" />
            )}
            {manifest && <ManifestSummary manifest={manifest} />}
          </div>
        )}

        {phase === "key" ? (
          <div className="space-y-4">
            {error && <Notice tone="warning" icon={KeyRound} title="Master-Schlüssel benötigt">{error}</Notice>}
            <p className="text-sm text-zinc-600 dark:text-zinc-300">
              Das Archiv ist bereits entpackt. Gib den Master-Schlüssel des alten Servers ein (Inhalt von <code className="font-mono">master.key</code>{" "}
              bzw. der Wert von <code className="font-mono">POSTWERK_MASTER_KEY</code>){fingerprint ? ` – Fingerabdruck ${fingerprint}` : ""}. Er wird als{" "}
              <code className="font-mono">master.key</code> im Datenverzeichnis gespeichert. Nutzt dieser Server selbst{" "}
              <code className="font-mono">POSTWERK_MASTER_KEY</code>, trage den alten Schlüssel stattdessen dort ein und starte den Container neu.
            </p>
            <Field label="Master-Schlüssel" required>
              {(id) => (
                <Input
                  id={id}
                  value={masterKey}
                  onChange={(e) => setMasterKey(e.target.value)}
                  autoComplete="off"
                  spellCheck={false}
                  autoFocus
                  placeholder="Base64, 44 Zeichen"
                  className="font-mono text-[13px]"
                />
              )}
            </Field>
          </div>
        ) : (
          <fieldset disabled={busy} className="space-y-6">
            <Field label="Setup-Token" required error={ctx.tokenError} help="Derselbe Token wie für die Neueinrichtung (docker exec postwerk postwerk setup-token).">
              {(id) => (
                <Input
                  id={id}
                  icon={KeyRound}
                  value={ctx.token}
                  onChange={(e) => ctx.setToken(e.target.value)}
                  invalid={!!ctx.tokenError}
                  autoComplete="off"
                  spellCheck={false}
                  className="font-mono text-[13px]"
                />
              )}
            </Field>

            <div className="grid gap-2.5 sm:grid-cols-2" role="radiogroup" aria-label="Quelle der Sicherung">
              {(Object.keys(sources) as Source[]).map((s) => {
                const meta = sources[s];
                const active = s === source;
                return (
                  <button
                    key={s}
                    type="button"
                    role="radio"
                    aria-checked={active}
                    onClick={() => {
                      setSource(s);
                      setError(null);
                    }}
                    className={cn(
                      "flex min-w-0 flex-col gap-1.5 rounded-xl border p-3 text-left transition",
                      "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-indigo-500/50",
                      active
                        ? "border-indigo-400 bg-indigo-50/60 ring-1 ring-indigo-400 dark:border-indigo-500/60 dark:bg-indigo-500/10 dark:ring-indigo-500/60"
                        : "border-zinc-200 hover:border-zinc-300 hover:bg-zinc-50 dark:border-zinc-800 dark:hover:border-zinc-700 dark:hover:bg-zinc-800/50",
                    )}
                  >
                    <span className="flex items-center gap-2 text-sm font-medium text-zinc-900 dark:text-zinc-100">
                      <meta.icon className="h-4 w-4 text-indigo-500" /> {meta.title}
                    </span>
                    <span className="text-xs leading-relaxed text-zinc-500 dark:text-zinc-400">{meta.text}</span>
                  </button>
                );
              })}
            </div>

            {source === "upload" ? (
              <div className="space-y-2">
                <input
                  ref={fileRef}
                  type="file"
                  accept=".gz,.enc,application/gzip,application/octet-stream"
                  className="hidden"
                  onChange={(e) => {
                    setFile(e.target.files?.[0] ?? null);
                    setError(null);
                  }}
                />
                <div className="flex flex-wrap items-center gap-3">
                  <Button icon={FileArchive} onClick={() => fileRef.current?.click()}>
                    {file ? "Andere Datei wählen" : "Archiv auswählen"}
                  </Button>
                  {file && (
                    <span className="min-w-0 truncate font-mono text-xs text-zinc-600 dark:text-zinc-300">
                      {file.name} · {formatBytes(file.size)}
                    </span>
                  )}
                </div>
                <p className="text-xs text-zinc-500 dark:text-zinc-400">
                  Große Archive lieber vom Sicherungsziel laden oder per SMB/WebDAV bereitstellen – der Upload läuft über den Browser.
                </p>
              </div>
            ) : (
              <div className="space-y-4">
                <BackupTargetFields
                  target={target}
                  onChange={(patch) => {
                    setTarget((t) => ({ ...t, ...patch }));
                    setArchives(null);
                    setSelected("");
                  }}
                />
                <Button icon={Search} onClick={() => list.mutate()} loading={list.isPending} disabled={!token || backupTargetInvalid(target)}>
                  Sicherungen suchen
                </Button>
                {listError && <Notice tone="error">{listError}</Notice>}
                {archives && archives.length === 0 && !listError && (
                  <Notice tone="warning">Am Ziel liegen keine Postwerk-Sicherungen (postwerk-….tar.gz bzw. .tar.gz.enc).</Notice>
                )}
                {archives && archives.length > 0 && (
                  <div className="divide-y divide-zinc-100 rounded-xl border border-zinc-200 dark:divide-zinc-800 dark:border-zinc-800" role="radiogroup" aria-label="Sicherung">
                    {archives.map((a) => (
                      <label key={a.name} className="flex cursor-pointer items-center gap-3 px-3 py-2.5 text-sm hover:bg-zinc-50 dark:hover:bg-zinc-800/50">
                        <input type="radio" name="archive" checked={selected === a.name} onChange={() => setSelected(a.name)} className="accent-indigo-500" />
                        <span className="min-w-0 flex-1 truncate font-mono text-[12.5px] text-zinc-800 dark:text-zinc-200">{a.name}</span>
                        {a.encrypted && <Badge tone="indigo">verschlüsselt</Badge>}
                        <span className="hidden text-xs text-zinc-500 sm:inline">{formatDateTime(a.modified)}</span>
                        <span className="w-16 text-right text-xs text-zinc-500">{formatBytes(a.size)}</span>
                      </label>
                    ))}
                  </div>
                )}
              </div>
            )}

            {encrypted && (
              <Field label="Backup-Passwort" required help="Das Passwort, das auf dem alten Server für die Verschlüsselung der Sicherungen eingestellt war.">
                {(id) => <PasswordInput id={id} value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="off" />}
              </Field>
            )}

            {showKey ? (
              <Field
                label="Master-Schlüssel des alten Servers"
                help="Nur nötig, wenn der alte Server den Schlüssel aus POSTWERK_MASTER_KEY las – dann steckt er nicht im Archiv."
              >
                {(id) => (
                  <Input id={id} value={masterKey} onChange={(e) => setMasterKey(e.target.value)} autoComplete="off" spellCheck={false} className="font-mono text-[13px]" />
                )}
              </Field>
            ) : (
              <button type="button" className="text-xs text-indigo-600 hover:underline dark:text-indigo-400" onClick={() => setShowKey(true)}>
                Der alte Server nutzte POSTWERK_MASTER_KEY? Schlüssel angeben
              </button>
            )}

            {error && phase === "form" && <Notice tone="error">{error}</Notice>}

            <Notice tone="info">
              Das Archiv wird vor dem Einspielen vollständig geprüft (Format, Passwort, Datenbank, Master-Schlüssel). Ist etwas nicht in
              Ordnung, bleibt der Server unverändert. Danach startet er einmal neu.
            </Notice>
          </fieldset>
        )}
      </form>
    </WizardStep>
  );
}

function ManifestSummary({ manifest: m }: { manifest: BackupManifest }) {
  return (
    <dl className="grid grid-cols-2 gap-x-4 gap-y-1 text-xs text-zinc-600 dark:text-zinc-300 sm:grid-cols-4">
      <div>
        <dt className="text-zinc-500">Server</dt>
        <dd className="truncate font-mono">{m.hostname || "–"}</dd>
      </div>
      <div>
        <dt className="text-zinc-500">Gesichert am</dt>
        <dd>{formatDateTime(m.createdAt)}</dd>
      </div>
      <div>
        <dt className="text-zinc-500">Konten / Domains</dt>
        <dd>
          {m.accounts} / {m.domains}
        </dd>
      </div>
      <div>
        <dt className="text-zinc-500">Nachrichten</dt>
        <dd>{m.messages.toLocaleString("de-DE")}</dd>
      </div>
    </dl>
  );
}
