import { useState } from "react";
import { Bot, Cloud, Cpu, Globe, MessageCircle, Server, Sparkles, TriangleAlert } from "lucide-react";
import type { AiProvider, AiTestResult, Settings } from "@/api/types";
import { Badge, Field, Input, Notice, PasswordInput } from "@/components/ui";
import { cn } from "@/lib/utils";
import { CHATGPT_MODELS, ChatGptConnection } from "./ChatGptConnection";

const MASK = "••••";

type Ai = Settings["ai"];

// ---------------------------------------------------------------------------
// Presets
// ---------------------------------------------------------------------------

interface Preset {
  id: string;
  title: string;
  text: string;
  icon: typeof Bot;
  provider: AiProvider;
  baseUrl?: string;          // undefined: keep what the user typed
  chatModel?: string;
  embeddingModel?: string;
  local?: boolean;
}

export const AI_PRESETS: Preset[] = [
  {
    id: "ollama",
    title: "Ollama (lokal)",
    text: "Läuft auf deinem Server, nichts verlässt ihn.",
    icon: Cpu,
    provider: "openai",
    baseUrl: "http://ollama:11434/v1",
    chatModel: "qwen2.5:7b",
    embeddingModel: "nomic-embed-text",
    local: true,
  },
  {
    id: "openai",
    title: "OpenAI",
    text: "api.openai.com mit API-Schlüssel.",
    icon: Sparkles,
    provider: "openai",
    baseUrl: "https://api.openai.com/v1",
    embeddingModel: "text-embedding-3-small",
  },
  {
    id: "anthropic",
    title: "Anthropic (Claude)",
    text: "api.anthropic.com mit API-Schlüssel.",
    icon: Bot,
    provider: "anthropic",
    baseUrl: "https://api.anthropic.com",
    chatModel: "claude-opus-5-5",
    embeddingModel: "",
  },
  {
    id: "chatgpt",
    title: "ChatGPT-Abo (Anmeldung mit ChatGPT)",
    text: "Eigenes Plus- oder Pro-Abo statt API-Schlüssel.",
    icon: MessageCircle,
    provider: "chatgpt",
    chatModel: CHATGPT_MODELS[0],
    embeddingModel: "",
  },
  {
    id: "openrouter",
    title: "OpenRouter",
    text: "Viele Modelle über eine OpenAI-kompatible API.",
    icon: Globe,
    provider: "openai",
    baseUrl: "https://openrouter.ai/api/v1",
    embeddingModel: "",
  },
  {
    id: "custom-openai",
    title: "Anderer Dienst (OpenAI-kompatibel)",
    text: "LM Studio, vLLM, LocalAI, Mistral, Groq, …",
    icon: Server,
    provider: "openai",
  },
  {
    id: "custom-anthropic",
    title: "Anderer Dienst (Anthropic-kompatibel)",
    text: "Dienste mit Anthropic-Messages-API.",
    icon: Cloud,
    provider: "anthropic",
  },
];

const norm = (u: string) => u.trim().replace(/\/+$/, "").toLowerCase();

export function activePreset(ai: Pick<Ai, "provider" | "baseUrl">): Preset {
  if (ai.provider === "chatgpt") return AI_PRESETS.find((p) => p.id === "chatgpt")!;
  const provider: AiProvider = ai.provider === "anthropic" ? "anthropic" : "openai";
  const match = AI_PRESETS.find((p) => p.baseUrl && p.provider === provider && norm(p.baseUrl) === norm(ai.baseUrl ?? ""));
  if (match) return match;
  if (provider === "openai" && /(^|\/\/)(ollama|localhost|127\.0\.0\.1)(:11434)/i.test(ai.baseUrl ?? "")) return AI_PRESETS[0];
  return AI_PRESETS.find((p) => p.id === `custom-${provider}`)!;
}

