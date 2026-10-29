# Signed typed route queries and recovery

Status: implemented for development with passing Linux browser/native evidence
at initial functional commit a328ee5cf44ac376a05531e588f9555373c14f06.
The query-identity follow-up below requires a separate native check. P06 and
full platform release acceptance remain in progress. This extends the signed
package descriptor; database tables, columns, migrations and dependencies are
unchanged. Production, Status adoption, catalogue publication, stable SDK freeze
and server beta configuration remain outside this development step.

## User-visible result

An app can keep filter/page state in its host URL and restore it through browser
back/forward, a deep link or refresh. The SDK exposes committed values with their
actual types: page 2 is a number, false is a boolean, and Chinese filter text is a
string. Query changes retain the same iframe/root and mounted state. Refresh
creates a fresh document and restores only URL state; unsaved component state
still requires separate persistence.

The host validates the destination's signed query rules before changing the URL.
The SDK never changes its committed query optimistically from a send boolean.
Dirty navigation uses the existing host confirmation; cancel keeps the old path
and query. Invalid deep-link parameters mount no executable content and show a
localized host error with an App home recovery button. Recovery clears the bad
query without fetching launch metadata again. Hidden or unauthorized apps still
follow the existing unavailable/session gates.

## Descriptor fields for developers

Add optional `query` to an existing route. Absent, null or an empty array means
there are no permitted query fields. Each field is optional in a URL and can use
one exact variant:

```json
{
  "id":"home", "path":"/", "layout":"document",
  "query":[
    {"name":"page", "type":"integer", "min":0, "max":100},
    {"name":"q", "type":"text", "max_bytes":64},
    {"name":"active", "type":"boolean"},
    {"name":"sort", "type":"enum", "values":["newest","oldest"]}
  ]
}
```

There are at most 16 fields per route. Names are unique lowercase ASCII identifiers
up to 32 characters; constructor/prototype are reserved. Integer min/max are
required, ordered and lie within +/-1000000000. Text max_bytes is 1..256 UTF-8
bytes. Enums have 1..16 unique ASCII codes of at most 64 characters. Boolean rules
have no numeric/text/enum constraints. Extra or irrelevant fields and explicit
null rule members are refused. Every query field can be absent, so normal app
defaults can apply. Explicit zero, false and empty text remain meaningful values.

The schema's conditional variants help editors and AJV tooling; the strict Go/SDK
parsers also enforce unique names, reserved names, range ordering, exact numeric
syntax and signature byte identity. Signature/trust/compatibility and existing
install review still precede any executable admission.

## SDK and URL behavior

```tsx
const nav = useAppNavigation();
const page = typeof nav.query.page === 'number' ? nav.query.page : 0;
nav.navigate(nav.path ?? '/', {
  query: {...nav.query, page: page + 1},
  replace: true,
});
```

Use `nav.query` as the readonly typed committed view and `nav.search` for the
canonical encoded string. Sending replaces the complete query set. Spread the
current query to retain other filters; omit query or send {} to clear it. A send
boolean reports local validation/send only. The later host presentation commits
query state. The experimental API uses an options object instead of dev.5's
positional replace flag and requires a matched dev.6 host/SDK.

Canonical encoding uses sorted field names and URLSearchParams, decimal integer
strings and literal true/false booleans. The example produces:

```text
?active=false&page=2&q=%E4%B8%AD%E6%96%87+%26+test&sort=newest
```

Unknown names, duplicate keys, wrong types, noncanonical order/encoding, leading
zeroes, malformed Unicode, controls and exceeded limits are refused. Encoded
search is capped at 2048 ASCII bytes independently of each field's text bound.
Fragments and arbitrary external route destinations remain refused. Query state
is visible address/history data; use the approved backend/secret/draft mechanisms
for their corresponding data. Query hints grant no backend authority.

## Implementation map

1. `internal/appplatform/contract/descriptor-v2.schema.json` adds optional
   Route.query and the closed RouteQuery variants. The generator emits Go/TS
   types, domains, constraints and pattern constants. Optional numeric fields use
   pointers in Go so a signed min 0 survives omitempty serialization. A dedicated
   UnmarshalJSON rejects explicit null aliases before semantic validation.
2. `internal/appplatform/route_query.go` validates rule presence, variant-specific
   constraints, unique names/enum values and ordered bounds. Current signed launch
   admission returns those declared fields through existing routes metadata.
3. `web/src/lib/route-query-v2.ts` is pure shared query validation/serialization.
   It freezes rules/enum arrays and returns a frozen typed query with no inherited
   object prototype. Descriptor tooling, host, bridge and SDK use the same source.
4. `scripts/generate_app_contract.py` copies that source/tests into the Apps SDK
   and writes a separate SHA256 pin. Apps check mode refuses changed or deleted
   validators. Existing bridge source/test pinning remains mandatory.
5. `bridge-v2` advances to dev.6 and validates mandatory canonical search in
   presentation/navigation requests against the signed destination route. Existing
   envelope, revision, presentation-ack, rate, reset and retirement guards remain.
6. `AppViewV2` validates the raw URL search before mounting a document, forwards
   only canonical committed search and validates requests again before router
   navigation. Its localized invalid-link error/recovery remains host owned.
