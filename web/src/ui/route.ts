// Links into the app. A notification opens "/?camera=<id>".

/** The camera named in the page's query string, else the first one. */
export function pickCamera<T extends { id: string }>(cameras: readonly T[], search: string): T | undefined {
  const wanted = new URLSearchParams(search).get("camera");
  return cameras.find((c) => c.id === wanted) ?? cameras[0];
}
