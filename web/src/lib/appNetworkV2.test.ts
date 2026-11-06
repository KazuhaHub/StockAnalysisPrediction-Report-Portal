import { afterEach, expect, test, vi } from "vitest";
import { onSessionLost } from "../api/client";
import {
  createAppNetworkStreamTransport,
  createAppNetworkTransport,
} from "./appNetworkV2";

const binding = {
  installationId: "00000000-0000-0000-0000-000000000001",
  generation: "9007199254740993",
  policyRevision: "1",
  releaseSha256: "a".repeat(64),
};
const request = {
  method: "POST",
  url: "https://api.example.com/v1/check?q=%E4%B8%AD",
  accept: "application/json",
  contentType: "application/octet-stream",
  bodyBase64: "AP+A",
};

afterEach(() => {
  vi.unstubAllGlobals();
  onSessionLost(null);
});

test("parent network transport fixes the installation destination and preserves binary results", async () => {
  const fetch = vi.fn().mockResolvedValue(
    new Response(
      JSON.stringify({
        status: 401,
        contentType: "application/octet-stream",
        bodyBase64: "AP+A",
      }),
    ),
  );
  vi.stubGlobal("fetch", fetch);
  const transport = createAppNetworkTransport(binding);
  binding.policyRevision = "2";
  try {
    const signal = new AbortController().signal;
    await expect(transport(request, signal)).resolves.toEqual({
      status: 401,
      contentType: "application/octet-stream",
      bodyBase64: "AP+A",
    });
    const [url, options] = fetch.mock.calls[0];
    expect(url).toBe(
      `/api/app-platform/installations/${binding.installationId}/network`,
    );
    expect(options).toMatchObject({
      method: "POST",
      credentials: "same-origin",
      redirect: "error",
      cache: "no-store",
      signal,
      headers: { "Content-Type": "application/json" },
    });
    expect(JSON.parse(options.body)).toEqual({
      binding: {
        generation: binding.generation,
        policyRevision: "1",
        releaseSha256: binding.releaseSha256,
      },
      ...request,
    });
  } finally {
    binding.policyRevision = "1";
  }
});

test("parent transport forwards only the reviewed binding name", async () => {
  const fetch = vi.fn().mockResolvedValue(
    new Response(
      JSON.stringify({
        status: 200,
        contentType: "application/json",
        bodyBase64: "e30=",
      }),
    ),
  );
  vi.stubGlobal("fetch", fetch);
  await createAppNetworkTransport(binding)(
    { ...request, credential: "upstream-api" },
    new AbortController().signal,
  );
  const body = JSON.parse(fetch.mock.calls[0][1].body);
  expect(body.credential).toBe("upstream-api");
  expect(JSON.stringify(body)).not.toContain("credentialValue");
  expect(JSON.stringify(body)).not.toContain("Authorization");
});

test("remote authentication status stays inside the response envelope", async () => {
  const lost = vi.fn();
  onSessionLost(lost);
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          status: 401,
          contentType: "application/json",
          bodyBase64: "eyJlcnJvciI6InJlbW90ZSJ9",
        }),
      ),
    ),
  );
  await expect(
    createAppNetworkTransport(binding)(request, new AbortController().signal),
  ).resolves.toMatchObject({ status: 401 });
  expect(lost).not.toHaveBeenCalled();
});

test("host failures use fixed non-retryable outcomes and never expose details", async () => {
  const fetch = vi
    .fn()
    .mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          code: "app_platform_request_uncertain",
          message: "private upstream detail",
          retryable: false,
        }),
        { status: 502 },
      ),
    )
    .mockRejectedValueOnce(new Error("private socket"))
    .mockResolvedValueOnce(
      new Response(JSON.stringify({ code: "app_platform_busy" }), {
        status: 429,
      }),
    )
    .mockResolvedValueOnce(
      new Response(JSON.stringify({ code: "app_platform_runtime_unavailable" }), {
        status: 503,
      }),
    )
    .mockResolvedValueOnce(
      new Response(JSON.stringify({ code: "app_platform_invalid_request" }), {
        status: 400,
      }),
    );
  vi.stubGlobal("fetch", fetch);
  const transport = createAppNetworkTransport(binding);
  for (const code of [
    "uncertain",
    "uncertain",
    "busy",
    "unavailable",
    "invalid_request",
  ]) {
    await expect(
      transport(request, new AbortController().signal),
    ).rejects.toMatchObject({ code, retryable: false });
  }
  expect(fetch).toHaveBeenCalledTimes(5);
});

test("malformed or oversized envelopes are uncertain and bounded", async () => {
  const fetch = vi
    .fn()
    .mockResolvedValueOnce(new Response("not JSON"))
    .mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          status: 200,
          contentType: "",
          bodyBase64: "",
          headers: { cookie: "private" },
        }),
      ),
    )
    .mockResolvedValueOnce(new Response("x".repeat(2 * 1048576 + 1)));
  vi.stubGlobal("fetch", fetch);
  const transport = createAppNetworkTransport(binding);
  for (let index = 0; index < 3; index++) {
    await expect(
      transport(request, new AbortController().signal),
    ).rejects.toMatchObject({ code: "uncertain", retryable: false });
  }
});

