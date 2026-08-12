import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@merchant/i18n";
import { ThemeProvider } from "@merchant/ui";
import "@merchant/ui/styles.css";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { HashRouter } from "react-router-dom";
import { App } from "./App";
import "./styles.css";
import { captureInvitationTokenFromLocation } from "./invitation-token";

captureInvitationTokenFromLocation();

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 30_000,
      refetchOnWindowFocus: false,
      retry: 1
    }
  }
});

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <ThemeProvider defaultTheme="light" storageKey="ocrypt-theme-v1">
      <I18nProvider>
        <QueryClientProvider client={queryClient}>
          <HashRouter>
            <App />
          </HashRouter>
        </QueryClientProvider>
      </I18nProvider>
    </ThemeProvider>
  </StrictMode>
);
