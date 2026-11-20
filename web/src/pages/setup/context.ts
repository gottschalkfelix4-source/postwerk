import { createContext, useContext } from "react";
import type { Me, SetupStatus } from "@/api/types";

export interface InitForm {
  hostname: string;
  domain: string;
  adminName: string;
  adminEmail: string;
  password: string;
  repeat: string;
  /** The user edited the field, so it is no longer derived automatically. */
  domainTouched: boolean;
  emailTouched: boolean;
}

export interface SetupCtx {
  status: SetupStatus;
  /** The wizard started without any account ("initial" mode). */
  initialFlow: boolean;
  /** An admin session exists (always true in "configure" mode). */
  initialized: boolean;
  me: Me | undefined;
  token: string;
  setToken: (token: string) => void;
  tokenError: string | null;
  setTokenError: (error: string | null) => void;
  /** Domain chosen for the DNS check. */
  domain: string;
  setDomain: (domain: string) => void;
  initForm: InitForm;
  setInitForm: (form: InitForm) => void;
  markInitialized: (domain: string) => void;
  goTo: (index: number) => void;
  /** Leave the wizard for now ("Später einrichten"). */
  skip: () => void;
}

export const SetupContext = createContext<SetupCtx | null>(null);

export function useSetup(): SetupCtx {
  const ctx = useContext(SetupContext);
  if (!ctx) throw new Error("useSetup must be used inside SetupContext");
  return ctx;
}

export interface StepProps {
  onBack?: () => void;
  onNext: () => void;
}

/** Shared query keys so the wizard and the regular admin pages share caches. */
export const setupKeys = {
  settings: ["admin", "settings"] as const,
  domains: ["admin", "domains"] as const,
  dnsCheck: (domain: string) => ["admin", "dns-check", domain] as const,
  systemCheck: ["admin", "system-check"] as const,
};
