import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { SoundEvent } from "../api";
import { SoundStream, type EventSourceLike, type SoundHello, type SoundLevel } from "./stream";

class FakeSource implements EventSourceLike {
  readyState = 0;
  onerror: ((ev: Event) => unknown) | null = null;
  closed = false;
  private listeners = new Map<string, (ev: { data: string }) => void>();
  constructor(readonly url: string) {}
  addEventListener(type: string, listener: (ev: { data: string }) => void): void {
    this.listeners.set(type, listener);
  }
  close(): void {
    this.closed = true;
  }
  emit(type: string, data: unknown): void {
    this.listeners.get(type)?.({ data: typeof data === "string" ? data : JSON.stringify(data) });
  }
  fail(readyState: number): void {
    this.readyState = readyState;
    this.onerror?.({} as Event);
  }
}

function setup() {
  const sources: FakeSource[] = [];
  const hellos: SoundHello[] = [];
  const levels: (SoundLevel | null)[] = [];
  const events: SoundEvent[] = [];
  const stream = new SoundStream({
    cameraId: "dog cam",
    onHello: (h) => hellos.push(h),
    onLevel: (l) => levels.push(l),
    onEvent: (e) => events.push(e),
    createSource: (url) => {
      const s = new FakeSource(url);
      sources.push(s);
      return s;
    },
    staleMs: 2000,
    retryMs: 5000,
  });
  return { stream, sources, hellos, levels, events };
}

const level = (dbfs: number): SoundLevel => ({ cameraId: "dog", t: 1, dbfs, loud: false });

describe("SoundStream", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("opens the stream for the camera and delivers typed messages", () => {
    const { stream, sources, hellos, levels, events } = setup();
    stream.start();
    stream.start(); // no second connection
    expect(sources).toHaveLength(1);
    expect(sources[0].url).toBe("/api/events/stream?camera=dog%20cam");

    sources[0].emit("hello", { enabled: true, thresholdDbfs: -30, releaseDbfs: -36, ongoing: [] });
    sources[0].emit("level", level(-48));
    sources[0].emit("sound", { id: "e1", cameraId: "dog", kind: "sound", startedAt: "x", endedAt: null, peakDbfs: -9 });
    sources[0].emit("level", "not json");

    expect(hellos[0].thresholdDbfs).toBe(-30);
    expect(levels).toEqual([level(-48)]);
    expect(events[0].id).toBe("e1");
  });

  it("reports null when levels stop, and again only after new ones", () => {
    const { stream, sources, levels } = setup();
    stream.start();
    sources[0].emit("level", level(-50));
    vi.advanceTimersByTime(1500);
    sources[0].emit("level", level(-49));
    vi.advanceTimersByTime(1500);
    expect(levels).toHaveLength(2); // timer was re-armed by the second level
    vi.advanceTimersByTime(600);
    expect(levels[2]).toBeNull();
    vi.advanceTimersByTime(10_000);
    expect(levels).toHaveLength(3);
  });

  it("reports null if no level ever arrives", () => {
    const { stream, levels } = setup();
    stream.start();
    vi.advanceTimersByTime(2000);
    expect(levels).toEqual([null]);
  });

  it("leaves reconnecting to EventSource but retries after a final error", () => {
    const { stream, sources } = setup();
    stream.start();
    sources[0].fail(0); // CONNECTING: the browser is retrying on its own
    vi.advanceTimersByTime(10_000);
    expect(sources).toHaveLength(1);

    sources[0].fail(2); // CLOSED: e.g. HTTP 503 while the service restarts
    expect(sources[0].closed).toBe(true);
    vi.advanceTimersByTime(4999);
    expect(sources).toHaveLength(1);
    vi.advanceTimersByTime(1);
    expect(sources).toHaveLength(2);
  });

  it("stop closes the connection and cancels timers; start reopens", () => {
    const { stream, sources, levels } = setup();
    stream.start();
    sources[0].fail(2);
    stream.stop();
    vi.advanceTimersByTime(60_000);
    expect(sources).toHaveLength(1);
    expect(levels).toEqual([]);

    stream.start();
    expect(sources).toHaveLength(2);
    stream.stop();
    expect(sources[1].closed).toBe(true);
  });
});
