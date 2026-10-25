import { MessageChannel as NodeMessageChannel } from "node:worker_threads";
import { afterEach, expect, test, vi } from "vitest";
import {
  createBridgeHost,
  createBridgeClient,
  parseBridgePresentation,
  BRIDGE_PROTOCOL,
  BRIDGE_DEADLINE_MS,
  type BridgeBinding,
} from "./bridge-v2";

const cleanup: (() => void)[] = [];
afterEach(() => {
  for (const close of cleanup.splice(0)) close();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

function windows() {
  const listeners = [
    new Set<(event: MessageEvent) => void>(),
    new Set<(event: MessageEvent) => void>(),
  ];
  const sent: { from: number; data: Record<string, unknown> }[] = [];
  const received: { to: number; event: MessageEvent }[] = [];
  const peers = [0, 1].map((index) => ({
    addEventListener: (
      _type: string,
      listener: (event: MessageEvent) => void,
    ) => listeners[index].add(listener),
    removeEventListener: (
      _type: string,
      listener: (event: MessageEvent) => void,
    ) => listeners[index].delete(listener),
    postMessage: (
      data: Record<string, unknown>,
      target: string,
      ports: MessagePort[] = [],
    ) => {
      expect(target).toBe(index === 1 ? "*" : "https://portal.example.invalid");
      const from = 1 - index;
      sent.push({ from, data: structuredClone(data) });
      const cloned = structuredClone({ data, ports }, { transfer: ports });
      const event = {
        ...cloned,
        source: peers[from],
        origin: from === 0 ? "https://portal.example.invalid" : "null",
      } as unknown as MessageEvent;
      received.push({ to: index, event });
      queueMicrotask(() => {
        for (const listener of listeners[index]) listener(event);
      });
    },
  }));
  cleanup.push(() => {
    for (const item of received)
      for (const port of item.event.ports) port.close();
  });
  return {
    host: peers[0] as unknown as Window,
    child: peers[1] as unknown as Window,
    sent,
    received,
    listeners,
  };
}

const binding = {
  installationId: "00000000-0000-0000-0000-000000000001",
  generation: "9007199254740993",
  policyRevision: "1",
  releaseSha256: "a".repeat(64),
};
const presentation = {
  locale: "zh-CN",
  timeZone: "Asia/Shanghai",
  layout: "document",
  theme: {
    dark: true,
    colorPrimary: "#1677ff",
    colorBg: "#141414",
    colorText: "rgba(255,255,255,0.88)",
    colorBorder: "#424242",
    colorBgLayout: "#000000",
    borderRadius: 8,
  },
} as const;

test("host waits for applied presentation and keeps 64-bit binding exact", async () => {
  vi.stubGlobal("MessageChannel", NodeMessageChannel);
  const w = windows(),
    ready = vi.fn(),
    applied = vi.fn();
  let finish!: () => void;
  const wait = new Promise<void>((resolve) => {
    finish = resolve;
  });
  const host = createBridgeHost({
    localWindow: w.host,
    frameWindow: w.child,
    binding,
    presentation,
    capabilities: ["reports.read"],
    onReady: ready,
  });
  cleanup.push(() => host.close());
  const client = createBridgeClient({
    localWindow: w.child,
    parentWindow: w.host,
    parentOrigin: "https://portal.example.invalid",
    onPresentation: applied.mockReturnValue(wait),
  });
  cleanup.push(() => client.close());
  await vi.waitFor(() => expect(applied).toHaveBeenCalledOnce());
  expect(ready).not.toHaveBeenCalled();
  finish();
  await vi.waitFor(() => expect(ready).toHaveBeenCalledOnce());
  expect(client.state()).toBe("ready");
  expect(
    w.sent.find((item) => item.data.type === "app:init")?.data.generation,
  ).toBe(binding.generation);
});

function hostOnly(override: Partial<BridgeBinding> = {}) {
  vi.stubGlobal("MessageChannel", NodeMessageChannel);
  const w = windows(),
    ready = vi.fn(),
    failed = vi.fn();
  const host = createBridgeHost({
    localWindow: w.host,
    frameWindow: w.child,
    binding: { ...binding, ...override },
    presentation,
    capabilities: ["reports.read"],
    onReady: ready,
    onFailure: failed,
  });
  cleanup.push(() => host.close());
  return { ...w, hostSession: host, ready, failed };
}
const hello = (childNonce = "c".repeat(32)) => ({
  type: "app:hello",
  protocols: [BRIDGE_PROTOCOL],
  childNonce,
});
function boundHello(
  w: ReturnType<typeof windows>,
  childNonce = "c".repeat(32),
) {
  return {
    ...hello(childNonce),
    hostNonce: w.sent.find((item) => item.data.type === "app:challenge")!.data
      .hostNonce,
  };
}
function acknowledgement(
  init: Record<string, unknown>,
  changes: Record<string, unknown> = {},
) {
  return {
    ...Object.fromEntries(
      Object.entries(init).filter(
        ([key]) => !["type", "presentation", "capabilities"].includes(key),
      ),
    ),
    type: "app:ready",
    ...changes,
  };
}

test("coalesced presentation updates wait for latest application before reveal", async () => {
  const w = hostOnly();
  let finish!: () => void;
  const applied = vi.fn().mockImplementationOnce(
    () =>
      new Promise<void>((resolve) => {
        finish = resolve;
      }),
  );
  const client = createBridgeClient({
    localWindow: w.child,
    parentWindow: w.host,
    parentOrigin: "https://portal.example.invalid",
    onPresentation: applied,
  });
  cleanup.push(() => client.close());
  await vi.waitFor(() => expect(applied).toHaveBeenCalledOnce());
  expect(
    w.hostSession.updatePresentation({ ...presentation, locale: "zh-TW" }),
  ).toBe(true);
  expect(
    w.hostSession.updatePresentation({ ...presentation, locale: "en-US" }),
  ).toBe(true);
  finish();
  await vi.waitFor(() => expect(w.ready).toHaveBeenCalledOnce());
  expect(applied).toHaveBeenCalledTimes(2);
  expect(w.ready.mock.calls[0][0].locale).toBe("en-US");
  expect(client.presentation()?.locale).toBe("en-US");
  expect(Object.isFrozen(client.presentation()?.theme)).toBe(true);
  expect(Object.isFrozen(client.context()?.capabilities)).toBe(true);
  expect(w.sent.filter((item) => item.data.type === "app:init")).toHaveLength(
    1,
  );
});

test("wrong window/origin and stale acknowledgements cannot reveal the frame", async () => {
  const w = hostOnly();
  for (const listener of w.listeners[0]) {
    listener({
      data: hello(),
      source: {},
      origin: "null",
      ports: [],
    } as unknown as MessageEvent);
    listener({
      data: hello(),
      source: w.child,
      origin: "https://other.example.invalid",
      ports: [],
    } as unknown as MessageEvent);
  }
  expect(w.sent.filter((item) => item.data.type === "app:init")).toHaveLength(
    0,
  );
  w.host.postMessage(boundHello(w), "https://portal.example.invalid");
  await vi.waitFor(() =>
    expect(
      w.received.some(
        (item) => item.to === 1 && item.event.data.type === "app:init",
      ),
    ).toBe(true),
  );
  const init = w.received.find(
    (item) => item.to === 1 && item.event.data.type === "app:init",
  )!.event;
  const port = init.ports[0];
  port.postMessage(acknowledgement(init.data, { generation: "1" }));
  port.postMessage(acknowledgement(init.data, { revision: "999" }));
  await new Promise((resolve) => setTimeout(resolve, 10));
  expect(w.ready).not.toHaveBeenCalled();
  port.postMessage(acknowledgement(init.data));
  await vi.waitFor(() => expect(w.ready).toHaveBeenCalledOnce());
});

test("late presentation completion after close cannot acknowledge a document", async () => {
  const w = hostOnly();
  let finish!: () => void;
  const apply = vi.fn(
    (_p, _context, _signal) =>
      new Promise<void>((resolve) => {
        finish = resolve;
      }),
  );
  const client = createBridgeClient({
    localWindow: w.child,
    parentWindow: w.host,
    parentOrigin: "https://portal.example.invalid",
    onPresentation: apply,
  });
  cleanup.push(() => client.close());
  await vi.waitFor(() => expect(apply).toHaveBeenCalledOnce());
  client.close();
  expect(apply.mock.calls[0][2].aborted).toBe(true);
  finish();
  await Promise.resolve();
  expect(client.state()).toBe("closed");
  expect(w.ready).not.toHaveBeenCalled();
});

test("handshake timeout disposes listeners and never falls back to legacy", async () => {
  vi.useFakeTimers();
  const w = hostOnly();
  vi.advanceTimersByTime(BRIDGE_DEADLINE_MS);
  expect(w.failed).toHaveBeenCalledExactlyOnceWith("handshake_timeout");
  expect(w.listeners[0].size).toBe(0);
  const failed = vi.fn();
  const client = createBridgeClient({
    localWindow: w.child,
    parentWindow: w.host,
    parentOrigin: "https://portal.example.invalid",
    onPresentation: vi.fn(),
    onFailure: failed,
  });
  cleanup.push(() => client.close());
  await vi.advanceTimersByTimeAsync(BRIDGE_DEADLINE_MS);
  expect(failed).toHaveBeenCalledExactlyOnceWith("handshake_timeout");
  expect(w.listeners[1].size).toBe(0);
  expect(
    w.sent
      .filter((item) => item.from === 1)
      .every((item) => item.data.type === "app:hello"),
  ).toBe(true);
  const count = w.sent.length;
  await vi.advanceTimersByTimeAsync(60_000);
  expect(w.sent.length).toBe(count);
});

test("construction snapshots its peer window even if the options object changes", async () => {
  vi.stubGlobal("MessageChannel", NodeMessageChannel);
  const w = windows(),
    ready = vi.fn();
  const options = {
    localWindow: w.host,
    frameWindow: w.child,
    binding,
    presentation,
    capabilities: [],
    onReady: ready,
  };
  const host = createBridgeHost(options);
  cleanup.push(() => host.close());
  options.frameWindow = {} as Window;
  const client = createBridgeClient({
    localWindow: w.child,
    parentWindow: w.host,
    parentOrigin: "https://portal.example.invalid",
    onPresentation: vi.fn(),
  });
  cleanup.push(() => client.close());
  await vi.waitFor(() => expect(ready).toHaveBeenCalledOnce());
});

function clientOnly() {
  const w = windows(),
    apply = vi.fn(),
    failed = vi.fn();
  const client = createBridgeClient({
    localWindow: w.child,
    parentWindow: w.host,
    parentOrigin: "https://portal.example.invalid",
    onPresentation: apply,
    onFailure: failed,
  });
  cleanup.push(() => client.close());
  const channel = new NodeMessageChannel();
  cleanup.push(() => {
    channel.port1.close();
    channel.port2.close();
  });
  const init = {
    type: "app:init",
    protocol: BRIDGE_PROTOCOL,
    ...binding,
    hostNonce: "a".repeat(32),
    childNonce: w.sent[0].data.childNonce,
    channelNonce: "b".repeat(32),
    revision: "1",
    presentation,
    capabilities: ["reports.read"],
  };
  function deliver(
    data: unknown,
    origin = "https://portal.example.invalid",
    source = w.host,
    ports = [channel.port2 as unknown as MessagePort],
  ) {
    for (const listener of w.listeners[1])
      listener({
        data: structuredClone(data),
        origin,
        source,
        ports,
      } as unknown as MessageEvent);
  }
  deliver(
    {
      type: "app:challenge",
      protocol: BRIDGE_PROTOCOL,
      hostNonce: init.hostNonce,
    },
    undefined,
    undefined,
    [],
  );
  return { ...w, client, apply, failed, init, deliver, port: channel.port1 };
}

test("client refuses wrong parent/origin and stale child nonce before initialization", async () => {
  const w = clientOnly();
  w.deliver(w.init, "https://other.example.invalid");
  w.deliver(w.init, "https://portal.example.invalid", {} as Window);
  // No port attached to stale/unrelated noise; keep the actual candidate port.
  w.deliver(
    { ...w.init, childNonce: "d".repeat(32) },
    undefined,
    undefined,
    [],
  );
  expect(w.apply).not.toHaveBeenCalled();
  w.deliver(w.init);
  await vi.waitFor(() => expect(w.client.state()).toBe("ready"));
  expect(w.apply).toHaveBeenCalledOnce();
});

for (const changes of [
  { token: "fixture-secret" },
  { protocol: "rp:init" },
  { capabilities: ["host.exec"] },
  { capabilities: ["reports.read", "reports.read"] },
]) {
  test(`closed init refuses unexpected fields/version/capabilities ${JSON.stringify(changes)}`, () => {
    const w = clientOnly();
    w.deliver({ ...w.init, ...changes });
    expect(w.client.state()).toBe("failed");
    expect(w.failed).toHaveBeenCalledExactlyOnceWith("protocol_error");
    expect(w.apply).not.toHaveBeenCalled();
  });
}

test("a stale generation cannot apply a live presentation update", async () => {
  const w = clientOnly();
  w.deliver(w.init);
  await vi.waitFor(() => expect(w.client.state()).toBe("ready"));
  const change = {
    ...acknowledgement(w.init),
    type: "presentation.changed",
    revision: "2",
    presentation: { ...presentation, locale: "zh-TW" },
  };
  w.port.postMessage({ ...change, generation: "1" });
  await new Promise((resolve) => setTimeout(resolve, 10));
  expect(w.client.presentation()?.locale).toBe("zh-CN");
  w.port.postMessage(change);
  await vi.waitFor(() => expect(w.client.presentation()?.locale).toBe("zh-TW"));
});

test("application callback failure exposes only a bounded code", async () => {
  const w = hostOnly(),
    failed = vi.fn();
  const client = createBridgeClient({
    localWindow: w.child,
    parentWindow: w.host,
    parentOrigin: "https://portal.example.invalid",
    onPresentation() {
      throw new Error("fixture credential must never cross the channel");
    },
    onFailure: failed,
  });
  cleanup.push(() => client.close());
  await vi.waitFor(() =>
    expect(failed).toHaveBeenCalledExactlyOnceWith("presentation_failed"),
  );
  expect(JSON.stringify(w.sent)).not.toContain("fixture credential");
});

test("distinct hello attempts have a fixed per-document budget", async () => {
  const w = hostOnly();
  for (let index = 0; index < 5; index++)
    w.host.postMessage(
      boundHello(w, index.toString().padStart(32, "0")),
      "https://portal.example.invalid",
    );
  await vi.waitFor(() =>
    expect(w.failed).toHaveBeenCalledExactlyOnceWith("protocol_error"),
  );
  expect(w.sent.filter((item) => item.data.type === "app:init")).toHaveLength(
    4,
  );
});

test("updating pending presentation cannot extend its acknowledgement deadline", async () => {
  const w = hostOnly();
  w.host.postMessage(boundHello(w), "https://portal.example.invalid");
  await vi.waitFor(() =>
    expect(
      w.received.some(
        (item) => item.to === 1 && item.event.data.type === "app:init",
      ),
    ).toBe(true),
  );
  const init = w.received.find(
    (item) => item.to === 1 && item.event.data.type === "app:init",
  )!.event;
  init.ports[0].postMessage(acknowledgement(init.data));
  await vi.waitFor(() => expect(w.ready).toHaveBeenCalledOnce());
  vi.useFakeTimers();
  w.hostSession.updatePresentation({ ...presentation, locale: "en-US" });
  vi.advanceTimersByTime(BRIDGE_DEADLINE_MS - 1);
  w.hostSession.updatePresentation({ ...presentation, locale: "zh-TW" });
  vi.advanceTimersByTime(1);
  expect(w.failed).toHaveBeenCalledExactlyOnceWith("presentation_timeout");
});

test("missing secure randomness fails without weak fallback", () => {
  vi.stubGlobal("crypto", undefined);
  const w = hostOnly();
  expect(w.failed).toHaveBeenCalledExactlyOnceWith("unsupported_environment");
  expect(w.listeners[0].size).toBe(0);
});

test("live channel messages cannot transfer extra ports", async () => {
  const w = clientOnly();
  w.deliver(w.init);
  await vi.waitFor(() => expect(w.client.state()).toBe("ready"));
  const extra = new NodeMessageChannel();
  cleanup.push(() => {
    extra.port1.close();
    extra.port2.close();
  });
  w.port.postMessage(
    {
      ...acknowledgement(w.init),
      type: "presentation.changed",
      revision: "2",
      presentation,
    },
    [extra.port2],
  );
  await vi.waitFor(() =>
    expect(w.failed).toHaveBeenCalledExactlyOnceWith("protocol_error"),
  );
});

test("a fresh host session can renegotiate the same WindowProxy", async () => {
  const w = hostOnly(),
    reset = vi.fn();
  const client = createBridgeClient({
    localWindow: w.child,
    parentWindow: w.host,
    parentOrigin: "https://portal.example.invalid",
    onPresentation: vi.fn(),
    onReset: reset,
  });
  cleanup.push(() => client.close());
  await vi.waitFor(() => expect(w.ready).toHaveBeenCalledOnce());
  const oldChallenge = w.sent.find(
    (item) => item.data.type === "app:challenge",
  )!.data;
  w.hostSession.close();
  const ready = vi.fn();
  const host = createBridgeHost({
    localWindow: w.host,
    frameWindow: w.child,
    binding: { ...binding, generation: "9007199254740994" },
    presentation,
    capabilities: [],
    onReady: ready,
  });
  cleanup.push(() => host.close());
  await vi.waitFor(() => expect(ready).toHaveBeenCalledOnce());
  expect(reset).toHaveBeenCalledOnce();
  expect(client.context()?.binding.generation).toBe("9007199254740994");
  w.child.postMessage(oldChallenge, "*");
  await new Promise((resolve) => setTimeout(resolve, 10));
  expect(reset).toHaveBeenCalledOnce();
  expect(client.context()?.binding.generation).toBe("9007199254740994");
});

test("a late rejection from the previous handshake cannot fail its successor", async () => {
  const w = hostOnly();
  let reject!: (error: unknown) => void;
  const apply = vi.fn().mockImplementationOnce(
    () =>
      new Promise<void>((_resolve, fail) => {
        reject = fail;
      }),
  );
  const failed = vi.fn();
  const client = createBridgeClient({
    localWindow: w.child,
    parentWindow: w.host,
    parentOrigin: "https://portal.example.invalid",
    onPresentation: apply,
    onFailure: failed,
  });
  cleanup.push(() => client.close());
  await vi.waitFor(() => expect(apply).toHaveBeenCalledOnce());
  const oldSignal = apply.mock.calls[0][2];
  w.hostSession.close();
  const ready = vi.fn();
  const host = createBridgeHost({
    localWindow: w.host,
    frameWindow: w.child,
    binding,
    presentation,
    capabilities: [],
    onReady: ready,
  });
  cleanup.push(() => host.close());
  await vi.waitFor(() => expect(ready).toHaveBeenCalledOnce());
  expect(oldSignal.aborted).toBe(true);
  reject(new Error("late fixture failure"));
  await Promise.resolve();
  expect(failed).not.toHaveBeenCalled();
  expect(client.state()).toBe("ready");
});

for (const [name, value] of [
  ["decimal newline", { generation: "1\n" }],
  ["UUID newline", { installationId: binding.installationId + "\n" }],
  ["digest newline", { releaseSha256: binding.releaseSha256 + "\n" }],
  ["integer overflow", { generation: "9223372036854775808" }],
  ["leading zero", { generation: "01" }],
] as const) {
  test(`noncanonical binding refuses ${name}`, () => {
    const w = hostOnly(value);
    expect(w.hostSession.state()).toBe("failed");
    expect(w.failed).toHaveBeenCalledExactlyOnceWith("invalid_context");
    expect(w.sent).toHaveLength(0);
  });
}

for (const value of [
  "#1677ff\n",
  "url(https://example.invalid/)",
  "rgba(256,0,0,1)",
  "rgb(0,0,0,0.5)",
  "var(--host-secret)",
]) {
  test(`presentation rejects unsafe/noncanonical color ${JSON.stringify(value)}`, () => {
    expect(
      parseBridgePresentation({
        ...presentation,
        theme: { ...presentation.theme, colorPrimary: value },
      }),
    ).toBeNull();
  });
}
