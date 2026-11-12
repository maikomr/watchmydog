import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, api, isUnauthorized } from "./api";

type Call = { url: string; init?: RequestInit };

function stubFetch(status: number, body?: unknown): Call[] {
  const calls: Call[] = [];
  vi.stubGlobal("fetch", async (url: string, init?: RequestInit) => {
    calls.push({ url, init });
    const text = body === undefined ? null : JSON.stringify(body);
    return new Response(text, { status, headers: { "Content-Type": "application/json" } });
  });
  return calls;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("api", () => {
  it("maps 401 to an ApiError recognised by isUnauthorized", async () => {
    stubFetch(401, { error: "unauthorized" });
    const err = await api.cameras().catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).status).toBe(401);
    expect((err as ApiError).message).toBe("unauthorized");
    expect(isUnauthorized(err)).toBe(true);
    expect(isUnauthorized(new Error("x"))).toBe(false);
    expect(isUnauthorized(new ApiError("x", 500))).toBe(false);
  });

  it("carries retryAfterSeconds on 429", async () => {
    stubFetch(429, { error: "too many failed attempts", retryAfterSeconds: 840 });
    const err = (await api.login("x").catch((e: unknown) => e)) as ApiError;
    expect(err.status).toBe(429);
    expect(err.retryAfterSeconds).toBe(840);
  });

  it("falls back to the HTTP status when the error body is not JSON", async () => {
    vi.stubGlobal("fetch", async () => new Response("<html>", { status: 502 }));
    await expect(api.health()).rejects.toMatchObject({ status: 502, message: "HTTP 502" });
  });

  it("posts the login secret as JSON", async () => {
    const calls = stubFetch(200, { authenticated: true, authRequired: true, subject: "owner" });
    const s = await api.login("my-secret");
    expect(s.subject).toBe("owner");
    expect(calls[0].url).toBe("/api/login");
    expect(calls[0].init?.method).toBe("POST");
    expect((calls[0].init?.headers as Record<string, string>)["Content-Type"]).toBe("application/json");
    expect(JSON.parse(calls[0].init?.body as string)).toEqual({ secret: "my-secret" });
  });

  it("logout accepts an empty 204 response", async () => {
    const calls = stubFetch(204);
    await expect(api.logout()).resolves.toBeUndefined();
    expect(calls[0]).toMatchObject({ url: "/api/logout", init: { method: "POST" } });
  });

  it("lists events for a camera", async () => {
    const event = { id: "e1", cameraId: "dog", kind: "sound", startedAt: "2026-11-12T10:00:00Z", endedAt: null, peakDbfs: -9 };
    const calls = stubFetch(200, { events: [event] });
    await expect(api.events("a b", 20)).resolves.toEqual([event]);
    expect(calls[0].url).toBe("/api/events?camera=a%20b&limit=20");
  });

  it("escapes camera IDs in URLs", async () => {
    const calls = stubFetch(200, { id: "a b" });
    await api.pause("a b");
    expect(calls[0].url).toBe("/api/cameras/a%20b/pause");
  });

  it("subscribes, tests and unsubscribes notifications with 204 answers", async () => {
    const calls = stubFetch(204);
    const sub = { endpoint: "https://push.example/x", keys: { p256dh: "p", auth: "a" } };
    await expect(api.pushSubscribe(sub)).resolves.toBeUndefined();
    await expect(api.pushTest(sub.endpoint)).resolves.toBeUndefined();
    await expect(api.pushUnsubscribe(sub.endpoint)).resolves.toBeUndefined();
    expect(calls.map((c) => [c.url, c.init?.method])).toEqual([
      ["/api/push/subscribe", "POST"],
      ["/api/push/test", "POST"],
      ["/api/push/unsubscribe", "POST"],
    ]);
    expect(JSON.parse(calls[0].init?.body as string)).toEqual(sub);
    expect(JSON.parse(calls[1].init?.body as string)).toEqual({ endpoint: sub.endpoint });
  });

  it("passes on the server's reason when a test notification fails", async () => {
    stubFetch(502, { error: "webpush: push service answered 403 Forbidden" });
    await expect(api.pushTest("https://push.example/x")).rejects.toMatchObject({ status: 502, message: /403/ });
  });
});
