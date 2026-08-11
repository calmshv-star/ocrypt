import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  build: {
    rollupOptions: {
      output: {
        manualChunks: {
          "i18n-vendor": ["@merchant/i18n"],
          "icons-vendor": ["lucide-react"],
          "query-vendor": ["@tanstack/react-query"],
          "router-vendor": ["react-router-dom"]
        }
      }
    }
  },
  server: { port: 4173 },
  preview: { port: 4173 }
});
