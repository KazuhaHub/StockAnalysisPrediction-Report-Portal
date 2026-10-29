# Complete writable background installation and independent ownership

This follows [114](114-writable-install-scheduling.md). The previous native case
proved background queue selection/progress only to a deliberate failure boundary.
This continuation adds a second fresh application whose complete installation is
performed exclusively by the actual background service. Production runtime code,
protocols, schema, dependencies and deployment configuration do not change.

## Fresh install through the service

`testNativeWritableBackgroundCompletion` uses the existing disposable real root
runner and non-root Portal controller. Its application ID is
`notes-background-demo`, distinct from the earlier writable failure fixture and
readonly managed fixture. Use the actual controlled immutable OCI image and
architecture from the authenticated runtime observation, not a fabricated profile.

1. Build and sign the descriptor, stage the actual frontend archive and create
   the writable review with the current installer account as the reviewed user
   audience. Consume through the actual writable admission path.
2. Require queued state and no effects. Save only the test cleanup ownership
   record described below before any background/native work begins.
3. Start `newPlatformWritableInstallScheduler(...).serve` as the actual non-root
   controller with a 50 ms interval and a 25-second enclosing deadline. Poll
   durable operation state; do not drive stages from the polling loop.
4. The service selects/claims, renews serially and advances all artifact, data,
   writer/start, readiness, registration/boot and activation stages itself. No
   manual claim, stage/candidate preparation or injected failure advances this
   new installation. Internal bounded observation/retry behavior remains allowed.
5. When the database says complete, cancel and join the service before inspecting
   its mutable fields. Require ready state, exactly four applied effects, one
   claim audit, fence 1, no review transition/error and cleared database/local
   ownership. A duplicate native start is independently checked by the root
   witness, not inferred solely from the four Portal rows.
6. Prepare current completed backend access, serialize its public signed start,
   original writer receipt and actual running receipt for the independent root
   witness. No private registration document or secret is exported.
7. Demote the fixture installer to an ordinary user and call the actual backend
   POST/GET using current Portal access. A forged role/subject in the app body
   must not change the authenticated viewer; POST increments once, GET stays at
   one, and the backend returns the current ordinary-user identity. Restore the
   fixture account role afterward so existing independent cases retain their setup.
8. Generic managed browser launch must still refuse. This private host request
   proof does not silently bypass or implement the pending browser route gate.

The new full case runs before the older readonly registration RPC coordination
case. Its extra work therefore cannot age the older case's captured short-lived
command while that case waits for final parent inspection.

## Cleanup ownership before native start

A fully automatic service cannot require a test to manually prepare its start
command just to save a cleanup witness. Before starting the service, the
controller writes `writable-auto-owner.json` in its already owned disposable
fixture directory. This contains only a canonical signed **test ownership** record:

- Separate signature domain `portal.fixture.writable-install-owner/v1`.
- Exact deployment/epoch/control revision, unique admitted operation/installation,
  release digest, expected controlled image reference and issuance/expiry.
- At most 90 seconds of cleanup lifetime, no future or expired record accepted.
- A fixture-only file and parser; no production listener, execution command,
  readiness proof, table, persistent side-store or public SDK contract consumes it.

The root parent verifies the expected authority and controlled image, signature,
canonical outer/inner JSON, UUIDs/digest, revision/epoch and time bounds. It never
imports private keys. The fixture's existing development control seed signs this
separate purpose; the backend has no access to that seed or fixture directory.
Ownership-record unit tests cover signature, canonical bytes, wrong deployment/
epoch/revision, identity/hash/image mismatch, future/expiry and excess lifetime.
They establish cleanup binding only, not a native execution observation.

## Independent physical witness and fallback cleanup

After the child has completed, the root verifies that the public completed start
belongs to the pre-recorded operation/installation/release. Resolve the controlled
immutable image to its native image ID, and repeat the existing strong start
witness checks: historical command signature, exact writer/running receipts,
ownership labels, original process identity, no restart and full physical
256 MiB / 65,536-inode quota proof. A missing completed start fails acceptance.

Then preserve that verified public source as
`writable-auto-start.verified.json`, intentionally making the usual completed
snapshot unavailable for cleanup. This exercises the interrupted-controller
cleanup path after successful physical witnessing:

1. Stop/join the root transport service before cleanup.
2. Select by the exact unique operation from the pre-recorded signed owner,
   accepting zero resources or at most one exact container; ambiguity refuses.
