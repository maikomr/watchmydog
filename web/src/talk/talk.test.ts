import { describe, expect, it } from "vitest";
import { CHUNK_MS, describeTalkError, Talker, type MicLike, type RecorderLike, type TalkEnv, type TalkState } from "./talk";

class FakeRecorder implements RecorderLike {
  ondataavailable: ((ev: { data: Blob }) => void) | null = null;
  onstop: (() => void) | null = null;
  startedWith: number | null = null;
  stopped = false;
  start(ms: number): void {
    this.startedWith = ms;
  }
  /** Like the real one: a last chunk, then "stopped". */
  stop(): void {
    if (this.stopped) return;
    this.stopped = true;
    this.emit("last");
    this.onstop?.();
  }
  emit(text: string): void {
    this.ondataavailable?.({ data: new Blob([text]) });
  }
}

interface Deferred<T> {
  promise: Promise<T>;
  resolve: (v: T) => void;
  reject: (e: unknown) => void;
}

function deferred<T>(): Deferred<T> {
  let resolve!: (v: T) => void;
  let reject!: (e: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function setup(opts: { mic?: Promise<MicLike>; start?: Promise<string>; failChunk?: number } = {}) {
  const tracksStopped: number[] = [];
  const mic: MicLike = { getTracks: () => [{ stop: () => tracksStopped.push(1) }] };
  const recorders: FakeRecorder[] = [];
  const env: TalkEnv = {
    supported: true,
    getMicrophone: () => opts.mic ?? Promise.resolve(mic),
    createRecorder: () => {
      const r = new FakeRecorder();
      recorders.push(r);
      return r;
    },
  };
  const calls: string[] = [];
  const api = {
    talkStart: async (cameraId: string) => {
      calls.push(`start ${cameraId}`);
      return opts.start ?? "s1";
    },
    talkChunk: async (session: string, seq: number, data: Blob) => {
      const text = await data.text();
      if (seq === opts.failChunk) {
        calls.push(`chunk ${seq} FAILS`);
        throw new Error("talk session not found or already ended");
      }
      calls.push(`chunk ${session} ${seq} ${text}`);
    },
    talkStop: async (session: string) => {
      calls.push(`stop ${session}`);
    },
  };
  const states: [TalkState, string | undefined][] = [];
  const talker = new Talker(env, api, "dog", (s, note) => states.push([s, note]));
  return { talker, calls, states, recorders, tracksStopped, mic };
}

/** Lets pending promise callbacks run. */
const settle = () => new Promise((r) => setTimeout(r, 0));

describe("Talker", () => {
  it("records while pressed and sends the chunks in order", async () => {
    const t = setup();
    await t.talker.press();
    expect(t.talker.currentState).toBe("talking");
    expect(t.recorders[0].startedWith).toBe(CHUNK_MS);

    t.recorders[0].emit("a");
    t.recorders[0].emit("b");
    await t.talker.release();

    expect(t.calls).toEqual(["start dog", "chunk s1 0 a", "chunk s1 1 b", "chunk s1 2 last", "stop s1"]);
    expect(t.states.map((s) => s[0])).toEqual(["starting", "talking", "stopping", "idle"]);
    expect(t.tracksStopped.length).toBe(1); // microphone switched off
  });

  it("can talk again after a release", async () => {
    const t = setup();
    await t.talker.press();
    await t.talker.release();
    await t.talker.press();
    t.recorders[1].emit("x");
    await t.talker.release();
    expect(t.calls.filter((c) => c.startsWith("start"))).toHaveLength(2);
    expect(t.calls).toContain("chunk s1 0 x"); // numbering starts over
  });

  it("ignores empty chunks and a second press", async () => {
    const t = setup();
    await t.talker.press();
    await t.talker.press();
    t.recorders[0].ondataavailable?.({ data: new Blob([]) });
    await t.talker.release();
    expect(t.recorders).toHaveLength(1);
    expect(t.calls).toEqual(["start dog", "chunk s1 0 last", "stop s1"]);
  });

  it("does not talk when the button was released during the permission prompt", async () => {
    const mic = deferred<MicLike>();
    const t = setup({ mic: mic.promise });
    const pressing = t.talker.press();
    await t.talker.release(); // the user tapped "Allow", finger is up
    mic.resolve(t.mic);
    await pressing;

    expect(t.calls).toEqual([]); // the server was never asked
    expect(t.recorders).toHaveLength(0);
    expect(t.tracksStopped.length).toBe(1);
    expect(t.states.at(-1)).toEqual(["idle", "Microphone ready. Hold the button while you talk."]);
  });

  it("closes the session when the button was released while the server answered", async () => {
    const start = deferred<string>();
    const t = setup({ start: start.promise });
    const pressing = t.talker.press();
    await settle();
    await t.talker.release();
    start.resolve("s9");
    await pressing;

    expect(t.calls).toEqual(["start dog", "stop s9"]);
    expect(t.recorders).toHaveLength(0);
    expect(t.talker.currentState).toBe("idle");
  });

  it("explains a blocked microphone", async () => {
    const t = setup({ mic: Promise.reject(Object.assign(new Error("denied"), { name: "NotAllowedError" })) });
    await t.talker.press();
    expect(t.states.at(-1)).toEqual([
      "idle",
      "The microphone is blocked. Allow it for this app in the browser's site settings.",
    ]);
    expect(t.calls).toEqual([]);
  });

  it("reports a server that cannot open the loudspeaker", async () => {
    const t = setup({ start: Promise.reject(new Error("could not open the loudspeaker")) });
    await t.talker.press();
    expect(t.states.at(-1)).toEqual(["idle", "Could not talk: could not open the loudspeaker"]);
    expect(t.tracksStopped.length).toBe(1);
  });

  it("stops when a chunk cannot be sent, and sends nothing after it", async () => {
    const t = setup({ failChunk: 1 });
    await t.talker.press();
    t.recorders[0].emit("a");
    t.recorders[0].emit("b"); // fails
    t.recorders[0].emit("c");
    await settle();
    await settle();

    expect(t.talker.currentState).toBe("idle");
    expect(t.states.at(-1)?.[1]).toBe("Could not talk: talk session not found or already ended");
    expect(t.calls).toEqual(["start dog", "chunk s1 0 a", "chunk 1 FAILS", "stop s1"]);
    expect(t.recorders[0].stopped).toBe(true);
    expect(t.tracksStopped.length).toBe(1);

    // The finger comes up afterwards: nothing more happens.
    await t.talker.release();
    expect(t.calls.filter((c) => c.startsWith("stop"))).toHaveLength(1);
  });
});

describe("describeTalkError", () => {
  it("names the usual causes", () => {
    expect(describeTalkError(Object.assign(new Error("x"), { name: "NotFoundError" }))).toBe("This device has no microphone.");
    expect(describeTalkError(Object.assign(new Error("x"), { name: "SecurityError" }))).toContain("blocked");
    expect(describeTalkError(new Error("HTTP 503"))).toBe("Could not talk: HTTP 503");
    expect(describeTalkError("odd")).toBe("Could not talk: odd");
  });
});
