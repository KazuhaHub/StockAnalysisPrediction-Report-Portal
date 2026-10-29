import { MessageChannel as NodeMessageChannel } from "node:worker_threads";
import { afterEach, expect, test, vi } from "vitest";
import {
  createBridgeHost,
  createBridgeClient,
  parseBridgePresentation,
  BRIDGE_PROTOCOL,
  BRIDGE_DEADLINE_MS,
  type BridgeBinding,
  BridgeBackendError,
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
        ([key]) =>
          !["type", "presentation", "capabilities", "backend"].includes(key),
      ),
    ),
    type: "app:ready",
    ...changes,
  };
}

async function backendPair(
  onBackendRequest?: Parameters<typeof createBridgeHost>[0]["onBackendRequest"],
) {
  vi.stubGlobal("MessageChannel", NodeMessageChannel);
  const w = windows();
  const host = createBridgeHost({
    localWindow: w.host,
    frameWindow: w.child,
    binding,
    presentation,
    capabilities: [],
    onBackendRequest,
  });
  const client = createBridgeClient({
    localWindow: w.child,
    parentWindow: w.host,
    parentOrigin: "https://portal.example.invalid",
    onPresentation: () => {},
  });
  cleanup.push(
    () => client.close(),
    () => host.close(),
  );
  await vi.waitFor(() => expect(host.state()).toBe("ready"));
  return { ...w, host, client };
}

test("own backend channel preserves JSON bytes without platform capabilities", async () => {
  const dispatch = vi.fn(async (_request: unknown, _signal: AbortSignal) => ({
    status: 200,
    body: '{"ok":true}',
  }));
  const { client } = await backendPair(dispatch);
  expect(client.context()?.capabilities).toEqual([]);
  expect(client.context()?.backendAvailable).toBe(true);
  const request = {
    method: "POST",
    path: "/v1/echo",
    body: '{ "role": "admin", "value": "中文" }',
  };
  await expect(client.requestBackend(request)).resolves.toEqual({
    status: 200,
    body: '{"ok":true}',
  });
  expect(dispatch).toHaveBeenCalledOnce();
  expect(dispatch.mock.calls[0][0]).toEqual(request);
  expect(dispatch.mock.calls[0][1]).toBeInstanceOf(AbortSignal);
});

test("backend requests fail closed before ready, without support and on invalid input", async () => {
  const { client } = await backendPair();
  expect(client.context()?.backendAvailable).toBe(false);
  await expect(
    client.requestBackend({ method: "GET", path: "/v1/echo", body: "" }),
  ).rejects.toMatchObject({ code: "unsupported" });
  const dispatch = vi.fn(async () => ({ status: 200, body: "{}" }));
  const connected = await backendPair(dispatch);
  for (const request of [
    { method: "GET", path: "/v1/echo", body: "{}" },
    { method: "POST", path: "https://other.invalid/v1/echo", body: "{}" },
    { method: "POST", path: "/v1/handshake", body: "{}" },
    { method: "POST", path: "/v1/echo", body: "not JSON" },
    {
      method: "POST",
      path: "/v1/echo",
      body: JSON.stringify("a".repeat(65536)),
    },
    {
      method: "POST",
      path: "/v1/echo",
      body: "{}",
      headers: { authorization: "ignored" },
    },
  ])
    await expect(
      connected.client.requestBackend(request),
    ).rejects.toMatchObject({ code: "invalid_request" });
  expect(dispatch).not.toHaveBeenCalled();
  connected.client.close();
  await expect(
    connected.client.requestBackend({
      method: "GET",
      path: "/v1/echo",
      body: "",
    }),
  ).rejects.toMatchObject({ code: "not_ready" });
});

test("closing backend channel aborts dispatch and marks outstanding writes uncertain", async () => {
  let signal: AbortSignal | undefined;
  let finish!: (v: { status: number; body: string }) => void;
  const dispatch = vi.fn((_request, abort: AbortSignal) => {
    signal = abort;
    return new Promise<{ status: number; body: string }>((resolve) => {
      finish = resolve;
    });
  });
  const { client, host } = await backendPair(dispatch);
  const pending = client.requestBackend({
    method: "POST",
    path: "/v1/echo",
    body: "{}",
  });
  const rejected = expect(pending).rejects.toMatchObject({
    code: "uncertain",
    retryable: false,
  });
  await vi.waitFor(() => expect(dispatch).toHaveBeenCalledOnce());
  host.close();
  client.close();
  await rejected;
  expect(signal?.aborted).toBe(true);
  finish({ status: 200, body: '{"late":true}' });
  expect(client.state()).toBe("closed");
});

