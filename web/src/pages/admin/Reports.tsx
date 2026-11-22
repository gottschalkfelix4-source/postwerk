import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { FileBarChart, Lock, ShieldCheck, Trash2 } from "lucide-react";
import { adminApi } from "@/api/endpoints";
import { mailAuthApi, type DmarcReport, type DmarcSource, type TlsFailureGroup } from "@/api/mailauth";
import { formatDate, formatDateTime, formatNumber } from "@/lib/format";
import { cn } from "@/lib/utils";
import {
  Badge,
  Button,
  Card,
  CardHeader,
  EmptyState,
  ErrorState,
  Modal,
  PageHeader,
  Segmented,
  Select,
  SkeletonRows,
  StatTile,
  Switch,
  TBody,
  THead,
  Table,
  TableToolbar,
  Tabs,
  Td,
  Th,
  Tr,
  useConfirm,
  useToast,
} from "@/components/ui";

type Tab = "dmarc" | "tls";
type Period = "7" | "30" | "90";

const tlsResultLabels: Record<string, string> = {
  "starttls-not-supported": "STARTTLS nicht angeboten",
  "certificate-host-mismatch": "Zertifikat passt nicht zum Hostnamen",
  "certificate-expired": "Zertifikat abgelaufen",
  "certificate-not-trusted": "Zertifikat nicht vertrauenswürdig",
  "validation-failure": "Zertifikatsprüfung fehlgeschlagen",
  "tlsa-invalid": "TLSA-Eintrag ungültig (DANE)",
  "dnssec-invalid": "DNSSEC ungültig",
  "dane-required": "DANE erforderlich, aber nicht möglich",
  "sts-policy-fetch-error": "MTA-STS-Richtlinie nicht abrufbar",
  "sts-policy-invalid": "MTA-STS-Richtlinie ungültig",
  "sts-webpki-invalid": "MTA-STS: HTTPS-Zertifikat ungültig",
};

const percent = (part: number, total: number) => (total > 0 ? `${Math.round((part / total) * 1000) / 10} %` : "–");

export default function ReportsPage() {
  const [tab, setTab] = useState<Tab>("dmarc");
  const [domain, setDomain] = useState("");
  const [period, setPeriod] = useState<Period>("30");
  const domains = useQuery({ queryKey: ["admin", "domains"], queryFn: adminApi.domains });
  const query = { domain: domain || undefined, days: Number(period) };

  return (
    <>
      <PageHeader
        title="Berichte"
        description="DMARC- und TLS-Berichte, die andere Mailserver an postmaster@ deiner Domains schicken."
      />
      <DeliveryCard />
      <Tabs
        className="mb-4"
        items={[
          { value: "dmarc", label: "DMARC" },
          { value: "tls", label: "TLS-Verschlüsselung" },
        ]}
        value={tab}
        onChange={setTab}
      />
      <TableToolbar className="mb-4 rounded-xl border border-zinc-200 bg-white dark:border-zinc-800 dark:bg-zinc-900">
        <Select
          className="w-full max-w-xs"
          size="sm"
          value={domain}
          onChange={(e) => setDomain(e.target.value)}
          options={[{ value: "", label: "Alle Domains" }, ...(domains.data ?? []).map((d) => ({ value: d.name, label: d.name }))]}
          aria-label="Domain"
        />
        <Segmented
          size="sm"
          items={[
            { value: "7", label: "7 Tage" },
            { value: "30", label: "30 Tage" },
            { value: "90", label: "90 Tage" },
          ]}
          value={period}
          onChange={setPeriod}
        />
      </TableToolbar>
      {tab === "dmarc" ? <DmarcTab query={query} /> : <TlsTab query={query} />}
    </>
  );
}