7. `AppFrameV2` forwards search only through the current epoch-fenced observer.
   `AppRoot` derives readonly typed query from validated committed presentation
   and keeps its document-bound navigate function stable across ordinary updates.
8. The local actual-page fixture now uses createBrowserRouter, with loopback-only
   shell delivery for deep links. Synthetic cookies/stores stay disposable. This
   replaces its older memory-router limitation for address/history/reload checks.

## Checks and evidence

Meaningful tests failed before implementation: Go rejected the new signed query
rules and the pure query test import was absent. A new generator regression also
checks optional numeric pointer presence. The repaired implementation passes:

- Shared descriptor corpus expanded 44 to 56 cases, pinned identically in Go/SDK.
  Cases include zero-bound rules, null/empty no-query declarations, missing/null/
  reversed bounds, duplicate names/enums, irrelevant constraints and excess fields.
- Pure query tests verify Unicode/boolean/integer roundtrip, canonical search,
  duplicate/unknown/refused encodings, strict SDK value types, malformed text,
  total encoded budget, maximum field count and prototype-key refusal.
- Real bridge tests prove destination rules are used rather than active-route
  rules, and current-source query dispatch remains bound. Real React/MessagePort
  tests retain old query until host commit and preserve a stateful component.
- Host data-router tests verify deep query links, back/forward, one metadata
  request/same frame, invalid-link refusal and home recovery. Existing dirty,
  layout, backend transport and retired-channel tests remain.
- Local Portal 165 affected tests, SDK 249 tests, typechecks/SPA/lint and all
  maintained SDK/template/demo/Status frontend checks passed. Generated Status
  public assets have no diff. Generator/collector/public-fixture Python tests
  passed 12/14/2. Go descriptor/current HTTP regressions, actual signed browser ZIP
  handler and Linux amd64 compilation passed. Existing unrelated hook lint
  warnings remain; the inherited x/net vulnerability gate is separate.

A background browser plugin exercised actual AppViewV2, SDK and disposable
HTTP/session/SQLite facts. It set false/page 2/Chinese/enum filters in the real
host URL, went back/forward retaining counter 1, reloaded that URL and restored all
typed query values with a fresh counter 0. It switched host language to Chinese,
opened an unknown query, observed no iframe plus the invalid-link error, and
recovered to App home with an empty query. Screenshot `app-typed-query.jpg`
records this local check. Its temporary page/server were closed after verification.
This is isolated development evidence; no production credentials or version
configuration were used.

The Linux native/browser fixture now signs typed query rules before background
install and requires address/history/typed-value/reload assertions alongside
existing route, layout, current-viewer, no-retry, revoke and dirty-guard cases.
New marker SDK_NATIVE_BROWSER_TYPED_QUERY must survive child/parent/collector,
producing sdk_native_browser_typed_query in redacted evidence. Its reviewed Apps
current pin is fa34a900f3c29ee5218d51dd5a1b6ea7bf0c326c. Runtime evidence is pending actual CI
and will be recorded with its exact functional commit; source marker presence
alone proves nothing.

Full P00–P14, persistent drafts, shared page actions/overlays, capability brokers,
marketplace lifecycle, D5 visual/accessibility review and D6 rollout remain open.


CI now runs the pure Go descriptor/query package before the existing vulnerability
gate, so malformed shared declarations are tested even when that separate gate
fails. Native workflow path filters also cover the shared schema/query/bridge,
frame/page and generator sources. The vulnerability gate and workflow permissions
are unchanged.


## Initial functional evidence and identity follow-up

Initial Portal a328ee5cf44ac376a05531e588f9555373c14f06 with Apps e3770921
passed Linux quota/native run40868846704/job123430411682. Redacted evidence has
sdk_native_browser_typed_query, navigation, dirty guard, current viewer, document
layout, uncertainty without retry and revoked-access flags true. Typed query
checks included actual URL/back/forward/reload with numbers, false, Chinese text
and enums. Owned cleanup/neighbor preservation passed; execution_ready is false.
Registry/public fixture cleanup completed.

General run40868846720 passed web shards/build/lint, all app race shards/aggregate,
Docker, workflow lint, the new pure descriptor/query suite0.070s, and affected
SQLite/PostgreSQL suites55.895s/29.242s/8.402s/9.835s/72.040s/0.137s. Go job
123430412200 failed only inherited x/net GO-2026-6866/6860 findings; its ordinary
full suite was skipped. Dependencies/gate remain unchanged. Apps e3770921 passed
all jobs in run40868483698.

Final SDK review found that locale updates recreated the query object despite
unchanged contents, which could re-run an app's query-dependent data effect.
A real React/MessagePort identity assertion failed first. The root now memoizes
query values by signed definition/search/document signal, preserving identity
across unrelated appearance updates. All249 SDK tests/typechecks/root build pass
locally with that regression. Apps follow-up fa34a900 adds this fix. The native
fixture now observes a query-dependent layout effect around a locale update and
requires no extra effect execution. This follow-up needs its own CI; earlier
runtime evidence is not relabeled as the later source.
