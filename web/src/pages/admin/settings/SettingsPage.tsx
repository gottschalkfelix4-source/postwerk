import { NavLink, Navigate, useParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Bot, FileStack, Lock, ServerCog, ShieldAlert, ShieldCheck, Bug, type LucideIcon } from "lucide-react";
import { adminApi } from "@/api/endpoints";
import type { Settings, SettingsUpdate } from "@/api/types";
import { cn } from "@/lib/utils";
import { Card, ErrorState, PageHeader, Skeleton, useToast } from "@/components/ui";
import { settingsSections, type SettingsSectionId } from "../crumbs";
import { AiSection, AntivirusSection, DmsSection, SecuritySection, ServerSection, SpamSection, TlsSection } from "./sections";

const icons: Record<SettingsSectionId, LucideIcon> = {
  server: ServerCog,
  tls: Lock,
  spam: ShieldAlert,
  antivirus: Bug,
  ai: Bot,
  dms: FileStack,
  security: ShieldCheck,
};

export interface SectionProps {
  settings: Settings;
  save: (update: SettingsUpdate) => void;
  saving: boolean;
}

export default function SettingsPage() {
  const { section } = useParams();
  const qc = useQueryClient();
  const toast = useToast();
  const q = useQuery({ queryKey: ["admin", "settings"], queryFn: adminApi.settings, staleTime: 60_000 });

  const mutation = useMutation({
    mutationFn: adminApi.saveSettings,
    onSuccess: (s) => {
      qc.setQueryData(["admin", "settings"], s);
      void qc.invalidateQueries({ queryKey: ["ai", "status"] });
      toast.success("Einstellungen gespeichert");
    },
    onError: (err) => toast.error(err, "Speichern fehlgeschlagen"),
  });

  if (!settingsSections.some((s) => s.id === section)) return <Navigate to="/admin/settings/server" replace />;
  const id = section as SettingsSectionId;

  const props: SectionProps | null = q.data
    ? { settings: q.data, save: (u) => mutation.mutate(u), saving: mutation.isPending }
    : null;

  return (
    <>
      <PageHeader title="Einstellungen" description="Serverweite Konfiguration. Jeder Abschnitt wird separat gespeichert." />
      <div className="grid gap-6 lg:grid-cols-[12rem_1fr]">
        <nav className="flex gap-1 overflow-x-auto pb-1 lg:flex-col lg:overflow-visible lg:pb-0" aria-label="Einstellungsbereiche">
          {settingsSections.map((s) => {
            const Icon = icons[s.id];
            return (
              <NavLink
                key={s.id}
                to={`/admin/settings/${s.id}`}
                className={({ isActive }) =>
                  cn(
                    "flex shrink-0 items-center gap-2.5 rounded-lg px-3 py-2 text-sm font-medium transition-colors",
                    isActive
                      ? "bg-white text-indigo-700 shadow-sm ring-1 ring-zinc-200 dark:bg-zinc-900 dark:text-indigo-300 dark:ring-zinc-800"
                      : "text-zinc-600 hover:bg-white/70 hover:text-zinc-900 dark:text-zinc-400 dark:hover:bg-zinc-900/60 dark:hover:text-zinc-100",
                  )
                }
              >
                {({ isActive }) => (
                  <>
                    <Icon className={cn("h-4 w-4", isActive ? "text-indigo-500" : "text-zinc-400")} />
                    {s.label}
                  </>
                )}
              </NavLink>
            );
          })}
        </nav>

        <div className="min-w-0">
          {q.isLoading ? (
            <Skeleton className="h-[28rem] rounded-xl" />
          ) : q.error || !props ? (
            <Card>
              <ErrorState error={q.error} onRetry={() => void q.refetch()} />
            </Card>
          ) : id === "server" ? (
            <ServerSection key={id} {...props} />
          ) : id === "tls" ? (
            <TlsSection key={id} {...props} />
          ) : id === "spam" ? (
            <SpamSection key={id} {...props} />
          ) : id === "antivirus" ? (
            <AntivirusSection key={id} {...props} />
          ) : id === "ai" ? (
            <AiSection key={id} {...props} />
          ) : id === "dms" ? (
            <DmsSection key={id} {...props} />
          ) : (
            <SecuritySection key={id} {...props} />
          )}
        </div>
      </div>
    </>
  );
}