/** Whether evaluated report mails still land in the postmaster mailbox (settings.mailauth). */
function DeliveryCard() {
  const qc = useQueryClient();
  const toast = useToast();
  const settings = useQuery({ queryKey: ["admin", "settings"], queryFn: adminApi.settings, staleTime: 60_000 });
  const skip = settings.data?.mailauth?.skipReportDelivery ?? false;
  const save = useMutation({
    mutationFn: (value: boolean) => adminApi.saveSettings({ mailauth: { skipReportDelivery: value } }),
    onSuccess: (s) => {
      qc.setQueryData(["admin", "settings"], s);
      toast.success("Gespeichert");
    },
    onError: (err) => toast.error(err, "Speichern fehlgeschlagen"),
  });

  return (
    <Card className="mb-4">
      <div className="flex items-start justify-between gap-4 p-4 sm:px-5">
        <label htmlFor="skip-report-delivery" className="min-w-0 cursor-pointer">
          <div className="text-sm font-medium text-zinc-900 dark:text-zinc-100">Berichtsmails nicht zustellen</div>
          <p className="mt-0.5 text-sm text-zinc-500 dark:text-zinc-400">
            Vollständig ausgewertete Berichte erscheinen nur noch hier und landen nicht mehr im Postfach von postmaster@. Berichte, die sich nicht auswerten lassen, werden weiterhin zugestellt.
          </p>
        </label>
        <Switch
          id="skip-report-delivery"
          checked={save.isPending ? !!save.variables : skip}
          onChange={(v) => save.mutate(v)}
          disabled={!settings.data || save.isPending}
          aria-label="Berichtsmails nicht zustellen"
        />
      </div>
    </Card>
  );
}

function NoReports({ kind }: { kind: Tab }) {
  return (
    <Card>
      <EmptyState
        icon={FileBarChart}
        title="Noch keine Berichte"
        description={
          kind === "dmarc"
            ? "Große Anbieter schicken täglich DMARC-Berichte, sobald dein DMARC-Eintrag eine rua-Adresse enthält (siehe empfohlene DNS-Einträge der Domain)."
            : "TLS-Berichte kommen, sobald der TXT-Eintrag _smtp._tls (TLS-RPT) gesetzt ist."
        }
      />
    </Card>
  );
}

