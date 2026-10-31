import { noteSessionLost } from "../api/client";
import {
  BridgeBackendError,
  parseBridgeBackendRequest,
  parseBridgeBackendResponse,
  type BridgeBinding,
  type BridgeBackendRequest,
  type BridgeBackendResponse,
} from "./bridge-v2";

// Parent-only transport. App data never supplies a URL, headers or identity.
// A failure after fetch starts may follow a successful app write; never retry.
export function createAppBackendTransport(binding: BridgeBinding) {
  const { installationId, generation, policyRevision, releaseSha256 } = binding;
  if (
    !/^[a-f0-9]{8}(-[a-f0-9]{4}){3}-[a-f0-9]{12}$/.test(installationId) ||
    !/^[1-9][0-9]{0,18}$/.test(generation) ||
    BigInt(generation) > 9223372036854775807n ||
    !/^[1-9][0-9]{0,18}$/.test(policyRevision) ||
    BigInt(policyRevision) > 9223372036854775807n ||
    !/^[a-f0-9]{64}$/.test(releaseSha256)
  )
    throw new BridgeBackendError("unavailable");
  const bound = Object.freeze({ generation, policyRevision, releaseSha256 });
  const url = `/api/app-platform/installations/${installationId}/backend`;
  return async (
    value: BridgeBackendRequest,
    signal: AbortSignal,
  ): Promise<BridgeBackendResponse> => {
    const request = parseBridgeBackendRequest(value);
    if (!request) throw new BridgeBackendError("invalid_request");
    if (signal.aborted) throw new BridgeBackendError("not_ready");
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
      throw new BridgeBackendError("uncertain");
    }
    // 1 MiB of app UTF-8 may expand sixfold inside the JSON string envelope.
    const limit = 6 * 1048576 + 1024;
    let text = "";
    const reader = response.body?.getReader();
    if (!reader) throw new BridgeBackendError("uncertain");
    try {
      const decoder = new TextDecoder("utf-8", { fatal: true });
      let bytes = 0;
      for (;;) {
        const part = await reader.read();
        if (part.done) break;
        bytes += part.value.byteLength;
        if (bytes > limit) throw new Error("response limit");
        text += decoder.decode(part.value, { stream: true });
      }
      text += decoder.decode();
    } catch {
      throw new BridgeBackendError("uncertain");
    } finally {
      try {
        await reader.cancel();
      } catch {
        /* no response details cross the port */
      }
      reader.releaseLock();
    }
    if (signal.aborted) throw new BridgeBackendError("uncertain");
    let data: unknown;
    try {
      data = JSON.parse(text);
    } catch {
      if (response.status === 404 && text === "application unavailable\n")
        throw new BridgeBackendError("unavailable");
      throw new BridgeBackendError("uncertain");
    }
    if (response.status !== 200) {
      const code =
        data && typeof data === "object" && "code" in data
          ? data.code
          : undefined;
      if (typeof code === "string") noteSessionLost(response.status, code);
      const safe: Record<number, string> = {
        400: "app_platform_invalid_request",
        401: "session_expired",
        403: "app_platform_origin_denied",
        415: "app_platform_json_required",
        429: "app_platform_busy",
        503: "app_platform_runtime_unavailable",
      };
      if (typeof code === "string" && code === safe[response.status])
        throw new BridgeBackendError(
          response.status === 429 ? "busy" : "unavailable",
        );
      throw new BridgeBackendError("uncertain");
    }
    const parsed = parseBridgeBackendResponse(data);
    if (!parsed) throw new BridgeBackendError("uncertain");
    return parsed;
  };
}
