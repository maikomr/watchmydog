// Live sound updates from GET /api/events/stream (server-sent events).
//
// The browser's EventSource keeps the connection and reconnects after
// network drops by itself. This wrapper adds what it lacks: typed messages,
// a "no level for a while" signal (camera released, service restarting) and
// a retry when the server answers with an error, which EventSource treats
// as final.

import type { SoundEvent } from "../api";

export interface SoundHello {
  /** False when detection is switched off on the server. */
  enabled: boolean;
  thresholdDbfs: number;
  releaseDbfs: number;
  /** Events going on at the moment of connecting. */
  ongoing: SoundEvent[];
}

export interface SoundLevel {
  cameraId: string;
  /** Unix milliseconds. */
  t: number;
  dbfs: number;
  /** True while an event is open. */
  loud: boolean;
}

/** The part of EventSource we use, so tests can script one. */
export interface EventSourceLike {
  readyState: number;
  onerror: ((ev: Event) => unknown) | null;
  addEventListener(type: string, listener: (ev: { data: string }) => void): void;
  close(): void;
}

export interface SoundStreamOptions {
  cameraId: string;
  /** Called on every (re)connect: a good moment to reload the event list. */
  onHello: (hello: SoundHello) => void;
  /** A reading, or null when none arrived for `staleMs`. */
  onLevel: (level: SoundLevel | null) => void;
  /** An event started or ended. */
  onEvent: (ev: SoundEvent) => void;
  createSource?: (url: string) => EventSourceLike;
  staleMs?: number;
  retryMs?: number;
}

const CLOSED = 2; // EventSource.CLOSED

export class SoundStream {
  private source: EventSourceLike | null = null;
  private staleTimer: ReturnType<typeof setTimeout> | null = null;
  private retryTimer: ReturnType<typeof setTimeout> | null = null;
  private readonly staleMs: number;
  private readonly retryMs: number;

  constructor(private readonly opts: SoundStreamOptions) {
    this.staleMs = opts.staleMs ?? 2000;
    this.retryMs = opts.retryMs ?? 5000;
  }

  /** Opens the stream; a no-op while it is open. */
  start(): void {
    if (this.source) return;
    this.clearRetry();
    const url = `/api/events/stream?camera=${encodeURIComponent(this.opts.cameraId)}`;
    const source = (this.opts.createSource ?? ((u) => new EventSource(u)))(url);
    this.source = source;

    source.addEventListener("hello", (ev) => {
      const hello = parse<SoundHello>(ev.data);
      if (hello) this.opts.onHello(hello);
    });
    source.addEventListener("level", (ev) => {
      const level = parse<SoundLevel>(ev.data);
      if (!level) return;
      this.armStale();
      this.opts.onLevel(level);
    });
    source.addEventListener("sound", (ev) => {
      const event = parse<SoundEvent>(ev.data);
      if (event) this.opts.onEvent(event);
    });
    source.onerror = () => {
      // While EventSource is reconnecting by itself (readyState CONNECTING)
      // there is nothing to do. CLOSED means it gave up (HTTP error such as
      // an expired session or a restarting server): try again later.
      if (source.readyState !== CLOSED || this.source !== source) return;
      this.close();
      this.retryTimer = setTimeout(() => this.start(), this.retryMs);
    };
    // Until the first reading arrives the meter has nothing to show.
    this.armStale();
  }

  /** Closes the stream and stops all timers. start() opens it again. */
  stop(): void {
    this.close();
    this.clearRetry();
  }

  private close(): void {
    this.source?.close();
    this.source = null;
    if (this.staleTimer !== null) clearTimeout(this.staleTimer);
    this.staleTimer = null;
  }

  private clearRetry(): void {
    if (this.retryTimer !== null) clearTimeout(this.retryTimer);
    this.retryTimer = null;
  }

  private armStale(): void {
    if (this.staleTimer !== null) clearTimeout(this.staleTimer);
    this.staleTimer = setTimeout(() => this.opts.onLevel(null), this.staleMs);
  }
}

function parse<T>(data: string): T | null {
  try {
    return JSON.parse(data) as T;
  } catch {
    return null;
  }
}
