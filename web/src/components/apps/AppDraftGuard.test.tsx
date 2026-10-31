import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import {
  createMemoryRouter,
  RouterProvider,
  Link,
  Routes,
  Route,
} from "react-router";
import { useState } from "react";
import AppDraftGuard from "./AppDraftGuard";
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));
afterEach(() => vi.restoreAllMocks());
function fixture() {
  function Editor() {
    const [dirty, setDirty] = useState(false);
    return (
      <>
        <AppDraftGuard dirty={dirty} />
        <button onClick={() => setDirty((v) => !v)}>Toggle dirty</button>
        <Link to="/away">Leave</Link>
        <span>Editor</span>
      </>
    );
  }
  const router = createMemoryRouter([
    {
      path: "*",
      element: (
        <Routes>
          <Route path="/" element={<Editor />} />
          <Route path="/away" element={<span>Away</span>} />
        </Routes>
      ),
    },
  ]);
  const view = render(<RouterProvider router={router} />);
  return { ...view, router };
}
test("clean app leaves without a prompt", async () => {
  fixture();
  fireEvent.click(screen.getByRole("link", { name: "Leave" }));
  await screen.findByText("Away");
  expect(screen.queryByRole("dialog")).toBeNull();
});
test("dirty app supports cancel then user-confirmed departure without asking the app", async () => {
  const { router } = fixture();
  fireEvent.click(screen.getByRole("button", { name: "Toggle dirty" }));
  fireEvent.click(screen.getByRole("link", { name: "Leave" }));
  await screen.findByRole("dialog");
  expect(screen.getByText("Editor")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "apps.draftStay" }));
  await waitFor(() =>
    expect(
      [...router.state.blockers.values()].every((b) => b.state === "unblocked"),
    ).toBe(true),
  );
  fireEvent.click(screen.getByRole("link", { name: "Leave" }));
  await screen.findByRole("dialog");
  fireEvent.click(screen.getByRole("button", { name: "apps.draftLeave" }));
  await screen.findByText("Away");
});
test("browser history departure is guarded and cancellation keeps the current location", async () => {
  const { router } = fixture();
  await act(async () => router.navigate("/away"));
  await act(async () => router.navigate("/"));
  fireEvent.click(screen.getByRole("button", { name: "Toggle dirty" }));
  await act(async () => router.navigate(-1));
  await screen.findByRole("dialog");
  fireEvent.click(screen.getByRole("button", { name: "apps.draftStay" }));
  await waitFor(() =>
    expect(
      router.state.blockers.size === 0 ||
        [...router.state.blockers.values()].every(
          (b) => b.state === "unblocked",
        ),
    ).toBe(true),
  );
  expect(router.state.location.pathname).toBe("/");
});
test("refresh protection exists only while dirty and retires with the page", () => {
  const view = fixture();
  const clean = new Event("beforeunload", { cancelable: true });
  window.dispatchEvent(clean);
  expect(clean.defaultPrevented).toBe(false);
  fireEvent.click(screen.getByRole("button", { name: "Toggle dirty" }));
  const dirty = new Event("beforeunload", { cancelable: true });
  window.dispatchEvent(dirty);
  expect(dirty.defaultPrevented).toBe(true);
  view.unmount();
  const retired = new Event("beforeunload", { cancelable: true });
  window.dispatchEvent(retired);
  expect(retired.defaultPrevented).toBe(false);
});

test("saving while a departure is blocked cancels the pending departure", async () => {
  const { router } = fixture();
  fireEvent.click(screen.getByRole("button", { name: "Toggle dirty" }));
  fireEvent.click(screen.getByRole("link", { name: "Leave" }));
  await screen.findByRole("dialog");
  fireEvent.click(screen.getByRole("button", { name: "Toggle dirty" }));
  await waitFor(() =>
    expect(
      [...router.state.blockers.values()].every((b) => b.state === "unblocked"),
    ).toBe(true),
  );
  expect(router.state.location.pathname).toBe("/");
  fireEvent.click(screen.getByRole("link", { name: "Leave" }));
  await screen.findByText("Away");
});
