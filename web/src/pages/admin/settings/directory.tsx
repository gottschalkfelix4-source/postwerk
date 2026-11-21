import { useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CircleCheck, CircleX, Info, PlugZap, RefreshCw, TriangleAlert, UserPlus, Zap } from "lucide-react";
import { ssoApi, type LdapSyncResult, type LdapSyncStatus, type LdapTestResult, type OidcTestResult } from "@/api/sso";
import type { LdapSettings, OidcSettings } from "@/api/types";
import { useDraft } from "@/lib/hooks";
import { cn } from "@/lib/utils";
import { formatBytes } from "@/lib/format";
import {
  Badge,
  Button,
  ChipInput,
  CopyButton,
  Field,
  FormSection,
  Input,
  Notice,
  SwitchRow,
  useConfirm,
  useToast,
  type BadgeTone,
} from "@/components/ui";
import { QuotaInput } from "@/pages/admin/shared";
import { SecretInput, Shell } from "./sections";
import type { SectionProps } from "./SettingsPage";

const mono = "font-mono text-[13px]";

function ResultBox({ ok, title, latencyMs, error, children }: { ok: boolean; title: string; latencyMs?: number; error?: string; children?: ReactNode }) {
  return (
    <div
      className={cn(
        "rounded-xl border p-4",
        ok
          ? "border-emerald-200 bg-emerald-50/60 dark:border-emerald-500/30 dark:bg-emerald-500/[0.07]"
          : "border-red-200 bg-red-50/60 dark:border-red-500/30 dark:bg-red-500/[0.07]",
      )}
    >
      <div className="flex flex-wrap items-center gap-2">
        {ok ? <CircleCheck className="h-5 w-5 text-emerald-600" /> : <CircleX className="h-5 w-5 text-red-600" />}
        <span className="text-sm font-semibold text-zinc-900 dark:text-zinc-100">{title}</span>
        {ok && latencyMs !== undefined && <Badge tone="green">{latencyMs} ms</Badge>}
      </div>
      {error && <p className="mt-2 break-words font-mono text-xs text-red-700 dark:text-red-300">{error}</p>}
      {children}
    </div>
  );
}

function Pending({ label }: { label: string }) {
  return (
    <div className="flex items-center gap-3 rounded-xl border border-zinc-200 bg-zinc-50 p-4 text-sm text-zinc-600 dark:border-zinc-800 dark:bg-zinc-950/50 dark:text-zinc-400">
      <Zap className="h-4 w-4 animate-pulse text-indigo-500" /> {label}
    </div>
  );
}

// ---------------------------------------------------------------------------
// LDAP

