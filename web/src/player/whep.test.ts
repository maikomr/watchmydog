import { describe, expect, it } from "vitest";
import { WhepError, postOffer, whepUrl } from "./whep";

function response(status: number, body: string): Response {
  return new Response(body, { status, headers: { "Content-Type": "application/sdp" } });
}

describe("postOffer", () => {
  it("posts the offer as application/sdp and returns the answer", async () => {
    let seen: { url: string; init?: RequestInit } | null = null;
    const fetchFn = async (url: string, init?: RequestInit) => {
      seen = { url, init };
      return response(201, "v=0\r\nanswer");
    };
    const answer = await postOffer(fetchFn, "/media/api/webrtc?src=dog", "v=0\r\noffer");
    expect(answer).toBe("v=0\r\nanswer");
    expect(seen!.url).toBe("/media/api/webrtc?src=dog");
    expect(seen!.init?.method).toBe("POST");
    expect((seen!.init?.headers as Record<string, string>)["Content-Type"]).toBe("application/sdp");
    expect(seen!.init?.body).toBe("v=0\r\noffer");
  });

  it("surfaces HTTP errors with status and body", async () => {
    const fetchFn = async () => response(404, "stream not found");
    await expect(postOffer(fetchFn, "/x", "v=0")).rejects.toMatchObject({
      name: "WhepError",
      status: 404,
      message: "HTTP 404: stream not found",
    });
  });

  it("rejects non-SDP bodies and network failures", async () => {
    await expect(postOffer(async () => response(200, "<html>"), "/x", "v=0")).rejects.toBeInstanceOf(WhepError);
    await expect(
      postOffer(async () => {
        throw new Error("offline");
      }, "/x", "v=0"),
    ).rejects.toThrow("network error: offline");
  });

  it("builds the proxied endpoint URL", () => {
    expect(whepUrl("dog_low")).toBe("/media/api/webrtc?src=dog_low");
    expect(whepUrl("a b")).toBe("/media/api/webrtc?src=a%20b");
  });
});
