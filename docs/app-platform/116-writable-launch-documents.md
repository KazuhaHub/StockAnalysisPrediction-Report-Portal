# Completed writable applications: directory, launch and frontend documents

This follows [115](115-writable-background-completion.md). Background installation
already completes against the actual runner. This continuation connects completed
writable installations to the existing session-only launch/document routes and
V2 app directory. It is frontend admission, not a new iframe backend RPC protocol,
live health observation or complete interactive reference-app delivery.

## Concrete problem and resulting behavior

Previously `withPlatformLaunchAccountReadContext` always rejected a non-NULL
runtime and a descriptor with a backend. A fully completed writable installation
therefore could make private authenticated backend requests but could not use the
standard app document URL. The directory also withheld its open action.

The entry point now reads the runtime column only as a family-routing hint. It
then chooses exactly one independently checked transaction:

- NULL runtime: the original reader, renamed `withPlatformStaticLaunchReadContext`.
  Its current account/OU/publisher/installation/namespace checks, NULL-runtime and
  no-backend restrictions remain in place.
- Runtime present: `withPlatformWritableLaunchReadContext`, using the full
  completed writable history and shared current backend-access transaction.

A source change between hint and transaction causes refusal. There is no fallback
after either family's failure. Unsupported readonly-managed history, incomplete
work, prepared/unknown effects, retained data, bad keys/runtime, wrong epoch or
revoked access cannot become static frontend authority. Context cancellation is
preserved when the preliminary query is canceled.

## One current transaction for writable frontend reads

`withPlatformBackendRead` contains the existing shared backend read transaction.
The original `withPlatformBackendAccess` wrapper adapts its old callback signature,
so readonly/writable request callers retain their existing interface and behavior.
The new private transaction callback also receives `migExec` for bounded local
archive/metadata reads; it must perform no native/network or unbounded work.

The writable frontend path uses `loadPlatformWritableBackendHistory`, which
already checks the consumed canonical review, exact runtime/profile, original
namespace/writer/start/registration receipts, one completed initial operation and
four applied effects. The shared transaction still checks current session/account
lifecycle, OU/audience, publisher/signature, restore epoch, runtime and activation/
policy/fence consistency before and after the callback.

Inside that boundary:

1. Parse the already verified exact managed descriptor.
2. Build the public binding from installation ID, generation 1, current audience
   policy, signed release/frontend hashes and declared entry path. Audience-only
   policy edits retain original bootstrap/native identity.
3. Invoke the existing bounded local read callback. Launch metadata compiles the
   actual frontend profile before advertising its URL; document reads require
   the requested entire binding to equal the current one and compile the actual
   archive. Viewer facts use the same current account and identity derivation.
4. Keep all results buffered until final current account, key-time, epoch and
   signature checks commit. On callback/final failure return zero public output.
5. Close the private registration handle on success; failure cleanup remains in
   the shared transaction. No private document, seed or signing key is returned.

Frontend serving is a local authenticated read, not a current backend-health
assertion. It does not contact the runtime socket, start/restart a backend, renew
a worker lease, deliver a new registration or authorize a future business request.
A later backend call still needs its separate current authorization and native
before/after-IO checks. Reader key access cannot initialize or repair a keyring.

## Existing HTTP and presentation boundaries remain

No new public route, cookie or token type is added. The same routes now admit the
completed writable family through its own history:

```text
GET /api/app-platform/apps
GET /api/app-platform/installations/{installation}/launch
GET|HEAD /app-documents/{installation}/{generation}/{policy}/{release}/{frontend}/{entry}
```

The existing current-cookie/enrollment wall, two-document budget, five-second
request context, exact URL/counter/hash checks, complete-buffer response, no-store,
nosniff/referrer policy and opaque `sandbox allow-scripts` document CSP remain.
No allow-same-origin, direct network fetch, arbitrary backend URL or app-supplied
viewer identity is introduced. Anonymous, encoded, queried, stale or revoked
source URLs cannot release document bytes. Launch JSON contains the same public
binding/display/layout/timezone and an empty platform capability list, not native
connection details or registration material.

The existing AppsHub/AppViewV2/AppFrameV2 can consume that unchanged metadata.
They retain React/Ant Design and Portal preferences/presentation. No independent
language control or new application-specific shell is added. A real SDK app and
browser-rendered backend interaction remain to be verified separately; the native
HTTP fixture archive contains a minimal frontend, not a React/SDK demo.

## Directory status is a projection

Authorized rows are still filtered before pagination by current user/OU lifecycle
identity. `platformWritableLaunchEligible` adds a bounded local metadata check for
runtime-bound ready rows: one completed initial operation, matching current fence/
epoch, consumed canonical writable projection/digest/request, exact runtime,
writable profile and supported development API. Current binding metadata must
still be enabled and match the reviewed runtime. Readonly or unsupported managed
profiles cannot acquire an open action through this path.