test("backend failure details never cross the channel and requests are not retried", async () => {
  const dispatch = vi
    .fn()
    .mockRejectedValueOnce(new Error("private upstream credential"))
    .mockRejectedValueOnce(new BridgeBackendError("unavailable"))
    .mockResolvedValueOnce({ status: 302, body: "{}" });
  const { client } = await backendPair(dispatch);
  const request = { method: "POST", path: "/v1/echo", body: "{}" };
  for (const code of ["uncertain", "unavailable", "uncertain"]) {
    await expect(client.requestBackend(request)).rejects.toMatchObject({
      code,
      retryable: false,
    });
  }
  expect(dispatch).toHaveBeenCalledTimes(3);
});

test("backend concurrency is bounded and cancelling a request aborts only its dispatch", async () => {
  const signals: AbortSignal[] = [],
    finish: ((value: { status: number; body: string }) => void)[] = [];
  const dispatch = vi.fn((_request, signal: AbortSignal) => {
    signals.push(signal);
    return new Promise<{ status: number; body: string }>((resolve) =>
      finish.push(resolve),
    );
  });
  const { client } = await backendPair(dispatch),
    abort = new AbortController();
  const request = { method: "POST", path: "/v1/echo", body: "{}" };
  const one = client.requestBackend(request, abort.signal),
    two = client.requestBackend(request);
  const cancelled = expect(one).rejects.toMatchObject({
    code: "uncertain",
    retryable: false,
  });
  await vi.waitFor(() => expect(dispatch).toHaveBeenCalledTimes(2));
  await expect(client.requestBackend(request)).rejects.toMatchObject({
    code: "busy",
  });
  abort.abort();
  await cancelled;
  await vi.waitFor(() => expect(signals[0].aborted).toBe(true));
  expect(signals[1].aborted).toBe(false);
  finish[0]({ status: 200, body: '{"late":true}' });
  finish[1]({ status: 200, body: '{"second":true}' });
  await expect(two).resolves.toEqual({ status: 200, body: '{"second":true}' });
  expect(client.state()).toBe("ready");
});

test("a stale channel cannot dispatch a backend request and request IDs cannot replay", async () => {
  const dispatch = vi.fn(async () => ({ status: 200, body: "{}" }));
  const w = await backendPair(dispatch);
  const init = w.sent.find((item) => item.data.type === "app:init")!.data;
  const port = w.received.find((item) => item.event.data.type === "app:init")!
    .event.ports[0];
  const message = {
    ...acknowledgement(init),
    type: "backend.request",
    requestId: "1",
    request: { method: "POST", path: "/v1/echo", body: "{}" },
  };
  delete (message as Record<string, unknown>).revision;
  port.postMessage({ ...message, generation: "2" });
  await expect(w.client.requestBackend(message.request)).resolves.toEqual({
    status: 200,
    body: "{}",
  });
  expect(dispatch).toHaveBeenCalledOnce();
  port.postMessage(message);
  await vi.waitFor(() => expect(w.host.state()).toBe("failed"));
  expect(dispatch).toHaveBeenCalledOnce();
});

test("a backend timeout is uncertain, cancels native work and does not retry", async () => {
  let signal: AbortSignal | undefined;
  const dispatch = vi.fn((_request, abort: AbortSignal) => {
    signal = abort;
    return new Promise<{ status: number; body: string }>(() => {});
  });
  const { client } = await backendPair(dispatch);
  vi.useFakeTimers();
  const pending = client.requestBackend({
    method: "POST",
    path: "/v1/echo",
    body: "{}",
  });
  const result = expect(pending).rejects.toMatchObject({
    code: "uncertain",
    retryable: false,
  });
  await vi.waitFor(() => expect(dispatch).toHaveBeenCalledOnce());
  vi.advanceTimersByTime(30_000);
  await vi.waitFor(() => expect(signal?.aborted).toBe(true));
  await result;
  expect(dispatch).toHaveBeenCalledOnce();
  expect(client.state()).toBe("ready");
});

