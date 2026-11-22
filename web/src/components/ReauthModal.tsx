import { useEffect, useState } from "react";
import { useMe } from "@/lib/auth";
import { Button, Field, Input, Modal, PasswordInput } from "@/components/ui";

export interface ReauthValues {
  password: string;
  code: string;
}

/**
 * Asks for the account password (and 2FA code) before a sensitive action.
 * SSO accounts have no password; for them a fresh SSO login confirms.
 */
export function ReauthModal({
  open,
  title,
  description,
  confirmLabel,
  danger,
  loading,
  onClose,
  onConfirm,
}: {
  open: boolean;
  title: string;
  description?: string;
  confirmLabel: string;
  danger?: boolean;
  loading?: boolean;
  onClose: () => void;
  onConfirm: (v: ReauthValues) => void;
}) {
  const me = useMe();
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  useEffect(() => {
    if (!open) {
      setPassword("");
      setCode("");
    }
  }, [open]);
  const sso = me.source === "oidc";
  const ready = sso || (password.length > 0 && (!me.totpEnabled || code.trim().length > 0));
  const submit = () => {
    if (ready) onConfirm({ password, code: code.trim() });
  };
  return (
    <Modal
      open={open}
      onClose={onClose}
      size="sm"
      title={title}
      description={
        (description ? description + " " : "") +
        (sso
          ? "Zur Sicherheit ist das nur bis 15 Minuten nach einer Single-Sign-on-Anmeldung möglich – sonst bitte ab- und wieder anmelden."
          : "Zur Sicherheit bitte mit deinem Passwort bestätigen.")
      }
      footer={
        <>
          <Button onClick={onClose}>Abbrechen</Button>
          <Button variant={danger ? "danger" : "primary"} disabled={!ready} loading={loading} onClick={submit}>
            {confirmLabel}
          </Button>
        </>
      }
    >
      {!sso && (
        <form
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault();
            submit();
          }}
        >
          <Field label="Dein Passwort">
            {(id) => <PasswordInput id={id} value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" autoFocus />}
          </Field>
          {me.totpEnabled && (
            <Field label="Code" help="6-stelliger Code aus der Authenticator-App oder ein Wiederherstellungscode.">
              {(id) => <Input id={id} value={code} onChange={(e) => setCode(e.target.value)} autoComplete="one-time-code" placeholder="123456" />}
            </Field>
          )}
          <button type="submit" className="hidden" />
        </form>
      )}
    </Modal>
  );
}
