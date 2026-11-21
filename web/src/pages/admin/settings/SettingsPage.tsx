import { NavLink, Navigate, useParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { BellRing, Bot, Building2, FileStack, Gauge, KeyRound, Lock, RefreshCw, Send, ServerCog, ShieldAlert, ShieldCheck, Bug, Waypoints, type LucideIcon } from "lucide-react";
import { LdapSection, OidcSection } from "./directory";
import { adminApi } from "@/api/endpoints";
import type { Settings, SettingsUpdate } from "@/api/types";
import { cn } from "@/lib/utils";
import { Card, ErrorState, PageHeader, Skeleton, useToast } from "@/components/ui";
import { settingsSections, type SettingsSectionId } from "../crumbs";
import { dynIpKeys } from "@/pages/admin/dynip/DynIpParts";
import { EncryptionCard } from "@/pages/admin/EncryptionParts";
import {
  AiSection,
  AntivirusSection,
  DmsSection,
  DynDNSSection,
  RelaySection,
  SecuritySection,
  ServerSection,
  SpamSection,
  TlsSection,
  WebSection,
} from "./sections";
import { MailAuthCard } from "./MailAuthCard";
import { LimitsSection } from "./LimitsSection";
import { NotificationsSection } from "./NotificationsSection";

const icons: Record<SettingsSectionId, LucideIcon> = {
  server: ServerCog,
  dyndns: RefreshCw,
  relay: Send,
  web: Waypoints,
  tls: Lock,
  spam: ShieldAlert,
  antivirus: Bug,
  ai: Bot,
  dms: FileStack,
  security: ShieldCheck,
  limits: Gauge,
  notifications: BellRing,
  ldap: Building2,
  oidc: KeyRound,
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
    onSuccess: (s, update) => {
      qc.setQueryData(["admin", "settings"], s);
      void qc.invalidateQueries({ queryKey: ["ai", "status"] });
      // TLS and proxy changes apply immediately: reload for a fresh certificate status and detection.
      if (update.tls || update.web) {
        void qc.invalidateQueries({ queryKey: ["admin", "settings"] });
        void qc.invalidateQueries({ queryKey: ["admin", "system-check"] });
        void qc.invalidateQueries({ queryKey: ["admin", "proxy-detect"] });
      }
      if (update.dyndns) void qc.invalidateQueries({ queryKey: dynIpKeys.dyndnsStatus });
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
          ) : id === "dyndns" ? (
            <DynDNSSection key={id} {...props} />
          ) : id === "relay" ? (
            <RelaySection key={id} {...props} />
          ) : id === "web" ? (
            <WebSection key={id} {...props} />
          ) : id === "tls" ? (
            <TlsSection key={id} {...props} />
          ) : id === "spam" ? (
            <>
              <SpamSection key={id} {...props} />
              <MailAuthCard {...props} />
            </>
          ) : id === "antivirus" ? (
            <AntivirusSection key={id} {...props} />
          ) : id === "ai" ? (
            <AiSection key={id} {...props} />
          ) : id === "dms" ? (
            <DmsSection key={id} {...props} />
          ) : id === "ldap" ? (
            <LdapSection key={id} {...props} />
          ) : id === "oidc" ? (
            <OidcSection key={id} {...props} />
          ) : id === "limits" ? (
            <LimitsSection key={id} {...props} />
          ) : id === "notifications" ? (
            <NotificationsSection key={id} {...props} />
          ) : (
            <>
              <SecuritySection key={id} {...props} />
              <EncryptionCard />
            </>
          )}
        </div>
      </div>
    </>
  );
}
