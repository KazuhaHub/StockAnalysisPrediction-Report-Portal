# Portal install data intent and receipt commit

This connects the Portal install operation to signed native data preparation and
back to the existing transaction/state model. It advances an approved initial
static install from prepare_data to activate only after authenticated data
receipt retrieval. It does not activate the app, start a backend or register an
automatic worker/HTTP installer. The full application platform remains in progress.

## Static application versus shared data service

An initial static installation keeps runtime_id NULL and managed=false. The
platform's single enabled runtime binding supplies the shared data-preparation
service; only the typed prepare-data effect references it. Data service selection
must not accidentally turn a static frontend into a managed backend application.
No managed package/capability acceptance is broadened.

## Prepare durable intent

`Server.preparePlatformInstallData(ctx, lease)` accepts only the exact current
initial static install lease at prepare_data. Before opening the transaction it
loads the existing DEK cache. The context-bound transaction locks OU mutation,
publisher trust, the one enabled confirmed runtime, installation and operation.
It rechecks the consumed review, exact signed release/artifact, original sponsor's
current account generation/admin/active/expiry, declined capabilities, installation
policy and current controller/pause/lease state.

The runtime key must still be sealed for this binding, have its stored public-key
fingerprint, match the controller epoch and remain enabled at the expected initial
confirmed revision. The seed is decrypted only for signing and is not returned;
local seed/private-key copies are cleared after use. No key reaches an app, a
command body or this documentation.

The transaction creates exactly one pair, or validates that exact original pair:

| Record | Initial values |
| --- | --- |
| data namespace | Stable random UUID; origin/owner this installation; review_required; format 0; reviewed 256 MiB quota; observed bytes 0; writer fence 0; revision 1; no retain/delete timestamps |
| operation effect | prepare_data / slot 1 / prepare-data; stable random effect UUID; exact logical payload digest; data runtime ID; current epoch/original lease fence; observed fence 0; prepared; receipt {} |

Here review_required means unprepared intent, never active data. A restored row
has cleared ownership and increased revision/fence, so it cannot look like this
fresh pair. Missing halves, extra effects, unexpected access/activation rows,
changed owner/schema/observation, uncertain prior execution or fence takeover
refuse rather than reconstructing intent or replacing a historical effect.

The command derives namespace, release and data schema from the durable pair and
verified reviewed descriptor. It carries the reviewed 256 MiB byte limit, initial
fixed 65,536-inode policy and writer fence 1. Signature renewal preserves logical
digest, namespace, effect and original fence. A reclaimed higher-fence lease needs
the future explicit takeover/reconciliation protocol; this method refuses it.

A command is signed only after late lease/publisher/sponsor checks. The DB and
local UTC clocks must agree within one second. Expiry is the earlier of local
issue + 30 seconds and DB lease expiry minus the full one-second skew bound; an
exhausted conservative window refuses. Signature validity is checked after commit
before returning. A failed namespace/effect/audit transaction returns no command
and leaves no partial pair. `control.Sign` validates the complete signed envelope
against explicit authority; signing itself never admits an effect or grants a lease.

The method creates intent only. It opens no socket and does not dispatch work.
Native admission/reservation/one-shot execution and recovery remain separate
runner infrastructure, with all existing uncertainty rules.

## Retrieve and commit actual evidence

`Server.completePlatformInstallData(ctx, lease, intent, configuredEndpoint)` calls
the root/non-root-controller receipt reader. It accepts no receipt JSON, app-ready
flag or admission acknowledgement from a request. Unsupported platforms refuse.
The authenticated channel creates the opaque observation; only this endpoint
entry point produces the private repository observation in production code.
Synthetic observations are limited to repository transaction tests.

The repository transaction repeats all current runtime/key, review, publisher,
sponsor, installation, namespace/effect and lease checks. It reconstructs the
command from durable state and verifies the exact original raw signature, public
key and payload. Evidence must match the configured root profile/path/UID/group,
controller identity, exact command digest and original logical ownership. It must
be no older than three seconds, nonfuture, canonical, and have a valid observed
time and exact reviewed quota/schema/fences. Both normal and recovered native
receipt formats use the shared receipt validator.

In one context-bound transaction it then:

1. Changes only the exact pending namespace to active, verified schema/observed
   usage, writer counter 1 and revision 2.
