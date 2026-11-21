import { useEffect, useState, type FormEvent } from "react";
import { useLocation, useNavigate } from "react-router-dom";
import { useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, KeyRound, Lock, Mail, ShieldCheck } from "lucide-react";
import { accountApi, authApi } from "@/api/endpoints";
import { errorMessage, isApiError } from "@/api/client";
import type { Me } from "@/api/types";
import { homeFor } from "@/lib/auth";
import { qk } from "@/lib/queries";
import { Button, CodeInput, Field, Input, PasswordInput } from "@/components/ui";
import { Logo } from "@/components/layout/Logo";

export function LoginPage() {
  const navigate = useNavigate();
  const location = useLocation();
  const qc = useQueryClient();
  const from = (location.state as { from?: string } | null)?.from;

  const [step, setStep] = useState<"credentials" | "totp" | "change">("credentials");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [useRecovery, setUseRecovery] = useState(false);
  const [recovery, setRecovery] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [repeatPassword, setRepeatPassword] = useState("");
  const [askCurrent, setAskCurrent] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // Skip the form when a valid session already exists.
  useEffect(() => {
    let cancelled = false;
    authApi
      .me(true)
      .then((me) => {
        if (!cancelled) finish(me);
      })
      .catch(() => undefined);
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  function finish(me: Me) {
    if (me.mustChangePassword) {
      // A generated initial password must be replaced before anything else.
      setAskCurrent(password === "");
      setStep("change");
      setError(null);
      return;
    }
    qc.setQueryData(qk.me, me);
    const target = from && !from.startsWith("/login") && (me.role === "admin" || !from.startsWith("/admin")) ? from : homeFor(me);
    navigate(target, { replace: true });
  }

  async function submit(totp?: string) {
    setBusy(true);
    setError(null);
    try {
      const me = await authApi.login({ email: email.trim(), password, ...(totp ? { totp } : {}) });
      finish(me);
    } catch (err) {
      if (isApiError(err, "totp_required")) {
        if (step === "totp") {
          setError("Der Code ist ungültig oder abgelaufen.");
          setCode("");
        } else {
          setStep("totp");
        }
      } else if (isApiError(err, "unauthorized")) {
        setError(step === "totp" ? "Der Code ist ungültig." : "E-Mail-Adresse oder Passwort ist falsch.");
        if (step === "totp") setCode("");
      } else {
        setError(errorMessage(err));
      }
    } finally {
      setBusy(false);
    }
  }

  async function changePassword() {
    setBusy(true);
    setError(null);
    try {
      await accountApi.changePassword(password, newPassword, false);
      finish(await authApi.me(true));
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  const newTooShort = newPassword.length > 0 && newPassword.length < 10;
  const newMismatch = repeatPassword.length > 0 && newPassword !== repeatPassword;
  const changeReady = password.length > 0 && newPassword.length >= 10 && newPassword === repeatPassword;
  const totpReady = useRecovery ? recovery.trim().length > 0 : code.length === 6;

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    if (step === "credentials") void submit();
    else if (step === "change") {
      if (changeReady) void changePassword();
    } else if (totpReady) void submit(useRecovery ? recovery.trim() : code);
  };

  return (
    <div className="relative flex min-h-screen items-center justify-center overflow-hidden bg-zinc-50 px-4 py-12 dark:bg-zinc-950">
      <div
        aria-hidden
        className="pointer-events-none absolute inset-0 bg-[radial-gradient(60rem_30rem_at_50%_-10%,rgba(99,102,241,0.14),transparent)] dark:bg-[radial-gradient(60rem_30rem_at_50%_-10%,rgba(99,102,241,0.18),transparent)]"
      />
      <div
        aria-hidden
        className="pointer-events-none absolute inset-0 bg-[linear-gradient(to_right,rgba(113,113,122,0.06)_1px,transparent_1px),linear-gradient(to_bottom,rgba(113,113,122,0.06)_1px,transparent_1px)] bg-[size:48px_48px] [mask-image:radial-gradient(ellipse_at_center,black_30%,transparent_75%)]"
      />

      <div className="relative w-full max-w-sm">
        <div className="mb-8 flex justify-center">
          <Logo className="scale-110" />
        </div>

        <div className="rounded-2xl border border-zinc-200 bg-white p-7 shadow-pop dark:border-zinc-800 dark:bg-zinc-900">
          {step === "credentials" ? (
            <>
              <h1 className="text-lg font-semibold text-zinc-900 dark:text-zinc-50">Anmelden</h1>
              <p className="mt-1 text-sm text-zinc-500 dark:text-zinc-400">Melde dich mit deinem Postwerk-Konto an.</p>
            </>
          ) : step === "change" ? (
            <>
              <div className="mb-4 flex h-11 w-11 items-center justify-center rounded-xl bg-amber-50 text-amber-600 dark:bg-amber-500/15 dark:text-amber-300">
                <KeyRound className="h-6 w-6" />
              </div>
              <h1 className="text-lg font-semibold text-zinc-900 dark:text-zinc-50">Neues Passwort festlegen</h1>
              <p className="mt-1 text-sm text-zinc-500 dark:text-zinc-400">
                Dieses Konto verwendet ein automatisch erzeugtes Passwort. Bitte lege jetzt ein eigenes fest.
              </p>
            </>
          ) : (
            <>
              <div className="mb-4 flex h-11 w-11 items-center justify-center rounded-xl bg-indigo-50 text-indigo-600 dark:bg-indigo-500/15 dark:text-indigo-300">
                <ShieldCheck className="h-6 w-6" />
              </div>
              <h1 className="text-lg font-semibold text-zinc-900 dark:text-zinc-50">Zwei-Faktor-Bestätigung</h1>
              <p className="mt-1 text-sm text-zinc-500 dark:text-zinc-400">
                {useRecovery
                  ? "Gib einen deiner Wiederherstellungscodes ein. Jeder Code funktioniert nur einmal."
                  : "Gib den 6-stelligen Code aus deiner Authenticator-App ein."}
              </p>
            </>
          )}

          <form onSubmit={onSubmit} className="mt-6 space-y-4" noValidate>
            {step === "credentials" ? (
              <>
                <Field label="E-Mail-Adresse">
                  {(id) => (
                    <Input
                      id={id}
                      type="email"
                      icon={Mail}
                      autoComplete="username"
                      autoFocus
                      required
                      value={email}
                      onChange={(e) => setEmail(e.target.value)}
                      placeholder="name@beispiel.de"
                    />
                  )}
                </Field>
                <Field label="Passwort">
                  {(id) => (
                    <PasswordInput
                      id={id}
                      icon={Lock}
                      autoComplete="current-password"
                      required
                      value={password}
                      onChange={(e) => setPassword(e.target.value)}
                    />
                  )}
                </Field>
              </>
            ) : step === "change" ? (
              <>
                {askCurrent && (
                  <Field label="Aktuelles Passwort">
                    {(id) => (
                      <PasswordInput id={id} icon={Lock} autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
                    )}
                  </Field>
                )}
                <Field label="Neues Passwort" error={newTooShort ? "Mindestens 10 Zeichen." : null}>
                  {(id) => (
                    <PasswordInput
                      id={id}
                      autoComplete="new-password"
                      autoFocus
                      invalid={newTooShort}
                      value={newPassword}
                      onChange={(e) => setNewPassword(e.target.value)}
                    />
                  )}
                </Field>
                <Field label="Wiederholen" error={newMismatch ? "Passwörter stimmen nicht überein." : null}>
                  {(id) => (
                    <PasswordInput
                      id={id}
                      autoComplete="new-password"
                      invalid={newMismatch}
                      value={repeatPassword}
                      onChange={(e) => setRepeatPassword(e.target.value)}
                    />
                  )}
                </Field>
              </>
            ) : useRecovery ? (
              <Field label="Wiederherstellungscode">
                {(id) => (
                  <Input
                    id={id}
                    icon={KeyRound}
                    autoComplete="one-time-code"
                    autoFocus
                    value={recovery}
                    onChange={(e) => {
                      setRecovery(e.target.value);
                      setError(null);
                    }}
                    placeholder="abcd-efgh-jkmn"
                  />
                )}
              </Field>
            ) : (
              <CodeInput
                value={code}
                onChange={(v) => {
                  setCode(v);
                  setError(null);
                }}
                onComplete={(v) => void submit(v)}
                disabled={busy}
                invalid={!!error}
                autoFocus
              />
            )}

            {error && (
              <div className="rounded-lg border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700 dark:border-red-500/30 dark:bg-red-500/10 dark:text-red-300" role="alert">
                {error}
              </div>
            )}

            <Button
              type="submit"
              variant="primary"
              size="lg"
              className="w-full"
              loading={busy}
              disabled={step === "credentials" ? !email || !password : step === "change" ? !changeReady : !totpReady}
            >
              {step === "credentials" ? "Anmelden" : step === "change" ? "Passwort speichern" : "Bestätigen"}
            </Button>

            {step === "totp" && (
              <button
                type="button"
                onClick={() => {
                  setUseRecovery((v) => !v);
                  setCode("");
                  setRecovery("");
                  setError(null);
                }}
                className="mx-auto block text-sm text-indigo-600 hover:text-indigo-800 dark:text-indigo-400 dark:hover:text-indigo-300"
              >
                {useRecovery ? "Code aus der Authenticator-App verwenden" : "Wiederherstellungscode verwenden"}
              </button>
            )}

            {step === "totp" && (
              <button
                type="button"
                onClick={() => {
                  setStep("credentials");
                  setCode("");
                  setRecovery("");
                  setUseRecovery(false);
                  setError(null);
                }}
                className="mx-auto flex items-center gap-1.5 text-sm text-zinc-500 hover:text-zinc-800 dark:text-zinc-400 dark:hover:text-zinc-200"
              >
                <ArrowLeft className="h-4 w-4" /> Zurück
              </button>
            )}
          </form>
        </div>

        <p className="mt-6 text-center text-xs text-zinc-400 dark:text-zinc-500">
          Für IMAP, SMTP und mobile Geräte bitte App-Passwörter verwenden.
        </p>
      </div>
    </div>
  );
}
