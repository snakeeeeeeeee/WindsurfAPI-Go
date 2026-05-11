import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  base: "/dashboard/",
  plugins: [react()],
  build: {
    outDir: "../../internal/dashboard/dist",
    emptyOutDir: true
  },
  server: {
    port: 5173,
    proxy: {
      "/dashboard/api": "http://127.0.0.1:3456",
      "/debug": "http://127.0.0.1:3456",
      "/auth": "http://127.0.0.1:3456",
      "/healthz": "http://127.0.0.1:3456",
      "/readyz": "http://127.0.0.1:3456"
    }
  }
});
