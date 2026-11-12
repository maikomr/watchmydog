// Pure helpers for the sound section: meter scale, event list bookkeeping
// and text formatting. No DOM, so everything here is unit-tested.

import type { SoundEvent } from "../api";

/** Left edge of the level meter. A quiet room is around -50 dBFS. */
export const METER_FLOOR_DBFS = -70;

/**
 * Position of a level on the meter, 0 (floor or below) to 1 (full scale).
 * dBFS is already logarithmic, like hearing, so the scale is linear in dB.
 */
export function levelFraction(dbfs: number, floor = METER_FLOOR_DBFS): number {
  if (!Number.isFinite(dbfs)) return 0;
  return Math.min(1, Math.max(0, (dbfs - floor) / -floor));
}

/**
 * Returns the list with `ev` added or, if an event with that id is already
 * there, replaced (an event is announced when it starts and again when it
 * ends). Newest first, at most `max` entries. Does not modify `list`.
 */
export function mergeEvent(list: readonly SoundEvent[], ev: SoundEvent, max: number): SoundEvent[] {
  const out = list.filter((e) => e.id !== ev.id);
  out.push(ev);
  out.sort((a, b) => Date.parse(b.startedAt) - Date.parse(a.startedAt));
  return out.slice(0, max);
}

/** "0.4 s", "12 s", "3 min 05 s", "1 h 02 min". */
export function formatDuration(ms: number): string {
  if (ms < 950) return `${(Math.max(ms, 0) / 1000).toFixed(1)} s`;
  const total = Math.round(ms / 1000);
  if (total < 60) return `${total} s`;
  const pad = (n: number) => String(n).padStart(2, "0");
  if (total < 3600) return `${Math.floor(total / 60)} min ${pad(total % 60)} s`;
  return `${Math.floor(total / 3600)} h ${pad(Math.floor((total % 3600) / 60))} min`;
}

/**
 * When an event started, for the list: the time alone if it was today,
 * otherwise with the date. Locale and time zone default to the device's;
 * tests pass fixed ones.
 */
export function formatWhen(startedAt: Date, now: Date, locale?: string, timeZone?: string): string {
  const day = (d: Date) => d.toLocaleDateString("en-CA", { timeZone });
  const time = startedAt.toLocaleTimeString(locale, { hour: "2-digit", minute: "2-digit", second: "2-digit", timeZone });
  if (day(startedAt) === day(now)) return time;
  const date = startedAt.toLocaleDateString(locale, { month: "short", day: "numeric", timeZone });
  return `${date}, ${time}`;
}

/** "12 s · peak -8 dB", or "happening now" for an event without an end. */
export function describeEvent(ev: SoundEvent): string {
  if (ev.endedAt === null) return "happening now";
  const ms = Date.parse(ev.endedAt) - Date.parse(ev.startedAt);
  // 0 ms: the service was stopped mid-event and the real end is unknown.
  const length = ms > 0 ? formatDuration(ms) : "length unknown";
  return `${length} · peak ${ev.peakDbfs.toFixed(0)} dB`;
}
