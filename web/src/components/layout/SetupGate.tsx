import { Suspense, useEffect } from "react";
import { Navigate, Outlet, useLocation, useNavigate } from "react-router-dom";
import type { Me } from "@/api/types";
import { useSetupStatus } from "@/lib/queries";
import { deferSetup, isSetupDeferred } from "@/lib/setup";
import { FullPageSpinner } from "@/components/ui";

function Screen() {
  return (
    <div className="flex min-h-screen bg-zinc-50 dark:bg-zinc-950">
      <FullPageSpinner />
    </div>
  );
}

/**
 * Root route: while the server has no account yet ("initial"), every route
 * leads to the setup wizard. Errors (e.g. an older backend without the setup
 * endpoint) never block the app.
 */
export function SetupGate() {
  const { pathname } = useLocation();
  const status = useSetupStatus();

  if (status.isLoading) return <Screen />;
  if (status.data?.mode === "initial" && !pathname.startsWith("/setup")) {
    return <Navigate to="/setup" replace />;
  }
  return (
    <Suspense fallback={<Screen />}>
      <Outlet />
    </Suspense>
  );
}

/**
 * True while an admin should be sent to the setup wizard once per browser
 * session because the server is in "configure" mode. Performs the redirect.
 */
export function useSetupPrompt(me: Me | undefined): boolean {
  const navigate = useNavigate();
  const status = useSetupStatus();
  const prompt = !!me && me.role === "admin" && status.data?.mode === "configure" && !isSetupDeferred();

  useEffect(() => {
    if (!prompt) return;
    deferSetup();
    navigate("/setup", { replace: true });
  }, [prompt, navigate]);

  return prompt;
}
