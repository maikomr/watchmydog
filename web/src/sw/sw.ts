// watchmydog service worker. It makes the app installable (Chrome requires a
// fetch handler) and keeps the app shell available offline so the installed
// app opens and explains that the Mac is unreachable instead of showing
// Chrome's error page. Policy lives in ./strategy.ts.
//
// It also receives push messages from the server (via the browser vendor's
// push service) and shows them as notifications, even when the app is
// closed (ADR-0008). That logic lives in ./notify.ts.
//
// Built by Vite as a separate entry into dist/sw.js (see vite.config.ts) and
// type-checked with tsconfig.sw.json (WebWorker lib).

import { notificationFor, pickWindow, safeAppPath } from "./notify";
import { CACHE_NAME, SHELL, strategyFor } from "./strategy";

declare const self: ServiceWorkerGlobalScope;

self.addEventListener("install", (event) => {
  event.waitUntil(
    caches
      .open(CACHE_NAME)
      .then((cache) => cache.addAll(SHELL))
      .then(() => self.skipWaiting()),
  );
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== CACHE_NAME).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  );
});

async function put(req: Request | string, res: Response): Promise<void> {
  if (!res.ok || res.type !== "basic") return;
  const cache = await caches.open(CACHE_NAME);
  await cache.put(req, res);
}

async function networkFirst(req: Request): Promise<Response> {
  try {
    const res = await fetch(req);
    // All pages are the same SPA shell; store it under "/".
    void put("/", res.clone());
    return res;
  } catch {
    return (await caches.match("/")) ?? Response.error();
  }
}

async function cacheFirst(req: Request): Promise<Response> {
  const hit = await caches.match(req);
  if (hit) return hit;
  const res = await fetch(req);
  void put(req, res.clone());
  return res;
}

async function staleWhileRevalidate(req: Request): Promise<Response> {
  const hit = await caches.match(req);
  const refresh = fetch(req).then((res) => {
    void put(req, res.clone());
    return res;
  });
  return hit ?? refresh;
}

self.addEventListener("fetch", (event) => {
  const req = event.request;
  switch (strategyFor(req, self.location.origin)) {
    case "network-first":
      event.respondWith(networkFirst(req));
      break;
    case "cache-first":
      event.respondWith(cacheFirst(req));
      break;
    case "stale-while-revalidate":
      event.respondWith(staleWhileRevalidate(req));
      break;
    default:
      // network-only / passthrough: let the browser handle it.
      break;
  }
});

// A push message arrived: show it. Chrome requires a visible notification
// for every push, so notificationFor always returns one.
self.addEventListener("push", (event) => {
  const spec = notificationFor(event.data ? event.data.text() : null);
  // renotify and timestamp are in the Notifications standard and in Chrome,
  // but not in TypeScript's NotificationOptions type.
  const options: NotificationOptions = spec.options;
  event.waitUntil(self.registration.showNotification(spec.title, options));
});

// The notification was tapped: bring the app to the front, or open it. The
// app's start page is the live view, which starts playing by itself.
self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const url = safeAppPath((event.notification.data as { url?: unknown } | null)?.url);
  event.waitUntil(
    (async () => {
      const windows = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
      const i = pickWindow(windows, self.location.origin);
      if (i >= 0) {
        try {
          await windows[i].focus();
          return;
        } catch {
          /* focusing can be refused; open a new window instead */
        }
      }
      await self.clients.openWindow(url);
    })(),
  );
});
