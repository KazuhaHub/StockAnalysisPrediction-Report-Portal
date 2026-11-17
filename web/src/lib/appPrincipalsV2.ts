import { noteSessionLost } from "../api/client";
import {
  BridgePrincipalError,
  parseBridgePrincipalRequest,
  parseBridgePrincipalResponse,
  type BridgeBinding,
  type BridgePrincipalRequest,
  type BridgePrincipalResponse,
} from "./bridge-v2";

// 100 items of a 160 byte label and a 71 byte principal, with JSON overhead and escaping headroom.
const responseLimit = 256 * 1024;

export function createAppPrincipalsTransport(binding: BridgeBinding) {
  const { installationId, generation, policyRevision, releaseSha256 } = binding;
  if (
    !/^[a-f0-9]{8}(-[a-f0-9]{4}){3}-[a-f0-9]{12}$/.test(installationId) ||
    !/^[1-9][0-9]{0,18}$/.test(generation) ||
    BigInt(generation) > 9223372036854775807n ||
    !/^[1-9][0-9]{0,18}$/.test(policyRevision) ||
    BigInt(policyRevision) > 9223372036854775807n ||
    !/^[a-f0-9]{64}$/.test(releaseSha256)
  )
    throw new BridgePrincipalError("unavailable");
  const bound = Object.freeze({ generation, policyRevision, releaseSha256 });
  const base = `/api/app-platform/installations/${installationId}/principals`;
  return async (
    value: BridgePrincipalRequest,
    signal: AbortSignal,
  ): Promise<BridgePrincipalResponse> => {
    const request = parseBridgePrincipalRequest(value);
    if (!request) throw new BridgePrincipalError("invalid_request");
    if (signal.aborted) throw new BridgePrincipalError("not_ready");
    const body =
      request.action === "search"
        ? {
            binding: bound,
            kind: request.kind,
            query: request.query,
            after: request.after,
            limit: request.limit,
          }
        : { binding: bound, principals: request.principals };
    let response: Response;
    try {
      response = await fetch(`${base}/${request.action}`, {
        method: "POST",
        credentials: "same-origin",
        redirect: "error",
        cache: "no-store",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
        signal,
      });
    } catch {
      throw new BridgePrincipalError("uncertain");
    }
    let text = "";
    const reader = response.body?.getReader();
    if (!reader) throw new BridgePrincipalError("uncertain");
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
      throw new BridgePrincipalError("uncertain");
    } finally {
      try {
        await reader.cancel();
      } catch {
        /* Response internals never cross the bridge. */
      }
      reader.releaseLock();
    }
    if (signal.aborted) throw new BridgePrincipalError("uncertain");
    let data: unknown;
    try {
      data = JSON.parse(text);
    } catch {
      data = undefined;
    }
    if (response.status === 200) {
      if (!data || typeof data !== "object" || Array.isArray(data))
        throw new BridgePrincipalError("uncertain");
      const record = data as Record<string, unknown>;
      const parsed = parseBridgePrincipalResponse({
        ...record,
        action: request.action,
      });
      if (!parsed) throw new BridgePrincipalError("uncertain");
      return parsed;
    }
    const code =
      data && typeof data === "object" && "code" in data && typeof data.code === "string"
        ? data.code
        : undefined;
    if (response.status === 401) {
      noteSessionLost(response.status, code);
      throw new BridgePrincipalError("unavailable");
    }
    if (response.status === 400) throw new BridgePrincipalError("invalid_request");
    if (response.status === 403) throw new BridgePrincipalError("not_permitted");
    if (response.status === 429) throw new BridgePrincipalError("busy");
    if (response.status === 404) throw new BridgePrincipalError("unavailable");
    throw new BridgePrincipalError("uncertain");
  };
}
