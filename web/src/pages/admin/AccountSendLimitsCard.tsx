import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Gauge } from "lucide-react";
import { limitsApi, type SendLimitOverride } from "@/api/limits";
import { formatDateTime } from "@/lib/format";
import { Badge, Button, Card, CardBody, CardFooter, CardHeader, Field, Input, Notice, Progress, Skeleton, SwitchRow, useToast } from "@/components/ui";

const empty: SendLimitOverride = { maxRecipientsPerMessage: null, maxMessagesPerHour: null, maxRecipientsPerDay: null, exempt: false };

type LimitKey = "maxRecipientsPerMessage" | "maxMessagesPerHour" | "maxRecipientsPerDay";

const fields: { key: LimitKey; label: string; max: number }[] = [
  { key: "maxRecipientsPerMessage", label: "Empfänger pro Nachricht", max: 1000 },
  { key: "maxMessagesPerHour", label: "Nachrichten pro Stunde", max: 1_000_000 },
  { key: "maxRecipientsPerDay", label: "Externe Empfänger pro Tag", max: 1_000_000 },
];

function limitText(v: number) {
  return v === 0 ? "unbegrenzt" : String(v);
}

function UsageRow({ label, used, limit, exempt }: { label: string; used: number; limit: number; exempt: boolean }) {
  const capped = !exempt && limit > 0;
  return (
    <div className="space-y-1">
      <div className="flex items-baseline justify-between text-sm">
        <span className="text-zinc-600 dark:text-zinc-400">{label}</span>
        <span className="tabular-nums font-medium text-zinc-900 dark:text-zinc-100">
          {used}
          <span className="font-normal text-zinc-500"> / {capped ? limit : "∞"}</span>
        </span>
      </div>
      {capped && <Progress value={Math.min(100, (used / limit) * 100)} />}
    </div>
  );
}

/** Admin account detail: current outbound volume and per-account send limit overrides. */
export function AccountSendLimitsCard({ accountId }: { accountId: number }) {
  const qc = useQueryClient();
  const toast = useToast();
  const key = ["admin", "account", accountId, "send-limits"];
  const q = useQuery({ queryKey: key, queryFn: () => limitsApi.accountLimits(accountId), refetchInterval: 60_000 });
  const [form, setForm] = useState<SendLimitOverride>(empty);
  useEffect(() => {
    if (q.data) setForm(q.data.override);
  }, [q.data]);
  const save = useMutation({
    mutationFn: (o: SendLimitOverride) => limitsApi.saveAccountLimits(accountId, o),
    onSuccess: (info) => {
      qc.setQueryData(key, info);
      toast.success("Sendelimits gespeichert");
    },
    onError: (err) => toast.error(err, "Speichern fehlgeschlagen"),
  });

  const info = q.data;
  const dirty = info ? JSON.stringify(form) !== JSON.stringify(info.override) : false;
  const invalid = fields.some((f) => {
    const v = form[f.key];
    return v !== null && (!Number.isInteger(v) || v < 0 || v > f.max);
  });

  return (
    <Card>
      <CardHeader
        title="Versand & Sendelimits"
        description="Ausgehende Mails an externe Empfänger in den letzten 60 Minuten bzw. 24 Stunden. Lokale Empfänger zählen nicht."
        icon={<Gauge className="h-4 w-4 text-zinc-400" />}
      />
      <CardBody className="space-y-5">
        {!info ? (
          <Skeleton className="h-32 rounded-lg" />
        ) : (
          <>
            {info.hitsToday > 0 && (
              <Notice tone="warning" title={`Sendelimit in den letzten 24 Stunden ${info.hitsToday === 1 ? "einmal" : `${info.hitsToday}-mal`} erreicht`}>
                Zuletzt {formatDateTime(info.lastHit)}. Ungewöhnlich viel Versand kann auf ein kompromittiertes Konto hindeuten.
              </Notice>
            )}
            <div className="grid gap-4 sm:grid-cols-2">
              <UsageRow label="Nachrichten (letzte Stunde)" used={info.usage.messagesLastHour} limit={info.effective.maxMessagesPerHour} exempt={info.effective.exempt} />
              <UsageRow label="Externe Empfänger (24 Std.)" used={info.usage.recipientsLastDay} limit={info.effective.maxRecipientsPerDay} exempt={info.effective.exempt} />
            </div>
            <SwitchRow
              label="Von Sendelimits ausnehmen"
              description="Für vertrauenswürdige Konten wie Newsletter- oder Systemabsender. Gilt auch für Admins nur, wenn hier eingeschaltet."
              checked={form.exempt}
              onChange={(v) => setForm((f) => ({ ...f, exempt: v }))}
            />
            {info.effective.exempt && <Badge tone="amber">Keine Limits für dieses Konto</Badge>}
            <div className="grid gap-4 sm:grid-cols-3">
              {fields.map((f) => (
                <Field key={f.key} label={f.label} help={`Leer = Standard (${limitText(info.defaults[f.key])}), 0 = unbegrenzt`}>
                  {(id) => (
                    <Input
                      id={id}
                      type="number"
                      min={0}
                      max={f.max}
                      disabled={form.exempt}
                      value={form[f.key] ?? ""}
                      placeholder={limitText(info.defaults[f.key])}
                      onChange={(e) =>
                        setForm((cur) => ({ ...cur, [f.key]: e.target.value === "" ? null : Math.floor(Number(e.target.value)) }))
                      }
                      className="tabular-nums"
                    />
                  )}
                </Field>
              ))}
            </div>
          </>
        )}
      </CardBody>
      <CardFooter className="justify-end gap-2">
        <Button size="sm" variant="ghost" disabled={!dirty || save.isPending} onClick={() => info && setForm(info.override)}>
          Verwerfen
        </Button>
        <Button size="sm" variant="primary" disabled={!dirty || invalid} loading={save.isPending} onClick={() => save.mutate(form)}>
          Speichern
        </Button>
      </CardFooter>
    </Card>
  );
}
