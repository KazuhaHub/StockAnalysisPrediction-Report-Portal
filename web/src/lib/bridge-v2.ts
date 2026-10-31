import { AppNetworkError, parseNetworkRequest, parseNetworkResponse, type NetworkRequest, type NetworkResponse, type AppNetworkFailure } from "./network-v2";
export { AppNetworkError, encodeNetworkBody, decodeNetworkBody, type NetworkRequest, type NetworkResponse } from "./network-v2";
import {
  createPageActions,
  parseAppPage,
  type AppPage,
  type PageActionSnapshot,
} from "./page-v2";
export type { AppPage, AppPageAction, PageActionSnapshot } from "./page-v2";
// Experimental presentation and own-backend transport. No token or legacy fallback.
import {
  DESCRIPTOR_CAPABILITIES,
  DESCRIPTOR_LAYOUTS,
  DESCRIPTOR_LOCALES,
  DESCRIPTOR_PATTERNS,
} from "./descriptor-contract.generated";

import {
  parseRouteQueryRules,
  parseRouteSearch,
  serializeRouteQuery,
  type RouteQueryValues,
  type RouteQueryRule,
} from "./route-query-v2";
export interface BridgeNavigationOptions {
  readonly replace?: boolean;
  readonly query?: RouteQueryValues;
}
export const BRIDGE_PROTOCOL = "portal.app.bridge/2-dev.8";
export const BRIDGE_DOCUMENT_MAX_HEIGHT = 100_000;
export const BRIDGE_DEADLINE_MS = 10_000;
const RETRY_MS = 200;
const MAX_HELLOS = 4;
const MAX_REVISION = 9223372036854775807n;
export type BridgeState =
  | "waiting"
  | "applying"
  | "ready"
  | "failed"
  | "closed";
export type BridgeFailure =
  | "invalid_context"
  | "unsupported_environment"
  | "protocol_error"
  | "handshake_timeout"
  | "presentation_timeout"
  | "presentation_failed"
  | "transport_error";
export interface BridgeBinding {
  readonly installationId: string;
  readonly generation: string;
  readonly policyRevision: string;
  readonly releaseSha256: string;
}
export interface BridgeRoute {
  readonly query?: readonly RouteQueryRule[];
  readonly id: string;
  readonly path: string;
  readonly layout: (typeof DESCRIPTOR_LAYOUTS)[number];
}
export interface BridgeNavigation {
  readonly search: string;
  readonly path: string;
  readonly routes: readonly BridgeRoute[];
}
export interface BridgePresentation {
  readonly navigation?: BridgeNavigation;
  readonly locale: (typeof DESCRIPTOR_LOCALES)[number];
  readonly timeZone: string;
  readonly layout: (typeof DESCRIPTOR_LAYOUTS)[number];
  readonly theme: {
    readonly dark: boolean;
    readonly colorPrimary: string;
    readonly colorBg: string;
    readonly colorText: string;
    readonly colorBorder: string;
    readonly colorBgLayout: string;
    readonly borderRadius: number;
  };
}
export interface BridgeContext {
  readonly binding: Readonly<BridgeBinding>;
  readonly capabilities: readonly string[];
  readonly backendAvailable: boolean;
}
export interface BridgeBackendRequest {
  readonly method: string;
  readonly path: string;
  readonly body: string;
}
export interface BridgeBackendResponse {
  readonly status: number;
  readonly body: string;
}
export interface BridgeReportQuery {
  readonly symbol?: string;
  readonly q?: string;
  readonly kind?: string;
  readonly subtype?: string;
  readonly date?: string;
  readonly since?: string;
  readonly until?: string;
  readonly limit?: number;
  readonly offset?: number;
}
export type BridgeReportRequest =
  | { readonly kind: "query"; readonly query: BridgeReportQuery }
  | { readonly kind: "get"; readonly id: number };
export interface BridgeReportSummary {
  readonly id: number;
  readonly runId: string;
  readonly symbol: string;
  readonly name: string;
  readonly date: string;
  readonly time: string;
  readonly kind: string;
  readonly subtype: string;
  readonly title: string;
  readonly source: string;
  readonly version: string;
}
export interface BridgeReportDetail extends BridgeReportSummary {
  readonly bodyMarkdown: string;
}
export type BridgeReportResponse =
  | {
      readonly kind: "query";
      readonly count: number;
      readonly total: number;
      readonly offset: number;
      readonly limit: number;
      readonly items: readonly BridgeReportSummary[];
    }
  | { readonly kind: "get"; readonly report: BridgeReportDetail };
export interface BridgeObjectSummary {
  readonly id: string;
  readonly collection: string;
  readonly label: string;
  readonly mediaType: string;
  readonly sizeBytes: number;
  readonly revision: number;
  readonly createdAt: number;
  readonly updatedAt: number;
}
export type BridgeObjectRequest =
  | {
      readonly action: "list";
      readonly collection: string;
      readonly cursor?: string;
      readonly limit?: number;
    }
  | { readonly action: "get"; readonly id: string }
  | {
      readonly action: "create";
      readonly requestId: string;
      readonly collection: string;
      readonly label: string;
      readonly mediaType: string;
      readonly contentBase64: string;
    }
  | {
      readonly action: "update";
      readonly id: string;
      readonly expectedRevision: number;
      readonly label: string;
      readonly mediaType: string;
      readonly contentBase64: string;
    }
  | {
      readonly action: "delete";
      readonly id: string;
      readonly expectedRevision: number;
    };
export type BridgeObjectResponse =
  | {
      readonly action: "list";
      readonly items: readonly BridgeObjectSummary[];
      readonly nextCursor?: string;
    }
  | {
      readonly action: "get";
      readonly object: BridgeObjectSummary;
      readonly contentBase64: string;
    }
  | {
      readonly action: "create" | "update";
      readonly object: BridgeObjectSummary;
    }
  | { readonly action: "delete"; readonly id: string };
export type BridgeBackendFailure =
  | "not_ready"
  | "unsupported"
  | "invalid_request"
  | "busy"
  | "unavailable"
  | "uncertain";
const backendFailures = new Set<BridgeBackendFailure>([
  "not_ready",
  "unsupported",
  "invalid_request",
  "busy",
  "unavailable",
  "uncertain",
]);
const networkFailures = new Set<AppNetworkFailure>(backendFailures);
const objectFailures = new Set<BridgeObjectFailure>([
  ...backendFailures,
  "conflict",
  "storage_full",
]);
export class BridgeBackendError extends Error {
  readonly retryable = false;
  constructor(readonly code: BridgeBackendFailure) {
    super(`Application request: ${code}`);
    this.name = "BridgeBackendError";
  }
}
export class BridgeReportError extends Error {
  readonly retryable = false;
  constructor(readonly code: BridgeBackendFailure) {
    super(`Application reports: ${code}`);
    this.name = "BridgeReportError";
  }
}
export type BridgeObjectFailure =
  | BridgeBackendFailure
  | "conflict"
  | "storage_full";
export class BridgeObjectError extends Error {
  readonly retryable = false;
  constructor(readonly code: BridgeObjectFailure) {
    super(`Application objects: ${code}`);
    this.name = "BridgeObjectError";
  }
}
export function parseBridgeBackendRequest(
  value: unknown,
): BridgeBackendRequest | null {
  if (
    !exact(value, ["method", "path", "body"]) ||
    !isString(value.method) ||
    !["GET", "POST", "PUT", "DELETE"].includes(value.method) ||
    !isString(value.path) ||
    value.path.length > 2048 ||
    !value.path.startsWith("/v1/") ||
    /[\\#\x00-\x20\x7f-\uffff]/.test(value.path) ||
    value.path.split("?")[0] === "/v1/handshake" ||
    value.path.startsWith("/v1/handshake/") ||
    !isString(value.body) ||
    new TextEncoder().encode(value.body).length > 65536 ||
    (value.method === "GET" && value.body !== "")
  )
    return null;
  try {
    if (value.body !== "") JSON.parse(value.body);
  } catch {
    return null;
  }
  return Object.freeze({
    method: value.method,
    path: value.path,
    body: value.body,
  });
}
export function parseBridgeBackendResponse(
  value: unknown,
): BridgeBackendResponse | null {
  if (
    !exact(value, ["status", "body"]) ||
    typeof value.status !== "number" ||
    !Number.isInteger(value.status) ||
    !(
      (value.status >= 200 && value.status < 300) ||
      [400, 403, 404, 409, 422, 429].includes(value.status)
    ) ||
    !isString(value.body) ||
    value.body === "" ||
    new TextEncoder().encode(value.body).length > 1048576
  )
    return null;
  try {
    JSON.parse(value.body);
  } catch {
    return null;
  }
  return Object.freeze({ status: value.status, body: value.body });
}
const reportQueryKeys = [
  "symbol",
  "q",
  "kind",
  "subtype",
  "date",
  "since",
  "until",
  "limit",
  "offset",
];
function reportText(value: unknown, limit = 512): value is string {
  return isString(value) && value.length <= limit && !/[\x00\r\n]/.test(value);
}
function reportDate(value: unknown): value is string {
  return reportText(value, 32) && (value === "" || value === "today" || /^\d{4}-\d{2}-\d{2}$/.test(value));
}
function reportBody(value: unknown): value is string {
  return isString(value) && value.length <= 1_048_576 && !value.includes("\x00");
}
function integer(value: unknown): value is number {
  return typeof value === "number" && Number.isInteger(value);
}
export function parseBridgeReportRequest(
  value: unknown,
): BridgeReportRequest | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const kind = (value as Record<string, unknown>).kind;
  if (kind === "get") {
    if (!exact(value, ["kind", "id"])) return null;
    return typeof value.id === "number" &&
      Number.isSafeInteger(value.id) &&
      value.id > 0
      ? Object.freeze({ kind: "get", id: value.id })
      : null;
  }
  if (kind !== "query" || !exact(value, ["kind", "query"])) return null;
  const query = value.query;
  if (!query || typeof query !== "object" || Array.isArray(query)) return null;
  const q = query as Record<string, unknown>;
  if (
    !Object.keys(q).every((key) => reportQueryKeys.includes(key)) ||
    !["symbol", "q", "kind", "subtype"].every(
      (key) => q[key] === undefined || reportText(q[key]),
    ) ||
    !["date", "since", "until"].every(
      (key) => q[key] === undefined || reportDate(q[key]),
    ) ||
    (q.limit !== undefined &&
      (!integer(q.limit) || q.limit < 1 || q.limit > 50)) ||
    (q.offset !== undefined &&
      (!integer(q.offset) || q.offset < 0 || q.offset > 1_000_000))
  )
    return null;
  return Object.freeze({
    kind: "query",
    query: Object.freeze({ ...q }) as BridgeReportQuery,
  });
}
function parseBridgeReportSummary(value: unknown): BridgeReportSummary | null {
  if (
    !value ||
    typeof value !== "object" ||
    Array.isArray(value) ||
    !exact(value, [
      "id",
      "runId",
      "symbol",
      "name",
      "date",
      "time",
      "kind",
      "subtype",
      "title",
      "source",
      "version",
    ]) ||
    typeof value.id !== "number" ||
    !Number.isSafeInteger(value.id) ||
    value.id <= 0
  )
    return null;
  for (const key of [
    "runId",
    "symbol",
    "name",
    "date",
    "time",
    "kind",
    "subtype",
    "title",
    "source",
    "version",
  ]) {
    if (!reportText(value[key])) return null;
  }
  return Object.freeze({
    id: value.id,
    runId: value.runId as string,
    symbol: value.symbol as string,
    name: value.name as string,
    date: value.date as string,
    time: value.time as string,
    kind: value.kind as string,
    subtype: value.subtype as string,
    title: value.title as string,
    source: value.source as string,
    version: value.version as string,
  });
}
export function parseBridgeReportResponse(
  value: unknown,
): BridgeReportResponse | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const kind = (value as Record<string, unknown>).kind;
  if (kind === "get") {
    if (!exact(value, ["kind", "report"])) return null;
    const raw = value.report;
    if (!raw || typeof raw !== "object" || Array.isArray(raw)) return null;
    const { bodyMarkdown, ...summaryValue } = raw as Record<string, unknown>;
    const summary = parseBridgeReportSummary(summaryValue);
    if (!summary || !reportBody(bodyMarkdown)) return null;
    return Object.freeze({
      kind: "get",
      report: Object.freeze({ ...summary, bodyMarkdown }),
    });
  }
  if (
    kind !== "query" ||
    !exact(value, ["kind", "count", "total", "offset", "limit", "items"]) ||
    !integer(value.count) ||
    value.count < 0 ||
    value.count > 50 ||
    !integer(value.total) ||
    value.total < 0 ||
    !integer(value.offset) ||
    value.offset < 0 ||
    !integer(value.limit) ||
    value.limit < 1 ||
    value.limit > 50 ||
    !Array.isArray(value.items) ||
    value.items.length !== value.count
  )
    return null;
  const items = value.items.map(parseBridgeReportSummary);
  if (items.some((item) => item === null)) return null;
  return Object.freeze({
    kind: "query",
    count: value.count,
    total: value.total,
    offset: value.offset,
    limit: value.limit,
    items: Object.freeze(items as BridgeReportSummary[]),
  });
}
const objectIDPattern =
  /^[a-f0-9]{8}(-[a-f0-9]{4}){3}-[a-f0-9]{12}$/;
