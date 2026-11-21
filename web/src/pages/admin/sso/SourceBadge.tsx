import { Building2, KeyRound } from "lucide-react";
import type { AccountSource } from "@/api/types";
import { Badge } from "@/components/ui";

export const sourceLabels: Record<AccountSource, string> = {
  local: "Lokal",
  ldap: "LDAP",
  oidc: "SSO",
};

/** Shows where an account's password lives; nothing for local accounts unless `showLocal`. */
export function SourceBadge({ source, showLocal }: { source?: AccountSource; showLocal?: boolean }) {
  if (!source || (source === "local" && !showLocal)) return null;
  if (source === "ldap") {
    return (
      <Badge tone="teal" icon={Building2} title="Passwort wird im LDAP-Verzeichnis geprüft">
        LDAP
      </Badge>
    );
  }
  if (source === "oidc") {
    return (
      <Badge tone="purple" icon={KeyRound} title="Anmeldung per Single Sign-on, kein Postwerk-Passwort">
        SSO
      </Badge>
    );
  }
  return <Badge tone="neutral">Lokal</Badge>;
}
