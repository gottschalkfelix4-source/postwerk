import { Fragment, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Link, useLocation, useMatches, useNavigate, useParams } from "react-router-dom";
import {
  ChevronRight,
  LogOut,
  Mail,
  Menu,
  Monitor,
  Moon,
  Search,
  ServerCog,
  Sun,
  UserCog,
  type LucideIcon,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { useLogout, useMe } from "@/lib/auth";
import { useTheme, type ThemeMode } from "@/lib/theme";
import { Avatar, Badge, Dropdown, DropdownItem, DropdownLabel, DropdownSeparator, Kbd } from "@/components/ui";
import { adminNav, mailPages } from "./nav";
import type { RouteHandle } from "./routeHandle";

function Breadcrumbs() {
  const matches = useMatches();
  const params = useParams();
  const { pathname } = useLocation();
  const admin = pathname.startsWith("/admin");

  const crumbs: { label: ReactNode; to: string }[] = [{ label: admin ? "Verwaltung" : "Webmail", to: admin ? "/admin" : "/mail" }];
  for (const m of matches) {
    const handle = m.handle as RouteHandle | undefined;
    if (!handle?.crumb) continue;
    const label = typeof handle.crumb === "function" ? handle.crumb(params) : handle.crumb;
    crumbs.push({ label, to: m.pathname });
  }

  return (
    <nav aria-label="Brotkrumen" className="flex min-w-0 items-center gap-1.5 text-sm">
      {crumbs.map((c, i) => {
        const last = i === crumbs.length - 1;
        return (
          <Fragment key={i}>
            {i > 0 && <ChevronRight className="h-3.5 w-3.5 shrink-0 text-zinc-300 dark:text-zinc-600" />}
            {last ? (
              <span className="truncate font-medium text-zinc-900 dark:text-zinc-100">{c.label}</span>
            ) : (
              <Link to={c.to} className="hidden truncate text-zinc-500 hover:text-zinc-800 dark:text-zinc-400 dark:hover:text-zinc-200 sm:inline">
                {c.label}
              </Link>
            )}
          </Fragment>
        );
      })}
    </nav>
  );
}

interface SearchResult {
  key: string;
  label: string;
  hint?: string;
  icon: LucideIcon;
  to: string;
}

function GlobalSearch() {
  const me = useMe();
  const navigate = useNavigate();
  const [query, setQuery] = useState("");
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const wrapRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        inputRef.current?.focus();
        setOpen(true);
      }
    };
    const onClick = (e: MouseEvent) => {
      if (wrapRef.current && !wrapRef.current.contains(e.target as Node)) setOpen(false);
    };
    window.addEventListener("keydown", onKey);
    document.addEventListener("mousedown", onClick);
    return () => {
      window.removeEventListener("keydown", onKey);
      document.removeEventListener("mousedown", onClick);
    };
  }, []);

  const results = useMemo<SearchResult[]>(() => {
    const q = query.trim().toLowerCase();
    const pages = [
      ...(me.role === "admin" ? adminNav.flatMap((g) => g.items.map((i) => ({ ...i, group: g.title }))) : []),
      ...mailPages.map((i) => ({ ...i, group: "Webmail" })),
    ];
    const matched = pages
      .filter((p) => !q || p.label.toLowerCase().includes(q) || p.keywords?.includes(q) || p.group.toLowerCase().includes(q))
      .slice(0, q ? 6 : 8)
      .map<SearchResult>((p) => ({ key: p.to, label: p.label, hint: p.group, icon: p.icon, to: p.to }));
    if (!q) return matched;
    const enc = encodeURIComponent(query.trim());
    const actions: SearchResult[] = [
      { key: "mail", label: `E-Mails nach „${query.trim()}“ durchsuchen`, icon: Mail, to: `/mail?q=${enc}` },
    ];
    if (me.role === "admin") {
      actions.push(
        { key: "accounts", label: `Konten nach „${query.trim()}“ durchsuchen`, icon: UserCog, to: `/admin/accounts?q=${enc}` },
        { key: "logs", label: `Logs nach „${query.trim()}“ durchsuchen`, icon: ServerCog, to: `/admin/logs?q=${enc}` },
      );
    }
    return [...actions, ...matched];
  }, [query, me.role]);

  const go = (r: SearchResult) => {
    navigate(r.to);
    setOpen(false);
    setQuery("");
    inputRef.current?.blur();
  };

  return (
    <div ref={wrapRef} className="relative w-full max-w-xs">
      <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-zinc-400" />
      <input
        ref={inputRef}
        value={query}
        onChange={(e) => {
          setQuery(e.target.value);
          setActive(0);
          setOpen(true);
        }}
        onFocus={() => setOpen(true)}
        onKeyDown={(e) => {
          if (e.key === "ArrowDown") {
            e.preventDefault();
            setActive((a) => Math.min(results.length - 1, a + 1));
          } else if (e.key === "ArrowUp") {
            e.preventDefault();
            setActive((a) => Math.max(0, a - 1));
          } else if (e.key === "Enter" && results[active]) {
            e.preventDefault();
            go(results[active]);
          } else if (e.key === "Escape") {
            setOpen(false);
            inputRef.current?.blur();
          }
        }}
        placeholder="Suchen …"
        aria-label="Globale Suche"
        className="h-9 w-full rounded-lg border border-zinc-200 bg-zinc-50 pl-9 pr-14 text-sm text-zinc-900 placeholder:text-zinc-400 transition focus:border-indigo-500 focus:bg-white focus:outline-none focus:ring-2 focus:ring-indigo-500/25 dark:border-zinc-800 dark:bg-zinc-800/60 dark:text-zinc-100 dark:focus:border-indigo-400 dark:focus:bg-zinc-900"
      />
      <span className="pointer-events-none absolute right-2 top-1/2 hidden -translate-y-1/2 gap-0.5 sm:flex">
        <Kbd>Strg</Kbd>
        <Kbd>K</Kbd>
      </span>
      {open && results.length > 0 && (
        <div className="absolute left-0 right-0 top-full z-40 mt-1.5 max-h-96 animate-scale-in overflow-y-auto rounded-xl border border-zinc-200 bg-white p-1 shadow-pop dark:border-zinc-700 dark:bg-zinc-800 sm:w-96">
          {results.map((r, i) => (
            <button
              key={r.key}
              type="button"
              onMouseEnter={() => setActive(i)}
              onClick={() => go(r)}
              className={cn(
                "flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2 text-left text-sm",
                i === active ? "bg-indigo-50 text-indigo-700 dark:bg-indigo-500/15 dark:text-indigo-200" : "text-zinc-700 dark:text-zinc-200",
              )}
            >
              <r.icon className="h-4 w-4 shrink-0 opacity-70" />
              <span className="min-w-0 flex-1 truncate">{r.label}</span>
              {r.hint && <span className="text-xs text-zinc-400">{r.hint}</span>}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

function ThemeToggle() {
  const { mode, resolved, setMode } = useTheme();
  const options: { value: ThemeMode; label: string; icon: LucideIcon }[] = [
    { value: "light", label: "Hell", icon: Sun },
    { value: "dark", label: "Dunkel", icon: Moon },
    { value: "system", label: "System", icon: Monitor },
  ];
  return (
    <Dropdown
      align="right"
      trigger={({ toggle }) => (
        <button
          type="button"
          onClick={toggle}
          className="rounded-lg p-2 text-zinc-500 transition hover:bg-zinc-100 hover:text-zinc-900 dark:text-zinc-400 dark:hover:bg-zinc-800 dark:hover:text-zinc-100"
          aria-label="Farbschema wählen"
          title="Farbschema"
        >
          {resolved === "dark" ? <Moon className="h-[18px] w-[18px]" /> : <Sun className="h-[18px] w-[18px]" />}
        </button>
      )}
      menuClassName="min-w-[10rem]"
    >
      {(close) =>
        options.map((o) => (
          <DropdownItem
            key={o.value}
            icon={o.icon}
            selected={mode === o.value}
            onClick={() => {
              setMode(o.value);
              close();
            }}
          >
            {o.label}
          </DropdownItem>
        ))
      }
    </Dropdown>
  );
}

function UserMenu() {
  const me = useMe();
  const logout = useLogout();
  const navigate = useNavigate();
  const { pathname } = useLocation();
  return (
    <Dropdown
      align="right"
      trigger={({ toggle, open }) => (
        <button
          type="button"
          onClick={toggle}
          className={cn(
            "flex items-center gap-2 rounded-full p-0.5 pr-0.5 transition sm:rounded-lg sm:py-1 sm:pl-1 sm:pr-2",
            open ? "bg-zinc-100 dark:bg-zinc-800" : "hover:bg-zinc-100 dark:hover:bg-zinc-800",
          )}
          aria-label="Benutzermenü"
        >
          <Avatar name={me.name} email={me.email} size="sm" />
          <span className="hidden max-w-[10rem] truncate text-sm font-medium text-zinc-700 dark:text-zinc-200 md:block">
            {me.name || me.email}
          </span>
        </button>
      )}
      menuClassName="w-64"
    >
      {(close) => (
        <>
          <div className="flex items-center gap-3 px-2.5 py-2.5">
            <Avatar name={me.name} email={me.email} size="md" />
            <div className="min-w-0">
              <div className="truncate text-sm font-semibold text-zinc-900 dark:text-zinc-50">{me.name || me.email}</div>
              <div className="truncate text-xs text-zinc-500 dark:text-zinc-400">{me.email}</div>
            </div>
          </div>
          <div className="px-2.5 pb-2">
            <Badge tone={me.role === "admin" ? "indigo" : "neutral"}>{me.role === "admin" ? "Administrator" : "Benutzer"}</Badge>
          </div>
          <DropdownSeparator />
          <DropdownLabel>Konto</DropdownLabel>
          <DropdownItem
            icon={UserCog}
            onClick={() => {
              close();
              navigate("/mail/settings");
            }}
          >
            Konto & Sicherheit
          </DropdownItem>
          {me.role === "admin" && (
            <DropdownItem
              icon={pathname.startsWith("/admin") ? Mail : ServerCog}
              onClick={() => {
                close();
                navigate(pathname.startsWith("/admin") ? "/mail" : "/admin");
              }}
            >
              {pathname.startsWith("/admin") ? "Zum Webmail" : "Zur Verwaltung"}
            </DropdownItem>
          )}
          <DropdownSeparator />
          <DropdownItem
            icon={LogOut}
            danger
            onClick={() => {
              close();
              void logout();
            }}
          >
            Abmelden
          </DropdownItem>
        </>
      )}
    </Dropdown>
  );
}

export function Topbar({ onMenu }: { onMenu: () => void }) {
  return (
    <header className="sticky top-0 z-30 flex h-14 shrink-0 items-center gap-3 border-b border-zinc-200 bg-white/85 px-3 backdrop-blur dark:border-zinc-800 dark:bg-zinc-900/85 sm:px-5">
      <button
        type="button"
        onClick={onMenu}
        className="rounded-lg p-2 text-zinc-500 hover:bg-zinc-100 hover:text-zinc-900 dark:text-zinc-400 dark:hover:bg-zinc-800 lg:hidden"
        aria-label="Navigation öffnen"
      >
        <Menu className="h-5 w-5" />
      </button>
      <div className="min-w-0 flex-1">
        <Breadcrumbs />
      </div>
      <div className="hidden flex-1 justify-end md:flex">
        <GlobalSearch />
      </div>
      <div className="flex items-center gap-1">
        <ThemeToggle />
        <UserMenu />
      </div>
    </header>
  );
}
