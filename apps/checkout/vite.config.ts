import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

export default defineConfig({
  plugins: [react()],
  build: {
    rollupOptions: {
      output: {
        manualChunks: {
          "i18n-vendor": ["@merchant/i18n"],
          "icons-vendor": ["lucide-react"],
          "qrcode-vendor": ["qrcode"],
          "react-vendor": ["react", "react-dom"]
        }
      }
    }
  },
  server: { port: 4175 }
});