function DmarcTab({ query }: { query: { domain?: string; days: number } }) {
  const qc = useQueryClient();
  const toast = useToast();
  const confirm = useConfirm();
  const [openId, setOpenId] = useState<number | null>(null);
  const q = useQuery({ queryKey: ["admin", "reports", "dmarc", query], queryFn: () => mailAuthApi.dmarcReports(query) });

  const remove = useMutation({
    mutationFn: mailAuthApi.deleteDmarcReport,
    onSuccess: () => {
      toast.success("Bericht gelöscht");
      void qc.invalidateQueries({ queryKey: ["admin", "reports", "dmarc"] });
    },
    onError: (err) => toast.error(err, "Löschen fehlgeschlagen"),
  });

  const totals = useMemo(() => {
    const s = q.data?.summary ?? [];
    return s.reduce((a, d) => ({ reports: a.reports + d.reports, messages: a.messages + d.messages, failed: a.failed + d.failed }), { reports: 0, messages: 0, failed: 0 });
  }, [q.data]);

  if (q.isLoading) return <Card><SkeletonRows rows={6} cols={5} /></Card>;
  if (q.error) return <Card><ErrorState error={q.error} onRetry={() => void q.refetch()} /></Card>;
  if (!q.data || q.data.reports.length === 0) return <NoReports kind="dmarc" />;

  return (
    <div className="space-y-6">
      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <StatTile label="Berichte" value={formatNumber(totals.reports)} />
        <StatTile label="Nachrichten" value={formatNumber(totals.messages)} hint="von Empfängern gemeldet" />
        <StatTile label="DMARC bestanden" value={percent(totals.messages - totals.failed, totals.messages)} />
        <StatTile
          label="Fehlgeschlagen"
          value={<span className={totals.failed > 0 ? "text-red-600 dark:text-red-400" : undefined}>{formatNumber(totals.failed)}</span>}
          hint="weder SPF noch DKIM passend"
        />
      </div>

      <Card>
        <CardHeader title="Pro Domain" />
        <Table>
          <THead>
            <tr>
              <Th>Domain</Th>
              <Th className="text-right">Berichte</Th>
              <Th className="text-right">Nachrichten</Th>
              <Th className="text-right">Bestanden</Th>
              <Th className="text-right">Fehlgeschlagen</Th>
              <Th>Melder</Th>
            </tr>
          </THead>
          <TBody>
            {q.data.summary.map((d) => (
              <Tr key={d.domain}>
                <Td className="font-medium text-zinc-900 dark:text-zinc-100">{d.domain}</Td>
                <Td className="text-right tabular-nums">{formatNumber(d.reports)}</Td>
                <Td className="text-right tabular-nums">{formatNumber(d.messages)}</Td>
                <Td className="text-right tabular-nums">{percent(d.passed, d.messages)}</Td>
                <Td className={cn("text-right tabular-nums", d.failed > 0 && "font-medium text-red-600 dark:text-red-400")}>{formatNumber(d.failed)}</Td>
                <Td className="text-zinc-500 dark:text-zinc-400">{d.reporters.join(", ")}</Td>
              </Tr>
            ))}
          </TBody>
        </Table>
      </Card>

      <Card>
        <CardHeader title="Absender" description="Server, die im Namen deiner Domains Mails verschickt haben. Fehlgeschlagene zuerst – sie sind entweder Fälschungen oder fehlen noch im SPF-Eintrag bzw. signieren nicht mit DKIM." />
        <Table>
          <THead>
            <tr>
              <Th>IP-Adresse</Th>
              <Th>Domain</Th>
              <Th className="text-right">Nachrichten</Th>
              <Th className="text-right">SPF</Th>
              <Th className="text-right">DKIM</Th>
              <Th>DMARC</Th>
              <Th>Melder</Th>
            </tr>
          </THead>
          <TBody>
            {q.data.sources.map((s) => (
              <SourceRow key={`${s.domain}-${s.sourceIp}`} s={s} />
            ))}
          </TBody>
        </Table>
      </Card>

      <Card>
        <CardHeader title="Letzte Berichte" />
        <Table>
          <THead>
            <tr>
              <Th>Zeitraum</Th>
              <Th>Melder</Th>
              <Th>Domain</Th>
              <Th>Richtlinie</Th>
              <Th className="text-right">Nachrichten</Th>
              <Th className="text-right">Fehlgeschlagen</Th>
              <Th className="w-12" />
            </tr>
          </THead>
          <TBody>
            {q.data.reports.map((r) => (
              <Tr key={r.id} onClick={() => setOpenId(r.id)} className="cursor-pointer">
                <Td className="whitespace-nowrap">{formatDate(r.begin)} – {formatDate(r.end)}</Td>
                <Td>{r.orgName}</Td>
                <Td>{r.domain}</Td>
                <Td>{r.policy.p ? <Badge tone={r.policy.p === "reject" ? "green" : r.policy.p === "quarantine" ? "blue" : "amber"}>p={r.policy.p}</Badge> : "–"}</Td>
                <Td className="text-right tabular-nums">{formatNumber(r.messages)}</Td>
                <Td className={cn("text-right tabular-nums", r.failed > 0 && "font-medium text-red-600 dark:text-red-400")}>{formatNumber(r.failed)}</Td>
                <Td className="text-right" onClick={(e) => e.stopPropagation()}>
                  <Button
                    size="icon-sm"
                    variant="danger-ghost"
                    icon={Trash2}
                    aria-label="Bericht löschen"
                    onClick={async () => {
                      if (await confirm({ title: "Bericht löschen?", message: `Bericht ${r.reportId} von ${r.orgName}.`, confirmLabel: "Löschen", danger: true })) remove.mutate(r.id);
                    }}
                  />
                </Td>
              </Tr>
            ))}
          </TBody>
        </Table>
      </Card>
      <DmarcReportDialog id={openId} onClose={() => setOpenId(null)} />
    </div>
  );
}

function SourceRow({ s }: { s: DmarcSource }) {
  const failed = s.count - s.dmarcPass;
  return (
    <Tr className={failed > 0 ? "bg-red-50/70 dark:bg-red-500/5" : undefined}>
      <Td className="font-mono text-[13px]">{s.sourceIp}</Td>
      <Td>{s.domain}</Td>
      <Td className="text-right tabular-nums">{formatNumber(s.count)}</Td>
      <Td className="text-right tabular-nums">{percent(s.spfPass, s.count)}</Td>
      <Td className="text-right tabular-nums">{percent(s.dkimPass, s.count)}</Td>
      <Td>
        {failed === 0 ? (
          <Badge tone="green" icon={ShieldCheck}>bestanden</Badge>
        ) : (
          <div className="flex flex-wrap gap-1">
            <Badge tone="red">{formatNumber(failed)} fehlgeschlagen</Badge>
            {s.rejected > 0 && <Badge tone="neutral">{formatNumber(s.rejected)} abgelehnt</Badge>}
            {s.quarantined > 0 && <Badge tone="amber">{formatNumber(s.quarantined)} im Spam</Badge>}
          </div>
        )}
      </Td>
      <Td className="text-zinc-500 dark:text-zinc-400">{s.reporters.join(", ")}</Td>
    </Tr>
  );
}

