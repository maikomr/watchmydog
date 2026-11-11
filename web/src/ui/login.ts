// Login screen: one secret field. The session cookie is set by the server
// (HttpOnly), so this view never sees or stores the token.

import { ApiError, api } from "../api";
import { el } from "./dom";

export class LoginView {
  private readonly input = el("input", {
    type: "password",
    name: "secret",
    autocomplete: "current-password",
    autocapitalize: "none",
    spellcheck: "false",
    placeholder: "Secret",
    required: "",
  });
  private readonly submit = el("button", { class: "btn primary", type: "submit" }, "Log in");
  private readonly error = el("p", { class: "login-error", role: "alert" });

  constructor(
    private readonly root: HTMLElement,
    private readonly onSuccess: () => void,
  ) {}

  mount(): void {
    const form = el(
      "form",
      { class: "login" },
      el("h1", {}, "watchmydog"),
      el("p", { class: "muted" }, "Enter the secret from the Mac mini to watch the camera."),
      this.input,
      this.submit,
      this.error,
    );
    form.addEventListener("submit", (ev) => {
      ev.preventDefault();
      void this.login();
    });
    this.root.replaceChildren(form);
    this.input.focus();
  }

  private async login(): Promise<void> {
    const secret = this.input.value.trim();
    if (!secret) return;
    this.submit.disabled = true;
    this.error.textContent = "";
    try {
      await api.login(secret);
      this.onSuccess();
    } catch (err) {
      this.error.textContent = loginErrorMessage(err);
      this.input.select();
    } finally {
      this.submit.disabled = false;
    }
  }
}

export function loginErrorMessage(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.status === 401) return "Wrong secret.";
    if (err.status === 429) {
      const min = Math.max(1, Math.ceil((err.retryAfterSeconds ?? 60) / 60));
      return `Too many attempts. Try again in ${min} min.`;
    }
    return `Login failed: ${err.message}`;
  }
  return "Server unreachable. Check that Tailscale is on.";
}
