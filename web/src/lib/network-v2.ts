// Shared, provisional network wire shape. These parsers grant no authority;
// current host SQL and reviewed policy govern every actual request.
export const NETWORK_REQUEST_MAX_BYTES = 65_536;
export const NETWORK_RESPONSE_MAX_BYTES = 1_048_576;
export const NETWORK_STREAM_MAX_BYTES = 4_194_304;
export const NETWORK_STREAM_FRAME_MAX_BYTES = 32_768;
export const NETWORK_STREAM_MAX_FRAMES = 160;
export type AppNetworkFailure = "not_ready" | "unsupported" | "invalid_request" | "busy" | "unavailable" | "uncertain";
export class AppNetworkError extends Error {
  readonly retryable = false;
  constructor(readonly code: AppNetworkFailure) {
    super(`Application network request: ${code}`);
    this.name = "AppNetworkError";
  }
}
export interface NetworkRequest {
  readonly method: string;
  readonly url: string;
  readonly accept: string;
  readonly contentType: string;
  readonly bodyBase64: string;
  readonly credential?: string;
}
export interface NetworkResponse {
  readonly status: number;
  readonly contentType: string;
  readonly bodyBase64: string;
}
export interface NetworkStreamStart {
  readonly status: number;
  readonly contentType: string;
}
export interface NetworkStreamChunk {
  readonly sequence: number;
  readonly bodyBase64: string;
}
export interface NetworkStreamEnd {
  readonly sequence: number;
  readonly bytes: number;
  readonly sha256: string;
}
function exact(value: unknown, keys: readonly string[]): value is Record<string,unknown> {
  return !!value && typeof value === "object" && !Array.isArray(value) && Object.keys(value).length === keys.length && keys.every(key => Object.prototype.hasOwnProperty.call(value,key));
}
function header(value: unknown): value is string {
  return typeof value === "string" && value.length <= 256 && !/[^\x20-\x7e]/.test(value);
}
function binary(value: unknown, limit: number): string | null {
  if (typeof value !== "string" || value.length > 4*Math.ceil(limit/3) || !/^[A-Za-z0-9+/]*={0,2}$/.test(value)) return null;
  try {
    const decoded = atob(value);
    return decoded.length <= limit && btoa(decoded) === value ? decoded : null;
  } catch { return null; }
}
export function encodeNetworkBody(bytes: Uint8Array): string {
  if (!(bytes instanceof Uint8Array) || bytes.byteLength > NETWORK_REQUEST_MAX_BYTES) throw new AppNetworkError("invalid_request");
  const chunks: string[] = [];
  for (let i=0;i<bytes.length;i+=8192) chunks.push(String.fromCharCode(...bytes.subarray(i,i+8192)));
  return btoa(chunks.join(""));
}
export function decodeNetworkBody(value: string): Uint8Array {
  const decoded = binary(value,NETWORK_RESPONSE_MAX_BYTES);
  if (decoded === null) throw new AppNetworkError("uncertain");
  const bytes = new Uint8Array(decoded.length);
  for (let i=0;i<decoded.length;i++) bytes[i]=decoded.charCodeAt(i);
  return bytes;
}
export function parseNetworkRequest(value: unknown): NetworkRequest | null {
  if ((!exact(value,["method","url","accept","contentType","bodyBase64"]) && !exact(value,["method","url","accept","contentType","bodyBase64","credential"])) || typeof value.method !== "string" || !["GET","HEAD","POST","PUT","PATCH","DELETE","OPTIONS"].includes(value.method) || typeof value.url !== "string" || new TextEncoder().encode(value.url).length>4096 || /[\x00-\x20\x7f\\#]/.test(value.url) || !header(value.accept) || !header(value.contentType) || binary(value.bodyBase64,NETWORK_REQUEST_MAX_BYTES) === null) return null;
  if ("credential" in value && (typeof value.credential !== "string" || !/^[a-z][a-z0-9-]{0,63}$/.test(value.credential))) return null;
  try {
    const url = new URL(value.url);
    if (url.protocol!=="https:" || url.username || url.password || !url.hostname || !value.url.startsWith("https://")) return null;
    return Object.freeze({method:value.method,url:value.url,accept:value.accept,contentType:value.contentType,bodyBase64:value.bodyBase64 as string,...("credential" in value?{credential:value.credential as string}:{})});
  } catch { return null; }
}
export function parseNetworkResponse(value: unknown): NetworkResponse | null {
  if (!exact(value,["status","contentType","bodyBase64"]) || typeof value.status!=="number" || !Number.isInteger(value.status) || value.status<200 || value.status>599 || value.status>=300 && value.status<400 || !header(value.contentType) || binary(value.bodyBase64,NETWORK_RESPONSE_MAX_BYTES) === null) return null;
  return Object.freeze({status:value.status,contentType:value.contentType,bodyBase64:value.bodyBase64 as string});
}
export function parseNetworkStreamStart(value: unknown): NetworkStreamStart | null {
  if (!exact(value,["type","status","contentType"]) || value.type!=="start" || typeof value.status!=="number" || !Number.isInteger(value.status) || value.status<200 || value.status>=300 || !header(value.contentType)) return null;
  return Object.freeze({status:value.status,contentType:value.contentType});
}
export function parseNetworkStreamChunk(value: unknown): NetworkStreamChunk | null {
  if (!exact(value,["type","sequence","bodyBase64"]) || value.type!=="chunk" || typeof value.sequence!=="number" || !Number.isInteger(value.sequence) || value.sequence<1 || value.sequence>NETWORK_STREAM_MAX_FRAMES || binary(value.bodyBase64,NETWORK_STREAM_FRAME_MAX_BYTES)===null) return null;
  return Object.freeze({sequence:value.sequence,bodyBase64:value.bodyBase64 as string});
}
export function parseNetworkStreamEnd(value: unknown): NetworkStreamEnd | null {
  if (!exact(value,["type","sequence","bytes","sha256"]) || value.type!=="end" || typeof value.sequence!=="number" || !Number.isInteger(value.sequence) || value.sequence<0 || value.sequence>NETWORK_STREAM_MAX_FRAMES || typeof value.bytes!=="number" || !Number.isInteger(value.bytes) || value.bytes<0 || value.bytes>NETWORK_STREAM_MAX_BYTES || (value.sequence===0)!==(value.bytes===0) || typeof value.sha256!=="string" || !/^[a-f0-9]{64}$/.test(value.sha256)) return null;
  return Object.freeze({sequence:value.sequence,bytes:value.bytes,sha256:value.sha256});
}
export function parseNetworkStreamError(value: unknown): AppNetworkFailure | null {
  if (!exact(value,["type","code","retryable"]) || value.type!=="error" || value.retryable!==false || typeof value.code!=="string") return null;
  return value.code==="uncertain" || value.code==="unavailable" || value.code==="busy" || value.code==="invalid_request" || value.code==="not_ready" || value.code==="unsupported" ? value.code : null;
}
export function decodeNetworkStreamFrame(value: string): Uint8Array {
  const decoded = binary(value,NETWORK_STREAM_FRAME_MAX_BYTES);
  if (decoded === null) throw new AppNetworkError("uncertain");
  const bytes = new Uint8Array(decoded.length);
  for (let i=0;i<decoded.length;i++) bytes[i]=decoded.charCodeAt(i);
  return bytes;
}
