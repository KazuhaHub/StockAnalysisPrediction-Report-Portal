import { afterEach, expect, test, vi } from "vitest";
import { onSessionLost } from "../api/client";
import { createAppObjectsTransport } from "./appObjectsV2";

const binding = {
  installationId: "00000000-0000-4000-8000-000000000001",
  generation: "9007199254740993",
  policyRevision: "1",
  releaseSha256: "a".repeat(64),
};
const object = {
  id: "00000000-0000-4000-8000-000000000042",
  collection: "note",
  label: "First note",
  mediaType: "application/json",
  sizeBytes: 5,
  revision: 1,
  createdAt: 1,
  updatedAt: 1,
};
afterEach(() => {
  vi.unstubAllGlobals();
  onSessionLost(null);
});

test("object transport fixes the binding and parses list/get/mutations", async () => {
  const fetch = vi
    .fn()
    .mockResolvedValueOnce(
      new Response(JSON.stringify({ ok: true, action: "list", items: [object] })),
    )
    .mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          ok: true,
          action: "get",
          object,
          contentBase64: "aGVsbG8=",
        }),
      ),
    )
    .mockResolvedValueOnce(
      new Response(JSON.stringify({ ok: true, action: "create", object })),
    )
    .mockResolvedValueOnce(
      new Response(JSON.stringify({ ok: true, action: "delete", id: object.id })),
    );
  vi.stubGlobal("fetch", fetch);
  const transport = createAppObjectsTransport(binding);
  const signal = new AbortController().signal;
  await expect(
    transport({ action: "list", collection: "note", limit: 20 }, signal),
  ).resolves.toEqual({ action: "list", items: [object] });
  await expect(
    transport({ action: "get", id: object.id }, signal),
  ).resolves.toEqual({ action: "get", object, contentBase64: "aGVsbG8=" });
  await expect(
    transport(
      {
        action: "create",
        requestId: "create-1",
        collection: "note",
        label: "First note",
        mediaType: "application/json",
        contentBase64: "aGVsbG8=",
      },
      signal,
    ),
  ).resolves.toEqual({ action: "create", object });
  await expect(
    transport({ action: "delete", id: object.id, expectedRevision: 1 }, signal),
  ).resolves.toEqual({ action: "delete", id: object.id });
  expect(fetch).toHaveBeenCalledTimes(4);
  expect(fetch.mock.calls[0][0]).toBe(
    `/api/app-platform/installations/${binding.installationId}/objects`,
  );
  expect(JSON.parse(fetch.mock.calls[0][1].body)).toMatchObject({
    binding: {
      generation: binding.generation,
      policyRevision: binding.policyRevision,
      releaseSha256: binding.releaseSha256,
    },
    action: "list",
    collection: "note",
  });
});

test("object transport maps conflict, quota and uncertain without retrying", async () => {
  const fetch = vi
    .fn()
    .mockResolvedValueOnce(
      new Response(JSON.stringify({ error: { code: "conflict" } }), {
        status: 409,
      }),
    )
    .mockResolvedValueOnce(
      new Response(JSON.stringify({ error: { code: "storage_full" } }), {
        status: 413,
      }),
    )
    .mockResolvedValueOnce(new Response("not JSON", { status: 404 }))
    .mockRejectedValueOnce(new Error("socket detail"));
  vi.stubGlobal("fetch", fetch);
  const transport = createAppObjectsTransport(binding);
  for (const code of ["conflict", "storage_full", "unavailable", "uncertain"]) {
    await expect(
      transport({ action: "list", collection: "note" }, new AbortController().signal),
    ).rejects.toMatchObject({ code, retryable: false });
  }
  expect(fetch).toHaveBeenCalledTimes(4);
});

test("object transport rejects malformed or oversized responses", async () => {
  const fetch = vi
    .fn()
    .mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          ok: true,
          action: "list",
          items: [{ ...object, extra: true }],
        }),
      ),
    )
    .mockResolvedValueOnce(new Response("x".repeat(8 * 1024 * 1024 + 1)));
  vi.stubGlobal("fetch", fetch);
  const transport = createAppObjectsTransport(binding);
  for (let index = 0; index < 2; index++) {
    await expect(
      transport({ action: "list", collection: "note" }, new AbortController().signal),
    ).rejects.toMatchObject({ code: "uncertain", retryable: false });
  }
});

test("object session expiry invokes the Portal session handler", async () => {
  const lost = vi.fn();
  onSessionLost(lost);
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValueOnce(
      new Response(JSON.stringify({ error: { code: "session_expired" } }), {
        status: 401,
      }),
    ),
  );
  await expect(
    createAppObjectsTransport(binding)(
      { action: "list", collection: "note" },
      new AbortController().signal,
    ),
  ).rejects.toMatchObject({ code: "unavailable", retryable: false });
  expect(lost).toHaveBeenCalledOnce();
});

test("pre-aborted and invalid object requests never fetch", async () => {
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  const transport = createAppObjectsTransport(binding);
  const abort = new AbortController();
  abort.abort();
  await expect(
    transport({ action: "list", collection: "note" }, abort.signal),
  ).rejects.toMatchObject({ code: "not_ready" });
  for (const request of [
    { action: "list", collection: "Note" },
    { action: "get", id: "bad" },
    { action: "delete", id: object.id, expectedRevision: 0 },
    { action: "list", collection: "note", extra: true },
  ]) {
    await expect(
      transport(request as never, new AbortController().signal),
    ).rejects.toMatchObject({ code: "invalid_request" });
  }
  expect(fetch).not.toHaveBeenCalled();
});
