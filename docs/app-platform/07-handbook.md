# Developer and operator handbook specification

Status: required workflows for the future platform. The v2 tools, product pages
and endpoints described here do not exist yet. Do not paste proposed commands
into production. [Implementation work packages](04-implementation.md) distinguish
existing repository checks from future deliverables.

## 1. Where work belongs

| Work | Repository / owner |
| --- | --- |
| Generic host, app shell, account/OU policy and platform APIs | Portal repository |
| First runner/control/broker adapter | Portal repository, separately built executable/image |
| SDK, templates, package/publish validation | Shared Apps repository |
| Individual business apps and reference samples | Shared Apps repository under `apps/<id>` |
| Production data, runtime keys and deployment receipts | Protected server storage, never Git |

Do not copy a business app into Portal to make an API integration easier. Do not
change the core analysis queue to run app tasks. Adding a new app after v2 release
must require only a compatible package and its declared permissions/configuration.

## 2. Developer walkthrough to implement and verify

The eventual CLI examples below are a specification for P11. Their names can be
changed at D3, but the complete workflow and outputs must remain documented and
tested. Current `scripts/create-app.py` creates a legacy SDK frontend and is not
this v2 managed-app generator.

```text
portal-app create notes-sample --template static
portal-app create digest-sample --template managed
portal-app dev
portal-app validate
portal-app test
portal-app pack --output dist/
portal-app inspect dist/<app>-<version>.portalapp
```

Expected developer steps:

1. Choose static or managed. The generated project includes the manifest schema,
   typed locales, frontend root, tests, package config and a compatible SDK pin.
   Managed mode adds a backend Unix-socket entrypoint, safe health route, explicit
   data schema and example idempotent task/event handlers.
2. Run the development host. It supplies a fake authenticated viewer, current
   OU ancestry, themes/locales/timezone and denied/allowed capability scenarios.
   It never copies the developer's production cookie, credentials or database.
3. Describe routes and page metadata through SDK APIs. Use host title/actions
   and navigation; render business content only. All user copy uses typed locales.
4. Add only required capabilities. Specify optional feature behavior and network
   destinations. Do not request raw credential read when broker use is sufficient.
5. Test normal and denied cases, unsaved drafts, keyboard/focus, narrow layout,
   background retry and revision/identity changes. Do not test only component render.
6. Build a complete package. Its backend includes runtime libraries and other
   build dependencies. It cannot download arbitrary executable dependencies at
   installation or startup outside the signed artifact contract.
7. Inspect without executing. Verify compatibility, routes, capabilities, digests,
   resource defaults and frontend/backend identity.
8. Install through the normal local acceptance market/permission flow. Avoid a
   special development endpoint that bypasses publication/authorization checks.
9. Publish using the guarded signing pipeline; externally download and verify the
   actual artifacts before adding the version to the canonical market.

The generator integration test must perform these steps using a clean checkout,
not depend on globally installed tools or hidden symlinks. Fixture secrets must
be unmistakably disposable and cannot be allowed to reach real external services.

## 3. Developer checklist for a first app

- Manifest and signed artifact set match the code actually served/executed.
- No parent DOM/cookie/local storage access and no private host imports.
- Shared root, theme/language/timezone and a registered route.
- Required/optional capabilities and denied-state explanations.
- Foreground writes happen only after deliberate actions; rerender/locale changes
  cannot repeat them. SDK retries reads only within documented rules.
- Background handlers have idempotency, deadlines and safe classifications.
- Credentials absent from logs, errors, frontend bundles and app metadata.
- Explicit schema compatibility and migration/rollback fixtures.
- Stop/uninstall/recovery behavior tested before publication.

## 4. One-time runtime administrator setup

This procedure must be delivered as a versioned tested deployment guide in P04/
P13. It is not a request to modify the current server.

1. Confirm a supported Linux runtime profile and sufficient isolated disk/resources.
   Record architecture, Docker mode, kernel and quota support. Unsupported hosts
   remain static-only until a supported adapter is installed.
2. Obtain the platform-compatible runner from the official verified release.
   Verify signature/digest and compatibility before execution.
3. Prepare owner-only runtime directories. Proposed logical roots are control,
   journal, verified artifacts, installation data, sockets and recovery. Resolve
   final filesystem ownership/path layout in D1/D2, not from an app manifest.
4. Provision the runner's restricted control socket and infrastructure trust keys
   outside Git. Mount only the required control interface into Portal. Never mount
   the Docker socket or application data roots into Portal/app frontends.
5. Start the runner with explicit host-wide CPU/memory/disk reservations and app
   quotas. Ensure the Portal process, database and OS retain measured headroom.
6. Enroll that deployment through the reviewed local bootstrap mechanism; this
   is platform infrastructure pairing, performed once, not per-app pairing.
7. In the management UI inspect runtime compatibility/health and run a harmless
   isolated self-test. Confirm no direct app egress or host port is possible.
8. Configure trusted publishers and applicable outbound/private-network policy.
   Catalogue selection alone never grants executable-code trust.
