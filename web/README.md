# Postwerk Web

Weboberfläche für Postwerk: Verwaltung (`/admin`) und Webmail (`/mail`).

Stack: Vite, React 18, TypeScript (strict), Tailwind CSS 3, React Router 6, TanStack Query 5,
lucide-react, DOMPurify, Recharts, qrcode.

## Entwicklung

```bash
cd web
npm install
npm run dev
```

Der Dev-Server läuft auf <http://localhost:5173> und leitet `/api`, `/metrics` und `/healthz`
an das Backend auf `http://localhost:8080` weiter (siehe `vite.config.ts`).

Der API-Vertrag steht in `../docs/API.md`; die Typen liegen 1:1 in `src/api/types.ts`.

## Build

```bash
npm run build
```

Führt zuerst die Typprüfung (`tsc --noEmit`) aus und schreibt das Ergebnis nach
`../server/internal/webui/dist` (wird vorher geleert). Der Go-Server bettet diesen Ordner ein.

## Struktur

```
src/
  api/          types.ts (API-Typen), client.ts (fetch-Wrapper, ApiError), endpoints.ts
  components/
    ui/         eigene Basiskomponenten (Button, Modal, Table, Toast, …)
    layout/     App-Rahmen: Sidebar, Kopfleiste, Breadcrumbs, Suche
  lib/          Theme, Auth-Kontext, Formatierung, Hooks
  pages/
    admin/      Dashboard, Konten, Domains, Aliase, Warteschlange, Logs, Sicherheit, Metriken, Einstellungen
    mail/       Webmail (Liste, Lesebereich, KI-Panel, Composer), Kalender, Aufgaben, Konto
```

## Tastenkürzel im Webmail

| Taste | Aktion |
|---|---|
| `c` | Neue Nachricht |
| `r` | Antworten |
| `j` / `k` | Nächste / vorherige Nachricht |
| `e` | Archivieren |
| `#` | Löschen |
| `/` | Suche fokussieren |
| `Strg`+`K` | Globale Suche |
| `Strg`+`Enter` | Im Editor: Senden |

## Anderer Backend-Port

Ist Port 8080 belegt (z. B. durch WSL), den Server mit `POSTWERK_HTTP_ADDR=127.0.0.1:8091` starten und Vite so:

```bash
POSTWERK_API=http://127.0.0.1:8091 PORT=3417 npm run dev
```
