import { Suspense, useEffect, useState } from "react";
import { Navigate, Outlet, useLocation, useMatches } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { authApi } from "@/api/endpoints";
import { isApiError } from "@/api/client";
import { MeProvider, useMe } from "@/lib/auth";
import { qk } from "@/lib/queries";
import { cn } from "@/lib/utils";
import { ErrorState, FullPageSpinner } from "@/components/ui";
import { ComposerProvider } from "@/pages/mail/Composer";
import { Sidebar } from "./Sidebar";
import { Topbar } from "./Topbar";
import type { RouteHandle } from "./routeHandle";

/** Loads the session and renders the application frame around all protected pages. */
export function ProtectedLayout() {
  const location = useLocation();
  const me = useQuery({ queryKey: qk.me, queryFn: () => authApi.me(true), retry: false, staleTime: 60_000 });

  if (me.isLoading) {
    return (
      <div className="flex min-h-screen bg-zinc-50 dark:bg-zinc-950">
        <FullPageSpinner />
      </div>
    );
  }
  if (me.error) {
    if (isApiError(me.error) && me.error.status === 401) {
      return <Navigate to="/login" replace state={{ from: location.pathname + location.search }} />;
    }
    return (
      <div className="flex min-h-screen items-center justify-center bg-zinc-50 dark:bg-zinc-950">
        <ErrorState error={me.error} onRetry={() => void me.refetch()} />
      </div>
    );
  }
  if (!me.data) return null;

  return (
    <MeProvider me={me.data}>
      <ComposerProvider>
        <AppShell />
      </ComposerProvider>
    </MeProvider>
  );
}

export function AdminGuard() {
  const me = useMe();
  if (me.role !== "admin") return <Navigate to="/mail" replace />;
  return <Outlet />;
}

function AppShell() {
  const [drawer, setDrawer] = useState(false);
  const location = useLocation();
  const matches = useMatches();
  const fullBleed = matches.some((m) => (m.handle as RouteHandle | undefined)?.fullBleed);

  useEffect(() => setDrawer(false), [location.pathname, location.search]);

  return (
    <div className="flex h-screen overflow-hidden bg-zinc-50 dark:bg-zinc-950">
      <div className="hidden lg:flex lg:shrink-0">
        <Sidebar />
      </div>

      {drawer && (
        <div className="fixed inset-0 z-40 flex lg:hidden">
          <div className="absolute inset-0 animate-fade-in bg-zinc-950/40 backdrop-blur-[2px]" onClick={() => setDrawer(false)} />
          <div className="relative h-full animate-slide-in-left shadow-pop">
            <Sidebar onClose={() => setDrawer(false)} />
          </div>
        </div>
      )}

      <div className="flex min-w-0 flex-1 flex-col">
        <Topbar onMenu={() => setDrawer(true)} />
        <main className={cn("min-h-0 flex-1", fullBleed ? "flex overflow-hidden" : "overflow-y-auto")}>
          <Suspense fallback={<FullPageSpinner />}>
            {fullBleed ? (
              <Outlet />
            ) : (
              <div className="mx-auto w-full max-w-7xl px-4 py-6 sm:px-6 lg:px-8 lg:py-8">
                <Outlet />
              </div>
            )}
          </Suspense>
        </main>
      </div>
    </div>
  );
}
