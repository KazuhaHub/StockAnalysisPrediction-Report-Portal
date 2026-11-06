import { afterEach, expect, test, vi } from "vitest";
import { parseAppPage, createPageActions } from "./page-v2";

const page = {
  title: "运行记录",
  description: "最近执行情况",
  actions: [
    {
      id: "refresh",
      label: "刷新",
      variant: "default",
      disabled: false,
      loading: false,
    },
    {
      id: "save",
      label: "保存",
      variant: "primary",
      disabled: false,
      loading: false,
    },
  ],
} as const;
const cleanup: Array<() => void> = [];
afterEach(() => {
  cleanup.splice(0).forEach((close) => close());
  vi.useRealTimers();
});

test("copies and freezes bounded plain-text metadata; no executable values", () => {
  const input = structuredClone(page);
  const parsed = parseAppPage(input)!;
  expect(parsed).toEqual(page);
  expect(Object.isFrozen(parsed)).toBe(true);
  expect(Object.isFrozen(parsed.actions)).toBe(true);
  expect(Object.isFrozen(parsed.actions[0])).toBe(true);
  expect(parsed.actions).not.toBe(input.actions);
  expect(parseAppPage({ ...page, title: "" })).not.toBeNull();
  expect(parseAppPage({ ...page, title: "😀".repeat(80) })).not.toBeNull();
  expect(parseAppPage({ ...page, title: "😀".repeat(81) })).toBeNull();
  // Literal markup is rendered as text by the host; it grants no HTML authority.
  expect(parseAppPage({ ...page, title: "<img src=x>" })?.title).toBe(
    "<img src=x>",
  );
});

test.each(["\u0000", "\n", "\u007f", "\u202e", "\u2066", "\ud800", "\udfff"])(
  "refuses control, directional or malformed Unicode %j",
  (value) => {
    expect(parseAppPage({ ...page, title: value })).toBeNull();
    expect(parseAppPage({ ...page, description: value })).toBeNull();
    expect(
      parseAppPage({
        ...page,
        actions: [{ ...page.actions[0], label: value }],
      }),
    ).toBeNull();
  },
);

test("closed schemas, limits, unique identifiers and one primary action", () => {
  for (const value of [
    null,
    [],
    { ...page, html: "x" },
    { title: "x", actions: [] },
    { ...page, title: "a".repeat(81) },
    { ...page, description: "a".repeat(241) },
    {
      ...page,
      actions: Array.from({ length: 7 }, (_, i) => ({
        ...page.actions[0],
        id: `a${i}`,
      })),
    },
    { ...page, actions: [page.actions[0], page.actions[0]] },
    {
      ...page,
      actions: [page.actions[1], { ...page.actions[1], id: "other" }],
    },
    ...[
      "",
      "A",
      "../x",
      "constructor",
      "prototype",
      "__proto__",
      "a".repeat(33),
    ].map((id) => ({ ...page, actions: [{ ...page.actions[0], id }] })),
    ...[
      { label: "" },
      { label: "x".repeat(49) },
      { variant: "link" },
      { disabled: 0 },
      { loading: null },
      { onClick: () => {} },
      { href: "/admin" },
    ].map((extra) => ({
      ...page,
      actions: [{ ...page.actions[0], ...extra }],
    })),
  ])
    expect(parseAppPage(value), JSON.stringify(value)).toBeNull();
});

function fixture() {
  const publish = vi.fn(),
    execute =
      vi.fn<(id: string, signal: AbortSignal) => void | Promise<void>>();
  const actions = createPageActions({ publish, execute, deadlineMs: 100 });
  cleanup.push(actions.close);
  actions.setPage(page);
  return { actions, publish, execute };
}

test("one operation at a time, synchronous busy notification, fixed completion", async () => {
  const { actions, publish, execute } = fixture();
  let resolve!: () => void;
  execute.mockImplementation(
    () =>
      new Promise<void>((done) => {
        resolve = done;
      }),
  );
  const click = actions.snapshot()!.invoke;
  expect(click("save")).toBe(true);
  expect(publish.mock.lastCall?.[0].busyAction).toBe("save");
  expect(click("refresh")).toBe(false);
  expect(execute).toHaveBeenCalledTimes(1);
  resolve();
  await Promise.resolve();
  expect(actions.snapshot()?.outcome).toBe("complete");
  expect(actions.snapshot()?.busyAction).toBeNull();
});

