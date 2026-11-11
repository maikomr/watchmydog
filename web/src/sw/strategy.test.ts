import { describe, expect, it } from "vitest";
import { strategyFor } from "./strategy";

const origin = "https://maikos-mac-mini.tail54b4a7.ts.net";
const req = (path: string, mode = "cors", method = "GET") => ({ method, mode, url: origin + path });

describe("strategyFor", () => {
  it("never caches API or media, even for GETs", () => {
    expect(strategyFor(req("/api/cameras"), origin)).toBe("network-only");
    expect(strategyFor(req("/api/session"), origin)).toBe("network-only");
    expect(strategyFor(req("/media/api/stream.mp4?src=dog"), origin)).toBe("network-only");
    expect(strategyFor(req("/api/cameras", "navigate"), origin)).toBe("network-only");
  });

  it("leaves non-GET and cross-origin requests alone", () => {
    expect(strategyFor(req("/api/login", "cors", "POST"), origin)).toBe("passthrough");
    expect(strategyFor(req("/", "navigate", "POST"), origin)).toBe("passthrough");
    expect(strategyFor({ method: "GET", mode: "cors", url: "https://example.com/x.js" }, origin)).toBe("passthrough");
  });

  it("uses network-first for pages and cache-first for hashed assets", () => {
    expect(strategyFor(req("/", "navigate"), origin)).toBe("network-first");
    expect(strategyFor(req("/clips", "navigate"), origin)).toBe("network-first");
    expect(strategyFor(req("/assets/index-CHIigXAt.js"), origin)).toBe("cache-first");
    expect(strategyFor(req("/icons/icon-192.png"), origin)).toBe("stale-while-revalidate");
    expect(strategyFor(req("/manifest.webmanifest"), origin)).toBe("stale-while-revalidate");
  });
});
