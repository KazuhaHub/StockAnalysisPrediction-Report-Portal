# Current-viewer reports capability

Status: H04 development candidate, 2026-10-30. This adds a read-only resource
capability for the current Portal viewer. It grants no background/service
identity, report write, export, file access or credential access.

## Resource boundary

The selected first resource is the existing report list and detail view:

- opening an application;
- reading a report list and total count; and
- reading one report summary and Markdown body.

The capability is `reports.read`. A required static declaration is selected by
the install review. An optional declaration remains denied unless a later review
explicitly grants it. Launch advertises the capability only while the coherent
installation snapshot contains the grant at the current policy revision.

No database table or column was added. The implementation reuses the existing
reports, report versions, viewer ownership, installation, capability grant,
session and owner-scope records. It does not expose a table name, SQL fragment,
filesystem path or caller-supplied identity.

Credential balance and file/resource storage are separate later tasks. They are
not implied by `reports.read`.

## Request contract

The parent-only endpoint is:

```text
POST /api/app-platform/installations/{installation}/reports
```

It accepts either a bounded query:

```json
{
  "binding": {
    "generation": "1",
    "policyRevision": "1",
    "releaseSha256": "<64 lowercase hex>"
  },
  "query": {
    "q": "current",
    "date": "today",
    "limit": 20,
    "offset": 0
  }
}
```

or a detail read:

```json
{
  "binding": {
    "generation": "1",
    "policyRevision": "1",
    "releaseSha256": "<64 lowercase hex>"
  },
  "id": 42
}
```

Queries accept `symbol`, `q`, `kind`, `subtype`, `date`, `since`, `until`,
`limit` and `offset`. Text is bounded, `limit` is 1..50, `offset` is nonnegative,
and dates are exact `YYYY-MM-DD` or `today`. List results include count, total,
offset, limit and bounded summaries. Detail adds `body_md`. The app-facing bridge
uses camel-case summary fields and `bodyMarkdown`.

Unknown fields, a mixed query/detail body, an invalid counter, an oversized body
or an invalid date fails closed as unavailable. The endpoint is not a generic
report API.

## Authorization sequence

For every direct HTTP request, Portal:

1. admits the current Portal session and installation route;
2. verifies the launch binding, active release, generation and policy revision;
3. verifies the current `reports.read` grant at that policy revision;
4. resolves the current account and a repeatable-read owner-scope snapshot;
5. performs the bounded report query/detail read;
6. repeats the complete launch/account/capability check; and
7. returns data only if the launch binding and owner scope are unchanged.

The detail lookup uses the current viewer's owner scope. A report outside that
scope is the same 404 as a missing report, so direct IDs do not provide an
enumeration oracle. A report query returns zero rows and no cross-Owner-Unit
count when the current scope grants nothing.

No capability means no business read. Revocation during a read discards the
already-read result. An old generation, wrong release, stale policy, moved OU,
changed session/account generation or demoted administrator cannot reuse the
earlier authorization.

## Bridge and SDK

Development bridge protocol `2-dev.8` adds `reports.request`,
`reports.cancel` and `reports.result`. Reports have their own monotonic request
sequence and pending map, independent of own-backend and reviewed-network calls.
A connection allows two pending report reads. Cancellation aborts the Portal
fetch; timeout, disconnect and channel replacement reject the old promise and
never retry automatically.

`AppFrameV2` installs the reports transport only when launch advertises
`reports.read`. Capability or binding changes replace the document and close the
old channel. The parent transport fixes the installation route, sends only
same-origin cookies, disables redirects/cache, bounds the UTF-8 JSON stream,
maps failures to stable codes and strictly parses the final bridge shape.

Apps SDK `useAppReports()` returns stable `available`, `ready`, `query` and `get`
values. `ready` requires the initial React/Ant commit and bridge acknowledgement.
Caller abort signals are combined with the document lifetime. A retired closure
returns `not_ready`; unsupported, invalid, busy, unavailable and uncertain are
fixed non-retryable codes. The SDK documentation requires safe Markdown
rendering without raw HTML injection.

Portal owns `bridge-v2.ts`, its tests and the resource adapter. The contract
generator copies and pins the bridge source/test into Apps. The paired Apps SDK
commit is `576c6445bba85b0e348cbb9b2435c43c2b897647`.

## Verification

Host tests cover current scope and capability, revocation, two disjoint
organizational units (OUs), cross-OU detail denial, old generation, no app access,
administrator demotion and current account/session checks.

Portal web tests cover query/detail parsing, exact destination, fixed errors,
session-expiry handling, abort/cancellation, timeouts, replay refusal, pending
limits and frame wiring. Apps tests cover the real root/bridge query and detail
path, unavailable grants, validation, stable errors, appearance updates and old
closures.

The real Linux browser fixture requires marker `SDK_NATIVE_BROWSER_REPORTS`.
It installs a managed application requiring both `network.http` and
`reports.read`, seeds one report in the current viewer's scope, and exercises:

```text
real launch -> AppRoot -> useAppReports -> MessagePort bridge
-> Portal session fetch -> current-viewer reports route -> query/get
```

After removing current app access, the same old frame must receive
`error:unavailable` and must not retain report data. The paired fixture is
checked out by exact Apps SHA, and shared-source drift is verified before the
browser build.

The paired candidates passed pushed CI:

- Portal `374993b87a68efbb7bb07c5940de7056240f7637`: test workflow
  `41062843928`, quota/browser workflow `41062843843` and app-runner daemon
  workflow `41062843932`;
- Apps `576c6445bba85b0e348cbb9b2435c43c2b897647`: check workflows
  `41059898215` and `41059889070`; and
- the quota/browser workflow checked out the exact Apps pin before generating
  shared source, building the real SDK fixture and running the native Portal
  lifecycle.

The report resource remains read-only. File storage, credential binding/use,
background service identity and production activation remain separate gates.
