# Application installation settings

The v2 administration surface now has a settings page at
`/manage/apps/installations/:installation`. My Apps sends administrators with a
non-openable application directly to that installation instead of the generic
installation tab. Existing update, enable, disable and uninstall actions remain
on the reviewed lifecycle page; settings links there with the installation
selection preserved in the URL.

Most configuration is read-only. For a disabled or removed managed
installation whose retained data namespace still exists, settings can start the
existing reviewed `delete_data` flow. It does not delete data while the
application is active and does not bypass the exact namespace, runtime,
activation, receipt, quota or recovery-set checks in the backend.

A disabled or recovery/review-required managed installation can also request a
reviewed repair when it has a usable current release, active data namespace and
registered runtime. Repair re-observes the fixed runtime, rechecks the signed
release, grants, audience and compatibility, then creates the existing exact
repair plan. It retains the current data and policy tuple and cannot add
permissions or change the data schema.

## Projection

`GET /api/admin/app-platform/installations/{installation}/settings` returns the
current administrator projection for exactly one installation:

- installation, app and publisher identity;
- desired state, phase, generation, installation revision and policy revision;
- the active release, or the latest retired release for a disabled application;
- recorded audience users and organizational units;
- current capability grants, plus required capabilities from the signed
  descriptor;
- managed backend requirement and bounded runtime registration state;
- data namespace identity, state, schema, quota and observed usage when one exists;
- whether a recovery point exists and whether it needs review;
- bounded configuration gaps.

The response does not include the sealed identity seed, controller epoch,
runtime fingerprints, capability policy JSON, credential values or encrypted
credential material. Audience output is capped at 128 principals and capability
output at 256 entries. Configuration gaps are limited to missing required
capabilities, an unregistered or disabled managed backend, `setup_required`
and data that needs review.

Credential metadata remains on the existing installation secret-binding
endpoint. The page shows binding name, credential label, operation, state and
revision, but never the credential value. The credential management link keeps
using the existing app-scoped credential page.

## Presentation

The settings page uses the shared Portal theme and three localized languages.
Status is conveyed by words and icon through `StatusTag`, not colour alone.
Long audience tags wrap at 320 px. The current release digest, quota, usage and
namespace identity are bounded and copyable. The page reports explicit loading,
missing-installation and retry states.

Permanent deletion is offered only when a managed backend is registered and the
installation is disabled or removed. The confirmation names the exact data
namespace, generation transition, data format and deletion scope. A plan is
created before confirmation; commit reuses one idempotency key while the dialog
remains open, and successful admission navigates to the durable operation
progress page.

Repair is offered only from the projection's server-derived eligibility state:
the installation must be disabled or in recovery/review state, have no pending
operation, retain an active namespace and have an enabled runtime registration.
The confirmation names the current release, generation transition, policy
revision, retained data and unchanged grants. The plan and commit APIs repeat
all runtime, trust, authority, compatibility and state checks; the browser hint
is not authority.

Completed operation polling now refreshes the release directory once when it
first observes a terminal result. This removes the stale installable row without
starting a refresh loop.

## Evidence

The repeatable Chromium matrix now captures:

- settings at 1440 px English/light;
- settings at 320 px Simplified Chinese/dark;
- a reviewed retained-data deletion with the exact namespace and recovery scope;
- a reviewed managed repair at 1440 px English/light and 320 px Simplified
  Chinese/light, including retained data and unchanged grants;
- the existing install, update, enable, disable, uninstall and queued, paused,
  recovery-required lifecycle scenarios.

All settings views have no page overflow, clipped tags or serious/critical axe
violations. The 320 px repair dialog has no clipping, its focus starts inside the
dialog and the footer remains reachable. The machine report binds code
`49754cbb`; screenshots and
`acceptance.json` are in
[platform install evidence](handoff/evidence/platform-install).

Full local Web verification passed 270 files / 2842 tests, typecheck, lint and
the production build. Focused settings tests pass against SQLite, with the same
case included in the PostgreSQL `TestPlatform*Postgres` group. Full
`internal/app` verification passed in 444.062s. Browser evidence binds
`49754cbb`. Candidate `49754cbb` passed quota/browser run pending, general test
run pending and runner daemon run pending.

## Remaining scope

Editing audiences, grants or credentials continues through their exact reviewed
flows. The full D5 review still needs manual screen-reader testing, a repair
confirmation/progress browser path against the real runner, restore actions,
retained-data reinstall, enlarged-text coverage and the remaining lifecycle
pages.
