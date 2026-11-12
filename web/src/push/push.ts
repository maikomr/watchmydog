// Notifications on this device: asks the browser for a push subscription
// and registers it with the server (ADR-0008). The browser and the API are
// injected, so the logic is tested without either.
//
// A subscription lives in the browser, per installation. The server only
// keeps a copy to know where to send. "On" therefore means: permission
// granted, the browser has a subscription made with this server's key, and
// the server has been told about it.

import type { PushConfig, PushSubscriptionData } from "../api";

export type PushState =
  /** The browser cannot do push (or the service worker is not running). */
  | "unsupported"
  /** Switched off in the server's config. */
  | "disabled"
  /** The user blocked notifications for this site. */
  | "denied"
  | "off"
  | "on";

export interface PushStatus {
  state: PushState;
  /** Null when the server could not be asked. */
  config: PushConfig | null;
}

export interface SubscriptionLike {
  endpoint: string;
  options?: { applicationServerKey: ArrayBuffer | null };
  toJSON(): PushSubscriptionData;
  unsubscribe(): Promise<boolean>;
}

export interface PushManagerLike {
  getSubscription(): Promise<SubscriptionLike | null>;
  subscribe(options: { userVisibleOnly: boolean; applicationServerKey: Uint8Array<ArrayBuffer> }): Promise<SubscriptionLike>;
}

/** The parts of the browser this module needs. */
export interface PushEnv {
  permission(): NotificationPermission;
  requestPermission(): Promise<NotificationPermission>;
  pushManager: PushManagerLike;
}

export interface PushApi {
  pushConfig(): Promise<PushConfig>;
  pushSubscribe(sub: PushSubscriptionData): Promise<void>;
  pushUnsubscribe(endpoint: string): Promise<void>;
  pushTest(endpoint: string): Promise<void>;
}

/** base64url (as VAPID keys are written) to bytes. */
export function base64UrlToBytes(value: string): Uint8Array<ArrayBuffer> {
  const b64 = value.replace(/-/g, "+").replace(/_/g, "/").replace(/=+$/, "");
  const bin = atob(b64 + "=".repeat((4 - (b64.length % 4)) % 4));
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

function sameBytes(a: ArrayBuffer | null | undefined, b: Uint8Array): boolean {
  if (!a) return false;
  const x = new Uint8Array(a);
  return x.length === b.length && x.every((v, i) => v === b[i]);
}

export class PushControl {
  private config: PushConfig | null = null;

  /** env is null when the browser cannot do push. */
  constructor(
    private readonly env: PushEnv | null,
    private readonly api: PushApi,
  ) {}

  /**
   * Finds out where this device stands. When it is subscribed, the server
   * is told again: that repairs a server that lost its database, and costs
   * one small request per app start.
   */
  async init(): Promise<PushStatus> {
    this.config = await this.api.pushConfig();
    const state = await this.currentState();
    return { state, config: this.config };
  }

  private async currentState(): Promise<PushState> {
    if (!this.env) return "unsupported";
    if (!this.config?.enabled) return "disabled";
    if (this.env.permission() === "denied") return "denied";
    const sub = await this.env.pushManager.getSubscription();
    if (!sub || this.env.permission() !== "granted") return "off";
    if (!this.madeWithServerKey(sub)) {
      // The server has a new key pair: this subscription can no longer be
      // used by it. Drop it; the user switches notifications on again.
      await sub.unsubscribe();
      return "off";
    }
    await this.api.pushSubscribe(sub.toJSON());
    return "on";
  }

  private madeWithServerKey(sub: SubscriptionLike): boolean {
    // Browsers that do not expose the key get the benefit of the doubt.
    if (!sub.options) return true;
    return sameBytes(sub.options.applicationServerKey, base64UrlToBytes(this.config!.publicKey));
  }

  /** Must be called from a tap: browsers only ask for permission then. */
  async enable(): Promise<PushStatus> {
    if (!this.env) return { state: "unsupported", config: this.config };
    this.config = await this.api.pushConfig(); // the key may have changed
    if (!this.config.enabled) return { state: "disabled", config: this.config };

    const permission = this.env.permission() === "granted" ? "granted" : await this.env.requestPermission();
    if (permission === "denied") return { state: "denied", config: this.config };
    if (permission !== "granted") return { state: "off", config: this.config }; // prompt dismissed

    const key = base64UrlToBytes(this.config.publicKey);
    let sub = await this.env.pushManager.getSubscription();
    if (sub && !this.madeWithServerKey(sub)) {
      await sub.unsubscribe();
      sub = null;
    }
    sub ??= await this.env.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key });
    try {
      await this.api.pushSubscribe(sub.toJSON());
    } catch (err) {
      // Do not leave a subscription the server knows nothing about.
      await sub.unsubscribe().catch(() => undefined);
      throw err;
    }
    return { state: "on", config: this.config };
  }

  async disable(): Promise<PushStatus> {
    if (!this.env) return { state: "unsupported", config: this.config };
    const sub = await this.env.pushManager.getSubscription();
    if (sub) {
      // Browser first: once it is gone there, nothing can arrive. If telling
      // the server fails, it finds out on its next send and drops the device.
      await sub.unsubscribe();
      await this.api.pushUnsubscribe(sub.endpoint).catch(() => undefined);
    }
    return { state: await this.currentState(), config: this.config };
  }

  /** Asks the server to send a test notification to this device. */
  async test(): Promise<void> {
    const sub = await this.env?.pushManager.getSubscription();
    if (!sub) throw new Error("Notifications are not on for this device.");
    await this.api.pushTest(sub.endpoint);
  }
}

/** One or two sentences for under the switch. */
export function describePush(status: PushStatus): string {
  switch (status.state) {
    case "unsupported":
      return "This browser cannot receive notifications. On Android use Chrome; on an iPhone, add the app to the Home Screen first.";
    case "disabled":
      return "Notifications are switched off on the server.";
    case "denied":
      return "Notifications are blocked for this site. Allow them in the browser's site settings, then reload.";
    case "off":
      return "Get a notification on this device when noise is detected.";
    case "on":
      return `On for this device.${describeRules(status.config)}`;
  }
}

function describeRules(c: PushConfig | null): string {
  if (!c) return "";
  let out = "";
  if (c.cooldownSeconds > 0) out += ` At most one every ${formatSpan(c.cooldownSeconds)}.`;
  if (c.quietHours) out += ` None between ${c.quietHours.start} and ${c.quietHours.end} (the Mac's clock).`;
  return out;
}

function formatSpan(seconds: number): string {
  if (seconds % 3600 === 0) return seconds === 3600 ? "hour" : `${seconds / 3600} hours`;
  if (seconds % 60 === 0) return seconds === 60 ? "minute" : `${seconds / 60} minutes`;
  return `${seconds} seconds`;
}

/**
 * The real browser, or null when it cannot do push. registration is the
 * active service worker registration (null in development, where no
 * service worker is registered).
 */
export function browserPushEnv(registration: ServiceWorkerRegistration | null): PushEnv | null {
  if (!registration || !("PushManager" in globalThis) || !("Notification" in globalThis)) return null;
  return {
    permission: () => Notification.permission,
    requestPermission: () => Notification.requestPermission(),
    pushManager: registration.pushManager,
  };
}
