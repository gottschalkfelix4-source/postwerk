import { useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, AtSign, Globe, Info, KeyRound, Trash2, Users, Wand2 } from "lucide-react";
import { adminApi } from "@/api/endpoints";
import type { DnsRecord, Domain, SystemCheck } from "@/api/types";
import { formatDate } from "@/lib/format";
import { isEmail } from "@/lib/utils";
import {
  Badge,
  Button,
  Card,
  CardBody,
  CardFooter,
  CardHeader,
  CopyButton,
  EmptyState,
  ErrorState,
  Field,
  Input,
  Skeleton,
  SkeletonRows,
  StatTile,
  SwitchRow,
  TBody,
  THead,
  Table,
  Td,
  Th,
  Tr,
  useConfirm,
  useToast,
  type BadgeTone,
} from "@/components/ui";
import { DnsAutoSetup } from "./dns/DnsAutoSetup";

const typeTones: Record<DnsRecord["type"], BadgeTone> = {
  MX: "indigo",
  TXT: "teal",
  CNAME: "purple",
  A: "blue",
  AAAA: "blue",
  TLSA: "amber",
  SRV: "pink",
};

export default function DomainDetailPage() {
  const { name = "" } = useParams();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const toast = useToast();
  const confirm = useConfirm();

  const domains = useQuery({ queryKey: ["admin", "domains"], queryFn: adminApi.domains });
  const dns = useQuery({ queryKey: ["admin", "domain-dns", name], queryFn: () => adminApi.domainDns(name), enabled: !!name });
  const domain = domains.data?.find((d) => d.name === name);
  const [autoOpen, setAutoOpen] = useState(false);
  // Pre-fill the IPv4 from a cached system check (setup wizard), without fetching it here.
  const defaultIpv4 =
    qc.getQueryData<SystemCheck>(["admin", "system-check"])?.hostnameIps.find((ip) => /^\d{1,3}(\.\d{1,3}){3}$/.test(ip)) ?? null;

  const remove = useMutation({
    mutationFn: () => adminApi.deleteDomain(name),
    onSuccess: () => {
      toast.success("Domain gelöscht", name);
      void qc.invalidateQueries({ queryKey: ["admin", "domains"] });
      navigate("/admin/domains", { replace: true });
    },
    onError: (err) => toast.error(err, "Löschen fehlgeschlagen"),
  });

  return (
    <>
      <Link to="/admin/domains" className="mb-4 inline-flex items-center gap-1.5 text-sm text-zinc-500 hover:text-zinc-800 dark:text-zinc-400 dark:hover:text-zinc-200">
        <ArrowLeft className="h-4 w-4" /> Alle Domains
      </Link>

      <div className="mb-6 flex flex-wrap items-center justify-between gap-4">
        <div className="flex items-center gap-4">
          <span className="flex h-12 w-12 items-center justify-center rounded-xl bg-indigo-50 text-indigo-600 dark:bg-indigo-500/15 dark:text-indigo-300">
            <Globe className="h-6 w-6" />
          </span>
          <div>
            <h1 className="text-2xl font-semibold tracking-tight text-zinc-900 dark:text-zinc-50">{name}</h1>
            {domain && <p className="text-sm text-zinc-500 dark:text-zinc-400">Hinzugefügt am {formatDate(domain.createdAt)}</p>}
          </div>
        </div>
        <Button
          variant="danger-ghost"
          icon={Trash2}
          loading={remove.isPending}
          onClick={async () => {
            const ok = await confirm({
              title: `${name} löschen?`,
              message: "Die Domain und ihr DKIM-Schlüssel werden entfernt. Konten dieser Domain können dann keine Mails mehr empfangen.",
              confirmLabel: "Löschen",
              danger: true,
            });
            if (ok) remove.mutate();
          }}
        >
          Domain löschen
        </Button>
      </div>

      {domains.isLoading ? (
        <div className="mb-6 grid gap-4 sm:grid-cols-3">
          <Skeleton className="h-24 rounded-xl" />
          <Skeleton className="h-24 rounded-xl" />
          <Skeleton className="h-24 rounded-xl" />
        </div>
      ) : domains.error ? (
        <Card className="mb-6">
          <ErrorState error={domains.error} onRetry={() => void domains.refetch()} />
        </Card>
      ) : !domain ? (
        <Card className="mb-6">
          <EmptyState icon={Globe} title="Domain nicht gefunden" description={`„${name}“ ist auf diesem Server nicht angelegt.`} />
        </Card>
      ) : (
        <>
          <div className="mb-6 grid gap-4 sm:grid-cols-3">
            <StatTile label="Konten" value={domain.accountCount} icon={<Users className="h-4 w-4 text-zinc-400" />} />
            <StatTile label="Aliase" value={domain.aliasCount} icon={<AtSign className="h-4 w-4 text-zinc-400" />} />
            <StatTile
              label="DKIM-Selektor"
              value={<span className="font-mono text-xl">{domain.dkimSelector}</span>}
              icon={<KeyRound className="h-4 w-4 text-zinc-400" />}
            />
          </div>
          <CatchAllCard domain={domain} />
        </>
      )}

      <Card className="mt-6">
        <CardHeader
          title="Empfohlene DNS-Einträge"
          description="Trage diese Einträge beim DNS-Anbieter der Domain ein, damit Zustellung, DKIM, SPF und DMARC funktionieren."
          actions={
            <Button size="sm" variant="primary" icon={Wand2} onClick={() => setAutoOpen(true)} disabled={!name}>
              Automatisch einrichten
            </Button>
          }
        />
        {dns.isLoading ? (
          <SkeletonRows rows={5} cols={3} />
        ) : dns.error ? (
          <ErrorState error={dns.error} onRetry={() => void dns.refetch()} />
        ) : !dns.data || dns.data.length === 0 ? (
          <EmptyState compact title="Keine Einträge" />
        ) : (
          <Table>
            <THead>
              <tr>
                <Th className="w-20">Typ</Th>
                <Th>Name</Th>
                <Th>Wert</Th>
                <Th>Zweck</Th>
              </tr>
            </THead>
            <TBody>
              {dns.data.map((r, i) => (
                <Tr key={`${r.type}-${r.name}-${i}`}>
                  <Td className="align-top">
                    <Badge tone={typeTones[r.type] ?? "neutral"} className="font-mono">
                      {r.type}
                    </Badge>
                  </Td>
                  <Td className="align-top">
                    <div className="flex items-start gap-1">
                      <code className="break-all font-mono text-[13px] text-zinc-800 dark:text-zinc-200">{r.name}</code>
                      <CopyButton value={r.name} />
                    </div>
                  </Td>
                  <Td className="align-top">
                    <div className="flex max-w-xl items-start gap-1">
                      <code className="block max-h-24 flex-1 overflow-y-auto break-all rounded-md bg-zinc-50 px-2 py-1 font-mono text-xs leading-relaxed text-zinc-700 ring-1 ring-inset ring-zinc-200 dark:bg-zinc-950 dark:text-zinc-300 dark:ring-zinc-800">
                        {r.value}
                      </code>
                      <CopyButton value={r.value} />
                    </div>
                  </Td>
                  <Td className="align-top text-xs text-zinc-500 dark:text-zinc-400">{r.purpose}</Td>
                </Tr>
              ))}
            </TBody>
          </Table>
        )}
        <CardFooter className="justify-start">
          <p className="flex items-center gap-2 text-xs text-zinc-500 dark:text-zinc-400">
            <Info className="h-3.5 w-3.5 shrink-0" />
            DNS-Änderungen können je nach TTL einige Minuten bis Stunden brauchen, bis sie überall sichtbar sind.
          </p>
        </CardFooter>
      </Card>

      <DnsAutoSetup
        open={autoOpen}
        onClose={() => setAutoOpen(false)}
        domain={name}
        defaultIpv4={defaultIpv4}
        onApplied={() => {
          void qc.invalidateQueries({ queryKey: ["admin", "dns-check", name] });
          void qc.invalidateQueries({ queryKey: ["admin", "domain-dns", name] });
        }}
      />
    </>
  );
}

