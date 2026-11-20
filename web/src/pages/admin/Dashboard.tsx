import { useMemo } from "react";
import { Link } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import {
  Area,
  AreaChart,
  Bar,
  BarChart,
  CartesianGrid,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import {
  ArrowRight,
  Bot,
  Clock,
  Database,
  Globe,
  Inbox,
  ListOrdered,
  Mail,
  Send,
  ShieldX,
  Sparkles,
  Bug,
  Ban,
  Users,
} from "lucide-react";
import { adminApi } from "@/api/endpoints";
import type { Dashboard as DashboardData, LogEntry } from "@/api/types";
import { useTheme } from "@/lib/theme";
import { formatBytes, formatDuration, formatHourLabel, formatNumber, formatRelative } from "@/lib/format";
import { cn } from "@/lib/utils";
import { Badge, Card, CardBody, CardHeader, EmptyState, ErrorState, PageHeader, Skeleton, StatTile } from "@/components/ui";
import { LevelBadge } from "./shared";

// Categorical slots 1–3 of the validated reference palette, stepped per mode.
const SERIES = {
  light: { received: "#2a78d6", sent: "#eb6834", spam: "#1baf7a", grid: "#e4e4e7", axis: "#71717a", surface: "#ffffff" },
  dark: { received: "#3987e5", sent: "#d95926", spam: "#199e70", grid: "#27272a", axis: "#a1a1aa", surface: "#18181b" },
};

const SERIES_LABELS = { received: "Empfangen", sent: "Gesendet", spam: "Spam" } as const;

export default function DashboardPage() {
  const q = useQuery({ queryKey: ["admin", "dashboard"], queryFn: adminApi.dashboard, refetchInterval: 30_000 });

  return (
    <>
      <PageHeader title="Dashboard" description="Überblick über Zustellung, Speicher und Systemzustand." />
      {q.isLoading ? (
        <DashboardSkeleton />
      ) : q.error ? (
        <Card>
          <ErrorState error={q.error} onRetry={() => void q.refetch()} />
        </Card>
      ) : q.data ? (
        <DashboardContent data={q.data} />
      ) : null}
    </>
  );
}

function DashboardSkeleton() {
  return (
    <div className="space-y-6">
      <div className="grid grid-cols-2 gap-4 md:grid-cols-3 xl:grid-cols-5">
        {Array.from({ length: 5 }, (_, i) => (
          <Skeleton key={i} className="h-[104px] rounded-xl" />
        ))}
      </div>
      <div className="grid gap-6 xl:grid-cols-3">
        <Skeleton className="h-80 rounded-xl xl:col-span-2" />
        <Skeleton className="h-80 rounded-xl" />
      </div>
    </div>
  );
}

function tileIcon(Icon: typeof Users, tone: string) {
  return (
    <span className={cn("flex h-8 w-8 items-center justify-center rounded-lg", tone)}>
      <Icon className="h-4 w-4" />
    </span>
  );
}

function DashboardContent({ data }: { data: DashboardData }) {
  const s = data.stats;
  const neutralTone = "bg-zinc-100 text-zinc-500 dark:bg-zinc-800 dark:text-zinc-400";
  return (
    <div className="space-y-6">
      <div className="grid grid-cols-2 gap-4 md:grid-cols-3 xl:grid-cols-5">
        <StatTile label="Konten" value={formatNumber(s.accounts)} icon={tileIcon(Users, neutralTone)} />
        <StatTile label="Domains" value={formatNumber(s.domains)} icon={tileIcon(Globe, neutralTone)} />
        <StatTile label="Nachrichten" value={formatNumber(s.messages)} icon={tileIcon(Mail, neutralTone)} />
        <StatTile label="Speicher" value={formatBytes(s.storageBytes)} icon={tileIcon(Database, neutralTone)} />
        <StatTile
          label="Warteschlange"
          value={formatNumber(s.queueSize)}
          icon={tileIcon(ListOrdered, s.queueSize > 0 ? "bg-amber-50 text-amber-600 dark:bg-amber-500/10 dark:text-amber-400" : neutralTone)}
          hint={
            <Link to="/admin/queue" className="inline-flex items-center gap-1 hover:text-indigo-600 dark:hover:text-indigo-300">
              Zur Warteschlange <ArrowRight className="h-3 w-3" />
            </Link>
          }
        />
      </div>

      <div className="grid gap-6 xl:grid-cols-3">
        <Card className="xl:col-span-2">
          <CardHeader title="Nachrichtenfluss" description="Letzte 24 Stunden, pro Stunde" />
          <CardBody className="pt-4">
            <div className="mb-4 grid grid-cols-2 gap-x-6 gap-y-3 sm:grid-cols-5">
              <MiniStat icon={Inbox} label="Empfangen" value={s.received24h} />
              <MiniStat icon={Send} label="Gesendet" value={s.sent24h} />
              <MiniStat icon={ShieldX} label="Spam" value={s.spam24h} />
              <MiniStat icon={Bug} label="Viren" value={s.virus24h} />
              <MiniStat icon={Ban} label="Abgelehnt" value={s.rejected24h} />
            </div>
            <TrafficChart series={data.series} />
          </CardBody>
        </Card>

        <div className="space-y-6">
          <StatusCard data={data} />
          <CategoriesCard categories={data.categories} />
        </div>
      </div>

      <RecentEvents events={data.recentEvents} />
    </div>
  );
}

function MiniStat({ icon: Icon, label, value }: { icon: typeof Inbox; label: string; value: number }) {
  return (
    <div>
      <div className="flex items-center gap-1.5 text-xs text-zinc-500 dark:text-zinc-400">
        <Icon className="h-3.5 w-3.5" /> {label}
      </div>
      <div className="mt-0.5 text-lg font-semibold tabular-nums text-zinc-900 dark:text-zinc-50">{formatNumber(value)}</div>
    </div>
  );
}

interface TooltipEntry {
  dataKey?: unknown;
  name?: unknown;
  value?: unknown;
  color?: string;
}

function ChartTooltip({ active, payload, label }: { active?: boolean; payload?: ReadonlyArray<TooltipEntry>; label?: unknown }) {
  if (!active || !payload?.length) return null;
  return (
    <div className="rounded-lg border border-zinc-200 bg-white px-3 py-2 text-xs shadow-pop dark:border-zinc-700 dark:bg-zinc-800">
      <div className="mb-1 font-medium text-zinc-900 dark:text-zinc-50">{String(label ?? "")}</div>
      {payload.map((p) => (
        <div key={String(p.dataKey)} className="flex items-center gap-2 py-0.5 text-zinc-600 dark:text-zinc-300">
          <span className="h-2 w-2 rounded-full" style={{ background: p.color }} />
          <span className="flex-1">{String(p.name ?? "")}</span>
          <span className="font-medium tabular-nums text-zinc-900 dark:text-zinc-50">{formatNumber(Number(p.value))}</span>
        </div>
      ))}
    </div>
  );
}

function TrafficChart({ series }: { series: DashboardData["series"] }) {
  const { resolved } = useTheme();
  const c = SERIES[resolved];
  const rows = useMemo(
    () =>
      series.hours.map((h, i) => ({
        hour: formatHourLabel(h),
        received: series.received[i] ?? 0,
        sent: series.sent[i] ?? 0,
        spam: series.spam[i] ?? 0,
      })),
    [series],
  );

  if (rows.length === 0) {
    return <EmptyState compact title="Noch keine Daten" description="Sobald Nachrichten fließen, erscheint hier der Verlauf." />;
  }

  const keys = ["received", "sent", "spam"] as const;

  return (
    <div>
      <div className="mb-3 flex flex-wrap gap-4 text-xs text-zinc-600 dark:text-zinc-300" aria-label="Legende">
        {keys.map((k) => (
          <span key={k} className="inline-flex items-center gap-1.5">
            <span className="h-2.5 w-2.5 rounded-sm" style={{ background: c[k] }} />
            {SERIES_LABELS[k]}
          </span>
        ))}
      </div>
      <div className="h-64">
        <ResponsiveContainer width="100%" height="100%">
          <AreaChart data={rows} margin={{ top: 4, right: 8, bottom: 0, left: -12 }}>
            <defs>
              {keys.map((k) => (
                <linearGradient key={k} id={`fill-${k}`} x1="0" y1="0" x2="0" y2="1">
                  <stop offset="0%" stopColor={c[k]} stopOpacity={0.22} />
                  <stop offset="100%" stopColor={c[k]} stopOpacity={0.02} />
                </linearGradient>
              ))}
            </defs>
            <CartesianGrid vertical={false} stroke={c.grid} strokeDasharray="3 3" />
            <XAxis dataKey="hour" tick={{ fill: c.axis, fontSize: 11 }} tickLine={false} axisLine={false} interval="preserveStartEnd" minTickGap={24} />
            <YAxis tick={{ fill: c.axis, fontSize: 11 }} tickLine={false} axisLine={false} allowDecimals={false} width={44} />
            <Tooltip content={(props) => <ChartTooltip active={props.active} payload={props.payload as ReadonlyArray<TooltipEntry> | undefined} label={props.label} />} cursor={{ stroke: c.axis, strokeDasharray: "3 3" }} />
            {keys.map((k) => (
              <Area
                key={k}
                type="monotone"
                dataKey={k}
                name={SERIES_LABELS[k]}
                stroke={c[k]}
                strokeWidth={2}
                fill={`url(#fill-${k})`}
                activeDot={{ r: 4, strokeWidth: 2, stroke: c.surface }}
                dot={false}
                isAnimationActive={false}
              />
            ))}
          </AreaChart>
        </ResponsiveContainer>
      </div>
    </div>
  );
}

function StatusCard({ data }: { data: DashboardData }) {
  const ai = data.ai;
  const aiState = !ai.enabled
    ? { tone: "neutral" as const, label: "Deaktiviert" }
    : ai.reachable
      ? { tone: "green" as const, label: "Erreichbar" }
      : { tone: "red" as const, label: "Nicht erreichbar" };
  return (
    <Card>
      <CardHeader title="Systemstatus" />
      <CardBody className="space-y-4 py-4">
        <div className="flex items-center justify-between gap-3">
          <div className="flex items-center gap-3">
            <span className="flex h-9 w-9 items-center justify-center rounded-lg bg-violet-50 text-violet-600 dark:bg-violet-500/10 dark:text-violet-300">
              <Bot className="h-[18px] w-[18px]" />
            </span>
            <div className="min-w-0">
              <div className="text-sm font-medium text-zinc-900 dark:text-zinc-100">KI-Endpunkt</div>
              <div className="truncate text-xs text-zinc-500 dark:text-zinc-400">{ai.model || "Kein Modell konfiguriert"}</div>
            </div>
          </div>
          <Badge tone={aiState.tone} dot>
            {aiState.label}
          </Badge>
        </div>
        <div className="grid grid-cols-2 gap-3 border-t border-zinc-100 pt-4 dark:border-zinc-800">
          <div>
            <div className="flex items-center gap-1.5 text-xs text-zinc-500 dark:text-zinc-400">
              <Clock className="h-3.5 w-3.5" /> Laufzeit
            </div>
            <div className="mt-0.5 text-sm font-semibold text-zinc-900 dark:text-zinc-100">{formatDuration(data.uptimeSeconds)}</div>
          </div>
          <div>
            <div className="text-xs text-zinc-500 dark:text-zinc-400">Version</div>
            <div className="mt-0.5 font-mono text-sm font-semibold text-zinc-900 dark:text-zinc-100">{data.version}</div>
          </div>
        </div>
      </CardBody>
    </Card>
  );
}

function CategoriesCard({ categories }: { categories: DashboardData["categories"] }) {
  const { resolved } = useTheme();
  const c = SERIES[resolved];
  const rows = [...categories].sort((a, b) => b.count - a.count).slice(0, 8);
  return (
    <Card>
      <CardHeader
        title="KI-Kategorien"
        description="Letzte 7 Tage"
        icon={<Sparkles className="h-4 w-4 text-violet-500" />}
      />
      <CardBody className="py-3">
        {rows.length === 0 ? (
          <EmptyState compact title="Keine Kategorien" description="Die KI hat noch keine Nachrichten eingeordnet." />
        ) : (
          <div style={{ height: rows.length * 34 + 8 }}>
            <ResponsiveContainer width="100%" height="100%">
              <BarChart data={rows} layout="vertical" margin={{ top: 4, right: 36, bottom: 4, left: 0 }} barCategoryGap={8}>
                <XAxis type="number" hide allowDecimals={false} />
                <YAxis
                  type="category"
                  dataKey="name"
                  width={96}
                  tick={{ fill: c.axis, fontSize: 12 }}
                  tickLine={false}
                  axisLine={false}
                />
                <Tooltip
                  cursor={{ fill: resolved === "dark" ? "rgba(255,255,255,0.04)" : "rgba(0,0,0,0.03)" }}
                  content={(props) => <ChartTooltip active={props.active} payload={props.payload as ReadonlyArray<TooltipEntry> | undefined} label={props.label} />}
                />
                <Bar
                  dataKey="count"
                  name="Nachrichten"
                  fill={c.received}
                  radius={[0, 4, 4, 0]}
                  isAnimationActive={false}
                  label={{ position: "right", fill: c.axis, fontSize: 11 }}
                />
              </BarChart>
            </ResponsiveContainer>
          </div>
        )}
      </CardBody>
    </Card>
  );
}

function RecentEvents({ events }: { events: LogEntry[] }) {
  return (
    <Card>
      <CardHeader
        title="Letzte Ereignisse"
        actions={
          <Link to="/admin/logs" className="inline-flex items-center gap-1 text-sm font-medium text-indigo-600 hover:text-indigo-500 dark:text-indigo-400">
            Alle Logs <ArrowRight className="h-4 w-4" />
          </Link>
        }
      />
      {events.length === 0 ? (
        <EmptyState compact title="Keine Ereignisse" />
      ) : (
        <ul className="divide-y divide-zinc-100 dark:divide-zinc-800">
          {events.slice(0, 10).map((e, i) => (
            <li key={i} className="flex items-start gap-3 px-5 py-3">
              <LevelBadge level={e.level} />
              <div className="min-w-0 flex-1">
                <p className="truncate text-sm text-zinc-800 dark:text-zinc-200">{e.message}</p>
                <p className="mt-0.5 text-xs text-zinc-500 dark:text-zinc-400">
                  <span className="font-mono">{e.component}</span>
                </p>
              </div>
              <time className="shrink-0 text-xs text-zinc-400" dateTime={e.time} title={e.time}>
                {formatRelative(e.time)}
              </time>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}
