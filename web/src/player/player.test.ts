import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Backoff } from "./backoff";
import { LivePlayer, type PeerConnectionLike, type PlayerState } from "./player";

// A scriptable RTCPeerConnection stand-in.
class FakePC implements PeerConnectionLike {
  connectionState: RTCPeerConnectionState = "new";
  iceGatheringState: RTCIceGatheringState = "complete";
  localDescription: RTCSessionDescription | null = null;
  onconnectionstatechange: ((ev: Event) => void) | null = null;
  onicegatheringstatechange: ((ev: Event) => void) | null = null;
  ontrack: ((ev: RTCTrackEvent) => void) | null = null;
  closed = false;
  framesDecoded = 0;
  transceivers: string[] = [];
  remote: RTCSessionDescriptionInit | null = null;

  addTransceiver(kind: string): unknown {
    this.transceivers.push(kind);
    return {};
  }
  async createOffer(): Promise<RTCSessionDescriptionInit> {
    return { type: "offer", sdp: "v=0\r\noffer" };
  }
  async setLocalDescription(desc: RTCSessionDescriptionInit): Promise<void> {
    this.localDescription = { type: desc.type, sdp: desc.sdp } as RTCSessionDescription;
  }
  async setRemoteDescription(desc: RTCSessionDescriptionInit): Promise<void> {
    this.remote = desc;
  }
  async getStats(): Promise<RTCStatsReport> {
    const entries = [
      { type: "inbound-rtp", kind: "video", framesDecoded: this.framesDecoded, bytesReceived: this.framesDecoded * 1000 },
      { type: "inbound-rtp", kind: "audio", bytesReceived: 10 },
    ];
    return { forEach: (fn: (s: unknown) => void) => entries.forEach(fn) } as unknown as RTCStatsReport;
  }
  close(): void {
    this.closed = true;
  }
  // test helpers
  fireState(s: RTCPeerConnectionState): void {
    this.connectionState = s;
    this.onconnectionstatechange?.(new Event("connectionstatechange"));
  }
}

class FakeVideo {
  srcObject: MediaStream | null = null;
  plays = 0;
  async play(): Promise<void> {
    this.plays += 1;
  }
}

describe("LivePlayer", () => {
  let pcs: FakePC[];
  let fetchCalls: string[];
  let fetchImpl: (url: string) => Promise<Response>;
  let video: FakeVideo;
  let states: PlayerState[];
  let player: LivePlayer;

  beforeEach(() => {
    vi.useFakeTimers();
    pcs = [];
    fetchCalls = [];
    fetchImpl = async () => new Response("v=0\r\nanswer", { status: 201 });
    video = new FakeVideo();
    states = [];
    player = new LivePlayer({
      video,
      fetch: (url) => {
        fetchCalls.push(url);
        return fetchImpl(url);
      },
      createPeerConnection: () => {
        const pc = new FakePC();
        pcs.push(pc);
        return pc;
      },
      createMediaStream: () => ({ addTrack: () => undefined }) as unknown as MediaStream,
      now: () => Date.now(),
      statsIntervalMs: 100,
      stallTimeoutMs: 300,
      backoff: new Backoff({ base: 1000, max: 4000, jitter: 0 }),
    });
    player.on("state", (s) => states.push(s));
  });

  afterEach(() => {
    player.stop();
    vi.useRealTimers();
  });

  async function flush(): Promise<void> {
    // Let promise chains settle without advancing timers.
    for (let i = 0; i < 10; i++) await Promise.resolve();
  }

  it("negotiates via WHEP and goes live once frames arrive", async () => {
    player.play("dog");
    await flush();
    expect(fetchCalls).toEqual(["/media/api/webrtc?src=dog"]);
    expect(pcs[0].transceivers).toEqual(["video", "audio"]);
    expect(pcs[0].remote?.sdp).toBe("v=0\r\nanswer");
    expect(states).toEqual(["connecting"]);

    pcs[0].framesDecoded = 30;
    await vi.advanceTimersByTimeAsync(100);
    expect(states.at(-1)).toBe("live");
  });

  it("reconnects with backoff when signaling fails (camera paused), then recovers", async () => {
    fetchImpl = async () => new Response("stream not found", { status: 404 });
    player.play("dog");
    await flush();
    expect(states).toEqual(["connecting", "reconnecting"]);
    expect(pcs[0].closed).toBe(true);

    await vi.advanceTimersByTimeAsync(1000); // first backoff
    await flush();
    expect(fetchCalls.length).toBe(2);
    expect(states.at(-1)).toBe("reconnecting");

    await vi.advanceTimersByTimeAsync(2000); // second backoff doubled
    await flush();
    expect(fetchCalls.length).toBe(3);

    // Camera resumed: signaling succeeds again.
    fetchImpl = async () => new Response("v=0\r\nanswer", { status: 201 });
    await vi.advanceTimersByTimeAsync(4000);
    await flush();
    expect(fetchCalls.length).toBe(4);
    pcs.at(-1)!.framesDecoded = 5;
    await vi.advanceTimersByTimeAsync(100);
    expect(states.at(-1)).toBe("live");
  });

  it("reconnects when frames stop arriving (engine restarted under us)", async () => {
    player.play("dog");
    await flush();
    pcs[0].framesDecoded = 10;
    await vi.advanceTimersByTimeAsync(100);
    expect(states.at(-1)).toBe("live");

    // No new frames for longer than stallTimeoutMs.
    await vi.advanceTimersByTimeAsync(400);
    expect(states.at(-1)).toBe("reconnecting");
    expect(pcs[0].closed).toBe(true);
    expect(video.srcObject).toBeNull();
  });

  it("reconnects on ICE failure and reconnectNow skips the backoff", async () => {
    player.play("dog");
    await flush();
    pcs[0].fireState("failed");
    expect(states.at(-1)).toBe("reconnecting");

    player.reconnectNow();
    await vi.advanceTimersByTimeAsync(0);
    await flush();
    expect(fetchCalls.length).toBe(2);
    expect(pcs.length).toBe(2);
  });

  it("play() with another stream tears down the old connection", async () => {
    player.play("dog");
    await flush();
    player.play("dog_low");
    await flush();
    expect(pcs[0].closed).toBe(true);
    expect(fetchCalls).toEqual(["/media/api/webrtc?src=dog", "/media/api/webrtc?src=dog_low"]);
    expect(player.currentStream).toBe("dog_low");
  });

  it("stop() ends everything and ignores late events", async () => {
    player.play("dog");
    await flush();
    player.stop();
    expect(states.at(-1)).toBe("stopped");
    pcs[0].fireState("failed");
    await vi.advanceTimersByTimeAsync(5000);
    expect(fetchCalls.length).toBe(1);
  });
});
