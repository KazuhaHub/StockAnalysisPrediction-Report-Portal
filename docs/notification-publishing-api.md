# Publishing notifications from external services and apps

This is a notification capability, not an in-process plugin hook. The portal
continues to use API-in, signed Webhook-out and sandboxed iframe-in. It does not
load third-party server code or expose arbitrary host page modification. Status
monitoring is a future consumer and is not implemented by this endpoint.

## Administrator setup for a service

1. Open Manage > API tokens and choose the source-notification publication scope.
2. Choose a stable publisher name: a lowercase letter followed by up to 47
   lowercase letters, digits or hyphens. For example, `example-monitor`.
3. Copy the new token once into the service's credential store. It has the exact
   scope `notify:example-monitor`. Do not put it in an iframe bundle, URL, source
   repository or exported workflow.
4. Open Manage > Notifications. The event catalogue now includes
   `extension.service.example-monitor.notice`. Create a channel and a rule for
   this exact event. Explicit account/OU grants select personal recipients, and
   resource/audience checks remain active. A fixed external destination is an
   administrator-controlled egress.
5. Run the selected channel's explicit test first. Then make a real publication
   with a unique, stable occurrence ID and inspect its delivery history.

The ordinary `all`, `ingest` and `query` scopes do not authorize this endpoint.
A dedicated scope is intentional: importing reports does not authorize publishing
new user-facing alerts. Revoking/deleting/expiring the service token immediately
stops future publication. Token rotation with the same publisher name retains the
same event namespace and source idempotency identity.

## HTTP request

```http
POST /api/v1/notifications
Authorization: Bearer <dedicated-source-token>
Content-Type: application/json
```

```json
{
  "occurrence_id": "provider-credit-empty-2026-10-21-001",
  "title": "Provider credit unavailable",
  "message": "The configured provider has no remaining credit.",
  "severity": "warning"
}
```

All four fields are required. `occurrence_id` contains 1–128 ASCII letters,
digits, underscores, dots, colons or hyphens. Titles must be nonblank and at most
240 UTF-8 bytes; messages may be empty and are at most 8192 UTF-8 bytes. Severities
are `info`, `warning`, `error` and `resolved`. The complete request is bounded at
16 KiB. Exactly one JSON object is accepted; unknown fields are refused.

The caller cannot provide an event type, origin label, recipient, account/OU,
channel, route, resource ID, destination URL or executable/HTML action. The server
derives `extension.service.<publisher>.notice` and provenance from the actual
credential, and assigns its own UTC occurrence instant. Message content is plain
text, escaped for Email and Slack and rendered as text in the inbox. The shared
formatter prefixes the subject with the external publisher identity.

Example response:

```json
{"ok":true,"id":"<opaque-event-id>","created":true}
```

Reuse the same occurrence ID when retrying the same real occurrence. Identical
content returns the original event ID and `created:false`. Different content
under that ID returns HTTP 409. Create a new occurrence ID for a subsequent state
transition, including recovery. The occurrence ID is not a report/job ID alone
when that object can change repeatedly.

Publication and its selected outgoing/inbox fan-out commit together. Nothing is
sent by the HTTP handler. Workers own delivery, permission checks, retries and
uncertainty. A 200 confirms durable capture, not destination acceptance. A source
with no matching rules can capture successfully without notifying anybody.

Errors use the existing v1 envelope `{ok:false,error:{code,message}}`. Invalid
input returns 400; missing/incorrect publication credentials return 401; an
occurrence conflict returns 409; capacity or per-source quota returns 429; capture
failure returns 500. Retry a transient response with the same occurrence ID.
There are at most 120 newly captured events per source in a rolling hour, enforced
inside the serialized capture transaction. Identical retry returns happen before
this new-event quota. Existing global/per-channel delivery limits also apply.

Do not use this endpoint to send authentication secrets or account recovery links.
Its content can become external Email/Slack/raw data under administrator rules.

## Sandboxed installed apps

An installed app declares `notify` in its `app.json` `scopes` array. The existing
install preview/permission prompt presents this new capability. It does not grant
an app the `all` scope or change its iframe sandbox.

The trusted host grants publication only when an administrator opens the app;
ordinary users are downgraded without `notify`. The ephemeral token remains in
the host page and is never handed to the iframe. Its publication identity is
bound to the app ID, issuer account, account creation identity and session
revision, and expires with the existing short app-token lifetime.

The app requests publication through the existing bridge:

```js
parent.postMessage({
  type: 'rp:api',
  reqId: 'notice-1',
  method: 'POST',
  path: '/api/v1/notifications',
  body: {
    occurrence_id: 'check-001',
    title: 'Example notice',
    message: 'A source-owned notification.',
    severity: 'info'
  }
}, '*');
```

The host validates the iframe's window and granted capabilities through the
existing bridge. `notify` permits POST to this one endpoint, not query, report
writes, delivery history, personal inbox access or administrator settings. Query
or ingest permission alone cannot publish. The server repeats its authoritative
check; frontend validation is not the security boundary.

The resulting event is `extension.app.<installed-app-id>.notice`. The app cannot
choose another app's namespace. Each publication checks that the app is still
installed with `notify`, and that its issuer still exists, is active/nonexpired,
is an administrator and matches the token's account/session identity. Uninstall,
scope removal, password/session revision changes or account replacement revoke
publication through an old token.

An iframe runs only while open. A scheduled/background source must run in its own
service using a dedicated machine publication token; do not rely on a browser tab
or allow the portal to load its code into the backend.

## Delivery, inbox and retention

External events use the same bounded capture, explicit rules, adapters, history,
review/replay and retention as native operational events. Original personal
candidates intersect current rule grants on inbox reads; revoked OU membership
hides old items and counts. Publishing does not grant access to report/job data.
Deleting a publication credential does not retroactively erase accepted history;
configured rule selectors remain manageable so an operator can disable or remove
them after revocation.

Replay concerns an existing delivery only. It cannot call the publisher again,
recheck a provider balance, restart an analysis or recreate the event. Retention
can compact an unreferenced body, while its source identity/digest prevents a
later retry from recreating the event. No Status application, provider balance
probe or `/status` route is introduced by this API.
