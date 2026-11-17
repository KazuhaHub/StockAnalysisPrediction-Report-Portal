# Business configuration setup

Status: development candidate. This record does not claim production approval,
external provider credentials, or a neutral-operator walkthrough.

## Product

Scheduled Digest is the first reference application with a required business
configuration. The installation-scoped binding named `digest-feed` is the setup
boundary. A setup-only (`raw_read`) binding completes the gate without granting
network use; when optional `network.http` is later approved, the same binding
name can be reapproved as a reviewed HTTP binding. The binding uses the existing
reviewed credential and reviewed-network custody path; no new Portal schema or
global configuration table was added.

After initial activation, a Scheduled Digest installation whose binding is not
active is persisted as `setup_required`. It is not openable and its backend is
not launchable. The settings projection reports both the phase gap and the exact
missing binding. The administrator can open the existing credential page, create
the credential, and approve the `digest-feed` setup binding. Approval atomically
advances the same installation to `ready`; it does not grant a new capability or
change the install audience.

Removing the binding, revoking its credential, or rotating it into
`review_required` returns the installation to `setup_required`. Reapproval
through the normal review path restores `ready`. Uninstall continues to revoke
bindings and clear the selected installation's dedicated plugin data.

## Jobs

Task admission checks the current installation phase and required setup binding
before persisting work. Worker claim, service-task preparation, and completion
continue to recheck current generation, policy, release, epoch, grants, and
setup state. A `setup_required` or binding-incomplete installation cannot admit
an unconfigured digest job.

## User states

The installed-app directory distinguishes `pending` from `setup_required`.
Ordinary users see a localized setup-required tag and an explanation that an
administrator must finish setup. They cannot open the app or receive a backend
session while that state is effective.

## Verification

Focused SQLite tests cover binding-driven `setup_required`, approval to `ready`,
credential rotation back to `setup_required`, reapproval, binding revocation,
setup-only binding without network authority, and refusal to persist an
unconfigured task. Portal web tests cover the directory state, settings gap,
exact `digest-feed` guidance, credential-page prefill, and the administrator
link.

The exact native run `41918062142` at Portal `dc2e7e8e` and Apps `fa4acc03`
starts the Scheduled Digest image, observes `setup_required`, approves the
setup-only binding, then completes registration, boot, the fixed task,
uninstall and fresh reinstall. Its artifact reports
`native_scheduled_digest_setup_required_and_configured=true` and image
`localhost:32768/scheduled-digest-exact@sha256:bbc3da22...`.

The full native matrix `41915897527` and quota/browser run `41920808163`
also pass. Chromium proves an ordinary user sees the localized setup-required
state, cannot open the application, and can open it after the fixture advances
to a configured projection. Evidence is in
[business setup](handoff/evidence/platform-business-setup/acceptance.json) and
[reference apps](handoff/evidence/platform-reference-apps/acceptance.json).
Production setup, real external credentials and the human screen-reader audit
remain outside this development acceptance.
