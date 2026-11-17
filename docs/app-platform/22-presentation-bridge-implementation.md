# Experimental presentation bridge

Status: P06 in progress. Candidate development protocol `portal.app.bridge/2-dev.1`;
not a stable SDK, registered application host or production bridge upgrade.
[Candidate A development approval](18-runtime-and-trust-proposal.md#9-approval-record)
does not waive visual, public-freeze or rollout acceptance.

## Source ownership and public surface

Portal owns `web/src/lib/bridge-v2.ts` and its adjacent tests. The contract
generator distributes exact copies to Apps, with only the test import path
adapted. A source/test SHA-256 pin makes Apps `--check` refuse independent changes.
Update through Portal and regenerate both checkouts using the
[shared contract workflow](21-descriptor-schema-implementation.md).

The descriptor generator also emits the same domain/type product into Portal's
`web/src/lib/descriptor-contract.generated.ts`. Both bridge roles consume those
capability/locale/layout domains. No second manually maintained permission list
is introduced. The shared module contains host and client roles; application
authors use `createBridgeClient` from `@portal-app/sdk/bridge-v2`.

This subpath does not import the legacy React provider or listen for `rp:*`.
It exposes no API requests, bearer token, fetch dispatch, arbitrary host HTML/CSS,
navigation handler, credential binding or runtime command. Capability names in
its context are presentation hints, never backend authorization evidence.

## Handshake and live settings

```mermaid
sequenceDiagram
  participant H as Portal host
  participant A as App SDK
  H->>A: challenge (fresh host nonce)
  A->>H: hello (protocol offers, child nonce, echoed host nonce)
  H->>A: init (bound context + presentation + one transferred port)
  A->>A: apply settings; await completion
  A->>H: ready on the bound port (revision 1)
  H->>H: reveal only after latest pending settings are acknowledged
  H->>A: presentation.changed (next revision)
  A->>A: apply locale/theme/timezone without frame navigation
  A->>H: ready for that revision
```

The child installs its listener before hello and retries every 200 ms until init
or the ten-second deadline. An unbound hello discovers the current challenge.
The host sends at most four challenges and accepts at most four distinct bound
hello attempts per session. Neither retries nor queued settings extend the
original handshake deadline.

The host fixes its Window handles and sanitized context at construction. It
accepts hello only from the exact frame Window and opaque origin `null`. The
client accepts initialization/challenge only from its configured parent Window
and exact canonical HTTP(S) parent origin, not an origin learned from an arbitrary
message. Provide that origin through trusted bootstrap configuration. Do not
read the host DOM, infer permissions from a referrer or weaken sandbox flags.

The design follows the [HTML messaging standard](https://html.spec.whatwg.org/multipage/web-messaging.html):
listeners precede readiness announcements, transferred ports establish a separate
channel, and receivers check source, origin and message shape. This design uses
`*` delivery only toward the opaque sandbox, with public initialization metadata.
No confidential credential may be added to that payload.

Init and every port message bind installation UUID, generation, policy revision,
release SHA-256, protocol and all three nonces. Nonces use 128 random bits from
Web Crypto; no weak randomness fallback exists. Generation/revision counters use
canonical decimal strings through signed 64-bit maximum; no Number conversion
rounds them. The client exposes frozen snapshots with readonly types.

Only init transfers a port. Extra ports on hello, challenge or live channel
messages refuse; cleanup is bounded. Unknown fields/types, unsupported protocol,
malformed settings, unknown/duplicate capability hints and invalid identities do
not open a generic RPC path. A legacy message never negotiates a v1 fallback.

## Setting application and backpressure

The `onPresentation(presentation, context, signal)` callback must complete only
after settings have been applied. A synchronous DOM update can return void;
React integration must resolve after the relevant commit/layout effect.
Calling `setState` and immediately resolving is insufficient to prove that the
new presentation is on screen. The React root/host loading surface is separate
remaining work, not supplied by this transport helper.

The host keeps one in-flight presentation and one latest pending snapshot.
Updates during application coalesce; intermediates are not queued indefinitely.
Initial `onReady` waits for the latest pending snapshot's acknowledgement. After
reveal, setting updates retain the document and do not repeat initial `onReady`.
Each later in-flight update has a ten-second acknowledgement deadline, which
further updates cannot extend.

Presentation is a closed, bounded structure: en-US/zh-CN/zh-TW, document/viewport,
an Intl-recognized timezone up to 64 ASCII characters, five safe color tokens up
to 48 characters and finite radius 0..32. Hex/RGB/RGBA literals are accepted;
URLs, CSS variables and arbitrary style declarations are not. Accepted frames are
well below the proposed 64 KiB control-message limit. This does not prevent the
browser's initial structured-clone cost for an invalid oversized message.

Errors use fixed safe codes. Exceptions and upstream text are not serialized.
Close/failure clears timers and listeners, closes owned ports, clears exposed
context and aborts the application callback's signal. An application callback
must respect that signal for its own asynchronous DOM/resource work.

## Refresh, remount and old work

WindowProxy identity can survive document changes. Treat an iframe load or a
controlled source/binding change as a new host session: close the old controller,
keep the host loading cover in place and construct a new controller for the
verified current frame. Do not reuse a controller across installations or policy/
generation changes. Do not reveal the frame just because `load` fired.

The fresh host challenge also handles a client that had already received an init
before the next load/remount boundary. On a new host nonce, the client closes its
previous port, aborts old application work, clears context, calls `onReset` and
renegotiates. The root integration must hide its content while this happens.
Old callback success/rejection cannot acknowledge or fail the successor session.
Retired challenges are ignored; at most four retired host nonces are retained
per client document, after which a fresh document is required.

Explicitly failed/closed clients stay terminal; retry creates a fresh document/
client rather than silently restoring a failed session. Initial shell background,
actual before-paint behavior, React state/draft preservation, resizing/history,
keyboard/focus and browser acceptance remain P06 work. This module alone does not
prove the existing Status frame/refresh appearance is fixed.

## Verification

The common tests use real Node MessageChannels and structured port transfer with
controlled Window event endpoints. They cover apply-before-ready, exact large
generation values, coalesced settings, stale bindings/revisions, wrong source/
origin, closed shapes, unsafe colors, extra ports, timeout/budget enforcement,
immutable peer selection, secure-randomness absence and bounded errors. Refresh
tests cover same-WindowProxy renegotiation, retired challenge replay and late
callback rejection after replacement. The peer-selection and replacement/extra-
port regressions were observed failing before their fixes.

These are source/transport tests, not real-browser paint or platform acceptance.
Local checks passed: 1,615 Portal web tests (including 29 common bridge cases),
111 SDK tests, nine Status UI tests, two demo tests, both typecheck lanes, Portal
lint/build, template typecheck/test/build, foundation checks, 53 Python harness
tests and the cross-repository source-pin check. The predecessor schema PR's
regexp-generation staticcheck issue was fixed at its source; the updated Portal
and Apps schema PRs passed their exact-source CI. This bridge change requires its
own CI before merge.

Apps CI verifies the source/test pins. Legacy AppView, bridge/provider and installed
apps are unchanged. No database, production service, market release or server
`beta` configuration changes.
