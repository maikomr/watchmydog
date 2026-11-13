import { describe, expect, it } from "vitest";
import { audioHealth, describeAudioHealth, readAudioCounters, type AudioCounters } from "./audio-health";

const c = (over: Partial<AudioCounters> = {}): AudioCounters => ({
  packetsReceived: 0,
  packetsLost: 0,
  totalSamplesReceived: 0,
  concealedSamples: 0,
  silentConcealedSamples: 0,
  ...over,
});

describe("audioHealth", () => {
  it("is clean when nothing was concealed or lost", () => {
    const h = audioHealth(c(), c({ packetsReceived: 100, totalSamplesReceived: 96000 }));
    expect(h).toEqual({ gapPct: 0, lostPct: 0 });
  });

  it("reports the change between two readings, not the totals", () => {
    const prev = c({ packetsReceived: 1000, packetsLost: 50, totalSamplesReceived: 960000, concealedSamples: 48000 });
    const cur = c({ packetsReceived: 1098, packetsLost: 52, totalSamplesReceived: 1056000, concealedSamples: 48960 });
    const h = audioHealth(prev, cur)!;
    expect(h.gapPct).toBeCloseTo(1, 5); // 960 of 96000 samples
    expect(h.lostPct).toBeCloseTo(2, 5); // 2 of 100 packets
  });

  it("ignores concealment during silence", () => {
    const cur = c({ packetsReceived: 100, totalSamplesReceived: 96000, concealedSamples: 9600, silentConcealedSamples: 9600 });
    expect(audioHealth(c(), cur)!.gapPct).toBe(0);
  });

  it("returns null when no audio was played, and never leaves 0-100", () => {
    expect(audioHealth(c(), c())).toBeNull();
    const reset = audioHealth(c({ concealedSamples: 500 }), c({ totalSamplesReceived: 100 }))!;
    expect(reset.gapPct).toBe(0);
  });
});

describe("readAudioCounters", () => {
  it("treats missing fields as zero", () => {
    expect(readAudioCounters({ packetsReceived: 5, concealedSamples: "x" })).toEqual(c({ packetsReceived: 5 }));
  });
});

describe("describeAudioHealth", () => {
  it("says nothing when the audio is fine or unknown", () => {
    expect(describeAudioHealth(null)).toBe("");
    expect(describeAudioHealth({ gapPct: 0.01, lostPct: 0 })).toBe("");
  });
  it("shows gaps, and loss when there is some", () => {
    expect(describeAudioHealth({ gapPct: 1.23, lostPct: 0 })).toBe("audio gaps 1.2%");
    expect(describeAudioHealth({ gapPct: 1.23, lostPct: 0.84 })).toBe("audio gaps 1.2% (lost 0.8%)");
  });
});
