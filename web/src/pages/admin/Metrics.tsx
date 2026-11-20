import { Activity, ExternalLink, HeartPulse, Info } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { Badge, Card, CardBody, CardHeader, CopyButton, PageHeader } from "@/components/ui";

function CodeBlock({ code, title }: { code: string; title: string }) {
  return (
    <div className="overflow-hidden rounded-xl border border-zinc-200 dark:border-zinc-800">
      <div className="flex items-center justify-between border-b border-zinc-200 bg-zinc-50 px-4 py-2 dark:border-zinc-800 dark:bg-zinc-900">
        <span className="font-mono text-xs text-zinc-500 dark:text-zinc-400">{title}</span>
        <CopyButton value={code} label="Kopieren" />
      </div>
      <pre className="overflow-x-auto bg-zinc-950 p-4 font-mono text-[12.5px] leading-relaxed text-zinc-100">
        <code>{code}</code>
      </pre>
    </div>
  );
}

export default function MetricsPage() {
  const origin = window.location.origin;
  const host = window.location.host;
  const scheme = window.location.protocol.replace(":", "");

  const health = useQuery({
    queryKey: ["healthz"],
    queryFn: async () => {
      const res = await fetch("/healthz", { credentials: "include" });
      return res.ok;
    },
    refetchInterval: 30_000,
    retry: false,
  });

  const scrape = `scrape_configs:
  - job_name: "postwerk"
    scheme: ${scheme}
    metrics_path: /metrics
    # Only required if METRICS_TOKEN is set on the server
    authorization:
      type: Bearer
      credentials: "<METRICS_TOKEN>"
    static_configs:
      - targets: ["${host}"]`;

  const curl = `curl -H "Authorization: Bearer <METRICS_TOKEN>" ${origin}/metrics`;

  return (
    <>
      <PageHeader title="Metriken" description="Prometheus-kompatible Kennzahlen für Monitoring und Alarmierung." />
      <div className="grid gap-6 xl:grid-cols-3">
        <div className="space-y-6 xl:col-span-2">
          <Card>
            <CardHeader
              title="Prometheus-Endpunkt"
              icon={<Activity className="h-4 w-4 text-indigo-500" />}
              actions={
                <a
                  href="/metrics"
                  target="_blank"
                  rel="noreferrer"
                  className="inline-flex items-center gap-1 text-sm font-medium text-indigo-600 hover:text-indigo-500 dark:text-indigo-400"
                >
                  Öffnen <ExternalLink className="h-3.5 w-3.5" />
                </a>
              }
            />
            <CardBody className="space-y-5">
              <p className="text-sm leading-relaxed text-zinc-600 dark:text-zinc-400">
                Postwerk stellt Metriken im Prometheus-Textformat unter{" "}
                <code className="rounded bg-zinc-100 px-1.5 py-0.5 font-mono text-[13px] dark:bg-zinc-800">{origin}/metrics</code> bereit.
                Ist auf dem Server die Variable <code className="font-mono text-[13px]">METRICS_TOKEN</code> gesetzt, muss der Scraper das Token als Bearer-Token mitsenden.
              </p>
              <CodeBlock title="prometheus.yml" code={scrape} />
              <CodeBlock title="Schnelltest" code={curl} />
            </CardBody>
          </Card>
        </div>
        <div className="space-y-6">
          <Card>
            <CardHeader title="Health-Check" icon={<HeartPulse className="h-4 w-4 text-zinc-400" />} />
            <CardBody className="space-y-3">
              <div className="flex items-center justify-between">
                <code className="font-mono text-sm text-zinc-700 dark:text-zinc-300">GET /healthz</code>
                {health.isLoading ? (
                  <Badge>Prüfe …</Badge>
                ) : health.data ? (
                  <Badge tone="green" dot>
                    OK
                  </Badge>
                ) : (
                  <Badge tone="red" dot>
                    Fehler
                  </Badge>
                )}
              </div>
              <p className="text-xs text-zinc-500 dark:text-zinc-400">Geeignet für Load-Balancer, Docker-Healthchecks und Uptime-Monitore.</p>
            </CardBody>
          </Card>
          <Card>
            <CardBody className="flex gap-3 text-sm text-zinc-600 dark:text-zinc-400">
              <Info className="mt-0.5 h-4 w-4 shrink-0 text-indigo-500" />
              <p>
                Tipp: Mit Grafana lassen sich aus diesen Metriken Dashboards für Zustellraten, Spam-Quote, Warteschlangenlänge und
                KI-Latenzen bauen.
              </p>
            </CardBody>
          </Card>
        </div>
      </div>
    </>
  );
}
