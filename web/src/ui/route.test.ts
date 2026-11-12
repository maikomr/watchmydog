import { describe, expect, it } from "vitest";
import { pickCamera } from "./route";

describe("pickCamera", () => {
  const cams = [{ id: "dog" }, { id: "yard" }];
  it("picks the camera named in the link", () => {
    expect(pickCamera(cams, "?camera=yard")).toEqual({ id: "yard" });
  });
  it("falls back to the first camera", () => {
    expect(pickCamera(cams, "")).toEqual({ id: "dog" });
    expect(pickCamera(cams, "?camera=cat")).toEqual({ id: "dog" });
  });
  it("returns undefined without cameras", () => {
    expect(pickCamera([], "?camera=dog")).toBeUndefined();
  });
});