test("document measurements are bound, coalesced and can shrink without reloading", async () => {
  vi.stubGlobal("MessageChannel", NodeMessageChannel);
  const w = windows(),
    height = vi.fn();
  const host = createBridgeHost({
    localWindow: w.host,
    frameWindow: w.child,
    binding,
    presentation,
    capabilities: [],
    onDocumentHeight: height,
  });
  const client = createBridgeClient({
    localWindow: w.child,
    parentWindow: w.host,
    parentOrigin: "https://portal.example.invalid",
    onPresentation: () => {},
  });
  cleanup.push(
    () => client.close(),
    () => host.close(),
  );
  await vi.waitFor(() => expect(host.state()).toBe("ready"));
  for (const value of [-1, NaN, Infinity, 100001, 1.5])
    expect(client.reportDocumentHeight(value)).toBe(false);
  expect(client.reportDocumentHeight(100000)).toBe(true);
  await vi.waitFor(() => expect(height).toHaveBeenLastCalledWith(100000));
  for (let i = 0; i < 100; i++) client.reportDocumentHeight(500 + i);
  await vi.waitFor(() => expect(height).toHaveBeenLastCalledWith(599));
  expect(height).toHaveBeenCalledTimes(2);
  client.reportDocumentHeight(0);
  await vi.waitFor(() => expect(height).toHaveBeenLastCalledWith(0));
  client.close();
  expect(client.reportDocumentHeight(200)).toBe(false);
});

test("viewport mode cannot size the host", async () => {
  vi.stubGlobal("MessageChannel", NodeMessageChannel);
  const w = windows(),
    height = vi.fn();
  const host = createBridgeHost({
    localWindow: w.host,
    frameWindow: w.child,
    binding,
    presentation: { ...presentation, layout: "viewport" },
    capabilities: [],
    onDocumentHeight: height,
  });
  const client = createBridgeClient({
    localWindow: w.child,
    parentWindow: w.host,
    parentOrigin: "https://portal.example.invalid",
    onPresentation: () => {},
  });
  cleanup.push(
    () => client.close(),
    () => host.close(),
  );
  await vi.waitFor(() => expect(host.state()).toBe("ready"));
  expect(client.reportDocumentHeight(500)).toBe(false);
  expect(height).not.toHaveBeenCalled();
});

test("host refuses malicious height and ignores old generation measurements", async () => {
  vi.stubGlobal("MessageChannel", NodeMessageChannel);
  const w = windows(),
    height = vi.fn();
  const host = createBridgeHost({
    localWindow: w.host,
    frameWindow: w.child,
    binding,
    presentation,
    capabilities: [],
    onDocumentHeight: height,
  });
  const client = createBridgeClient({
    localWindow: w.child,
    parentWindow: w.host,
    parentOrigin: "https://portal.example.invalid",
    onPresentation: () => {},
  });
  cleanup.push(
    () => client.close(),
    () => host.close(),
  );
  await vi.waitFor(() => expect(host.state()).toBe("ready"));
  const init = w.sent.find((item) => item.data.type === "app:init")!.data;
  const port = w.received.find((item) => item.event.data.type === "app:init")!
    .event.ports[0];
  const message = {
    ...acknowledgement(init),
    type: "layout.measure",
    revision: "1",
    presentationRevision: "1",
    height: 500,
  };
  port.postMessage({ ...message, generation: "2" });
  client.reportDocumentHeight(100);
  await vi.waitFor(() => expect(height).toHaveBeenLastCalledWith(100));
  expect(height).toHaveBeenCalledOnce();
  port.postMessage({ ...message, revision: "2", height: Infinity });
  await vi.waitFor(() => expect(host.state()).toBe("failed"));
  expect(height).toHaveBeenCalledOnce();
});

test("draft state is a bounded boolean hint tied to the current channel", async () => {
  vi.stubGlobal("MessageChannel", NodeMessageChannel);
  const w = windows(),
    draft = vi.fn();
  const host = createBridgeHost({
    localWindow: w.host,
    frameWindow: w.child,
    binding,
    presentation,
    capabilities: [],
    onDraftChange: draft,
  });
  const client = createBridgeClient({
    localWindow: w.child,
    parentWindow: w.host,
    parentOrigin: "https://portal.example.invalid",
    onPresentation: () => {},
  });
  cleanup.push(
    () => client.close(),
    () => host.close(),
  );
  expect(client.setDraftDirty(true)).toBe(false);
  await vi.waitFor(() => expect(host.state()).toBe("ready"));
  expect(client.setDraftDirty(true)).toBe(true);
  await vi.waitFor(() => expect(draft).toHaveBeenLastCalledWith(true));
  client.setDraftDirty(false);
  await vi.waitFor(() => expect(draft).toHaveBeenLastCalledWith(false));
  expect(client.setDraftDirty("draft text" as unknown as boolean)).toBe(false);
  client.close();
  expect(client.setDraftDirty(true)).toBe(false);
});

