import { useMutation, useQuery } from "@tanstack/react-query";
import { BellRing, CircleCheck, CircleX, Send } from "lucide-react";
import { limitsApi, type NotificationTestResult } from "@/api/limits";
import type { NotificationCategory, NotificationSettings, SettingsUpdate } from "@/api/types";
import { useDraft } from "@/lib/hooks";
import { formatDateTime } from "@/lib/format";
import {
  Badge,
  Button,
  Card,
  CardBody,
  CardHeader,
  Checkbox,
  ChipInput,
  Field,
  FormSection,
  Input,
  Notice,
  Select,
  SwitchRow,
  useToast,
} from "@/components/ui";
import { SecretInput, Shell } from "./sections";
import type { SectionProps } from "./SettingsPage";

const categories: { id: NotificationCategory; label: string; help: string }[] = [
  { id: "certificate", label: "Zertifikat", help: "läuft ab, abgelaufen, selbstsigniert, falscher Hostname" },
  { id: "backup", label: "Sicherung", help: "fehlgeschlagen oder zu alt" },
  { id: "queue", label: "Warteschlange", help: "Nachrichten hängen länger als eine Stunde" },
  { id: "blacklist", label: "Sperrlisten", help: "eigene IP auf einer DNS-Blacklist" },
  { id: "dns", label: "DNS", help: "Reverse-DNS (PTR) passt nicht" },
  { id: "sendlimit", label: "Sendelimits", help: "ein Konto hat ein Sendelimit erreicht" },
  { id: "system", label: "Sonstiges", help: "alle übrigen Warnungen" },
];

const emailRe = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
const fallback: NotificationSettings = {
  enabled: false,
  recipients: [],
  categories: categories.map((c) => c.id),
  minSeverity: "warning",
  webhookUrl: "",
  webhookToken: "",
  telegram: { enabled: false, botToken: "", chatId: "" },
  ntfy: { enabled: false, server: "", topic: "", token: "", priority: 3 },
};

