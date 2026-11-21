import { createContext, useCallback, useContext, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import type { Me } from "@/api/types";
import { authApi } from "@/api/endpoints";

const MeContext = createContext<Me | null>(null);

export function MeProvider({ me, children }: { me: Me; children: ReactNode }) {
  return <MeContext.Provider value={me}>{children}</MeContext.Provider>;
}

/** The signed-in user. Only usable below the protected layout. */
export function useMe(): Me {
  const me = useContext(MeContext);
  if (!me) throw new Error("useMe must be used inside MeProvider");
  return me;
}

export function useLogout(): () => Promise<void> {
  const qc = useQueryClient();
  const navigate = useNavigate();
  return useCallback(async () => {
    try {
      const res = await authApi.logout();
      if (res.endSessionUrl) {
        // Signed in via SSO: end the session at the identity provider too.
        qc.clear();
        window.location.assign(res.endSessionUrl);
        return;
      }
    } catch {
      // The session may already be gone; leave anyway.
    }
    qc.clear();
    navigate("/login", { replace: true });
  }, [qc, navigate]);
}

export function homeFor(me: Pick<Me, "role">): string {
  return me.role === "admin" ? "/admin" : "/mail";
}
