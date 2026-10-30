# Signed app routes and host-owned history

Status: development implementation with passing Linux browser/native acceptance
at functional source `df293dada59f1738137e95ebdb9d32a09e0d44ef`. Local
server/SDK/host/browser checks pass. This
extends P06 without completing the full shell/platform. No storage/descriptor
schema, dependency, publication, production, Status or server beta change.

## Result and boundaries

An app route is selected from its verified signed descriptor, exposed by current
launch admission and validated again in the host/bridge/SDK. Portal owns the URL
and browser history. A child requests a path; it does not write parent history or
optimistically change its committed route. Canceling the standard dirty prompt
leaves the current path and mounted document unchanged. Confirming changes the
path through the router, and the subsequent presentation commits it to the app.

Both `/apps/v2/<installation>` (home) and `/apps/v2/<installation>/editor`
(declared example path) use the same installation-keyed LaunchPage, iframe source,
bridge channel and React root. Host back/forward updates presentation without
another launch request, document reload or backend write replay. Components that
remain mounted retain their state. A route may change document/viewport layout;
the existing covered height/presentation contract still applies.

This step uses the descriptor's existing static paths. No query field is currently
declared, so query/fragment state is rejected. Dynamic parameter templates, typed
query declarations, per-page shell actions, persisted drafts and full D5 review
remain unfinished work. Do not introduce arbitrary JSON route state or tokens in
URLs to bypass those contracts.

## Implementation map

1. `internal/app/app_platform_http.go`: copy `m.Presentation.Routes` only inside
   existing current-account/OU/session/trust/native launch admission. Return the
   copied registry with the admitted document binding. Choose default layout by
   the `/` route, not the first descriptor entry. Keep final admission, no-store,
   identity and document inspection rules unchanged.
2. `web/src/lib/bridge-v2.ts`: parse and freeze the exact registry and navigation
   presentation, including active path/layout agreement. Child request messages
   carry the complete channel envelope, independent increasing int64 revision,
   current presentation revision, path and replace boolean. Refuse undeclared
   destinations/extra fields; ignore stale or unacknowledged presentation work.
3. `web/src/lib/appLaunchV2.ts`: require a bounded valid registry and matching
   root layout. Its hints never replace server authorization on subsequent reads.
4. `web/src/App.tsx`: register the intentional installation wildcard. The old
   legacy app path remains separate. `AppViewV2` derives a raw canonical path
   relative to its exact installation prefix and refuses search/hash/unknown
   paths before mounting executable content.
5. `AppViewV2`: keep metadata loading keyed to installation/retry, not app route.
   Choose the signed route layout; include committed path/registry in current
   presentation. Keep the same AppFrame key/source across route changes. A blocked
   confirmation cannot be replaced by another app-originated route request.
6. `AppDraftGuard`: expose the same blocker/prompt for the host page and retain
   its existing component adapter. There is only one blocker per mounted app,
   so app requests, host links and POP use the same confirmation state.
7. `AppFrameV2`: forward navigation to a current observer ref only from the live
   channel epoch. Reload/failure/replacement/unmount retire stale callbacks.
8. Apps SDK `root-v2`: expose `useAppNavigation()` with ready/path/routes and a
   document-bound `navigate` function. The path comes only from a committed host
   presentation. Ordinary locale/theme/navigation updates preserve the root;
   fresh connection generations do not inherit old requests or state.

## Contract details

The matched development protocol is `portal.app.bridge/2-dev.5`, not a stable
SDK release. A presentation optionally adds exact `navigation: {path, routes}`.
Registries contain 1..32 exact `{id,path,layout}` objects, unique IDs/paths, one
home path `/`, canonical descriptor paths and known layouts. The active path
must exist and its layout must match presentation layout. Frozen copies prevent
an input object from later mutating a parsed registry.

`navigation.request` is exactly type, complete existing envelope, canonical
positive int64 revision/presentationRevision, path and replace boolean. There are
no arbitrary URLs, query objects, HTML, callback or history state fields. The
host ignores old envelopes/revisions and requests while a presentation is queued
or unacknowledged. Unknown destinations or invalid shape fail the channel.
The host fails more than twenty increasing requests in a receive-second window.
The client permits at most one request per 100 ms, with no queue or retry. Close,
reset and terminal failure prevent new dispatches and fresh challenges reset the
local revision. No new backend/platform capability is granted.

