import { afterEach, expect, test, vi } from "vitest";
import { onSessionLost } from "../api/client";
import { BridgePrincipalError } from "./bridge-v2";
import { createAppPrincipalsTransport } from "./appPrincipalsV2";

const binding = {
  installationId: "00000000-0000-0000-0000-000000000001",
  generation: "9007199254740993",
  policyRevision: "1",
  releaseSha256: "a".repeat(64),
};
const userP = "user:" + "a".repeat(64);
const ouP = "ou:" + "b".repeat(64);
const search = { action: "search" as const, kind: "ou" as const, query: "res", after: "", limit: 20 };
const resolve = { action: "resolve" as const, principals: [userP, ouP] };
const boundBody = { generation: binding.generation, policyRevision: "1", releaseSha256: binding.releaseSha256 };

afterEach(() => {
  vi.unstubAllGlobals();
  onSessionLost(null);
});

const reply = (status: number, body: unknown) => new Response(JSON.stringify(body), { status });

test("principal transport posts the bound search and maps the page", async () => {
  const fetch = vi.fn().mockResolvedValue(
    reply(200, { items: [{ principal: ouP, label: "Research", parent: null }], next: "abc" }),
  );
  vi.stubGlobal("fetch", fetch);
  const transport = createAppPrincipalsTransport(binding);
  binding.policyRevision = "2";
  try {
    const signal = new AbortController().signal;
    await expect(transport(search, signal)).resolves.toEqual({
      action: "search",
      items: [{ principal: ouP, label: "Research", parent: null }],
      next: "abc",
    });
    const [url, options] = fetch.mock.calls[0];
    expect(url).toBe(`/api/app-platform/installations/${binding.installationId}/principals/search`);
    expect(options).toMatchObject({
      method: "POST",
      credentials: "same-origin",
      redirect: "error",
      cache: "no-store",
      signal,
      headers: { "Content-Type": "application/json" },
    });
    expect(JSON.parse(options.body)).toEqual({
      binding: boundBody,
      kind: "ou",
      query: "res",
      after: "",
      limit: 20,
    });
  } finally {
    binding.policyRevision = "1";
  }
});

test("principal transport posts resolve and maps unknown principals", async () => {
  const fetch = vi.fn().mockResolvedValue(
    reply(200, { items: [{ principal: userP, label: "Ada" }], unknown: [ouP] }),
  );
  vi.stubGlobal("fetch", fetch);
  const transport = createAppPrincipalsTransport(binding);
  await expect(transport(resolve, new AbortController().signal)).resolves.toEqual({
    action: "resolve",
    items: [{ principal: userP, label: "Ada" }],
    unknown: [ouP],
  });
  const [url, options] = fetch.mock.calls[0];
  expect(url).toBe(`/api/app-platform/installations/${binding.installationId}/principals/resolve`);
  expect(JSON.parse(options.body)).toEqual({ binding: boundBody, principals: [userP, ouP] });
});

test("principal transport maps exact refusals and session loss", async () => {
  const fetch = vi
    .fn()
    .mockResolvedValueOnce(reply(403, { code: "not_permitted" }))
    .mockResolvedValueOnce(reply(429, { code: "rate_limited" }))
    .mockResolvedValueOnce(reply(400, { code: "app_platform_invalid_request" }))
    .mockResolvedValueOnce(reply(401, { code: "session_expired" }))
    .mockResolvedValueOnce(reply(404, { code: "not_found" }))
    .mockResolvedValueOnce(reply(500, { code: "db_error" }));
  vi.stubGlobal("fetch", fetch);
  const lost = vi.fn();
  onSessionLost(lost);
  const transport = createAppPrincipalsTransport(binding);
  const codes: string[] = [];
  for (let i = 0; i < 6; i++) {
    try {
      await transport(search, new AbortController().signal);
    } catch (error) {
      expect(error).toBeInstanceOf(BridgePrincipalError);
      codes.push((error as BridgePrincipalError).code);
    }
  }
  expect(codes).toEqual(["not_permitted", "busy", "invalid_request", "unavailable", "unavailable", "uncertain"]);
  expect(lost).toHaveBeenCalledOnce();
});

test("principal transport rejects malformed settings, requests and responses", async () => {
  expect(() => createAppPrincipalsTransport({ ...binding, releaseSha256: "bad" })).toThrow(BridgePrincipalError);
  const fetch = vi.fn().mockResolvedValue(reply(200, { items: [{ principal: "user:x", label: "A" }], next: "" }));
  vi.stubGlobal("fetch", fetch);
  const transport = createAppPrincipalsTransport(binding);
  await expect(transport({ action: "search", kind: "group" } as never, new AbortController().signal)).rejects.toMatchObject({
    code: "invalid_request",
  });
  expect(fetch).not.toHaveBeenCalled();
  await expect(transport(search, new AbortController().signal)).rejects.toMatchObject({ code: "uncertain" });
  fetch.mockResolvedValueOnce(reply(200, { items: [], next: "", extra: 1 }));
  await expect(transport(search, new AbortController().signal)).rejects.toMatchObject({ code: "uncertain" });
});

test("principal transport treats aborts and network failures as uncertain without a request", async () => {
  const fetch = vi.fn().mockRejectedValue(new TypeError("offline"));
  vi.stubGlobal("fetch", fetch);
  const transport = createAppPrincipalsTransport(binding);
  const aborted = new AbortController();
  aborted.abort();
  await expect(transport(search, aborted.signal)).rejects.toMatchObject({ code: "not_ready" });
  expect(fetch).not.toHaveBeenCalled();
  await expect(transport(search, new AbortController().signal)).rejects.toMatchObject({ code: "uncertain" });
});