2. Changes only the exact prepared effect to applied, observed original fence and
   canonical native receipt JSON.
3. Advances the operation model to activate, incrementing state/row revisions with
   a full epoch/owner/fence/deadline CAS, and writes its stage audit.
4. Rechecks current lease/pause, publisher/sponsor authority, proof age and command
   signature after potentially blocking writes before committing.

All statements use the existing context transaction adapter. Context cancellation,
expiry, audit delay or any write failure rolls the three records back together.
A later retry must read fresh authenticated evidence; it must not re-execute native
creation. A lost DB commit acknowledgement requires inspecting durable stage and
receipt history, not guessing the stage failed.

Namespace active and writer counter 1 are metadata, not an application writer UID,
mount, capability grant or container permission. The installation stays pending,
runtime_id remains NULL, and no activation/access row is created. Audience review,
activation authority and later runtime/readiness gates still apply.

## Verification

Tests first failed for the missing intent API. SQLite and PostgreSQL tests cover
stable signed identity, conservative lease expiry, exact pending tuples, changed
key/runtime/sponsor/fence, restore pause, atomic namespace/effect insertion, opaque
endpoint requirement, transactional data/effect/stage commit, stale retry and
private/stale/future/mismatched receipt refusal. Trigger failures after namespace
or effect writes prove rollback on both databases. Real PostgreSQL audit delays
prove post-write proof expiry and query-context cancellation roll all rows back.
A held SQLite connection verifies canceled pool waits do not commit. Shared signer
checks refuse short/wrong keys, expired messages and mismatched payload/authority.

The extended owned-XFS workflow runs an actual non-root Portal process. Its test
bootstrap seeds an enabled binding for the already enrolled public development
fixture deployment using the real DEK sealing format; operator enrollment and
confirmation have separate prior cross-UID acceptance. No production credentials
or fixture key enrollment are used. The child consumes a real signed publisher
review, stages an install and emits its signed typed data intent through a public
fixture pipe. The root parent verifies it, admits/reserves/claims/executes actual
XFS preparation, and exposes the root receipt endpoint. The child calls the real
completePlatformInstallData entry point, retrieves the authenticated native receipt
and proves data/effect/stage commit with no app activation/backend. The parent
independently inspects the exact physical inode and 256 MiB / 65,536-inode limits.
The same single runner journal is reused without overlapping project allocation.
The public descriptor is copied into an owned traversable fixture tree; the child
never needs the CI worker's private checkout/home access.

```sh
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 go test -p 2 ./...
TEST_POSTGRES_DSN='<disposable test database>' GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 go test -race -p 2 ./internal/app -run 'TestPlatformData(Intent|Stage|Context)'
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 go vet ./internal/app ./internal/apprunner/control
python3 -B -m unittest discover -s scripts -p project_quota_probe_test.py
```

Source `daf3f154fa1b65bc8d7fa2f89e7211783f8e87c8` passed
[native run 40311415337](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40311415337).
The downloaded report confirms native_portal_install_data_stage and
native_portal_data_stage_not_activation, prior native receipt/recovery/quota
checks, and owned cleanup. Full local Go tests with disposable PostgreSQL passed
(application package 143.415 seconds); targeted SQLite/PostgreSQL race tests
passed in 21.947 seconds, including actual delayed-audit expiry/cancellation.
Vet/staticcheck, Linux amd64/arm64 cross-compilation and twelve Python refusal
tests pass. A negative overlay removing the root-profile requirement fails at the
private-profile stage assertion. Main/daemon CI have independent status. A test
binding bootstrap is not production bootstrap/rollout acceptance. No native arm64
claim follows from cross-compilation.

## Remaining work

The overall plan's runtime-free/non-Linux static installation path remains a
requirement; this private native-data adapter is not the final provider choice
for every static deployment. Existing static/legacy routes are unchanged.
Static activation/audience grants, automatic bounded worker dispatch/recovery,
market review/install/progress UI, higher-fence takeover, partial/absence recovery,
managed start/probe/writer admission, backups/removal, SDK/visual freeze and release
acceptance remain. There is no production startup, privilege enrollment, schema or
baseline change, real service credential use, Status migration or server beta
configuration change in this slice.
