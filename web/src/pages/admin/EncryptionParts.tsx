import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { HardDrive, KeyRound, Lock, LockOpen, RefreshCw, ShieldCheck, TriangleAlert } from "lucide-react";
import { atRestApi, atRestKeys, type EncryptionStatus, type MasterKeySource } from "@/api/atrest";
import { adminApi } from "@/api/endpoints";
import { formatBytes, formatNumber } from "@/lib/format";
import { Badge, Button, Card, CardBody, CardHeader, Notice, Progress, Skeleton, SwitchRow, useConfirm, useToast } from "@/components/ui";

const sourceLabel: Record<MasterKeySource, string> = {
  env: "Umgebungsvariable POSTWERK_MASTER_KEY",
  "env-file": "Datei aus POSTWERK_MASTER_KEY_FILE",
  "data-file": "Datei master.key im Datenverzeichnis",
};

function useEncryptionStatus() {
  return useQuery({
    queryKey: atRestKeys.encryptionStatus,
    queryFn: () => atRestApi.encryptionStatus(true),
    refetchInterval: (query) => (query.state.data?.blobs.job?.running ? 1500 : false),
  });
}

function MasterKeyBox({ status }: { status: EncryptionStatus }) {
  const mk = status.masterKey;
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <KeyRound className="h-4 w-4 text-zinc-400" />
        <span className="font-medium text-zinc-900 dark:text-zinc-100">Master-Schlüssel</span>
        <Badge tone={mk.inDataDir ? "amber" : "green"}>{mk.inDataDir ? "im Datenverzeichnis" : "außerhalb des Datenverzeichnisses"}</Badge>
        <span className="font-mono text-xs text-zinc-500 dark:text-zinc-400">ID {mk.fingerprint}</span>
      </div>
      <p className="text-sm text-zinc-600 dark:text-zinc-400">
        Quelle: {sourceLabel[mk.source]}
        {mk.file && <span className="font-mono text-xs"> ({mk.file})</span>}. Damit sind API-Schlüssel, Passwörter und Tokens in den Einstellungen,
        TOTP-Geheimnisse, DKIM-Schlüssel und das SRS-Geheimnis in der Datenbank mit AES-256-GCM verschlüsselt.
      </p>
      {mk.inDataDir ? (
        <Notice tone="info">
          Der Schlüssel liegt neben der Datenbank und wird deshalb mitgesichert. Das schützt vor dem Auslesen einzelner Datenbank-Dateien, aber nicht
          vor jemandem, der das ganze Datenverzeichnis oder ein unverschlüsseltes Backup bekommt. Für mehr Schutz den Inhalt von master.key als
          POSTWERK_MASTER_KEY setzen und die Datei danach entfernen.
        </Notice>
      ) : (
        <Notice tone="warning" icon={TriangleAlert}>
          Der Schlüssel ist nicht in Backups enthalten. Bewahre ihn getrennt sicher auf – für jede Wiederherstellung muss derselbe POSTWERK_MASTER_KEY gesetzt sein,
          sonst startet Postwerk nicht.
        </Notice>
      )}
    </div>
  );
}

function BlobEncryption({ status }: { status: EncryptionStatus }) {
  const qc = useQueryClient();
  const toast = useToast();
  const confirm = useConfirm();
  const { blobs, masterKey } = status;
  const job = blobs.job;
  const stats = blobs.stats;
  const mismatch = stats ? (blobs.enabled ? stats.encrypted < stats.total : stats.encrypted > 0) : false;

  const toggle = useMutation({
    mutationFn: (v: { enabled: boolean; migrate: boolean }) => atRestApi.setBlobEncryption(v.enabled, v.migrate),
    onSuccess: (s) => {
      qc.setQueryData(atRestKeys.encryptionStatus, s);
      toast.success(s.blobs.enabled ? "Speicherverschlüsselung aktiviert" : "Speicherverschlüsselung deaktiviert");
    },
    onError: (err) => toast.error(err, "Ändern fehlgeschlagen"),
  });
  const migrate = useMutation({
    mutationFn: (encrypt: boolean) => atRestApi.migrateBlobs(encrypt),
    onSuccess: (s) => qc.setQueryData(atRestKeys.encryptionStatus, s),
    onError: (err) => toast.error(err, "Start fehlgeschlagen"),
  });

  const onToggle = async (enabled: boolean) => {
    const ok = await confirm({
      title: enabled ? "Nachrichten auf der Festplatte verschlüsseln?" : "Speicherverschlüsselung abschalten?",
      message: enabled
        ? "Neue Nachrichten werden verschlüsselt gespeichert, vorhandene werden im Hintergrund umgewandelt. Ohne den Master-Schlüssel sind die Nachrichten danach nicht mehr lesbar – auch nicht aus einem Backup."
        : "Neue Nachrichten werden unverschlüsselt gespeichert und vorhandene im Hintergrund entschlüsselt.",
      confirmLabel: enabled ? "Verschlüsseln" : "Abschalten",
      danger: !enabled,
    });
    if (ok) toggle.mutate({ enabled, migrate: true });
  };

  return (
    <div className="space-y-3 border-t border-zinc-100 pt-5 dark:border-zinc-800">
      <SwitchRow
        label="Nachrichten auf der Festplatte verschlüsseln"
        description="Alle Nachrichten-Dateien (Blobs) werden mit dem Master-Schlüssel verschlüsselt (AES-256-GCM). Postwerk liest sie transparent; IMAP, Suche und KI funktionieren weiter."
        checked={blobs.enabled}
        onChange={(v) => void onToggle(v)}
        disabled={toggle.isPending || job?.running}
      />
      {masterKey.inDataDir && (
        <Notice tone="warning" icon={TriangleAlert}>
          Schützt nur vor Festplatten- oder Backup-Diebstahl, wenn der Master-Schlüssel außerhalb des Datenverzeichnisses liegt (POSTWERK_MASTER_KEY). Solange master.key
          daneben liegt, kann jeder mit Zugriff auf das Verzeichnis die Nachrichten entschlüsseln.
        </Notice>
      )}
      {stats && (
        <div className="flex flex-wrap items-center gap-2 text-xs text-zinc-500 dark:text-zinc-400">
          <HardDrive className="h-4 w-4 text-zinc-400" />
          {formatNumber(stats.total)} Dateien ({formatBytes(stats.bytes)}), davon {formatNumber(stats.encrypted)} verschlüsselt
        </div>
      )}
      {job && (
        <div className="space-y-1.5">
          <div className="flex justify-between text-xs text-zinc-500 dark:text-zinc-400">
            <span>
              {job.running
                ? job.encrypt
                  ? "Dateien werden verschlüsselt …"
                  : "Dateien werden entschlüsselt …"
                : job.error
                  ? `Beendet mit Fehlern: ${job.error}`
                  : "Umwandlung abgeschlossen"}
            </span>
            <span className="tabular-nums">
              {formatNumber(job.done)} / {formatNumber(job.total)}
            </span>
          </div>
          <Progress value={job.total > 0 ? job.done / job.total : job.running ? 0 : 1} tone="indigo" className="h-2" />
        </div>
      )}
      {mismatch && !job?.running && (
        <Button size="sm" variant="secondary" icon={blobs.enabled ? Lock : LockOpen} loading={migrate.isPending} onClick={() => migrate.mutate(blobs.enabled)}>
          {blobs.enabled ? "Restliche Dateien verschlüsseln" : "Restliche Dateien entschlüsseln"}
        </Button>
      )}
    </div>
  );
}

