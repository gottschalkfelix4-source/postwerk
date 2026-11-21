import { useRef, useState, type DragEvent } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { CircleCheck, CircleX, FileArchive, FileUp, Mail, RotateCcw, Upload, X } from "lucide-react";
import { mailApi } from "@/api/endpoints";
import { errorMessage } from "@/api/client";
import type { ImportResult } from "@/api/types";
import { formatBytes, formatNumber } from "@/lib/format";
import { qk, useMailboxes } from "@/lib/queries";
import { useMe } from "@/lib/auth";
import { cn } from "@/lib/utils";
import { Badge, Button, Card, CardBody, CardHeader, Field, Notice, Progress, Select, useToast } from "@/components/ui";

const ACCEPTED = [".mbox", ".eml", ".zip"];
const GB = 1024 * 1024 * 1024;

type Phase = { kind: "idle" } | { kind: "uploading"; progress: number } | { kind: "processing" } | { kind: "done"; result: ImportResult } | { kind: "error"; message: string };

function extensionOf(name: string): string {
  const i = name.lastIndexOf(".");
  return i >= 0 ? name.slice(i).toLowerCase() : "";
}

const guides = [
  {
    title: "Thunderbird",
    text: "Add-on „ImportExportTools NG“ installieren, Rechtsklick auf den Ordner → „Exportieren“ → „Ordner (mbox)“.",
  },
  {
    title: "Apple Mail",
    text: "Postfach in der Seitenleiste auswählen → Menü „Postfach“ → „Postfach exportieren …“. Die .mbox-Datei im exportierten Ordner hochladen (bzw. den Ordner als .zip).",
  },
  {
    title: "Outlook",
    text: "Nachrichten per Drag & Drop aus Outlook in einen Ordner ziehen – sie werden als .eml gespeichert. Mehrere Dateien als .zip packen.",
  },
];

