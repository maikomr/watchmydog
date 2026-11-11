import { getHealth } from "./api";

const statusEl = document.getElementById("status")!;

async function render(): Promise<void> {
  try {
    const h = await getHealth();
    statusEl.textContent = `server ok · ${h.version} (${h.commit})`;
    statusEl.className = "ok";
  } catch (err) {
    statusEl.textContent = `server unreachable: ${(err as Error).message}`;
    statusEl.className = "err";
  }
}

void render();
