import { useState } from "react";
import { useSearchParams } from "react-router-dom";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { RefreshCw, ScrollText, Search } from "lucide-react";
import { adminApi } from "@/api/endpoints";
import type { LogEntry } from "@/api/types";
import { useDebounced, useStoredFlag } from "@/lib/hooks";
import { cn } from "@/lib/utils";
import { Button, Card, EmptyState, ErrorState, Input, PageHeader, Segmented, Select, Skeleton, Switch } from "@/components/ui";
import { levelTextClass } from "./shared";

type LevelFilter = "" | LogEntry["level"];

function formatLogTime(value: string): string {
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return value;
  const pad = (n: number, l = 2) => String(n).padStart(l, "0");
  return `${pad(d.getDate())}.${pad(d.getMonth() + 1)}. ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}.${pad(d.getMilliseconds(), 3)}`;
}

export default function LogsPage() {
  const [params] = useSearchParams();
  const [level, setLevel] = useState<LevelFilter>("");
  const [search, setSearch] = useState(params.get("q") ?? "");
  const [limit, setLimit] = useState("200");
  const [auto, setAuto] = useStoredFlag("postwerk.logs.autoRefresh", false);
  const [wrap, setWrap] = useStoredFlag("postwerk.logs.wrap", true);
  const q = useDebounced(search.trim(), 300);

  const logs = useQuery({
    queryKey: ["admin", "logs", level, q, limit],
    queryFn: () => adminApi.logs({ level: level || undefined, q: q || undefined, limit: Number(limit) }),
    refetchInterval: auto ? 5000 : false,
    placeholderData: keepPreviousData,
  });

  return (
    <>
      <PageHeader
        title="Logs"
        description="Ereignisprotokoll des Servers, neueste Einträge zuerst."
        actions={
          <Button icon={RefreshCw} onClick={() => void logs.refetch()} loading={logs.isFetching && !logs.isLoading && !auto}>
            Aktualisieren
          </Button>
        }
      />
      <Card className="overflow-hidden">
        <div className="flex flex-wrap items-center gap-3 border-b border-zinc-100 px-4 py-3 dark:border-zinc-800">
          <div className="w-full max-w-xs">
            <Input icon={Search} value={search} onChange={(e) => setSearch(e.target.value)} placeholder="Volltextsuche …" />
          </div>
          <Segmented<LevelFilter>
            value={level}
            onChange={setLevel}
            items={[
              { value: "", label: "Alle" },
              { value: "debug", label: "Debug" },
              { value: "info", label: "Info" },
              { value: "warn", label: "Warnung" },
              { value: "error", label: "Fehler" },
            ]}
          />
          <Select
            value={limit}
            onChange={(e) => setLimit(e.target.value)}
            options={["100", "200", "500", "1000"].map((v) => ({ value: v, label: `${v} Einträge` }))}
            className="w-36"
          />
          <div className="ml-auto flex items-center gap-5">
            <label className="flex items-center gap-2 text-sm text-zinc-600 dark:text-zinc-300">
              <Switch size="sm" checked={wrap} onChange={setWrap} aria-label="Zeilen umbrechen" />
              Umbrechen
            </label>
            <label className="flex items-center gap-2 text-sm text-zinc-600 dark:text-zinc-300">
              <Switch size="sm" checked={auto} onChange={setAuto} aria-label="Automatisch aktualisieren" />
              Auto-Refresh
              {auto && <span className="h-2 w-2 animate-pulse rounded-full bg-emerald-500" aria-hidden />}
            </label>
          </div>
        </div>

        {logs.isLoading ? (
          <div className="space-y-2 p-4">
            {Array.from({ length: 12 }, (_, i) => (
              <Skeleton key={i} className="h-4" />
            ))}
          </div>
        ) : logs.error ? (
          <ErrorState error={logs.error} onRetry={() => void logs.refetch()} />
        ) : !logs.data || logs.data.length === 0 ? (
          <EmptyState icon={ScrollText} title="Keine Einträge" description="Für diese Filter gibt es keine Log-Einträge." />
        ) : (
          <div className="max-h-[calc(100vh-17rem)] overflow-auto bg-zinc-50/60 font-mono text-[12.5px] leading-6 dark:bg-zinc-950/60">
            {logs.data.map((e, i) => (
              <div
                key={i}
                className={cn(
                  "flex gap-3 border-b border-zinc-100 px-4 py-1 hover:bg-white dark:border-zinc-900 dark:hover:bg-zinc-900",
                  e.level === "error" && "bg-red-50/50 dark:bg-red-500/[0.05]",
                  e.level === "warn" && "bg-amber-50/40 dark:bg-amber-500/[0.04]",
                )}
              >
                <span className="shrink-0 text-zinc-400 dark:text-zinc-500" title={e.time}>
                  {formatLogTime(e.time)}
                </span>
                <span className={cn("w-12 shrink-0 font-semibold uppercase", levelTextClass[e.level])}>{e.level}</span>
                <span className="w-28 shrink-0 truncate text-violet-600 dark:text-violet-400" title={e.component}>
                  {e.component}
                </span>
                <span className={cn("min-w-0 flex-1 text-zinc-800 dark:text-zinc-200", wrap ? "whitespace-pre-wrap break-words" : "truncate whitespace-nowrap")}>
                  {e.message}
                  {e.fields &&
                    Object.entries(e.fields).map(([k, v]) => (
                      <span key={k} className="ml-2 text-zinc-500 dark:text-zinc-400">
                        <span className="text-sky-700 dark:text-sky-400">{k}</span>={v}
                      </span>
                    ))}
                </span>
              </div>
            ))}
          </div>
        )}
        {logs.data && logs.data.length > 0 && (
          <div className="border-t border-zinc-100 px-4 py-2 text-xs text-zinc-500 dark:border-zinc-800 dark:text-zinc-400">
            {logs.data.length} Einträge angezeigt
          </div>
        )}
      </Card>
    </>
  );
}