/** Self-service import of .mbox/.eml/.zip files into the own mailbox. */
export function ImportCard() {
  const qc = useQueryClient();
  const me = useMe();
  const maxGB = me.role === "admin" ? 2 : 1;
  const toast = useToast();
  const mailboxes = useMailboxes();
  const inputRef = useRef<HTMLInputElement>(null);
  const abortRef = useRef<AbortController | null>(null);
  const [file, setFile] = useState<File | null>(null);
  const [fileError, setFileError] = useState<string | null>(null);
  const [mailboxId, setMailboxId] = useState("");
  const [dragging, setDragging] = useState(false);
  const [phase, setPhase] = useState<Phase>({ kind: "idle" });

  const busy = phase.kind === "uploading" || phase.kind === "processing";

  const pick = (f: File | undefined) => {
    if (!f || busy) return;
    setPhase({ kind: "idle" });
    if (!ACCEPTED.includes(extensionOf(f.name))) {
      setFile(null);
      setFileError("Nur .mbox-, .eml- oder .zip-Dateien werden unterstützt.");
      return;
    }
    if (f.size > maxGB * GB) {
      setFile(null);
      setFileError(`Die Datei ist zu groß (${formatBytes(f.size)}). Maximal ${maxGB} GB.`);
      return;
    }
    setFileError(null);
    setFile(f);
  };

  const onDrop = (e: DragEvent<HTMLDivElement>) => {
    e.preventDefault();
    setDragging(false);
    pick(e.dataTransfer.files?.[0]);
  };

  const start = async () => {
    if (!file) return;
    const controller = new AbortController();
    abortRef.current = controller;
    setPhase({ kind: "uploading", progress: 0 });
    try {
      const result = await mailApi.importFile(file, {
        mailboxId: mailboxId ? Number(mailboxId) : undefined,
        signal: controller.signal,
        onProgress: (p) => setPhase(p >= 1 ? { kind: "processing" } : { kind: "uploading", progress: p }),
      });
      setPhase({ kind: "done", result });
      setFile(null);
      void qc.invalidateQueries({ queryKey: qk.mailboxes });
      void qc.invalidateQueries({ queryKey: qk.messages });
      void qc.invalidateQueries({ queryKey: qk.tags });
      toast.success("Import abgeschlossen", `${formatNumber(result.imported)} Nachrichten importiert.`);
    } catch (err) {
      if (err instanceof DOMException && err.name === "AbortError") {
        setPhase({ kind: "idle" });
        toast.info("Import abgebrochen");
      } else {
        setPhase({ kind: "error", message: errorMessage(err) });
      }
    } finally {
      abortRef.current = null;
    }
  };

  const options = [
    { value: "", label: "Posteingang (Standard)" },
    ...(mailboxes.data ?? []).filter((m) => m.role !== "inbox").map((m) => ({ value: String(m.id), label: m.name })),
  ];

  return (
    <Card id="import">
      <CardHeader
        title="Import"
        description="Nachrichten aus .mbox-, .eml- oder .zip-Dateien in dein Postfach übernehmen."
        icon={<FileUp className="h-4 w-4 text-zinc-400" />}
      />
      <CardBody className="space-y-5">
        <div
          role="button"
          tabIndex={0}
          onClick={() => !busy && inputRef.current?.click()}
          onKeyDown={(e) => {
            if ((e.key === "Enter" || e.key === " ") && !busy) {
              e.preventDefault();
              inputRef.current?.click();
            }
          }}
          onDragOver={(e) => {
            e.preventDefault();
            if (!busy) setDragging(true);
          }}
          onDragLeave={() => setDragging(false)}
          onDrop={onDrop}
          className={cn(
            "flex cursor-pointer flex-col items-center justify-center rounded-xl border-2 border-dashed px-6 py-8 text-center transition-colors",
            "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-indigo-500/50",
            dragging
              ? "border-indigo-400 bg-indigo-50/70 dark:border-indigo-500/60 dark:bg-indigo-500/10"
              : "border-zinc-300 hover:border-zinc-400 hover:bg-zinc-50 dark:border-zinc-700 dark:hover:border-zinc-600 dark:hover:bg-zinc-800/40",
            busy && "pointer-events-none opacity-60",
          )}
        >
          <input
            ref={inputRef}
            type="file"
            accept=".mbox,.eml,.zip,application/mbox,message/rfc822,application/zip"
            className="hidden"
            onChange={(e) => {
              pick(e.target.files?.[0]);
              e.target.value = "";
            }}
          />
          <span
            className={cn(
              "mb-3 flex h-11 w-11 items-center justify-center rounded-xl ring-1 ring-inset transition-colors",
              dragging
                ? "bg-indigo-100 text-indigo-600 ring-indigo-200 dark:bg-indigo-500/20 dark:text-indigo-300 dark:ring-indigo-500/30"
                : "bg-zinc-100 text-zinc-400 ring-zinc-200 dark:bg-zinc-800 dark:text-zinc-500 dark:ring-zinc-700",
            )}
          >
            <Upload className="h-5 w-5" />
          </span>
          <p className="text-sm font-medium text-zinc-800 dark:text-zinc-200">
            {dragging ? "Datei hier ablegen" : "Datei hierher ziehen oder klicken zum Auswählen"}
          </p>
          <p className="mt-1 text-xs text-zinc-500 dark:text-zinc-400">.mbox, .eml oder .zip (mit .eml/.mbox) · max. {maxGB} GB</p>
        </div>

        {fileError && (
          <Notice tone="error" icon={CircleX}>
            {fileError}
          </Notice>
        )}

        {file && (
          <div className="flex items-center gap-3 rounded-lg border border-zinc-200 px-3 py-2.5 dark:border-zinc-800">
            {extensionOf(file.name) === ".zip" ? (
              <FileArchive className="h-5 w-5 shrink-0 text-zinc-400" />
            ) : (
              <Mail className="h-5 w-5 shrink-0 text-zinc-400" />
            )}
            <div className="min-w-0 flex-1">
              <div className="truncate text-sm font-medium text-zinc-900 dark:text-zinc-100">{file.name}</div>
              <div className="text-xs text-zinc-500 dark:text-zinc-400">{formatBytes(file.size)}</div>
            </div>
            {!busy && <Button size="icon-sm" variant="ghost" icon={X} onClick={() => setFile(null)} aria-label="Datei entfernen" />}
          </div>
        )}

        <div className="flex flex-wrap items-end gap-3">
          <Field label="Zielordner" className="w-full max-w-xs" help={file && extensionOf(file.name) === ".zip" ? "Unterordner im Archiv werden als eigene Ordner angelegt." : undefined}>
            {(id) => <Select id={id} value={mailboxId} onChange={(e) => setMailboxId(e.target.value)} options={options} disabled={busy} />}
          </Field>
          <div className="flex gap-2 pb-px">
            <Button variant="primary" icon={Upload} disabled={!file || busy} loading={busy} onClick={() => void start()}>
              Importieren
            </Button>
            {phase.kind === "uploading" && (
              <Button variant="ghost" onClick={() => abortRef.current?.abort()}>
                Abbrechen
              </Button>
            )}
          </div>
        </div>

        {(phase.kind === "uploading" || phase.kind === "processing") && (
          <div className="space-y-1.5">
            <div className="flex justify-between text-xs text-zinc-500 dark:text-zinc-400">
              <span>{phase.kind === "uploading" ? "Wird hochgeladen …" : "Nachrichten werden verarbeitet …"}</span>
              {phase.kind === "uploading" && <span className="tabular-nums">{Math.round(phase.progress * 100)} %</span>}
            </div>
            <Progress value={phase.kind === "uploading" ? phase.progress : 1} tone="indigo" className={cn("h-2", phase.kind === "processing" && "animate-pulse")} />
          </div>
        )}

        {phase.kind === "error" && (
          <Notice tone="error" icon={CircleX} title="Import fehlgeschlagen">
            {phase.message}
          </Notice>
        )}

        {phase.kind === "done" && <ResultView result={phase.result} onReset={() => setPhase({ kind: "idle" })} />}

        <div className="border-t border-zinc-100 pt-4 dark:border-zinc-800">
          <div className="mb-2 text-xs font-semibold uppercase tracking-wide text-zinc-500 dark:text-zinc-400">So exportierst du deine Mails</div>
          <div className="grid gap-3 sm:grid-cols-3">
            {guides.map((g) => (
              <div key={g.title} className="rounded-lg bg-zinc-50 p-3 dark:bg-zinc-950/50">
                <div className="text-sm font-medium text-zinc-900 dark:text-zinc-100">{g.title}</div>
                <p className="mt-0.5 text-xs leading-relaxed text-zinc-500 dark:text-zinc-400">{g.text}</p>
              </div>
            ))}
          </div>
        </div>
      </CardBody>
    </Card>
  );
}

