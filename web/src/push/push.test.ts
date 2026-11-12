import { describe, expect, it } from "vitest";
import type { PushConfig, PushSubscriptionData } from "../api";
import { base64UrlToBytes, describePush, PushControl, type PushApi, type PushEnv, type SubscriptionLike } from "./push";

const KEY = "AQIDBAUGBwgJCg"; // bytes 1..10, base64url
const config = (over: Partial<PushConfig> = {}): PushConfig => ({
  enabled: true,
  publicKey: KEY,
  cooldownSeconds: 300,
  quietHours: null,
  ...over,
});

class FakeSub implements SubscriptionLike {
  unsubscribed = false;
  options: { applicationServerKey: ArrayBuffer | null } | undefined;
  constructor(
    readonly endpoint: string,
    key: string | null = KEY,
    private readonly owner?: FakeBrowser,
  ) {
    this.options = key === null ? undefined : { applicationServerKey: base64UrlToBytes(key).buffer };
  }
  toJSON(): PushSubscriptionData {
    return { endpoint: this.endpoint, keys: { p256dh: "p", auth: "a" } };
  }
  async unsubscribe(): Promise<boolean> {
    this.unsubscribed = true;
    if (this.owner && this.owner.sub === this) this.owner.sub = null;
    return true;
  }
}

class FakeBrowser implements PushEnv {
  perm: NotificationPermission = "default";
  /** What the user answers when asked. */
  answer: NotificationPermission = "granted";
  asked = 0;
  sub: FakeSub | null = null;
  subscribedWith: Uint8Array | null = null;

  permission = () => this.perm;
  requestPermission = async () => {
    this.asked++;
    this.perm = this.answer;
    return this.perm;
  };
  pushManager = {
    getSubscription: async () => this.sub,
    subscribe: async (o: { userVisibleOnly: boolean; applicationServerKey: Uint8Array<ArrayBuffer> }) => {
      if (!o.userVisibleOnly) throw new Error("userVisibleOnly must be true");
      this.subscribedWith = o.applicationServerKey;
      this.sub = new FakeSub("https://push.example/new", KEY, this);
      return this.sub;
    },
  };

  existing(endpoint: string, key: string | null = KEY): FakeSub {
    this.perm = "granted";
    this.sub = new FakeSub(endpoint, key, this);
    return this.sub;
  }
}

class FakeApi implements PushApi {
  cfg = config();
  subscribed: PushSubscriptionData[] = [];
  unsubscribed: string[] = [];
  tested: string[] = [];
  failSubscribe: Error | null = null;

  pushConfig = async () => this.cfg;
  pushSubscribe = async (s: PushSubscriptionData) => {
    if (this.failSubscribe) throw this.failSubscribe;
    this.subscribed.push(s);
  };
  pushUnsubscribe = async (e: string) => {
    this.unsubscribed.push(e);
  };
  pushTest = async (e: string) => {
    this.tested.push(e);
  };
}

describe("base64UrlToBytes", () => {
  it("decodes base64url with and without padding", () => {
    expect([...base64UrlToBytes("AQIDBAUGBwgJCg")]).toEqual([1, 2, 3, 4, 5, 6, 7, 8, 9, 10]);
    expect([...base64UrlToBytes("AQIDBAUGBwgJCg==")]).toEqual([1, 2, 3, 4, 5, 6, 7, 8, 9, 10]);
    expect([...base64UrlToBytes("-_8")]).toEqual([251, 255]);
  });
});

describe("PushControl.init", () => {
  it("reports unsupported without a browser environment", async () => {
    const api = new FakeApi();
    expect((await new PushControl(null, api).init()).state).toBe("unsupported");
  });

  it("reports disabled when the server has notifications off", async () => {
    const api = new FakeApi();
    api.cfg = config({ enabled: false, publicKey: "" });
    expect((await new PushControl(new FakeBrowser(), api).init()).state).toBe("disabled");
  });

  it("reports denied when the user blocked the site", async () => {
    const browser = new FakeBrowser();
    browser.perm = "denied";
    expect((await new PushControl(browser, new FakeApi()).init()).state).toBe("denied");
  });

  it("is off without a subscription and does not ask for permission", async () => {
    const browser = new FakeBrowser();
    const api = new FakeApi();
    expect((await new PushControl(browser, api).init()).state).toBe("off");
    expect(browser.asked).toBe(0);
    expect(api.subscribed).toHaveLength(0);
  });

  it("is on with a subscription and tells the server again", async () => {
    const browser = new FakeBrowser();
    browser.existing("https://push.example/old");
    const api = new FakeApi();
    const status = await new PushControl(browser, api).init();
    expect(status.state).toBe("on");
    expect(status.config).toEqual(config());
    expect(api.subscribed.map((s) => s.endpoint)).toEqual(["https://push.example/old"]);
  });

  it("drops a subscription made with another server key", async () => {
    const browser = new FakeBrowser();
    const old = browser.existing("https://push.example/old", "CgkIBwYFBAMCAQ");
    const api = new FakeApi();
    expect((await new PushControl(browser, api).init()).state).toBe("off");
    expect(old.unsubscribed).toBe(true);
    expect(api.subscribed).toHaveLength(0);
  });

  it("trusts a subscription whose key the browser does not expose", async () => {
    const browser = new FakeBrowser();
    browser.existing("https://push.example/old", null);
    expect((await new PushControl(browser, new FakeApi()).init()).state).toBe("on");
  });
});

