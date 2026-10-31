# Application notification publishing

Status: H08 development implementation, 2026-10-30.

## Integration

Installed v2 apps publish through the existing notification capture, central
routes, channels, inbox, history, replay/review and retention implementation.
This extension adds no notification table, queue, recipient registry or delivery
worker.

`notifications.publish` is now an explicit selected install capability. The
signed manifest must declare it and the current capability row must grant it.

There are two application entry points:

```text
POST /api/app-platform/installations/{installation}/notifications
POST /api/app-platform/installations/{installation}/notifications/background
```

The foreground route requires the current session and exact launch binding. The
background route requires a current H07 service assertion with
`notifications.publish` in its resource lease. The service assertion is sent in
the bounded `X-Portal-Service-Identity` header as canonical unpadded base64url.

## Closed publication input

Both routes accept only:

- occurrence ID;
- title;
- plain-text message; and
- severity (`info`, `warning`, `error`, `resolved`).

The foreground body additionally carries the existing launch binding. Requests
cannot provide an event type, recipient, account/OU, channel, route, event ID,
destination URL, signing secret, HTML or executable action. Unknown fields,
duplicate keys, trailing JSON, oversized content and noncanonical encodings are
refused.

The server derives the namespace from the current verified app:
`extension.app.<app-id>.notice`. An app cannot publish in another app's or an
external service's namespace.

Occurrence identity is `extension:app.<app-id>:<occurrence-id>`. Identical retry
returns the original event and `created:false`; changed bytes under the same
occurrence return conflict. New source events remain subject to existing global
and per-source capacity.

## Authority and delivery

Foreground publication rechecks the current session, app audience, launch
generation/policy/release and notification capability before capture.
Background publication rechecks the H07 service assertion, installation state,
epoch, policy, release, capability and resource lease immediately before
capture.

Administrator rules alone decide recipient mode and channels. The source cannot
address a destination. Capture commits the event and selected inbox/outgoing
candidates together; the HTTP request performs no network delivery. Existing
workers recheck rules, audience, preferences and channel revisions before send.

The administrator event catalogue includes currently installed app namespaces,
and keeps already configured selectors visible after publication revocation.
Disable, uninstall, capability removal, generation/policy change, restore pause
or expired service assertion denies new publication.

Audit records contain the event and non-secret application publisher only.
Titles/messages are bounded plain text and are escaped by the existing Email,
Slack, webhook and inbox renderers.

## Verification

Focused tests cover:

- foreground anonymous refusal and launch-binding admission;
- service-assertion publication with no browser session;
- catalogue registration, occurrence idempotency and conflict;
- recipient/unknown-field injection refusal;
- central rule routing to a disposable webhook receiver;
- shared worker delivery and no HTTP-handler network side effect;
- capability revocation and service-resource drift; and
- capability selection for `notifications.publish`.

Native browser acceptance, real external providers and complete CI remain
evidence gates. H08A streaming remains a separate bounded protocol decision.