const objectCollectionPattern = /^[a-z0-9_]{1,32}$/;
const objectRequestPattern = /^[A-Za-z0-9._:-]{1,128}$/;
const objectMediaTypePattern =
  /^[a-z0-9!#$&^_.+-]+\/[a-z0-9!#$&^_.+-]+$/;
function objectText(value: unknown, limit: number): value is string {
  if (!isString(value) || value.length > limit) return false;
  for (const char of value) {
    const code = char.codePointAt(0)!;
    if (code < 0x20 || code === 0x7f) return false;
  }
  return true;
}
function objectContent(value: unknown): value is string {
  if (!isString(value) || value.length > 4 * Math.ceil(1_048_576 / 3)) {
    return false;
  }
  try {
    const decoded = atob(value);
    return decoded.length <= 1_048_576 && btoa(decoded) === value;
  } catch {
    return false;
  }
}
function objectCursor(value: unknown): value is string {
  if (!isString(value) || value.length > 288) return false;
  const [millis, id, extra] = value.split(":");
  return (
    extra === undefined &&
    /^[1-9][0-9]{0,18}$/.test(millis) &&
    BigInt(millis) <= MAX_REVISION &&
    objectIDPattern.test(id)
  );
}
export function parseBridgeObjectRequest(
  value: unknown,
): BridgeObjectRequest | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const action = (value as Record<string, unknown>).action;
  if (action === "list") {
    if (
      !exact(value, ["action", "collection", "cursor", "limit"]) &&
      !exact(value, ["action", "collection", "cursor"]) &&
      !exact(value, ["action", "collection", "limit"]) &&
      !exact(value, ["action", "collection"])
    )
      return null;
    if (
      !isString(value.collection) ||
      !objectCollectionPattern.test(value.collection) ||
      (value.cursor !== undefined && !objectCursor(value.cursor)) ||
      (value.limit !== undefined &&
        (!integer(value.limit) || value.limit < 1 || value.limit > 50))
    )
      return null;
    return Object.freeze({
      action: "list",
      collection: value.collection,
      ...(value.cursor === undefined ? {} : { cursor: value.cursor }),
      ...(value.limit === undefined ? {} : { limit: value.limit }),
    });
  }
  if (action === "get") {
    if (!exact(value, ["action", "id"]) || !isString(value.id) || !objectIDPattern.test(value.id))
      return null;
    return Object.freeze({ action: "get", id: value.id });
  }
  if (action === "create") {
    if (
      !exact(value, [
        "action",
        "requestId",
        "collection",
        "label",
        "mediaType",
        "contentBase64",
      ]) ||
      !isString(value.requestId) ||
      !objectRequestPattern.test(value.requestId) ||
      !isString(value.collection) ||
      !objectCollectionPattern.test(value.collection) ||
      !objectText(value.label, 256) ||
      !value.label ||
      !isString(value.mediaType) ||
      !objectMediaTypePattern.test(value.mediaType) ||
      !objectContent(value.contentBase64)
    )
      return null;
    return Object.freeze({
      action: "create",
      requestId: value.requestId,
      collection: value.collection,
      label: value.label,
      mediaType: value.mediaType,
      contentBase64: value.contentBase64,
    });
  }
  if (action === "update") {
    if (
      !exact(value, [
        "action",
        "id",
        "expectedRevision",
        "label",
        "mediaType",
        "contentBase64",
      ]) ||
      !isString(value.id) ||
      !objectIDPattern.test(value.id) ||
      !integer(value.expectedRevision) ||
      value.expectedRevision < 1 ||
      !objectText(value.label, 256) ||
      !value.label ||
      !isString(value.mediaType) ||
      !objectMediaTypePattern.test(value.mediaType) ||
      !objectContent(value.contentBase64)
    )
      return null;
    return Object.freeze({
      action: "update",
      id: value.id,
      expectedRevision: value.expectedRevision,
      label: value.label,
      mediaType: value.mediaType,
      contentBase64: value.contentBase64,
    });
  }
  if (action === "delete") {
    if (
      !exact(value, ["action", "id", "expectedRevision"]) ||
      !isString(value.id) ||
      !objectIDPattern.test(value.id) ||
      !integer(value.expectedRevision) ||
      value.expectedRevision < 1
    )
      return null;
    return Object.freeze({
      action: "delete",
      id: value.id,
      expectedRevision: value.expectedRevision,
    });
  }
  return null;
}
function parseBridgeObjectSummary(
  value: unknown,
): BridgeObjectSummary | null {
  if (
    !exact(value, [
      "id",
      "collection",
      "label",
      "mediaType",
      "sizeBytes",
      "revision",
      "createdAt",
      "updatedAt",
    ]) ||
    !isString(value.id) ||
    !objectIDPattern.test(value.id) ||
    !isString(value.collection) ||
    !objectCollectionPattern.test(value.collection) ||
    !objectText(value.label, 256) ||
    !value.label ||
    !isString(value.mediaType) ||
    !objectMediaTypePattern.test(value.mediaType) ||
    !integer(value.sizeBytes) ||
    value.sizeBytes < 0 ||
    value.sizeBytes > 1_048_576 ||
    !integer(value.revision) ||
    value.revision < 1 ||
    !integer(value.createdAt) ||
    value.createdAt < 0 ||
    !integer(value.updatedAt) ||
    value.updatedAt < value.createdAt
  )
    return null;
  return Object.freeze({
    id: value.id,
    collection: value.collection,
    label: value.label,
    mediaType: value.mediaType,
    sizeBytes: value.sizeBytes,
    revision: value.revision,
    createdAt: value.createdAt,
    updatedAt: value.updatedAt,
  });
}
export function parseBridgeObjectResponse(
  value: unknown,
): BridgeObjectResponse | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const action = (value as Record<string, unknown>).action;
  if (action === "list") {
    if (
      (!exact(value, ["action", "items", "nextCursor"]) &&
        !exact(value, ["action", "items"])) ||
      !Array.isArray(value.items) ||
      value.items.length > 50 ||
      (value.nextCursor !== undefined && !objectCursor(value.nextCursor))
    )
      return null;
    const items = value.items.map(parseBridgeObjectSummary);
    if (items.some((item) => item === null)) return null;
    return Object.freeze({
      action: "list",
      items: Object.freeze(items as BridgeObjectSummary[]),
      ...(value.nextCursor === undefined
        ? {}
        : { nextCursor: value.nextCursor }),
    });
  }
  if (action === "get") {
    if (!exact(value, ["action", "object", "contentBase64"])) return null;
    const object = parseBridgeObjectSummary(value.object);
    if (
      !object ||
      !objectContent(value.contentBase64) ||
      atob(value.contentBase64).length !== object.sizeBytes
    )
      return null;
    return Object.freeze({
      action: "get",
      object,
      contentBase64: value.contentBase64,
    });
  }
  if (action === "create" || action === "update") {
    if (!exact(value, ["action", "object"])) return null;
    const object = parseBridgeObjectSummary(value.object);
    return object ? Object.freeze({ action, object }) : null;
  }
  if (action === "delete") {
    if (
      !exact(value, ["action", "id"]) ||
      !isString(value.id) ||
      !objectIDPattern.test(value.id)
    )
      return null;
    return Object.freeze({ action: "delete", id: value.id });
  }
  return null;
}
interface Session {
  state(): BridgeState;
  close(): void;
}
interface BaseOptions {
  localWindow: Window;
  onFailure?: (code: BridgeFailure) => void;
}
export interface BridgeHostOptions extends BaseOptions {
  frameWindow: Window;
  binding: BridgeBinding;
  presentation: BridgePresentation;
  capabilities: readonly string[];
  onReady?: (presentation: BridgePresentation) => void;
  onBackendRequest?: (
    request: BridgeBackendRequest,
    signal: AbortSignal,
  ) => Promise<BridgeBackendResponse>;
  onNetworkRequest?: (request: NetworkRequest, signal: AbortSignal) => Promise<NetworkResponse>;
  onReportRequest?: (
    request: BridgeReportRequest,
    signal: AbortSignal,
  ) => Promise<BridgeReportResponse>;
  onObjectRequest?: (
    request: BridgeObjectRequest,
    signal: AbortSignal,
  ) => Promise<BridgeObjectResponse>;
  onDocumentHeight?: (height: number) => void;
  onPageChange?: (snapshot: PageActionSnapshot | null) => void;
  onDraftChange?: (dirty: boolean) => void;
  onNavigate?: (path: string, replace: boolean, search: string) => void;
}
export interface BridgeClientOptions extends BaseOptions {
  onReset?: () => void;
  parentWindow: Window;
  parentOrigin: string;
  onPresentation: (
    presentation: BridgePresentation,
    context: BridgeContext,
    signal: AbortSignal,
  ) => void | Promise<void>;
  onReady?: (presentation: BridgePresentation) => void;
}

