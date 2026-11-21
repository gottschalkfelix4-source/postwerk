import { useEffect, useState } from "react";
import { Eraser } from "lucide-react";
import { Button, Card, CardBody, CardFooter, CardHeader, Field, SwitchRow } from "@/components/ui";
import { NumberInput } from "./sections";
import type { SectionProps } from "./SettingsPage";

/** Housekeeping of retired DKIM keys and DAV tombstones (settings.server.retention), shown on the server page. */
export function CleanupCard({ settings, save, saving }: SectionProps) {
  const ret = settings.server.retention;
  const curDkim = ret?.dkimCleanup ?? true;
  const curDays = ret?.davTombstoneDays ?? 90;
  const [dkim, setDkim] = useState(curDkim);
  const [days, setDays] = useState(curDays);
  useEffect(() => {
    setDkim(curDkim);
    setDays(curDays);
  }, [curDkim, curDays]);
  const dirty = dkim !== curDkim || days !== curDays;
  const invalid = days !== 0 && (days < 7 || days > 3650);

  return (
    <Card className="mt-6">
      <CardHeader
        icon={<Eraser className="h-5 w-5 text-zinc-400" />}
        title="Automatisch aufräumen"
        description="Einmal täglich entfernt Postwerk Daten, die nicht mehr gebraucht werden."
      />
      <CardBody className="space-y-5">
        <SwitchRow
          label="Ausgemusterte DKIM-Schlüssel löschen"
          description="Nach einem Schlüsselwechsel bleibt der alte Schlüssel 7 Tage veröffentlicht. Weitere 7 Tage später wird er gelöscht; den zugehörigen TXT-Eintrag bietet „Automatisch einrichten“ zum Entfernen an."
          checked={dkim}
          onChange={setDkim}
        />
        <Field
          label="Gelöschte Kalender- und Kontakteinträge merken"
          help="Synchronisierende Apps (CalDAV/CardDAV) erfahren so von Löschungen. War eine App länger nicht verbunden, lädt sie danach einmal alles neu. 0 = nie vergessen, sonst 7 bis 3650 Tage."
          error={invalid ? "0 oder 7 bis 3650 Tage" : undefined}
        >
          {(id) => <NumberInput id={id} value={days} min={0} max={3650} step={1} suffix="Tage" onChange={(v) => setDays(Math.max(0, Math.round(v)))} />}
        </Field>
      </CardBody>
      <CardFooter>
        <Button
          variant="primary"
          size="sm"
          disabled={!dirty || invalid}
          loading={saving}
          onClick={() => save({ server: { retention: { dkimCleanup: dkim, davTombstoneDays: days } } })}
        >
          Speichern
        </Button>
      </CardFooter>
    </Card>
  );
}
