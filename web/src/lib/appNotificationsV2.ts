import { noteSessionLost } from "../api/client";
import {
  BridgeNotificationError,
  parseBridgeNotificationRequest,
  parseBridgeNotificationResponse,
  type BridgeBinding,
  type BridgeNotificationRequest,
  type BridgeNotificationResponse,
} from "./bridge-v2";

const responseLimit = 16 * 1024;

function decodeResponse(text: string): BridgeNotificationResponse | null {
  let value: unknown;
  try {
    value = JSON.parse(text);
  } catch {
    return null;
  }
  if (
    !value ||
    typeof value !== "object" ||
    Array.isArray(value) ||
    Object.keys(value).some(
      (key) => key !== "ok" && key !== "id" && key !== "created",
    ) ||
    (value as Record<string, unknown>).ok !== true ||
    !("id" in value)
  )
    return null;
  return parseBridgeNotificationResponse({ id: value.id });
}

export function createAppNotificationsTransport(binding: BridgeBinding) {
  const { installationId, generation, policyRevision, releaseSha256 } = binding;
  if (
    !/^[a-f0-9]{8}(-[a-f0-9]{4}){3}-[a-f0-9]{12}$/.test(installationId) ||
    !/^[1-9][0-9]{0,18}$/.test(generation) ||
    BigInt(generation) > 9223372036854775807n ||
    !/^[1-9][0-9]{0,18}$/.test(policyRevision) ||
    BigInt(policyRevision) > 9223372036854775807n ||
    !/^[a-f0-9]{64}$/.test(releaseSha256)
  )
    throw new BridgeNotificationError("unavailable");
  const bound = Object.freeze({ generation, policyRevision, releaseSha256 });
  const url = `/api/app-platform/installations/${installationId}/notifications`;
  return async (
    value: BridgeNotificationRequest,
    signal: AbortSignal,
  ): Promise<BridgeNotificationResponse> => {
    const request = parseBridgeNotificationRequest(value);
    if (!request) throw new BridgeNotificationError("invalid_request");
    if (signal.aborted) throw new BridgeNotificationError("not_ready");
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
      throw new BridgeNotificationError("uncertain");
    }
    let text = "";
    const reader = response.body?.getReader();
    if (!reader) throw new BridgeNotificationError("uncertain");
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
      throw new BridgeNotificationError("uncertain");
    } finally {
      try {
        await reader.cancel();
      } catch {
        /* Response internals never cross the bridge. */
      }
      reader.releaseLock();
    }
    if (signal.aborted) throw new BridgeNotificationError("uncertain");
    if (response.status === 200) {
      const parsed = decodeResponse(text);
      if (!parsed) throw new BridgeNotificationError("uncertain");
      return parsed;
    }
    let code = "";
    try {
      const value = JSON.parse(text) as { code?: unknown };
      if (value && typeof value.code === "string") code = value.code;
    } catch {
      /* Stable client codes below do not depend on error body shape. */
    }
    if (response.status === 401) {
      noteSessionLost(response.status, code || undefined);
      throw new BridgeNotificationError("unavailable");
    }
    if (response.status === 400) {
      throw new BridgeNotificationError("invalid_request");
    }
    if (response.status === 409) {
      throw new BridgeNotificationError("conflict");
    }
    if (response.status === 429) {
      throw new BridgeNotificationError("capacity");
    }
    if (response.status === 403 || response.status === 404) {
      throw new BridgeNotificationError("unavailable");
    }
    throw new BridgeNotificationError("uncertain");
  };
}
