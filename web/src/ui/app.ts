// Live view UI: one camera, one <video>, status pill, controls. Pure DOM,
// no framework; the player logic lives in ../player and is tested on its own.

import { api, type Camera, type StreamVariant } from "../api";
import { LivePlayer, type PlayerState, type PlayerStats } from "../player/player";

const QUALITY_KEY = "wmd.quality";
const CAMERA_POLL_MS = 10_000;

function el<K extends keyof HTMLElementTagNameMap>(tag: K, attrs: Record<string, string> = {}, ...children: (Node | string)[]): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) node.setAttribute(k, v);
  node.append(...children);
  return node;
}

function readStoredQuality(): string | null {
  try {
    return localStorage.getItem(QUALITY_KEY);
  } catch {
    return null;
  }
}

function storeQuality(q: string): void {
  try {
    localStorage.setItem(QUALITY_KEY, q);
  } catch {
    /* private mode etc. */
  }
}

export class App {
  private camera: Camera | null = null;
  private variant: StreamVariant | null = null;
  private player: LivePlayer | null = null;

  private readonly video = el("video", { playsinline: "", autoplay: "", muted: "" });
  private readonly title = el("h1", {}, "watchmydog");
  private readonly pill = el("span", { class: "pill" }, "loading");
  private readonly overlay = el("div", { class: "overlay hidden" });
  private readonly overlayText = el("div", { class: "overlay-text" });
  private readonly unmuteBtn = el("button", { class: "btn primary" }, "Tap for sound");
  private readonly resumeBtn = el("button", { class: "btn primary hidden" }, "Resume camera");
  private readonly qualityBtn = el("button", { class: "btn" }, "Quality");
  private readonly fullscreenBtn = el("button", { class: "btn" }, "Fullscreen");
  private readonly pauseBtn = el("button", { class: "btn" }, "Release camera");
  private readonly stats = el("div", { class: "stats" }, "");
  private readonly stage = el("div", { class: "stage" });

  constructor(private readonly root: HTMLElement) {}

  mount(): void {
    this.video.muted = true;
    this.overlay.append(this.overlayText, this.unmuteBtn, this.resumeBtn);
    this.stage.append(this.video, this.overlay);
    const header = el("header", {}, this.title, this.pill);
    const controls = el("div", { class: "controls" }, this.qualityBtn, this.fullscreenBtn, this.pauseBtn);
    this.root.replaceChildren(header, this.stage, controls, this.stats);

    this.unmuteBtn.addEventListener("click", () => {
      this.video.muted = false;
      void this.video.play().catch(() => undefined);
      this.renderOverlay();
    });
    this.resumeBtn.addEventListener("click", () => void this.setPaused(false));
    this.pauseBtn.addEventListener("click", () => void this.setPaused(this.camera?.state !== "paused"));
    this.qualityBtn.addEventListener("click", () => this.toggleQuality());
    this.fullscreenBtn.addEventListener("click", () => void this.toggleFullscreen());
    this.video.addEventListener("volumechange", () => this.renderOverlay());

    document.addEventListener("visibilitychange", () => {
      if (document.visibilityState === "visible") this.player?.reconnectNow();
    });

    void this.load();
  }

  private async load(): Promise<void> {
    try {
      const cameras = await api.cameras();
      if (cameras.length === 0) {
        this.setPill("no cameras", "err");
        return;
      }
      this.applyCamera(cameras[0]);
      setInterval(() => void this.refreshCamera(), CAMERA_POLL_MS);
      this.startPlayer();
    } catch (err) {
      this.setPill(`server unreachable: ${(err as Error).message}`, "err");
      setTimeout(() => void this.load(), 5000);
    }
  }

  private applyCamera(c: Camera): void {
    this.camera = c;
    this.title.textContent = c.name || c.id;
    const stored = readStoredQuality();
    this.variant = c.streams.find((s) => s.quality === stored) ?? c.streams[0];
    this.qualityBtn.textContent = `Quality: ${this.variant.quality} (${this.variant.height}p)`;
    this.qualityBtn.disabled = c.streams.length < 2;
    this.pauseBtn.textContent = c.state === "paused" ? "Resume camera" : "Release camera";
    this.renderOverlay();
  }

  private async refreshCamera(): Promise<void> {
    if (!this.camera) return;
    try {
      const c = await api.camera(this.camera.id);
      const wasPaused = this.camera.state === "paused";
      this.applyCamera(c);
      if (wasPaused && c.state !== "paused") this.player?.reconnectNow();
    } catch {
      /* keep last known state; the player reports connectivity */
    }
  }

  private startPlayer(): void {
    if (!this.variant) return;
    if (!this.player) {
      this.player = new LivePlayer({ video: this.video });
      this.player.on("state", (s, d) => this.onPlayerState(s, d));
      this.player.on("stats", (s) => this.onStats(s));
    }
    this.player.play(this.variant.name);
  }

  private onPlayerState(state: PlayerState, detail?: string): void {
    const paused = this.camera?.state === "paused";
    switch (state) {
      case "connecting":
        this.setPill(paused ? "camera paused" : "connecting", paused ? "warn" : "");
        break;
      case "live":
        this.setPill("live", "ok");
        break;
      case "reconnecting":
        this.setPill(paused ? "camera paused" : `reconnecting (${detail ?? ""})`, "warn");
        this.stats.textContent = "";
        break;
      case "stopped":
        this.setPill("stopped", "");
        break;
    }
    this.renderOverlay();
  }

  private onStats(s: PlayerStats): void {
    this.stats.textContent = `${s.fps.toFixed(0)} fps · ${s.kbps.toFixed(0)} kbit/s · ${this.variant?.height ?? ""}p`;
  }

  private setPill(text: string, cls: "" | "ok" | "warn" | "err"): void {
    this.pill.textContent = text;
    this.pill.className = `pill ${cls}`;
  }

  private renderOverlay(): void {
    const state = this.player?.currentState ?? "idle";
    const paused = this.camera?.state === "paused";
    const needsUnmute = state === "live" && this.video.muted;
    this.unmuteBtn.classList.toggle("hidden", !needsUnmute);
    this.resumeBtn.classList.toggle("hidden", !paused);
    let text = "";
    if (paused) text = "Camera released for other apps";
    else if (state === "connecting") text = "Connecting…";
    else if (state === "reconnecting") text = "Reconnecting…";
    this.overlayText.textContent = text;
    this.overlay.classList.toggle("hidden", !text && !needsUnmute);
  }

  private async setPaused(paused: boolean): Promise<void> {
    if (!this.camera) return;
    this.pauseBtn.disabled = true;
    try {
      const c = paused ? await api.pause(this.camera.id) : await api.resume(this.camera.id);
      this.applyCamera(c);
      if (!paused) this.player?.reconnectNow();
    } catch (err) {
      this.setPill(`error: ${(err as Error).message}`, "err");
    } finally {
      this.pauseBtn.disabled = false;
    }
  }

  private toggleQuality(): void {
    if (!this.camera || this.camera.streams.length < 2) return;
    const idx = this.camera.streams.findIndex((s) => s.name === this.variant?.name);
    this.variant = this.camera.streams[(idx + 1) % this.camera.streams.length];
    storeQuality(this.variant.quality);
    this.qualityBtn.textContent = `Quality: ${this.variant.quality} (${this.variant.height}p)`;
    this.startPlayer();
  }

  private async toggleFullscreen(): Promise<void> {
    try {
      if (document.fullscreenElement) await document.exitFullscreen();
      else await this.stage.requestFullscreen();
    } catch {
      /* unsupported */
    }
  }
}