/** Settings section "notifications": operational warnings by mail and webhook. */
export function NotificationsSection({ settings, save, saving }: SectionProps) {
  const { draft, setDraft, dirty, reset } = useDraft<NotificationSettings>(settings.notifications ?? fallback);
  if (!draft) return null;
  const set = (patch: Partial<NotificationSettings>) => setDraft((d) => ({ ...d, ...patch }));
  const toggle = (c: NotificationCategory, on: boolean) =>
    set({ categories: on ? [...draft.categories.filter((x) => x !== c), c] : draft.categories.filter((x) => x !== c) });
  const urlInvalid = draft.webhookUrl.trim() !== "" && !/^https?:\/\/[^/\s]+/i.test(draft.webhookUrl.trim());
  const onSave = () => {
    const notifications: NonNullable<SettingsUpdate["notifications"]> = { ...draft, webhookUrl: draft.webhookUrl.trim() };
    if (draft.webhookToken === settings.notifications?.webhookToken) delete notifications.webhookToken;
    save({ notifications });
  };

  return (
    <>
      <Shell
        title="Benachrichtigungen"
        description="Probleme aus dem Dashboard per Mail (und optional Webhook, Telegram oder ntfy) melden – z. B. ablaufende Zertifikate, fehlgeschlagene Sicherungen oder eine gesperrte IP."
        dirty={dirty && !urlInvalid}
        saving={saving}
        onReset={reset}
        onSave={onSave}
      >
        <FormSection
          title="Versand"
          description="Jede neue Warnung wird einmal gemeldet, solange sie besteht höchstens einmal pro Tag erneut, und sobald sie behoben ist mit einer kurzen Entwarnung."
        >
          <SwitchRow label="Benachrichtigungen aktivieren" checked={draft.enabled} onChange={(v) => set({ enabled: v })} />
          <Field label="Empfänger" help="Leer = alle Admin-Konten. Externe Adressen werden über die normale Warteschlange zugestellt.">
            {(id) => (
              <ChipInput
                id={id}
                value={draft.recipients}
                onChange={(v) => set({ recipients: v })}
                placeholder="admin@example.com"
                transform={(v) => v.trim().toLowerCase()}
                validate={(v) => (emailRe.test(v) ? null : `„${v}“ ist keine gültige Adresse.`)}
              />
            )}
          </Field>
          <Field label="Mindestschwere">
            {(id) => (
              <Select
                id={id}
                value={draft.minSeverity}
                onChange={(e) => set({ minSeverity: e.target.value as NotificationSettings["minSeverity"] })}
                options={[
                  { value: "warning", label: "Warnungen und Fehler" },
                  { value: "error", label: "Nur Fehler" },
                ]}
                className="max-w-xs"
              />
            )}
          </Field>
        </FormSection>
        <FormSection title="Kategorien" description="Welche Arten von Warnungen gemeldet werden.">
          <div className="space-y-2.5">
            {categories.map((c) => (
              <label key={c.id} className="flex cursor-pointer items-start gap-2.5 text-sm">
                <Checkbox className="mt-0.5" checked={draft.categories.includes(c.id)} onChange={(e) => toggle(c.id, e.target.checked)} />
                <span>
                  <span className="font-medium text-zinc-800 dark:text-zinc-200">{c.label}</span>
                  <span className="ml-1.5 text-xs text-zinc-500 dark:text-zinc-400">{c.help}</span>
                </span>
              </label>
            ))}
          </div>
        </FormSection>
        <FormSection
          title="Webhook"
          description="Optional: zusätzlich ein JSON-POST pro Meldung (Felder title, message, priority, severity, event …). Passt z. B. zu Gotify; für ntfy einen JSON-fähigen Empfänger verwenden."
        >
          <Field label="Webhook-URL" error={urlInvalid ? "Die URL muss mit http:// oder https:// beginnen." : undefined}>
            {(id) => (
              <Input
                id={id}
                value={draft.webhookUrl}
                onChange={(e) => set({ webhookUrl: e.target.value })}
                placeholder="https://gotify.example.com/message"
                className="font-mono text-[13px]"
              />
            )}
          </Field>
          <Field label="Token" help="Optional, wird als „Authorization: Bearer …“ gesendet.">
            {(id) => <SecretInput id={id} value={draft.webhookToken} onChange={(v) => set({ webhookToken: v })} />}
          </Field>
        </FormSection>
        <FormSection
          title="Telegram & ntfy"
          description="Optional zusätzliche Push-Kanäle: ein Telegram-Bot oder ein ntfy.sh-kompatibler Server (z. B. selbst gehostet). Beide sind standardmäßig aus."
        >
          <SwitchRow
            label="Telegram aktivieren"
            checked={draft.telegram.enabled}
            onChange={(v) => set({ telegram: { ...draft.telegram, enabled: v } })}
          />
          <div className={draft.telegram.enabled ? "" : "pointer-events-none opacity-50"}>
            <Field label="Bot-Token" help="Von @BotFather, Form „123456:ABC-DEF…“.">
              {(id) => (
                <SecretInput
                  id={id}
                  value={draft.telegram.botToken}
                  onChange={(v) => set({ telegram: { ...draft.telegram, botToken: v } })}
                />
              )}
            </Field>
            <Field label="Chat-ID" help="Ziel-Chat oder -Kanal, z. B. 123456789 oder @meinkanal.">
              {(id) => (
                <Input
                  id={id}
                  value={draft.telegram.chatId}
                  onChange={(e) => set({ telegram: { ...draft.telegram, chatId: e.target.value } })}
                  placeholder="123456789"
                />
              )}
            </Field>
          </div>
          <SwitchRow
            label="ntfy aktivieren"
            checked={draft.ntfy.enabled}
            onChange={(v) => set({ ntfy: { ...draft.ntfy, enabled: v } })}
          />
          <div className={draft.ntfy.enabled ? "" : "pointer-events-none opacity-50"}>
            <Field label="Server" help="z. B. https://ntfy.sh oder ein selbst gehosteter Server.">
              {(id) => (
                <Input
                  id={id}
                  value={draft.ntfy.server}
                  onChange={(e) => set({ ntfy: { ...draft.ntfy, server: e.target.value } })}
                  placeholder="https://ntfy.sh"
                />
              )}
            </Field>
            <Field label="Thema (Topic)">
              {(id) => (
                <Input
                  id={id}
                  value={draft.ntfy.topic}
                  onChange={(e) => set({ ntfy: { ...draft.ntfy, topic: e.target.value } })}
                  placeholder="postwerk-alerts"
                />
              )}
            </Field>
            <Field label="Token" help="Optional für private Themen („Authorization: Bearer …“).">
              {(id) => <SecretInput id={id} value={draft.ntfy.token} onChange={(v) => set({ ntfy: { ...draft.ntfy, token: v } })} />}
            </Field>
            <Field label="Priorität (1–5)">
              {(id) => (
                <Input
                  id={id}
                  type="number"
                  min={1}
                  max={5}
                  value={draft.ntfy.priority}
                  onChange={(e) => set({ ntfy: { ...draft.ntfy, priority: Number(e.target.value) || 0 } })}
                />
              )}
            </Field>
          </div>
        </FormSection>
      </Shell>
      <NotificationTestCard dirty={dirty} />
    </>
  );
}