test("metadata/loading updates preserve an already running action; removed action aborts", async () => {
  const { actions, execute } = fixture();
  let signal!: AbortSignal, resolve!: () => void;
  execute.mockImplementation((_id, s) => {
    signal = s;
    return new Promise<void>((done) => {
      resolve = done;
    });
  });
  actions.snapshot()!.invoke("save");
  actions.setPage({
    ...page,
    title: "Saving",
    actions: page.actions.map((a) => ({ ...a, loading: true })),
  });
  expect(signal.aborted).toBe(false);
  expect(actions.snapshot()?.busyAction).toBe("save");
  actions.setPage({ ...page, actions: [page.actions[0]] });
  expect(signal.aborted).toBe(true);
  resolve();
  await Promise.resolve();
  expect(actions.snapshot()?.outcome).toBeNull();
});

test("retired page callbacks cannot invoke successor page actions or complete into it", async () => {
  const { actions, execute } = fixture();
  let signal!: AbortSignal, resolve!: () => void;
  execute.mockImplementation((_id, s) => {
    signal = s;
    return new Promise<void>((done) => {
      resolve = done;
    });
  });
  const old = actions.snapshot()!.invoke;
  old("save");
  actions.retire();
  actions.setPage(page);
  expect(signal.aborted).toBe(true);
  expect(old("save")).toBe(false);
  resolve();
  await Promise.resolve();
  expect(actions.snapshot()?.outcome).toBeNull();
  expect(execute).toHaveBeenCalledTimes(1);
  expect(actions.snapshot()!.invoke("refresh")).toBe(true);
});

test("exceptions and deadline have uncertain outcomes, never retries or raw diagnostics", async () => {
  vi.useFakeTimers();
  const { actions, publish, execute } = fixture();
  execute.mockImplementation(() => {
    throw Error("private service credential");
  });
  expect(actions.snapshot()!.invoke("save")).toBe(true);
  expect(actions.snapshot()?.outcome).toBe("uncertain");
  let signal!: AbortSignal;
  execute.mockImplementation((_id, s) => {
    signal = s;
    return new Promise<void>(() => {});
  });
  actions.snapshot()!.invoke("refresh");
  await vi.advanceTimersByTimeAsync(101);
  expect(signal.aborted).toBe(true);
  expect(actions.snapshot()?.outcome).toBe("uncertain");
  await vi.advanceTimersByTimeAsync(1000);
  expect(execute).toHaveBeenCalledTimes(2);
  expect(JSON.stringify(publish.mock.calls)).not.toContain(
    "private service credential",
  );
});

test("unknown, disabled and loading actions cannot execute; unavailable pages stay closed", () => {
  const { actions, execute } = fixture();
  actions.setPage({
    ...page,
    actions: page.actions.map((a) => ({ ...a, disabled: true })),
  });
  expect(actions.snapshot()!.invoke("save")).toBe(false);
  actions.setPage({
    ...page,
    actions: page.actions.map((a) => ({ ...a, loading: true })),
  });
  expect(actions.snapshot()!.invoke("save")).toBe(false);
  expect(actions.snapshot()!.invoke("missing")).toBe(false);
  const old = actions.snapshot()!.invoke;
  actions.close();
  actions.setPage(page);
  expect(actions.snapshot()).toBeNull();
  expect(old("save")).toBe(false);
  expect(execute).not.toHaveBeenCalled();
});

test("appearance acknowledgement gates new clicks without cancelling running work", () => {
  const { actions, execute } = fixture();
  execute.mockImplementation(() => new Promise<void>(() => {}));
  actions.setReady(false);
  expect(actions.snapshot()!.invoke("save")).toBe(false);
  actions.setReady(true);
  expect(actions.snapshot()!.invoke("save")).toBe(true);
  const signal = execute.mock.calls[0][1];
  actions.setReady(false);
  expect(signal.aborted).toBe(false);
  actions.retire();
  expect(signal.aborted).toBe(true);
});
