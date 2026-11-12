// What the service worker does with a push message, as pure functions so
// they can be unit-tested outside a service-worker runtime. The payload
// shape matches Payload in internal/adapters/notify/webpush.

export interface PushPayload {
  title: string;
  body: string;
  /** Path inside the app to open when the notification is tapped. */
  url: string;
  tag?: string;
  /** When the reported thing happened, Unix milliseconds. */
  ts?: number;
}

export interface NotificationSpec {
  title: string;
  options: {
    body: string;
    tag?: string;
    /** Alert again (sound/vibration) when replacing one with the same tag. */
    renotify?: boolean;
    icon: string;
    badge: string;
    timestamp?: number;
    data: { url: string };
  };
}

const ICON = "/icons/icon-192.png";

/**
 * Turns the text of a push message into a notification. Chrome requires
 * that every push shows a notification, so a message we cannot read still
 * produces a generic one instead of nothing.
 */
export function notificationFor(text: string | null): NotificationSpec {
  let p: Partial<PushPayload> = {};
  try {
    const parsed: unknown = text ? JSON.parse(text) : null;
    if (parsed && typeof parsed === "object") p = parsed as Partial<PushPayload>;
  } catch {
    /* not JSON: fall through to the generic notification */
  }
  const title = typeof p.title === "string" && p.title ? p.title : "watchmydog";
  const spec: NotificationSpec = {
    title,
    options: {
      body: typeof p.body === "string" ? p.body : "",
      icon: ICON,
      badge: ICON,
      data: { url: safeAppPath(p.url) },
    },
  };
  if (typeof p.tag === "string" && p.tag) {
    spec.options.tag = p.tag;
    spec.options.renotify = true;
  }
  if (typeof p.ts === "number" && Number.isFinite(p.ts)) spec.options.timestamp = p.ts;
  return spec;
}

/**
 * Only paths inside the app are opened: a notification must never be able
 * to send the user to another site.
 */
export function safeAppPath(url: unknown): string {
  if (typeof url !== "string" || !url.startsWith("/") || url.startsWith("//") || url.includes("\\")) return "/";
  return url;
}

export interface WindowLike {
  url: string;
}

/**
 * Picks the app window to bring to the front when a notification is tapped:
 * any window of this origin (the app is a single page). Returns its index,
 * or -1 when a new window must be opened.
 */
export function pickWindow(windows: readonly WindowLike[], origin: string): number {
  return windows.findIndex((w) => {
    try {
      return new URL(w.url).origin === origin;
    } catch {
      return false;
    }
  });
}
