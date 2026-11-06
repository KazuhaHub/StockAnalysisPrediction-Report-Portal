import { noteSessionLost } from "../api/client";
import {
  BridgeReportError,
  parseBridgeReportRequest,
  parseBridgeReportResponse,
  type BridgeBinding,
  type BridgeReportRequest,
  type BridgeReportResponse,
} from "./bridge-v2";

// A bounded Markdown body can expand sixfold inside its JSON string envelope.
const responseLimit = 6 * 1024 * 1024 + 1024;

function reportSummary(value: Record<string, unknown>) {
  return {
    id: value.id,
    runId: value.run_id,
    symbol: value.symbol,
    name: value.name,
    date: value.date,
    time: value.time,
    kind: value.kind,
    subtype: value.subtype,
    title: value.title,
    source: value.source,
    version: value.version,
  };
}

export function createAppReportsTransport(binding: BridgeBinding) {
  const { installationId, generation, policyRevision, releaseSha256 } = binding;
  if (
    !/^[a-f0-9]{8}(-[a-f0-9]{4}){3}-[a-f0-9]{12}$/.test(installationId) ||
    !/^[1-9][0-9]{0,18}$/.test(generation) ||
    BigInt(generation) > 9223372036854775807n ||
    !/^[1-9][0-9]{0,18}$/.test(policyRevision) ||
    BigInt(policyRevision) > 9223372036854775807n ||
    !/^[a-f0-9]{64}$/.test(releaseSha256)
  )
    throw new BridgeReportError("unavailable");
  const bound = Object.freeze({ generation, policyRevision, releaseSha256 });
  const url = `/api/app-platform/installations/${installationId}/reports`;
  return async (
    value: BridgeReportRequest,
    signal: AbortSignal,
  ): Promise<BridgeReportResponse> => {
    const request = parseBridgeReportRequest(value);
    if (!request) throw new BridgeReportError("invalid_request");
    if (signal.aborted) throw new BridgeReportError("not_ready");
    const payload =
      request.kind === "query"
        ? { binding: bound, query: request.query }
        : { binding: bound, id: request.id };
    let response: Response;
    try {
      response = await fetch(url, {
        method: "POST",
        credentials: "same-origin",
        redirect: "error",
        cache: "no-store",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload),
        signal,
      });
    } catch {
      throw new BridgeReportError("uncertain");
    }
    let text = "";
    const reader = response.body?.getReader();
    if (!reader) throw new BridgeReportError("uncertain");
    try {
      const decoder = new TextDecoder("utf-8", { fatal: true });
      let bytes = 0;
      for (;;) {
        const part = await reader.read();
        if (part.done) break;
        bytes += part.value.byteLength;
        if (bytes > responseLimit) throw new Error("response limit");
        text += decoder.decode(part.value, { stream: true });
      }
      text += decoder.decode();
    } catch {
      throw new BridgeReportError("uncertain");
    } finally {
      try {
        await reader.cancel();
      } catch {
        /* no response details cross the port */
      }
      reader.releaseLock();
    }
    if (signal.aborted) throw new BridgeReportError("uncertain");
    let data: unknown;
    try {
      data = JSON.parse(text);
    } catch {
      if (response.status === 404) throw new BridgeReportError("unavailable");
      if (response.status === 429) throw new BridgeReportError("busy");
      if ([400, 422].includes(response.status))
        throw new BridgeReportError("invalid_request");
      if ([401, 503].includes(response.status))
        throw new BridgeReportError("unavailable");
      throw new BridgeReportError("uncertain");
    }
    if (response.status !== 200) {
      const code =
        data && typeof data === "object" && "error" in data &&
        data.error && typeof data.error === "object" &&
        "code" in data.error
          ? data.error.code
          : undefined;
      if (response.status === 401) {
        noteSessionLost(
          response.status,
          typeof code === "string" ? code : undefined,
        );
      }
      if (response.status === 404) throw new BridgeReportError("unavailable");
      if (response.status === 429) throw new BridgeReportError("busy");
      if ([400, 422].includes(response.status))
        throw new BridgeReportError("invalid_request");
      if ([401, 503].includes(response.status))
        throw new BridgeReportError("unavailable");
      throw new BridgeReportError("uncertain");
    }
    if (!data || typeof data !== "object") throw new BridgeReportError("uncertain");
    const record = data as Record<string, unknown>;
    let parsed: unknown;
    if (request.kind === "query") {
      if (!Array.isArray(record.items)) throw new BridgeReportError("uncertain");
      parsed = {
        kind: "query",
        count: record.count,
        total: record.total,
        offset: record.offset,
        limit: record.limit,
        items: record.items.map((item) =>
          item && typeof item === "object"
            ? reportSummary(item as Record<string, unknown>)
            : null,
        ),
      };
    } else {
      parsed =
        record.report && typeof record.report === "object"
          ? {
              kind: "get",
              report: {
                ...reportSummary(record.report as Record<string, unknown>),
                bodyMarkdown: (
                  record.report as Record<string, unknown>
                ).body_md,
              },
            }
          : null;
    }
    const result = parseBridgeReportResponse(parsed);
    if (!result) throw new BridgeReportError("uncertain");
    return result;
  };
}
