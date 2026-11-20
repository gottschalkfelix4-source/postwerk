import { lazy } from "react";
import { createBrowserRouter, Navigate } from "react-router-dom";
import { AdminGuard, ProtectedLayout } from "@/components/layout/AppShell";
import { SetupGate } from "@/components/layout/SetupGate";
import type { RouteHandle } from "@/components/layout/routeHandle";
import { useMe, homeFor } from "@/lib/auth";
import { LoginPage } from "@/pages/Login";
import { NotFoundPage } from "@/pages/NotFound";
import { AccountCrumb, MigrationCrumb, settingsSectionLabel } from "@/pages/admin/crumbs";

const Dashboard = lazy(() => import("@/pages/admin/Dashboard"));
const Accounts = lazy(() => import("@/pages/admin/Accounts"));
const AccountDetail = lazy(() => import("@/pages/admin/AccountDetail"));
const Domains = lazy(() => import("@/pages/admin/Domains"));
const DomainDetail = lazy(() => import("@/pages/admin/DomainDetail"));
const Aliases = lazy(() => import("@/pages/admin/Aliases"));
const Queue = lazy(() => import("@/pages/admin/Queue"));
const Logs = lazy(() => import("@/pages/admin/Logs"));
const Security = lazy(() => import("@/pages/admin/Security"));
const Metrics = lazy(() => import("@/pages/admin/Metrics"));
const SettingsPage = lazy(() => import("@/pages/admin/settings/SettingsPage"));
const BackupPage = lazy(() => import("@/pages/admin/backup/BackupPage"));
const MigrationList = lazy(() => import("@/pages/admin/migration/MigrationList"));
const MigrationNew = lazy(() => import("@/pages/admin/migration/MigrationNew"));
const MigrationDetail = lazy(() => import("@/pages/admin/migration/MigrationDetail"));
const SetupPage = lazy(() => import("@/pages/setup/SetupPage"));

const MailPage = lazy(() => import("@/pages/mail/MailPage"));
const CalendarPage = lazy(() => import("@/pages/mail/CalendarPage"));
const TasksPage = lazy(() => import("@/pages/mail/TasksPage"));
const MailSettingsPage = lazy(() => import("@/pages/mail/MailSettingsPage"));

function HomeRedirect() {
  const me = useMe();
  return <Navigate to={homeFor(me)} replace />;
}

const h = (handle: RouteHandle) => handle;

export const router = createBrowserRouter([
  {
    element: <SetupGate />,
    children: [
      { path: "/setup", element: <SetupPage /> },
      { path: "/login", element: <LoginPage /> },
      {
        path: "/",
        element: <ProtectedLayout />,
        children: [
          { index: true, element: <HomeRedirect /> },
          {
            path: "admin",
            element: <AdminGuard />,
            children: [
              { index: true, element: <Dashboard />, handle: h({ crumb: "Dashboard" }) },
              {
                path: "accounts",
                handle: h({ crumb: "Konten" }),
                children: [
                  { index: true, element: <Accounts /> },
                  {
                    path: ":id",
                    element: <AccountDetail />,
                    handle: h({ crumb: (p) => <AccountCrumb id={Number(p.id)} /> }),
                  },
                ],
              },
              {
                path: "domains",
                handle: h({ crumb: "Domains" }),
                children: [
                  { index: true, element: <Domains /> },
                  { path: ":name", element: <DomainDetail />, handle: h({ crumb: (p) => p.name ?? "" }) },
                ],
              },
              { path: "aliases", element: <Aliases />, handle: h({ crumb: "Aliase" }) },
              {
                path: "migration",
                handle: h({ crumb: "Migration" }),
                children: [
                  { index: true, element: <MigrationList /> },
                  { path: "new", element: <MigrationNew />, handle: h({ crumb: "Neue Migration" }) },
                  {
                    path: ":id",
                    element: <MigrationDetail />,
                    handle: h({ crumb: (p) => <MigrationCrumb id={Number(p.id)} /> }),
                  },
                ],
              },
              { path: "backup", element: <BackupPage />, handle: h({ crumb: "Backup" }) },
              { path: "queue", element: <Queue />, handle: h({ crumb: "Warteschlange" }) },
              { path: "logs", element: <Logs />, handle: h({ crumb: "Logs" }) },
              { path: "security", element: <Security />, handle: h({ crumb: "Sicherheit" }) },
              { path: "metrics", element: <Metrics />, handle: h({ crumb: "Metriken" }) },
              {
                path: "settings",
                handle: h({ crumb: "Einstellungen" }),
                children: [
                  { index: true, element: <Navigate to="server" replace /> },
                  {
                    path: ":section",
                    element: <SettingsPage />,
                    handle: h({ crumb: (p) => settingsSectionLabel(p.section) }),
                  },
                ],
              },
            ],
          },
          {
            path: "mail",
            children: [
              { index: true, element: <MailPage />, handle: h({ crumb: "Nachrichten", fullBleed: true }) },
              { path: "calendar", element: <CalendarPage />, handle: h({ crumb: "Kalender" }) },
              { path: "tasks", element: <TasksPage />, handle: h({ crumb: "Aufgaben" }) },
              { path: "settings", element: <MailSettingsPage />, handle: h({ crumb: "Konto & Sicherheit" }) },
            ],
          },
          { path: "*", element: <NotFoundPage /> },
        ],
      },
    ],
  },
]);
