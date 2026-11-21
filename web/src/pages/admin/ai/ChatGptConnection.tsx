import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CircleCheck, ExternalLink, Gauge, LogIn, LogOut, TriangleAlert } from "lucide-react";
import { adminApi } from "@/api/endpoints";
import { errorMessage, isApiError } from "@/api/client";
import type { ChatGptLogin } from "@/api/types";
import { Badge, Button, CopyButton, Notice, Spinner } from "@/components/ui";

const STATUS_KEY = ["admin", "chatgpt"] as const;

/** Models of the Codex catalog for ChatGPT sign-ins; other names can be typed in. */
export const CHATGPT_MODELS = [
  "gpt-6.2-sol", "gpt-6.2-astra", "gpt-6.1-terra", "gpt-6.1-sol", "gpt-6-astra",
  "gpt-6-sol", "gpt-6-luna", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5",
];

function timeOf(iso?: string) {
  if (!iso) return "";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "" : d.toLocaleTimeString("de-DE", { hour: "2-digit", minute: "2-digit" });
}

function dateTimeOf(iso?: string) {
  if (!iso) return "";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "" : d.toLocaleString("de-DE", { day: "2-digit", month: "2-digit", hour: "2-digit", minute: "2-digit" });
}

/** Sign-in with a ChatGPT subscription via device code. The server polls; the browser only shows the code. */
export function ChatGptConnection() {
  const qc = useQueryClient();
  const status = useQuery({ queryKey: STATUS_KEY, queryFn: adminApi.chatgptStatus });
  const [login, setLogin] = useState<ChatGptLogin | null>(null);
  const [error, setError] = useState<string | null>(null);

  // A sign-in started before a page reload continues.
  const resumable = status.data?.login;
  useEffect(() => {
    if (!login && resumable?.status === "pending") setLogin(resumable);
  }, [resumable, login]);

  const pending = login?.status === "pending";
  const poll = useQuery({
    queryKey: ["admin", "chatgpt", "login", login?.id],
    queryFn: () => adminApi.chatgptLoginStatus(login!.id),
    enabled: pending,
    refetchInterval: 2000,
    retry: false,
  });

  useEffect(() => {
    const l = poll.data;
    if (!l || !login || l.id !== login.id || l.status === login.status) return;
    if (l.status === "success") {
      setLogin(null);
      setError(null);
      void qc.invalidateQueries({ queryKey: STATUS_KEY });
    } else {
      setLogin(l);
    }
  }, [poll.data, login, qc]);

  useEffect(() => {
    if (pending && poll.error && isApiError(poll.error, "not_found")) {
      setLogin(null);
      setError("Die Anmeldung ist dem Server nicht mehr bekannt (z. B. nach einem Neustart) – bitte neu starten.");
    }
  }, [poll.error, pending]);

  const start = useMutation({
    mutationFn: adminApi.chatgptLogin,
    onSuccess: (l) => {
      setError(null);
      setLogin(l);
    },
    onError: (err) => setError(errorMessage(err)),
  });
  const logout = useMutation({
    mutationFn: adminApi.chatgptLogout,
    onSuccess: () => {
      setLogin(null);
      setError(null);
      void qc.invalidateQueries({ queryKey: STATUS_KEY });
    },
    onError: (err) => setError(errorMessage(err)),
  });

  const s = status.data;
  const failedLogin = login && !pending ? login : null;

  return (
    <div className="space-y-3 rounded-lg border border-zinc-200 p-4 dark:border-zinc-800">
      <div className="text-sm font-medium text-zinc-900 dark:text-zinc-100">Anmeldung mit ChatGPT</div>

      {status.isLoading ? (
        <div className="flex items-center gap-2 text-sm text-zinc-500 dark:text-zinc-400">
          <Spinner className="h-4 w-4" /> Status wird geladen …
        </div>
      ) : pending && login ? (
        <div className="space-y-3">
          <ol className="space-y-1 text-sm text-zinc-700 dark:text-zinc-300">
            <li>
              1. Öffne{" "}
              <a
                href={login.verificationUrl}
                target="_blank"
                rel="noopener noreferrer"
                className="inline-flex items-center gap-1 font-medium text-indigo-600 hover:underline dark:text-indigo-400"
              >
                {login.verificationUrl.replace(/^https?:\/\//, "")}
                <ExternalLink className="h-3.5 w-3.5" />
              </a>{" "}
              und melde dich mit deinem ChatGPT-Konto an.
            </li>
            <li>2. Gib dort diesen Code ein:</li>
          </ol>
          <div className="flex flex-wrap items-center gap-2">
            <span className="select-all rounded-lg bg-zinc-100 px-4 py-2 font-mono text-2xl font-semibold tracking-[0.2em] text-zinc-900 dark:bg-zinc-800 dark:text-zinc-100">
              {login.userCode}
            </span>
            <CopyButton value={login.userCode} label="Kopieren" />
          </div>
          <div className="flex flex-wrap items-center gap-3 text-sm text-zinc-500 dark:text-zinc-400">
            <span className="inline-flex items-center gap-2">
              <Spinner className="h-4 w-4" /> Warte auf die Bestätigung … (Code gültig bis {timeOf(login.expiresAt)} Uhr)
            </span>
            <Button size="sm" variant="ghost" loading={logout.isPending} onClick={() => logout.mutate()}>
              Abbrechen
            </Button>
          </div>
          <p className="text-xs text-zinc-500 dark:text-zinc-400">
            Gib den Code nur ein, wenn du diese Anmeldung selbst gestartet hast.
          </p>
        </div>
      ) : s?.connected ? (
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex items-center gap-2 text-sm text-zinc-800 dark:text-zinc-200">
            <CircleCheck className="h-4 w-4 text-emerald-600" />
            <span>
              Verbunden als <span className="font-medium">{s.email || "unbekannt"}</span>
              {s.plan && (
                <>
                  {" "}
                  <Badge tone="indigo">{s.plan}</Badge>
                </>
              )}
            </span>
          </div>
          <Button size="sm" variant="secondary" icon={LogOut} loading={logout.isPending} onClick={() => logout.mutate()}>
            Abmelden
          </Button>
        </div>
      ) : (
        <div className="space-y-3">
          {s?.error && (
            <Notice tone="error" icon={TriangleAlert} title="Verbindung zu ChatGPT getrennt">
              {s.error}
            </Notice>
          )}
          {failedLogin && (
            <Notice tone="error" icon={TriangleAlert} title="Anmeldung nicht abgeschlossen">
              {failedLogin.error || "Die Anmeldung ist fehlgeschlagen."}
            </Notice>
          )}
          <Button icon={LogIn} loading={start.isPending} onClick={() => start.mutate()}>
            Mit ChatGPT anmelden
          </Button>
        </div>
      )}

      {error && (
        <Notice tone="error" icon={TriangleAlert}>
          {error}
        </Notice>
      )}
      {s?.limit && (
        <Notice tone="warning" icon={Gauge} title="Nutzungslimit erreicht">
          {s.limit}
          {s.limitUntil && ` Die Analyse neuer Mails wird ab ${dateTimeOf(s.limitUntil)} Uhr fortgesetzt.`}
        </Notice>
      )}

      <ul className="list-disc space-y-1 pl-4 text-xs text-zinc-500 dark:text-zinc-400">
        <li>Lehnt die Code-Seite die Anmeldung ab: in ChatGPT unter Einstellungen → Sicherheit die Gerätecode-Anmeldung aktivieren.</li>
        <li>Alle KI-Anfragen zählen zu den Nutzungslimits deines ChatGPT-Abos (Plus, Pro, …). Ist ein Limit erreicht, pausiert die Analyse bis zum Zurücksetzen.</li>
        <li>Für die semantische Suche ist zusätzlich ein Embedding-Endpunkt nötig, z. B. Ollama (siehe unten).</li>
      </ul>
    </div>
  );
}
