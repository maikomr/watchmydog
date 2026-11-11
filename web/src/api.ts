// Thin typed wrapper over the watchmydog HTTP API.
// Every endpoint gets a function here so UI code never builds URLs itself.

export interface Health {
  status: "ok";
  version: string;
  commit: string;
  time: string;
}

export async function getHealth(): Promise<Health> {
  const res = await fetch("/api/health");
  if (!res.ok) {
    throw new Error(`health: HTTP ${res.status}`);
  }
  return (await res.json()) as Health;
}
