import { useQuery } from "@tanstack/react-query";
import { adminApi } from "@/api/endpoints";

export const settingsSections = [
  { id: "server", label: "Server" },
  { id: "tls", label: "TLS / ACME" },
  { id: "spam", label: "Spamfilter" },
  { id: "antivirus", label: "Virenscan" },
  { id: "ai", label: "KI" },
  { id: "dms", label: "DMS / Paperless" },
  { id: "security", label: "Sicherheit" },
] as const;

export type SettingsSectionId = (typeof settingsSections)[number]["id"];

export function settingsSectionLabel(id: string | undefined): string {
  return settingsSections.find((s) => s.id === id)?.label ?? "Abschnitt";
}

export function AccountCrumb({ id }: { id: number }) {
  const q = useQuery({
    queryKey: ["admin", "account", id],
    queryFn: () => adminApi.account(id),
    enabled: Number.isFinite(id),
  });
  return <>{q.data?.email ?? `Konto #${id}`}</>;
}

export function MigrationCrumb({ id }: { id: number }) {
  const q = useQuery({
    queryKey: ["admin", "migration", id],
    queryFn: () => adminApi.migration(id),
    enabled: Number.isFinite(id),
  });
  return <>{q.data?.name || `Migration #${id}`}</>;
}
