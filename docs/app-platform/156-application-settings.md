# Application installation settings

The v2 administration surface now has a read-only settings page at
`/manage/apps/installations/:installation`. My Apps sends administrators with a
non-openable application directly to that installation instead of the generic
installation tab. Existing update, enable, disable and uninstall actions remain
on the reviewed lifecycle page; settings links there with the installation
selection preserved in the URL.

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
- data namespace state, schema, quota and observed usage when one exists;
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
Long audience tags wrap at 320 px. The current release digest, quota and usage
are bounded and copyable. The page reports explicit loading, missing-installation
and retry states.

Completed operation polling now refreshes the release directory once when it
first observes a terminal result. This removes the stale installable row without
starting a refresh loop.

## Evidence

The repeatable Chromium matrix now captures:

- settings at 1440 px English/light;
- settings at 320 px Simplified Chinese/dark;
- the existing install, update, enable, disable, uninstall and queued, paused,
  recovery-required lifecycle scenarios.

Both settings views have no page overflow, clipped tags or serious/critical axe
violations. The machine report binds code `f1a4550f`; screenshots and
`acceptance.json` are in
[platform install evidence](handoff/evidence/platform-install).

Full local Web verification passed 270 files / 2840 tests, typecheck, lint and
the production build. Focused settings tests pass against SQLite, with the same
case included in the PostgreSQL `TestPlatform*Postgres` group. Full
`internal/app` verification also passes. Candidate `f1a4550f` passed
quota/browser run `41437808872`, general test run `41437808855` and runner
daemon run `41437808888`.

## Remaining scope

This page is intentionally read-only. Editing audiences, grants or credentials
continues through their exact reviewed flows. The full D5 review still needs
manual screen-reader testing, retained-data reinstall/delete states, repair and
restore actions, and the remaining lifecycle pages.