const bindingKeys = [
  "installationId",
  "generation",
  "policyRevision",
  "releaseSha256",
];
const envelopeKeys = [
  "protocol",
  ...bindingKeys,
  "hostNonce",
  "childNonce",
  "channelNonce",
];
const colors = [
  "colorPrimary",
  "colorBg",
  "colorText",
  "colorBorder",
  "colorBgLayout",
] as const;
const capabilities = new Set<string>(DESCRIPTOR_CAPABILITIES);
const failures = new Set<BridgeFailure>([
  "invalid_context",
  "unsupported_environment",
  "protocol_error",
  "handshake_timeout",
  "presentation_timeout",
  "presentation_failed",
  "transport_error",
]);
const isString = (value: unknown): value is string => typeof value === "string";
function exact(
  value: unknown,
  keys: readonly string[],
): value is Record<string, unknown> {
  return (
    value !== null &&
    typeof value === "object" &&
    !Array.isArray(value) &&
    Object.keys(value).length === keys.length &&
    keys.every((key) => Object.prototype.hasOwnProperty.call(value, key))
  );
}
function decimal(value: unknown): value is string {
  return (
    isString(value) &&
    /^[1-9][0-9]{0,18}$/.test(value) &&
    BigInt(value) <= MAX_REVISION
  );
}
function nonce(value: unknown): value is string {
  return isString(value) && /^[a-f0-9]{32}$/.test(value);
}
function randomNonce(): string {
  return Array.from(
    globalThis.crypto.getRandomValues(new Uint8Array(16)),
    (byte) => byte.toString(16).padStart(2, "0"),
  ).join("");
}
function bindingOf(value: unknown): Readonly<BridgeBinding> | null {
  if (
    !exact(value, bindingKeys) ||
    !isString(value.installationId) ||
    !/^[a-f0-9]{8}(-[a-f0-9]{4}){3}-[a-f0-9]{12}$/.test(value.installationId) ||
    !decimal(value.generation) ||
    !decimal(value.policyRevision) ||
    !isString(value.releaseSha256) ||
    !/^[a-f0-9]{64}$/.test(value.releaseSha256)
  )
    return null;
  return Object.freeze({
    installationId: value.installationId,
    generation: value.generation,
    policyRevision: value.policyRevision,
    releaseSha256: value.releaseSha256,
  });
}
function validColor(value: unknown): value is string {
  if (!isString(value) || value.length > 48) return false;
  if (/^#(?:[a-f0-9]{6}|[a-f0-9]{8})$/i.test(value)) return true;
  const match =
    /^(rgb|rgba)\(([0-9]{1,3}), ?([0-9]{1,3}), ?([0-9]{1,3})(?:, ?(0(?:\.[0-9]{1,4})?|1(?:\.0{1,4})?))?\)$/.exec(
      value,
    );
  return (
    !!match &&
    [match[2], match[3], match[4]].every((part) => Number(part) <= 255) &&
    (match[1] === "rgba") === (match[5] !== undefined)
  );
}
export function parseBridgeRoutes(
  value: unknown,
): readonly BridgeRoute[] | null {
  if (!Array.isArray(value) || value.length < 1 || value.length > 32)
    return null;
  const ids = new Set<string>(),
    paths = new Set<string>();
  const routes: BridgeRoute[] = [];
  for (const route of value) {
    if (
      (!exact(route, ["id", "path", "layout"]) &&
        !exact(route, ["id", "path", "layout", "query"])) ||
      !isString(route.id) ||
      /\s/.test(route.id) ||
      !new RegExp(DESCRIPTOR_PATTERNS.slug).test(route.id) ||
      !isString(route.path) ||
      /\s/.test(route.path) ||
      route.path.length > 257 ||
      !(
        route.path === "/" ||
        (route.path.startsWith("/") &&
          new RegExp(DESCRIPTOR_PATTERNS.path).test(route.path.slice(1)) &&
          !route.path.includes("..") &&
          !route.path.split("/").includes(".") &&
          !route.path.includes("//") &&
          !route.path.endsWith("/"))
      ) ||
      !isString(route.layout) ||
      !(DESCRIPTOR_LAYOUTS as readonly string[]).includes(route.layout) ||
      ids.has(route.id) ||
      paths.has(route.path)
    )
      return null;
    const query = parseRouteQueryRules(route.query);
    if (!query) return null;
    ids.add(route.id);
    paths.add(route.path);
    routes.push(
      Object.freeze({
        ...(query.length ? { query } : {}),
        id: route.id,
        path: route.path,
        layout: route.layout as BridgeRoute["layout"],
      }),
    );
  }
  return paths.has("/") ? Object.freeze(routes) : null;
}
function parseNavigation(value: unknown): BridgeNavigation | null {
  if (!exact(value, ["path", "search", "routes"]) || !isString(value.path))
    return null;
  const routes = parseBridgeRoutes(value.routes);
  const route = routes?.find((route) => route.path === value.path);
  if (!routes || !route) return null;
  const query = parseRouteSearch(route.query ?? [], value.search);
  return query
    ? Object.freeze({ path: value.path, search: query.search, routes })
    : null;
}
export function parseBridgePresentation(
  value: unknown,
): BridgePresentation | null {
  if (
    (!exact(value, ["locale", "timeZone", "layout", "theme"]) &&
      !exact(value, ["locale", "timeZone", "layout", "theme", "navigation"])) ||
    !isString(value.locale) ||
    !(DESCRIPTOR_LOCALES as readonly string[]).includes(value.locale) ||
    !isString(value.layout) ||
    !(DESCRIPTOR_LAYOUTS as readonly string[]).includes(value.layout) ||
    !isString(value.timeZone) ||
    !/^[A-Za-z0-9_+/-]{1,64}$/.test(value.timeZone)
  )
    return null;
  try {
    new Intl.DateTimeFormat("en-US", { timeZone: value.timeZone });
  } catch {
    return null;
  }
  const navigation =
    "navigation" in value ? parseNavigation(value.navigation) : undefined;
  if (
    navigation === null ||
    (navigation &&
      navigation.routes.find((route) => route.path === navigation.path)
        ?.layout !== value.layout)
  )
    return null;
  const theme = value.theme;
  if (
    !exact(theme, ["dark", ...colors, "borderRadius"]) ||
    typeof theme.dark !== "boolean" ||
    !colors.every((key) => validColor(theme[key])) ||
    typeof theme.borderRadius !== "number" ||
    !Number.isFinite(theme.borderRadius) ||
    theme.borderRadius < 0 ||
    theme.borderRadius > 32
  )
    return null;
  return Object.freeze({
    ...(navigation ? { navigation } : {}),
    locale: value.locale as BridgePresentation["locale"],
    timeZone: value.timeZone,
    layout: value.layout as BridgePresentation["layout"],
    theme: Object.freeze({
      dark: theme.dark,
      colorPrimary: theme.colorPrimary as string,
      colorBg: theme.colorBg as string,
      colorText: theme.colorText as string,
      colorBorder: theme.colorBorder as string,
      colorBgLayout: theme.colorBgLayout as string,
      borderRadius: theme.borderRadius,
    }),
  });
}
function capabilityList(value: unknown): readonly string[] | null {
  return Array.isArray(value) &&
    value.length <= capabilities.size &&
    value.every((item) => isString(item) && capabilities.has(item)) &&
    new Set(value).size === value.length
    ? Object.freeze([...value])
    : null;
}
function closePort(port?: MessagePort) {
  try {
    if (port) {
      port.onmessage = null;
      port.onmessageerror = null;
      port.close();
    }
  } catch {
    /* Detached or already closed. */
  }
}
function closeTransfers(event: MessageEvent) {
  for (const port of event.ports.slice(0, 8)) closePort(port);
}
function notifyFailure(
  callback: BaseOptions["onFailure"],
  code: BridgeFailure,
) {
  try {
    callback?.(code);
  } catch {
    /* Never echo callback errors across the bridge. */
  }
}
function parentOrigin(value: string): boolean {
  try {
    const url = new URL(value);
    return ["http:", "https:"].includes(url.protocol) && url.origin === value;
  } catch {
    return false;
  }
}

export function createBridgeHost(
  options: BridgeHostOptions,
): Session & { updatePresentation(value: unknown): boolean } {
  const { localWindow, frameWindow, onReady, onFailure } = options;
  let state: BridgeState = "waiting",
    port: MessagePort | undefined,
    outbound: MessagePort | undefined;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let currentEnvelope: Record<string, string> | undefined;
  let latest = parseBridgePresentation(options.presentation),
    current: BridgePresentation | null = null;
  const binding = bindingOf(options.binding),
    granted = capabilityList(options.capabilities),
    seen = new Set<string>();
  let hostNonce = "",
    revision = 0n,
    inflight = false,
    dirty = false,
    announced = false;
  let lastRequest = 0n;
  let lastNetworkRequest = 0n;
  let reportIssued = 0n;
  let objectIssued = 0n;
  let lastMeasure = 0n,
    measureWindow = 0,
    measureCount = 0;
  let lastNavigation = 0n,
    navigationWindow = 0,
    navigationCount = 0;
  let lastDraft = 0n,
    draftWindow = 0,
    draftCount = 0;
  let heightTimer: ReturnType<typeof setTimeout> | undefined,
    lastHeightAt = -Infinity;
  let pendingHeight:
    | { height: number; presentationRevision: string }
    | undefined;
  const requests = new Map<
    string,
    { abort: AbortController; timer: ReturnType<typeof setTimeout> }
  >();
  const networkRequests = new Map<
    string,
    { abort: AbortController; timer: ReturnType<typeof setTimeout> }
  >();
  const reportRequests = new Map<
    string,
    { abort: AbortController; timer: ReturnType<typeof setTimeout> }
  >();
  const objectRequests = new Map<
    string,
    { abort: AbortController; timer: ReturnType<typeof setTimeout> }
  >();
  let lastDescription = 0n,
    pageRevision = 0n,
    pageScope = "",
    pageInvocation = 0n,
    pageWindow = 0,
    pageCount = 0;
  let pageRequest:
    | {
        invocation: string;
        scope: string;
        pageRevision: string;
        resolve(): void;
        reject(): void;
        dispose(): void;
      }
    | undefined;
  const pageActions = createPageActions({
    publish: (snapshot) => options.onPageChange?.(snapshot),
    execute: (id, signal) =>
      new Promise<void>((resolve, reject) => {
        if (
          !port ||
          !currentEnvelope ||
          inflight ||
          !announced ||
          pageInvocation === MAX_REVISION
        ) {
          reject();
          return;
        }
        const invocation = (++pageInvocation).toString(),
          scope = pageScope,
          described = pageRevision.toString();
        const cancel = () => {
          if (pageRequest?.invocation !== invocation) return;
          pageRequest = undefined;
          signal.removeEventListener("abort", cancel);
          try {
            port?.postMessage({
              type: "page.action.cancel",
              ...currentEnvelope,
              invocation,
              scope,
              pageRevision: described,
            });
          } catch {
            fail("transport_error");
          }
          reject();
        };
        pageRequest = {
          invocation,
          scope,
          pageRevision: described,
          resolve,
          reject,
          dispose: () => signal.removeEventListener("abort", cancel),
        };
        signal.addEventListener("abort", cancel, { once: true });
        try {
          port.postMessage({
            type: "page.action",
            ...currentEnvelope,
            invocation,
            scope,
            pageRevision: described,
            presentationRevision: revision.toString(),
            actionId: id,
          });
        } catch {
          cancel();
          fail("transport_error");
        }
      }),
  });
  pageActions.setReady(false);
  const terminal = () => state === "closed" || state === "failed";
  function dispose() {
    pageActions.close();
    clearTimeout(heightTimer);
    pendingHeight = undefined;
    for (const request of requests.values()) {
      clearTimeout(request.timer);
      request.abort.abort();
    }
    requests.clear();
    for (const request of networkRequests.values()) {
      clearTimeout(request.timer);
      request.abort.abort();
    }
    networkRequests.clear();
    for (const request of reportRequests.values()) {
      clearTimeout(request.timer);
      request.abort.abort();
    }
    reportRequests.clear();
    for (const request of objectRequests.values()) {
      clearTimeout(request.timer);
      request.abort.abort();
    }
    objectRequests.clear();
    clearTimeout(timer);
    localWindow.removeEventListener("message", onWindow);
    closePort(port);
    closePort(outbound);
    port = outbound = undefined;
  }
  function fail(code: BridgeFailure) {
    if (!terminal()) {
      state = "failed";
      dispose();
      notifyFailure(onFailure, code);
    }
  }
  function deadline() {
    clearTimeout(timer);
    timer = setTimeout(
      () => fail(announced ? "presentation_timeout" : "handshake_timeout"),
      BRIDGE_DEADLINE_MS,
    );
  }
  function postPresentation() {
    if (!port || !currentEnvelope || !latest || revision === MAX_REVISION) {
      fail("protocol_error");
      return;
    }
    pageActions.setReady(false);
    revision++;
    current = latest;
    dirty = false;
    inflight = true;
    if (announced) deadline();
    try {
      port.postMessage({
        type: "presentation.changed",
        ...currentEnvelope,
        revision: revision.toString(),
        presentation: current,
      });
    } catch {
      fail("transport_error");
    }
  }
  function onPort(event: MessageEvent) {
    if (event.ports.length) {
      closeTransfers(event);
      fail("protocol_error");
      return;
    }
    if (terminal() || !currentEnvelope) return;
    const data = event.data;
    if (
      data &&
      typeof data === "object" &&
      data.type === "navigation.request"
    ) {
      if (
        !exact(data, [
          "type",
          ...envelopeKeys,
          "revision",
          "presentationRevision",
          "path",
          "search",
          "replace",
        ])
      ) {
        fail("protocol_error");
        return;
      }
      if (!matches(data)) return;
      if (
        !announced ||
        !decimal(data.revision) ||
        !decimal(data.presentationRevision) ||
        typeof data.replace !== "boolean" ||
        !isString(data.path) ||
        !isString(data.search) ||
        data.path.length > 257
      ) {
        fail("protocol_error");
        return;
      }
      if (BigInt(data.revision) <= lastNavigation) return;
      lastNavigation = BigInt(data.revision);
      const now = Date.now();
      if (now - navigationWindow >= 1000) {
        navigationWindow = now;
        navigationCount = 0;
      }
      if (++navigationCount > 20) {
        fail("protocol_error");
        return;
      }
      if (
        inflight ||
        dirty ||
        data.presentationRevision !== revision.toString()
      )
        return;
      const route = current?.navigation?.routes.find(
        (route) => route.path === data.path,
      );
      if (!route || !parseRouteSearch(route.query ?? [], data.search)) {
        fail("protocol_error");
        return;
      }
      if (
        data.path === current!.navigation!.path &&
        data.search === current!.navigation!.search
      )
        return;
      try {
        options.onNavigate?.(data.path, data.replace, data.search);
      } catch {
        fail("protocol_error");
      }
      return;
    }
    if (data && typeof data === "object" && data.type === "page.describe") {
      if (
        !exact(data, [
          "type",
          ...envelopeKeys,
          "revision",
          "presentationRevision",
          "scope",
          "page",
        ])
      ) {
        fail("protocol_error");
        return;
      }
      if (!matches(data)) return;
      if (
        !decimal(data.revision) ||
        !decimal(data.presentationRevision) ||
        !isString(data.scope) ||
        !/^[a-f0-9]{32}$/.test(data.scope)
      ) {
        fail("protocol_error");
        return;
      }
      const parsed = data.page === null ? null : parseAppPage(data.page);
      if (data.page !== null && !parsed) {
        fail("protocol_error");
        return;
      }
      if (BigInt(data.revision) <= lastDescription) return;
      lastDescription = BigInt(data.revision);
      const now = Date.now();
      if (now - pageWindow >= 1000) {
        pageWindow = now;
        pageCount = 0;
      }
      if (++pageCount > 20) {
        fail("protocol_error");
        return;
      }
      if (
        !announced ||
        inflight ||
        data.presentationRevision !== revision.toString()
      )
        return;
      pageRevision = BigInt(data.revision);
      if (pageScope !== data.scope || !parsed) pageActions.retire();
      pageScope = data.scope;
      if (parsed) pageActions.setPage(parsed);
      return;
    }
    if (
      data &&
      typeof data === "object" &&
      data.type === "page.action.result"
    ) {
      if (
        !exact(data, [
          "type",
          ...envelopeKeys,
          "invocation",
          "scope",
          "pageRevision",
          "code",
        ])
      ) {
        fail("protocol_error");
        return;
      }
      if (!matches(data)) return;
      if (
        !decimal(data.invocation) ||
        BigInt(data.invocation) > pageInvocation ||
        !decimal(data.pageRevision) ||
        !isString(data.scope) ||
        !/^[a-f0-9]{32}$/.test(data.scope) ||
        !["complete", "uncertain", "unavailable"].includes(data.code as string)
      ) {
        fail("protocol_error");
        return;
      }
      const request = pageRequest;
      if (
        !request ||
        request.invocation !== data.invocation ||
        request.scope !== data.scope ||
        request.pageRevision !== data.pageRevision
      )
        return;
      pageRequest = undefined;
      request.dispose();
      if (data.code === "complete") request.resolve();
      else request.reject();
      return;
    }
    if (data && typeof data === "object" && data.type === "draft.changed") {
      if (!exact(data, ["type", ...envelopeKeys, "revision", "dirty"])) {
        fail("protocol_error");
        return;
      }
      if (!matches(data)) return;
      if (
        !announced ||
        !decimal(data.revision) ||
        typeof data.dirty !== "boolean"
      ) {
        fail("protocol_error");
        return;
      }
      if (BigInt(data.revision) <= lastDraft) return;
      lastDraft = BigInt(data.revision);
      const now = Date.now();
      if (now - draftWindow >= 1000) {
        draftWindow = now;
        draftCount = 0;
      }
      if (++draftCount > 20) {
        fail("protocol_error");
        return;
      }
      try {
        options.onDraftChange?.(data.dirty);
      } catch {
        fail("protocol_error");
      }
      return;
    }
    if (data && typeof data === "object" && data.type === "layout.measure") {
      if (
        !exact(data, [
          "type",
          ...envelopeKeys,
          "revision",
          "presentationRevision",
          "height",
        ])
      ) {
        fail("protocol_error");
        return;
      }
      if (!matches(data)) return;
      if (
        !decimal(data.revision) ||
        !decimal(data.presentationRevision) ||
        !Number.isInteger(data.height) ||
        typeof data.height !== "number" ||
        data.height < 0 ||
        data.height > BRIDGE_DOCUMENT_MAX_HEIGHT
      ) {
        fail("protocol_error");
        return;
      }
      if (BigInt(data.revision) <= lastMeasure) return;
      lastMeasure = BigInt(data.revision);
      const now = Date.now();
      if (now - measureWindow >= 1000) {
        measureWindow = now;
        measureCount = 0;
      }
      if (++measureCount > 20) {
        fail("protocol_error");
        return;
      }
      if (
        !announced ||
        inflight ||
        data.presentationRevision !== revision.toString() ||
        current?.layout !== "document"
      )
        return;
      pendingHeight = {
        height: data.height,
        presentationRevision: data.presentationRevision,
      };
      if (heightTimer === undefined)
        heightTimer = setTimeout(
          () => {
            heightTimer = undefined;
            const value = pendingHeight;
            pendingHeight = undefined;
            if (
              !value ||
              terminal() ||
              inflight ||
              value.presentationRevision !== revision.toString() ||
              current?.layout !== "document"
            )
              return;
            lastHeightAt = Date.now();
            try {
              options.onDocumentHeight?.(value.height);
            } catch {
              fail("protocol_error");
            }
          },
          Math.max(0, 100 - (Date.now() - lastHeightAt)),
        );
      return;
    }
    if (
      data &&
      typeof data === "object" &&
      (data.type === "objects.request" || data.type === "objects.cancel")
    ) {
      const cancel = data.type === "objects.cancel";
      if (
        !exact(data, [
          "type",
          ...envelopeKeys,
          "requestId",
          ...(cancel ? [] : ["request"]),
        ])
      ) {
        fail("protocol_error");
        return;
      }
      if (!matches(data)) return;
      if (!decimal(data.requestId) || !announced) {
        fail("protocol_error");
        return;
      }
      const id = data.requestId;
      if (cancel) {
        if (BigInt(id) > objectIssued) {
          fail("protocol_error");
          return;
        }
        const active = objectRequests.get(id);
        if (active) {
          clearTimeout(active.timer);
          objectRequests.delete(id);
          active.abort.abort();
        }
        return;
      }
      if (BigInt(id) <= objectIssued) {
        fail("protocol_error");
        return;
      }
      objectIssued = BigInt(id);
      const request = parseBridgeObjectRequest(data.request);
      if (!request) {
        fail("protocol_error");
        return;
      }
      const result = (value: Record<string, unknown>) => {
        if (terminal() || !port || !currentEnvelope) return;
        try {
          port.postMessage({
            type: "objects.result",
            ...currentEnvelope,
            requestId: id,
            ...value,
          });
        } catch {
          fail("transport_error");
        }
      };
      if (!options.onObjectRequest || !granted?.includes("storage.files")) {
        result({ code: "unsupported" });
        return;
      }
      if (objectRequests.size >= 2) {
        result({ code: "busy" });
        return;
      }
      const controller = new AbortController();
      const finish = (value: Record<string, unknown>) => {
        const active = objectRequests.get(id);
        if (!active || active.abort !== controller) return;
        clearTimeout(active.timer);
        objectRequests.delete(id);
        result(value);
      };
      const timeout = setTimeout(() => {
        finish({ code: "uncertain" });
        controller.abort();
      }, 20_000);
      objectRequests.set(id, { abort: controller, timer: timeout });
      Promise.resolve()
        .then(() => {
          if (controller.signal.aborted)
            throw new BridgeObjectError("uncertain");
          return options.onObjectRequest!(request, controller.signal);
        })
        .then(
          (value) => {
            const response = parseBridgeObjectResponse(value);
            finish(response ? { response } : { code: "uncertain" });
          },
          (error) =>
            finish({
              code:
                error instanceof BridgeObjectError &&
                objectFailures.has(error.code)
                  ? error.code
                  : "uncertain",
            }),
        );
      return;
    }
    if (
      data &&
      typeof data === "object" &&
      (data.type === "reports.request" || data.type === "reports.cancel")
    ) {
      const cancel = data.type === "reports.cancel";
      if (
        !exact(data, [
          "type",
          ...envelopeKeys,
          "requestId",
          ...(cancel ? [] : ["request"]),
        ])
      ) {
        fail("protocol_error");
        return;
      }
      if (!matches(data)) return;
      if (!decimal(data.requestId) || !announced) {
        fail("protocol_error");
        return;
      }
      const id = data.requestId;
      if (cancel) {
        if (BigInt(id) > reportIssued) {
          fail("protocol_error");
          return;
        }
        const active = reportRequests.get(id);
        if (active) {
          clearTimeout(active.timer);
          reportRequests.delete(id);
          active.abort.abort();
        }
        return;
      }
      if (BigInt(id) <= reportIssued) {
        fail("protocol_error");
        return;
      }
      reportIssued = BigInt(id);
      const request = parseBridgeReportRequest(data.request);
      if (!request) {
        fail("protocol_error");
        return;
      }
      const result = (value: Record<string, unknown>) => {
        if (terminal() || !port || !currentEnvelope) return;
        try {
          port.postMessage({
            type: "reports.result",
            ...currentEnvelope,
            requestId: id,
            ...value,
          });
        } catch {
          fail("transport_error");
        }
      };
      if (!options.onReportRequest || !granted?.includes("reports.read")) {
        result({ code: "unsupported" });
        return;
      }
      if (reportRequests.size >= 2) {
        result({ code: "busy" });
        return;
      }
      const controller = new AbortController();
      const finish = (value: Record<string, unknown>) => {
        const active = reportRequests.get(id);
        if (!active || active.abort !== controller) return;
        clearTimeout(active.timer);
        reportRequests.delete(id);
        result(value);
      };
      const timeout = setTimeout(() => {
        finish({ code: "uncertain" });
        controller.abort();
      }, 15_000);
      reportRequests.set(id, { abort: controller, timer: timeout });
      Promise.resolve()
        .then(() => {
          if (controller.signal.aborted) throw new BridgeReportError("uncertain");
          return options.onReportRequest!(request, controller.signal);
        })
        .then(
          (value) => {
            const response = parseBridgeReportResponse(value);
            finish(response ? { response } : { code: "uncertain" });
          },
          (error) =>
            finish({
              code:
                error instanceof BridgeReportError &&
                backendFailures.has(error.code)
                  ? error.code
                  : "uncertain",
            }),
        );
      return;
    }
    if (
      data &&
      typeof data === "object" &&
      (data.type === "network.request" || data.type === "network.cancel")
    ) {
      const cancel = data.type === "network.cancel";
      if (
        !exact(data, [
          "type",
          ...envelopeKeys,
          "requestId",
          ...(cancel ? [] : ["request"]),
        ])
      ) {
        fail("protocol_error");
        return;
      }
      if (!matches(data)) return;
      if (!decimal(data.requestId) || !announced) {
        fail("protocol_error");
        return;
      }
      const id = data.requestId;
      if (cancel) {
        if (BigInt(id) > lastNetworkRequest) {
          fail("protocol_error");
          return;
        }
        const active = networkRequests.get(id);
        if (active) {
          clearTimeout(active.timer);
          networkRequests.delete(id);
          active.abort.abort();
        }
        return;
      }
      if (BigInt(id) <= lastNetworkRequest) {
        fail("protocol_error");
        return;
      }
      lastNetworkRequest = BigInt(id);
      const request = parseNetworkRequest(data.request);
      if (!request) {
        fail("protocol_error");
        return;
      }
      const result = (value: Record<string, unknown>) => {
        if (terminal() || !port || !currentEnvelope) return;
        try {
          port.postMessage({
            type: "network.result",
            ...currentEnvelope,
            requestId: id,
            ...value,
          });
        } catch {
          fail("transport_error");
        }
      };
      if (!options.onNetworkRequest || !granted?.includes("network.http")) {
        result({ code: "unsupported" });
        return;
      }
      if (networkRequests.size >= 2) {
        result({ code: "busy" });
        return;
      }
      const controller = new AbortController();
      const finish = (value: Record<string, unknown>) => {
        const active = networkRequests.get(id);
        if (!active || active.abort !== controller) return;
        clearTimeout(active.timer);
        networkRequests.delete(id);
        result(value);
      };
      const timeout = setTimeout(() => {
        finish({ code: "uncertain" });
        controller.abort();
      }, 30_000);
      networkRequests.set(id, { abort: controller, timer: timeout });
      Promise.resolve()
        .then(() => {
          if (controller.signal.aborted)
            throw new AppNetworkError("uncertain");
          return options.onNetworkRequest!(request, controller.signal);
        })
        .then(
          (value) => {
            const response = parseNetworkResponse(value);
            finish(response ? { response } : { code: "uncertain" });
          },
          (error) =>
            finish({
              code:
                error instanceof AppNetworkError &&
                networkFailures.has(error.code)
                  ? error.code
                  : "uncertain",
            }),
        );
      return;
    }
    if (
      data &&
      typeof data === "object" &&
      (data.type === "backend.request" || data.type === "backend.cancel")
    ) {
      const cancel = data.type === "backend.cancel";
      if (
        !exact(data, [
          "type",
          ...envelopeKeys,
          "requestId",
          ...(cancel ? [] : ["request"]),
        ])
      ) {
        fail("protocol_error");
        return;
      }
      if (!matches(data)) return;
      if (!decimal(data.requestId) || !announced) {
        fail("protocol_error");
        return;
      }
      const id = data.requestId;
      if (cancel) {
        if (BigInt(id) > lastRequest) {
          fail("protocol_error");
          return;
        }
        const active = requests.get(id);
        if (active) {
          clearTimeout(active.timer);
          requests.delete(id);
          active.abort.abort();
        }
        return;
      }
      if (BigInt(id) <= lastRequest) {
        fail("protocol_error");
        return;
      }
      lastRequest = BigInt(id);
      const request = parseBridgeBackendRequest(data.request);
      if (!request) {
        fail("protocol_error");
        return;
      }
      const result = (value: Record<string, unknown>) => {
        if (terminal() || !port || !currentEnvelope) return;
        try {
          port.postMessage({
            type: "backend.result",
            ...currentEnvelope,
            requestId: id,
            ...value,
          });
        } catch {
          fail("transport_error");
        }
      };
      if (!options.onBackendRequest) {
        result({ code: "unsupported" });
        return;
      }
      if (requests.size >= 2) {
        result({ code: "busy" });
        return;
      }
      const controller = new AbortController();
      const finish = (value: Record<string, unknown>) => {
        const active = requests.get(id);
        if (!active || active.abort !== controller) return;
        clearTimeout(active.timer);
        requests.delete(id);
        result(value);
      };
      const timeout = setTimeout(() => {
        finish({ code: "uncertain" });
        controller.abort();
      }, 30_000);
      requests.set(id, { abort: controller, timer: timeout });
      Promise.resolve()
        .then(() => {
          if (controller.signal.aborted)
            throw new BridgeBackendError("uncertain");
          return options.onBackendRequest!(request, controller.signal);
        })
        .then(
          (value) => {
            const response = parseBridgeBackendResponse(value);
            finish(response ? { response } : { code: "uncertain" });
          },
          (error) =>
            finish({
              code:
                error instanceof BridgeBackendError &&
                backendFailures.has(error.code)
                  ? error.code
                  : "uncertain",
            }),
        );
      return;
    }
    if (
      exact(data, ["type", ...envelopeKeys, "code"]) &&
      data.type === "app:failed" &&
      matches(data)
    ) {
      fail(
        failures.has(data.code as BridgeFailure)
          ? (data.code as BridgeFailure)
          : "protocol_error",
      );
      return;
    }
    if (
      !exact(data, ["type", ...envelopeKeys, "revision"]) ||
      data.type !== "app:ready"
    ) {
      fail("protocol_error");
      return;
    }
    if (!matches(data) || data.revision !== revision.toString() || !inflight)
      return;
    inflight = false;
    if (dirty) {
      postPresentation();
      return;
    }
    clearTimeout(timer);
    state = "ready";
    pageActions.setReady(true);
    if (!announced) {
      announced = true;
      try {
        onReady?.(current!);
      } catch {
        fail("presentation_failed");
      }
    }
  }
  let challenges = 0;
  function challenge() {
    if (terminal() || announced || challenges >= MAX_HELLOS) return;
    challenges++;
    try {
      frameWindow.postMessage(
        { type: "app:challenge", protocol: BRIDGE_PROTOCOL, hostNonce },
        "*",
      );
    } catch {
      fail("transport_error");
    }
  }
  function matches(data: Record<string, unknown>) {
    return (
      !!currentEnvelope &&
      envelopeKeys.every((key) => data[key] === currentEnvelope![key])
    );
  }
  function onWindow(event: MessageEvent) {
    if (event.source !== frameWindow) return;
    closeTransfers(event);
    if (
      terminal() ||
      announced ||
      event.origin !== "null" ||
      event.ports.length
    )
      return;
    const data = event.data;
    if (
      (!exact(data, ["type", "protocols", "childNonce"]) &&
        !exact(data, ["type", "protocols", "childNonce", "hostNonce"])) ||
      data.type !== "app:hello" ||
      !nonce(data.childNonce) ||
      !Array.isArray(data.protocols) ||
      data.protocols.length < 1 ||
      data.protocols.length > 4 ||
      !data.protocols.every(
        (value) => isString(value) && /^[A-Za-z0-9/.-]{1,64}$/.test(value),
      ) ||
      new Set(data.protocols).size !== data.protocols.length ||
      !data.protocols.includes(BRIDGE_PROTOCOL)
    )
      return;
    if (
      Object.prototype.hasOwnProperty.call(data, "hostNonce") &&
      !nonce(data.hostNonce)
    )
      return;
    if (data.hostNonce !== hostNonce) {
      challenge();
      return;
    }
    if (seen.has(data.childNonce)) return;
    if (seen.size === MAX_HELLOS) {
      fail("protocol_error");
      return;
    }
    seen.add(data.childNonce);
    closePort(port);
    closePort(outbound);
    try {
      const channel = new MessageChannel();
      port = channel.port1;
      outbound = channel.port2;
      currentEnvelope = {
        protocol: BRIDGE_PROTOCOL,
        ...binding!,
        hostNonce,
        childNonce: data.childNonce,
        channelNonce: randomNonce(),
      };
      const captured = port;
      port.onmessage = (message) => {
        if (port === captured) onPort(message);
      };
      port.onmessageerror = () => {
        if (port === captured) fail("transport_error");
      };
      port.start();
      state = "applying";
      revision = 1n;
      current = latest;
      dirty = false;
      inflight = true;
      frameWindow.postMessage(
        {
          type: "app:init",
          ...currentEnvelope,
          revision: "1",
          presentation: current,
          capabilities: granted,
          backend: typeof options.onBackendRequest === "function",
        },
        "*",
        [outbound],
      );
      outbound = undefined;
    } catch {
      fail("unsupported_environment");
    }
  }
  const session = {
    state: () => state,
    close() {
      if (state !== "closed") {
        state = "closed";
        dispose();
      }
    },
    updatePresentation(value: unknown) {
      if (terminal()) return false;
      const parsed = parseBridgePresentation(value);
      if (!parsed) return false;
      if (
        JSON.stringify([
          latest?.navigation?.path,
          latest?.navigation?.search,
        ]) !==
        JSON.stringify([parsed.navigation?.path, parsed.navigation?.search])
      )
        pageActions.retire();
      latest = parsed;
      dirty = true;
      if (port && !inflight) postPresentation();
      return !terminal();
    },
  };
  if (!binding || !latest || !granted || frameWindow === localWindow) {
    fail("invalid_context");
    return session;
  }
  try {
    hostNonce = randomNonce();
    localWindow.addEventListener("message", onWindow);
    deadline();
    challenge();
  } catch {
    fail("unsupported_environment");
  }
  return session;
}

export function createBridgeClient(options: BridgeClientOptions): Session & {
  presentation(): BridgePresentation | null;
  context(): BridgeContext | null;
  requestBackend(
    request: BridgeBackendRequest,
    signal?: AbortSignal,
  ): Promise<BridgeBackendResponse>;
  requestNetwork(request: NetworkRequest, signal?: AbortSignal): Promise<NetworkResponse>;
  requestReports(
    request: BridgeReportRequest,
    signal?: AbortSignal,
  ): Promise<BridgeReportResponse>;
  requestObjects(
    request: BridgeObjectRequest,
    signal?: AbortSignal,
  ): Promise<BridgeObjectResponse>;
  describePage(
    scope: string,
    page: AppPage | null,
    handler?: (id: string, signal: AbortSignal) => void | Promise<void>,
  ): boolean;
  reportDocumentHeight(height: number): boolean;
  setDraftDirty(dirty: boolean): boolean;
  navigate(path: string, options?: BridgeNavigationOptions): boolean;
} {
  const {
    localWindow,
    parentWindow,
    parentOrigin: expectedOrigin,
    onPresentation,
    onReset,
    onReady,
    onFailure,
  } = options;
  let state: BridgeState = "waiting",
    port: MessagePort | undefined,
    envelope: Record<string, string> | undefined;
  let timer: ReturnType<typeof setTimeout> | undefined,
    retry: ReturnType<typeof setInterval> | undefined;
  let childNonce = "",
    revision = 0n,
    applied = false,
    current: BridgePresentation | null = null,
    context: BridgeContext | null = null;
  let abort = new AbortController(),
    connectionRevision = 0;
  let expectedHostNonce: string | undefined;
  const retiredHosts = new Set<string>();
  let navigationRevision = 0n,
    lastNavigationAt = -Infinity;
  let draftRevision = 0n,
    draftTimer: ReturnType<typeof setTimeout> | undefined,
    lastDraftAt = -Infinity;
  let queuedDraft: boolean | undefined, lastDraftValue: boolean | undefined;
  function clearDraft() {
    clearTimeout(draftTimer);
    draftTimer = undefined;
    queuedDraft = undefined;
    lastDraftValue = undefined;
  }
  function flushDraft() {
    draftTimer = undefined;
    const dirty = queuedDraft;
    queuedDraft = undefined;
    if (
      dirty === undefined ||
      terminal() ||
      !applied ||
      !port ||
      !envelope ||
      dirty === lastDraftValue
    )
      return;
    if (draftRevision === MAX_REVISION) {
      fail("protocol_error");
      return;
    }
    lastDraftAt = Date.now();
    lastDraftValue = dirty;
    try {
      port.postMessage({
        type: "draft.changed",
        ...envelope,
        revision: (++draftRevision).toString(),
        dirty,
      });
    } catch {
      fail("transport_error");
    }
  }
  let measureRevision = 0n,
    lastMeasureAt = -Infinity;
  let measureTimer: ReturnType<typeof setTimeout> | undefined;
  let nextMeasure: { height: number; presentationRevision: string } | undefined;
  let emittedMeasure:
    | { height: number; presentationRevision: string }
    | undefined;
  function clearMeasure() {
    clearTimeout(measureTimer);
    measureTimer = undefined;
    nextMeasure = undefined;
    emittedMeasure = undefined;
  }
  function flushMeasure() {
    measureTimer = undefined;
    const value = nextMeasure;
    nextMeasure = undefined;
    if (
      !value ||
      terminal() ||
      state !== "ready" ||
      !port ||
      !envelope ||
      current?.layout !== "document" ||
      value.presentationRevision !== revision.toString()
    )
      return;
    if (
      emittedMeasure?.height === value.height &&
      emittedMeasure.presentationRevision === value.presentationRevision
    )
      return;
    if (measureRevision === MAX_REVISION) {
      fail("protocol_error");
      return;
    }
    lastMeasureAt = Date.now();
    emittedMeasure = value;
    try {
      port.postMessage({
        type: "layout.measure",
        ...envelope,
        revision: (++measureRevision).toString(),
        ...value,
      });
    } catch {
      fail("transport_error");
    }
  }
  let issued = 0n;
  let networkIssued = 0n;
  let reportIssued = 0n;
  let objectIssued = 0n;
  const requests = new Map<
    string,
    {
      resolve: (value: BridgeBackendResponse) => void;
      reject: (error: BridgeBackendError) => void;
      dispose: () => void;
    }
  >();
  const networkRequests = new Map<
    string,
    {
      resolve: (value: NetworkResponse) => void;
      reject: (error: AppNetworkError) => void;
      dispose: () => void;
    }
  >();
  const reportRequests = new Map<
    string,
    {
      resolve: (value: BridgeReportResponse) => void;
      reject: (error: BridgeReportError) => void;
      dispose: () => void;
    }
  >();
  const objectRequests = new Map<
    string,
    {
      resolve: (value: BridgeObjectResponse) => void;
      reject: (error: BridgeObjectError) => void;
      dispose: () => void;
    }
  >();
  function retireRequests() {
    for (const request of requests.values()) {
      request.dispose();
      request.reject(new BridgeBackendError("uncertain"));
    }
    requests.clear();
    for (const request of networkRequests.values()) {
      request.dispose();
      request.reject(new AppNetworkError("uncertain"));
    }
    networkRequests.clear();
    for (const request of reportRequests.values()) {
      request.dispose();
      request.reject(new BridgeReportError("uncertain"));
    }
    reportRequests.clear();
    for (const request of objectRequests.values()) {
      request.dispose();
      request.reject(new BridgeObjectError("uncertain"));
    }
    objectRequests.clear();
  }
  let pageRevision = 0n,
    lastInvocation = 0n,
    lastPageAt = -Infinity;
  let pageTimer: ReturnType<typeof setTimeout> | undefined;
  let description:
    | {
        scope: string;
        page: AppPage | null;
        handler?: (id: string, signal: AbortSignal) => void | Promise<void>;
      }
    | undefined;
  let sentPage:
    | { scope: string; revision: string; page: AppPage | null }
    | undefined;
  let runningAction:
    | {
        invocation: string;
        scope: string;
        pageRevision: string;
        abort: AbortController;
        timer: ReturnType<typeof setTimeout>;
      }
    | undefined;
  function cancelAction() {
    if (!runningAction) return;
    const previous = runningAction;
    runningAction = undefined;
    clearTimeout(previous.timer);
    previous.abort.abort();
  }
  function clearPage() {
    clearTimeout(pageTimer);
    pageTimer = undefined;
    description = sentPage = undefined;
    cancelAction();
  }
  function flushPage() {
    pageTimer = undefined;
    if (!description || state !== "ready" || !applied || !port || !envelope)
      return;
    if (pageRevision === MAX_REVISION) {
      fail("protocol_error");
      return;
    }
    lastPageAt = Date.now();
    sentPage = {
      scope: description.scope,
      page: description.page,
      revision: (++pageRevision).toString(),
    };
    try {
      port.postMessage({
        type: "page.describe",
        ...envelope,
        revision: sentPage.revision,
        presentationRevision: revision.toString(),
        scope: description.scope,
        page: description.page,
      });
    } catch {
      fail("transport_error");
    }
  }
  function schedulePage() {
    if (pageTimer === undefined)
      pageTimer = setTimeout(
        flushPage,
        Math.max(0, 100 - (Date.now() - lastPageAt)),
      );
  }
  const terminal = () => state === "closed" || state === "failed";
  function dispose() {
    clearPage();
    clearMeasure();
    clearDraft();
    retireRequests();
    clearTimeout(timer);
    clearInterval(retry);
    localWindow.removeEventListener("message", onWindow);
    closePort(port);
    port = undefined;
    context = current = null;
    abort.abort();
  }
  function fail(code: BridgeFailure) {
    if (terminal()) return;
    try {
      if (port && envelope)
        port.postMessage({ type: "app:failed", ...envelope, code });
    } catch {
      /* Report only the bounded local code. */
    }
    state = "failed";
    dispose();
    notifyFailure(onFailure, code);
  }
  function deadline() {
    clearTimeout(timer);
    timer = setTimeout(
      () => fail(applied ? "presentation_timeout" : "handshake_timeout"),
      BRIDGE_DEADLINE_MS,
    );
  }
  function apply(presentation: BridgePresentation, next: bigint) {
    if (
      current &&
      JSON.stringify([current.navigation?.path, current.navigation?.search]) !==
        JSON.stringify([
          presentation.navigation?.path,
          presentation.navigation?.search,
        ])
    )
      clearPage();
    state = "applying";
    const connection = connectionRevision,
      applyingPort = port;
    let result: void | Promise<void>;
    try {
      result = onPresentation(presentation, context!, abort.signal);
    } catch {
      fail("presentation_failed");
      return;
    }
    Promise.resolve(result).then(
      () => {
        if (
          terminal() ||
          !port ||
          !envelope ||
          connection !== connectionRevision ||
          port !== applyingPort
        )
          return;
        revision = next;
        current = presentation;
        state = "ready";
        clearTimeout(timer);
        try {
          port.postMessage({
            type: "app:ready",
            ...envelope,
            revision: revision.toString(),
          });
          if (!applied) {
            applied = true;
            onReady?.(presentation);
          }
          schedulePage();
        } catch {
          fail("presentation_failed");
        }
      },
      () => {
        if (connection === connectionRevision) fail("presentation_failed");
      },
    );
  }
  function onPort(event: MessageEvent) {
    if (event.ports.length) {
      closeTransfers(event);
      fail("protocol_error");
      return;
    }
    if (terminal() || !envelope) return;
    const data = event.data;
    if (
      data &&
      typeof data === "object" &&
      (data.type === "page.action" || data.type === "page.action.cancel")
    ) {
      const cancel = data.type === "page.action.cancel";
      if (
        !exact(data, [
          "type",
          ...envelopeKeys,
          "invocation",
          "scope",
          "pageRevision",
          ...(cancel ? [] : ["presentationRevision", "actionId"]),
        ])
      ) {
        fail("protocol_error");
        return;
      }
      if (!envelopeKeys.every((key) => data[key] === envelope![key])) return;
      if (
        !decimal(data.invocation) ||
        !decimal(data.pageRevision) ||
        !isString(data.scope) ||
        !/^[a-f0-9]{32}$/.test(data.scope) ||
        (!cancel &&
          (!decimal(data.presentationRevision) || !isString(data.actionId)))
      ) {
        fail("protocol_error");
        return;
      }
      if (cancel) {
        if (
          runningAction?.invocation === data.invocation &&
          runningAction.scope === data.scope &&
          runningAction.pageRevision === data.pageRevision
        )
          cancelAction();
        return;
      }
      if (BigInt(data.invocation) <= lastInvocation) return;
      lastInvocation = BigInt(data.invocation);
      const action = description?.page?.actions.find(
        (action) => action.id === data.actionId,
      );
      const reply = (code: string) => {
        try {
          port?.postMessage({
            type: "page.action.result",
            ...envelope,
            invocation: data.invocation,
            scope: data.scope,
            pageRevision: data.pageRevision,
            code,
          });
        } catch {
          fail("transport_error");
        }
      };
      if (
        state !== "ready" ||
        data.presentationRevision !== revision.toString() ||
        sentPage?.revision !== data.pageRevision ||
        sentPage.scope !== data.scope ||
        description?.scope !== data.scope ||
        !description.handler ||
        !action ||
        action.disabled ||
        action.loading ||
        runningAction
      ) {
        reply("unavailable");
        return;
      }
      const abort = new AbortController();
      const running = {
        invocation: data.invocation,
        scope: data.scope,
        pageRevision: data.pageRevision,
        abort,
        timer: setTimeout(() => finish("uncertain", true), 25_000),
      };
      runningAction = running;
      function finish(code: string, expired = false) {
        if (runningAction !== running) return;
        runningAction = undefined;
        clearTimeout(running.timer);
        if (expired) abort.abort();
        reply(code);
      }
      try {
        Promise.resolve(
          description.handler(data.actionId as string, abort.signal),
        ).then(
          () => finish("complete"),
          () => finish("uncertain"),
        );
      } catch {
        finish("uncertain");
      }
      return;
    }
    if (data && typeof data === "object" && data.type === "reports.result") {
      const response = exact(data, [
        "type",
        ...envelopeKeys,
        "requestId",
        "response",
      ]);
      const failure = exact(data, [
        "type",
        ...envelopeKeys,
        "requestId",
        "code",
      ]);
      if (!response && !failure) {
        fail("protocol_error");
        return;
      }
      if (!envelopeKeys.every((key) => data[key] === envelope![key])) return;
      if (!decimal(data.requestId) || BigInt(data.requestId) > reportIssued) {
        fail("protocol_error");
        return;
      }
      const value = response ? parseBridgeReportResponse(data.response) : null;
      if (
        (response && !value) ||
        (failure && !backendFailures.has(data.code as BridgeBackendFailure))
      ) {
        fail("protocol_error");
        return;
      }
      const pending = reportRequests.get(data.requestId);
      if (!pending) return;
      reportRequests.delete(data.requestId);
      pending.dispose();
      if (value) pending.resolve(value);
      else
        pending.reject(
          new BridgeReportError(data.code as BridgeBackendFailure),
        );
      return;
    }
    if (data && typeof data === "object" && data.type === "objects.result") {
      const response = exact(data, [
        "type",
        ...envelopeKeys,
        "requestId",
        "response",
      ]);
      const failure = exact(data, [
        "type",
        ...envelopeKeys,
        "requestId",
        "code",
      ]);
      if (!response && !failure) {
        fail("protocol_error");
        return;
      }
      if (!envelopeKeys.every((key) => data[key] === envelope![key])) return;
      if (!decimal(data.requestId) || BigInt(data.requestId) > objectIssued) {
        fail("protocol_error");
        return;
      }
      const value = response ? parseBridgeObjectResponse(data.response) : null;
      if (
        (response && !value) ||
        (failure && !objectFailures.has(data.code as BridgeObjectFailure))
      ) {
        fail("protocol_error");
        return;
      }
      const pending = objectRequests.get(data.requestId);
      if (!pending) return;
      objectRequests.delete(data.requestId);
      pending.dispose();
      if (value) pending.resolve(value);
      else
        pending.reject(
          new BridgeObjectError(data.code as BridgeObjectFailure),
        );
      return;
    }
    if (data && typeof data === "object" && data.type === "network.result") {
      const response = exact(data, [
        "type",
        ...envelopeKeys,
        "requestId",
        "response",
      ]);
      const failure = exact(data, [
        "type",
        ...envelopeKeys,
        "requestId",
        "code",
      ]);
      if (!response && !failure) {
        fail("protocol_error");
        return;
      }
      if (!envelopeKeys.every((key) => data[key] === envelope![key])) return;
      if (!decimal(data.requestId) || BigInt(data.requestId) > networkIssued) {
        fail("protocol_error");
        return;
      }
      const value = response ? parseNetworkResponse(data.response) : null;
      if (
        (response && !value) ||
        (failure && !networkFailures.has(data.code as AppNetworkFailure))
      ) {
        fail("protocol_error");
        return;
      }
      const pending = networkRequests.get(data.requestId);
      if (!pending) return;
      networkRequests.delete(data.requestId);
      pending.dispose();
      if (value) pending.resolve(value);
      else
        pending.reject(
          new AppNetworkError(data.code as AppNetworkFailure),
        );
      return;
    }
    if (data && typeof data === "object" && data.type === "backend.result") {
      const response = exact(data, [
        "type",
        ...envelopeKeys,
        "requestId",
        "response",
      ]);
      const failure = exact(data, [
        "type",
        ...envelopeKeys,
        "requestId",
        "code",
      ]);
      if (!response && !failure) {
        fail("protocol_error");
        return;
      }
      if (!envelopeKeys.every((key) => data[key] === envelope![key])) return;
      if (!decimal(data.requestId) || BigInt(data.requestId) > issued) {
        fail("protocol_error");
        return;
      }
      const value = response ? parseBridgeBackendResponse(data.response) : null;
      if (
        (response && !value) ||
        (failure && !backendFailures.has(data.code as BridgeBackendFailure))
      ) {
        fail("protocol_error");
        return;
      }
      const pending = requests.get(data.requestId);
      if (!pending) return;
      requests.delete(data.requestId);
      pending.dispose();
      if (value) pending.resolve(value);
      else
        pending.reject(
          new BridgeBackendError(data.code as BridgeBackendFailure),
        );
      return;
    }
    if (
      !exact(data, ["type", ...envelopeKeys, "revision", "presentation"]) ||
      data.type !== "presentation.changed"
    ) {
      fail("protocol_error");
      return;
    }
    if (!envelopeKeys.every((key) => data[key] === envelope![key])) return;
    if (
      !decimal(data.revision) ||
      BigInt(data.revision) !== revision + 1n ||
      state !== "ready"
    ) {
      fail("protocol_error");
      return;
    }
    const presentation = parseBridgePresentation(data.presentation);
    if (!presentation) {
      fail("protocol_error");
      return;
    }
    deadline();
    apply(presentation, BigInt(data.revision));
  }
  function onWindow(event: MessageEvent) {
    if (event.source !== parentWindow || event.origin !== expectedOrigin)
      return;
    if (terminal()) {
      closeTransfers(event);
      return;
    }
    const data = event.data;
    if (data && typeof data === "object" && data.type === "app:challenge") {
      if (
        !exact(data, ["type", "protocol", "hostNonce"]) ||
        data.protocol !== BRIDGE_PROTOCOL ||
        !nonce(data.hostNonce) ||
        event.ports.length
      ) {
        closeTransfers(event);
        fail("protocol_error");
        return;
      }
      if (retiredHosts.has(data.hostNonce)) return;
      if (data.hostNonce === expectedHostNonce) {
        hello();
        return;
      }
      if (expectedHostNonce !== undefined) {
        if (retiredHosts.size >= MAX_HELLOS) {
          fail("protocol_error");
          return;
        }
        retiredHosts.add(expectedHostNonce);
        closePort(port);
        port = undefined;
        envelope = undefined;
        context = current = null;
        abort.abort();
        clearMeasure();
        clearDraft();
        clearPage();
        pageRevision = lastInvocation = 0n;
        lastPageAt = -Infinity;
        navigationRevision = 0n;
        lastNavigationAt = -Infinity;
        draftRevision = 0n;
        lastDraftAt = -Infinity;
        measureRevision = 0n;
        lastMeasureAt = -Infinity;
        retireRequests();
        issued = 0n;
        networkIssued = 0n;
        reportIssued = 0n;
        objectIssued = 0n;
        abort = new AbortController();
        connectionRevision++;
        revision = 0n;
        applied = false;
        state = "waiting";
        clearInterval(retry);
        retry = setInterval(hello, RETRY_MS);
        deadline();
        try {
          onReset?.();
        } catch {
          fail("presentation_failed");
          return;
        }
      }
      expectedHostNonce = data.hostNonce;
      hello();
      return;
    }
    if (port) {
      closeTransfers(event);
      return;
    }
    if (
      !data ||
      typeof data !== "object" ||
      data.type !== "app:init" ||
      data.childNonce !== childNonce ||
      data.hostNonce !== expectedHostNonce
    ) {
      closeTransfers(event);
      return;
    }
    if (
      !exact(data, [
        "type",
        ...envelopeKeys,
        "revision",
        "presentation",
        "capabilities",
        "backend",
      ]) ||
      data.protocol !== BRIDGE_PROTOCOL ||
      !nonce(data.hostNonce) ||
      !nonce(data.channelNonce) ||
      data.revision !== "1" ||
      event.ports.length !== 1
    ) {
      closeTransfers(event);
      fail("protocol_error");
      return;
    }
    const binding = bindingOf(
      Object.fromEntries(bindingKeys.map((key) => [key, data[key]])),
    );
    const presentation = parseBridgePresentation(data.presentation),
      granted = capabilityList(data.capabilities);
    if (
      !binding ||
      !presentation ||
      !granted ||
      typeof data.backend !== "boolean"
    ) {
      closeTransfers(event);
      fail("protocol_error");
      return;
    }
    port = event.ports[0];
    clearInterval(retry);
    envelope = Object.fromEntries(
      envelopeKeys.map((key) => [key, data[key] as string]),
    );
    context = Object.freeze({
      binding,
      capabilities: granted,
      backendAvailable: data.backend,
    });
    const captured = port;
    port.onmessage = (message) => {
      if (port === captured) onPort(message);
    };
    port.onmessageerror = () => {
      if (port === captured) fail("transport_error");
    };
    try {
      port.start();
      apply(presentation, 1n);
    } catch {
      fail("transport_error");
    }
  }
  function hello() {
    if (terminal() || port) return;
    try {
      parentWindow.postMessage(
        {
          type: "app:hello",
          protocols: [BRIDGE_PROTOCOL],
          childNonce,
          ...(expectedHostNonce === undefined
            ? {}
            : { hostNonce: expectedHostNonce }),
        },
        expectedOrigin,
      );
    } catch {
      fail("transport_error");
    }
  }
  const session = {
    state: () => state,
    presentation: () => current,
    context: () => context,
    describePage(
      scope: string,
      page: AppPage | null,
      handler?: (id: string, signal: AbortSignal) => void | Promise<void>,
    ): boolean {
      if (
        terminal() ||
        !isString(scope) ||
        !/^[a-f0-9]{32}$/.test(scope) ||
        (handler !== undefined && typeof handler !== "function")
      )
        return false;
      const parsed = page === null ? null : parseAppPage(page);
      if (page !== null && !parsed) return false;
      if (description?.scope !== scope || !parsed) cancelAction();
      const changed =
        description?.scope !== scope ||
        JSON.stringify(description.page) !== JSON.stringify(parsed);
      description = { scope, page: parsed, handler };
      if (changed) schedulePage();
      return true;
    },
    navigate(path: string, options: BridgeNavigationOptions = {}): boolean {
      if (
        terminal() ||
        !applied ||
        state !== "ready" ||
        !port ||
        !envelope ||
        !options ||
        typeof options !== "object" ||
        Array.isArray(options) ||
        Object.keys(options).some((key) => key !== "replace" && key !== "query")
      )
        return false;
      const replace = options.replace === undefined ? false : options.replace;
      const route = current?.navigation?.routes.find(
        (route) => route.path === path,
      );
      if (
        typeof replace !== "boolean" ||
        !route ||
        Date.now() - lastNavigationAt < 100
      )
        return false;
      const target = serializeRouteQuery(
        route.query ?? [],
        options.query === undefined ? {} : options.query,
      );
      if (!target) return false;
      if (
        path === current!.navigation!.path &&
        target.search === current!.navigation!.search
      )
        return true;
      if (navigationRevision === MAX_REVISION) {
        fail("protocol_error");
        return false;
      }
      lastNavigationAt = Date.now();
      try {
        port.postMessage({
          type: "navigation.request",
          ...envelope,
          revision: (++navigationRevision).toString(),
          presentationRevision: revision.toString(),
          path,
          search: target.search,
          replace,
        });
        return true;
      } catch {
        fail("transport_error");
        return false;
      }
    },
    setDraftDirty(dirty: boolean): boolean {
      if (
        terminal() ||
        !applied ||
        !port ||
        !envelope ||
        typeof dirty !== "boolean"
      )
        return false;
      queuedDraft = dirty;
      if (draftTimer === undefined)
        draftTimer = setTimeout(
          flushDraft,
          Math.max(0, 100 - (Date.now() - lastDraftAt)),
        );
      return true;
    },
    reportDocumentHeight(height: number): boolean {
      if (
        terminal() ||
        state !== "ready" ||
        !applied ||
        !port ||
        current?.layout !== "document" ||
        !Number.isInteger(height) ||
        height < 0 ||
        height > BRIDGE_DOCUMENT_MAX_HEIGHT
      )
        return false;
      nextMeasure = { height, presentationRevision: revision.toString() };
      if (measureTimer === undefined)
        measureTimer = setTimeout(
          flushMeasure,
          Math.max(0, 100 - (Date.now() - lastMeasureAt)),
        );
      return true;
    },
    requestBackend(
      value: BridgeBackendRequest,
      signal?: AbortSignal,
    ): Promise<BridgeBackendResponse> {
      if (terminal() || !applied || !port || !envelope || !context)
        return Promise.reject(new BridgeBackendError("not_ready"));
      if (!context.backendAvailable)
        return Promise.reject(new BridgeBackendError("unsupported"));
      const request = parseBridgeBackendRequest(value);
      if (!request)
        return Promise.reject(new BridgeBackendError("invalid_request"));
      if (signal?.aborted)
        return Promise.reject(new BridgeBackendError("not_ready"));
      if (requests.size >= 2 || issued === MAX_REVISION)
        return Promise.reject(new BridgeBackendError("busy"));
      const id = (++issued).toString();
      return new Promise((resolve, reject) => {
        const cancel = () => {
          const active = requests.get(id);
          if (!active) return;
          requests.delete(id);
          active.dispose();
          reject(new BridgeBackendError("uncertain"));
          try {
            port?.postMessage({
              type: "backend.cancel",
              ...envelope,
              requestId: id,
            });
          } catch {
            fail("transport_error");
          }
        };
        const timeout = setTimeout(cancel, 35_000);
        const dispose = () => {
          clearTimeout(timeout);
          signal?.removeEventListener("abort", cancel);
        };
        requests.set(id, { resolve, reject, dispose });
        signal?.addEventListener("abort", cancel, { once: true });
        try {
          port!.postMessage({
            type: "backend.request",
            ...envelope,
            requestId: id,
            request,
          });
        } catch {
          fail("transport_error");
        }
      });
    },
    requestNetwork(
      value: NetworkRequest,
      signal?: AbortSignal,
    ): Promise<NetworkResponse> {
      if (terminal() || !applied || !port || !envelope || !context)
        return Promise.reject(new AppNetworkError("not_ready"));
      if (!context.capabilities.includes("network.http"))
        return Promise.reject(new AppNetworkError("unsupported"));
      const request = parseNetworkRequest(value);
      if (!request)
        return Promise.reject(new AppNetworkError("invalid_request"));
      if (signal?.aborted)
        return Promise.reject(new AppNetworkError("not_ready"));
      if (networkRequests.size >= 2 || networkIssued === MAX_REVISION)
        return Promise.reject(new AppNetworkError("busy"));
      const id = (++networkIssued).toString();
      return new Promise((resolve, reject) => {
        const cancel = () => {
          const active = networkRequests.get(id);
          if (!active) return;
          networkRequests.delete(id);
          active.dispose();
          reject(new AppNetworkError("uncertain"));
          try {
            port?.postMessage({
              type: "network.cancel",
              ...envelope,
              requestId: id,
            });
          } catch {
            fail("transport_error");
          }
        };
        const timeout = setTimeout(cancel, 35_000);
        const dispose = () => {
          clearTimeout(timeout);
          signal?.removeEventListener("abort", cancel);
        };
        networkRequests.set(id, { resolve, reject, dispose });
        signal?.addEventListener("abort", cancel, { once: true });
        try {
          port!.postMessage({
            type: "network.request",
            ...envelope,
            requestId: id,
            request,
          });
        } catch {
          fail("transport_error");
        }
      });
    },
    requestReports(
      value: BridgeReportRequest,
      signal?: AbortSignal,
    ): Promise<BridgeReportResponse> {
      if (terminal() || !applied || !port || !envelope || !context)
        return Promise.reject(new BridgeReportError("not_ready"));
      if (!context.capabilities.includes("reports.read"))
        return Promise.reject(new BridgeReportError("unsupported"));
      const request = parseBridgeReportRequest(value);
      if (!request)
        return Promise.reject(new BridgeReportError("invalid_request"));
      if (signal?.aborted)
        return Promise.reject(new BridgeReportError("not_ready"));
      if (reportRequests.size >= 2 || reportIssued === MAX_REVISION)
        return Promise.reject(new BridgeReportError("busy"));
      const id = (++reportIssued).toString();
      return new Promise((resolve, reject) => {
        const cancel = () => {
          const active = reportRequests.get(id);
          if (!active) return;
          reportRequests.delete(id);
          active.dispose();
          reject(new BridgeReportError("uncertain"));
          try {
            port?.postMessage({
              type: "reports.cancel",
              ...envelope,
              requestId: id,
            });
          } catch {
            fail("transport_error");
          }
        };
        const timeout = setTimeout(cancel, 20_000);
        const dispose = () => {
          clearTimeout(timeout);
          signal?.removeEventListener("abort", cancel);
        };
        reportRequests.set(id, { resolve, reject, dispose });
        signal?.addEventListener("abort", cancel, { once: true });
        try {
          port!.postMessage({
            type: "reports.request",
            ...envelope,
            requestId: id,
            request,
          });
        } catch {
          fail("transport_error");
        }
      });
    },
    requestObjects(
      value: BridgeObjectRequest,
      signal?: AbortSignal,
    ): Promise<BridgeObjectResponse> {
      if (terminal() || !applied || !port || !envelope || !context)
        return Promise.reject(new BridgeObjectError("not_ready"));
      if (!context.capabilities.includes("storage.files"))
        return Promise.reject(new BridgeObjectError("unsupported"));
      const request = parseBridgeObjectRequest(value);
      if (!request)
        return Promise.reject(new BridgeObjectError("invalid_request"));
      if (signal?.aborted)
        return Promise.reject(new BridgeObjectError("not_ready"));
      if (objectRequests.size >= 2 || objectIssued === MAX_REVISION)
        return Promise.reject(new BridgeObjectError("busy"));
      const id = (++objectIssued).toString();
      return new Promise((resolve, reject) => {
        const cancel = () => {
          const active = objectRequests.get(id);
          if (!active) return;
          objectRequests.delete(id);
          active.dispose();
          reject(new BridgeObjectError("uncertain"));
          try {
            port?.postMessage({
              type: "objects.cancel",
              ...envelope,
              requestId: id,
            });
          } catch {
            fail("transport_error");
          }
        };
        const timeout = setTimeout(cancel, 20_000);
        const dispose = () => {
          clearTimeout(timeout);
          signal?.removeEventListener("abort", cancel);
        };
        objectRequests.set(id, { resolve, reject, dispose });
        signal?.addEventListener("abort", cancel, { once: true });
        try {
          port!.postMessage({
            type: "objects.request",
            ...envelope,
            requestId: id,
            request,
          });
        } catch {
          fail("transport_error");
        }
      });
    },
    close() {
      if (state !== "closed") {
        state = "closed";
        dispose();
      }
    },
  };
  if (localWindow === parentWindow || !parentOrigin(expectedOrigin)) {
    fail("invalid_context");
    return session;
  }
  try {
    childNonce = randomNonce();
    localWindow.addEventListener("message", onWindow);
    deadline();
    retry = setInterval(hello, RETRY_MS);
    hello();
  } catch {
    fail("unsupported_environment");
  }
  return session;
}
