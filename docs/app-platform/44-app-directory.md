# Audience-filtered application directory

The existing `/apps` hub now includes v2 installations alongside built-in and
legacy apps. A current audience member can discover a static installation and
open its protected `/apps/v2/:installation` page without knowing an installation
UUID. This is discovery over existing control records, not a completed installer,
marketplace release, managed backend lifecycle or production rollout.

Subsequent [signed display integration](45-signed-display-metadata.md) adds
verified localized copy/icons without frontend archive reads. The description
below records the initial directory slice.

## Server contract

`GET /api/app-platform/apps` uses the central signed-session/enrolment wall,
private no-store response headers and the existing two-slot platform HTTP budget.
It accepts no parameter other than one optional `after` containing a canonical
installation UUID. Unknown/duplicate/malformed parameters are refused. Queries
and transactions have the existing five-second platform timeout; blocked SQL uses
the context-bound executor.

The response is `{items: [...], nextCursor: "..."}`. Each item contains only:

| Field | Meaning |
| --- | --- |
| `installationId` | Canonical identity used by the protected launch page |
| `applicationId` | Bounded plain-text application ID from its admitted control record |
| `state` | `installed`, `pending`, `disabled` or `attention` |
| `openable` | Whether the current control-state projection offers a launch attempt |

No descriptor, secret, runtime address, permission plan, identity grant, launch
binding or health probe result is returned. Localized publisher display names and
icons still need their verified metadata integration; this slice uses the app ID
and the shared generic Ant icon. Directory labels are not a signature recheck.
The launch/document handlers separately recheck the actual package and current
publisher trust before serving executable bytes.

## Audience and transaction rules

1. Reconstruct the current session/account lifecycle and hold the principal guard.
2. Load the actual OU chain and its creation identities under the same topology
   protection used by launch authorization. A Default policy fallback is not an
   actual ancestor and must not manufacture access.
3. Filter by exact direct-user lifecycle or exact current OU lifecycle **before**
   the page limit. Administrator role adds no audience bypass. Removed
   installations are omitted. Read at most 33 matching installation candidates.
4. Return at most 32 items, rechecking/holding each matching audience grant. A
   nonempty continuation is the last returned authorized installation ID; an
   unauthorized app cannot consume the page or become its cursor.
5. Refresh database time and recheck the current account/session before commit.
   Expiry, cancellation or any error discards the complete page.

Pagination uses installation UUID order and a strict `id > after` continuation.
It has no total count and is not a multi-request snapshot: installations/grants
can change between pages, so a refresh starts again at the beginning. Each page
independently authorizes its reader. Previously displayed metadata is not
remotely recalled after revocation; opening still makes a fresh authorization
request. Live invalidation remains later work.

## Control-state projection

- `disabled`: desired state is disabled; no open action.
- `pending`: pending/setup-required control phase; no open action.
- `installed`: enabled/ready static control row, current controller epoch and
  restore not paused; offer a launch attempt.
- `attention`: remaining combinations, including managed runtime rows for which
  this implementation has no launch provider; no open action.

These labels do not assert physical readiness, service health, completed provider
work, successful signature inspection or a permission to run. Even an
`installed` item can be refused by the actual launch handler if activation,
namespace, package, trust, session or authorization has changed. A fresh database
returns an empty array without bootstrapping control state, keys or a runtime.
No table, migration or persistent storage product is added.

## Client integration

`useAppDirectory` requests the initial page and exposes a localized Load more
control in AppsHub. The parser enforces bounded pages, canonical increasing IDs,
known states, boolean open hints and a continuation matching the last item.
Backward/duplicate continuations are refused without appending data. Failure
shows a fixed translated warning with retry; raw diagnostics are not displayed.
A retry restarts the list to pick up current grants and state.

Account changes hide previous cards during render, cancel pending requests and
fence stale responses. Unmount also cancels a pending pagination request. The
existing API client still owns session-loss handling. This is not an independent
credential/session store. Built-in/legacy apps remain visible when the v2 endpoint
fails, and v2 apps remain visible when the legacy endpoint fails.

Cards reuse Portal's existing grid, Ant components, tokens and translations in
English, Simplified Chinese and Traditional Chinese. Disabled/pending/attention
cards expose an accessible disabled state without a click/keyboard open action.
Installed cards navigate to the existing protected launch page. No independent
app-language selector, iframe wrapper or alternate theme system is introduced.

## Verification and limits

- SQLite and disposable PostgreSQL tests cover direct-user lifecycle grants,
  actual parent OU access, topology changes, Default fallback refusal, stale OU
  identities and filtering before pagination.
- Real mux tests cover authenticated/anonymous access, no-store headers,
  revocation and strict query handling. Administrator role does not bypass an
  audience grant. An uninitialized empty directory leaves durable state intact.
- Actual PostgreSQL installation-lock waits test session expiry after admission
  and context cancellation before the holder releases. Both return no page and
  leave durable state intact.
- React/API tests cover launch navigation, disabled cards, partial endpoint
  failures, parser refusals, continuation, retry, account-switch stale responses
  and unmount cancellation. These are component/hook tests, not full browser or
  D5 visual acceptance.

The preceding launch-page source at
`bb98074e5414975b964da0ff1a5d9ecf81e79c6c` passed CI run 40273040782.
Full local web regression passed 1657 tests across 148 files. Typecheck, lint
(existing unrelated warnings), production web build, Go vet, staticcheck and Go
build passed. Targeted directory/launch/HTTP race checks with disposable PostgreSQL
passed in 46.990 seconds. Exact-source CI for this slice must still be checked.
Production and server beta configuration are untouched. Full installer activation,
market linkage, verified display names, managed launch, navigation/drafts,
document sizing, sensitive broker policy and remaining P00–P14 acceptance stay
open.
