# Shared page metadata and actions

Status: development implementation, verification in progress. This is part of
P06, addressing UX-01/UX-06/UX-08/UX-09; it does not complete those acceptance
cases, D5, P06 or P00–P14. No production deployment, Status adoption, stable SDK
freeze, catalogue publication, database change or server beta change is included.

## Visible behavior

The application declares its page title, description and up to six actions using
`useAppPage` from `@portal-app/sdk/root-v2`. Portal renders one shared Ant Design
header above the sandbox. The signed application name and icon stay visible;
application-supplied text never replaces the global Portal navigation or identity.
An empty title falls back to the signed application name. Text wraps; action
buttons wrap on narrow layouts. Applications should not render a duplicate page
header inside their document when using this API.

The app retains its React tree across appearance and signed route/query changes.
Page ownership changes on route/query changes, document reload, failure or close.
A stored button callback from a retired page cannot invoke a successor page.
Ordinary label/loading/disabled changes on the same owner preserve an in-progress
operation. Removing its action or replacing its page cancels the operation.

Only one page operation may run at a time. Portal immediately disables page action
buttons and shows loading on the selected action. Back navigation remains subject
to the existing dirty-state guard. An exception, refused invocation or missing
acknowledgement produces a fixed localized warning to check the result inside the
application. No raw exception, backend diagnostic or automatic retry is exposed.
Callback completion only confirms that the callback resolved. It does not prove a
durable transaction; the app must await and check its own backend result.

## Developer API

```tsx
import { useAppBackend, useAppPage } from '@portal-app/sdk/root-v2';

function Records() {
  const backend = useAppBackend();
  useAppPage({
    title: 'Records', // Resolve text from the current Portal locale in a real app.
    description: 'Recent records',
    actions: [{
      id: 'save', label: 'Save', variant: 'primary',
      disabled: !backend.ready,
      onAction: async signal => {
        const result = await backend.request({
          method: 'POST', path: '/v1/records', body: JSON.stringify({ value: 1 }),
        }, signal);
        if (result.status !== 200) throw new Error('Save not confirmed');
      },
    }],
  });
  return <div>Record editor</div>;
}
```

Mount the hook inside AppRoot. Mount one page descriptor at a time. The fields
`title`, `description`, `actions`, and action flags are optional in the hook:
empty text/array, `default`, false and false are their defaults. An action must
provide an ID, nonblank label and `onAction(AbortSignal)` callback. Inline callback
functions may change without replacing page ownership. Use `useAppDraft` for
unsaved state; page actions do not implicitly mark forms dirty.

Use the abort signal for cooperative cancellation, including backend requests.
Cancellation cannot roll back work already committed by a service. A callback that
ignores its signal may continue app-local work; the platform ignores its late
completion and never retries it. Persist business operation identity where the
business requires deduplication. This UI primitive grants no new capability.

## Data bounds and wire contract

The shared `page-v2.ts` parser copies and freezes closed metadata objects. It
allows only `{title, description, actions}` and actions with exact
`{id, label, variant, disabled, loading}`. Limits are 80 Unicode scalar values for
titles, 240 for descriptions, 48 for nonblank labels and six unique action IDs.
IDs match `[a-z][a-z0-9_-]{0,31}` except reserved constructor/prototype names.
Variants are default/primary/danger, with at most one primary action. C0/C1/DEL,
directional formatting/isolate controls and lone surrogates are refused. Literal
markup is plain React text. There is no HTML, CSS, URL, icon or callback in wire
metadata. The signed package remains the source of identity/icon authority.

Experimental bridge `portal.app.bridge/2-dev.7` adds:

- `page.describe`: bound envelope, independent positive int64 revision,
  acknowledged presentation revision, 32-hex owner scope, page or null.
- `page.action`: bound envelope, positive int64 invocation ID, owner scope,
  described-page revision, presentation revision, action ID.
- `page.action.result`: bound envelope, invocation/scope/described-page revision,
  fixed code complete/uncertain/unavailable.
- `page.action.cancel`: bound envelope, invocation/scope/described-page revision.

All existing installation/generation/policy/digest/channel nonces are required.
Descriptions coalesce at ten per second; the host rejects above twenty per second.
Descriptions from uncommitted presentations and replayed revisions cannot replace
the visible header. The host distinguishes the latest seen description revision
from the visible description revision when correlating an action. The client
checks the latest published snapshot and current registered flags before invoking
a callback. It refuses a stale owner, description, presentation or duplicate ID.
Only one action may be pending. The client deadline is 25 seconds; the host is
30 seconds. Timers, listeners and pending results retire on their owning lifetime.
No automatic replay occurs after navigation, reload, rejection or timeout.

The React registry preserves the same owner through StrictMode effect replay.
Its deferred unmount clear is fenced by registry lifetime, so an old route cleanup
cannot erase a new route descriptor. Appearance commits resend metadata against
the new presentation revision. The host memoizes its presentation independently
of page action state to avoid a metadata/presentation rerender loop.

## Verification and remaining work

Meaningful failing tests first demonstrated missing parser/controller, missing
bridge method, missing React hook and missing header. The full SDK suite then
caught two StrictMode ownership/late-cleanup regressions; both received fixes.
Tests cover immutable text bounds, injection-as-text, duplicate/primary limits,
one-operation admission, immediate busy state, deadline/exception uncertainty,
cooperative abort, retired callbacks, late results, route/theme preservation,
exact envelopes and closed payloads, plus frame observer retirement.

The real SDK/Portal browser fixture adds shared Ant header buttons, app-local
count changes and an abortable action across signed navigation. Its required
`SDK_NATIVE_BROWSER_PAGE_ACTIONS` marker is checked by both native Go layers and
the evidence collector. A source marker is not acceptance evidence. Linux/browser
results and exact source pins must be recorded after those jobs complete.

Remaining work includes whole-app visual/keyboard/accessibility acceptance,
shared overlays, persistent drafts, the broader capability broker and remaining
P00–P14 lifecycle/market/release requirements. This step does not approve D5/D6.
