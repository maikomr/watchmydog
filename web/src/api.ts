// Thin typed wrapper over the watchmydog HTTP API.
// Every endpoint gets a function here so UI code never builds URLs itself.

export interface Health {
  status: "ok" | "degraded";
  version: string;
  commit: string;
  time: string;
  engine: string;
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

async function getJSON<T>(url: string, init?: RequestInit): Promise<T> {
  const res = await fetch(url, init);
  if (!res.ok) {
    let msg = `HTTP ${res.status}`;
    try {
      const body = (await res.json()) as { error?: string };
      if (body.error) msg = body.error;
    } catch {
      /* no JSON body */
    }
    throw new Error(msg);
  }
  return (await res.json()) as T;
}

export const api = {
  health: () => getJSON<Health>("/api/health"),
  cameras: async () => (await getJSON<{ cameras: Camera[] }>("/api/cameras")).cameras,
  camera: (id: string) => getJSON<Camera>(`/api/cameras/${encodeURIComponent(id)}`),
  pause: (id: string) => getJSON<Camera>(`/api/cameras/${encodeURIComponent(id)}/pause`, { method: "POST" }),
  resume: (id: string) => getJSON<Camera>(`/api/cameras/${encodeURIComponent(id)}/resume`, { method: "POST" }),
};
