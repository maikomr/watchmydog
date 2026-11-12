import { describe, expect, it } from "vitest";
import type { SoundEvent } from "../api";
import { describeEvent, formatDuration, formatWhen, levelFraction, mergeEvent } from "./format";

function ev(id: string, startedAt: string, endedAt: string | null = null, peakDbfs = -10): SoundEvent {
  return { id, cameraId: "dog", kind: "sound", startedAt, endedAt, peakDbfs };
}

describe("levelFraction", () => {
  it("maps floor..0 dBFS to 0..1 and clamps", () => {
    expect(levelFraction(-70)).toBe(0);
    expect(levelFraction(-35)).toBe(0.5);
    expect(levelFraction(0)).toBe(1);
    expect(levelFraction(-100)).toBe(0);
    expect(levelFraction(5)).toBe(1);
    expect(levelFraction(Number.NaN)).toBe(0);
  });
});

describe("mergeEvent", () => {
  const a = ev("a", "2026-11-12T10:00:00Z", "2026-11-12T10:00:05Z");
  const b = ev("b", "2026-11-12T11:00:00Z");

  it("adds new events newest first without touching the input", () => {
    const list = [a];
    const out = mergeEvent(list, b, 10);
    expect(out.map((e) => e.id)).toEqual(["b", "a"]);
    expect(list).toEqual([a]);
  });

  it("replaces an event with the same id (start, then end)", () => {
    const ended = { ...b, endedAt: "2026-11-12T11:00:09Z" };
    const out = mergeEvent([b, a], ended, 10);
    expect(out).toHaveLength(2);
    expect(out[0].endedAt).toBe("2026-11-12T11:00:09Z");
  });

  it("keeps at most max entries, dropping the oldest", () => {
    expect(mergeEvent([b, a], ev("c", "2026-11-12T12:00:00Z"), 2).map((e) => e.id)).toEqual(["c", "b"]);
  });
});

describe("formatDuration", () => {
  it("picks a readable unit", () => {
    expect(formatDuration(400)).toBe("0.4 s");
    expect(formatDuration(-5)).toBe("0.0 s");
    expect(formatDuration(1000)).toBe("1 s");
    expect(formatDuration(12_400)).toBe("12 s");
    expect(formatDuration(185_000)).toBe("3 min 05 s");
    expect(formatDuration(3_725_000)).toBe("1 h 02 min");
  });
});

describe("formatWhen", () => {
  const now = new Date("2026-11-12T15:00:00Z");
  it("shows only the time for today", () => {
    expect(formatWhen(new Date("2026-11-12T09:03:07Z"), now, "en-GB", "UTC")).toBe("09:03:07");
  });
  it("adds the date for other days", () => {
    expect(formatWhen(new Date("2026-11-11T22:15:00Z"), now, "en-GB", "UTC")).toBe("11 Nov, 22:15:00");
  });
  it("decides 'today' in the given time zone", () => {
    // 01:30 UTC on the 12th is still the 11th in New York.
    const out = formatWhen(new Date("2026-11-12T01:30:00Z"), now, "en-GB", "America/New_York");
    expect(out).toBe("11 Nov, 20:30:00");
  });
});

describe("describeEvent", () => {
  it("describes finished, ongoing and cut-short events", () => {
    expect(describeEvent(ev("a", "2026-11-12T10:00:00Z", "2026-11-12T10:00:12Z", -8.4))).toBe("12 s · peak -8 dB");
    expect(describeEvent(ev("a", "2026-11-12T10:00:00Z"))).toBe("happening now");
    expect(describeEvent(ev("a", "2026-11-12T10:00:00Z", "2026-11-12T10:00:00Z", -20))).toBe("length unknown · peak -20 dB");
  });
});
