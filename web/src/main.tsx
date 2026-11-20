import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "react-router-dom";
import { ApiError, setUnauthorizedHandler } from "@/api/client";
import { ThemeProvider } from "@/lib/theme";
import { ConfirmProvider, ToastProvider } from "@/components/ui";
import { router } from "./router";
import "./index.css";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 15_000,
      refetchOnWindowFocus: false,
      retry: (count, err) => {
        if (err instanceof ApiError && err.status >= 400 && err.status < 500) return false;
        return count < 2;
      },
    },
  },
});

setUnauthorizedHandler(() => {
  const { pathname, search } = window.location;
  if (pathname.startsWith("/login")) return;
  window.setTimeout(() => {
    queryClient.clear();
    void router.navigate("/login", { replace: true, state: { from: pathname + search } });
  }, 0);
});

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <ThemeProvider>
      <QueryClientProvider client={queryClient}>
        <ToastProvider>
          <ConfirmProvider>
            <RouterProvider router={router} />
          </ConfirmProvider>
        </ToastProvider>
      </QueryClientProvider>
    </ThemeProvider>
  </StrictMode>,
);
