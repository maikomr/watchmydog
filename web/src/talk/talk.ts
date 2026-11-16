// Talking to the dog: records the phone's microphone while a button is held
// and uploads the recording in small pieces, which the server plays on the
// Mac's loudspeaker (docs/adr/0010).
//
// The browser's MediaRecorder hands out a piece ("chunk") of WebM/Opus
// audio every CHUNK_MS. Chunks are sent one after the other, each as its
// own request with a running number, so the server can play them in order.
//
// All platform objects come in through TalkEnv, so the logic is tested
// without a microphone.

/** How much audio one chunk holds. Smaller is less delay but more requests. */
export const CHUNK_MS = 200;

/** The recording format: the one Chrome and Firefox produce and ffplay reads. */
export const TALK_MIME = "audio/webm;codecs=opus";

export type TalkState =
  /** Nothing is happening. */
  | "idle"
  /** Button pressed; waiting for the microphone and the server. */
  | "starting"
  /** Recording and sending. */
  | "talking"
  /** Button released; sending what is left. */
  | "stopping";

export interface MicLike {
  getTracks(): { stop(): void }[];
}

export interface RecorderLike {
  ondataavailable: ((ev: { data: Blob }) => void) | null;
  onstop: (() => void) | null;
  start(timesliceMs: number): void;
  stop(): void;
}

export interface TalkEnv {
  /** False when this browser cannot record in the needed format. */
  supported: boolean;
  /** Asks for the microphone; the browser may show a permission prompt. */
  getMicrophone(): Promise<MicLike>;
  createRecorder(mic: MicLike): RecorderLike;
}

export interface TalkApi {
  /** Opens the loudspeaker; resolves to the session id. */
  talkStart(cameraId: string): Promise<string>;
  talkChunk(session: string, seq: number, data: Blob): Promise<void>;
  talkStop(session: string): Promise<void>;
}

/** Called on every change; `note` is something to show to the user. */
export type TalkListener = (state: TalkState, note?: string) => void;

/** Turns a failure into a sentence for the user. */
export function describeTalkError(err: unknown): string {
  const name = (err as { name?: string } | null)?.name;
  if (name === "NotAllowedError" || name === "SecurityError") {
    return "The microphone is blocked. Allow it for this app in the browser's site settings.";
  }
  if (name === "NotFoundError") return "This device has no microphone.";
  const msg = err instanceof Error ? err.message : String(err);
  return `Could not talk: ${msg}`;
}

export class Talker {
  private state: TalkState = "idle";
  /** True while the button is down. */
  private pressed = false;
  private mic: MicLike | null = null;
  private recorder: RecorderLike | null = null;
  private session: string | null = null;
  private seq = 0;
  /** Resolves when every chunk handed out so far has been sent. */
  private sending: Promise<void> = Promise.resolve();
  private failed = false;

  constructor(
    private readonly env: TalkEnv,
    private readonly api: TalkApi,
    private readonly cameraId: string,
    private readonly listener: TalkListener,
  ) {}

  get currentState(): TalkState {
    return this.state;
  }

  private set(state: TalkState, note?: string): void {
    this.state = state;
    this.listener(state, note);
  }

  /** The button went down. */
  async press(): Promise<void> {
    this.pressed = true;
    if (this.state !== "idle") return;
    this.set("starting");
    try {
      const mic = await this.env.getMicrophone();
      this.mic = mic;
      if (!this.pressed) {
        // Released while the browser asked for permission: the user was
        // answering the prompt, not talking.
        this.cleanup();
        this.set("idle", "Microphone ready. Hold the button while you talk.");
        return;
      }
      const session = await this.api.talkStart(this.cameraId);
      this.session = session;
      if (!this.pressed) {
        await this.finish();
        return;
      }
      this.seq = 0;
      this.failed = false;
      this.sending = Promise.resolve();
      const recorder = this.env.createRecorder(mic);
      this.recorder = recorder;
      recorder.ondataavailable = (ev) => this.enqueue(session, ev.data);
      recorder.start(CHUNK_MS);
      this.set("talking");
    } catch (err) {
      await this.finish(describeTalkError(err));
    }
  }

  /** The button went up (or the touch was cancelled). */
  async release(): Promise<void> {
    this.pressed = false;
    if (this.state !== "talking") return; // "starting" notices `pressed` itself
    this.set("stopping");
    const recorder = this.recorder;
    if (recorder) {
      // The recorder hands out its last chunk, then reports that it stopped.
      await new Promise<void>((resolve) => {
        recorder.onstop = resolve;
        recorder.stop();
      });
    }
    await this.finish();
  }

  /** Sends chunks strictly one after the other. */
  private enqueue(session: string, data: Blob): void {
    if (data.size === 0 || this.failed) return;
    const seq = this.seq++;
    this.sending = this.sending.then(async () => {
      if (this.failed) return;
      try {
        await this.api.talkChunk(session, seq, data);
      } catch (err) {
        // One lost chunk makes the rest unplayable: stop here.
        this.failed = true;
        void this.abort(describeTalkError(err));
      }
    });
  }

  /** Stops at once after a failure while talking. */
  private async abort(note: string): Promise<void> {
    if (this.state !== "talking") return;
    this.recorder?.stop();
    await this.finish(note);
  }

  /** Waits for the queue, closes the session and the microphone. */
  private async finish(note?: string): Promise<void> {
    await this.sending;
    const session = this.session;
    this.session = null;
    if (session) await this.api.talkStop(session).catch(() => undefined);
    this.cleanup();
    this.set("idle", note);
  }

  private cleanup(): void {
    if (this.recorder) this.recorder.ondataavailable = null;
    this.recorder = null;
    // Stopping the tracks switches the phone's microphone indicator off.
    for (const t of this.mic?.getTracks() ?? []) t.stop();
    this.mic = null;
  }
}

/** The real browser. */
export function browserTalkEnv(): TalkEnv {
  const supported =
    typeof navigator !== "undefined" &&
    !!navigator.mediaDevices?.getUserMedia &&
    typeof MediaRecorder !== "undefined" &&
    MediaRecorder.isTypeSupported(TALK_MIME);
  return {
    supported,
    getMicrophone: () =>
      navigator.mediaDevices.getUserMedia({
        // The phone removes echo and steady noise and evens out the level.
        audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true, channelCount: 1 },
      }),
    createRecorder: (mic) =>
      new MediaRecorder(mic as MediaStream, { mimeType: TALK_MIME, audioBitsPerSecond: 32_000 }) as unknown as RecorderLike,
  };
}
