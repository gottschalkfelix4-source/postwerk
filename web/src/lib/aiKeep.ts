/** Why an analyzed message stayed in the inbox (reason codes of the AI worker). */
export function keptLabel(reason: string, mode?: string): string {
  switch (reason) {
    case "autoFileOff":
      return "Einsortieren ist ausgeschaltet";
    case "suspicious":
      return "Verdacht auf Manipulation der KI (wird nie verschoben)";
    case "important":
      return "Als wichtig erkannt („Wichtige Nachrichten im Posteingang lassen“)";
    case "noTarget":
      if (mode === "folders") return "KI hat keinen passenden eigenen Ordner gewählt";
      if (mode === "categories") return "Keine Kategorie oder Kategorie ohne Zielordner";
      return "Kein passender Ordner gefunden";
    case "notInInbox":
      return "Lag nicht mehr im Posteingang (z. B. Archiv oder bereits verschoben)";
    case "folderMissing":
      return "Von der KI gewählter Ordner existiert nicht";
    case "moveFailed":
      return "Verschieben fehlgeschlagen (siehe Protokoll)";
    default:
      return reason;
  }
}
