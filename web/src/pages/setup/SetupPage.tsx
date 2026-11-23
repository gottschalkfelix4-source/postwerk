import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Navigate, useNavigate } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { Moon, Sun } from "lucide-react";
import { authApi } from "@/api/endpoints";
import { isApiError } from "@/api/client";
import { qk, useSetupStatus } from "@/lib/queries";
import { deferSetup, domainFromHostname } from "@/lib/setup";
import { useTheme } from "@/lib/theme";
import { Button, ErrorState, FullPageSpinner, Progress, WizardLayout, type StepItem } from "@/components/ui";
import { Logo } from "@/components/layout/Logo";
import { SetupContext, type InitForm, type SetupCtx } from "./context";
import { DnsStep, ServerAdminStep, ServerStep, SystemStep, WelcomeStep } from "./steps";
import { DataStep, DoneStep, ProtectionStep, ProxyStep, TlsStep } from "./settingsSteps";
import { ConnectionStep } from "./connectionStep";
import { RestoreFlow } from "./restoreStep";

type StepId = "welcome" | "server" | "dns" | "system" | "connection" | "proxy" | "tls" | "protection" | "data" | "done";

export default function SetupPage() {
  const navigate = useNavigate();
  const status = useSetupStatus();
  const [startMode, setStartMode] = useState<"initial" | "configure" | null>(() =>
    status.data ? (status.data.mode === "initial" ? "initial" : "configure") : null,
  );
  const [initialized, setInitialized] = useState(false);
  // Mirrors `initialized` so navigation right after initialization is not blocked by a stale closure.
  const initializedRef = useRef(false);
  const [token, setToken] = useState("");
  const [tokenError, setTokenError] = useState<string | null>(null);
  const [domain, setDomain] = useState("");
  const [initForm, setInitForm] = useState<InitForm | null>(null);
  const [current, setCurrent] = useState(0);
  const [reached, setReached] = useState(0);
  const [direction, setDirection] = useState<"forward" | "back">("forward");
  const [restoring, setRestoring] = useState(false);

  useEffect(() => {
    if (status.data && startMode === null) setStartMode(status.data.mode === "initial" ? "initial" : "configure");
  }, [status.data, startMode]);

  // Being here counts as the one-time prompt for this browser session.
  useEffect(() => deferSetup(), []);

  const hasSession = startMode === "configure" || initialized;
  const me = useQuery({
    queryKey: qk.me,
    queryFn: () => authApi.me(true),
    retry: false,
    staleTime: 60_000,
    enabled: hasSession,
  });

  const initialFlow = startMode === "initial";

  const steps = useMemo<(StepItem & { id: StepId })[]>(
    () => [
      { id: "welcome", label: "Willkommen", description: "Überblick" },
      initialFlow
        ? { id: "server", label: "Server & Admin", description: "Hostname, Domain, Konto" }
        : { id: "server", label: "Server", description: "Hostname" },
      { id: "dns", label: "DNS", description: "Einträge prüfen" },
      { id: "system", label: "System-Check", description: "Ports und Reverse-DNS" },
      { id: "connection", label: "Internetanschluss", description: "Feste oder dynamische IP" },
      { id: "proxy", label: "Reverse-Proxy", description: "Weboberfläche" },
      { id: "tls", label: "TLS", description: "Zertifikatsquelle" },
      { id: "protection", label: "Schutz & KI", description: "Optional" },
      { id: "data", label: "Daten übernehmen", description: "Optional" },
      { id: "done", label: "Fertig" },
    ],
    [initialFlow],
  );

  const go = (index: number) => {
    const target = Math.max(0, Math.min(steps.length - 1, index));
    // Before the first admin exists only the first two steps are reachable.
    if (initialFlow && !initializedRef.current && target > 1) return;
    setDirection(target < current ? "back" : "forward");
    setCurrent(target);
    setReached((r) => Math.max(r, target));
    window.scrollTo({ top: 0, behavior: "smooth" });
  };

  if (status.isLoading || startMode === null) {
    if (status.error) {
      return (
        <Frame>
          <ErrorState error={status.error} onRetry={() => void status.refetch()} />
        </Frame>
      );
    }
    return (
      <Frame>
        <FullPageSpinner />
      </Frame>
    );
  }

  if (hasSession) {
    if (me.isLoading) {
      return (
        <Frame>
          <FullPageSpinner />
        </Frame>
      );
    }
    if (me.error) {
      if (isApiError(me.error) && me.error.status === 401) {
        return <Navigate to="/login" replace state={{ from: "/setup" }} />;
      }
      return (
        <Frame>
          <ErrorState error={me.error} onRetry={() => void me.refetch()} />
        </Frame>
      );
    }
    if (me.data && me.data.role !== "admin") return <Navigate to="/mail" replace />;
  }

  const hostname = status.data?.hostname ?? "";
  const ctx: SetupCtx = {
    status: status.data ?? { mode: startMode, hostname, version: "" },
    initialFlow,
    initialized: hasSession,
    me: me.data,
    token,
    setToken: (t) => {
      setToken(t);
      setTokenError(null);
    },
    tokenError,
    setTokenError,
    domain: domain || (initForm ? initForm.domain : "") || "",
    setDomain,
    initForm: initForm ?? {
      hostname,
      domain: domainFromHostname(hostname),
      adminName: "",
      adminEmail: domainFromHostname(hostname) ? `admin@${domainFromHostname(hostname)}` : "",
      password: "",
      repeat: "",
      domainTouched: false,
      emailTouched: false,
    },
    setInitForm,
    markInitialized: (d) => {
      initializedRef.current = true;
      setInitialized(true);
      setDomain(d);
    },
    goTo: go,
    skip: () => {
      deferSetup();
      navigate("/admin", { replace: true });
    },
    startRestore: () => {
      setRestoring(true);
      window.scrollTo({ top: 0, behavior: "smooth" });
    },
  };

  if (initialFlow && !initialized && restoring) {
    return (
      <SetupContext.Provider value={ctx}>
        <Frame wide version={status.data?.version}>
          <div className="mx-auto max-w-3xl">
            <RestoreFlow onCancel={() => setRestoring(false)} />
          </div>
        </Frame>
      </SetupContext.Provider>
    );
  }

  const id = steps[current].id;
  const back = current > 0 ? () => go(current - 1) : undefined;
  const next = () => go(current + 1);
  const props = { onBack: back, onNext: next };

  return (
    <SetupContext.Provider value={ctx}>
      <Frame
        wide
        version={status.data?.version}
        onSkip={!initialFlow || initialized ? ctx.skip : undefined}
        progress={current / (steps.length - 1)}
      >
        <WizardLayout
          steps={steps}
          current={current}
          reached={initialFlow && !initialized ? Math.min(reached, 1) : reached}
          direction={direction}
          onSelect={go}
        >
          {id === "welcome" ? (
            <WelcomeStep {...props} />
          ) : id === "server" ? (
            initialFlow && !initialized ? (
              <ServerAdminStep {...props} />
            ) : (
              <ServerStep {...props} />
            )
          ) : id === "dns" ? (
            <DnsStep {...props} />
          ) : id === "system" ? (
            <SystemStep {...props} />
          ) : id === "connection" ? (
            <ConnectionStep {...props} />
          ) : id === "proxy" ? (
            <ProxyStep {...props} />
          ) : id === "tls" ? (
            <TlsStep {...props} />
          ) : id === "protection" ? (
            <ProtectionStep {...props} />
          ) : id === "data" ? (
            <DataStep {...props} />
          ) : (
            <DoneStep {...props} />
          )}
        </WizardLayout>
      </Frame>
    </SetupContext.Provider>
  );
}

