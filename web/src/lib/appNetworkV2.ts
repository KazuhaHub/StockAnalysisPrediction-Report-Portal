import { noteSessionLost } from "../api/client";
import {
  AppNetworkError,
  NETWORK_RESPONSE_MAX_BYTES,
  NETWORK_STREAM_MAX_BYTES,
  decodeNetworkStreamFrame,
  parseNetworkRequest,
  parseNetworkResponse,
  parseNetworkStreamChunk,
  parseNetworkStreamEnd,
  parseNetworkStreamError,
  parseNetworkStreamStart,
  type NetworkRequest,
  type NetworkResponse,
  type NetworkStreamEnd,
} from "./network-v2";
import type { BridgeBinding } from "./bridge-v2";

const responseEnvelopeLimit = 2 * NETWORK_RESPONSE_MAX_BYTES;
const streamLineLimit = 128 * 1024;
const encoder = new TextEncoder();

export type AppNetworkStreamEvent =
  | { readonly type: "start"; readonly status: number; readonly contentType: string }
  | { readonly type: "chunk"; readonly sequence: number; readonly bodyBase64: string }
  | { readonly type: "end"; readonly sequence: number; readonly bytes: number; readonly sha256: string };

export type AppNetworkStreamEmit = (event: AppNetworkStreamEvent) => Promise<void>;

function fixedNetworkError(status: number, code: unknown): AppNetworkError {
  if (status === 400 && code === "app_platform_invalid_request")
    return new AppNetworkError("invalid_request");
  if (status === 429 && code === "app_platform_busy")
    return new AppNetworkError("busy");
  if (status === 503 && code === "app_platform_runtime_unavailable")
    return new AppNetworkError("unavailable");
  if (status === 502 && code === "app_platform_request_uncertain")
    return new AppNetworkError("uncertain");
  return new AppNetworkError("uncertain");
}

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
      if (
        (response.status === 401 || response.status === 403) &&
        typeof code === "string"
      )
        throw new AppNetworkError("unavailable");
      throw fixedNetworkError(response.status, code);
    }
    const parsed = parseNetworkResponse(data);
    if (!parsed) throw new AppNetworkError("uncertain");
    return parsed;
  };
}

export function createAppNetworkStreamTransport(binding: BridgeBinding) {
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
  const url = `/api/app-platform/installations/${installationId}/network/stream`;
  return async (
    value: NetworkRequest,
    signal: AbortSignal,
    emit: AppNetworkStreamEmit,
  ): Promise<NetworkStreamEnd> => {
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
    if (response.status !== 200) {
      let data: unknown;
      try {
        data = JSON.parse(await response.text());
      } catch {
        throw new AppNetworkError("uncertain");
      }
      const code =
        data && typeof data === "object" && "code" in data
          ? data.code
          : undefined;
      if (response.status === 401)
        noteSessionLost(
          response.status,
          typeof code === "string" ? code : undefined,
        );
      if (
        (response.status === 401 || response.status === 403) &&
        typeof code === "string"
      )
        throw new AppNetworkError("unavailable");
      throw fixedNetworkError(response.status, code);
    }
    if (!response.body) throw new AppNetworkError("uncertain");
    const reader = response.body.getReader();
    const decoder = new TextDecoder("utf-8", { fatal: true });
    const hashParts: Uint8Array[] = [];
    let leftover = "";
    let started = false;
    let sequence = 0;
    let total = 0;
    try {
      for (;;) {
        const part = await reader.read();
        if (part.done) break;
        leftover += decoder.decode(part.value, { stream: true });
        if (encoder.encode(leftover).byteLength > streamLineLimit && !leftover.includes("\n"))
          throw new AppNetworkError("uncertain");
        for (;;) {
          const newline = leftover.indexOf("\n");
          if (newline < 0) break;
          const line = leftover.slice(0, newline);
          leftover = leftover.slice(newline + 1);
          if (line.length === 0 || line.length > streamLineLimit)
            throw new AppNetworkError("uncertain");
          let value: unknown;
          try {
            value = JSON.parse(line);
          } catch {
            throw new AppNetworkError("uncertain");
          }
          if (!started) {
            const start = parseNetworkStreamStart(value);
            if (!start) throw new AppNetworkError("uncertain");
            started = true;
            await emit({ type: "start", ...start });
            continue;
          }
          const chunk = parseNetworkStreamChunk(value);
          if (chunk) {
            if (chunk.sequence !== sequence + 1)
              throw new AppNetworkError("uncertain");
            const bytes = decodeNetworkStreamFrame(chunk.bodyBase64);
            total += bytes.byteLength;
            if (total > NETWORK_STREAM_MAX_BYTES)
              throw new AppNetworkError("uncertain");
            sequence = chunk.sequence;
            hashParts.push(bytes);
            await emit({
              type: "chunk",
              sequence,
              bodyBase64: chunk.bodyBase64,
            });
            continue;
          }
          const end = parseNetworkStreamEnd(value);
          if (end) {
            if (
              end.sequence !== sequence ||
              end.bytes !== total ||
              !(await digestMatches(hashParts, end.sha256))
            )
              throw new AppNetworkError("uncertain");
            await emit({ type: "end", ...end });
            if ((await reader.read()).done !== true)
              throw new AppNetworkError("uncertain");
            return end;
          }
          const code = parseNetworkStreamError(value);
          if (code) throw new AppNetworkError(code);
          throw new AppNetworkError("uncertain");
        }
      }
      leftover += decoder.decode();
      if (leftover.length !== 0 || !started)
        throw new AppNetworkError("uncertain");
      throw new AppNetworkError("uncertain");
    } catch (error) {
      if (error instanceof AppNetworkError) throw error;
      throw new AppNetworkError("uncertain");
    } finally {
      try {
        await reader.cancel();
      } catch {
        /* Fixed failures only. */
      }
      reader.releaseLock();
    }
  };
}

async function digestMatches(parts: readonly Uint8Array[], expected: string) {
  if (!globalThis.crypto?.subtle) return false;
  const bytes = new Uint8Array(parts.reduce((total, part) => total + part.byteLength, 0));
  let offset = 0;
  for (const part of parts) {
    bytes.set(part, offset);
    offset += part.byteLength;
  }
  const digest = new Uint8Array(await globalThis.crypto.subtle.digest("SHA-256", bytes));
  const actual = Array.from(digest, (value) => value.toString(16).padStart(2, "0")).join("");
  return actual === expected;
}
