// LivePlayer: keeps one WebRTC stream playing in a <video> element and
// recovers from every failure mode we have seen: signaling errors (stream
// missing while the camera is paused), ICE failure, the engine restarting
// under us (connection stays "connected" for a while but frames stop), and
// the phone sleeping. All platform objects are injectable for tests.

import { Backoff } from "./backoff";
import { postOffer, whepUrl, type FetchLike } from "./whep";

export type PlayerState = "idle" | "connecting" | "live" | "reconnecting" | "stopped";

export interface PlayerEvents {
  state: (state: PlayerState, detail?: string) => void;
  stats: (s: PlayerStats) => void;
}

export interface PlayerStats {
  framesDecoded: number;
  bytesReceived: number;
  /** Frames per second over the last stats interval. */
  fps: number;
  /** Kilobits per second over the last stats interval. */
  kbps: number;
}

/** Minimal surface of RTCPeerConnection the player uses (mockable). */
export interface PeerConnectionLike {
  connectionState: RTCPeerConnectionState;
  iceGatheringState: RTCIceGatheringState;
  localDescription: RTCSessionDescription | null;
  onconnectionstatechange: ((ev: Event) => void) | null;
  onicegatheringstatechange: ((ev: Event) => void) | null;
  ontrack: ((ev: RTCTrackEvent) => void) | null;
  addTransceiver(kind: string, init?: RTCRtpTransceiverInit): unknown;
  createOffer(): Promise<RTCSessionDescriptionInit>;
  setLocalDescription(desc: RTCSessionDescriptionInit): Promise<void>;
  setRemoteDescription(desc: RTCSessionDescriptionInit): Promise<void>;
  getStats(): Promise<RTCStatsReport>;
  close(): void;
}

/** Minimal surface of the <video> element the player uses (mockable). */
export interface VideoLike {
  srcObject: MediaProvider | null;
  play(): Promise<void>;
}

export interface PlayerDeps {
  video: VideoLike;
  fetch?: FetchLike;
  createPeerConnection?: () => PeerConnectionLike;
  createMediaStream?: () => MediaStream;
  /** Timer functions, injectable for tests. */
  setTimeout?: typeof globalThis.setTimeout;
  clearTimeout?: typeof globalThis.clearTimeout;
  setInterval?: typeof globalThis.setInterval;
  clearInterval?: typeof globalThis.clearInterval;
  now?: () => number;
  /** Max wait for ICE gathering before sending the offer anyway (ms). */
  iceGatherTimeoutMs?: number;
  /** Stats polling period (ms). */
  statsIntervalMs?: number;
  /** Reconnect when no new frame arrived for this long while live (ms). */
  stallTimeoutMs?: number;
  backoff?: Backoff;
}

export class LivePlayer {
  private stream = "";
  private pc: PeerConnectionLike | null = null;
  private state: PlayerState = "idle";
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private statsTimer: ReturnType<typeof setInterval> | null = null;
  private abort: AbortController | null = null;
  private lastFrames = 0;
  private lastBytes = 0;
  private lastFrameAt = 0;
  private lastStatsAt = 0;
  private generation = 0;
  private listeners: { [K in keyof PlayerEvents]: PlayerEvents[K][] } = { state: [], stats: [] };

  private readonly fetchFn: FetchLike;
  private readonly createPC: () => PeerConnectionLike;
  private readonly createStream: () => MediaStream;
  private readonly setTimeoutFn: typeof globalThis.setTimeout;
  private readonly clearTimeoutFn: typeof globalThis.clearTimeout;
  private readonly setIntervalFn: typeof globalThis.setInterval;
  private readonly clearIntervalFn: typeof globalThis.clearInterval;
  private readonly now: () => number;
  private readonly iceGatherTimeoutMs: number;
  private readonly statsIntervalMs: number;
  private readonly stallTimeoutMs: number;
  private readonly backoff: Backoff;

  constructor(private readonly deps: PlayerDeps) {
    this.fetchFn = deps.fetch ?? ((input, init) => fetch(input, init));
    this.createPC = deps.createPeerConnection ?? (() => new RTCPeerConnection() as unknown as PeerConnectionLike);
    this.createStream = deps.createMediaStream ?? (() => new MediaStream());
    this.setTimeoutFn = deps.setTimeout ?? globalThis.setTimeout.bind(globalThis);
    this.clearTimeoutFn = deps.clearTimeout ?? globalThis.clearTimeout.bind(globalThis);
    this.setIntervalFn = deps.setInterval ?? globalThis.setInterval.bind(globalThis);
    this.clearIntervalFn = deps.clearInterval ?? globalThis.clearInterval.bind(globalThis);
    this.now = deps.now ?? Date.now;
    this.iceGatherTimeoutMs = deps.iceGatherTimeoutMs ?? 1000;
    this.statsIntervalMs = deps.statsIntervalMs ?? 2000;
    this.stallTimeoutMs = deps.stallTimeoutMs ?? 6000;
    this.backoff = deps.backoff ?? new Backoff({ base: 1000, max: 15000 });
  }

  on<K extends keyof PlayerEvents>(event: K, fn: PlayerEvents[K]): () => void {
    (this.listeners[event] as PlayerEvents[K][]).push(fn);
    return () => {
      this.listeners[event] = (this.listeners[event] as PlayerEvents[K][]).filter((f) => f !== fn) as never;
    };
  }

  get currentState(): PlayerState {
    return this.state;
  }

  get currentStream(): string {
    return this.stream;
  }

  /** Starts (or switches to) a stream. Safe to call while playing. */
  play(stream: string): void {
    this.stream = stream;
    this.backoff.reset();
    this.teardown();
    this.setState("connecting");
    void this.connect();
  }