function ResultView({ result, onReset }: { result: ImportResult; onReset: () => void }) {
  const [showAll, setShowAll] = useState(false);
  const errors = result.errors ?? [];
  const visible = showAll ? errors : errors.slice(0, 5);
  return (
    <div
      className={cn(
        "rounded-xl border p-4",
        result.failed > 0
          ? "border-amber-200 bg-amber-50/50 dark:border-amber-500/30 dark:bg-amber-500/[0.06]"
          : "border-emerald-200 bg-emerald-50/50 dark:border-emerald-500/30 dark:bg-emerald-500/[0.06]",
      )}
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-2 text-sm font-semibold text-zinc-900 dark:text-zinc-100">
          <CircleCheck className={cn("h-5 w-5", result.failed > 0 ? "text-amber-500" : "text-emerald-600")} />
          Import abgeschlossen
        </div>
        <Button size="xs" variant="ghost" icon={RotateCcw} onClick={onReset}>
          Weitere Datei
        </Button>
      </div>
      <div className="mt-3 flex flex-wrap gap-2">
        <Badge tone="green">{formatNumber(result.imported)} importiert</Badge>
        <Badge tone="neutral">{formatNumber(result.skipped)} übersprungen</Badge>
        <Badge tone={result.failed > 0 ? "red" : "neutral"}>{formatNumber(result.failed)} fehlerhaft</Badge>
      </div>
      {result.skipped > 0 && <p className="mt-2 text-xs text-zinc-500 dark:text-zinc-400">Übersprungen wurden Nachrichten, die bereits im Postfach vorhanden sind.</p>}
      {errors.length > 0 && (
        <div className="mt-3">
          <ul className="max-h-48 space-y-1 overflow-y-auto rounded-md bg-white/70 p-2 font-mono text-xs text-red-700 ring-1 ring-inset ring-zinc-200 dark:bg-zinc-950/50 dark:text-red-300 dark:ring-zinc-800">
            {visible.map((e, i) => (
              <li key={i} className="break-words">
                {e}
              </li>
            ))}
          </ul>
          {errors.length > 5 && (
            <button type="button" onClick={() => setShowAll((v) => !v)} className="mt-1.5 text-xs font-medium text-indigo-600 hover:underline dark:text-indigo-300">
              {showAll ? "Weniger anzeigen" : `Alle ${errors.length} Fehler anzeigen`}
            </button>
          )}
        </div>
      )}
    </div>
  );
}
