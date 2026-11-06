import { afterEach, expect, test, vi } from "vitest";
import { createAppBackendTransport } from "./appBackendV2";
import { onSessionLost } from "../api/client";
const binding = {
  installationId: "00000000-0000-0000-0000-000000000001",
  generation: "9007199254740993",
  policyRevision: "1",
  releaseSha256: "a".repeat(64),
};
const request = {
  method: "POST",
  path: "/v1/echo",
  body: '{ "value": "中文" }',
};
afterEach(() => {
  vi.unstubAllGlobals();
  onSessionLost(null);
});
test("parent transport fixes installation destination and preserves request/response bytes", async () => {
  const fetch = vi
    .fn()
    .mockResolvedValue(
      new Response(JSON.stringify({ status: 403, body: '{ "denied": true }' })),
    );
  vi.stubGlobal("fetch", fetch);
  const transport = createAppBackendTransport(binding);
  binding.policyRevision = "2";
  try {
    const signal = new AbortController().signal;
    await expect(transport(request, signal)).resolves.toEqual({
      status: 403,
      body: '{ "denied": true }',
    });
    expect(fetch).toHaveBeenCalledOnce();
    const [url, options] = fetch.mock.calls[0];
    expect(url).toBe(
      `/api/app-platform/installations/${binding.installationId}/backend`,
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
test("preflight refusal and transport ambiguity remain distinct and never retry", async () => {
  const fetch = vi
    .fn()
    .mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          code: "app_platform_request_uncertain",
          retryable: false,
        }),
        { status: 502 },
      ),
    )
    .mockRejectedValueOnce(new Error("private socket"))
    .mockResolvedValueOnce(
      new Response(
        JSON.stringify({ code: "app_platform_runtime_unavailable" }),
        { status: 503 },
      ),
    );
  vi.stubGlobal("fetch", fetch);
  const transport = createAppBackendTransport(binding);
  for (const code of ["uncertain", "uncertain", "unavailable"])
    await expect(
      transport(request, new AbortController().signal),
    ).rejects.toMatchObject({ code, retryable: false });
  expect(fetch).toHaveBeenCalledTimes(3);
});
test("malformed or oversized app output is uncertain and bounded", async () => {
  const fetch = vi
    .fn()
    .mockResolvedValueOnce(new Response("not JSON"))
    .mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          status: 200,
          body: "{}",
          headers: { cookie: "private" },
        }),
      ),
    )
    .mockResolvedValueOnce(new Response("x".repeat(6 * 1048576 + 1025)));
  vi.stubGlobal("fetch", fetch);
  const transport = createAppBackendTransport(binding);
  for (let i = 0; i < 3; i++)
    await expect(
      transport(request, new AbortController().signal),
    ).rejects.toMatchObject({ code: "uncertain" });
});
test("only a Portal session-expired response invokes the host session handler", async () => {
  const lost = vi.fn();
  onSessionLost(lost);
  const fetch = vi
    .fn()
    .mockResolvedValueOnce(
      new Response(
        JSON.stringify({ status: 403, body: '{"code":"session_expired"}' }),
      ),
    )
    .mockResolvedValueOnce(
      new Response(JSON.stringify({ code: "session_expired" }), {
        status: 401,
      }),
    );
  vi.stubGlobal("fetch", fetch);
  const transport = createAppBackendTransport(binding);
  await transport(request, new AbortController().signal);
  expect(lost).not.toHaveBeenCalled();
  await expect(
    transport(request, new AbortController().signal),
  ).rejects.toMatchObject({ code: "unavailable" });
  expect(lost).toHaveBeenCalledOnce();
});
test("pre-aborted and invalid requests do not fetch", async () => {
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  const transport = createAppBackendTransport(binding),
    abort = new AbortController();
  abort.abort();
  await expect(transport(request, abort.signal)).rejects.toMatchObject({
    code: "not_ready",
  });
  await expect(
    transport(
      { ...request, path: "https://evil.invalid/v1/echo" },
      new AbortController().signal,
    ),
  ).rejects.toMatchObject({ code: "invalid_request" });
  expect(fetch).not.toHaveBeenCalled();
});
