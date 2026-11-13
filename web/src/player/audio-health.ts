// How well the audio is arriving, from the browser's WebRTC statistics.
//
// When audio data is late or lost, the browser fills the hole with made-up
// sound ("concealment"). A lot of that is heard as short cuts. The counters
// are totals since the connection started; health is their change between
// two readings.

/** Totals from the audio "inbound-rtp" statistics entry. */
export interface AudioCounters {
  packetsReceived: number;
  packetsLost: number;
  totalSamplesReceived: number;
  concealedSamples: number;
  /** Concealment while nothing was expected to play; not audible as a cut. */
  silentConcealedSamples: number;
}

export interface AudioHealth {
  /** Share of the played audio that was filled in, in percent. */
  gapPct: number;
  /** Share of audio packets that never arrived, in percent. */
  lostPct: number;
}

function num(v: unknown): number {
  return typeof v === "number" && Number.isFinite(v) ? v : 0;
}

export function readAudioCounters(s: Record<string, unknown>): AudioCounters {
  return {
    packetsReceived: num(s.packetsReceived),
    packetsLost: num(s.packetsLost),
    totalSamplesReceived: num(s.totalSamplesReceived),
    concealedSamples: num(s.concealedSamples),
    silentConcealedSamples: num(s.silentConcealedSamples),
  };
}

/** Health between two readings; null when no audio was played in between. */
export function audioHealth(prev: AudioCounters, cur: AudioCounters): AudioHealth | null {
  const samples = cur.totalSamplesReceived - prev.totalSamplesReceived;
  if (samples <= 0) return null;
  const audible =
    cur.concealedSamples - cur.silentConcealedSamples - (prev.concealedSamples - prev.silentConcealedSamples);
  const lost = cur.packetsLost - prev.packetsLost;
  const expected = cur.packetsReceived - prev.packetsReceived + lost;
  const pct = (part: number, whole: number) => (whole > 0 ? Math.min(100, Math.max(0, (100 * part) / whole)) : 0);
  return { gapPct: pct(audible, samples), lostPct: pct(lost, expected) };
}

/** Short text for the stats line, e.g. "audio gaps 1.2% (lost 0.8%)". Empty when fine. */
export function describeAudioHealth(h: AudioHealth | null | undefined): string {
  if (!h || (h.gapPct < 0.05 && h.lostPct < 0.05)) return "";
  const lost = h.lostPct >= 0.05 ? ` (lost ${h.lostPct.toFixed(1)}%)` : "";
  return `audio gaps ${h.gapPct.toFixed(1)}%${lost}`;
}
