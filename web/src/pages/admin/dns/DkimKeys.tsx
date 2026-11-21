import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CircleCheck, CircleX, KeyRound, Plus, RefreshCw, ShieldCheck, Trash2 } from "lucide-react";
import { isApiError } from "@/api/client";
import { mailAuthApi, type DkimAlgorithm, type DkimKey, type DkimPublication } from "@/api/mailauth";
import { formatDate, formatDateTime } from "@/lib/format";
import { DkimDnsCleanup } from "./DkimDnsCleanup";
import {
  Badge,
  Button,
  Card,
  CardBody,
  CardHeader,
  CopyButton,
  EmptyState,
  ErrorState,
  Modal,
  Segmented,
  Skeleton,
  useConfirm,
  useToast,
  type BadgeTone,
} from "@/components/ui";

const stateLabel: Record<DkimKey["state"], { label: string; tone: BadgeTone }> = {
  active: { label: "Aktiv", tone: "green" },
  pending: { label: "Neu – noch nicht aktiv", tone: "amber" },
  retired: { label: "Ausgemustert", tone: "neutral" },
};

const algLabel = (a: DkimAlgorithm) => (a === "ed25519" ? "Ed25519" : "RSA 2048");

/** DKIM key list with the rotation workflow (create → publish → check → activate). */
export function DkimKeysCard({ domain }: { domain: string }) {
  const qc = useQueryClient();
  const toast = useToast();
  const confirm = useConfirm();
  const [createOpen, setCreateOpen] = useState(false);
  const [checks, setChecks] = useState<Record<number, DkimPublication>>({});

  const q = useQuery({ queryKey: ["admin", "dkim", domain], queryFn: () => mailAuthApi.dkimKeys(domain), enabled: !!domain });

  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ["admin", "dkim", domain] });
    void qc.invalidateQueries({ queryKey: ["admin", "domains"] });
    void qc.invalidateQueries({ queryKey: ["admin", "domain-dns", domain] });
    void qc.invalidateQueries({ queryKey: ["admin", "dns-check", domain] });
    void qc.invalidateQueries({ queryKey: ["admin", "dkim-dns-cleanup", domain] });
  };

  const check = useMutation({
    mutationFn: (k: DkimKey) => mailAuthApi.checkDkimKey(domain, k.id),
    onSuccess: (res, k) => {
      setChecks((c) => ({ ...c, [k.id]: res }));
      if (res.published) toast.success("Schlüssel ist veröffentlicht", res.name);
      else toast.info("Noch nicht veröffentlicht", res.error);
    },
    onError: (err) => toast.error(err, "Prüfung fehlgeschlagen"),
  });

  const activate = useMutation({
    mutationFn: ({ k, force }: { k: DkimKey; force: boolean }) => mailAuthApi.activateDkimKey(domain, k.id, force),
    onSuccess: (keys, { k }) => {
      qc.setQueryData(["admin", "dkim", domain], keys);
      toast.success("Schlüssel aktiviert", `Ab sofort wird mit ${k.selector} signiert.`);
      refresh();
    },
    onError: async (err, { k, force }) => {
      if (!force && isApiError(err, "dkim_not_published")) {
        const ok = await confirm({
          title: "Trotzdem aktivieren?",
          message: `${err.message} Wird der Schlüssel aktiviert, bevor der DNS-Eintrag sichtbar ist, schlagen DKIM-Prüfungen bei den Empfängern fehl.`,
          confirmLabel: "Trotzdem aktivieren",
          danger: true,
        });
        if (ok) activate.mutate({ k, force: true });
        return;
      }
      toast.error(err, "Aktivieren fehlgeschlagen");
    },
  });

  const remove = useMutation({
    mutationFn: (k: DkimKey) => mailAuthApi.deleteDkimKey(domain, k.id),
    onSuccess: (_, k) => {
      toast.success("Schlüssel entfernt", k.selector);
      refresh();
    },
    onError: (err) => toast.error(err, "Entfernen fehlgeschlagen"),
  });

  return (
    <Card className="mt-6">
      <CardHeader
        title="DKIM-Schlüssel"
        description="Ausgehende Mails werden mit allen aktiven Schlüsseln signiert (RSA und optional Ed25519). Zum Wechseln einen neuen Schlüssel erzeugen, im DNS veröffentlichen, prüfen und aktivieren."
        actions={
          <Button size="sm" icon={Plus} onClick={() => setCreateOpen(true)} disabled={!domain}>
            Neuen Schlüssel erzeugen
          </Button>
        }
      />
      <CardBody className="space-y-3">
        {q.isLoading ? (
          <Skeleton className="h-24 rounded-lg" />
        ) : q.error ? (
          <ErrorState error={q.error} onRetry={() => void q.refetch()} />
        ) : !q.data || q.data.length === 0 ? (
          <EmptyState icon={KeyRound} title="Keine DKIM-Schlüssel" description="Erzeuge einen Schlüssel, damit ausgehende Mails signiert werden." />
        ) : (
          q.data.map((k) => (
            <KeyRow
              key={k.id}
              k={k}
              check={checks[k.id]}
              checking={check.isPending && check.variables?.id === k.id}
              activating={activate.isPending && activate.variables?.k.id === k.id}
              removing={remove.isPending && remove.variables?.id === k.id}
              onCheck={() => check.mutate(k)}
              onActivate={async () => {
                const ok = await confirm({
                  title: `${k.selector} aktivieren?`,
                  message:
                    k.algorithm === "rsa"
                      ? "Neue Mails werden mit diesem Schlüssel signiert. Der bisherige RSA-Schlüssel wird ausgemustert und sollte noch 7 Tage im DNS bleiben."
                      : "Neue Mails werden zusätzlich mit diesem Ed25519-Schlüssel signiert.",
                  confirmLabel: "Aktivieren",
                });
                if (ok) activate.mutate({ k, force: false });
              }}
              onRemove={async () => {
                const ok = await confirm({
                  title: `${k.selector} entfernen?`,
                  message:
                    k.state === "pending"
                      ? "Der noch nicht aktive Schlüssel wird verworfen. Einen bereits angelegten DNS-Eintrag kannst du danach löschen."
                      : `Der alte Schlüssel wird entfernt. Lösche danach auch den TXT-Eintrag ${k.dnsName} beim DNS-Anbieter.`,
                  confirmLabel: "Entfernen",
                  danger: true,
                });
                if (ok) remove.mutate(k);
              }}
            />
          ))
        )}
        <DkimDnsCleanup domain={domain} />
      </CardBody>
      <CreateKeyDialog
        open={createOpen}
        domain={domain}
        onClose={() => setCreateOpen(false)}
        hasPending={(alg) => !!q.data?.some((k) => k.algorithm === alg && k.state === "pending")}
        onCreated={(k) => {
          toast.success("Neuer Schlüssel erzeugt", `Jetzt den TXT-Eintrag für ${k.selector} veröffentlichen.`);
          refresh();
        }}
      />
    </Card>
  );
}

