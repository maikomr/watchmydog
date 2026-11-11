import { defineConfig } from "vite";

// During `vite dev` the client runs on :5173 and proxies API calls to the Go
// service on :8080, so both halves can be hot-reloaded independently.
// In production the Go binary embeds and serves web/dist itself.
export default defineConfig({
  server: {
    proxy: {
      "/api": "http://127.0.0.1:8080",
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
});