export function LdapSection({ settings, save, saving }: SectionProps) {
  const { draft, setDraft, dirty, reset } = useDraft<LdapSettings>(settings.ldap);
  if (!draft) return null;
  const set = (patch: Partial<LdapSettings>) => setDraft((d) => ({ ...d, ...patch }));
  const ldaps = draft.url.trim().toLowerCase().startsWith("ldaps://");
  const off = !draft.enabled;
  return (
    <Shell
      title="Verzeichnis (LDAP)"
      description="Konten aus einem LDAP-Verzeichnis (OpenLDAP, Active Directory, lldap, Authentik/Keycloak-LDAP) anmelden lassen."
      dirty={dirty}
      saving={saving}
      onReset={reset}
      // The masked bind password is sent as is: the server keeps it only for an unchanged URL.
      onSave={() => save({ ldap: draft })}
    >
      <FormSection
        title="Verbindung"
        description="Postwerk sucht Benutzer mit einem Dienstkonto und prüft das Passwort durch eine Anmeldung als dieser Benutzer."
      >
        <SwitchRow label="LDAP-Anmeldung aktivieren" checked={draft.enabled} onChange={(v) => set({ enabled: v })} />
        <Field label="Server-URL" help="ldaps://ldap.example.org:636 oder ldap://ldap.example.org:389 (dann StartTLS empfohlen)">
          {(id) => <Input id={id} value={draft.url} onChange={(e) => set({ url: e.target.value })} placeholder="ldaps://ldap.example.org" className={mono} disabled={off} />}
        </Field>
        <SwitchRow
          label="StartTLS"
          description="Verbindung über ldap:// nach dem Aufbau verschlüsseln."
          checked={draft.startTls && !ldaps}
          disabled={off || ldaps}
          onChange={(v) => set({ startTls: v })}
        />
        <SwitchRow
          label="Zertifikat nicht prüfen"
          description="Nur für Tests mit selbstsignierten Zertifikaten."
          checked={draft.skipTlsVerify}
          disabled={off}
          onChange={(v) => set({ skipTlsVerify: v })}
        />
        {draft.skipTlsVerify && (
          <Notice tone="warning" icon={TriangleAlert} title="Unsichere Verbindung">
            Ohne Zertifikatsprüfung kann ein Angreifer im Netzwerk die Passwörter aller Benutzer mitlesen. Besser das Zertifikat des Servers vertrauenswürdig machen.
          </Notice>
        )}
        {!draft.skipTlsVerify && !ldaps && !draft.startTls && draft.url.trim() !== "" && (
          <Notice tone="warning" icon={TriangleAlert}>
            Ohne ldaps:// oder StartTLS werden Passwörter unverschlüsselt zum Verzeichnis übertragen.
          </Notice>
        )}
        <Field label="Bind-DN (Dienstkonto)" help="Leer lassen für anonyme Suche.">
          {(id) => <Input id={id} value={draft.bindDn} onChange={(e) => set({ bindDn: e.target.value })} placeholder="cn=postwerk,ou=services,dc=example,dc=org" className={mono} disabled={off} />}
        </Field>
        <Field label="Bind-Passwort">{(id) => <SecretInput id={id} value={draft.bindPassword} onChange={(v) => set({ bindPassword: v })} />}</Field>
        <Field label="Basis-DN" help="Hier wird nach Benutzern gesucht (inklusive Unterebenen).">
          {(id) => <Input id={id} value={draft.baseDn} onChange={(e) => set({ baseDn: e.target.value })} placeholder="ou=people,dc=example,dc=org" className={mono} disabled={off} />}
        </Field>
      </FormSection>

      <FormSection title="Benutzer & Attribute" description="Wie Benutzer gefunden und auf Postwerk-Konten abgebildet werden.">
        <Field label="Benutzerfilter" help="%s steht für die (maskierte) E-Mail-Adresse. Active Directory: (&(objectClass=user)(mail=%s))">
          {(id) => <Input id={id} value={draft.userFilter} onChange={(e) => set({ userFilter: e.target.value })} className={mono} disabled={off} />}
        </Field>
        <div className="grid gap-4 sm:grid-cols-3">
          <Field label="E-Mail-Attribut">
            {(id) => <Input id={id} value={draft.mailAttribute} onChange={(e) => set({ mailAttribute: e.target.value })} placeholder="mail" className={mono} disabled={off} />}
          </Field>
          <Field label="Anzeigename">
            {(id) => <Input id={id} value={draft.nameAttribute} onChange={(e) => set({ nameAttribute: e.target.value })} placeholder="cn" className={mono} disabled={off} />}
          </Field>
          <Field label="Speicherplatz (optional)" help="Byte oder z. B. 5G">
            {(id) => <Input id={id} value={draft.quotaAttribute} onChange={(e) => set({ quotaAttribute: e.target.value })} placeholder="mailQuota" className={mono} disabled={off} />}
          </Field>
        </div>
        <Field label="Admin-Filter (optional)" help="Passende Benutzer werden Administratoren, alle anderen normale Benutzer. Leer = Rolle wird in Postwerk verwaltet.">
          {(id) => (
            <Input
              id={id}
              value={draft.adminFilter}
              onChange={(e) => set({ adminFilter: e.target.value })}
              placeholder="memberOf=cn=mail-admins,ou=groups,dc=example,dc=org"
              className={mono}
              disabled={off}
            />
          )}
        </Field>
      </FormSection>

      <FormSection title="Konten" description="Was beim ersten Login und bei entfernten Benutzern passiert.">
        <SwitchRow
          label="Konten automatisch anlegen"
          description="Beim ersten erfolgreichen Login wird das Postfach angelegt – sofern die Domain in Postwerk existiert."
          checked={draft.autoProvision}
          disabled={off}
          onChange={(v) => set({ autoProvision: v })}
        />
        <Field label="Speicherplatz für neue Konten" help="Gilt, wenn kein Speicherplatz-Attribut gesetzt ist. 0 = unbegrenzt.">
          {(id) => <QuotaInput id={id} value={draft.defaultQuotaBytes} onChange={(v) => set({ defaultQuotaBytes: v })} />}
        </Field>
        <SwitchRow
          label="Entfernte Benutzer deaktivieren"
          description="Wird ein Benutzer im Verzeichnis nicht mehr gefunden, wird sein Postwerk-Konto deaktiviert (Anmeldung ist so oder so nicht mehr möglich)."
          checked={draft.syncDisabled}
          disabled={off}
          onChange={(v) => set({ syncDisabled: v })}
        />
        <Notice tone="info" icon={Info}>
          LDAP-Benutzer melden sich auch bei IMAP, SMTP und POP3 mit ihrem Verzeichnis-Passwort an. App-Passwörter und die Zwei-Faktor-Anmeldung bleiben in
          Postwerk – mit aktiver 2FA sind für Mail-Programme App-Passwörter nötig. Das Passwort selbst wird im Verzeichnis geändert.
        </Notice>
      </FormSection>

      <LdapTestPanel draft={draft} />
      <LdapSyncPanel dirty={dirty} enabled={settings.ldap.enabled} />
    </Shell>
  );
}

