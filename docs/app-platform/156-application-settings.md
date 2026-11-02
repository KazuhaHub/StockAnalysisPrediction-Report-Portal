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

Completed operation polling now refreshes the release directory once when it
first observes a terminal result. This removes the stale installable row without
starting a refresh loop.

## Evidence

The repeatable Chromium matrix now captures:

- settings at 1440 px English/light;
- settings at 320 px Simplified Chinese/dark;
- a reviewed retained-data deletion with the exact namespace and recovery scope;
- the existing install, update, enable, disable, uninstall and queued, paused,
  recovery-required lifecycle scenarios.

All settings views have no page overflow, clipped tags or serious/critical axe
violations. The machine report binds code `8d882a2a`; screenshots and
`acceptance.json` are in
[platform install evidence](handoff/evidence/platform-install).

Full local Web verification passed 270 files / 2841 tests, typecheck, lint and
the production build. Focused settings tests pass against SQLite, with the same
case included in the PostgreSQL `TestPlatform*Postgres` group. Full
`internal/app` verification passed in 164.353s. Browser evidence binds
`8d882a2a`. Candidate `8d882a2a` passed quota/browser run `41445281563`,
general test run `41445281473` and runner daemon run `41445281626`.

## Remaining scope

Editing audiences, grants or credentials continues through their exact reviewed
flows. The full D5 review still needs manual screen-reader testing, repair and
restore actions, retained-data reinstall, and the remaining lifecycle pages.
