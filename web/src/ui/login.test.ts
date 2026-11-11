import { describe, expect, it } from "vitest";
import { ApiError } from "../api";
import { loginErrorMessage } from "./login";

describe("loginErrorMessage", () => {
  it("explains each failure in plain words", () => {
    expect(loginErrorMessage(new ApiError("wrong secret", 401))).toBe("Wrong secret.");
    expect(loginErrorMessage(new ApiError("too many", 429, 840))).toBe("Too many attempts. Try again in 14 min.");
    expect(loginErrorMessage(new ApiError("too many", 429, 5))).toBe("Too many attempts. Try again in 1 min.");
    expect(loginErrorMessage(new ApiError("boom", 500))).toBe("Login failed: boom");
    expect(loginErrorMessage(new TypeError("Failed to fetch"))).toBe("Server unreachable. Check that Tailscale is on.");
  });
});