function DmarcReportDialog({ id, onClose }: { id: number | null; onClose: () => void }) {
  const q = useQuery({ queryKey: ["admin", "reports", "dmarc-detail", id], queryFn: () => mailAuthApi.dmarcReport(id as number), enabled: id !== null });
  const r: DmarcReport | undefined = q.data;
  return (
    <Modal open={id !== null} onClose={onClose} size="xl" title={r ? `Bericht von ${r.orgName}` : "Bericht"} description={r ? `${r.domain} · ${formatDateTime(r.begin)} – ${formatDateTime(r.end)} · ID ${r.reportId}` : undefined}>
      {q.isLoading ? (
        <SkeletonRows rows={4} cols={4} />
      ) : q.error ? (
        <ErrorState error={q.error} />
      ) : r ? (
        <div className="space-y-3">
          <p className="text-sm text-zinc-600 dark:text-zinc-400">
            Veröffentlichte Richtlinie: p={r.policy.p || "–"}
            {r.policy.sp && `, sp=${r.policy.sp}`}
            {r.policy.adkim && `, adkim=${r.policy.adkim}`}
            {r.policy.aspf && `, aspf=${r.policy.aspf}`}
            {r.policy.pct && `, pct=${r.policy.pct}`}
          </p>
          <div className="max-h-[60vh] overflow-auto rounded-lg border border-zinc-200 dark:border-zinc-800">
            <Table>
              <THead>
                <tr>
                  <Th>IP-Adresse</Th>
                  <Th className="text-right">Anzahl</Th>
                  <Th>Behandlung</Th>
                  <Th>DKIM</Th>
                  <Th>SPF</Th>
                  <Th>Absender</Th>
                </tr>
              </THead>
              <TBody>
                {(r.records ?? []).map((rec, i) => {
                  const ok = rec.dkim === "pass" || rec.spf === "pass";
                  return (
                    <Tr key={i} className={ok ? undefined : "bg-red-50/70 dark:bg-red-500/5"}>
                      <Td className="font-mono text-[13px]">{rec.sourceIp}</Td>
                      <Td className="text-right tabular-nums">{formatNumber(rec.count)}</Td>
                      <Td>{rec.disposition || "–"}</Td>
                      <Td>
                        <Badge tone={rec.dkim === "pass" ? "green" : "red"}>{rec.dkim || "–"}</Badge>
                        {rec.dkimResults.map((d, j) => (
                          <div key={j} className="mt-1 text-xs text-zinc-500">{d.domain}{d.selector ? ` (${d.selector})` : ""}: {d.result}</div>
                        ))}
                      </Td>
                      <Td>
                        <Badge tone={rec.spf === "pass" ? "green" : "red"}>{rec.spf || "–"}</Badge>
                        {rec.spfResults.map((d, j) => (
                          <div key={j} className="mt-1 text-xs text-zinc-500">{d.domain}: {d.result}</div>
                        ))}
                      </Td>
                      <Td className="text-xs text-zinc-500">{rec.envelopeFrom || rec.headerFrom}</Td>
                    </Tr>
                  );
                })}
              </TBody>
            </Table>
          </div>
        </div>
      ) : null}
    </Modal>
  );
}

