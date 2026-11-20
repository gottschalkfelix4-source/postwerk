import type { ReactNode } from "react";
import { Link, NavLink, useLocation } from "react-router-dom";
import { Mail, ServerCog, X } from "lucide-react";
import { cn } from "@/lib/utils";
import { useMe } from "@/lib/auth";
import { Logo } from "./Logo";
import { adminNav, type NavItem } from "./nav";
import { MailSidebar } from "@/pages/mail/MailSidebar";

export function SidebarSectionTitle({ children, action }: { children: ReactNode; action?: ReactNode }) {
  return (
    <div className="mb-1 mt-6 flex items-center justify-between px-2.5">
      <span className="text-[11px] font-semibold uppercase tracking-[0.08em] text-zinc-400 dark:text-zinc-500">
        {children}
      </span>
      {action}
    </div>
  );
}

export function navItemClass(active: boolean): string {
  return cn(
    "group flex items-center gap-2.5 rounded-lg px-2.5 py-1.5 text-sm font-medium transition-colors",
    active
      ? "bg-indigo-50 text-indigo-700 dark:bg-indigo-500/15 dark:text-indigo-300"
      : "text-zinc-600 hover:bg-zinc-100 hover:text-zinc-900 dark:text-zinc-400 dark:hover:bg-zinc-800/70 dark:hover:text-zinc-100",
  );
}

export function navIconClass(active: boolean): string {
  return cn(
    "h-[18px] w-[18px] shrink-0",
    active ? "text-indigo-600 dark:text-indigo-400" : "text-zinc-400 group-hover:text-zinc-600 dark:text-zinc-500 dark:group-hover:text-zinc-300",
  );
}

function SidebarLink({ item }: { item: NavItem }) {
  return (
    <NavLink to={item.to} end={item.end} className={({ isActive }) => navItemClass(isActive)}>
      {({ isActive }) => (
        <>
          <item.icon className={navIconClass(isActive)} />
          <span className="truncate">{item.label}</span>
        </>
      )}
    </NavLink>
  );
}

function ModeSwitch({ mode }: { mode: "admin" | "mail" }) {
  const base = "flex flex-1 items-center justify-center gap-1.5 rounded-md py-1.5 text-xs font-semibold transition";
  const on = "bg-white text-zinc-900 shadow-sm dark:bg-zinc-700 dark:text-white";
  const off = "text-zinc-500 hover:text-zinc-800 dark:text-zinc-400 dark:hover:text-zinc-200";
  return (
    <div className="mx-3 mt-3 flex rounded-lg bg-zinc-100 p-0.5 dark:bg-zinc-800">
      <Link to="/admin" className={cn(base, mode === "admin" ? on : off)}>
        <ServerCog className="h-3.5 w-3.5" /> Verwaltung
      </Link>
      <Link to="/mail" className={cn(base, mode === "mail" ? on : off)}>
        <Mail className="h-3.5 w-3.5" /> Webmail
      </Link>
    </div>
  );
}

export function Sidebar({ onClose }: { onClose?: () => void }) {
  const me = useMe();
  const { pathname } = useLocation();
  const mode = pathname.startsWith("/admin") ? "admin" : "mail";

  return (
    <aside className="flex h-full w-64 flex-col border-r border-zinc-200 bg-white dark:border-zinc-800 dark:bg-zinc-900">
      <div className="flex h-14 shrink-0 items-center justify-between border-b border-zinc-100 px-4 dark:border-zinc-800">
        <Link to={me.role === "admin" ? "/admin" : "/mail"} className="rounded-md focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-indigo-500/50">
          <Logo />
        </Link>
        {onClose && (
          <button
            type="button"
            onClick={onClose}
            className="rounded-md p-1.5 text-zinc-400 hover:bg-zinc-100 hover:text-zinc-700 dark:hover:bg-zinc-800 lg:hidden"
            aria-label="Navigation schließen"
          >
            <X className="h-5 w-5" />
          </button>
        )}
      </div>

      {me.role === "admin" && <ModeSwitch mode={mode} />}

      <nav className="min-h-0 flex-1 overflow-y-auto px-3 pb-4 pt-2">
        {mode === "admin" ? (
          adminNav.map((group) => (
            <div key={group.title} className="first:[&>div:first-child]:mt-2">
              <SidebarSectionTitle>{group.title}</SidebarSectionTitle>
              <div className="space-y-0.5">
                {group.items.map((item) => (
                  <SidebarLink key={item.to} item={item} />
                ))}
              </div>
            </div>
          ))
        ) : (
          <MailSidebar />
        )}
      </nav>
    </aside>
  );
}
