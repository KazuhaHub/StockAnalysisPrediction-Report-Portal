import { MessageChannel as NodeMessageChannel } from "node:worker_threads";
import { afterEach, describe, expect, test, vi } from "vitest";
import {
  createBridgeHost,
  createBridgeClient,
  parseBridgePresentation,
  BRIDGE_PROTOCOL,
  BRIDGE_PROTOCOL_PREVIOUS,
  BRIDGE_TARGETS_BACKOFF_MS,
  BRIDGE_DEADLINE_MS,
  type BridgeBinding,
  type NetworkTargetRequest,
  AppNetworkError,
  BridgeBackendError,
  BridgeObjectError,
  BridgeNotificationError,
  BridgePrincipalError,
  parseBridgePrincipalRequest,
  parseBridgePrincipalResponse,
} from "./bridge-v2";

const cleanup: (() => void)[] = [];
afterEach(() => {
  for (const close of cleanup.splice(0)) close();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

const documentHref = "https://portal.example.invalid/app-documents/x/index.html";

function windows(
  childHref: string = documentHref,
  hostOrigin = "https://portal.example.invalid",
) {
  const listeners = [
    new Set<(event: MessageEvent) => void>(),
    new Set<(event: MessageEvent) => void>(),
  ];
  const sent: { from: number; data: Record<string, unknown> }[] = [];
  const received: { to: number; event: MessageEvent }[] = [];
  const peers = [0, 1].map((index) => ({
    location: {
      href: index === 1 ? childHref : "https://portal.example.invalid/",
    },
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
      expect(target).toBe(index === 1 ? "*" : hostOrigin);
      const from = 1 - index;
      sent.push({ from, data: structuredClone(data) });
      const cloned = structuredClone({ data, ports }, { transfer: ports });
      const event = {
        ...cloned,
        source: peers[from],
        origin: from === 0 ? hostOrigin : "null",
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
      search: "",
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
    onPresentation: () => {},
  });
  cleanup.push(
    () => client.close(),
    () => host.close(),
  );
  await vi.waitFor(() => expect(host.state()).toBe("ready"));
  expect(client.navigate("https://evil.invalid")).toBe(false);
  expect(client.navigate("/unknown")).toBe(false);
  expect(client.navigate("/editor")).toBe(true);
  await vi.waitFor(() =>
    expect(navigate).toHaveBeenCalledExactlyOnceWith(
      "/editor",
      false,
      "",
      false,
    ),
  );
  expect(client.presentation()?.navigation?.path).toBe("/");
  host.updatePresentation({
    ...routed,
    layout: "viewport",
    navigation: {
      ...routed.navigation,
      path: "/editor",
    },
  });
  await vi.waitFor(() =>
    expect(client.presentation()?.navigation?.path).toBe("/editor"),
  );
  client.close();
  expect(client.navigate("/", { replace: true })).toBe(false);
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
        navigation: {
          search: "",
          path: "/",
          routes,
        },
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
      navigation: {
        search: "",
        path: "/editor",
        routes,
      },
    }),
  ).toBeNull();
  expect(
    parseBridgePresentation({
      ...presentation,
      navigation: {
        search: "",
        path: "/",
        routes,
        query: { token: "secret" },
      },
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
      search: "",
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
    search: "",
    revision: "1",
    presentationRevision: "1",
    path: "/editor",
    replace: false,
    bypassDraft: false,
  };
  port.postMessage({ ...message, generation: "2" });
  port.postMessage({ ...message, presentationRevision: "2" });
  port.postMessage({ ...message, revision: "2" });
  await vi.waitFor(() => expect(navigate).toHaveBeenCalledOnce());
  port.postMessage({ ...message, revision: "3", path: "https://evil.invalid" });
  await vi.waitFor(() => expect(host.state()).toBe("failed"));
  expect(navigate).toHaveBeenCalledOnce();
});

test("navigation query is validated against its signed destination rather than the active route", async () => {
  vi.stubGlobal("MessageChannel", NodeMessageChannel);
  const w = windows(),
    navigate = vi.fn();
  const routed = {
    ...presentation,
    navigation: {
      path: "/",
      search: "",
      routes: [
        {
          id: "home",
          path: "/",
          layout: "document" as const,
          query: [{ name: "page", type: "integer" as const, min: 0, max: 100 }],
        },
        {
          id: "editor",
          path: "/editor",
          layout: "document" as const,
          query: [{ name: "q", type: "text" as const, max_bytes: 32 }],
        },
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
    onPresentation: () => {},
  });
  cleanup.push(
    () => client.close(),
    () => host.close(),
  );
  await vi.waitFor(() => expect(host.state()).toBe("ready"));
  expect(client.navigate("/editor", { query: { page: 2 } })).toBe(false);
  expect(client.navigate("/editor", { query: { q: "中文" } })).toBe(true);
  await vi.waitFor(() =>
    expect(navigate).toHaveBeenCalledExactlyOnceWith(
      "/editor",
      false,
      "?q=%E4%B8%AD%E6%96%87",
      false,
    ),
  );
  const init = w.sent.find((item) => item.data.type === "app:init")!.data,
    port = w.received.find((item) => item.event.data.type === "app:init")!.event
      .ports[0];
  port.postMessage({
    ...acknowledgement(init),
    type: "navigation.request",
    revision: "2",
    presentationRevision: "1",
    path: "/editor",
    search: "?page=2",
    replace: false,
  });
  await vi.waitFor(() => expect(host.state()).toBe("failed"));
  expect(navigate).toHaveBeenCalledOnce();
});

test("shared page actions execute once and retire with signed route changes", async () => {
  vi.stubGlobal("MessageChannel", NodeMessageChannel);
  const w = windows(),
    changed = vi.fn();
  const routes = [
    { id: "home", path: "/", layout: "document" },
    { id: "edit", path: "/edit", layout: "document" },
  ] as const;
  const p = { ...presentation, navigation: { path: "/", search: "", routes } };
  const host = createBridgeHost({
    localWindow: w.host,
    frameWindow: w.child,
    binding,
    presentation: p,
    capabilities: [],
    onPageChange: changed,
  });
  cleanup.push(() => host.close());
  const client = createBridgeClient({
    localWindow: w.child,
    parentWindow: w.host,
    onPresentation: () => {},
  });
  cleanup.push(() => client.close());
  await vi.waitFor(() => expect(host.state()).toBe("ready"));
  const page = {
    title: "Records",
    description: "Latest records",
    actions: [
      {
        id: "save",
        label: "Save",
        variant: "primary",
        disabled: false,
        loading: false,
      },
    ],
  } as const;
  let finish!: () => void;
  const execute = vi.fn(
    (_id: string, _signal: AbortSignal) =>
      new Promise<void>((resolve) => {
        finish = resolve;
      }),
  );
  expect(client.describePage("a".repeat(32), page, execute)).toBe(true);
  await vi.waitFor(() =>
    expect(changed.mock.lastCall?.[0]?.page.title).toBe("Records"),
  );
  const old = changed.mock.lastCall![0].invoke;
  expect(old("save")).toBe(true);
  expect(old("save")).toBe(false);
  await vi.waitFor(() => expect(execute).toHaveBeenCalledTimes(1));
  finish();
  await vi.waitFor(() =>
    expect(changed.mock.lastCall?.[0]?.outcome).toBe("complete"),
  );
  expect(old("save")).toBe(true);
  await vi.waitFor(() => expect(execute).toHaveBeenCalledTimes(2));
  const runningSignal = execute.mock.calls[1][1];
  host.updatePresentation({
    ...p,
    navigation: { ...p.navigation, path: "/edit" },
  });
  await vi.waitFor(() => expect(runningSignal.aborted).toBe(true));
  expect(changed.mock.lastCall?.[0]).toBeNull();
  expect(old("save")).toBe(false);
  finish();
  expect(client.describePage("b".repeat(32), page, execute)).toBe(true);
  await vi.waitFor(() =>
    expect(changed.mock.lastCall?.[0]?.page.title).toBe("Records"),
  );
  expect(changed.mock.lastCall?.[0]?.outcome).toBeNull();
  expect(old("save")).toBe(false);
});

test("page descriptions require exact current binding, revision, scope and closed metadata", async () => {
  vi.stubGlobal("MessageChannel", NodeMessageChannel);
  const w = windows(),
    changed = vi.fn(),
    failed = vi.fn();
  const host = createBridgeHost({
    localWindow: w.host,
    frameWindow: w.child,
    binding,
    presentation,
    capabilities: [],
    onPageChange: changed,
    onFailure: failed,
  });
  const client = createBridgeClient({
    localWindow: w.child,
    parentWindow: w.host,
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
  const page = { title: "Accepted", description: "", actions: [] };
  const message = {
    ...acknowledgement(init),
    type: "page.describe",
    revision: "1",
    presentationRevision: "1",
    scope: "a".repeat(32),
    page,
  };
  port.postMessage({
    ...message,
    generation: "2",
    page: { ...page, title: "Forged" },
  });
  port.postMessage(message);
  await vi.waitFor(() =>
    expect(changed.mock.lastCall?.[0]?.page.title).toBe("Accepted"),
  );
  port.postMessage({
    ...message,
    revision: "2",
    presentationRevision: "2",
    page: { ...page, title: "Uncommitted" },
  });
  port.postMessage({
    ...message,
    revision: "3",
    page: { ...page, title: "Current" },
  });
  await vi.waitFor(() =>
    expect(changed.mock.lastCall?.[0]?.page.title).toBe("Current"),
  );
  expect(
    changed.mock.calls.some(([value]) =>
      ["Forged", "Uncommitted"].includes(value?.page.title),
    ),
  ).toBe(false);
  port.postMessage({
    ...message,
    revision: "4",
    page: { ...page, html: "<script>" },
  });
  await vi.waitFor(() =>
    expect(failed).toHaveBeenCalledExactlyOnceWith("protocol_error"),
  );
});

async function networkPair(onNetworkRequest?: Parameters<typeof createBridgeHost>[0]["onNetworkRequest"], granted=true, extraCapabilities: readonly string[]=[]) {
 vi.stubGlobal("MessageChannel",NodeMessageChannel);
 const w=windows();
 const host=createBridgeHost({localWindow:w.host,frameWindow:w.child,binding,presentation,capabilities:granted?["network.http",...extraCapabilities]:[],onNetworkRequest});
 const client=createBridgeClient({localWindow:w.child,parentWindow:w.host,onPresentation:()=>{}});
 cleanup.push(()=>client.close(),()=>host.close());
 await vi.waitFor(()=>expect(host.state()).toBe("ready"));
 return {...w,host,client};
}
async function networkStreamPair(
 onNetworkStreamRequest?: Parameters<typeof createBridgeHost>[0]["onNetworkStreamRequest"],
 granted=true,
 extraCapabilities: readonly string[]=[],
) {
 vi.stubGlobal("MessageChannel",NodeMessageChannel);
 const w=windows();
 const host=createBridgeHost({
  localWindow:w.host,
  frameWindow:w.child,
  binding,
  presentation,
  capabilities:granted?["network.stream",...extraCapabilities]:[],
  onNetworkStreamRequest,
 });
 const client=createBridgeClient({
  localWindow:w.child,
  parentWindow:w.host,
  onPresentation:()=>{},
 });
 cleanup.push(()=>client.close(),()=>host.close());
 await vi.waitFor(()=>expect(host.state()).toBe("ready"));
 return {...w,host,client};
}
const reportSummary = {
 id:42,runId:"run-42",symbol:"600100",name:"Example",date:"2026-10-30",
 time:"09:30",kind:"research",subtype:"daily",title:"Current report",
 source:"analysis",version:"report-version",
};
async function reportsPair(
 onReportRequest?: Parameters<typeof createBridgeHost>[0]["onReportRequest"],
 granted=true,
) {
 vi.stubGlobal("MessageChannel",NodeMessageChannel);
 const w=windows();
 const host=createBridgeHost({
  localWindow:w.host,
  frameWindow:w.child,
  binding,
  presentation,
  capabilities:granted?["reports.read"]:[],
  onReportRequest,
 });
 const client=createBridgeClient({
  localWindow:w.child,
  parentWindow:w.host,
  onPresentation:()=>{},
 });
 cleanup.push(()=>client.close(),()=>host.close());
 await vi.waitFor(()=>expect(host.state()).toBe("ready"));
 return {...w,host,client};
}
test("report channel preserves query, detail and Markdown results",async()=>{
 const dispatch=vi.fn(async(request:{kind:string},_signal:AbortSignal)=>request.kind==="get"
  ? {kind:"get" as const,report:{...reportSummary,bodyMarkdown:"# Evidence"}}
  : {kind:"query" as const,count:1,total:3,offset:0,limit:20,items:[reportSummary]});
 const {client}=await reportsPair(dispatch);
 expect(client.context()?.capabilities).toEqual(["reports.read"]);
 await expect(client.requestReports({kind:"query",query:{q:"current",limit:20}})).resolves.toEqual({
  kind:"query",count:1,total:3,offset:0,limit:20,items:[reportSummary],
 });
 await expect(client.requestReports({kind:"get",id:42})).resolves.toEqual({
  kind:"get",report:{...reportSummary,bodyMarkdown:"# Evidence"},
 });
 expect(dispatch).toHaveBeenCalledTimes(2);
 expect(dispatch.mock.calls[0][0]).toEqual({kind:"query",query:{q:"current",limit:20}});
 expect(dispatch.mock.calls[0][1]).toBeInstanceOf(AbortSignal);
});
test("report requests fail closed without grant, transport or valid input",async()=>{
 const dispatch=vi.fn(async()=>({kind:"query" as const,count:0,total:0,offset:0,limit:20,items:[]}));
 const ungranted=await reportsPair(dispatch,false);
 await expect(ungranted.client.requestReports({kind:"query",query:{limit:20}})).rejects.toMatchObject({code:"unsupported",retryable:false});
 const unsupported=await reportsPair();
 await expect(unsupported.client.requestReports({kind:"query",query:{limit:20}})).rejects.toMatchObject({code:"unsupported"});
 const ready=await reportsPair(dispatch);
 for(const request of [
  {kind:"query",query:{limit:51}},
  {kind:"get",id:0},
  {kind:"query",query:{headers:{}}},
  {kind:"query",query:{limit:20},extra:true},
 ]) await expect(ready.client.requestReports(request as never)).rejects.toMatchObject({code:"invalid_request"});
 expect(dispatch).not.toHaveBeenCalled();
});
test("report failures stay bounded and a closed host aborts pending reads",async()=>{
 let signal:AbortSignal|undefined;
 const dispatch=vi.fn((_request:unknown,abort:AbortSignal)=>{
  signal=abort;
  return new Promise<never>(()=>{});
 });
 const {client,host}=await reportsPair(dispatch);
 const pending=client.requestReports({kind:"query",query:{limit:20}});
 const rejected=expect(pending).rejects.toMatchObject({code:"uncertain",retryable:false});
 await vi.waitFor(()=>expect(dispatch).toHaveBeenCalledOnce());
 host.close();
 await vi.waitFor(()=>expect(signal?.aborted).toBe(true));
 client.close();
 await rejected;
 expect(dispatch).toHaveBeenCalledOnce();
});
test("report failure details are replaced with stable channel codes",async()=>{
 const dispatch=vi
  .fn()
  .mockRejectedValueOnce(new Error("private upstream detail"))
  .mockResolvedValueOnce({kind:"query",count:1,total:1,offset:0,limit:20,items:[]});
 const {client}=await reportsPair(dispatch);
 await expect(client.requestReports({kind:"query",query:{limit:20}})).rejects.toMatchObject({code:"uncertain",retryable:false});
 await expect(client.requestReports({kind:"query",query:{limit:20}})).rejects.toMatchObject({code:"uncertain",retryable:false});
 expect(dispatch).toHaveBeenCalledTimes(2);
});
test("report request identities are isolated and cannot be replayed",async()=>{
 const dispatch=vi.fn(()=>new Promise<never>(()=>{}));
 const w=await reportsPair(dispatch);
 const init=w.sent.find(item=>item.data.type==="app:init")!.data;
 const port=w.received.find(item=>item.event.data.type==="app:init")!.event.ports[0];
 const message={
  ...acknowledgement(init),
  type:"reports.request",
  requestId:"1",
  request:{kind:"query",query:{limit:20}},
 };
 delete (message as Record<string,unknown>).revision;
 port.postMessage({...message,generation:"2"});
 port.postMessage(message);
 await vi.waitFor(()=>expect(dispatch).toHaveBeenCalledOnce());
 port.postMessage(message);
 await vi.waitFor(()=>expect(w.host.state()).toBe("failed"));
 expect(dispatch).toHaveBeenCalledOnce();
});
test("report deadline is uncertain, cancels native work and never retries",async()=>{
 let signal:AbortSignal|undefined;
 const dispatch=vi.fn((_request:unknown,abort:AbortSignal)=>{
  signal=abort;
  return new Promise<never>(()=>{});
 });
 const {client}=await reportsPair(dispatch);
 vi.useFakeTimers();
 const pending=client.requestReports({kind:"query",query:{limit:20}});
 const rejected=expect(pending).rejects.toMatchObject({code:"uncertain",retryable:false});
 await vi.waitFor(()=>expect(dispatch).toHaveBeenCalledOnce());
 await vi.advanceTimersByTimeAsync(20_000);
 await rejected;
 expect(signal?.aborted).toBe(true);
 expect(dispatch).toHaveBeenCalledOnce();
});
const notificationRequest={
 occurrenceId:"digest:2026-11-02",
 title:"Daily digest",
 message:"Three reports are ready.",
 severity:"info" as const,
};
async function notificationsPair(
 onNotificationRequest?: Parameters<typeof createBridgeHost>[0]["onNotificationRequest"],
 granted=true,
) {
 vi.stubGlobal("MessageChannel",NodeMessageChannel);
 const w=windows();
 const host=createBridgeHost({
  localWindow:w.host,
  frameWindow:w.child,
  binding,
  presentation,
  capabilities:granted?["notifications.publish"]:[],
  onNotificationRequest,
 });
 const client=createBridgeClient({
  localWindow:w.child,
  parentWindow:w.host,
  onPresentation:()=>{},
 });
 cleanup.push(()=>client.close(),()=>host.close());
 await vi.waitFor(()=>expect(host.state()).toBe("ready"));
 return {...w,host,client};
}
test("notification channel preserves occurrence idempotency result",async()=>{
 const dispatch=vi.fn(async()=>({id:"00000000-0000-4000-8000-000000000042"}));
 const {client}=await notificationsPair(dispatch);
 await expect(client.requestNotifications(notificationRequest)).resolves.toEqual({
  id:"00000000-0000-4000-8000-000000000042",
 });
 expect(dispatch).toHaveBeenCalledWith(notificationRequest,expect.any(AbortSignal));
});
test("notification channel refuses missing grants, unsupported transports and malformed input",async()=>{
 const dispatch=vi.fn(async()=>({id:"00000000-0000-4000-8000-000000000042"}));
 const absent=await notificationsPair(dispatch,false);
 await expect(absent.client.requestNotifications(notificationRequest)).rejects.toMatchObject({code:"unsupported",retryable:false});
 const unsupported=await notificationsPair();
 await expect(unsupported.client.requestNotifications(notificationRequest)).rejects.toMatchObject({code:"unsupported"});
 const ready=await notificationsPair(dispatch);
 for(const request of [
  {...notificationRequest,title:" "},
  {...notificationRequest,severity:"critical"},
  {...notificationRequest,message:"x".repeat(8193)},
  {...notificationRequest,extra:true},
 ]) await expect(ready.client.requestNotifications(request as never)).rejects.toMatchObject({code:"invalid_request"});
 expect(dispatch).not.toHaveBeenCalled();
});
test("notification failures remain stable and close aborts pending publishing",async()=>{
 let signal:AbortSignal|undefined;
 const dispatch=vi.fn((_request:unknown,abort:AbortSignal)=>{
  signal=abort;
  return new Promise<never>(()=>{});
 });
 const {client,host}=await notificationsPair(dispatch);
 const pending=client.requestNotifications(notificationRequest);
 const rejected=expect(pending).rejects.toMatchObject({code:"uncertain",retryable:false});
 await vi.waitFor(()=>expect(dispatch).toHaveBeenCalledOnce());
 host.close();
 await vi.waitFor(()=>expect(signal?.aborted).toBe(true));
 client.close();
 await rejected;
 const stable=await notificationsPair(vi.fn()
  .mockRejectedValueOnce(new BridgeNotificationError("conflict"))
  .mockRejectedValueOnce(new BridgeNotificationError("capacity")));
 for(const code of ["conflict","capacity"]){
  await expect(stable.client.requestNotifications(notificationRequest)).rejects.toMatchObject({code,retryable:false});
 }
});
const userP="user:"+"a".repeat(64);
const ouP="ou:"+"b".repeat(64);
const principalSearch={action:"search" as const,kind:"ou" as const,query:"res",after:"",limit:20};
const principalSearchResponse={action:"search" as const,items:[{principal:ouP,label:"Research",parent:null}],next:"abc_-"};
async function principalsPair(
 onPrincipalRequest?: Parameters<typeof createBridgeHost>[0]["onPrincipalRequest"],
 granted=true,
) {
 vi.stubGlobal("MessageChannel",NodeMessageChannel);
 const w=windows();
 const host=createBridgeHost({
  localWindow:w.host,
  frameWindow:w.child,
  binding,
  presentation,
  capabilities:granted?["principals.read"]:[],
  onPrincipalRequest,
 });
 const client=createBridgeClient({
  localWindow:w.child,
  parentWindow:w.host,
  onPresentation:()=>{},
 });
 cleanup.push(()=>client.close(),()=>host.close());
 await vi.waitFor(()=>expect(host.state()).toBe("ready"));
 return {...w,host,client};
}
test("principal parsers fix the wire shape and normalise search defaults",()=>{
 expect(parseBridgePrincipalRequest({action:"search",kind:"user"})).toEqual({action:"search",kind:"user",query:"",after:"",limit:50});
 expect(parseBridgePrincipalRequest(principalSearch)).toEqual(principalSearch);
 expect(parseBridgePrincipalRequest({action:"resolve",principals:[userP,ouP]})).toEqual({action:"resolve",principals:[userP,ouP]});
 for(const bad of [
  null,{action:"search"},{action:"search",kind:"group"},{action:"search",kind:"user",extra:1},
  {action:"search",kind:"user",query:"x".repeat(65)},{action:"search",kind:"user",limit:0},{action:"search",kind:"user",limit:51},
  {action:"search",kind:"user",limit:1.5},{action:"search",kind:"user",after:"a".repeat(129)},{action:"search",kind:"user",after:"a b"},
  {action:"resolve",principals:[]},{action:"resolve",principals:[userP,userP]},{action:"resolve",principals:["user:zz"]},
  {action:"resolve",principals:Array.from({length:101},(_,i)=>"user:"+i.toString(16).padStart(64,"0"))},
  {action:"resolve",principals:[userP],extra:true},{action:"other"},
 ]) expect(parseBridgePrincipalRequest(bad)).toBeNull();
 expect(parseBridgePrincipalResponse(principalSearchResponse)).toEqual(principalSearchResponse);
 expect(parseBridgePrincipalResponse({action:"search",items:[{principal:userP,label:"Ada"}],next:""})).toEqual({action:"search",items:[{principal:userP,label:"Ada"}],next:""});
 expect(parseBridgePrincipalResponse({action:"resolve",items:[{principal:userP,label:"Ada"}],unknown:[ouP]})).toEqual({action:"resolve",items:[{principal:userP,label:"Ada"}],unknown:[ouP]});
 for(const bad of [
  null,{action:"search",items:[],next:"a b"},{action:"search",items:[{principal:"user:x",label:"A"}],next:""},
  {action:"search",items:[{principal:userP,label:"x".repeat(161)}],next:""},{action:"search",items:[{principal:userP,label:"A",extra:1}],next:""},
  {action:"search",items:[],next:"",extra:1},{action:"search",items:Array.from({length:51},()=>({principal:userP,label:"A"})),next:""},
  {action:"resolve",items:[],unknown:["nope"]},{action:"resolve",items:[]},
 ]) expect(parseBridgePrincipalResponse(bad)).toBeNull();
});
test("principal channel carries search and resolve through the host callback",async()=>{
 const dispatch=vi.fn(async(request:{action:string})=>request.action==="search"
  ?principalSearchResponse
  :{action:"resolve" as const,items:[{principal:userP,label:"Ada"}],unknown:[]});
 const {client}=await principalsPair(dispatch);
 await expect(client.requestPrincipals(principalSearch)).resolves.toEqual(principalSearchResponse);
 await expect(client.requestPrincipals({action:"resolve",principals:[userP]})).resolves.toEqual({action:"resolve",items:[{principal:userP,label:"Ada"}],unknown:[]});
 expect(dispatch).toHaveBeenNthCalledWith(1,principalSearch,expect.any(AbortSignal));
});
test("principal channel refuses missing grants, unsupported transports and malformed input",async()=>{
 const dispatch=vi.fn(async()=>principalSearchResponse);
 const absent=await principalsPair(dispatch,false);
 await expect(absent.client.requestPrincipals(principalSearch)).rejects.toMatchObject({code:"unsupported",retryable:false});
 const unsupported=await principalsPair();
 await expect(unsupported.client.requestPrincipals(principalSearch)).rejects.toMatchObject({code:"unsupported"});
 const ready=await principalsPair(dispatch);
 await expect(ready.client.requestPrincipals({action:"search",kind:"group"} as never)).rejects.toMatchObject({code:"invalid_request"});
 expect(dispatch).not.toHaveBeenCalled();
});
test("principal failures stay stable and a malformed host answer becomes uncertain",async()=>{
 const stable=await principalsPair(vi.fn()
  .mockRejectedValueOnce(new BridgePrincipalError("not_permitted"))
  .mockRejectedValueOnce(new BridgePrincipalError("busy"))
  .mockRejectedValueOnce(new Error("raw detail"))
  .mockResolvedValueOnce({action:"search",items:[{principal:"user:x",label:"A"}],next:""}));
 for(const code of ["not_permitted","busy","uncertain","uncertain"]){
  await expect(stable.client.requestPrincipals(principalSearch)).rejects.toMatchObject({code,retryable:false});
 }
});
test("principal channel serialises requests and close aborts the pending call",async()=>{
 let signal:AbortSignal|undefined;
 const dispatch=vi.fn((_request:unknown,abort:AbortSignal)=>{signal=abort;return new Promise<never>(()=>{});});
 const {client,host}=await principalsPair(dispatch);
 const pending=client.requestPrincipals(principalSearch);
 const rejected=expect(pending).rejects.toMatchObject({code:"uncertain"});
 await expect(client.requestPrincipals(principalSearch)).rejects.toMatchObject({code:"busy"});
 await vi.waitFor(()=>expect(dispatch).toHaveBeenCalledOnce());
 host.close();
 await vi.waitFor(()=>expect(signal?.aborted).toBe(true));
 client.close();
 await rejected;
});
const networkRequest=()=>({method:"GET",url:"https://api.example.com/v1",accept:"application/json",contentType:"",bodyBase64:""});
const objectSummary={
 id:"00000000-0000-4000-8000-000000000042",collection:"note",label:"First note",
 mediaType:"application/json",sizeBytes:5,revision:1,createdAt:1,updatedAt:1,
};
async function objectsPair(
 onObjectRequest?: Parameters<typeof createBridgeHost>[0]["onObjectRequest"],
 granted=true,
) {
 vi.stubGlobal("MessageChannel",NodeMessageChannel);
 const w=windows();
 const host=createBridgeHost({
  localWindow:w.host,
  frameWindow:w.child,
  binding,
  presentation,
  capabilities:granted?["storage.files"]:[],
  onObjectRequest,
 });
 const client=createBridgeClient({
  localWindow:w.child,
  parentWindow:w.host,
  onPresentation:()=>{},
 });
 cleanup.push(()=>client.close(),()=>host.close());
 await vi.waitFor(()=>expect(host.state()).toBe("ready"));
 return {...w,host,client};
}
test("object channel preserves closed create/list/get/update/delete results",async()=>{
 const dispatch=vi.fn(async(request:{action:string})=>{
  switch(request.action){
   case "list":return {action:"list" as const,items:[objectSummary],nextCursor:"2:00000000-0000-4000-8000-000000000042"};
   case "get":return {action:"get" as const,object:objectSummary,contentBase64:"aGVsbG8="};
   case "delete":return {action:"delete" as const,id:objectSummary.id};
   default:return {action:request.action as "create"|"update",object:objectSummary};
  }
 });
 const {client}=await objectsPair(dispatch);
 const create={action:"create" as const,requestId:"create-1",collection:"note",label:"First note",mediaType:"application/json",contentBase64:"aGVsbG8="};
 await expect(client.requestObjects(create)).resolves.toEqual({action:"create",object:objectSummary});
 await expect(client.requestObjects({action:"list",collection:"note",limit:20})).resolves.toMatchObject({action:"list",items:[objectSummary]});
 await expect(client.requestObjects({action:"get",id:objectSummary.id})).resolves.toEqual({action:"get",object:objectSummary,contentBase64:"aGVsbG8="});
 await expect(client.requestObjects({action:"update",id:objectSummary.id,expectedRevision:1,label:"First note",mediaType:"application/json",contentBase64:"aGVsbG8="})).resolves.toEqual({action:"update",object:objectSummary});
 await expect(client.requestObjects({action:"delete",id:objectSummary.id,expectedRevision:1})).resolves.toEqual({action:"delete",id:objectSummary.id});
 expect(dispatch).toHaveBeenCalledTimes(5);
});
test("object channel fails closed without grant, transport or valid input",async()=>{
 const dispatch=vi.fn(async()=>({action:"list" as const,items:[]}));
 const absent=await objectsPair(dispatch,false);
 await expect(absent.client.requestObjects({action:"list",collection:"note"})).rejects.toMatchObject({code:"unsupported",retryable:false});
 const unsupported=await objectsPair();
 await expect(unsupported.client.requestObjects({action:"list",collection:"note"})).rejects.toMatchObject({code:"unsupported"});
 const ready=await objectsPair(dispatch);
 for(const request of [
  {action:"list",collection:"Note"},
  {action:"get",id:"bad"},
  {action:"create",requestId:"create-1",collection:"note",label:"First\nnote",mediaType:"application/json",contentBase64:"aGVsbG8="},
  {action:"delete",id:objectSummary.id,expectedRevision:0},
  {action:"list",collection:"note",extra:true},
 ]) await expect(ready.client.requestObjects(request as never)).rejects.toMatchObject({code:"invalid_request"});
 expect(dispatch).not.toHaveBeenCalled();
});
test("object conflicts and storage full stay stable; close aborts work",async()=>{
 let signal:AbortSignal|undefined;
 const dispatch=vi.fn((_request:unknown,abort:AbortSignal)=>{
  signal=abort;
  return new Promise<never>(()=>{});
 });
 const {client,host}=await objectsPair(dispatch);
 const pending=client.requestObjects({action:"create",requestId:"create-1",collection:"note",label:"First note",mediaType:"application/json",contentBase64:""});
 const rejected=expect(pending).rejects.toMatchObject({code:"uncertain",retryable:false});
 await vi.waitFor(()=>expect(dispatch).toHaveBeenCalledOnce());
 host.close();
 await vi.waitFor(()=>expect(signal?.aborted).toBe(true));
 client.close();
 await rejected;
 const stable=await objectsPair(vi.fn()
  .mockRejectedValueOnce(new BridgeObjectError("conflict"))
  .mockRejectedValueOnce(new BridgeObjectError("storage_full")));
 for(const code of ["conflict","storage_full"]){
  await expect(stable.client.requestObjects({action:"list",collection:"note"})).rejects.toMatchObject({code,retryable:false});
 }
});
test("reviewed network channel preserves binary response and remote auth status",async()=>{
 const dispatch=vi.fn(async(_request:unknown)=>({status:401,contentType:"application/octet-stream",bodyBase64:"AP+A"}));
 const {client}=await networkPair(dispatch);
 await expect(client.requestNetwork(networkRequest())).resolves.toEqual({status:401,contentType:"application/octet-stream",bodyBase64:"AP+A"});
 expect(dispatch).toHaveBeenCalledOnce();expect(dispatch.mock.calls[0][0]).toEqual(networkRequest());
});
test("network channel refuses ungranted, unsupported and malformed requests",async()=>{
 const dispatch=vi.fn(async()=>({status:200,contentType:"",bodyBase64:""}));
 const absent=await networkPair(dispatch,false);
 await expect(absent.client.requestNetwork(networkRequest())).rejects.toMatchObject({code:"unsupported",retryable:false});
 const unsupported=await networkPair();
 await expect(unsupported.client.requestNetwork(networkRequest())).rejects.toMatchObject({code:"unsupported",retryable:false});
 const ready=await networkPair(dispatch);
 await expect(ready.client.requestNetwork({...networkRequest(),headers:{}} as never)).rejects.toMatchObject({code:"invalid_request"});
 expect(dispatch).not.toHaveBeenCalled();
});
test("network cancellation and disposal never retry a possibly executed request",async()=>{
 let signal:AbortSignal|undefined;
 const dispatch=vi.fn((_request:unknown,s:AbortSignal)=>{signal=s;return new Promise<never>(()=>{});});
 const {client}=await networkPair(dispatch);
 const abort=new AbortController();const pending=client.requestNetwork(networkRequest(),abort.signal);
 await vi.waitFor(()=>expect(dispatch).toHaveBeenCalledOnce());abort.abort();
 await expect(pending).rejects.toMatchObject({code:"uncertain",retryable:false});
 await vi.waitFor(()=>expect(signal?.aborted).toBe(true));
 expect(dispatch).toHaveBeenCalledOnce();
 const next=client.requestNetwork(networkRequest());await vi.waitFor(()=>expect(dispatch).toHaveBeenCalledTimes(2));
 client.close();await expect(next).rejects.toMatchObject({code:"uncertain",retryable:false});
});
test("network failures are closed and pending work has a fixed ceiling",async()=>{
 const dispatch=vi.fn(async()=>{throw new Error("remote private detail");});const one=await networkPair(dispatch);
 await expect(one.client.requestNetwork(networkRequest())).rejects.toMatchObject({code:"uncertain",retryable:false});expect(dispatch).toHaveBeenCalledOnce();
 const held=await networkPair(()=>new Promise<never>(()=>{}));
 const first=held.client.requestNetwork(networkRequest()),second=held.client.requestNetwork(networkRequest());
 await expect(held.client.requestNetwork(networkRequest())).rejects.toMatchObject({code:"busy"});
 held.client.close();await expect(first).rejects.toMatchObject({code:"uncertain"});await expect(second).rejects.toMatchObject({code:"uncertain"});
});
test("credential binding name crosses only with secrets.use and no credential value",async()=>{
 const dispatch=vi.fn(async(request:{credential?:string})=>({status:200,contentType:"application/json",bodyBase64:"e30="}));
 const without=await networkPair(dispatch);
 await expect(without.client.requestNetwork({...networkRequest(),credential:"upstream-api"})).rejects.toMatchObject({code:"unsupported"});
 const withUse=await networkPair(dispatch,true,["secrets.use"]);
 await expect(withUse.client.requestNetwork({...networkRequest(),credential:"upstream-api"})).resolves.toEqual({status:200,contentType:"application/json",bodyBase64:"e30="});
 expect(dispatch).toHaveBeenCalledOnce();
 expect(dispatch.mock.calls[0][0]).toMatchObject({credential:"upstream-api"});
 expect(JSON.stringify(dispatch.mock.calls[0][0])).not.toContain("secret-value");
});
test("a signing request crosses only with secrets.use, as a name and a reviewed action, and never on a stream",async()=>{
 const signing={credential:"qwen-billing",scheme:"acs3" as const,action:"QueryAccountBalance",version:"2017-12-14"};
 const dispatch=vi.fn(async(_request:unknown)=>({status:200,contentType:"application/json",bodyBase64:"e30="}));
 const without=await networkPair(dispatch);
 await expect(without.client.requestNetwork({...networkRequest(),signing})).rejects.toMatchObject({code:"unsupported"});
 expect(dispatch).not.toHaveBeenCalled();
 const withUse=await networkPair(dispatch,true,["secrets.use"]);
 await expect(withUse.client.requestNetwork({...networkRequest(),signing})).resolves.toEqual({status:200,contentType:"application/json",bodyBase64:"e30="});
 expect(dispatch.mock.calls[0][0]).toMatchObject({signing});
 const stream=vi.fn(async()=>{});
 const streams=await networkStreamPair(stream,true,["secrets.use"]);
 await expect(streams.client.requestNetworkStream({...networkRequest(),signing})).rejects.toMatchObject({code:"unsupported"});
 expect(stream).not.toHaveBeenCalled();
});
test("network and own-backend channels keep independent request identities and results",async()=>{
 vi.stubGlobal("MessageChannel",NodeMessageChannel);
 const w=windows();
 const backend=vi.fn(async()=>({status:200,body:'{"channel":"backend"}'}));
 const network=vi.fn(async()=>({status:200,contentType:"application/json",bodyBase64:"eyJjaGFubmVsIjoibmV0d29yayJ9"}));
 const host=createBridgeHost({localWindow:w.host,frameWindow:w.child,binding,presentation,capabilities:["network.http"],onBackendRequest:backend,onNetworkRequest:network});
 const client=createBridgeClient({localWindow:w.child,parentWindow:w.host,onPresentation:()=>{}});
 cleanup.push(()=>client.close(),()=>host.close());
 await vi.waitFor(()=>expect(host.state()).toBe("ready"));
 const [backendResult,networkResult]=await Promise.all([
  client.requestBackend({method:"GET",path:"/v1/echo",body:""}),
  client.requestNetwork(networkRequest()),
 ]);
 expect(backendResult).toEqual({status:200,body:'{"channel":"backend"}'});
 expect(networkResult).toEqual({status:200,contentType:"application/json",bodyBase64:"eyJjaGFubmVsIjoibmV0d29yayJ9"});
 expect(backend).toHaveBeenCalledOnce();expect(network).toHaveBeenCalledOnce();
});
test("network results with extra fields are rejected as uncertain",async()=>{
 const dispatch=vi.fn(async()=>({status:200,contentType:"",bodyBase64:"",headers:{cookie:"private"}}));
 const {client}=await networkPair(dispatch);
 await expect(client.requestNetwork(networkRequest())).rejects.toMatchObject({code:"uncertain",retryable:false});
 expect(dispatch).toHaveBeenCalledOnce();
});
test("network deadline reports uncertainty once and ignores a late response",async()=>{
 let resolve!: (value:{status:number;contentType:string;bodyBase64:string})=>void;
 const dispatch=vi.fn(()=>new Promise<{status:number;contentType:string;bodyBase64:string}>(done=>{resolve=done;}));
 const {client}=await networkPair(dispatch);
 vi.useFakeTimers();
 const pending=client.requestNetwork(networkRequest());
 const rejected=expect(pending).rejects.toMatchObject({code:"uncertain",retryable:false});
 await vi.advanceTimersByTimeAsync(0);
 expect(dispatch).toHaveBeenCalledOnce();
 await vi.advanceTimersByTimeAsync(30_000);
 await rejected;
 resolve({status:200,contentType:"",bodyBase64:""});
 await vi.advanceTimersByTimeAsync(10_000);
 expect(dispatch).toHaveBeenCalledOnce();
});
test("network stream preserves order and waits for application backpressure",async()=>{
 let emitChunk!: () => Promise<void>;
 const chunkEntered = new Promise<void>((resolve) => {
  emitChunk = async () => { resolve(); };
 });
 let release!: () => void;
 const blocked = new Promise<void>((resolve) => { release = resolve; });
 const hostRequest = vi.fn(async (
  _request,
  _signal,
  emit: Parameters<NonNullable<Parameters<typeof createBridgeHost>[0]["onNetworkStreamRequest"]>>[2],
 ) => {
  await emit({type:"start",status:206,contentType:"application/x-ndjson"});
  await emit({type:"chunk",sequence:1,bodyBase64:"AQID"});
  await emit({type:"end",sequence:1,bytes:3,sha256:"a".repeat(64)});
 });
 const pair=await networkStreamPair(hostRequest);
 const {client}=pair;
 let chunkStarted = false;
 const pending=client.requestNetworkStream(
  {...networkRequest(),accept:"application/x-ndjson"},
  {
   onStart:(status,contentType)=>{
    expect([status,contentType]).toEqual([206,"application/x-ndjson"]);
   },
   onChunk:async(data,sequence)=>{
    chunkStarted=true;
    expect([...data,sequence]).toEqual([1,2,3,1]);
    await emitChunk();
    await blocked;
   },
  },
 );
 await chunkEntered;
 expect(chunkStarted).toBe(true);
 release();
 await expect(pending).resolves.toEqual({
  status:206,contentType:"application/x-ndjson",bytes:3,frames:1,
 });
 expect(hostRequest).toHaveBeenCalledOnce();
});
test("network stream fails closed without grant, transport, or a terminating frame",async()=>{
 const absent=await networkStreamPair(vi.fn(),false);
 await expect(absent.client.requestNetworkStream(networkRequest())).rejects.toMatchObject({code:"unsupported",retryable:false});
 const unsupported=await networkStreamPair();
 await expect(unsupported.client.requestNetworkStream(networkRequest())).rejects.toMatchObject({code:"unsupported"});
 const missing=await networkStreamPair(async(_request,_signal,emit)=>{
  await emit({type:"start",status:200,contentType:"text/event-stream"});
 });
 await expect(missing.client.requestNetworkStream({...networkRequest(),accept:"text/event-stream"})).rejects.toMatchObject({code:"uncertain",retryable:false});
});
test("network stream cancellation never retries a possibly executed stream",async()=>{
 let signal:AbortSignal|undefined;
 const dispatch=vi.fn(async(
  _request,
  current:AbortSignal,
  emit: Parameters<NonNullable<Parameters<typeof createBridgeHost>[0]["onNetworkStreamRequest"]>>[2],
 )=>{
  signal=current;
  await emit({type:"start",status:200,contentType:"text/event-stream"});
  await new Promise<void>((_resolve,reject)=>{
   current.addEventListener("abort",()=>reject(new AppNetworkError("uncertain")),{once:true});
  });
 });
 const {client}=await networkStreamPair(dispatch);
 const abort=new AbortController();
 const pending=client.requestNetworkStream(
  {...networkRequest(),accept:"text/event-stream"},
  {signal:abort.signal},
 );
 await vi.waitFor(()=>expect(dispatch).toHaveBeenCalledOnce());
 abort.abort();
 await expect(pending).rejects.toMatchObject({code:"uncertain",retryable:false});
 await vi.waitFor(()=>expect(signal?.aborted).toBe(true));
 expect(dispatch).toHaveBeenCalledOnce();
});
test("network stream with secrets.use forwards only the binding name",async()=>{
 const dispatch=vi.fn(async(
  _request:unknown,
  _signal:AbortSignal,
  emit: Parameters<NonNullable<Parameters<typeof createBridgeHost>[0]["onNetworkStreamRequest"]>>[2],
 )=>{
  await emit({type:"start",status:200,contentType:"text/event-stream"});
  await emit({type:"end",sequence:0,bytes:0,sha256:"a".repeat(64)});
 });
 const without=await networkStreamPair(dispatch);
 await expect(without.client.requestNetworkStream({...networkRequest(),credential:"upstream-api"})).rejects.toMatchObject({code:"unsupported"});
 const withUse=await networkStreamPair(dispatch,true,["secrets.use"]);
 await expect(withUse.client.requestNetworkStream({...networkRequest(),credential:"upstream-api"})).resolves.toMatchObject({bytes:0,frames:0});
 expect(dispatch.mock.calls[0][0]).toMatchObject({credential:"upstream-api"});
 expect(JSON.stringify(dispatch.mock.calls[0][0])).not.toContain("credentialValue");
});

const target: NetworkTargetRequest = {
  id: "api",
  origin: "https://api.example.com",
  paths: ["/v1/*"],
  methods: ["GET"],
  network: "public",
};
async function targetsPair(
  onNetworkTargetsRequest?: Parameters<typeof createBridgeHost>[0]["onNetworkTargetsRequest"],
  granted = true,
  onNetworkTargetsApplied?: Parameters<typeof createBridgeHost>[0]["onNetworkTargetsApplied"],
) {
  vi.stubGlobal("MessageChannel", NodeMessageChannel);
  const w = windows();
  const host = createBridgeHost({
    localWindow: w.host,
    frameWindow: w.child,
    binding,
    presentation,
    capabilities: granted ? ["network.http"] : [],
    onNetworkTargetsRequest,
    onNetworkTargetsApplied,
  });
  const client = createBridgeClient({
    localWindow: w.child,
    parentWindow: w.host,
    onPresentation: () => {},
  });
  cleanup.push(
    () => client.close(),
    () => host.close(),
  );
  await vi.waitFor(() => expect(host.state()).toBe("ready"));
  return { ...w, host, client };
}

test("network targets: applied answers the app, then hands over to the relaunch hook", async () => {
  const order: string[] = [];
  const ask = vi.fn(async (_targets: readonly NetworkTargetRequest[]) => ({ state: "applied", policyRevision: "2" }) as const);
  const applied = vi.fn(() => {
    order.push("relaunch");
  });
  const { client } = await targetsPair(ask, true, applied);
  expect(BRIDGE_PROTOCOL).toBe("portal.app.bridge/2-dev.9");
  await expect(client.requestNetworkTargets([target])).resolves.toEqual({
    state: "applied",
    policyRevision: "2",
  });
  expect(ask).toHaveBeenCalledOnce();
  expect(ask.mock.calls[0][0]).toEqual([target]);
  expect(applied).toHaveBeenCalledExactlyOnceWith("2");
  expect(order).toEqual(["relaunch"]);
});

test("network targets: no callback or no network.http grant is not_permitted without any request", async () => {
  const ask = vi.fn(async () => ({ state: "applied" }) as const);
  const nothing = await targetsPair(undefined);
  await expect(nothing.client.requestNetworkTargets([target])).resolves.toEqual({ state: "not_permitted" });
  const ungranted = await targetsPair(ask, false);
  await expect(ungranted.client.requestNetworkTargets([target])).rejects.toMatchObject({ code: "unsupported" });
  expect(ask).not.toHaveBeenCalled();
});

test("network targets: a decline is final for ten seconds, then the dialog may open again", async () => {
  let now = 1_000_000;
  vi.spyOn(Date, "now").mockImplementation(() => now);
  const ask = vi
    .fn<NonNullable<Parameters<typeof createBridgeHost>[0]["onNetworkTargetsRequest"]>>()
    .mockResolvedValueOnce({ state: "declined" })
    .mockResolvedValue({ state: "applied", policyRevision: "3" });
  const { client } = await targetsPair(ask);
  await expect(client.requestNetworkTargets([target])).resolves.toEqual({ state: "declined" });
  now += BRIDGE_TARGETS_BACKOFF_MS - 1;
  await expect(client.requestNetworkTargets([target])).resolves.toEqual({ state: "declined" });
  expect(ask).toHaveBeenCalledOnce();
  now += 1;
  await expect(client.requestNetworkTargets([target])).resolves.toEqual({ state: "applied", policyRevision: "3" });
  expect(ask).toHaveBeenCalledTimes(2);
  vi.restoreAllMocks();
});

test("network targets: a failing dialog callback declines and only one request is pending", async () => {
  vi.spyOn(Date, "now").mockReturnValue(5_000_000);
  const { client } = await targetsPair(async () => {
    throw new Error("plan refused");
  });
  await expect(client.requestNetworkTargets([target])).resolves.toEqual({ state: "declined" });
  vi.restoreAllMocks();
  const held = await targetsPair(() => new Promise<never>(() => {}));
  const first = held.client.requestNetworkTargets([target]);
  await expect(held.client.requestNetworkTargets([target])).rejects.toMatchObject({ code: "busy" });
  first.catch(() => {});
});

test("network targets: closing the host aborts the dialog", async () => {
  let signal!: AbortSignal;
  const { client, host } = await targetsPair((_, value) => {
    signal = value;
    return new Promise<never>(() => {});
  });
  client.requestNetworkTargets([target]).catch(() => {});
  await vi.waitFor(() => expect(signal).toBeDefined());
  host.close();
  expect(signal.aborted).toBe(true);
});

test("network targets: the request shape is closed and bounded", async () => {
  const ask = vi.fn(async () => ({ state: "declined" }) as const);
  const { client } = await targetsPair(ask);
  const many = Array.from({ length: 17 }, (_, index) => ({ ...target, id: `t${index}` }));
  for (const bad of [
    [],
    many,
    [{ ...target, bounds: { requestBytes: 1 } }],
    [{ ...target, headers: {} }],
    [{ ...target, requestBytes: 0 }],
    [{ ...target, paths: "/v1/*" }],
    [{ id: "api" }],
  ])
    await expect(
      client.requestNetworkTargets(bad as unknown as NetworkTargetRequest[]),
    ).rejects.toMatchObject({ code: "invalid_request" });
  expect(ask).not.toHaveBeenCalled();
});

test("a 2-dev.8 app still completes the handshake and has no network.targets messages", async () => {
  const w = hostOnly();
  expect(w.sent.find((item) => item.data.type === "app:challenge")?.data.protocol).toBe(
    BRIDGE_PROTOCOL_PREVIOUS,
  );
  w.host.postMessage(
    { ...boundHello(w), protocols: [BRIDGE_PROTOCOL_PREVIOUS] },
    "https://portal.example.invalid",
  );
  await vi.waitFor(() =>
    expect(w.received.some((item) => item.to === 1 && item.event.data.type === "app:init")).toBe(true),
  );
  const init = w.received.find((item) => item.to === 1 && item.event.data.type === "app:init")!.event;
  expect(init.data.protocol).toBe(BRIDGE_PROTOCOL_PREVIOUS);
  init.ports[0].postMessage(acknowledgement(init.data));
  await vi.waitFor(() => expect(w.ready).toHaveBeenCalledOnce());
  init.ports[0].postMessage({
    ...Object.fromEntries(
      Object.entries(init.data).filter(
        ([key]) => !["type", "presentation", "capabilities", "backend", "revision"].includes(key),
      ),
    ),
    type: "network.targets.request",
    id: "1",
    targets: [target],
  });
  await vi.waitFor(() => expect(w.failed).toHaveBeenCalledExactlyOnceWith("protocol_error"));
});

test("a host that offers only an unknown protocol never answers", () => {
  const w = hostOnly();
  w.host.postMessage(
    { ...boundHello(w), protocols: ["portal.app.bridge/2-dev.7"] },
    "https://portal.example.invalid",
  );
  expect(w.sent.filter((item) => item.data.type === "app:init")).toHaveLength(0);
});

test("a new client offers both protocols and, against a 2-dev.8 host, has no network.targets", async () => {
  const w = clientOnly();
  expect(w.sent[0].data.protocols).toEqual([BRIDGE_PROTOCOL, BRIDGE_PROTOCOL_PREVIOUS]);
  w.deliver({ ...w.init, protocol: BRIDGE_PROTOCOL_PREVIOUS, capabilities: ["network.http"] });
  await vi.waitFor(() => expect(w.client.state()).toBe("ready"));
  await expect(w.client.requestNetworkTargets([target])).rejects.toMatchObject({ code: "unsupported" });
});

describe("derived parent origin", () => {
  test("client derives the parent origin from its own document URL", async () => {
    vi.stubGlobal("MessageChannel", NodeMessageChannel);
    const w = windows(
      "http://127.0.0.1:31935/app-documents/y/index.html?x=1#h",
      "http://127.0.0.1:31935",
    );
    const client = createBridgeClient({
      localWindow: w.child,
      parentWindow: w.host,
      onPresentation: () => {},
    });
    cleanup.push(() => client.close());
    expect(client.state()).not.toBe("failed");
    await vi.waitFor(() =>
      expect(w.sent.some((i) => i.data.type === "app:hello")).toBe(true),
    );
  });

  for (const href of [
    "about:srcdoc",
    "data:text/html,x",
    "null",
    "blob:null/abc",
    "",
  ]) {
    test(`opaque or non-http document URL ${JSON.stringify(href)} fails immediately`, () => {
      vi.stubGlobal("MessageChannel", NodeMessageChannel);
      const w = windows(href);
      const failed = vi.fn();
      const client = createBridgeClient({
        localWindow: w.child,
        parentWindow: w.host,
        onPresentation: () => {},
        onFailure: failed,
      });
      cleanup.push(() => client.close());
      expect(client.state()).toBe("failed");
      expect(failed).toHaveBeenCalledExactlyOnceWith("invalid_context");
      expect(w.sent).toHaveLength(0);
    });
  }

  test("a valid challenge from a different origin is ignored, from the right origin it is answered", async () => {
    vi.stubGlobal("MessageChannel", NodeMessageChannel);
    const w = windows();
    const client = createBridgeClient({
      localWindow: w.child,
      parentWindow: w.host,
      onPresentation: () => {},
    });
    cleanup.push(() => client.close());
    const hostNonce = "c".repeat(32);
    const challenge = {
      type: "app:challenge",
      protocol: BRIDGE_PROTOCOL,
      hostNonce,
    };
    const answered = () =>
      w.sent.some(
        (item) =>
          item.from === 1 &&
          item.data.type === "app:hello" &&
          item.data.hostNonce === hostNonce,
      );
    const send = (origin: string) => {
      for (const listener of w.listeners[1])
        listener({
          data: structuredClone(challenge),
          origin,
          source: w.host,
          ports: [],
        } as unknown as MessageEvent);
    };
    send("https://evil.example.invalid");
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(answered()).toBe(false);
    expect(client.state()).not.toBe("failed");
    send("https://portal.example.invalid");
    await vi.waitFor(() => expect(answered()).toBe(true));
  });
});

async function surfacePair(surface?: "reader" | "admin", extra: Record<string, unknown> = {}) {
  vi.stubGlobal("MessageChannel", NodeMessageChannel);
  const w = windows();
  const host = createBridgeHost({
    localWindow: w.host,
    frameWindow: w.child,
    binding,
    presentation: { ...presentation, ...extra } as typeof presentation,
    capabilities: [],
    ...(surface ? { surface } : {}),
  });
  const client = createBridgeClient({ localWindow: w.child, parentWindow: w.host, onPresentation: () => {} });
  cleanup.push(() => client.close(), () => host.close());
  await vi.waitFor(() => expect(host.state()).toBe("ready"));
  return { ...w, client };
}

test("the bridge context tells the app which surface it serves", async () => {
  const reader = await surfacePair();
  expect(reader.client.context()?.surface).toBe("reader");
  // A reader's init message stays byte-compatible with an SDK that predates admin pages.
  expect(reader.sent.find((item) => item.data.type === "app:init")!.data).not.toHaveProperty("surface");
  const admin = await surfacePair("admin");
  expect(admin.client.context()?.surface).toBe("admin");
  expect(admin.sent.find((item) => item.data.type === "app:init")!.data.surface).toBe("admin");
});

test("a presentation may carry an admin route registry that has no root route", async () => {
  const navigation = {
    path: "/settings",
    search: "",
    routes: [{ id: "settings", path: "/settings", layout: "document" }],
  };
  expect(parseBridgePresentation({ ...presentation, navigation }, "admin")).not.toBeNull();
  expect(parseBridgePresentation({ ...presentation, navigation: { ...navigation, path: "/other" } }, "admin")).toBeNull();
  const admin = await surfacePair("admin", { navigation });
  expect(admin.client.presentation()?.navigation?.path).toBe("/settings");
});

test("a reader registry needs the root route and an admin one does not", async () => {
  const rootless = { path: "/settings", search: "", routes: [{ id: "settings", path: "/settings", layout: "document" }] };
  const rooted = { path: "/", search: "", routes: [{ id: "home", path: "/", layout: "document" }] };
  // The surface defaults to the reader, which always has a root route.
  expect(parseBridgePresentation({ ...presentation, navigation: rootless })).toBeNull();
  expect(parseBridgePresentation({ ...presentation, navigation: rootless }, "reader")).toBeNull();
  expect(parseBridgePresentation({ ...presentation, navigation: rooted })).not.toBeNull();
  expect(parseBridgePresentation({ ...presentation, navigation: rooted }, "reader")).not.toBeNull();
  expect(parseBridgePresentation({ ...presentation, navigation: rootless }, "admin")).not.toBeNull();
  // An admin registry may still carry a root route; it is just not required.
  expect(parseBridgePresentation({ ...presentation, navigation: rooted }, "admin")).not.toBeNull();
  // The client applies the surface its init announced to the presentation in the same message.
  const reader = clientOnly();
  reader.deliver({ ...reader.init, presentation: { ...presentation, navigation: rootless } });
  expect(reader.client.state()).toBe("failed");
  const admin = clientOnly();
  admin.deliver({ ...admin.init, surface: "admin", presentation: { ...presentation, navigation: rootless } });
  await vi.waitFor(() => expect(admin.client.state()).toBe("ready"));
  const explicit = clientOnly();
  explicit.deliver({ ...explicit.init, surface: "reader", presentation: { ...presentation, navigation: rootless } });
  expect(explicit.client.state()).toBe("failed");
});

for (const surface of ["x", null, 1, "Admin", ""]) {
  test(`init refuses the surface ${JSON.stringify(surface)}`, () => {
    const w = clientOnly();
    w.deliver({ ...w.init, surface });
    expect(w.client.state()).toBe("failed");
    expect(w.failed).toHaveBeenCalledExactlyOnceWith("protocol_error");
    expect(w.apply).not.toHaveBeenCalled();
  });
}

for (const [given, expected] of [["reader", "reader"], ["admin", "admin"], [undefined, "reader"]] as const) {
  test(`init surface ${given === undefined ? "absent" : given} is the ${expected} surface`, async () => {
    const w = clientOnly();
    w.deliver(given === undefined ? w.init : { ...w.init, surface: given });
    await vi.waitFor(() => expect(w.client.state()).toBe("ready"));
    expect(w.client.context()?.surface).toBe(expected);
  });
}
