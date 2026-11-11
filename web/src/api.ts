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

function postJSON<T>(url: string, body?: unknown): Promise<T> {
  return getJSON<T>(url, {
    method: "POST",
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
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
};