test("draft host rejects contents while stale hints cannot clear edits", async () => {
  vi.stubGlobal("MessageChannel", NodeMessageChannel);
  const w = windows(),
    draft = vi.fn();
  const host = createBridgeHost({
    localWindow: w.host,
    frameWindow: w.child,
    binding,
    presentation,
    capabilities: [],
    onDraftChange: draft,
  });
  const client = createBridgeClient({
    localWindow: w.child,
    parentWindow: w.host,
    parentOrigin: "https://portal.example.invalid",
    onPresentation: () => {},
  });
  cleanup.push(
    () => client.close(),
    () => host.close(),
  );
  await vi.waitFor(() => expect(host.state()).toBe("ready"));
  const init = w.sent.find((item) => item.data.type === "app:init")!.data;
  const port = w.received.find((item) => item.event.data.type === "app:init")!
    .event.ports[0];
  const message = {
    ...acknowledgement(init),
    type: "draft.changed",
    revision: "2",
    dirty: true,
  };
  port.postMessage(message);
  await vi.waitFor(() => expect(draft).toHaveBeenLastCalledWith(true));
  port.postMessage({ ...message, revision: "1", dirty: false });
  port.postMessage({
    ...message,
    generation: "2",
    revision: "3",
    dirty: false,
  });
  // A following accepted hint fences delivery of the earlier stale messages.
  port.postMessage({ ...message, revision: "3" });
  await vi.waitFor(() => expect(draft).toHaveBeenCalledTimes(2));
  expect(draft.mock.calls.every(([dirty]) => dirty === true)).toBe(true);
  port.postMessage({ ...message, revision: "4", contents: "private text" });
  await vi.waitFor(() => expect(host.state()).toBe("failed"));
  expect(draft).toHaveBeenCalledTimes(2);
});

test("draft host fails a flooding channel without delivering excess reports", async () => {
  vi.stubGlobal("MessageChannel", NodeMessageChannel);
  const w = windows(),
    draft = vi.fn();
  const host = createBridgeHost({
    localWindow: w.host,
    frameWindow: w.child,
    binding,
    presentation,
    capabilities: [],
    onDraftChange: draft,
  });
  const client = createBridgeClient({
    localWindow: w.child,
    parentWindow: w.host,
    parentOrigin: "https://portal.example.invalid",
    onPresentation: () => {},
  });
  cleanup.push(
    () => client.close(),
    () => host.close(),
  );
  await vi.waitFor(() => expect(host.state()).toBe("ready"));
  const init = w.sent.find((item) => item.data.type === "app:init")!.data;
  const port = w.received.find((item) => item.event.data.type === "app:init")!
    .event.ports[0];
  const now = vi.spyOn(Date, "now").mockReturnValue(1234000);
  try {
    for (let n = 1; n <= 21; n++)
      port.postMessage({
        ...acknowledgement(init),
        type: "draft.changed",
        revision: String(n),
        dirty: true,
      });
    await vi.waitFor(() => expect(host.state()).toBe("failed"));
    expect(draft).toHaveBeenCalledTimes(20);
  } finally {
    now.mockRestore();
  }
});

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
    backend: false,
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
  { backend: "true" },
  { backend: undefined },
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