function KeyRow({
  k,
  check,
  checking,
  activating,
  removing,
  onCheck,
  onActivate,
  onRemove,
}: {
  k: DkimKey;
  check?: DkimPublication;
  checking: boolean;
  activating: boolean;
  removing: boolean;
  onCheck: () => void;
  onActivate: () => void;
  onRemove: () => void;
}) {
  const st = stateLabel[k.state];
  const removable = k.state === "pending" || (k.state === "retired" && !!k.removableAt && new Date(k.removableAt) <= new Date());
  return (
    <div className="rounded-lg border border-zinc-200 p-4 dark:border-zinc-800">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-mono text-sm font-semibold text-zinc-900 dark:text-zinc-100">{k.selector}</span>
            <Badge tone="indigo">{algLabel(k.algorithm)}</Badge>
            <Badge tone={st.tone} dot>
              {st.label}
            </Badge>
          </div>
          <p className="mt-1 text-xs text-zinc-500 dark:text-zinc-400">
            Erstellt {formatDate(k.createdAt)}
            {k.activatedAt && <> · aktiv seit {formatDate(k.activatedAt)}</>}
            {k.retiredAt && <> · ausgemustert am {formatDate(k.retiredAt)}</>}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {k.state !== "retired" && (
            <Button size="sm" variant="ghost" icon={RefreshCw} loading={checking} onClick={onCheck}>
              Prüfen
            </Button>
          )}
          {k.state === "pending" && (
            <Button size="sm" variant="primary" icon={ShieldCheck} loading={activating} onClick={onActivate}>
              Aktivieren
            </Button>
          )}
          {k.state !== "active" && (
            <Button size="sm" variant="danger-ghost" icon={Trash2} loading={removing} disabled={!removable} onClick={onRemove}
              title={removable ? undefined : "Erst nach Ablauf der Schonfrist möglich"}>
              {k.state === "pending" ? "Verwerfen" : "Entfernen"}
            </Button>
          )}
        </div>
      </div>

      {k.state !== "retired" && (
        <div className="mt-3 space-y-1.5 rounded-md bg-zinc-50 p-3 text-xs dark:bg-zinc-800/50">
          <div className="flex items-center justify-between gap-2">
            <span className="text-zinc-500 dark:text-zinc-400">TXT-Eintrag</span>
            <CopyButton value={k.dnsName.replace(/\.$/, "")} label="Name kopieren" />
          </div>
          <code className="block break-all font-mono text-zinc-800 dark:text-zinc-200">{k.dnsName}</code>
          <div className="flex items-center justify-between gap-2 pt-1">
            <span className="text-zinc-500 dark:text-zinc-400">Wert</span>
            <CopyButton value={k.dnsValue} label="Wert kopieren" />
          </div>
          <code className="block max-h-24 overflow-y-auto break-all font-mono text-zinc-800 dark:text-zinc-200">{k.dnsValue}</code>
        </div>
      )}

      {k.state === "pending" && (
        <p className="mt-2 text-xs text-zinc-500 dark:text-zinc-400">
          Lege den TXT-Eintrag beim DNS-Anbieter an (oder nutze „Automatisch einrichten“), prüfe ihn und aktiviere den Schlüssel danach.
        </p>
      )}
      {k.state === "retired" && k.removableAt && (
        <p className="mt-2 text-xs text-zinc-500 dark:text-zinc-400">
          {new Date(k.removableAt) > new Date() ? (
            <>
              Bitte den TXT-Eintrag <code className="font-mono">{k.dnsName}</code> noch bis {formatDateTime(k.removableAt)} im DNS lassen, damit kürzlich signierte Mails gültig bleiben.
            </>
          ) : (
            <>
              Der TXT-Eintrag <code className="font-mono">{k.dnsName}</code> wird nicht mehr gebraucht und kann beim DNS-Anbieter gelöscht werden.
            </>
          )}
          {k.autoDeleteAt && <> Der Schlüssel wird am {formatDate(k.autoDeleteAt)} automatisch entfernt.</>}
        </p>
      )}

      {check && (
        <div
          className={
            check.published
              ? "mt-2 flex items-start gap-1.5 text-xs text-emerald-700 dark:text-emerald-400"
              : "mt-2 flex items-start gap-1.5 text-xs text-amber-700 dark:text-amber-400"
          }
        >
          {check.published ? <CircleCheck className="mt-px h-3.5 w-3.5 shrink-0" /> : <CircleX className="mt-px h-3.5 w-3.5 shrink-0" />}
          <span>{check.published ? `Im DNS veröffentlicht (${check.name}).` : check.error || "Nicht gefunden."}</span>
        </div>
      )}
    </div>
  );
}