test("pre-aborted or malformed requests never fetch", async () => {
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  const transport = createAppNetworkTransport(binding);
  const abort = new AbortController();
  abort.abort();
  await expect(transport(request, abort.signal)).rejects.toMatchObject({
    code: "not_ready",
  });
  await expect(
    transport(
      { ...request, headers: {} } as never,
      new AbortController().signal,
    ),
  ).rejects.toMatchObject({ code: "invalid_request" });
  expect(fetch).not.toHaveBeenCalled();
});

function streamResponse(lines: unknown[]) {
  const body = lines.map((line) => `${JSON.stringify(line)}\n`).join("");
  return new Response(body, {
    headers: { "Content-Type": "application/x-ndjson; charset=utf-8" },
  });
}

async function digest(bytes: Uint8Array) {
  const copy = new Uint8Array(bytes.byteLength);
  copy.set(bytes);
  const value = new Uint8Array(
    await globalThis.crypto.subtle.digest("SHA-256", copy.buffer),
  );
  return Array.from(value, (part) => part.toString(16).padStart(2, "0")).join(
    "",
  );
}

test("stream transport forwards ordered chunks and verifies the end digest", async () => {
  const bytes = new Uint8Array([0, 255, 128, 10]);
  const bodyBase64 = btoa(String.fromCharCode(...bytes));
  const response = streamResponse([
    { type: "start", status: 206, contentType: "application/x-ndjson" },
    { type: "chunk", sequence: 1, bodyBase64 },
    {
      type: "end",
      sequence: 1,
      bytes: bytes.byteLength,
      sha256: await digest(bytes),
    },
  ]);
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response));
  const events: string[] = [];
  await expect(
    createAppNetworkStreamTransport(binding)(
      request,
      new AbortController().signal,
      async (event) => {
        events.push(event.type);
      },
    ),
  ).resolves.toEqual({
    sequence: 1,
    bytes: bytes.byteLength,
    sha256: await digest(bytes),
  });
  expect(events).toEqual(["start", "chunk", "end"]);
});

test("stream transport waits for consumer backpressure before completing", async () => {
  const bytes = new Uint8Array([1, 2, 3]);
  const bodyBase64 = btoa(String.fromCharCode(...bytes));
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(
      streamResponse([
        { type: "start", status: 200, contentType: "text/event-stream" },
        { type: "chunk", sequence: 1, bodyBase64 },
        {
          type: "end",
          sequence: 1,
          bytes: bytes.byteLength,
          sha256: await digest(bytes),
        },
      ]),
    ),
  );
  let release!: () => void;
  const blocked = new Promise<void>((resolve) => {
    release = resolve;
  });
  let settled = false;
  let chunkStarted = false;
  const running = createAppNetworkStreamTransport(binding)(
    { ...request, accept: "text/event-stream" },
    new AbortController().signal,
    async (event) => {
      if (event.type === "chunk") {
        chunkStarted = true;
        await blocked;
      }
    },
  ).finally(() => {
    settled = true;
  });
  await vi.waitFor(() => expect(chunkStarted).toBe(true));
  expect(settled).toBe(false);
  release();
  await expect(running).resolves.toMatchObject({ bytes: bytes.byteLength });
});

test("stream transport refuses missing termination, duplicate frames and digest drift", async () => {
  const bytes = new Uint8Array([1]);
  const bodyBase64 = btoa(String.fromCharCode(...bytes));
  const good = await digest(bytes);
  const cases = [
    [
      { type: "start", status: 200, contentType: "text/event-stream" },
      { type: "chunk", sequence: 1, bodyBase64 },
    ],
    [
      { type: "start", status: 200, contentType: "text/event-stream" },
      { type: "chunk", sequence: 1, bodyBase64 },
      { type: "chunk", sequence: 1, bodyBase64 },
      { type: "end", sequence: 1, bytes: 1, sha256: good },
    ],
    [
      { type: "start", status: 200, contentType: "text/event-stream" },
      { type: "chunk", sequence: 1, bodyBase64 },
      { type: "end", sequence: 1, bytes: 1, sha256: "0".repeat(64) },
    ],
  ];
  const fetch = vi.fn();
  for (const lines of cases) fetch.mockResolvedValueOnce(streamResponse(lines));
  vi.stubGlobal("fetch", fetch);
  for (const lines of cases) {
    await expect(
      createAppNetworkStreamTransport(binding)(
        { ...request, accept: "text/event-stream" },
        new AbortController().signal,
        async () => {},
      ),
    ).rejects.toMatchObject({ code: "uncertain", retryable: false });
  }
});

test("stream transport maps the closed unavailable document without JSON", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(
      new Response("application unavailable\n", { status: 404 }),
    ),
  );
  await expect(
    createAppNetworkStreamTransport(binding)(
      { ...request, accept: "text/event-stream" },
      new AbortController().signal,
      async () => {},
    ),
  ).rejects.toMatchObject({ code: "unavailable", retryable: false });
});
