import type { ReactNode } from "react";
import type { Params } from "react-router-dom";

export interface RouteHandle {
  /** Breadcrumb label, either static or derived from route params. */
  crumb?: string | ((params: Params<string>) => ReactNode);
  /** Content fills the whole area below the top bar without page padding. */
  fullBleed?: boolean;
}