function TlsTab({ query }: { query: { domain?: string; days: number } }) {
  const qc = useQueryClient();
  const toast = useToast();
  const confirm = useConfirm();
  const q = useQuery({ queryKey: ["admin", "reports", "tls", query], queryFn: () => mailAuthApi.tlsReports(query) });
  const remove = useMutation({
    mutationFn: mailAuthApi.deleteTlsReport,
    onSuccess: () => {
      toast.success("Bericht gelöscht");
      void qc.invalidateQueries({ queryKey: ["admin", "reports", "tls"] });
    },
    onError: (err) => toast.error(err, "Löschen fehlgeschlagen"),
  });
  const totals = useMemo(
    () => (q.data?.summary ?? []).reduce((a, d) => ({ reports: a.reports + d.reports, success: a.success + d.success, failure: a.failure + d.failure }), { reports: 0, success: 0, failure: 0 }),
    [q.data],
  );

  if (q.isLoading) return <Card><SkeletonRows rows={6} cols={5} /></Card>;
  if (q.error) return <Card><ErrorState error={q.error} onRetry={() => void q.refetch()} /></Card>;
  if (!q.data || q.data.reports.length === 0) return <NoReports kind="tls" />;

  return (
    <div className="space-y-6">
      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <StatTile label="Berichte" value={formatNumber(totals.reports)} />
        <StatTile label="Erfolgreiche Sitzungen" value={formatNumber(totals.success)} hint="verschlüsselt zugestellt" />
        <StatTile
          label="Fehlgeschlagene Sitzungen"
          value={<span className={totals.failure > 0 ? "text-red-600 dark:text-red-400" : undefined}>{formatNumber(totals.failure)}</span>}
        />
        <StatTile label="Fehlerquote" value={percent(totals.failure, totals.success + totals.failure)} />
      </div>

      <Card>
        <CardHeader title="Fehler nach Typ" description="Probleme, auf die andere Server beim verschlüsselten Zustellen an dich gestoßen sind." />
        {q.data.failures.length === 0 ? (
          <EmptyState icon={Lock} title="Keine TLS-Fehler" description="Alle gemeldeten Sitzungen waren erfolgreich verschlüsselt." />
        ) : (
          <Table>
            <THead>
              <tr>
                <Th>Fehler</Th>
                <Th>Domain</Th>
                <Th>Mailserver</Th>
                <Th className="text-right">Sitzungen</Th>
                <Th>Einliefernde IPs</Th>
                <Th>Melder</Th>
                <Th>Zuletzt</Th>
              </tr>
            </THead>
            <TBody>
              {q.data.failures.map((f) => (
                <FailureRow key={`${f.domain}-${f.resultType}-${f.receivingMxHostname}`} f={f} />
              ))}
            </TBody>
          </Table>
        )}
      </Card>

      <Card>
        <CardHeader title="Letzte Berichte" />
        <Table>
          <THead>
            <tr>
              <Th>Zeitraum</Th>
              <Th>Melder</Th>
              <Th>Domain</Th>
              <Th>Richtlinie</Th>
              <Th className="text-right">Erfolgreich</Th>
              <Th className="text-right">Fehlgeschlagen</Th>
              <Th className="w-12" />
            </tr>
          </THead>
          <TBody>
            {q.data.reports.map((r) => (
              <Tr key={r.id}>
                <Td className="whitespace-nowrap">{formatDate(r.begin)} – {formatDate(r.end)}</Td>
                <Td>{r.orgName}</Td>
                <Td>{r.domain}</Td>
                <Td><Badge tone={r.policyType === "sts" ? "blue" : r.policyType === "tlsa" ? "purple" : "neutral"}>{r.policyType || "–"}</Badge></Td>
                <Td className="text-right tabular-nums">{formatNumber(r.successCount)}</Td>
                <Td className={cn("text-right tabular-nums", r.failureCount > 0 && "font-medium text-red-600 dark:text-red-400")}>{formatNumber(r.failureCount)}</Td>
                <Td className="text-right">
                  <Button
                    size="icon-sm"
                    variant="danger-ghost"
                    icon={Trash2}
                    aria-label="Bericht löschen"
                    onClick={async () => {
                      if (await confirm({ title: "Bericht löschen?", message: `Bericht ${r.reportId} von ${r.orgName}.`, confirmLabel: "Löschen", danger: true })) remove.mutate(r.id);
                    }}
                  />
                </Td>
              </Tr>
            ))}
          </TBody>
        </Table>
      </Card>
    </div>
  );
}

function FailureRow({ f }: { f: TlsFailureGroup }) {
  return (
    <Tr className="bg-red-50/70 dark:bg-red-500/5">
      <Td>
        <div className="font-medium text-zinc-900 dark:text-zinc-100">{tlsResultLabels[f.resultType] ?? f.resultType}</div>
        <div className="font-mono text-xs text-zinc-500">{f.resultType}</div>
      </Td>
      <Td>{f.domain}</Td>
      <Td className="font-mono text-[13px]">{f.receivingMxHostname || "–"}</Td>
      <Td className="text-right tabular-nums font-medium text-red-600 dark:text-red-400">{formatNumber(f.sessions)}</Td>
      <Td className="font-mono text-xs text-zinc-500">{f.sendingIps.slice(0, 3).join(", ")}{f.sendingIps.length > 3 && ` +${f.sendingIps.length - 3}`}</Td>
      <Td className="text-zinc-500 dark:text-zinc-400">{f.reporters.join(", ")}</Td>
      <Td className="whitespace-nowrap text-zinc-500">{formatDate(f.lastSeen)}</Td>
    </Tr>
  );
}
