import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Eraser } from "lucide-react";
import { mailAuthApi } from "@/api/mailauth";
import { Button, CopyButton, useToast } from "@/components/ui";

/** TXT records of deleted DKIM keys that can be removed from DNS. */
export function DkimDnsCleanup({ domain }: { domain: string }) {
  const qc = useQueryClient();
  const toast = useToast();
  const key = ["admin", "dkim-dns-cleanup", domain];
  const q = useQuery({ queryKey: key, queryFn: () => mailAuthApi.dkimDnsCleanup(domain), enabled: !!domain });
  const done = useMutation({
    mutationFn: (selector: string) => mailAuthApi.dkimDnsCleanupDone(domain, selector),
    onSuccess: () => void qc.invalidateQueries({ queryKey: key }),
    onError: (err) => toast.error(err, "Speichern fehlgeschlagen"),
  });
  if (!q.data || q.data.length === 0) return null;
  return (
    <div className="rounded-lg border border-amber-200 bg-amber-50/50 p-4 dark:border-amber-500/25 dark:bg-amber-500/[0.06]">
      <div className="flex items-center gap-2 text-sm font-medium text-amber-900 dark:text-amber-100">
        <Eraser className="h-4 w-4 text-amber-500" />
        Alte DKIM-Einträge im DNS entfernen
      </div>
      <p className="mt-1 text-xs text-amber-900/80 dark:text-amber-100/80">
        Diese Schlüssel wurden gelöscht. Ihre TXT-Einträge werden nicht mehr gebraucht und können beim DNS-Anbieter gelöscht werden – oder über „Automatisch einrichten“.
      </p>
      <ul className="mt-3 space-y-2">
        {q.data.map((c) => (
          <li key={c.selector} className="flex flex-wrap items-center justify-between gap-2">
            <code className="break-all font-mono text-xs text-zinc-800 dark:text-zinc-200">{c.dnsName}</code>
            <div className="flex items-center gap-1">
              <CopyButton value={c.dnsName.replace(/\.$/, "")} label="Name kopieren" />
              <Button size="sm" variant="ghost" icon={Check} loading={done.isPending && done.variables === c.selector} onClick={() => done.mutate(c.selector)}>
                Erledigt
              </Button>
            </div>
          </li>
        ))}
      </ul>
    </div>
  );
}
