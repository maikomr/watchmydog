// Thin typed wrapper over the watchmydog HTTP API.
// Every endpoint gets a function here so UI code never builds URLs itself.

export interface Health {
  status: "ok" | "degraded";
  version: string;
  commit: string;
  time: string;
  engine: string;
}

export interface Session {
  authenticated: boolean;
  /** False when the server runs with auth disabled. */
  authRequired: boolean;
  subject?: string;
  expiresAt?: string;
}

export type CameraState = "idle" | "active" | "paused" | "error";

export interface StreamVariant {
  quality: "high" | "low";
  name: string;
  width: number;
  height: number;
}

export interface Camera {
  id: string;
  name: string;
  width: number;
  height: number;
  fps: number;
  hasAudio: boolean;
  state: CameraState;
  consumers: number;
  detail?: string;
  stream: string;
  streams: StreamVariant[];
}

/** Something the microphone picked up (see docs/adr/0007). */
export interface SoundEvent {
  id: string;
  cameraId: string;
  kind: "sound";
  /** ISO 8601, UTC. */
  startedAt: string;
  /** null while the event is going on. */
  endedAt: string | null;
  /** Loudest 100 ms of the event in dBFS (0 = microphone's maximum). */
  peakDbfs: number;
}

/** A recorded piece of video around one or more noises (see docs/adr/0009). */
export interface Clip {
  id: string;
  cameraId: string;
  /** ISO 8601, UTC: the first picture of the clip. */
  startedAt: string;
  durationSeconds: number;
  sizeBytes: number;
  /** Plays the clip; with "?download=1" the browser saves it as a file. */
  videoUrl: string;
}

export interface ClipList {
  /** False when recording is switched off in the server's config. */
  enabled: boolean;
  /** Newest first. */
  clips: Clip[];
  /** Disk space of all clips, and the limit at which the oldest are deleted. */
  usedBytes: number;
  maxBytes: number;
  /** How long a clip is kept; 0 = until the disk limit removes it. */
  keepSeconds: number;
}

/** How this server sends notifications (see docs/adr/0008). */
export interface PushConfig {
  /** False when notifications are switched off in the server's config. */
  enabled: boolean;
  /** Key the browser subscribes with (VAPID public key, base64url). */
  publicKey: string;
  /** Minimum time between two notifications for the same camera. */
  cooldownSeconds: number;
  /** Daily span without notifications, on the Mac's clock ("HH:MM"). */
  quietHours: { start: string; end: string } | null;
}

/** What PushSubscription.toJSON() returns in the browser. */
export interface PushSubscriptionData {
  endpoint?: string;
  keys?: Record<string, string>;
}

/** An HTTP error from the API, with the server's message when it sent one. */
export class ApiError extends Error {
  constructor(
    message: string,
    public readonly status: number,
    public readonly retryAfterSeconds?: number,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

/** True when the request failed because the session is missing or expired. */
export function isUnauthorized(err: unknown): boolean {
  return err instanceof ApiError && err.status === 401;
}

async function request(url: string, init?: RequestInit): Promise<Response> {
  const res = await fetch(url, init);
  if (!res.ok) {
    let msg = `HTTP ${res.status}`;
    let retryAfter: number | undefined;
    try {
      const body = (await res.json()) as { error?: string; retryAfterSeconds?: number };
      if (body.error) msg = body.error;
      retryAfter = body.retryAfterSeconds;
    } catch {
      /* no JSON body */
    }
    throw new ApiError(msg, res.status, retryAfter);
  }
  return res;
}

async function getJSON<T>(url: string, init?: RequestInit): Promise<T> {
  return (await (await request(url, init)).json()) as T;
}

function jsonPost(body?: unknown): RequestInit {
  return {
    method: "POST",
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  };
}

function postJSON<T>(url: string, body?: unknown): Promise<T> {
  return getJSON<T>(url, jsonPost(body));
}

export const api = {
  health: () => getJSON<Health>("/api/health"),
  session: () => getJSON<Session>("/api/session"),
  login: (secret: string) => postJSON<Session>("/api/login", { secret }),
  logout: async () => {
    await request("/api/logout", { method: "POST" });
  },
  cameras: async () => (await getJSON<{ cameras: Camera[] }>("/api/cameras")).cameras,
  camera: (id: string) => getJSON<Camera>(`/api/cameras/${encodeURIComponent(id)}`),
  pause: (id: string) => postJSON<Camera>(`/api/cameras/${encodeURIComponent(id)}/pause`),
  resume: (id: string) => postJSON<Camera>(`/api/cameras/${encodeURIComponent(id)}/resume`),
  pushConfig: () => getJSON<PushConfig>("/api/push/config"),
  pushSubscribe: async (sub: PushSubscriptionData) => {
    await request("/api/push/subscribe", jsonPost(sub));
  },
  pushUnsubscribe: async (endpoint: string) => {
    await request("/api/push/unsubscribe", jsonPost({ endpoint }));
  },
  /** Asks the server for a test notification to this device. */
  pushTest: async (endpoint: string) => {
    await request("/api/push/test", jsonPost({ endpoint }));
  },
  clips: (cameraId: string, limit: number) =>
    getJSON<ClipList>(`/api/clips?camera=${encodeURIComponent(cameraId)}&limit=${limit}`),
  deleteClip: async (id: string) => {
    await request(`/api/clips/${encodeURIComponent(id)}`, { method: "DELETE" });
  },
  /** Newest first. */
  events: async (cameraId: string, limit: number) =>
    (await getJSON<{ events: SoundEvent[] }>(`/api/events?camera=${encodeURIComponent(cameraId)}&limit=${limit}`)).events,
};