This check does not decrypt keys, inspect executable ZIPs, attest health or grant
launch permission. The directory describes eligibility for a launch attempt. A
corrupt archive/history or a change after listing must still be refused by the
full launch/document transaction. Existing signed display and final account/key-
time checks remain. Hidden audience rows still cannot consume a page or cursor.

## Verification

SQL history fixtures establish frontend read/refusal behavior, not native success.
SQLite/PostgreSQL cases cover ordinary-user metadata/GET/HEAD, same CSP/no-store,
viewer identity, directory eligibility, stale policy/path/encoding/query refusal,
anonymous access and cached URL/directory revocation. Refusals cover grant,
runtime/key, publisher/account, restore pause, history, namespace, unsupported
family, corrupt same-length archive and mismatched policy.

The complete dump comparison in HTTP refusal tests first makes normal user/
session activity stamps current; ordinary authentication activity is separate
from a launch mutation. The protected complete store must then remain unchanged.
A canceled or failed direct bounded callback returns no binding and changes no
store state. Ancestor OU grants work at the current policy; reparenting removes
both document and directory access, and restoring the actual current relation
restores access without treating an old URL as a bearer grant.

The dedicated pre-vulnerability CI step also runs original static launch,
directory, viewer and HTTP cases, old readonly/writable access cases, and
PostgreSQL release/asset/document/directory/key-time/blocked-query cases:

```sh
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 go test -p 2 -count=1 ./internal/app \
  -run '^TestPlatform(WritableLaunch|WritableAccess|ManagedAccess|Launch|Directory|Viewer|HTTP)|^TestPostgresPlatform(AssetRead|Launch|DocumentRead|HTTPDocument|Directory|Display)'
```

The real Linux background-install fixture now fetches launch JSON and the exact
compiled document using the ordinary user's actual cookie, checks directory
eligibility, then deletes that app's audience grant and requires both old URLs
to return 404 and its directory item to disappear. Existing first-app policy-2
and writable-source tests require the precise successful launch binding instead
of their former blanket-denial assertion. Readonly managed sources still refuse.
Parent and Python reports require three new flags: current_document_http,
current_directory_http and document_http_revoked. Prior native/witness/quota/
cleanup requirements remain mandatory.

Local focused SQLite/legacy race checks passed (60.177s), expanded local frontend/
access tests passed (5.363s; PostgreSQL cases skip without its CI service), Linux
amd64/arm64 compilation and Linux vet/staticcheck passed, and Python14 passed.
Functional source `9ab08744748d1a3dd052466dbbb39fbfa538e893` passed
[quota workflow 40837291395](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40837291395),
job 123324962078. Checkout verified its merge into parent `144d02f3`. All three
new current-document/directory/revocation flags, previous native checks, full
quota witness and owned cleanup are true. Disposable registry cleanup succeeded;
`execution_ready=false` still prevents interpreting acceptance as production
runtime enablement.

General workflow 40837291397/job 123324962656 passed lifecycle SQL 76.436s,
current/legacy access SQL 39.245s, worker/static SQL 14.821s, queue/scheduling SQL
15.627s and the expanded frontend/static/access/PostgreSQL wait suite 106.754s.
The explicit PostgreSQL asset/document/launch reader and signing-key expiry,
directory expiry/cancellation, display-key expiry and blocked-query cases passed.
Build/vet/full Linux staticcheck, published market, arm64 numeric, all app race
shards/aggregate, web and Docker font/smoke checks passed. Shard 1/job
123324962575 ran full journal race 238.387s and its Portal subset 70.707s;
aggregate job 123327050103 passed. The Go job failed the existing x/net v0.60.0
GO-2026-6866/6860 vulnerability gate and skipped ordinary full tests. No dependency
change or suppression is included. No standalone daemon workflow was triggered
by this Portal/fixture change. Evidence applies to the named functional source;
later documentation-only heads may trigger CI again.

## Remaining integration

This is P05/P06/P12 and scoped AUTH-02/04/05 progress, not the full release.
Add a closed, versioned, nonce/binding-bound iframe backend request contract and
host-only HTTP gateway to the actual existing writable request path. Keep routes,
body/status sizes, timeout, current user/OU and post-IO revocation checks; do not
accept arbitrary destinations, credentials or viewer assertions from an iframe.
Update SDK/generator/reference app and common vectors together before any public
freeze. Then verify actual interactive frontend/backend use through the shared
shell, automatic document height, navigation and failure presentation. Production
startup/rollout, Status adoption, D5 acceptance, SDK freeze and remaining physical
lifecycle/recovery/broker/tasks/publication work retain their separate gates.
