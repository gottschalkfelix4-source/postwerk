// API and types for per-account send limits and operational notifications.
import { http } from "./client";
import type { SendLimitSettings } from "./types";

/** Per-account override: null keeps the default, 0 = unlimited. */
export interface SendLimitOverride {
  maxRecipientsPerMessage: number | null;
  maxMessagesPerHour: number | null;
  maxRecipientsPerDay: number | null;
  exempt: boolean;
}

export interface SendLimits extends SendLimitSettings {
  exempt: boolean;
}

export interface SendUsage {
  messagesLastHour: number;
  recipientsLastHour: number;
  messagesLastDay: number;
  recipientsLastDay: number;
}

export interface SendLimitInfo {
  override: SendLimitOverride;
  defaults: SendLimitSettings;
  effective: SendLimits;
  usage: SendUsage;
  hitsToday: number;
  lastHit: string | null;
}

/** A warning that was notified and has not cleared yet. */
export interface NotifyState {
  key: string;
  severity: "warning" | "error";
  title: string;
  firstSeen: string;
  lastSeen: string;
  lastNotified: string;
}

export interface NotificationTestResult {
  recipients: string[];
  mailError?: string;
  webhook: boolean;
  webhookError?: string;
}

export const limitsApi = {
  accountLimits: (id: number) => http.get<SendLimitInfo>(`/api/admin/accounts/${id}/send-limits`),
  saveAccountLimits: (id: number, body: SendLimitOverride) =>
    http.put<SendLimitInfo>(`/api/admin/accounts/${id}/send-limits`, body),
  notificationState: () => http.get<NotifyState[]>("/api/admin/notifications/state"),
  testNotification: () => http.post<NotificationTestResult>("/api/admin/notifications/test"),
};
