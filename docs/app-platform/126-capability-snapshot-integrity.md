# Capability snapshot integrity before broker adapters

Status: development implementation and verification in progress. This closes a
P07 authorization-consistency gap; it does not implement capability providers,
persistent application files, credential bindings, tasks or notification delivery.
P00–P14 and D5/D6 remain open. No new database products, migrations, dependency
changes, production activation, Status adoption, publication or beta changes.

## Problem and resulting behavior

The current development installer refuses every required platform capability and
records every optional capability as declined. It does not grant a network,
file, report or secret provider. Installation execution already checked each
stored denied row, while completed readers checked only for a nonzero granted
bit or a mismatched policy revision. That weaker reader check could still accept
a missing/unknown denied row, a changed policy JSON or a row bound to another
review. It did not create an implemented provider, but it left the live permission
snapshot inconsistent with the declaration/review before future adapters exist.

Readers now require a complete exact denied-capability snapshot derived from the
verified signed descriptor and the consumed review referenced by the completed
installation operation. An inconsistent record makes launch/document/backend
access unavailable. Readers never repair, grant, rewrite or normalize records.
The host returns its existing safe unavailable response; no stored policy or
review diagnostic is exposed to the application.

## Source of authority

The caller still owns current account/session/OU audience, installation state,
restore pause/epoch, release signature/trust, runtime and physical-readiness
validation. The new helper is called inside that existing transaction and grants
no standalone authority.

`verifyPlatformLaunchCapabilitySnapshot` checks:

1. There is exactly one completed install operation for the current installation,
   source generation zero, target one, matching controller epoch and target
   release, with a consumed install review from the same epoch.
2. The review is bounded, canonical, hashes to its stored digest and binds the
   same application, publisher, release, epoch and development API.
3. Its sorted declined set exactly matches the signed optional declarations.
   No required capability is admitted by the current provider-less profile.
4. Stored capability rows exactly cover that set. Each row is declined, contains
   precisely `{}` policy, has the installation's current policy revision and
   references that exact consumed review ID. Missing/extra/unknown rows, changed
   policy or another otherwise valid review all refuse admission.

The shared `verifyPlatformDeniedCapabilityRows` performs step four for both the
installation worker and completed readers. Worker errors retain the existing
stage-evidence code; reader errors retain unavailable. The helper closes rows on
all paths and stops at the first extra row; it does not build an unbounded array.
The primary-key capability order supplies deterministic set comparison.

Static launch/document readers and both managed/writable backend access paths
use the same check. The latter validate the exact stored descriptor digest after
current publisher verification before checking the snapshot. Backend dispatch
still repeats its full access boundary after external work before releasing any
buffered response. A cached launch binding cannot bypass a later refusal.

Current audience-only policy revision changes remain supported: denied rows must
match the new current policy, while the original consumed review continues to
bind the requested/declined capability set. Exact decimal values above JavaScript
safe integer range remain unchanged. Future code/capability updates require their
own reviewed activation model; this helper does not invent update approval.

## Test and acceptance evidence

Failing tests first proved that static readers accepted missing/unknown/policy
and corrupted-review snapshots, then that managed and writable backend readers
accepted an unexpected denied policy/row. They now refuse. Additional cases cover
a canonical rehashed review whose declined set no longer matches the signed
package, and a denied row linked to another valid copied review record.

The tests first verify a coherent fixture succeeds, mutate only the relevant
records, assert refusal and compare database dumps to prove the read did not
repair anything. They also try a previously returned launch binding against the
changed records. Both SQLite and PostgreSQL cases are included; PostgreSQL is
run in the existing CI database job before the inherited vulnerability gate.
Existing signed launch/history/current viewer, policy revision, actual install
staging, document generation and native/browser acceptance remain required.

Local affected SQLite launch/backend/installation/frontend suites, targeted race
checks, `go vet` and Linux amd64 test compilation passed. The initial six refusal
cases were first observed failing against the old reader behavior. PostgreSQL
is not available locally; its explicit CI step remains required. Native/browser
and exact CI revision evidence remain pending. No screenshot is authorization
evidence, and this local result does not imply complete P07 acceptance.

## Next required work

This is a prerequisite, not a replacement for the broker. Actual adapters must
independently enforce operation/resource bounds and current actor/service access,
review required/optional permissions and prove revocation before any grant is
supported. File ownership/quota, allowed network destinations, secret-binding
custody, schedules/events and notification-source rules remain separate work.
Their unapproved physical storage products still require a concrete extension
proposal and owner decision. The current zero-provider default stays closed.
