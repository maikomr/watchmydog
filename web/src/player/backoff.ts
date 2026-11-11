// Exponential backoff with jitter for reconnect attempts.

export interface BackoffOptions {
  /** First delay in ms. */
  base: number;
  /** Upper bound in ms. */
  max: number;
  /** Multiplier per attempt. */
  factor?: number;
  /** Random jitter as a fraction of the delay (0..1). */
  jitter?: number;
  /** Random source, injectable for tests. */
  random?: () => number;
}

export class Backoff {
  private attempt = 0;
  private readonly factor: number;
  private readonly jitter: number;
  private readonly random: () => number;

  constructor(private readonly opts: BackoffOptions) {
    this.factor = opts.factor ?? 2;
    this.jitter = opts.jitter ?? 0.2;
    this.random = opts.random ?? Math.random;
  }

  /** Returns the next delay and advances the attempt counter. */
  next(): number {
    const raw = Math.min(this.opts.max, this.opts.base * this.factor ** this.attempt);
    this.attempt += 1;
    const spread = raw * this.jitter;
    return Math.round(raw - spread + this.random() * 2 * spread);
  }

  /** Call after a successful connection so the next failure starts small. */
  reset(): void {
    this.attempt = 0;
  }

  get attempts(): number {
    return this.attempt;
  }
}