function CatchAllCard({ domain }: { domain: Domain }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [enabled, setEnabled] = useState(domain.catchAll !== null);
  const [target, setTarget] = useState(domain.catchAll ?? "");

  useEffect(() => {
    setEnabled(domain.catchAll !== null);
    setTarget(domain.catchAll ?? "");
  }, [domain.catchAll]);

  const save = useMutation({
    mutationFn: () => adminApi.updateDomain(domain.name, { catchAll: enabled ? target.trim() : null }),
    onSuccess: (d) => {
      qc.setQueryData<Domain[]>(["admin", "domains"], (list) => list?.map((x) => (x.name === d.name ? d : x)));
      toast.success("Catch-all gespeichert");
    },
    onError: (err) => toast.error(err, "Speichern fehlgeschlagen"),
  });

  const dirty = enabled !== (domain.catchAll !== null) || (enabled && target.trim() !== (domain.catchAll ?? ""));
  const invalid = enabled && !isEmail(target);

  return (
    <Card>
      <CardHeader title="Catch-all" description="Nachrichten an unbekannte Adressen dieser Domain an ein Postfach zustellen." />
      <CardBody className="space-y-4">
        <SwitchRow
          label="Catch-all aktivieren"
          description="Achtung: Catch-all-Postfächer ziehen erfahrungsgemäß viel Spam an."
          checked={enabled}
          onChange={setEnabled}
        />
        {enabled && (
          <Field label="Zieladresse" error={target && invalid ? "Bitte eine gültige E-Mail-Adresse eingeben." : null}>
            {(id) => (
              <Input id={id} value={target} onChange={(e) => setTarget(e.target.value)} placeholder={`postmaster@${domain.name}`} invalid={!!target && invalid} className="max-w-md" />
            )}
          </Field>
        )}
      </CardBody>
      <CardFooter>
        <Button variant="primary" size="sm" disabled={!dirty || invalid} loading={save.isPending} onClick={() => save.mutate()}>
          Speichern
        </Button>
      </CardFooter>
    </Card>
  );
}
