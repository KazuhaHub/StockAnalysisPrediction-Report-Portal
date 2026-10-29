// Shared experimental page metadata and action lifetime. Transport adapters must
// authenticate the bridge envelope before passing a page to this module.
export interface AppPageAction {
  readonly id: string;
  readonly label: string;
  readonly variant: "default" | "primary" | "danger";
  readonly disabled: boolean;
  readonly loading: boolean;
}
export interface AppPage {
  readonly title: string;
  readonly description: string;
  readonly actions: readonly AppPageAction[];
}
export type PageActionOutcome = "complete" | "uncertain";
export interface PageActionSnapshot {
  readonly page: AppPage;
  readonly ready: boolean;
  readonly busyAction: string | null;
  readonly outcome: PageActionOutcome | null;
  // Bound to a page lifetime, never reusable after retire/close.
  invoke(id: string): boolean;
}
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
function text(value: unknown, max: number, required = false): value is string {
  if (
    typeof value !== "string" ||
    value.length > max * 2 ||
    /[\x00-\x1f\x7f-\x9f\u202a-\u202e\u2066-\u2069]/u.test(value)
  )
    return false;
  let count = 0;
  for (const char of value) {
    const point = char.codePointAt(0)!;
    if ((point >= 0xd800 && point <= 0xdfff) || ++count > max) return false;
  }
  return !required || value.trim().length > 0;
}
export function parseAppPage(value: unknown): AppPage | null {
  if (
    !exact(value, ["title", "description", "actions"]) ||
    !text(value.title, 80) ||
    !text(value.description, 240) ||
    !Array.isArray(value.actions) ||
    value.actions.length > 6
  )
    return null;
  const ids = new Set<string>(),
    actions: AppPageAction[] = [];
  let primary = 0;
  for (const action of value.actions) {
    if (
      !exact(action, ["id", "label", "variant", "disabled", "loading"]) ||
      typeof action.id !== "string" ||
      !/^[a-z][a-z0-9_-]{0,31}$/.test(action.id) ||
      ["constructor", "prototype"].includes(action.id) ||
      ids.has(action.id) ||
      !text(action.label, 48, true) ||
      !["default", "primary", "danger"].includes(action.variant as string) ||
      typeof action.disabled !== "boolean" ||
      typeof action.loading !== "boolean"
    )
      return null;
    if (action.variant === "primary" && ++primary > 1) return null;
    ids.add(action.id);
    actions.push(
      Object.freeze({
        id: action.id,
        label: action.label,
        variant: action.variant as AppPageAction["variant"],
        disabled: action.disabled,
        loading: action.loading,
      }),
    );
  }
  return Object.freeze({
    title: value.title,
    description: value.description,
    actions: Object.freeze(actions),
  });
}
export interface PageActions {
  setPage(value: unknown): boolean;
  setReady(ready: boolean): void;
  retire(): void;
  close(): void;
  snapshot(): PageActionSnapshot | null;
}
// execute is supplied by the authenticated transport adapter, not by untrusted
// metadata. A resolved callback confirms only callback completion, not a durable
// business transaction. Aborting cannot undo an already committed operation.
export function createPageActions(options: {
  execute(id: string, signal: AbortSignal): void | Promise<void>;
  publish(snapshot: PageActionSnapshot | null): void;
  deadlineMs?: number;
}): PageActions {
  const deadline = options.deadlineMs ?? 30_000;
  if (!Number.isInteger(deadline) || deadline < 1 || deadline > 30_000)
    throw new Error("Invalid page action deadline");
  let page: AppPage | null = null,
    closed = false,
    ready = true,
    lifetime = 0;
  let outcome: PageActionOutcome | null = null;
  let pending: {
    id: string;
    abort: AbortController;
    timer: ReturnType<typeof setTimeout>;
  } | null = null;
  let current: PageActionSnapshot | null = null;
  function publish() {
    const own = lifetime;
    current =
      page && !closed
        ? Object.freeze({
            page,
            ready,
            busyAction: pending?.id ?? null,
            outcome,
            invoke: (id: string) => invoke(id, own),
          })
        : null;
    options.publish(current);
  }
  function cancel() {
    if (!pending) return;
    const previous = pending;
    pending = null;
    clearTimeout(previous.timer);
    previous.abort.abort();
  }
  function invoke(id: string, own: number): boolean {
    if (closed || own !== lifetime || !ready || !page || pending) return false;
    const action = page.actions.find((action) => action.id === id);
    if (!action || action.disabled || action.loading) return false;
    const abort = new AbortController();
    const running = {
      id,
      abort,
      timer: setTimeout(() => finish("uncertain", true), deadline),
    };
    pending = running;
    outcome = null;
    function finish(result: PageActionOutcome, expired = false) {
      if (pending !== running || own !== lifetime || closed) return;
      pending = null;
      clearTimeout(running.timer);
      outcome = result;
      if (expired) abort.abort();
      publish();
    }
    publish();
    // Publishing may synchronously unmount or replace this page.
    if (pending !== running || own !== lifetime || closed) return false;
    try {
      Promise.resolve(options.execute(id, abort.signal)).then(
        () => finish("complete"),
        () => finish("uncertain"),
      );
    } catch {
      finish("uncertain");
    }
    return true;
  }
  function retire() {
    lifetime++;
    page = null;
    outcome = null;
    cancel();
    publish();
  }
  return {
    setPage(value) {
      if (closed) return false;
      const next = parseAppPage(value);
      if (!next) return false;
      page = next;
      if (
        pending &&
        !page.actions.some((action) => action.id === pending!.id)
      ) {
        cancel();
        outcome = null;
      }
      publish();
      return true;
    },
    setReady(value) {
      if (closed) return;
      ready = value;
      publish();
    },
    retire,
    close() {
      if (closed) return;
      closed = true;
      retire();
    },
    snapshot: () => current,
  };
}
