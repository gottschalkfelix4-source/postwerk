import { useQuery } from "@tanstack/react-query";
import { aiApi, mailApi, setupApi } from "@/api/endpoints";
import type { AiStatus } from "@/api/types";

export const qk = {
  me: ["me"] as const,
  setupStatus: ["setup", "status"] as const,
  aiStatus: ["ai", "status"] as const,
  mailboxes: ["mail", "mailboxes"] as const,
  tags: ["mail", "tags"] as const,
  messages: ["mail", "messages"] as const,
  message: (id: number) => ["mail", "message", id] as const,
  thread: (id: number) => ["mail", "thread", id] as const,
  tasks: ["tasks"] as const,
  calendar: ["calendar"] as const,
};

const disabledStatus: AiStatus = {
  enabled: false,
  categories: [],
  dmsEnabled: false,
  features: {
    spamCheck: false,
    classify: false,
    priority: false,
    summarize: false,
    replyDraft: false,
    semanticSearch: false,
    extractEvents: false,
  },
};

/** AI availability for the current user. Falls back to "disabled" on errors. */
export function useAiStatus(): AiStatus & { loading: boolean } {
  const q = useQuery({
    queryKey: qk.aiStatus,
    queryFn: aiApi.status,
    staleTime: 5 * 60_000,
    retry: false,
  });
  const data = q.data ?? disabledStatus;
  return {
    enabled: !!data.enabled,
    features: { ...disabledStatus.features, ...(data.features ?? {}) },
    categories: data.categories ?? [],
    dmsEnabled: !!data.dmsEnabled,
    loading: q.isLoading,
  };
}

export function useMailboxes() {
  return useQuery({ queryKey: qk.mailboxes, queryFn: mailApi.mailboxes, staleTime: 15_000, refetchInterval: 60_000 });
}

export function useTags() {
  return useQuery({ queryKey: qk.tags, queryFn: mailApi.tags, staleTime: 60_000 });
}

/** Setup state of the server; requested without a session. */
export function useSetupStatus() {
  return useQuery({ queryKey: qk.setupStatus, queryFn: setupApi.status, staleTime: 30_000, retry: 1 });
}