/** Full-screen frame in the style of the login page. */
function Frame({
  children,
  wide,
  version,
  onSkip,
  progress,
}: {
  children: ReactNode;
  wide?: boolean;
  version?: string;
  onSkip?: () => void;
  progress?: number;
}) {
  const { resolved, toggle } = useTheme();
  return (
    <div className="relative min-h-screen overflow-hidden bg-zinc-50 dark:bg-zinc-950">
      <div
        aria-hidden
        className="pointer-events-none absolute inset-0 bg-[radial-gradient(60rem_30rem_at_50%_-10%,rgba(99,102,241,0.14),transparent)] dark:bg-[radial-gradient(60rem_30rem_at_50%_-10%,rgba(99,102,241,0.18),transparent)]"
      />
      <div
        aria-hidden
        className="pointer-events-none absolute inset-0 bg-[linear-gradient(to_right,rgba(113,113,122,0.06)_1px,transparent_1px),linear-gradient(to_bottom,rgba(113,113,122,0.06)_1px,transparent_1px)] bg-[size:48px_48px] [mask-image:radial-gradient(ellipse_at_top,black_20%,transparent_70%)]"
      />
      <div className={wide ? "relative mx-auto w-full max-w-6xl px-4 py-6 sm:px-6 lg:px-8 lg:py-10" : "relative mx-auto max-w-md px-4 py-16"}>
        <header className="mb-5 flex items-center justify-between gap-3">
          <div className="flex min-w-0 items-center gap-3">
            <Logo />
            <span className="hidden h-5 w-px bg-zinc-200 dark:bg-zinc-800 sm:block" />
            <span className="hidden truncate text-sm font-medium text-zinc-500 dark:text-zinc-400 sm:block">
              Einrichtung{version ? ` · v${version}` : ""}
            </span>
          </div>
          <div className="flex items-center gap-1">
            {onSkip && (
              <Button variant="ghost" size="sm" onClick={onSkip}>
                Später einrichten
              </Button>
            )}
            <Button
              variant="ghost"
              size="icon"
              icon={resolved === "dark" ? Sun : Moon}
              onClick={toggle}
              aria-label={resolved === "dark" ? "Helles Design" : "Dunkles Design"}
            />
          </div>
        </header>
        {progress !== undefined && <Progress value={progress} tone="indigo" className="mb-8 hidden h-1 lg:block" />}
        {children}
      </div>
    </div>
  );
}
