// Experimental presentation and own-backend transport. No token or legacy fallback.
import {
  DESCRIPTOR_CAPABILITIES,
  DESCRIPTOR_LAYOUTS,
  DESCRIPTOR_LOCALES,
} from "./descriptor-contract.generated";

export const BRIDGE_PROTOCOL = "portal.app.bridge/2-dev.2";
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
export interface BridgePresentation {
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
export class BridgeBackendError extends Error {
  readonly retryable = false;
  constructor(readonly code: BridgeBackendFailure) {
    super(`Application request: ${code}`);
    this.name = "BridgeBackendError";
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
export function parseBridgePresentation(
  value: unknown,
): BridgePresentation | null {
  if (
    !exact(value, ["locale", "timeZone", "layout", "theme"]) ||
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
  const requests = new Map<
    string,
    { abort: AbortController; timer: ReturnType<typeof setTimeout> }
  >();
  const terminal = () => state === "closed" || state === "failed";
  function dispose() {
    for (const request of requests.values()) {
      clearTimeout(request.timer);
      request.abort.abort();
    }
    requests.clear();
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
  let issued = 0n;
  const requests = new Map<
    string,
    {
      resolve: (value: BridgeBackendResponse) => void;
      reject: (error: BridgeBackendError) => void;
      dispose: () => void;
    }
  >();
  function retireRequests() {
    for (const request of requests.values()) {
      request.dispose();
      request.reject(new BridgeBackendError("uncertain"));
    }
    requests.clear();
  }
  const terminal = () => state === "closed" || state === "failed";
  function dispose() {
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
        retireRequests();
        issued = 0n;
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
