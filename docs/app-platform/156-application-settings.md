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

A managed installation in `enabled/recovery_required` state exposes its bounded
verified recovery points. An administrator can review restore from one exact
recovery set; the review binds the checkpoint operation, checkpoint ID, release,
manifest, data format, snapshot size and generation transition. Restore remains
disabled while global application control is paused.

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
- up to 16 recovery points with exact checkpoint, release, format, size and
  verification time, plus server-derived restore eligibility;
- whether controls are paused and why restore admission is unavailable;
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

Restore is offered per verified recovery point only for
`enabled/recovery_required`, with no pending operation, an active data namespace,
an enabled managed runtime and an unpaused controller. The destructive review
states that writes after the checkpoint may be lost and that the app remains
unavailable until the current writer stops, data is restored, health checks pass
and a new generation activates. Plan and confirmation revalidate the entire
recovery tuple and current grants/audience. Read-only settings remain available
while control is paused so the administrator can inspect the reason and recovery
boundary; all restore admission is refused.

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
- reviewed restore at 1440 px English/light and 320 px Simplified
  Chinese/light, including the exact recovery point and data-loss boundary;
- paused-control settings at 1440 px English/light and 320 px Simplified
  Chinese/light, with the pause reason and disabled restore action;
- settings and every deletion, repair, restore and paused-control review at
  browser-equivalent 200% zoom on a 1440x900 physical display;
- the existing install, update, enable, disable, uninstall and queued, paused,
  recovery-required lifecycle scenarios.

All settings views have no page overflow, clipped tags or serious/critical axe
violations. The 320 px repair and restore dialogs have no clipping, focus starts
inside the dialog and the footer remains reachable. At 200% zoom the scrollable
review body remains keyboard focusable, the dialog stays inside the viewport and
both footer actions remain reachable. Ordinary review buttons use the
high-contrast text token, including the CI-discovered Cancel contrast case. The
machine report binds code `ba0f4667`;
screenshots and
`acceptance.json` are in
[platform install evidence](handoff/evidence/platform-install).

Full local Web verification passed 270 files / 2844 tests, typecheck, lint and
the production build. Focused settings tests pass against SQLite, with the same
case included in the PostgreSQL `TestPlatform*Postgres` group. Full
`internal/app` verification passed in 271.645s. Browser evidence binds
`ba0f4667`. Candidate `ba0f4667` passed general test run `41493408915`, runner
daemon run `41493408984`, quota/browser run `41493409034` and CodeQL
`41493401071`. Two earlier attempts of the same browser step failed on the
actual Cancel-button contrast assertion; footer-scoped high-contrast styling
fixed the cause. Those failed runs remain recorded in PR #364.

The native Linux quota/browser run now also drives repair and restore
confirmation through the production SPA and normal management HTTP API. In each
case the browser confirms the exact review, the admitted operation runs through
the signed runner path, and a later browser session verifies `Operation
complete` on the durable progress page plus `Ready` on the settings projection.
The machine evidence binds candidate `ce3e07bd` and quota/browser run
`41509323922`; general test `41509323957`, runner daemon `41509323798` and
CodeQL `41509313480` also pass. Screenshots and operation IDs are in
[lifecycle runner evidence](handoff/evidence/platform-lifecycle-runner/acceptance.json).
Earlier controller isolation failures in this browser step remain recorded and
were fixed by staging the script, HOME and evidence inside the controller-owned
child directory.

## Remaining scope

Editing audiences, grants or credentials continues through their exact reviewed
flows. The full D5 review still needs manual screen-reader testing, retained-data
reinstall and the remaining lifecycle pages.
