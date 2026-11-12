import { describe, expect, it } from "vitest";
import { notificationFor, pickWindow, safeAppPath } from "./notify";

describe("notificationFor", () => {
  it("builds a notification from the server's payload", () => {
    const spec = notificationFor(
      JSON.stringify({ title: "Noise: Living room", body: "Tap to watch live.", url: "/?camera=dog", tag: "sound-dog", ts: 1731421920000 }),
    );
    expect(spec).toEqual({
      title: "Noise: Living room",
      options: {
        body: "Tap to watch live.",
        tag: "sound-dog",
        renotify: true,
        icon: "/icons/icon-192.png",
        badge: "/icons/icon-192.png",
        timestamp: 1731421920000,
        data: { url: "/?camera=dog" },
      },
    });
  });

  it("still shows something for an empty or unreadable message", () => {
    for (const text of [null, "", "not json", "42", "null", '{"title":7}']) {
      const spec = notificationFor(text);
      expect(spec.title).toBe("watchmydog");
      expect(spec.options.data.url).toBe("/");
      expect(spec.options.tag).toBeUndefined();
      expect(spec.options.renotify).toBeUndefined();
    }
  });
});

describe("safeAppPath", () => {
  it("keeps paths inside the app", () => {
    expect(safeAppPath("/")).toBe("/");
    expect(safeAppPath("/?camera=dog")).toBe("/?camera=dog");
  });
  it("replaces anything that could leave the app", () => {
    for (const bad of ["https://evil.example/", "//evil.example/", "/\\evil.example", "javascript:alert(1)", "", undefined, 5]) {
      expect(safeAppPath(bad)).toBe("/");
    }
  });
});

describe("pickWindow", () => {
  const origin = "https://mac.example.ts.net";
  it("finds a window of the app", () => {
    expect(pickWindow([{ url: "https://other.example/" }, { url: `${origin}/?camera=dog` }], origin)).toBe(1);
  });
  it("returns -1 when there is none", () => {
    expect(pickWindow([], origin)).toBe(-1);
    expect(pickWindow([{ url: "https://other.example/" }, { url: "garbage" }], origin)).toBe(-1);
  });
});
