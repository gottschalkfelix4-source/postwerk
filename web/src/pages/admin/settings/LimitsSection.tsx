import { Field, FormSection, Notice } from "@/components/ui";
import { useDraft } from "@/lib/hooks";
import type { SendLimitSettings } from "@/api/types";
import { NumberInput, Shell } from "./sections";
import type { SectionProps } from "./SettingsPage";

const fallback: SendLimitSettings = { maxRecipientsPerMessage: 100, maxMessagesPerHour: 300, maxRecipientsPerDay: 2000 };

/** Settings section "limits": default outbound send limits per account. */
export function LimitsSection({ settings, save, saving }: SectionProps) {
  const { draft, setDraft, dirty, reset } = useDraft<SendLimitSettings>(settings.limits ?? fallback);
  if (!draft) return null;
  const set = (patch: Partial<SendLimitSettings>) => setDraft((d) => ({ ...d, ...patch }));
  const invalid = draft.maxRecipientsPerMessage < 0 || draft.maxRecipientsPerMessage > 1000 || draft.maxMessagesPerHour < 0 || draft.maxRecipientsPerDay < 0;
  return (
    <Shell
      title="Sendelimits"
      description="Begrenzt, wie viel ein Konto versenden darf. Schützt den Ruf des Servers, falls ein Passwort in falsche Hände gerät."
      dirty={dirty && !invalid}
      saving={saving}
      onReset={reset}
      onSave={() => save({ limits: draft })}
    >
      <FormSection
        title="Standardwerte"
        description="Gelten für alle Konten, auch Admins. Abweichende Werte oder eine Ausnahme lassen sich pro Konto in der Kontoansicht festlegen. 0 = unbegrenzt."
      >
        <Field label="Empfänger pro Nachricht" help="Zählt alle Empfänger, auch lokale. Höchstens 1000. Über SMTP wird jeder weitere Empfänger mit 452 4.5.3 abgelehnt.">
          {(id) => <NumberInput id={id} value={draft.maxRecipientsPerMessage} min={0} max={1000} suffix="Empfänger" onChange={(v) => set({ maxRecipientsPerMessage: v })} />}
        </Field>
        <Field label="Nachrichten pro Stunde" help="Nur Nachrichten, die den Server verlassen (mindestens ein externer Empfänger). Gleitendes Fenster über die letzten 60 Minuten.">
          {(id) => <NumberInput id={id} value={draft.maxMessagesPerHour} min={0} suffix="Nachrichten" onChange={(v) => set({ maxMessagesPerHour: v })} />}
        </Field>
        <Field label="Externe Empfänger pro Tag" help="Summe aller externen Empfänger in den letzten 24 Stunden. Lokale Empfänger zählen nicht.">
          {(id) => <NumberInput id={id} value={draft.maxRecipientsPerDay} min={0} suffix="Empfänger" onChange={(v) => set({ maxRecipientsPerDay: v })} />}
        </Field>
        <Notice tone="info" title="Was gezählt wird">
          Versand über SMTP (Port 587/465), Webmail und JMAP sowie Sieve-Weiterleitungen an externe Adressen. Ist ein Limit erreicht,
          wird der Versand vorübergehend abgewiesen (450 4.7.1), im Dashboard gewarnt und – falls eingerichtet – eine Benachrichtigung verschickt.
        </Notice>
      </FormSection>
    </Shell>
  );
}
