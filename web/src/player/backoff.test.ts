import { describe, expect, it } from "vitest";
import { Backoff } from "./backoff";

describe("Backoff", () => {
  it("grows exponentially, caps at max, and resets", () => {
    const b = new Backoff({ base: 100, max: 1000, jitter: 0, random: () => 0.5 });
    expect([b.next(), b.next(), b.next(), b.next(), b.next()]).toEqual([100, 200, 400, 800, 1000]);
    expect(b.attempts).toBe(5);
    b.reset();
    expect(b.next()).toBe(100);
  });

  it("applies symmetric jitter", () => {
    const low = new Backoff({ base: 1000, max: 1000, jitter: 0.2, random: () => 0 });
    const high = new Backoff({ base: 1000, max: 1000, jitter: 0.2, random: () => 1 });
    expect(low.next()).toBe(800);
    expect(high.next()).toBe(1200);
  });
});