describe("PushControl.enable", () => {
  it("asks for permission, subscribes with the server key and registers", async () => {
    const browser = new FakeBrowser();
    const api = new FakeApi();
    const status = await new PushControl(browser, api).enable();
    expect(status.state).toBe("on");
    expect(browser.asked).toBe(1);
    expect([...browser.subscribedWith!]).toEqual([1, 2, 3, 4, 5, 6, 7, 8, 9, 10]);
    expect(api.subscribed).toEqual([{ endpoint: "https://push.example/new", keys: { p256dh: "p", auth: "a" } }]);
  });

  it("does not ask again when permission is already granted, and reuses the subscription", async () => {
    const browser = new FakeBrowser();
    browser.existing("https://push.example/old");
    const api = new FakeApi();
    expect((await new PushControl(browser, api).enable()).state).toBe("on");
    expect(browser.asked).toBe(0);
    expect(browser.subscribedWith).toBeNull();
    expect(api.subscribed[0].endpoint).toBe("https://push.example/old");
  });

  it("reports denied or off when the user says no or dismisses the prompt", async () => {
    for (const [answer, want] of [
      ["denied", "denied"],
      ["default", "off"],
    ] as const) {
      const browser = new FakeBrowser();
      browser.answer = answer;
      const api = new FakeApi();
      expect((await new PushControl(browser, api).enable()).state).toBe(want);
      expect(browser.sub).toBeNull();
      expect(api.subscribed).toHaveLength(0);
    }
  });

  it("replaces a subscription made with another server key", async () => {
    const browser = new FakeBrowser();
    const old = browser.existing("https://push.example/old", "CgkIBwYFBAMCAQ");
    const api = new FakeApi();
    expect((await new PushControl(browser, api).enable()).state).toBe("on");
    expect(old.unsubscribed).toBe(true);
    expect(api.subscribed[0].endpoint).toBe("https://push.example/new");
  });

  it("removes the browser subscription when the server refuses it", async () => {
    const browser = new FakeBrowser();
    const api = new FakeApi();
    api.failSubscribe = new Error("HTTP 500");
    await expect(new PushControl(browser, api).enable()).rejects.toThrow("HTTP 500");
    expect(browser.sub).toBeNull();
  });

  it("does nothing when the server has notifications off", async () => {
    const browser = new FakeBrowser();
    const api = new FakeApi();
    api.cfg = config({ enabled: false });
    expect((await new PushControl(browser, api).enable()).state).toBe("disabled");
    expect(browser.asked).toBe(0);
  });
});

describe("PushControl.disable and test", () => {
  it("unsubscribes in the browser and on the server", async () => {
    const browser = new FakeBrowser();
    const sub = browser.existing("https://push.example/old");
    const api = new FakeApi();
    const control = new PushControl(browser, api);
    await control.init();
    expect((await control.disable()).state).toBe("off");
    expect(sub.unsubscribed).toBe(true);
    expect(api.unsubscribed).toEqual(["https://push.example/old"]);
  });

  it("is off even when the server cannot be told", async () => {
    const browser = new FakeBrowser();
    const sub = browser.existing("https://push.example/old");
    const api = new FakeApi();
    api.pushUnsubscribe = async () => {
      throw new Error("offline");
    };
    const control = new PushControl(browser, api);
    await control.init();
    expect((await control.disable()).state).toBe("off");
    expect(sub.unsubscribed).toBe(true);
  });

  it("disable without a subscription is harmless", async () => {
    const api = new FakeApi();
    const control = new PushControl(new FakeBrowser(), api);
    await control.init();
    expect((await control.disable()).state).toBe("off");
    expect(api.unsubscribed).toHaveLength(0);
  });

  it("asks the server for a test to this device", async () => {
    const browser = new FakeBrowser();
    browser.existing("https://push.example/old");
    const api = new FakeApi();
    await new PushControl(browser, api).test();
    expect(api.tested).toEqual(["https://push.example/old"]);
  });

  it("refuses a test when not subscribed", async () => {
    await expect(new PushControl(new FakeBrowser(), new FakeApi()).test()).rejects.toThrow("not on");
    await expect(new PushControl(null, new FakeApi()).test()).rejects.toThrow("not on");
  });
});

describe("describePush", () => {
  it("explains each state", () => {
    expect(describePush({ state: "off", config: config() })).toContain("Get a notification");
    expect(describePush({ state: "denied", config: config() })).toContain("blocked");
    expect(describePush({ state: "disabled", config: config() })).toContain("server");
    expect(describePush({ state: "unsupported", config: null })).toContain("cannot receive");
  });

  it("lists the rules when on", () => {
    expect(describePush({ state: "on", config: config() })).toBe("On for this device. At most one every 5 minutes.");
    expect(
      describePush({ state: "on", config: config({ cooldownSeconds: 0, quietHours: { start: "23:00", end: "07:00" } }) }),
    ).toBe("On for this device. None between 23:00 and 07:00 (the Mac's clock).");
    expect(describePush({ state: "on", config: config({ cooldownSeconds: 3600 }) })).toContain("every hour.");
    expect(describePush({ state: "on", config: config({ cooldownSeconds: 90 }) })).toContain("every 90 seconds.");
    expect(describePush({ state: "on", config: null })).toBe("On for this device.");
  });
});