  /** Stops playback and all timers. */
  stop(): void {
    this.teardown();
    this.setState("stopped");
  }

  /** Forces a reconnect now (e.g. when the page becomes visible again). */
  reconnectNow(): void {
    if (this.state === "stopped" || this.state === "idle") return;
    this.backoff.reset();
    this.scheduleReconnect(0, "resumed");
  }

  private setState(state: PlayerState, detail?: string): void {
    this.state = state;
    for (const fn of this.listeners.state) fn(state, detail);
  }

  private teardown(): void {
    this.generation += 1;
    if (this.reconnectTimer !== null) {
      this.clearTimeoutFn(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    if (this.statsTimer !== null) {
      this.clearIntervalFn(this.statsTimer);
      this.statsTimer = null;
    }
    this.abort?.abort();
    this.abort = null;
    if (this.pc) {
      this.pc.onconnectionstatechange = null;
      this.pc.ontrack = null;
      this.pc.close();
      this.pc = null;
    }
    this.deps.video.srcObject = null;
  }

  private async connect(): Promise<void> {
    const gen = this.generation;
    const pc = this.createPC();
    this.pc = pc;
    this.abort = new AbortController();
    const signal = this.abort.signal;

    const remote = this.createStream();
    pc.ontrack = (ev) => {
      if (gen !== this.generation) return;
      remote.addTrack(ev.track);
      if (this.deps.video.srcObject !== remote) {
        this.deps.video.srcObject = remote;
        void this.deps.video.play().catch(() => {
          /* autoplay policy: UI handles unmute/tap */
        });
      }
    };
    pc.onconnectionstatechange = () => {
      if (gen !== this.generation) return;
      const cs = pc.connectionState;
      if (cs === "failed" || cs === "closed" || cs === "disconnected") {
        this.scheduleReconnect(this.backoff.next(), `connection ${cs}`);
      }
    };

    try {
      pc.addTransceiver("video", { direction: "recvonly" });
      pc.addTransceiver("audio", { direction: "recvonly" });
      const offer = await pc.createOffer();
      await pc.setLocalDescription(offer);
      await this.waitForIce(pc);
      if (gen !== this.generation) return;
      const sdp = pc.localDescription?.sdp ?? offer.sdp ?? "";
      const answer = await postOffer(this.fetchFn, whepUrl(this.stream), sdp, signal);
      if (gen !== this.generation) return;
      await pc.setRemoteDescription({ type: "answer", sdp: answer });
      if (gen !== this.generation) return;
      this.startStats(gen);
    } catch (err) {
      if (gen !== this.generation) return;
      this.scheduleReconnect(this.backoff.next(), (err as Error).message);
    }
  }

  /** Non-trickle ICE: wait (bounded) for candidates so the offer carries them. */
  private waitForIce(pc: PeerConnectionLike): Promise<void> {
    if (pc.iceGatheringState === "complete") return Promise.resolve();
    return new Promise((resolve) => {
      const timer = this.setTimeoutFn(() => {
        pc.onicegatheringstatechange = null;
        resolve();
      }, this.iceGatherTimeoutMs);
      pc.onicegatheringstatechange = () => {
        if (pc.iceGatheringState === "complete") {
          this.clearTimeoutFn(timer);
          pc.onicegatheringstatechange = null;
          resolve();
        }
      };
    });
  }

  private startStats(gen: number): void {
    this.lastFrames = 0;
    this.lastBytes = 0;
    this.lastFrameAt = this.now();
    this.lastStatsAt = this.now();
    this.statsTimer = this.setIntervalFn(() => void this.pollStats(gen), this.statsIntervalMs);
  }

  private async pollStats(gen: number): Promise<void> {
    if (gen !== this.generation || !this.pc) return;
    let frames = 0;
    let bytes = 0;
    try {
      const report = await this.pc.getStats();
      report.forEach((s: RTCStats & Record<string, unknown>) => {
        if (s.type === "inbound-rtp" && s.kind === "video") {
          frames = (s.framesDecoded as number) ?? 0;
          bytes += (s.bytesReceived as number) ?? 0;
        } else if (s.type === "inbound-rtp" && s.kind === "audio") {
          bytes += (s.bytesReceived as number) ?? 0;
        }
      });
    } catch {
      return;
    }
    if (gen !== this.generation) return;
    const now = this.now();
    const dt = Math.max(1, now - this.lastStatsAt) / 1000;
    const fps = (frames - this.lastFrames) / dt;
    const kbps = ((bytes - this.lastBytes) * 8) / dt / 1000;
    if (frames > this.lastFrames) {
      this.lastFrameAt = now;
      if (this.state !== "live") {
        this.backoff.reset();
        this.setState("live");
      }
    } else if (now - this.lastFrameAt > this.stallTimeoutMs) {
      this.scheduleReconnect(this.backoff.next(), "no frames received");
      return;
    }
    this.lastFrames = frames;
    this.lastBytes = bytes;
    this.lastStatsAt = now;
    for (const fn of this.listeners.stats) fn({ framesDecoded: frames, bytesReceived: bytes, fps, kbps });
  }

  private scheduleReconnect(delayMs: number, reason: string): void {
    if (this.state === "stopped") return;
    this.teardown();
    this.setState("reconnecting", `${reason}; retry in ${Math.round(delayMs / 1000)}s`);
    this.reconnectTimer = this.setTimeoutFn(() => {
      this.reconnectTimer = null;
      this.setState("connecting");
      void this.connect();
    }, delayMs);
  }
}
