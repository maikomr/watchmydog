// Entry point: decide between the login screen and the live view, and
// switch between them when the session starts, expires or ends.

import { api, isUnauthorized } from "./api";
import { App } from "./ui/app";
import { el } from "./ui/dom";
import { LoginView } from "./ui/login";

const root = document.getElementById("app")!;
let app: App | null = null;
let bootTimer: ReturnType<typeof setTimeout> | null = null;

function showLogin(): void {
  app?.destroy();
  app = null;
  new LoginView(root, () => void boot()).mount();
}

function showApp(): void {
  app?.destroy();
  app = new App(root, {
    onUnauthorized: showLogin,
    onLogout: () => {
      void api
        .logout()
        .catch(() => undefined)
        .finally(showLogin);
    },
  });
  app.mount();
}

async function boot(): Promise<void> {
  if (bootTimer !== null) clearTimeout(bootTimer);
  try {
    await api.session();
    showApp();
  } catch (err) {
    if (isUnauthorized(err)) {
      showLogin();
      return;
    }
    root.replaceChildren(el("p", { class: "boot-error" }, `Server unreachable (${(err as Error).message}). Retrying…`));
    bootTimer = setTimeout(() => void boot(), 5000);
  }
}

// The service worker makes the app installable and opens it offline. Only
// in production builds: during `vite dev` it would cache dev-server files.
if (import.meta.env.PROD && "serviceWorker" in navigator) {
  navigator.serviceWorker.register("/sw.js").catch((err: unknown) => {
    console.warn("service worker registration failed", err);
  });
}

void boot();
