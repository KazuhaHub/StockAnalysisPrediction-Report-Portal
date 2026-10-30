import { afterEach, expect, test, vi } from "vitest";
import { onSessionLost } from "../api/client";
import { createAppReportsTransport } from "./appReportsV2";

const binding = {
  installationId: "00000000-0000-0000-0000-000000000001",
  generation: "9007199254740993",
  policyRevision: "1",
  releaseSha256: "a".repeat(64),
};
const summary = {
  id: 42,
  run_id: "run-42",
  symbol: "600100",
  name: "Example",
  date: "2026-10-30",
  time: "09:30",
  kind: "research",
  subtype: "daily",
  title: "Current report",
  source: "analysis",
  version: "report-version",
};

afterEach(() => {
  vi.unstubAllGlobals();
  onSessionLost(null);
});

test("report transport fixes the installation binding and maps list results", async () => {
  const fetch = vi.fn().mockResolvedValue(
    new Response(
      JSON.stringify({
        ok: true,
        count: 1,
        total: 7,
        offset: 20,
        limit: 20,
        items: [summary],
      }),
    ),
  );
  vi.stubGlobal("fetch", fetch);
  const transport = createAppReportsTransport(binding);
  binding.policyRevision = "2";
  try {
    const signal = new AbortController().signal;
    const request = {
      kind: "query" as const,
      query: { q: "current", date: "today", limit: 20, offset: 20 },
    };
    await expect(transport(request, signal)).resolves.toEqual({
      kind: "query",
      count: 1,
      total: 7,
      offset: 20,
      limit: 20,
      items: [
        {
          id: 42,
          runId: "run-42",
          symbol: "600100",
          name: "Example",
          date: "2026-10-30",
          time: "09:30",
          kind: "research",
          subtype: "daily",
          title: "Current report",
          source: "analysis",
          version: "report-version",
        },
      ],
    });
    const [url, options] = fetch.mock.calls[0];
    expect(url).toBe(
      `/api/app-platform/installations/${binding.installationId}/reports`,
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
      query: request.query,
    });
  } finally {
    binding.policyRevision = "1";
  }
});

test("report detail preserves Markdown and rejects malformed output", async () => {
  const fetch = vi
    .fn()
    .mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          ok: true,
          report: { ...summary, body_md: "# Result\n\nEvidence." },
        }),
      ),
    )
    .mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          ok: true,
          report: {
            ...summary,
            body_md: "body\u0000private",
          },
        }),
      ),
    );
  vi.stubGlobal("fetch", fetch);
  const transport = createAppReportsTransport(binding);
  await expect(
    transport({ kind: "get", id: 42 }, new AbortController().signal),
  ).resolves.toMatchObject({
    kind: "get",
    report: { id: 42, bodyMarkdown: "# Result\n\nEvidence." },
  });
  expect(JSON.parse(fetch.mock.calls[0][1].body)).toEqual({
    binding: {
      generation: binding.generation,
      policyRevision: binding.policyRevision,
      releaseSha256: binding.releaseSha256,
    },
    id: 42,
  });
  await expect(
    transport({ kind: "get", id: 42 }, new AbortController().signal),
  ).rejects.toMatchObject({ code: "uncertain", retryable: false });
});

test("report failures use bounded stable codes without retrying", async () => {
  const fetch = vi
    .fn()
    .mockResolvedValueOnce(
      new Response(JSON.stringify({ error: { code: "rate_limited" } }), {
        status: 429,
      }),
    )
    .mockResolvedValueOnce(new Response("not JSON", { status: 502 }))
    .mockResolvedValueOnce(new Response("not JSON", { status: 404 }))
    .mockRejectedValueOnce(new Error("private socket"));
  vi.stubGlobal("fetch", fetch);
  const transport = createAppReportsTransport(binding);
  for (const code of ["busy", "uncertain", "unavailable", "uncertain"]) {
    await expect(
      transport(
        { kind: "query", query: { limit: 20 } },
        new AbortController().signal,
      ),
    ).rejects.toMatchObject({ code, retryable: false });
  }
  expect(fetch).toHaveBeenCalledTimes(4);
});

test("report responses are size-bounded and malformed output is uncertain", async () => {
  const fetch = vi
    .fn()
    .mockResolvedValueOnce(new Response("x".repeat(6 * 1048576 + 1025)))
    .mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          ok: true,
          count: 2,
          total: 2,
          offset: 0,
          limit: 20,
          items: [summary],
        }),
      ),
    );
  vi.stubGlobal("fetch", fetch);
  const transport = createAppReportsTransport(binding);
  for (let index = 0; index < 2; index++) {
    await expect(
      transport(
        { kind: "query", query: { limit: 20 } },
        new AbortController().signal,
      ),
    ).rejects.toMatchObject({ code: "uncertain", retryable: false });
  }
});

test("only a Portal session-expired response invokes the host session handler", async () => {
  const lost = vi.fn();
  onSessionLost(lost);
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          error: { code: "session_expired" },
        }),
        { status: 401 },
      ),
    ),
  );
  await expect(
    createAppReportsTransport(binding)(
      { kind: "query", query: { limit: 20 } },
      new AbortController().signal,
    ),
  ).rejects.toMatchObject({ code: "unavailable", retryable: false });
  expect(lost).toHaveBeenCalledOnce();
});

test("pre-aborted and malformed report requests never fetch", async () => {
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  const transport = createAppReportsTransport(binding);
  const abort = new AbortController();
  abort.abort();
  await expect(
    transport({ kind: "query", query: { limit: 20 } }, abort.signal),
  ).rejects.toMatchObject({ code: "not_ready" });
  for (const request of [
    { kind: "query", query: { limit: 51 } },
    { kind: "get", id: 0 },
    { kind: "query", query: { unknown: "field" } },
    { kind: "query", query: { limit: 20 }, extra: true },
  ]) {
    await expect(
      transport(request as never, new AbortController().signal),
    ).rejects.toMatchObject({ code: "invalid_request" });
  }
  expect(fetch).not.toHaveBeenCalled();
});
