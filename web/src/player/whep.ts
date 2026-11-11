// WHEP (WebRTC-HTTP Egress Protocol) signaling: POST an SDP offer, get an
// SDP answer. go2rtc serves it at /api/webrtc?src=<stream>; a cloud SFU
// would expose the same contract, which is why the player speaks WHEP and
// not go2rtc's own WebSocket protocol.

export class WhepError extends Error {
  constructor(
    message: string,
    public readonly status?: number,
  ) {
    super(message);
    this.name = "WhepError";
  }
}

export type FetchLike = (input: string, init?: RequestInit) => Promise<Response>;

export async function postOffer(fetchFn: FetchLike, url: string, offerSdp: string, signal?: AbortSignal): Promise<string> {
  let res: Response;
  try {
    res = await fetchFn(url, {
      method: "POST",
      headers: { "Content-Type": "application/sdp" },
      body: offerSdp,
      signal,
    });
  } catch (err) {
    throw new WhepError(`network error: ${(err as Error).message}`);
  }
  if (!res.ok) {
    const text = (await res.text().catch(() => "")).trim();
    throw new WhepError(text ? `HTTP ${res.status}: ${text}` : `HTTP ${res.status}`, res.status);
  }
  const answer = await res.text();
  if (!answer.startsWith("v=")) {
    throw new WhepError("response is not an SDP answer");
  }
  return answer;
}

/** URL of the WHEP endpoint for a stream, relative to the app origin. */
export function whepUrl(stream: string): string {
  return `/media/api/webrtc?src=${encodeURIComponent(stream)}`;
}