3. Require the native/config image ID to equal the inspected immutable image.
   The driver creates containers using image IDs, not image reference strings.
4. Require exact deployment/epoch/installation/operation/release, generation 1,
   control/writer fences 1, writable format/profile, UUID-valued namespace/
   resource/start identifiers and SHA-valued data/writer/start ownership digests.
5. Require the exact resource-derived name and creation time within the ownership
   window. A failure before image/container creation requires no image inspection
   or mutation. No broad container prune or name-prefix deletion is allowed.
6. Stop/remove that one owned container and release only its installation's IPC
   storage through the existing native API. This is disposable fixture cleanup,
   not implementation or acceptance of production disable/uninstall.
7. Independently re-witness the first writable application's actual process and
   quota proof while it remains alive, then let its existing cleanup run. Thus
   removing the new app must not consume its neighbor's resources.

The normal cleanup path can still use the full completed signed start whenever
that snapshot exists. The ownership-only fallback never counts as successful
installation evidence. Root cleanup has its own defer so a failed auto-case
cleanup does not prevent the older independent cleanup defer from running.

The Portal child test limit becomes 60 seconds, its parent test 100 seconds and
the Python subprocess 110 seconds to accommodate the additional actual app and
cleanup. These are bounded test-only limits; production attempt/readiness/lease
limits remain unchanged.

## Evidence and remaining scope

The report requires six new `native_portal_writable_background_*` flags: complete,
current_viewer, native_witness, owned_cleanup, fallback_cleanup and
neighbor_preserved. All earlier failure, transport, quota, identity and owned-
cleanup flags remain mandatory. A missing marker or failed parent/child cannot
produce an accepted report. Ownership parser tests also run in the explicit CI
SQL/scheduling step before the existing vulnerability gate.

Local ownership race checks (1.790s), Linux amd64/arm64 test compilation,
Linux vet/staticcheck and Python14 passed. Functional source
`ecc1134fb177eba871b1c7570818226a28f3d47d` passed
[quota workflow 40833806816](https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/actions/runs/40833806816),
job 123313800713. Checkout verified its merge into parent `a8d16940`.
All six new background flags, prior native flags, full quota witness and owned
cleanup are true. The root verified fallback deletion and neighboring process
preservation, and disposable registry cleanup succeeded. The intentional
`execution_ready=false` does not turn fixture success into production enablement.
General workflow 40833806834/job 123313800959 passed all explicit SQLite/
PostgreSQL suites: lifecycle 77.011s, current/legacy access 39.915s, worker/static
15.208s, queue/scheduling/legacy plus ownership 16.098s. The ownership refusal
unit case explicitly passed. Build/vet/full Linux staticcheck, published market,
arm64 numeric, all app race shards/aggregate, workflow lint, web and Docker
font/smoke checks passed. Shard 1/job 123313800910 ran full journal race 211.815s
and its Portal subset 60.478s; aggregate job 123315663353 passed. The Go job
failed the existing x/net v0.60.0 GO-2026-6866/6860 vulnerability gate, so ordinary
full tests were skipped. Dependencies are unchanged and nothing is suppressed.
No standalone daemon workflow was triggered by this test/fixture-only change.
Evidence applies to the named functional source; later documentation-only heads
may trigger CI again.

This advances P05/P12 and the background installer portion of LIFE-02. Ordinary
viewer demotion/body-spoof checks cover scoped AUTH-04/05 behavior. It does not
complete AUTH-02 OU enumeration, clean public-market/browser installation,
production stop/uninstall, restart/expiry recovery, upgrades, broker/tasks,
SDK/reference-app publication or the full P00–P14 release. D5 visual acceptance,
public SDK freeze, D6 production rollout and Status adoption remain separate.

## Browser integration boundary

The current launch reader still deliberately rejects runtime-bound installations
and descriptors with a backend (`app_platform_launch.go`); the legacy AppView
still uses its scoped static bridge. The next implementation must use completed
writable history and current account/OU/audience/trust checks for managed assets
and launch, with a typed host-only backend request bridge. Removing the runtime
or manifest rejection alone would bypass the missing integration boundary. Do
not reinterpret a managed app as static, relax sandbox/CSP, hand tokens/private
registration to the iframe or route an app-supplied backend URL. Shared language,
theme, navigation and shell presentation remain part of the broader P06 work.
