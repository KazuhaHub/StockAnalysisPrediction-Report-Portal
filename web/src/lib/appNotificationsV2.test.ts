import { afterEach, expect, test, vi } from "vitest";
import { onSessionLost } from "../api/client";
import { BridgeNotificationError } from "./bridge-v2";
import { createAppNotificationsTransport } from "./appNotificationsV2";

const binding = {
  installationId: "00000000-0000-0000-0000-000000000001",
  generation: "9007199254740993",
  policyRevision: "1",
  releaseSha256: "a".repeat(64),
};
const request = {
  occurrenceId: "digest:2026-11-02",
  title: "Daily digest",
  message: "Three reports are ready.",
  severity: "info" as const,
};

afterEach(() => {
  vi.unstubAllGlobals();
  onSessionLost(null);
});

test("notification transport fixes the installation binding and accepts idempotent capture", async () => {
  const fetch = vi.fn().mockResolvedValue(
    new Response(
      JSON.stringify({
        ok: true,
        id: "00000000-0000-4000-8000-000000000042",
      }),
    ),
  );
  vi.stubGlobal("fetch", fetch);
  const transport = createAppNotificationsTransport(binding);
  binding.policyRevision = "2";
  try {
    const signal = new AbortController().signal;
    await expect(transport(request, signal)).resolves.toEqual({
      id: "00000000-0000-4000-8000-000000000042",
    });
    const [url, options] = fetch.mock.calls[0];
    expect(url).toBe(
      `/api/app-platform/installations/${binding.installationId}/notifications`,
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

test("notification transport maps exact conflicts, capacity and session loss", async () => {
  const fetch = vi
    .fn()
    .mockResolvedValueOnce(
      new Response(JSON.stringify({ code: "notification_conflict" }), {
        status: 409,
      }),
    )
    .mockResolvedValueOnce(
      new Response(JSON.stringify({ code: "notification_capacity" }), {
        status: 429,
      }),
    )
    .mockResolvedValueOnce(
      new Response(JSON.stringify({ code: "session_expired" }), {
        status: 401,
      }),
    );
  vi.stubGlobal("fetch", fetch);
  const transport = createAppNotificationsTransport(binding);
  await expect(transport(request, new AbortController().signal)).rejects.toMatchObject({
    code: "conflict",
    retryable: false,
  });
  await expect(transport(request, new AbortController().signal)).rejects.toMatchObject({
    code: "capacity",
    retryable: false,
  });
  await expect(transport(request, new AbortController().signal)).rejects.toMatchObject({
    code: "unavailable",
    retryable: false,
  });
});

test("notification transport rejects malformed settings, requests and responses", async () => {
  expect(() =>
    createAppNotificationsTransport({ ...binding, releaseSha256: "bad" }),
  ).toThrow(BridgeNotificationError);
  const fetch = vi.fn().mockResolvedValue(
    new Response(JSON.stringify({ ok: true, id: "<script>" })),
  );
  vi.stubGlobal("fetch", fetch);
  const transport = createAppNotificationsTransport(binding);
  await expect(
    transport({ ...request, title: " " }, new AbortController().signal),
  ).rejects.toMatchObject({ code: "invalid_request" });
  await expect(transport(request, new AbortController().signal)).rejects.toMatchObject({
    code: "uncertain",
  });
  expect(fetch).toHaveBeenCalledOnce();
});