function LdapTestPanel({ draft }: { draft: LdapSettings }) {
  const [email, setEmail] = useState("");
  const [result, setResult] = useState<LdapTestResult | null>(null);
  const toast = useToast();
  const test = useMutation({
    mutationFn: () => ssoApi.testLdap(email.trim(), draft),
    onSuccess: setResult,
    onError: (err) => {
      setResult(null);
      toast.error(err, "Test fehlgeschlagen");
    },
  });
  const attrs = result?.entry?.attributes ? Object.entries(result.entry.attributes).sort(([a], [b]) => a.localeCompare(b)) : [];
  return (
    <FormSection title="Test" description="Prüft Verbindung und Dienstkonto mit den Werten im Formular (auch ungespeichert) und sucht optional einen Benutzer.">
      <div className="flex flex-wrap items-end gap-2">
        <Field label="E-Mail-Adresse (optional)" className="min-w-[16rem] flex-1">
          {(id) => <Input id={id} type="email" value={email} onChange={(e) => setEmail(e.target.value)} placeholder="anna@example.org" />}
        </Field>
        <Button icon={PlugZap} loading={test.isPending} onClick={() => test.mutate()} disabled={!draft.url.trim()}>
          {email.trim() ? "Benutzer suchen" : "Verbindung testen"}
        </Button>
      </div>
      {test.isPending ? (
        <Pending label="Verzeichnis wird abgefragt …" />
      ) : result ? (
        <ResultBox
          ok={result.ok}
          latencyMs={result.latencyMs}
          error={result.error}
          title={result.ok ? (result.entry ? "Benutzer gefunden" : "Verbindung und Dienstkonto in Ordnung") : "Test fehlgeschlagen"}
        >
          {result.entry && (
            <div className="mt-3 space-y-3 text-sm">
              <dl className="grid gap-x-4 gap-y-1 sm:grid-cols-[10rem_1fr]">
                <dt className="text-zinc-500 dark:text-zinc-400">DN</dt>
                <dd className="break-all font-mono text-xs">{result.entry.dn}</dd>
                <dt className="text-zinc-500 dark:text-zinc-400">Name</dt>
                <dd>{result.entry.name || "–"}</dd>
                <dt className="text-zinc-500 dark:text-zinc-400">Speicherplatz</dt>
                <dd>{result.entry.hasQuota ? formatBytes(result.entry.quotaBytes) : "– (Standardwert)"}</dd>
                <dt className="text-zinc-500 dark:text-zinc-400">Rolle</dt>
                <dd>{result.entry.admin ? <Badge tone="indigo">Administrator</Badge> : "Benutzer"}</dd>
                <dt className="text-zinc-500 dark:text-zinc-400">Postwerk-Konto</dt>
                <dd>
                  {result.account ? (
                    <Link to={`/admin/accounts/${result.account.id}`} className="text-indigo-600 hover:underline dark:text-indigo-400">
                      vorhanden ({result.account.source === "ldap" ? "LDAP" : result.account.source === "oidc" ? "SSO" : "lokales Passwort"})
                    </Link>
                  ) : (
                    "noch nicht angelegt"
                  )}
                </dd>
              </dl>
              {attrs.length > 0 && (
                <details className="rounded-lg border border-zinc-200 bg-white/70 dark:border-zinc-800 dark:bg-zinc-900/50">
                  <summary className="cursor-pointer px-3 py-2 text-xs font-medium text-zinc-600 dark:text-zinc-300">Alle Attribute ({attrs.length})</summary>
                  <table className="w-full text-xs">
                    <tbody>
                      {attrs.map(([k, v]) => (
                        <tr key={k} className="border-t border-zinc-100 align-top dark:border-zinc-800">
                          <td className="whitespace-nowrap px-3 py-1.5 font-mono text-zinc-500 dark:text-zinc-400">{k}</td>
                          <td className="break-all px-3 py-1.5 font-mono">{v.join(", ")}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </details>
              )}
            </div>
          )}
        </ResultBox>
      ) : null}
    </FormSection>
  );
}

const syncLabels: Record<LdapSyncStatus, [string, BadgeTone]> = {
  exists: ["vorhanden", "green"],
  created: ["angelegt", "green"],
  other_source: ["lokales Konto", "amber"],
  missing: ["fehlt", "blue"],
  no_domain: ["Domain fehlt", "neutral"],
  alias: ["ist ein Alias", "neutral"],
};

function LdapSyncPanel({ dirty, enabled }: { dirty: boolean; enabled: boolean }) {
  const [result, setResult] = useState<LdapSyncResult | null>(null);
  const toast = useToast();
  const confirm = useConfirm();
  const qc = useQueryClient();
  const sync = useMutation({
    mutationFn: (apply: boolean) => ssoApi.syncLdap(apply),
    onSuccess: (r, apply) => {
      setResult(r);
      if (apply) {
        toast.success("Abgleich ausgeführt", `${r.created} Konten angelegt, ${r.disabled} deaktiviert.`);
        void qc.invalidateQueries({ queryKey: ["admin"] });
      }
    },
    onError: (err) => toast.error(err, "Abgleich fehlgeschlagen"),
  });
  const missing = result?.users.filter((u) => u.status === "missing").length ?? 0;
  const orphansActive = result?.orphans.filter((o) => !o.disabled).length ?? 0;
  return (
    <FormSection title="Abgleich" description="Zeigt alle Verzeichnis-Benutzer mit E-Mail-Adresse und welche davon schon ein Postwerk-Konto haben (gespeicherte Einstellungen).">
      <div className="flex flex-wrap gap-2">
        <Button icon={RefreshCw} loading={sync.isPending && sync.variables === false} disabled={!enabled} onClick={() => sync.mutate(false)}>
          Abgleich anzeigen
        </Button>
        {result && (missing > 0 || orphansActive > 0) && (
          <Button
            variant="primary"
            icon={UserPlus}
            loading={sync.isPending && sync.variables === true}
            onClick={async () => {
              const ok = await confirm({
                title: "Abgleich ausführen?",
                message: `${missing} fehlende Konten werden angelegt.${orphansActive > 0 ? ` ${orphansActive} Konten ohne Verzeichnis-Eintrag werden deaktiviert (falls eingeschaltet).` : ""}`,
                confirmLabel: "Ausführen",
              });
              if (ok) sync.mutate(true);
            }}
          >
            Abgleich ausführen
          </Button>
        )}
      </div>
      {!enabled && <p className="text-xs text-zinc-500 dark:text-zinc-400">LDAP zuerst aktivieren und speichern.</p>}
      {dirty && enabled && (
        <p className="flex items-center gap-1.5 text-xs text-zinc-500 dark:text-zinc-400">
          <Info className="h-3.5 w-3.5" /> Der Abgleich verwendet die gespeicherte Konfiguration.
        </p>
      )}
      {result && (
        <div className="space-y-3">
          {result.truncated && <Notice tone="warning">Es werden nur die ersten 10.000 Benutzer angezeigt.</Notice>}
          <div className="max-h-96 overflow-auto rounded-lg border border-zinc-200 dark:border-zinc-800">
            <table className="w-full text-sm">
              <thead className="sticky top-0 bg-zinc-50 text-left text-xs text-zinc-500 dark:bg-zinc-900 dark:text-zinc-400">
                <tr>
                  <th className="px-3 py-2 font-medium">E-Mail</th>
                  <th className="px-3 py-2 font-medium">Name</th>
                  <th className="px-3 py-2 font-medium">Status</th>
                </tr>
              </thead>
              <tbody>
                {result.users.length === 0 && (
                  <tr>
                    <td colSpan={3} className="px-3 py-4 text-center text-zinc-500">
                      Keine Benutzer mit E-Mail-Adresse gefunden.
                    </td>
                  </tr>
                )}
                {result.users.map((u) => {
                  const [label, tone] = syncLabels[u.status];
                  return (
                    <tr key={u.email} className="border-t border-zinc-100 dark:border-zinc-800">
                      <td className="px-3 py-1.5 font-mono text-xs">
                        {u.accountId ? (
                          <Link to={`/admin/accounts/${u.accountId}`} className="hover:underline">
                            {u.email}
                          </Link>
                        ) : (
                          u.email
                        )}
                      </td>
                      <td className="px-3 py-1.5">
                        {u.name} {u.admin && <Badge tone="indigo">Admin</Badge>}
                      </td>
                      <td className="px-3 py-1.5">
                        <Badge tone={tone}>{label}</Badge>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
          {result.orphans.length > 0 && (
            <Notice tone="warning" icon={TriangleAlert} title="LDAP-Konten ohne Verzeichnis-Eintrag">
              {result.orphans.map((o) => `${o.email}${o.disabled ? " (deaktiviert)" : ""}`).join(", ")}
            </Notice>
          )}
        </div>
      )}
    </FormSection>
  );
}

// ---------------------------------------------------------------------------
// OIDC

export function OidcSection({ settings, save, saving }: SectionProps) {
  const { draft, setDraft, dirty, reset } = useDraft<OidcSettings>(settings.oidc);
  const info = useQuery({ queryKey: ["admin", "sso-info"], queryFn: ssoApi.info, staleTime: 30_000 });
  if (!draft) return null;
  const set = (patch: Partial<OidcSettings>) => setDraft((d) => ({ ...d, ...patch }));
  const off = !draft.enabled;
  const noLocalAdmin = info.data?.localAdmins === 0;
  return (
    <Shell
      title="Single Sign-on (OIDC)"
      description="Anmeldung an der Weboberfläche über einen OpenID-Connect-Anbieter wie Authentik, Keycloak, Authelia oder Zitadel."
      dirty={dirty}
      saving={saving}
      onReset={reset}
      onSave={() => save({ oidc: draft })}
    >
      <FormSection title="Beim Anbieter eintragen" description="Lege beim Identitätsanbieter einen vertraulichen OAuth2/OIDC-Client an und trage diese Adressen ein.">
        <UriRow label="Redirect-URI" value={info.data?.redirectUri} />
        <UriRow label="Post-Logout-Redirect-URI" value={info.data?.postLogoutRedirectUri} />
        {info.data && !info.data.publicUrlConfigured && (
          <Notice tone="warning" icon={TriangleAlert}>
            Die Adressen werden aus der aktuellen Browser-Adresse gebildet. Trage unter{" "}
            <Link to="/admin/settings/web" className="font-medium underline">
              Web & Proxy
            </Link>{" "}
            die öffentliche URL ein, damit sie stabil bleiben.
          </Notice>
        )}
      </FormSection>

      <FormSection title="Identitätsanbieter" description="Die Issuer-URL steht in der Discovery-Adresse vor /.well-known/openid-configuration.">
        <SwitchRow label="Single Sign-on aktivieren" checked={draft.enabled} onChange={(v) => set({ enabled: v })} />
        <Field label="Issuer-URL" help="Authentik: https://auth.example.org/application/o/postwerk/ · Keycloak: https://kc.example.org/realms/meinrealm">
          {(id) => <Input id={id} value={draft.issuerUrl} onChange={(e) => set({ issuerUrl: e.target.value })} placeholder="https://auth.example.org/application/o/postwerk/" className={mono} disabled={off} />}
        </Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Client-ID">
            {(id) => <Input id={id} value={draft.clientId} onChange={(e) => set({ clientId: e.target.value })} className={mono} disabled={off} />}
          </Field>
          <Field label="Client-Secret">{(id) => <SecretInput id={id} value={draft.clientSecret} onChange={(v) => set({ clientSecret: v })} />}</Field>
        </div>
        <Field label="Scopes" help="openid wird immer angefragt.">
          {(id) => <ChipInput id={id} value={draft.scopes} onChange={(v) => set({ scopes: v })} placeholder="z. B. groups" suggestions={["openid", "email", "profile", "groups"]} disabled={off} />}
        </Field>
        <OidcTestPanel issuerUrl={draft.issuerUrl} />
      </FormSection>

      <FormSection title="Anmeldung" description="Darstellung auf der Login-Seite und Prüfung der E-Mail-Adresse.">
        <Field label="Beschriftung des Buttons">
          {(id) => <Input id={id} value={draft.buttonLabel} maxLength={60} onChange={(e) => set({ buttonLabel: e.target.value })} placeholder="Mit Authentik anmelden" disabled={off} />}
        </Field>
        <Field label="Claim für die E-Mail-Adresse" help="Konten werden über diese Adresse zugeordnet.">
          {(id) => <Input id={id} value={draft.emailClaim} onChange={(e) => set({ emailClaim: e.target.value })} placeholder="email" className={cn(mono, "w-56")} disabled={off} />}
        </Field>
        <SwitchRow
          label="Unbestätigte E-Mail-Adressen erlauben"
          description="Standardmäßig muss der Anbieter email_verified=true senden."
          checked={draft.allowUnverifiedEmail}
          disabled={off}
          onChange={(v) => set({ allowUnverifiedEmail: v })}
        />
        {draft.allowUnverifiedEmail && (
          <Notice tone="warning" icon={TriangleAlert}>
            Nur einschalten, wenn Benutzer ihre E-Mail-Adresse beim Anbieter nicht selbst ändern können – sonst kann jemand fremde Postfächer übernehmen.
          </Notice>
        )}
        <SwitchRow
          label="Auch beim Anbieter abmelden"
          description="Beim Abmelden wird zusätzlich die Sitzung beim Identitätsanbieter beendet (end_session_endpoint)."
          checked={draft.rpLogout}
          disabled={off}
          onChange={(v) => set({ rpLogout: v })}
        />
      </FormSection>

      <FormSection title="Konten" description="Zuordnung und Anlage von Konten.">
        <SwitchRow
          label="Konten automatisch anlegen"
          description="Unbekannte Benutzer bekommen beim ersten Login ein Postfach – sofern die Domain in Postwerk existiert."
          checked={draft.autoProvision}
          disabled={off}
          onChange={(v) => set({ autoProvision: v })}
        />
        <Field label="Speicherplatz für neue Konten" help="0 = unbegrenzt.">
          {(id) => <QuotaInput id={id} value={draft.defaultQuotaBytes} onChange={(v) => set({ defaultQuotaBytes: v })} />}
        </Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Admin-Claim (optional)" help="z. B. groups">
            {(id) => <Input id={id} value={draft.adminClaim} onChange={(e) => set({ adminClaim: e.target.value })} placeholder="groups" className={mono} disabled={off} />}
          </Field>
          <Field label="Admin-Wert" help="z. B. mail-admins">
            {(id) => <Input id={id} value={draft.adminValue} onChange={(e) => set({ adminValue: e.target.value })} placeholder="mail-admins" className={mono} disabled={off} />}
          </Field>
        </div>
        <p className="text-xs text-zinc-500 dark:text-zinc-400">
          Die Admin-Rolle wird nur für per SSO angelegte Konten bei jeder Anmeldung aus dem Claim übernommen. Lokale und LDAP-Konten behalten ihre Rolle.
        </p>
      </FormSection>

      <FormSection title="Passwort-Anmeldung" description="Optional nur noch Single Sign-on für die Weboberfläche erlauben.">
        <SwitchRow
          label="Nur SSO erlauben"
          description="Die Anmeldung mit Passwort ist dann nur noch für lokale Administratorkonten möglich (Notzugang, falls der Anbieter ausfällt). IMAP und SMTP sind nicht betroffen."
          checked={draft.onlySso}
          disabled={off}
          onChange={(v) => set({ onlySso: v })}
        />
        {draft.onlySso && noLocalAdmin && (
          <Notice tone="error" icon={TriangleAlert}>
            Es gibt kein aktives lokales Administratorkonto. Lege zuerst eines an, sonst ist bei einem Ausfall des Anbieters kein Zugang mehr möglich.
          </Notice>
        )}
        <Notice tone="info" icon={Info}>
          Per SSO angemeldete Benutzer nutzen für IMAP, SMTP und CalDAV/CardDAV App-Passwörter (unter „Konto & Sicherheit“). Die Zwei-Faktor-Anmeldung übernimmt
          bei SSO der Identitätsanbieter – Postwerks eigene 2FA gilt nur für Passwort-Anmeldungen.
        </Notice>
      </FormSection>
    </Shell>
  );
}

function UriRow({ label, value }: { label: string; value?: string }) {
  return (
    <Field label={label}>
      {() => (
        <div className="flex items-center gap-2 rounded-lg border border-zinc-200 bg-zinc-50 px-3 py-2 dark:border-zinc-800 dark:bg-zinc-950/50">
          <code className="min-w-0 flex-1 break-all text-[13px] text-zinc-800 dark:text-zinc-200">{value ?? "…"}</code>
          {value && <CopyButton value={value} label="Kopieren" />}
        </div>
      )}
    </Field>
  );
}

function OidcTestPanel({ issuerUrl }: { issuerUrl: string }) {
  const [result, setResult] = useState<OidcTestResult | null>(null);
  const toast = useToast();
  const test = useMutation({
    mutationFn: () => ssoApi.testOidc(issuerUrl.trim()),
    onSuccess: setResult,
    onError: (err) => {
      setResult(null);
      toast.error(err, "Test fehlgeschlagen");
    },
  });
  const d = result?.discovery;
  const noPkce = d && d.pkceMethods && d.pkceMethods.length > 0 && !d.pkceMethods.includes("S256");
  return (
    <div className="space-y-3">
      <Button icon={PlugZap} size="sm" loading={test.isPending} disabled={!issuerUrl.trim()} onClick={() => test.mutate()}>
        Discovery testen
      </Button>
      {result && (
        <ResultBox ok={result.ok} latencyMs={result.latencyMs} error={result.error} title={result.ok ? "Anbieter gefunden" : "Discovery fehlgeschlagen"}>
          {d && (
            <dl className="mt-3 grid gap-x-4 gap-y-1 text-xs sm:grid-cols-[10rem_1fr]">
              <dt className="text-zinc-500 dark:text-zinc-400">Issuer</dt>
              <dd className="break-all font-mono">{d.issuer}</dd>
              <dt className="text-zinc-500 dark:text-zinc-400">Autorisierung</dt>
              <dd className="break-all font-mono">{d.authorizationEndpoint}</dd>
              <dt className="text-zinc-500 dark:text-zinc-400">Token</dt>
              <dd className="break-all font-mono">{d.tokenEndpoint}</dd>
              <dt className="text-zinc-500 dark:text-zinc-400">Abmeldung</dt>
              <dd className="break-all font-mono">{d.endSessionEndpoint || "– (keine Abmeldung beim Anbieter möglich)"}</dd>
            </dl>
          )}
          {noPkce && <p className="mt-2 text-xs text-amber-700 dark:text-amber-300">Der Anbieter meldet keine PKCE-Unterstützung mit S256.</p>}
        </ResultBox>
      )}
    </div>
  );
}
