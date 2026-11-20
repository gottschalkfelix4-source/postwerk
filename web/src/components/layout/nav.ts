import {
  Activity,
  ArrowRightLeft,
  ArchiveRestore,
  AtSign,
  CalendarDays,
  Globe,
  Inbox,
  LayoutDashboard,
  ListTodo,
  ScrollText,
  Send,
  Settings,
  ShieldAlert,
  UserCog,
  Users,
  type LucideIcon,
} from "lucide-react";

export interface NavItem {
  to: string;
  label: string;
  icon: LucideIcon;
  end?: boolean;
  keywords?: string;
}

export interface NavGroup {
  title: string;
  items: NavItem[];
}

export const adminNav: NavGroup[] = [
  {
    title: "Übersicht",
    items: [{ to: "/admin", label: "Dashboard", icon: LayoutDashboard, end: true, keywords: "start statistik" }],
  },
  {
    title: "Verzeichnis",
    items: [
      { to: "/admin/accounts", label: "Konten", icon: Users, keywords: "benutzer user" },
      { to: "/admin/domains", label: "Domains", icon: Globe, keywords: "dns dkim" },
      { to: "/admin/aliases", label: "Aliase", icon: AtSign, keywords: "weiterleitung" },
    ],
  },
  {
    title: "Daten",
    items: [
      { to: "/admin/migration", label: "Migration", icon: ArrowRightLeft, keywords: "import umzug imap gmail outlook" },
      { to: "/admin/backup", label: "Backup", icon: ArchiveRestore, keywords: "sicherung wiederherstellen archiv webdav smb nas" },
    ],
  },
  {
    title: "Warteschlange",
    items: [{ to: "/admin/queue", label: "Ausgehend", icon: Send, keywords: "queue zustellung" }],
  },
  {
    title: "Telemetrie",
    items: [
      { to: "/admin/logs", label: "Logs", icon: ScrollText, keywords: "protokoll" },
      { to: "/admin/security", label: "Sicherheit", icon: ShieldAlert, keywords: "sperren ban ip" },
      { to: "/admin/metrics", label: "Metriken", icon: Activity, keywords: "prometheus" },
    ],
  },
  {
    title: "Konfiguration",
    items: [{ to: "/admin/settings", label: "Einstellungen", icon: Settings, keywords: "tls spam ki dms" }],
  },
];

export const mailPages: NavItem[] = [
  { to: "/mail", label: "Posteingang", icon: Inbox, end: true, keywords: "mail nachrichten" },
  { to: "/mail/calendar", label: "Kalender", icon: CalendarDays, keywords: "termine" },
  { to: "/mail/tasks", label: "Aufgaben", icon: ListTodo, keywords: "todo" },
  { to: "/mail/settings", label: "Konto & Sicherheit", icon: UserCog, keywords: "passwort 2fa abwesenheit import mbox eml" },
];