/** Applies a preset to the draft. A stored key belongs to the old service, so it is cleared when the address changes. */
export function applyPreset(ai: Ai, p: Preset): Partial<Ai> {
  const patch: Partial<Ai> = { provider: p.provider };
  if (p.baseUrl !== undefined && norm(p.baseUrl) !== norm(ai.baseUrl)) {
    patch.baseUrl = p.baseUrl;
    patch.apiKey = "";
  }
  if (p.chatModel !== undefined) patch.chatModel = p.chatModel;
  else if (p.baseUrl !== undefined && norm(p.baseUrl) !== norm(ai.baseUrl)) patch.chatModel = "";
  if (p.embeddingModel !== undefined) patch.embeddingModel = p.embeddingModel;
  if (p.local) {
    patch.embeddingBaseUrl = "";
    patch.embeddingApiKey = "";
  }
  return patch;
}

/** True when the endpoint is (most likely) outside this server's network. */
export function isExternal(url: string): boolean {
  try {
    const h = new URL(url).hostname.toLowerCase();
    if (!h.includes(".")) return false; // docker service name like "ollama"
    if (h === "localhost" || h.endsWith(".local") || h.endsWith(".lan") || h.endsWith(".internal")) return false;
    if (/^(127\.|10\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.|\[?::1\]?$|\[?f[cd])/.test(h)) return false;
    return true;
  } catch {
    return false;
  }
}

function hostOf(url: string) {
  try {
    return new URL(url).host;
  } catch {
    return url;
  }
}

// ---------------------------------------------------------------------------
// Fields
// ---------------------------------------------------------------------------

function SecretField({ id, value, onChange, placeholder }: { id?: string; value: string; onChange: (v: string) => void; placeholder?: string }) {
  const masked = value === MASK;
  return (
    <PasswordInput
      id={id}
      value={masked ? "" : value}
      placeholder={masked ? "•••••••• (gespeichert – zum Ändern neu eingeben)" : placeholder}
      autoComplete="new-password"
      onChange={(e) => onChange(e.target.value === "" && masked ? MASK : e.target.value)}
    />
  );
}

export function AiProviderPicker({ value, onChange }: { value: Ai; onChange: (patch: Partial<Ai>) => void }) {
  const current = activePreset(value);
  return (
    <div role="radiogroup" aria-label="KI-Anbieter" className="grid gap-2 sm:grid-cols-2 xl:grid-cols-3">
      {AI_PRESETS.map((p) => {
        const active = p.id === current.id;
        const Icon = p.icon;
        return (
          <button
            key={p.id}
            type="button"
            role="radio"
            aria-checked={active}
            onClick={() => onChange(applyPreset(value, p))}
            className={cn(
              "flex items-start gap-3 rounded-lg border p-3 text-left transition-colors",
              active
                ? "border-indigo-500 bg-indigo-50/60 ring-1 ring-indigo-500 dark:border-indigo-400 dark:bg-indigo-500/10 dark:ring-indigo-400"
                : "border-zinc-200 hover:border-zinc-300 dark:border-zinc-800 dark:hover:border-zinc-700",
            )}
          >
            <Icon className={cn("mt-0.5 h-4 w-4 shrink-0", active ? "text-indigo-600 dark:text-indigo-400" : "text-zinc-400")} />
            <span className="min-w-0">
              <span className="flex flex-wrap items-center gap-1.5 text-sm font-medium text-zinc-900 dark:text-zinc-100">
                {p.title}
                {p.local && <Badge tone="green">Lokal</Badge>}
              </span>
              <span className="mt-0.5 block text-xs text-zinc-500 dark:text-zinc-400">{p.text}</span>
            </span>
          </button>
        );
      })}
    </div>
  );
}

/** Address, key and models for the chosen provider, plus the embedding endpoint. */
export function AiConnectionFields({ value, onChange }: { value: Ai; onChange: (patch: Partial<Ai>) => void }) {
  const anthropic = value.provider === "anthropic";
  const chatgpt = value.provider === "chatgpt";
  // Neither Anthropic nor the ChatGPT subscription offers embeddings.
  const noEmbeddings = anthropic || chatgpt;
  const preset = activePreset(value);
  const [ownEmbed, setOwnEmbed] = useState(!!value.embeddingBaseUrl);
  const showEmbedEndpoint = noEmbeddings || ownEmbed || !!value.embeddingBaseUrl;
  const external = !chatgpt && isExternal(value.baseUrl);

  return (
    <div className="space-y-4">
      {chatgpt ? (
        <>
          <ChatGptConnection />
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Chat-Modell" help="Ein Modell des Abos, z. B. gpt-6.2-sol. Leer = Standardmodell.">
              {(id) => (
                <>
                  <Input
                    id={id}
                    list="chatgpt-models"
                    value={value.chatModel}
                    onChange={(e) => onChange({ chatModel: e.target.value })}
                    placeholder={CHATGPT_MODELS[0]}
                    className="font-mono text-[13px]"
                  />
                  <datalist id="chatgpt-models">
                    {CHATGPT_MODELS.map((m) => (
                      <option key={m} value={m} />
                    ))}
                  </datalist>
                </>
              )}
            </Field>
          </div>
          <Notice tone="warning" icon={TriangleAlert} title="Externer Dienst">
            Für die KI-Funktionen werden Inhalte eingehender Mails an ChatGPT (<span className="font-mono">chatgpt.com</span>) gesendet. Wer das nicht
            möchte, nutzt Ollama auf dem eigenen Server oder schaltet einzelne Funktionen unten ab.
          </Notice>
        </>
      ) : (
        <div className="grid gap-4 sm:grid-cols-2">
          <Field
            label="Basis-URL"
            help={
              anthropic
                ? "Mit oder ohne /v1, z. B. https://api.anthropic.com"
                : preset.local
                  ? "Inklusive /v1. Ollama im selben Docker-Netz: http://ollama:11434/v1"
                  : "Inklusive /v1, z. B. https://api.openai.com/v1"
            }
          >
            {(id) => (
              <Input
                id={id}
                value={value.baseUrl}
                onChange={(e) => onChange({ baseUrl: e.target.value })}
                placeholder={anthropic ? "https://api.anthropic.com" : "http://ollama:11434/v1"}
                className="font-mono text-[13px]"
              />
            )}
          </Field>
          <Field label="API-Schlüssel" help={preset.local ? "Für Ollama nicht erforderlich." : "Gespeicherte Schlüssel werden nicht wieder angezeigt."}>
            {(id) => <SecretField id={id} value={value.apiKey} onChange={(v) => onChange({ apiKey: v })} placeholder={anthropic ? "sk-ant-…" : "sk-…"} />}
          </Field>
          <Field
            label="Chat-Modell"
            help={
              anthropic
                ? "z. B. claude-opus-5-5 oder das günstigere claude-haiku-5-5"
                : preset.local
                  ? "z. B. qwen2.5:7b oder llama3.1:8b"
                  : "Nach „Verbindung testen“ aus der Liste wählbar."
            }
          >
            {(id) => (
              <Input
                id={id}
                value={value.chatModel}
                onChange={(e) => onChange({ chatModel: e.target.value })}
                placeholder={anthropic ? "claude-opus-5-5" : ""}
                className="font-mono text-[13px]"
              />
            )}
          </Field>
          {anthropic ? (
            <Field label="Maximale Antwortlänge" help="max_tokens pro Anfrage (Standard 16000).">
              {(id) => (
                <Input
                  id={id}
                  type="number"
                  min={256}
                  max={128000}
                  value={value.maxTokens || 16000}
                  onChange={(e) => onChange({ maxTokens: Number(e.target.value) || 16000 })}
                  className="font-mono text-[13px]"
                />
              )}
            </Field>
          ) : (
            <Field label="Embedding-Modell" help="Für die semantische Suche, z. B. nomic-embed-text. Leer = aus.">
              {(id) => <Input id={id} value={value.embeddingModel} onChange={(e) => onChange({ embeddingModel: e.target.value })} className="font-mono text-[13px]" />}
            </Field>
          )}
        </div>
      )}

      {external && (
        <Notice tone="warning" icon={TriangleAlert} title="Externer Dienst">
          Für die KI-Funktionen werden Inhalte eingehender Mails an <span className="font-mono">{hostOf(value.baseUrl)}</span> gesendet. Wer das
          nicht möchte, nutzt Ollama auf dem eigenen Server oder schaltet einzelne Funktionen unten ab.
        </Notice>
      )}

      <div className="rounded-lg border border-zinc-200 p-3 dark:border-zinc-800">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div>
            <div className="text-sm font-medium text-zinc-900 dark:text-zinc-100">Embeddings für die semantische Suche</div>
            <p className="mt-0.5 text-xs text-zinc-500 dark:text-zinc-400">
              {noEmbeddings
                ? `${chatgpt ? "Das ChatGPT-Abo" : "Anthropic"} bietet keine Embeddings. Gib dafür einen OpenAI-kompatiblen Endpunkt an, z. B. Ollama mit nomic-embed-text. Leer lassen schaltet die semantische Suche ab.`
                : showEmbedEndpoint
                  ? "Embeddings laufen über einen eigenen OpenAI-kompatiblen Endpunkt."
                  : "Embeddings laufen über denselben Endpunkt wie der Chat."}
            </p>
          </div>
          {!noEmbeddings && (
            <button
              type="button"
              className="text-xs font-medium text-indigo-600 hover:underline dark:text-indigo-400"
              onClick={() => {
                if (showEmbedEndpoint) onChange({ embeddingBaseUrl: "", embeddingApiKey: "" });
                setOwnEmbed(!showEmbedEndpoint);
              }}
            >
              {showEmbedEndpoint ? "Gleichen Endpunkt nutzen" : "Eigenen Endpunkt angeben"}
            </button>
          )}
        </div>
        {showEmbedEndpoint && (
          <div className="mt-3 grid gap-4 sm:grid-cols-3">
            <Field label="Embedding-URL" help="Inklusive /v1">
              {(id) => (
                <Input
                  id={id}
                  value={value.embeddingBaseUrl}
                  onChange={(e) => onChange({ embeddingBaseUrl: e.target.value })}
                  placeholder="http://ollama:11434/v1"
                  className="font-mono text-[13px]"
                />
              )}
            </Field>
            <Field label="Schlüssel" help="Optional">
              {(id) => <SecretField id={id} value={value.embeddingApiKey} onChange={(v) => onChange({ embeddingApiKey: v })} placeholder="sk-…" />}
            </Field>
            {noEmbeddings && (
              <Field label="Embedding-Modell" help="z. B. nomic-embed-text">
                {(id) => <Input id={id} value={value.embeddingModel} onChange={(e) => onChange({ embeddingModel: e.target.value })} className="font-mono text-[13px]" />}
              </Field>
            )}
          </div>
        )}
      </div>
    </div>
  );
}

/** Extra lines for the test result: chat and embedding checks. */
export function AiTestDetails({ result }: { result: AiTestResult }) {
  if (!result.ok && !result.chatOk && result.embeddingOk === undefined) return null;
  return (
    <ul className="mt-2 space-y-1 text-xs text-zinc-600 dark:text-zinc-400">
      {result.chatOk && <li>✓ Testanfrage an das Chat-Modell beantwortet</li>}
      {result.embeddingOk && <li>✓ Embeddings funktionieren</li>}
      {result.embeddingError && (
        <li className="break-words text-amber-700 dark:text-amber-300">
          Embeddings fehlgeschlagen: <span className="font-mono">{result.embeddingError}</span>
        </li>
      )}
    </ul>
  );
}
