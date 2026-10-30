import { noteSessionLost } from "../api/client";
import {
  AppNetworkError,
  NETWORK_RESPONSE_MAX_BYTES,
  parseNetworkRequest,
  parseNetworkResponse,
  type NetworkRequest,
  type NetworkResponse,
} from "./network-v2";
import type { BridgeBinding } from "./bridge-v2";

const responseEnvelopeLimit = 2 * NETWORK_RESPONSE_MAX_BYTES;

// Parent-only transport. The app supplies a reviewed target and bytes, never a
// Portal actor, credential, internal route, or arbitrary header map.
export function createAppNetworkTransport(binding: BridgeBinding) {
  const { installationId, generation, policyRevision, releaseSha256 } = binding;
  if (
    !/^[a-f0-9]{8}(-[a-f0-9]{4}){3}-[a-f0-9]{12}$/.test(installationId) ||
    !/^[1-9][0-9]{0,18}$/.test(generation) ||
    BigInt(generation) > 9223372036854775807n ||
    !/^[1-9][0-9]{0,18}$/.test(policyRevision) ||
    BigInt(policyRevision) > 9223372036854775807n ||
    !/^[a-f0-9]{64}$/.test(releaseSha256)
  )
    throw new AppNetworkError("unavailable");
  const bound = Object.freeze({ generation, policyRevision, releaseSha256 });
  const url = `/api/app-platform/installations/${installationId}/network`;
  return async (
    value: NetworkRequest,
    signal: AbortSignal,
  ): Promise<NetworkResponse> => {
    const request = parseNetworkRequest(value);
    if (!request) throw new AppNetworkError("invalid_request");
    if (signal.aborted) throw new AppNetworkError("not_ready");
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
      throw new AppNetworkError("uncertain");
    }
    let text = "";
    const reader = response.body?.getReader();
    if (!reader) throw new AppNetworkError("uncertain");
    try {
      const decoder = new TextDecoder("utf-8", { fatal: true });
      let bytes = 0;
      for (;;) {
        const part = await reader.read();
        if (part.done) break;
        bytes += part.value.byteLength;
        if (bytes > responseEnvelopeLimit)
          throw new Error("response limit");
        text += decoder.decode(part.value, { stream: true });
      }
      text += decoder.decode();
    } catch {
      throw new AppNetworkError("uncertain");
    } finally {
      try {
        await reader.cancel();
      } catch {
        /* No response details cross the port. */
      }
      reader.releaseLock();
    }
    if (signal.aborted) throw new AppNetworkError("uncertain");
    let data: unknown;
    try {
      data = JSON.parse(text);
    } catch {
      if (
        response.status === 404 &&
        text === "application unavailable\n"
      )
        throw new AppNetworkError("unavailable");
      throw new AppNetworkError("uncertain");
    }
    if (response.status !== 200) {
      const code =
        data && typeof data === "object" && "code" in data
          ? data.code
          : undefined;
      if (response.status === 401)
        noteSessionLost(
          response.status,
          typeof code === "string" ? code : undefined,
        );
      if (response.status === 400 && code === "app_platform_invalid_request")
        throw new AppNetworkError("invalid_request");
      if (response.status === 429 && code === "app_platform_busy")
        throw new AppNetworkError("busy");
      if (
        (response.status === 401 || response.status === 403) &&
        typeof code === "string"
      )
        throw new AppNetworkError("unavailable");
      if (
        response.status === 503 &&
        code === "app_platform_runtime_unavailable"
      )
        throw new AppNetworkError("unavailable");
      throw new AppNetworkError("uncertain");
    }
    const parsed = parseNetworkResponse(data);
    if (!parsed) throw new AppNetworkError("uncertain");
    return parsed;
  };
}
