// Live view UI: one camera, one <video>, status pill, controls. Pure DOM,
// no framework; the player logic lives in ../player and is tested on its own.

import { api, isUnauthorized, type Camera, type Clip, type SoundEvent, type StreamVariant } from "../api";
import { describeClip, describeStorage, sameClips } from "../clips/format";
import { describeAudioHealth } from "../player/audio-health";
import { LivePlayer, type PlayerState, type PlayerStats } from "../player/player";
import { browserPushEnv, describePush, PushControl, type PushStatus } from "../push/push";
import { describeEvent, formatWhen, levelFraction, mergeEvent } from "../sound/format";
import { SoundStream, type SoundHello, type SoundLevel } from "../sound/stream";
import { el } from "./dom";
import { pickCamera } from "./route";

const QUALITY_KEY = "wmd.quality";
const CAMERA_POLL_MS = 10_000;
/** How many events the list shows. */
const EVENT_LIST_MAX = 20;
/** How many clips the list shows. */
const CLIP_LIST_MAX = 20;
/**
 * How often the clip list is reloaded while the app is visible. A clip
 * appears up to half a minute after its noise ended, so there is no event
 * to wait for.
 */
const CLIP_POLL_MS = 15_000;

export interface AppHooks {
  /** The session is missing or expired: show the login screen. */
  onUnauthorized: () => void;
  /** The user asked to log out. */
  onLogout: () => void;
  /**
   * Resolves to the active service worker registration, or null when there
   * is none (development, unsupported browser). Needed for notifications.
   */
  serviceWorker: Promise<ServiceWorkerRegistration | null>;
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
  private pollTimer: ReturnType<typeof setInterval> | null = null;
  private retryTimer: ReturnType<typeof setTimeout> | null = null;
  private destroyed = false;
  private sound: SoundStream | null = null;
  private events: SoundEvent[] = [];

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
  private readonly logoutBtn = el("button", { class: "btn" }, "Log out");
  private readonly stats = el("div", { class: "stats" }, "");
  private readonly stage = el("div", { class: "stage" });
  // Sound section: level meter with a threshold mark, then the event list.
  private readonly meterFill = el("div", { class: "meter-fill" });
  private readonly meterMark = el("div", { class: "meter-mark hidden", title: "Alert level" });
  private readonly meterValue = el("span", { class: "meter-value" }, "–");
  // Notifications: a switch for this device and a test button.
  private readonly pushBtn = el("button", { class: "btn" }, "Turn on");
  private readonly pushTestBtn = el("button", { class: "btn hidden" }, "Send test");
  private readonly pushNote = el("p", { class: "muted notify-note" }, "");
  private push: PushControl | null = null;
  private pushStatus: PushStatus | null = null;
  private readonly eventsNote = el("p", { class: "muted events-note" }, "");
  private readonly eventList = el("ul", { class: "events" });
  // Clips: recorded video around the noise events.
  private readonly clipsNote = el("p", { class: "muted events-note" }, "");
  private readonly clipList = el("ul", { class: "events clips" });
  private clips: Clip[] | null = null;
  /** The clip whose player is open, if any. */
  private openClip: string | null = null;
  private clipTimer: ReturnType<typeof setInterval> | null = null;

  private readonly onVisibility = () => {
    if (document.visibilityState === "visible") {
      this.player?.reconnectNow();
      this.sound?.start();
      void this.loadClips();
    } else {
      // No point in ten readings a second for a screen nobody sees.
      this.sound?.stop();
    }
  };

  constructor(
    private readonly root: HTMLElement,
    private readonly hooks: AppHooks,
  ) {}

