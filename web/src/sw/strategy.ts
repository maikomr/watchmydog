// Caching policy of the service worker, as a pure function so it can be
// unit-tested outside a service-worker runtime.

export type Strategy =
  /** Never touched by the worker: live data and anything login-dependent. */
  | "network-only"
  /** Not ours to handle (other origins, non-GET). */
  | "passthrough"
  /** Pages: fresh from the server, cached shell when offline. */
  | "network-first"
  /** Vite's content-hashed bundles: immutable, so cache wins. */
  | "cache-first"
  /** Icons, manifest: serve cached, refresh in the background. */
  | "stale-while-revalidate";

export interface RequestInfo {
  method: string;
  url: string;
  mode: string;
}

export function strategyFor(req: RequestInfo, origin: string): Strategy {
  if (req.method !== "GET") return "passthrough";
  const url = new URL(req.url);
  if (url.origin !== origin) return "passthrough";
  if (url.pathname.startsWith("/api/") || url.pathname.startsWith("/media/")) return "network-only";
  if (req.mode === "navigate") return "network-first";
  if (url.pathname.startsWith("/assets/")) return "cache-first";
  return "stale-while-revalidate";
}

/** Bump when the caching behaviour changes so old caches are dropped. */
export const CACHE_NAME = "wmd-shell-v1";

/** Fetched at install so the app opens (and says "offline") without network. */
export const SHELL = ["/", "/manifest.webmanifest", "/icons/icon-192.png", "/icons/icon-512.png"];