function NotificationTestCard({ dirty }: { dirty: boolean }) {
  const toast = useToast();
  const state = useQuery({ queryKey: ["admin", "notifications", "state"], queryFn: limitsApi.notificationState, refetchInterval: 60_000 });
  const test = useMutation({
    mutationFn: limitsApi.testNotification,
    onError: (err) => toast.error(err, "Testbenachrichtigung fehlgeschlagen"),
  });
  const res: NotificationTestResult | undefined = test.data;
  return (
    <Card className="mt-6">
      <CardHeader
        icon={<BellRing className="h-5 w-5 text-zinc-400" />}
        title="Test und Status"
        description="Die Testbenachrichtigung nutzt die gespeicherten Einstellungen – auch wenn Benachrichtigungen noch ausgeschaltet sind."
        actions={
          <Button size="sm" icon={Send} loading={test.isPending} disabled={dirty} onClick={() => test.mutate()}>
            Testbenachrichtigung senden
          </Button>
        }
      />
      <CardBody className="space-y-4">
        {dirty && <p className="text-xs text-zinc-500 dark:text-zinc-400">Bitte zuerst speichern.</p>}
        {res && (
          <div className="space-y-2">
            {res.mailError ? (
              <Notice tone="error" icon={CircleX} title="Mail nicht versendet">
                {res.mailError}
              </Notice>
            ) : (
              <Notice tone="success" icon={CircleCheck} title="Mail versendet">
                An {res.recipients.join(", ")}. Externe Empfänger erhalten sie über die Warteschlange.
              </Notice>
            )}
            {res.webhook &&
              (res.webhookError ? (
                <Notice tone="error" icon={CircleX} title="Webhook fehlgeschlagen">
                  {res.webhookError}
                </Notice>
              ) : (
                <Notice tone="success" icon={CircleCheck} title="Webhook erfolgreich aufgerufen" />
              ))}
            {res.telegram &&
              (res.telegramError ? (
                <Notice tone="error" icon={CircleX} title="Telegram fehlgeschlagen">
                  {res.telegramError}
                </Notice>
              ) : (
                <Notice tone="success" icon={CircleCheck} title="Telegram-Nachricht gesendet" />
              ))}
            {res.ntfy &&
              (res.ntfyError ? (
                <Notice tone="error" icon={CircleX} title="ntfy fehlgeschlagen">
                  {res.ntfyError}
                </Notice>
              ) : (
                <Notice tone="success" icon={CircleCheck} title="ntfy-Benachrichtigung gesendet" />
              ))}
          </div>
        )}
        <div>
          <h3 className="mb-2 text-sm font-semibold text-zinc-900 dark:text-zinc-100">Gemeldete, noch aktive Warnungen</h3>
          {state.data && state.data.length > 0 ? (
            <ul className="divide-y divide-zinc-100 text-sm dark:divide-zinc-800">
              {state.data.map((s) => (
                <li key={s.key} className="flex flex-wrap items-center justify-between gap-2 py-2">
                  <span className="flex min-w-0 items-center gap-2">
                    <Badge tone={s.severity === "error" ? "red" : "amber"}>{s.severity === "error" ? "Fehler" : "Warnung"}</Badge>
                    <span className="truncate text-zinc-800 dark:text-zinc-200">{s.title}</span>
                  </span>
                  <span className="text-xs text-zinc-500 dark:text-zinc-400">
                    seit {formatDateTime(s.firstSeen)} · zuletzt gemeldet {formatDateTime(s.lastNotified)}
                  </span>
                </li>
              ))}
            </ul>
          ) : (
            <p className="text-sm text-zinc-500 dark:text-zinc-400">Keine.</p>
          )}
        </div>
      </CardBody>
    </Card>
  );
}