  mount(): void {
    this.video.muted = true;
    this.overlay.append(this.overlayText, this.unmuteBtn, this.resumeBtn);
    this.stage.append(this.video, this.overlay);
    const header = el("header", {}, this.title, this.pill);
    const controls = el("div", { class: "controls" }, this.qualityBtn, this.fullscreenBtn, this.pauseBtn, this.logoutBtn);
    const meter = el("div", { class: "meter" }, this.meterFill, this.meterMark);
    const sound = el(
      "section",
      { class: "sound" },
      el("div", { class: "meter-row" }, el("span", { class: "muted" }, "Sound"), meter, this.meterValue),
      el("h2", {}, "Notifications"),
      el("div", { class: "notify-row" }, this.pushBtn, this.pushTestBtn),
      this.pushNote,
      el("h2", {}, "Noise events"),
      this.eventsNote,
      this.eventList,
      el("h2", {}, "Clips"),
      this.clipsNote,
      this.clipList,
    );
    this.pushBtn.disabled = true;
    this.root.replaceChildren(header, this.stage, controls, this.stats, sound);

    this.unmuteBtn.addEventListener("click", () => {
      this.video.muted = false;
      void this.video.play().catch(() => undefined);
      this.renderOverlay();
    });
    this.resumeBtn.addEventListener("click", () => void this.setPaused(false));
    this.pauseBtn.addEventListener("click", () => void this.setPaused(this.camera?.state !== "paused"));
    this.qualityBtn.addEventListener("click", () => this.toggleQuality());
    this.fullscreenBtn.addEventListener("click", () => void this.toggleFullscreen());
    this.logoutBtn.addEventListener("click", () => this.hooks.onLogout());
    this.pushBtn.addEventListener("click", () => void this.togglePush());
    this.pushTestBtn.addEventListener("click", () => void this.testPush());
    this.video.addEventListener("volumechange", () => this.renderOverlay());
    document.addEventListener("visibilitychange", this.onVisibility);

    void this.load();
    void this.initPush();
  }

  /** Stops playback, timers and listeners. The view can then be replaced. */
  destroy(): void {
    this.destroyed = true;
    this.player?.stop();
    this.player = null;
    this.sound?.stop();
    this.sound = null;
    if (this.pollTimer !== null) clearInterval(this.pollTimer);
    if (this.retryTimer !== null) clearTimeout(this.retryTimer);
    if (this.clipTimer !== null) clearInterval(this.clipTimer);
    this.pollTimer = this.retryTimer = this.clipTimer = null;
    document.removeEventListener("visibilitychange", this.onVisibility);
  }

  /** Routes 401s to the login screen; returns true when it did. */
  private handleAuthError(err: unknown): boolean {
    if (!isUnauthorized(err) || this.destroyed) return false;
    this.hooks.onUnauthorized();
    return true;
  }

  private async load(): Promise<void> {
    try {
      const cameras = await api.cameras();
      if (this.destroyed) return;
      if (cameras.length === 0) {
        this.setPill("no cameras", "err");
        return;
      }
      // A notification links to "/?camera=<id>".
      const camera = pickCamera(cameras, location.search)!;
      this.applyCamera(camera);
      this.pollTimer = setInterval(() => void this.refreshCamera(), CAMERA_POLL_MS);
      this.startPlayer();
      this.startSound(camera);
      void this.loadClips();
      this.clipTimer = setInterval(() => void this.loadClips(), CLIP_POLL_MS);
    } catch (err) {
      if (this.handleAuthError(err) || this.destroyed) return;
      this.setPill(`server unreachable: ${(err as Error).message}`, "err");
      this.retryTimer = setTimeout(() => void this.load(), 5000);
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
      if (this.destroyed) return;
      const wasPaused = this.camera.state === "paused";
      this.applyCamera(c);
      if (wasPaused && c.state !== "paused") this.player?.reconnectNow();
    } catch (err) {
      // Expired session: go to login. Otherwise keep the last known state;
      // the player reports connectivity on its own.
      this.handleAuthError(err);
    }
  }

