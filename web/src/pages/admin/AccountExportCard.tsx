import { PackageOpen } from "lucide-react";
import { Card, CardBody, CardHeader } from "@/components/ui";
import { DataExportPanel } from "@/pages/mail/DataExportCard";

/** Admin account detail: GDPR export of any account. */
export function AccountExportCard({ accountId }: { accountId: number }) {
  return (
    <Card>
      <CardHeader
        title="Datenexport (DSGVO)"
        description="ZIP mit allen Mails (mbox), Kalender, Aufgaben und Kontodaten – z. B. für eine Auskunftsanfrage. Downloads werden protokolliert."
        icon={<PackageOpen className="h-4 w-4 text-zinc-400" />}
      />
      <CardBody>
        <DataExportPanel accountId={accountId} />
      </CardBody>
    </Card>
  );
}