test("registered navigation is source-bound and the host owns the committed path", async () => {
  vi.stubGlobal("MessageChannel", NodeMessageChannel);
  const w = windows(),
    navigate = vi.fn();
  const routed = {
    ...presentation,
    navigation: {
      path: "/",
      routes: [
        { id: "home", path: "/", layout: "document" as const },
        { id: "editor", path: "/editor", layout: "viewport" as const },
      ],
    },
  };
  const host = createBridgeHost({
    localWindow: w.host,
    frameWindow: w.child,
    binding,
    presentation: routed,
    capabilities: [],
    onNavigate: navigate,
  });
  const client = createBridgeClient({
    localWindow: w.child,
    parentWindow: w.host,
    parentOrigin: "https://portal.example.invalid",
    onPresentation: () => {},
  });
  cleanup.push(
    () => client.close(),
    () => host.close(),
  );
  await vi.waitFor(() => expect(host.state()).toBe("ready"));
  expect(client.navigate("https://evil.invalid", false)).toBe(false);
  expect(client.navigate("/unknown", false)).toBe(false);
  expect(client.navigate("/editor", false)).toBe(true);
  await vi.waitFor(() =>
    expect(navigate).toHaveBeenCalledExactlyOnceWith("/editor", false),
  );
  expect(client.presentation()?.navigation?.path).toBe("/");
  host.updatePresentation({
    ...routed,
    layout: "viewport",
    navigation: { ...routed.navigation, path: "/editor" },
  });
  await vi.waitFor(() =>
    expect(client.presentation()?.navigation?.path).toBe("/editor"),
  );
  client.close();
  expect(client.navigate("/", true)).toBe(false);
});

for (const routes of [
  [],
  [{ id: "home", path: "/only", layout: "document" }],
  [
    { id: "home", path: "/", layout: "document" },
    { id: "home", path: "/editor", layout: "viewport" },
  ],
  [
    { id: "home", path: "/", layout: "document" },
    { id: "editor", path: "/", layout: "document" },
  ],
  [{ id: "home\n", path: "/", layout: "document" }],
  ...[
    "//evil",
    "/../x",
    "/a/./b",
    "/a/",
    "/%65dit",
    "/edit?x=1",
    "/edit#x",
    "/edit\n",
  ].map((path) => [
    { id: "home", path: "/", layout: "document" },
    { id: "editor", path, layout: "document" },
  ]),
]) {
  test(`presentation refuses an unsigned/noncanonical registry ${JSON.stringify(routes)}`, () => {
    expect(
      parseBridgePresentation({
        ...presentation,
        navigation: { path: "/", routes },
      }),
    ).toBeNull();
  });
}
test("route changes require the matching signed layout and cannot add query state", () => {
  const routes = [
    { id: "home", path: "/", layout: "document" },
    { id: "editor", path: "/editor", layout: "viewport" },
  ];
  expect(
    parseBridgePresentation({
      ...presentation,
      navigation: { path: "/editor", routes },
    }),
  ).toBeNull();
  expect(
    parseBridgePresentation({
      ...presentation,
      navigation: { path: "/", routes, query: { token: "secret" } },
    }),
  ).toBeNull();
});
test("host ignores stale navigation and refuses injected destinations before dispatch", async () => {
  vi.stubGlobal("MessageChannel", NodeMessageChannel);
  const w = windows(),
    navigate = vi.fn();
  const routed = {
    ...presentation,
    navigation: {
      path: "/",
      routes: [
        { id: "home", path: "/", layout: "document" as const },
        { id: "editor", path: "/editor", layout: "document" as const },
      ],
    },
  };
  const host = createBridgeHost({
    localWindow: w.host,
    frameWindow: w.child,
    binding,
    presentation: routed,
    capabilities: [],
    onNavigate: navigate,
  });
  const client = createBridgeClient({
    localWindow: w.child,
    parentWindow: w.host,
    parentOrigin: "https://portal.example.invalid",
    onPresentation: () => {},
  });
  cleanup.push(
    () => client.close(),
    () => host.close(),
  );
  await vi.waitFor(() => expect(host.state()).toBe("ready"));
  const init = w.sent.find((item) => item.data.type === "app:init")!.data;
  const port = w.received.find((item) => item.event.data.type === "app:init")!
    .event.ports[0];
  const message = {
    ...acknowledgement(init),
    type: "navigation.request",
    revision: "1",
    presentationRevision: "1",
    path: "/editor",
    replace: false,
  };
  port.postMessage({ ...message, generation: "2" });
  port.postMessage({ ...message, presentationRevision: "2" });
  port.postMessage({ ...message, revision: "2" });
  await vi.waitFor(() => expect(navigate).toHaveBeenCalledOnce());
  port.postMessage({ ...message, revision: "3", path: "https://evil.invalid" });
  await vi.waitFor(() => expect(host.state()).toBe("failed"));
  expect(navigate).toHaveBeenCalledOnce();
});
