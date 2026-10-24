# Notification inbox API

The personal inbox is a cookie-session surface. Every endpoint uses the existing
session wrapper and forced-enrollment gate. It does not accept an app token as a
substitute for the recipient's session, and it never accepts another username as
an input. Administrators use the separate management API for delivery evidence.

## Routes

| Method and path | Behavior |
| --- | --- |
| `GET /api/notifications` | Up to 50 currently authorized, nondismissed inbox items and authorized unread count |
| `GET /api/notifications/unread` | Authorized unread count only, without loading message bodies |
| `POST /api/notifications/{id}` | `{"action":"read"}` or `{"action":"dismiss"}` for the caller's authorized recipient row |
| `POST /api/notifications/read-snapshot` | Freeze the IDs of currently authorized unread items into a signed five-minute snapshot |
| `POST /api/notifications/read-all` | Mark only IDs from `{"token":"<snapshot>"}` that remain authorized as read |
| `GET /api/notifications/preferences` | Registered event categories and the caller's optional notification settings |
| `PUT /api/notifications/preferences` | Replace supplied category settings with `{"preferences":{"report.ingested":false}}` |

A list response has `items`, `unread` and `next_cursor`. Each item contains opaque
recipient `id`, `event_id`, registered `event_type`, plain-text `subject` and
`text`, and UTC RFC3339 `occurred_at` and `read_at` strings. Empty `read_at` means
unread. Pass `next_cursor` unchanged to the next list request. If the cursor row
was removed or is no longer authorized, reload from the first page after the
revision-conflict response rather than guessing another cursor.

Counts include all currently authorized, nondismissed unread candidates, not
just the current page. A maximum of 10,000 candidate rows is checked per request;
exceeding this bound returns capacity unavailable instead of a misleading count.
Retention and cleanup acceptance remain tracked separately. Queries load only
metadata until authorization succeeds, and message bodies are fetched only for
the requested page. Repeated resource and routing checks are cached within the
request, never across changes in account or resource permissions.

## Permission and audience checks

A persisted recipient row establishes original candidacy. It cannot grant
permission to a report, task or operations event. Each list, count and mutation
rechecks active/nonexpired account and current resource authority. A current
matching enabled inbox rule must still select the caller by account/OU grant or
creator strategy. OU grants include descendants; moving out of that scope or
removing its grants hides old rows and removes them from the unread count.

An explicit, consumed task-notification opt-in also authorizes the original
creator's inbox entry while that creator still owns the resource. Ownership is
bound to the account generation captured at submission, so a reused username
cannot inherit old task messages. Inbox items for a deleted resource become
invisible. A missing or unauthorized item uses the same not-found response.

`read` preserves the first read instant; `dismiss` preserves the first dismissal
instant and removes the item from subsequent lists/counts. Neither deletes
business data or another account's recipient state. Authority is checked again
before applying a read-all snapshot; a snapshot cannot restore revoked access.

## Snapshot semantics

The read-all action is two steps so the UI can show the exact count before
confirming. The snapshot contains original recipient IDs, the caller's account
name and generation, and a five-minute expiry, protected by a purpose-separated
HMAC. It is not encrypted and must be treated as a short-lived request value,
not logged or placed in a URL. Its contents contain no notification bodies,
credentials, report summaries or reset links.

The apply endpoint verifies signature, caller, expiry and account generation.
Later arrivals remain unread, including a new event whose business occurrence
time precedes the snapshot. A different account cannot use the token. Deleting
and recreating the same username invalidates it. Repeating a valid application
is safe: read timestamps already set are preserved.

## Preferences

An unset registered category defaults to enabled. Unknown categories are refused;
preferences cannot register an arbitrary event type or change a routing rule.
Preferences apply to future optional capture and unsent personal operational
Email. They do not delete already visible history. A mandatory rule may override
an optional preference, but cannot override resource authority or audience
revocation. Authentication, signup verification and password-reset Email never
consult these preferences.

## Browser behavior

`/notifications` provides message cards, read/dismiss actions, a confirmed
snapshot-based read-all action and category preferences. Message text is rendered
as React text, never injected HTML. The header bell links to the page and polls
only the count. Both use the existing visible-page polling helper on a 30-second
interval with cleanup on unmount. Failed count requests clear the badge rather
than asserting a false zero; the full page exposes a retryable load error.

The personal browser bindings are `notificationInbox` in
`web/src/api/notifications.ts`. They are intentionally separate from administrator
channel/history bindings in the same module. Current content formatting is the
shared system formatter; additional configurable software presets and template
acceptance remain separate work packages.
