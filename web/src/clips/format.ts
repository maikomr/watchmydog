// Pure helpers for the clips section: text formatting and list bookkeeping.
// No DOM, so everything here is unit-tested.

import type { Clip, ClipList } from "../api";
import { formatDuration } from "../sound/format";

/** "850 kB", "12 MB", "1.4 GB" (decimal units, like a phone's storage screen). */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return "0 MB";
  if (bytes < 1e6) return `${Math.max(1, Math.round(bytes / 1e3))} kB`;
  if (bytes < 1e9) return `${Math.round(bytes / 1e6)} MB`;
  return `${(bytes / 1e9).toFixed(1)} GB`;
}

/** "42 s · 12 MB" */
export function describeClip(clip: Clip): string {
  return `${formatDuration(clip.durationSeconds * 1000)} · ${formatBytes(clip.sizeBytes)}`;
}

/** "30 days", "36 hours", "45 minutes" */
function formatKeep(seconds: number): string {
  const plural = (n: number, unit: string) => `${n} ${unit}${n === 1 ? "" : "s"}`;
  if (seconds >= 2 * 86400) return plural(Math.round(seconds / 86400), "day");
  if (seconds >= 2 * 3600) return plural(Math.round(seconds / 3600), "hour");
  return plural(Math.max(1, Math.round(seconds / 60)), "minute");
}

/**
 * The line under the heading: what the server does with clips.
 * "1.2 GB of 10.0 GB used. Clips are deleted after 30 days, or earlier when the space runs out."
 */
export function describeStorage(list: ClipList): string {
  const parts: string[] = [];
  if (!list.enabled) parts.push("Recording is switched off.");
  if (list.clips.length === 0 && !list.enabled) return parts.join(" ");
  if (list.clips.length === 0) return "No clips yet. A noise is recorded with a few seconds before and after it.";
  parts.push(`${formatBytes(list.usedBytes)} of ${formatBytes(list.maxBytes)} used.`);
  if (list.keepSeconds > 0) {
    parts.push(`Clips are deleted after ${formatKeep(list.keepSeconds)}, or earlier when the space runs out.`);
  } else {
    parts.push("The oldest clips are deleted when the space runs out.");
  }
  return parts.join(" ");
}

/** True when both lists show the same clips in the same order. */
export function sameClips(a: readonly Clip[], b: readonly Clip[]): boolean {
  return a.length === b.length && a.every((c, i) => c.id === b[i].id);
}
