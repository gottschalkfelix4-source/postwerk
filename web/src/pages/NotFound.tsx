import { useNavigate } from "react-router-dom";
import { Compass } from "lucide-react";
import { EmptyState, Button } from "@/components/ui";
import { homeFor, useMe } from "@/lib/auth";

export function NotFoundPage() {
  const me = useMe();
  const navigate = useNavigate();
  return (
    <EmptyState
      icon={Compass}
      title="Seite nicht gefunden"
      description="Die aufgerufene Adresse existiert nicht oder wurde verschoben."
      action={
        <Button variant="primary" onClick={() => navigate(homeFor(me))}>
          Zur Startseite
        </Button>
      }
    />
  );
}
