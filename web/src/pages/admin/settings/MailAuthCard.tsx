import { useEffect, useState } from "react";
import { Forward } from "lucide-react";
import { Button, Card, CardBody, CardFooter, CardHeader, ChipInput, Field } from "@/components/ui";
import type { SectionProps } from "./SettingsPage";

const suggestions = ["google.com", "microsoft.com", "outlook.com", "icloud.com", "yahoo.com", "fastmail.com", "groups.io"];
const domainRe = /^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,}$/;

/** Trusted ARC sealers (settings.mailauth), shown below the spam filter settings. */
export function MailAuthCard({ settings, save, saving }: SectionProps) {
  const current = settings.mailauth?.trustedArcSealers ?? [];
  const [list, setList] = useState<string[]>(current);
  useEffect(() => setList(settings.mailauth?.trustedArcSealers ?? []), [settings.mailauth?.trustedArcSealers]);
  const dirty = list.join(",") !== current.join(",");

  return (
    <Card className="mt-6">
      <CardHeader
        icon={<Forward className="h-5 w-5 text-zinc-400" />}
        title="Weiterleitungen (ARC)"
        description="Weitergeleitete Mails und Mailinglisten brechen oft SPF und DKIM. Versiegelt ein vertrauenswürdiger Weiterleiter die Mail per ARC, wird eine DMARC-Ablehnung abgeschwächt: Hat er die Mail als echt bestätigt, wird sie zugestellt, sonst landet sie im Spam statt abgelehnt zu werden."
      />
      <CardBody>
        <Field label="Vertrauenswürdige ARC-Siegel" help="Domains der Weiterleiter (d= im ARC-Seal). Subdomains sind eingeschlossen. Leer = ARC wird nur geprüft und protokolliert.">
          {(id) => (
            <ChipInput
              id={id}
              value={list}
              onChange={setList}
              placeholder="google.com"
              transform={(v) => v.trim().toLowerCase().replace(/\.$/, "")}
              validate={(v) => (domainRe.test(v) ? null : `„${v}“ ist keine gültige Domain.`)}
              suggestions={suggestions}
            />
          )}
        </Field>
      </CardBody>
      <CardFooter>
        <Button variant="primary" size="sm" disabled={!dirty} loading={saving} onClick={() => save({ mailauth: { trustedArcSealers: list } })}>
          Speichern
        </Button>
      </CardFooter>
    </Card>
  );
}
