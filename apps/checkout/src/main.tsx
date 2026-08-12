import { I18nProvider, isLocale } from "@merchant/i18n";
import { ThemeProvider } from "@merchant/ui";
import "@merchant/ui/styles.css";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import "./styles.css";

const requestedLocale = new URLSearchParams(window.location.search).get("locale");

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <ThemeProvider defaultTheme="light" storageKey="ocrypt-theme-v1">
      <I18nProvider initialLocale={isLocale(requestedLocale) ? requestedLocale : undefined}>
        <App />
      </I18nProvider>
    </ThemeProvider>
  </StrictMode>
);
