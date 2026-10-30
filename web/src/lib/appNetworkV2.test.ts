import { afterEach, expect, test, vi } from "vitest";
import { onSessionLost } from "../api/client";
import { createAppNetworkTransport } from "./appNetworkV2";

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