9. Create and restore an isolated fixture recovery set, including required keys.
   Configure an approved off-host backup destination separately.
10. Enable managed installation after verification. From then on, app admins use
    Portal to install/configure/update/stop applications without shell access.

Reference deployment files must include exact supported profiles, backup paths,
ownership, health checks, safe log limits and how to undo bootstrap. If the runner
needs a privilege the profile does not provide, fail with a clear reason rather
than suggesting broad permissions as a routine fix.

## 5. Everyday administrator procedures

### Install

Open Market, select a compatible app, inspect publisher/permissions/resource/data
requirements, choose optional grants and app-use OUs, then confirm. Follow named
progress stages. Once installed, open the first-use wizard only if business
configuration is required. The wizard never requests an internal backend address,
container command or signing key. If a runtime is absent, explain the one-time
infrastructure requirement before review, with an administrator guide link.

### Update

Review the exact target and changed capabilities/schema/resource effects. Confirm
the recovery point and app-only maintenance impact. Observe the same operation
after page reload. Do not uninstall an app as a normal update technique. If
upgrade fails, use a platform-offered verified recovery path rather than changing
container tags manually or starting an older binary against unknown data.

### Disable and enable

Disable blocks access and new work immediately at the broker and reports runner
shutdown as pending until acknowledged. Enable rechecks the same trusted release
and health, preserving approved settings. Neither action means uninstall.

### Uninstall and data retention

Review stopped work, revoked integrations and the retained-data choice. Default
to retain. After completion the application has no authority or background work.
Find retained data in management; it is not an installed app icon. Reinstall with
data only after explicit compatibility/principal review. Irreversible deletion
has a separate confirmation and clearly states backup-copy coverage.

### Restore

Select a verified compatible recovery set; show possible loss of writes after its
checkpoint. Restore in app maintenance with fenced workers. Review credentials,
grants, destinations, subscriptions and uncertain external deliveries. Resume
individual capabilities deliberately. Restore never sends a notification merely
because a prior queue record exists.

## 6. Troubleshooting playbook

| Symptom | Inspect first | Safe response |
| --- | --- | --- |
| Managed install unavailable | Runtime health/compatibility and preflight findings | Fix the one-time runtime setup; do not install half the package |
| Stuck install/update | Operation ID, stage and observed runner result | Reconcile/retry same operation, never start duplicate commands |
| App page blank | Platform handshake error, safe console diagnostics, asset integrity | Retry document negotiation after cause is fixed; never relax sandbox |
| Wrong language/theme | Initial presentation and SDK protocol compatibility | Correct shared contract; do not add a second global preference |
| Backend crash loop | Safe readiness/log/resource evidence | Circuit-break and diagnose; keep data, do not grant privilege |
| Network/secret denied | Effective binding/capability revision and fixed destination | Review exact intended grant; do not widen to all networks/secrets |
| Background work stopped | Pause state, lease, handler result and trust revision | Correct policy/config then deliberately resume |
| Old app missing after uninstall | Retained-data record and approved package compatibility | Use supported reinstall/adoption, not manual DB asset edits |
| Restore cannot decrypt | Recovery set/key association | Refuse activation; recover matching protected keys or report key loss |
| Portal restored but runner differs | Controller epoch and checkpoint mismatch | Fence old authority and reconcile, never blindly accept runner state |

Logs expose operation IDs, stages, classified errors and resource statistics.
Sensitive upstream responses, DSNs, bearer links, provider credentials and user
payloads are excluded. A support bundle requires an explicit preview of included
files and destination; diagnostics are not automatically sent to a publisher.

## 7. Release and channel policy

Application versions and artifacts are immutable. The app market may offer a
stable/beta channel selector, but installation resolves one reviewed descriptor
digest and update never silently follows a moving tag. Default first-release
updates are administrator initiated; automatic updates require a later explicit
permission/maintenance/data compatibility policy.

Portal server deployments retain their existing `beta` channel configuration.
Do not rewrite server Compose image references to fixed versions/digests as part
of this project. Record the resolved digest/version for verification and recovery
without changing that configured channel. Runner compatibility and its channel
policy must be explicit; do not assume Portal's rolling tag can be reused for app
packages or a differently versioned runner.

The existing Portal release workflow remains authoritative for Portal releases.
Use exact-main CI, required bilingual reader notes and guarded artifact/channel
publication. A documentation PR must not dispatch a release or change a server.

## 8. Handoff template for every implemented work package

```text
Work package and acceptance case IDs:
Accepted decision/schema revision:
Source PRs and exact commits:
Contracts added or changed:
Tests run, fixture and results:
Failure/recovery evidence:
Known unsupported behavior:
Public artifact identity, if released:
Production changes, if separately authorized:
Next eligible work package:
```

Fill with actual evidence. Keep credentials and protected backup paths/material
out of public attachments when disclosure could enable access. Do not write
"complete" while required source, released-byte or runtime evidence is missing.
