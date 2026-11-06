import { noteSessionLost } from "../api/client";
import {
  BridgeObjectError,
  parseBridgeObjectRequest,
  parseBridgeObjectResponse,
  type BridgeBinding,
  type BridgeObjectRequest,
  type BridgeObjectResponse,
} from "./bridge-v2";

const responseLimit = 8 * 1024 * 1024;

export function createAppObjectsTransport(binding: BridgeBinding) {
  const { installationId, generation, policyRevision, releaseSha256 } = binding;
  if (
    !/^[a-f0-9]{8}(-[a-f0-9]{4}){3}-[a-f0-9]{12}$/.test(installationId) ||
    !/^[1-9][0-9]{0,18}$/.test(generation) ||
    BigInt(generation) > 9223372036854775807n ||
    !/^[1-9][0-9]{0,18}$/.test(policyRevision) ||
    BigInt(policyRevision) > 9223372036854775807n ||
    !/^[a-f0-9]{64}$/.test(releaseSha256)
  )
    throw new BridgeObjectError("unavailable");
  const bound = Object.freeze({ generation, policyRevision, releaseSha256 });
  const url = `/api/app-platform/installations/${installationId}/objects`;
  return async (
    value: BridgeObjectRequest,
    signal: AbortSignal,
  ): Promise<BridgeObjectResponse> => {
    const request = parseBridgeObjectRequest(value);
    if (!request) throw new BridgeObjectError("invalid_request");
    if (signal.aborted) throw new BridgeObjectError("not_ready");
    let response: Response;
    try {
      response = await fetch(url, {
        method: "POST",
        credentials: "same-origin",
        redirect: "error",
        cache: "no-store",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ binding: bound, ...request }),
        signal,
      });
    } catch {
      throw new BridgeObjectError("uncertain");
    }
    let text = "";
    const reader = response.body?.getReader();
    if (!reader) throw new BridgeObjectError("uncertain");
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
      throw new BridgeObjectError("uncertain");
    } finally {
      try {
        await reader.cancel();
      } catch {
        /* no response details cross the port */
      }
      reader.releaseLock();
    }
    if (signal.aborted) throw new BridgeObjectError("uncertain");
    let data: unknown;
    try {
      data = JSON.parse(text);
    } catch {
      if (response.status === 404) throw new BridgeObjectError("unavailable");
      if (response.status === 409) throw new BridgeObjectError("conflict");
      if (response.status === 413)
        throw new BridgeObjectError("storage_full");
      if ([400, 422].includes(response.status))
        throw new BridgeObjectError("invalid_request");
      if (response.status === 429) throw new BridgeObjectError("busy");
      if ([401, 503].includes(response.status))
        throw new BridgeObjectError("unavailable");
      throw new BridgeObjectError("uncertain");
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
      if (response.status === 404) throw new BridgeObjectError("unavailable");
      if (response.status === 409) throw new BridgeObjectError("conflict");
      if (response.status === 413)
        throw new BridgeObjectError("storage_full");
      if ([400, 422].includes(response.status))
        throw new BridgeObjectError("invalid_request");
      if (response.status === 429) throw new BridgeObjectError("busy");
      if ([401, 503].includes(response.status))
        throw new BridgeObjectError("unavailable");
      throw new BridgeObjectError("uncertain");
    }
    if (!data || typeof data !== "object") throw new BridgeObjectError("uncertain");
    const record = data as Record<string, unknown>;
    if (record.action !== request.action)
      throw new BridgeObjectError("uncertain");
    let parsed: unknown;
    if (request.action === "list") {
      parsed = {
        action: "list",
        items: record.items,
        ...(record.nextCursor === undefined
          ? {}
          : { nextCursor: record.nextCursor }),
      };
    } else if (request.action === "get") {
      parsed = {
        action: "get",
        object: record.object,
        contentBase64: record.contentBase64,
      };
    } else if (request.action === "create" || request.action === "update") {
      parsed = { action: request.action, object: record.object };
    } else {
      parsed = { action: "delete", id: record.id };
    }
    const result = parseBridgeObjectResponse(parsed);
    if (!result) throw new BridgeObjectError("uncertain");
    return result;
  };
}
