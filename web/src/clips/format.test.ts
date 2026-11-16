import { describe, expect, it } from "vitest";
import type { Clip, ClipList } from "../api";
import { describeClip, describeStorage, formatBytes, sameClips } from "./format";

function clip(id: string, over: Partial<Clip> = {}): Clip {
  return {
    id,
    cameraId: "dog",
    startedAt: "2026-11-16T14:30:00Z",
    durationSeconds: 42.3,
    sizeBytes: 12_400_000,
    videoUrl: `/api/clips/${id}/video.mp4`,
    ...over,
  };
}

function list(over: Partial<ClipList> = {}): ClipList {
  return { enabled: true, clips: [clip("a")], usedBytes: 1.24e9, maxBytes: 10e9, keepSeconds: 30 * 86400, ...over };
}

describe("formatBytes", () => {
  it("picks a unit that keeps the number short", () => {
    expect(formatBytes(0)).toBe("0 MB");
    expect(formatBytes(-5)).toBe("0 MB");
    expect(formatBytes(NaN)).toBe("0 MB");
    expect(formatBytes(300)).toBe("1 kB");
    expect(formatBytes(850_000)).toBe("850 kB");
    expect(formatBytes(12_400_000)).toBe("12 MB");
    expect(formatBytes(999_400_000)).toBe("999 MB");
    expect(formatBytes(1.44e9)).toBe("1.4 GB");
  });
});

describe("describeClip", () => {
  it("shows length and size", () => {
    expect(describeClip(clip("a"))).toBe("42 s · 12 MB");
    expect(describeClip(clip("a", { durationSeconds: 300, sizeBytes: 95e6 }))).toBe("5 min 00 s · 95 MB");
  });
});

describe("describeStorage", () => {
  it("explains space and age limits", () => {
    expect(describeStorage(list())).toBe(
      "1.2 GB of 10.0 GB used. Clips are deleted after 30 days, or earlier when the space runs out.",
    );
    expect(describeStorage(list({ keepSeconds: 36 * 3600 }))).toContain("after 36 hours,");
    expect(describeStorage(list({ keepSeconds: 60 }))).toContain("after 1 minute,");
  });

  it("handles clips kept until the space runs out", () => {
    expect(describeStorage(list({ keepSeconds: 0 }))).toBe(
      "1.2 GB of 10.0 GB used. The oldest clips are deleted when the space runs out.",
    );
  });

  it("explains an empty list", () => {
    expect(describeStorage(list({ clips: [] }))).toBe(
      "No clips yet. A noise is recorded with a few seconds before and after it.",
    );
  });

  it("says when recording is off, with or without old clips", () => {
    expect(describeStorage(list({ enabled: false, clips: [] }))).toBe("Recording is switched off.");
    expect(describeStorage(list({ enabled: false }))).toMatch(/^Recording is switched off\. 1\.2 GB of/);
  });
});

describe("sameClips", () => {
  it("compares ids in order", () => {
    expect(sameClips([], [])).toBe(true);
    expect(sameClips([clip("a"), clip("b")], [clip("a"), clip("b")])).toBe(true);
    expect(sameClips([clip("a"), clip("b")], [clip("b"), clip("a")])).toBe(false);
    expect(sameClips([clip("a")], [clip("a"), clip("b")])).toBe(false);
  });
});