/** Admin settings (security): master key status and blob encryption. */
export function EncryptionCard() {
  const q = useEncryptionStatus();
  return (
    <Card className="mt-6">
      <CardHeader
        title="Verschlüsselung im Ruhezustand"
        description="Geheimnisse in der Datenbank sind immer verschlüsselt. Optional auch alle gespeicherten Nachrichten."
        icon={<ShieldCheck className="h-4 w-4 text-zinc-400" />}
        actions={
          <Button size="icon-sm" variant="ghost" icon={RefreshCw} aria-label="Aktualisieren" loading={q.isFetching && !q.isLoading} onClick={() => void q.refetch()} />
        }
      />
      <CardBody className="space-y-5">
        {q.isLoading || !q.data ? (
          <Skeleton className="h-40 rounded-lg" />
        ) : (
          <>
            <MasterKeyBox status={q.data} />
            <BlobEncryption status={q.data} />
            <p className="border-t border-zinc-100 pt-4 text-xs text-zinc-500 dark:border-zinc-800 dark:text-zinc-400">
              {formatNumber(q.data.mailEncryptionAccounts)} Konten verschlüsseln eingehende Mails zusätzlich mit ihrem eigenen öffentlichen Schlüssel
              (OpenPGP/S/MIME, in den Konto-Einstellungen der Benutzer).
            </p>
          </>
        )}
      </CardBody>
    </Card>
  );
}

/** Backup page: what a restore needs regarding the master key. */
export function MasterKeyNotice() {
  const status = useQuery({ queryKey: atRestKeys.encryptionStatus, queryFn: () => atRestApi.encryptionStatus(false), staleTime: 60_000 });
  const settings = useQuery({ queryKey: ["admin", "settings"], queryFn: adminApi.settings, staleTime: 60_000 });
  const mk = status.data?.masterKey;
  if (!mk || !settings.data) return null;
  if (!mk.inDataDir) {
    return (
      <Notice tone="warning" icon={KeyRound} title="Master-Schlüssel nicht im Backup">
        Der Master-Schlüssel kommt aus {mk.source === "env" ? "POSTWERK_MASTER_KEY" : "POSTWERK_MASTER_KEY_FILE"} und ist in keinem Archiv enthalten. Für eine
        Wiederherstellung muss derselbe Schlüssel (ID {mk.fingerprint}) gesetzt sein – ohne ihn sind alle gespeicherten Passwörter, TOTP- und DKIM-Schlüssel
        {status.data?.blobs.enabled ? " sowie die verschlüsselten Nachrichten" : ""} verloren.
      </Notice>
    );
  }
  if (!settings.data.backup.encrypt) {
    return (
      <Notice tone="warning" icon={TriangleAlert} title="Unverschlüsselte Backups enthalten den Master-Schlüssel">
        Archive enthalten master.key, damit eine Wiederherstellung alle Geheimnisse behält. Bei unverschlüsselten Backups kann deshalb jeder mit Zugriff auf
        ein Archiv auch die verschlüsselten Geheimnisse lesen. Backup-Verschlüsselung in den Einstellungen aktivieren oder den Schlüssel per
        POSTWERK_MASTER_KEY außerhalb des Datenverzeichnisses halten.
      </Notice>
    );
  }
  return null;
}