  private startPlayer(): void {
    if (!this.variant || this.destroyed) return;
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
        // A 401 from the media endpoint means the session expired: do not
        // keep retrying, ask for login instead (camera poll would too).
        if (detail?.includes("HTTP 401")) this.hooks.onUnauthorized();
        break;
      case "stopped":
        this.setPill("stopped", "");
        break;
    }
    this.renderOverlay();
  }

  private onStats(s: PlayerStats): void {
    const audio = describeAudioHealth(s.audio);
    this.stats.textContent =
      `${s.fps.toFixed(0)} fps · ${s.kbps.toFixed(0)} kbit/s · ${this.variant?.height ?? ""}p` + (audio ? ` · ${audio}` : "");
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

  // --- sound ---------------------------------------------------------------

  private startSound(c: Camera): void {
    if (!c.hasAudio) {
      this.eventsNote.textContent = "This camera has no microphone.";
      return;
    }
    this.sound = new SoundStream({
      cameraId: c.id,
      onHello: (h) => this.onSoundHello(h),
      onLevel: (l) => this.renderLevel(l),
      onEvent: (ev) => {
        this.events = mergeEvent(this.events, ev, EVENT_LIST_MAX);
        this.renderEvents();
      },
    });
    this.renderEvents();
    if (document.visibilityState === "visible") this.sound.start();
  }

  /** Runs on every (re)connect of the stream. */
  private onSoundHello(h: SoundHello): void {
    this.meterMark.classList.toggle("hidden", !h.enabled);
    this.meterMark.style.left = `${levelFraction(h.thresholdDbfs) * 100}%`;
    this.eventsNote.dataset.disabled = h.enabled ? "" : "1";
    // Reload the list: events may have happened while we were not connected.
    void this.loadEvents(h.ongoing);
  }

  private async loadEvents(ongoing: SoundEvent[]): Promise<void> {
    if (!this.camera) return;
    try {
      let list = await api.events(this.camera.id, EVENT_LIST_MAX);
      if (this.destroyed) return;
      for (const ev of ongoing) list = mergeEvent(list, ev, EVENT_LIST_MAX);
      this.events = list;
      this.renderEvents();
    } catch (err) {
      if (!this.handleAuthError(err)) this.eventsNote.textContent = `Could not load events: ${(err as Error).message}`;
    }
  }

  private renderLevel(l: SoundLevel | null): void {
    if (!l) {
      this.meterFill.style.transform = "scaleX(0)";
      this.meterFill.className = "meter-fill";
      this.meterValue.textContent = "–";
      return;
    }
    this.meterFill.style.transform = `scaleX(${levelFraction(l.dbfs).toFixed(3)})`;
    this.meterFill.className = l.loud ? "meter-fill loud" : "meter-fill";
    this.meterValue.textContent = `${l.dbfs.toFixed(0)} dB`;
  }

  private renderEvents(): void {
    const now = new Date();
    this.eventList.replaceChildren(
      ...this.events.map((ev) =>
        el(
          "li",
          ev.endedAt === null ? { class: "ongoing" } : {},
          el("span", { class: "when" }, formatWhen(new Date(ev.startedAt), now)),
          el("span", { class: "what" }, describeEvent(ev)),
        ),
      ),
    );
    if (this.events.length > 0) this.eventsNote.textContent = "";
    else if (this.eventsNote.dataset.disabled) this.eventsNote.textContent = "Noise detection is switched off.";
    else this.eventsNote.textContent = "No noise detected yet.";
  }

  // --- clips ---------------------------------------------------------------

  private async loadClips(): Promise<void> {
    if (!this.camera || this.destroyed || document.visibilityState !== "visible") return;
    try {
      const list = await api.clips(this.camera.id, CLIP_LIST_MAX);
      if (this.destroyed) return;
      this.clipsNote.textContent = describeStorage(list);
      // Rebuilding the list would stop a clip that is playing, so only do
      // it when the clips changed.
      if (this.clips && sameClips(this.clips, list.clips)) return;
      this.clips = list.clips;
      this.renderClips();
    } catch (err) {
      if (!this.handleAuthError(err)) this.clipsNote.textContent = `Could not load clips: ${(err as Error).message}`;
    }
  }

  private renderClips(): void {
    const now = new Date();
    const clips = this.clips ?? [];
    if (!clips.some((c) => c.id === this.openClip)) this.openClip = null;
    this.clipList.replaceChildren(...clips.map((c) => this.clipItem(c, now)));
  }

  private clipItem(c: Clip, now: Date): HTMLLIElement {
    const open = c.id === this.openClip;
    const play = el("button", { class: "btn small" }, open ? "Close" : "Play");
    const save = el("a", { class: "btn small", href: `${c.videoUrl}?download=1`, download: "" }, "Save");
    const del = el("button", { class: "btn small" }, "Delete");
    play.addEventListener("click", () => {
      this.openClip = open ? null : c.id;
      this.renderClips();
    });
    del.addEventListener("click", () => void this.deleteClip(c, del));
    const item = el(
      "li",
      {},
      el(
        "div",
        { class: "clip-row" },
        el("span", { class: "when" }, formatWhen(new Date(c.startedAt), now)),
        el("span", { class: "what" }, describeClip(c)),
      ),
      el("div", { class: "clip-actions" }, play, save, del),
    );
    if (open) {
      const video = el("video", { class: "clip-video", controls: "", playsinline: "", preload: "metadata", src: c.videoUrl });
      // Two sound tracks at once are confusing: silence the live view
      // while a clip plays ("Tap for sound" brings it back).
      video.addEventListener("play", () => {
        this.video.muted = true;
      });
      item.append(video);
      void video.play().catch(() => undefined);
    }
    return item;
  }

  private async deleteClip(c: Clip, button: HTMLButtonElement): Promise<void> {
    if (!confirm("Delete this clip? This cannot be undone.")) return;
    button.disabled = true;
    try {
      await api.deleteClip(c.id);
    } catch (err) {
      if (this.handleAuthError(err) || this.destroyed) return;
      // 404: someone else or the clean-up already deleted it; just reload.
      this.clipsNote.textContent = `Could not delete the clip: ${(err as Error).message}`;
    }
    this.clips = null; // force a rebuild
    await this.loadClips();
  }

  // --- notifications -------------------------------------------------------

  private async initPush(): Promise<void> {
    try {
      this.push = new PushControl(browserPushEnv(await this.hooks.serviceWorker), api);
      const status = await this.push.init();
      if (!this.destroyed) this.renderPush(status);
    } catch (err) {
      if (this.handleAuthError(err) || this.destroyed) return;
      this.pushNote.textContent = `Notifications unavailable: ${(err as Error).message}`;
    }
  }

  private renderPush(status: PushStatus, note?: string): void {
    this.pushStatus = status;
    const on = status.state === "on";
    this.pushBtn.textContent = on ? "Turn off" : "Turn on";
    this.pushBtn.disabled = status.state !== "on" && status.state !== "off";
    this.pushBtn.classList.toggle("primary", status.state === "off");
    this.pushTestBtn.classList.toggle("hidden", !on);
    this.pushNote.textContent = note ?? describePush(status);
  }

  /** Runs from a tap, which browsers require before asking for permission. */
  private async togglePush(): Promise<void> {
    if (!this.push || !this.pushStatus) return;
    this.pushBtn.disabled = true;
    try {
      const status = this.pushStatus.state === "on" ? await this.push.disable() : await this.push.enable();
      if (!this.destroyed) this.renderPush(status);
    } catch (err) {
      if (this.handleAuthError(err) || this.destroyed) return;
      this.renderPush(this.pushStatus, `Could not change notifications: ${(err as Error).message}`);
    }
  }

  private async testPush(): Promise<void> {
    if (!this.push || !this.pushStatus) return;
    this.pushTestBtn.disabled = true;
    try {
      await this.push.test();
      this.renderPush(this.pushStatus, "Test sent. It should arrive within a few seconds.");
    } catch (err) {
      if (this.handleAuthError(err) || this.destroyed) return;
      this.renderPush(this.pushStatus, `Test failed: ${(err as Error).message}`);
    } finally {
      this.pushTestBtn.disabled = false;
    }
  }

  private async setPaused(paused: boolean): Promise<void> {
    if (!this.camera) return;
    this.pauseBtn.disabled = true;
    try {
      const c = paused ? await api.pause(this.camera.id) : await api.resume(this.camera.id);
      this.applyCamera(c);
      if (!paused) this.player?.reconnectNow();
    } catch (err) {
      if (!this.handleAuthError(err)) this.setPill(`error: ${(err as Error).message}`, "err");
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