function CreateKeyDialog({
  open,
  domain,
  onClose,
  onCreated,
  hasPending,
}: {
  open: boolean;
  domain: string;
  onClose: () => void;
  onCreated: (k: DkimKey) => void;
  hasPending: (alg: DkimAlgorithm) => boolean;
}) {
  const toast = useToast();
  const [alg, setAlg] = useState<DkimAlgorithm>("rsa");
  const create = useMutation({
    mutationFn: () => mailAuthApi.createDkimKey(domain, alg),
    onSuccess: (k) => {
      onCreated(k);
      onClose();
    },
    onError: (err) => toast.error(err, "Erzeugen fehlgeschlagen"),
  });
  const blocked = hasPending(alg);
  return (
    <Modal
      open={open}
      onClose={onClose}
      title="Neuen DKIM-Schlüssel erzeugen"
      description="Der Schlüssel wird zunächst nur vorbereitet. Signiert wird damit erst nach dem Aktivieren."
      footer={
        <>
          <Button onClick={onClose}>Abbrechen</Button>
          <Button variant="primary" loading={create.isPending} disabled={blocked} onClick={() => create.mutate()}>
            Erzeugen
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <Segmented
          items={[
            { value: "rsa", label: "RSA 2048" },
            { value: "ed25519", label: "Ed25519" },
          ]}
          value={alg}
          onChange={setAlg}
        />
        <p className="text-sm text-zinc-600 dark:text-zinc-400">
          {alg === "rsa"
            ? "RSA wird von allen Empfängern verstanden. Ein neuer RSA-Schlüssel ersetzt beim Aktivieren den bisherigen (Schlüsselwechsel)."
            : "Ed25519 (RFC 8463) ist kurz und modern, wird aber noch nicht überall geprüft. Er wird zusätzlich zu RSA verwendet (Doppelsignatur)."}
        </p>
        {blocked && (
          <p className="text-sm text-amber-700 dark:text-amber-400">
            Es gibt bereits einen neuen {alg === "rsa" ? "RSA" : "Ed25519"}-Schlüssel, der noch nicht aktiviert ist. Aktiviere oder verwirf ihn zuerst.
          </p>
        )}
      </div>
    </Modal>
  );
}