`navigate` returns a boolean for local send/validation, not confirmation or commit.
A current-path request is a no-op. During an open prompt, a host can decline another
app request; after cancel the user may request again. Never update application
route state from the return value. Render from `useAppNavigation().path` instead.
This avoids canceled navigation unmounting an unsaved form before host consent.

## Tests and reproducible checks

Tests were written first: the host/client navigation case failed on the absent
navigation contract, the SDK hook case failed before `useAppNavigation` existed,
and the launch HTTP test failed because no signed registry was returned.

- Go HTTP test verifies the registry is copied from signed launch facts. A second
  actual frontend-install fixture lists viewport `/editor` before document `/`
  and proves default layout follows `/`, not array order. Existing current-user,
  OU/policy/session/document refusal checks remain. No fixture edits live data.
- Shared bridge tests refuse empty/duplicate/missing-home/unsafe/newline registries,
  wrong active layout, extra query state and injected destinations. They verify
  current-channel request dispatch and stale hint refusal.
- The real React/MessagePort SDK test requests a path, verifies the old path stays
  until host commit, then checks a stateful counter survives the committed update.
- Real data-router page tests open a deep editor link, navigate home, POP back,
  cancel a dirty route change, confirm it and verify the same mounted frame and
  exactly one metadata request. Unknown/encoded/query/fragment deep links never
  mount executable content. Frame tests reject retired navigation callbacks.
- Local checks passed: Portal 147 affected tests/typecheck/SPA build/lint; SDK 217
  tests/workspace typechecks; frontend standard, root/template/demo/Status tests
  and builds; generated Status public assets unchanged. Generator/collector/
  public fixture Python tests passed 10/14/2. Actual built browser ZIP passed the
  signed document handler test; current HTTP regressions and Linux compilation
  are recorded when complete. Existing unrelated hook lint warnings remain.

The background local browser used actual AppViewV2, SDK and disposable HTTP/session
SQLite state. It changed `/` to `/editor` while retaining counter 1, marked edits,
changed Portal to Chinese, canceled a return and stayed on `/editor`, then confirmed
and observed `/` with counter 1 and edits still present. This fixture uses a memory
router; it proves the real page/SDK commit flow but does not prove address-bar
history. Linux browser acceptance below exercises actual browser history.
Screenshot `app-signed-navigation.jpg` is a temporary local verification artifact.

The Linux native/browser fixture now signs both routes before background install,
loads actual SDK/host sources, uses createBrowserRouter, requests editor, asserts
the browser URL, goes back/forward, preserves the counter, returns home and repeats
existing layout/backend/no-retry/revocation/draft guard checks. Required marker
`SDK_NATIVE_BROWSER_NAVIGATION` must survive child, parent and collector, yielding
`sdk_native_browser_navigation`. Source presence is not passing runtime evidence.
Its reviewed Apps pin is `dab705dfa233a204e2e7ca60a8af015f3edd3a4b` (Apps #31,
stacked on #30). Linux evidence will be appended only after actual completion.

Full P00–P14 acceptance, typed query contracts, persistence, shared overlays/actions,
marketplace lifecycle, capability brokers, D5 and D6 are still incomplete.


## Functional acceptance evidence

Functional Portal source `df293dada59f1738137e95ebdb9d32a09e0d44ef` passed
quota/native workflow 40863529181/job 123412436309. The downloaded redacted JSON
has navigation, draft guard, current viewer, document layout, no-retry uncertainty
and revoked-access browser flags all true. The new navigation case used actual
browser address/history, not the local memory-router fixture. Owned cleanup and
neighbor preservation passed; public browser fixture and registry cleanup
completed. `execution_ready` remains false. This is isolated acceptance, not D6
production readiness or authorization.

All Portal web shards, lint/typecheck/build, workflow lint and Docker checks
passed on this functional source. Go job 123412435880 in run40863528901 passed
lifecycle/current-access/worker/queue/document suites on SQLite/PostgreSQL
(53.312s,27.565s,7.791s,8.511s,66.855s,0.346s), including the new out-of-order
signed registry case, plus build/vet/staticcheck/pre-gate checks. It failed
inherited x/net v0.60.0 findings GO-2026-6866/GO-2026-6860 and skipped ordinary
full Go tests. No dependency upgrade, suppression or all-CI-pass claim is made.

Exact Apps source `dab705dfa233a204e2e7ca60a8af015f3edd3a4b` passed every job
in run40863149243: protocol, frontend standard, catalogue and maintained Status
browser/container/artifact checks. No package/release/catalogue was published.
The final document-only evidence commit may trigger new checks; results at the
functional source above are not silently relabeled as a newer source result.
