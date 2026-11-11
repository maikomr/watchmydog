import { defineConfig } from "vitest/config";

// During `vite dev` the client runs on :5173 and proxies API and media calls
// to the Go service on :8080, so both halves can be hot-reloaded independently.
// In production the Go binary serves web/dist itself.
export default defineConfig({
  server: {
    host: true,
    proxy: {
      "/api": "http://127.0.0.1:8080",
      "/media": { target: "http://127.0.0.1:8080", ws: true },
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    rollupOptions: {
      // Two entries: the app, and the service worker, which must live at a
      // fixed, unhashed URL (/sw.js) to control the whole origin.
      input: { main: "index.html", sw: "src/sw/sw.ts" },
      output: {
        entryFileNames: (chunk) => (chunk.name === "sw" ? "sw.js" : "assets/[name]-[hash].js"),
      },
    },
  },
  test: {
    environment: "node",
  },
});
