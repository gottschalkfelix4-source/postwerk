import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowRight, Lock, ShieldAlert } from "lucide-react";
import { adminApi } from "@/api/endpoints";
import type { SystemAlias } from "@/api/mailauth";
import { isEmail } from "@/lib/utils";
import { Badge, Button, Card, CardBody, CardFooter, CardHeader, Field, Input, Td, Tr, useToast } from "@/components/ui";

/** Read-only table rows for the implicit postmaster@/abuse@ aliases. */
export function SystemAliasRows({ items }: { items: SystemAlias[] }) {
  return (
    <>
      {items.map((a) => (
        <Tr key={`system-${a.address}`} className="bg-zinc-50/60 dark:bg-zinc-900/40">
          <Td className="w-10 pr-0">
            <Lock className="h-3.5 w-3.5 text-zinc-400" aria-label="Nicht änderbar" />
          </Td>
          <Td className="font-medium text-zinc-700 dark:text-zinc-300">
            <span className="inline-flex items-center gap-2">
              {a.address}
              <Badge tone="blue">System</Badge>
            </span>
          </Td>
          <Td className="px-0 text-zinc-300 dark:text-zinc-600">
            <ArrowRight className="h-4 w-4" />
          </Td>
          <Td>
            {a.targets.length === 0 ? (
              <span className="text-xs text-amber-700 dark:text-amber-400">Kein Ziel – lege ein Admin-Konto an oder trage eine Adresse ein</span>
            ) : (
              <div className="flex flex-wrap gap-1.5">
                {a.targets.map((t) => (
                  <span key={t} className="rounded-md bg-zinc-100 px-2 py-0.5 text-xs text-zinc-700 ring-1 ring-inset ring-zinc-200 dark:bg-zinc-800 dark:text-zinc-300 dark:ring-zinc-700">
                    {t}
                  </span>
                ))}
                <span className="text-xs text-zinc-400">{a.configured ? "(Einstellung)" : "(erstes Admin-Konto)"}</span>
              </div>
            )}
          </Td>
          <Td className="whitespace-nowrap text-zinc-400">automatisch</Td>
          <Td />
        </Tr>
      ))}
    </>
  );
}

/** Target address for implicit postmaster@ and abuse@ mail (settings.mailauth). */
export function PostmasterCard() {
  const qc = useQueryClient();
  const toast = useToast();
  const settings = useQuery({ queryKey: ["admin", "settings"], queryFn: adminApi.settings, staleTime: 60_000 });
  const current = settings.data?.mailauth?.postmasterAddress ?? "";
  const [value, setValue] = useState(current);
  useEffect(() => setValue(current), [current]);

  const save = useMutation({
    mutationFn: () => adminApi.saveSettings({ mailauth: { postmasterAddress: value.trim().toLowerCase() } }),
    onSuccess: (s) => {
      qc.setQueryData(["admin", "settings"], s);
      void qc.invalidateQueries({ queryKey: ["admin", "aliases"] });
      toast.success("Gespeichert");
    },
    onError: (err) => toast.error(err, "Speichern fehlgeschlagen"),
  });

  const invalid = value.trim() !== "" && !isEmail(value.trim());
  return (
    <Card className="mt-6">
      <CardHeader
        icon={<ShieldAlert className="h-5 w-5 text-zinc-400" />}
        title="postmaster@ und abuse@"
        description="Jede Domain muss Mails an postmaster@ annehmen (RFC 5321), abuse@ ist üblich. Ohne eigenes Konto oder eigenen Alias werden sie automatisch zugestellt. Dorthin gehen auch DMARC- und TLS-Berichte."
      />
      <CardBody>
        <Field
          label="Zieladresse"
          help="Leer lassen, um an das erste Admin-Konto zuzustellen."
          error={invalid ? "Bitte eine gültige E-Mail-Adresse eingeben." : null}
        >
          {(id) => (
            <Input id={id} value={value} onChange={(e) => setValue(e.target.value)} placeholder="admin@beispiel.de" invalid={invalid} className="max-w-md" />
          )}
        </Field>
      </CardBody>
      <CardFooter>
        <Button variant="primary" size="sm" disabled={invalid || value.trim().toLowerCase() === current || !settings.data} loading={save.isPending} onClick={() => save.mutate()}>
          Speichern
        </Button>
      </CardFooter>
    </Card>
  );
}
