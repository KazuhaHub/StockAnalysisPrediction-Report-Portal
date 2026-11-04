# Business configuration setup

Status: development candidate. This record does not claim production approval,
external provider credentials, or a neutral-operator walkthrough.

## Product

Scheduled Digest is the first reference application with a required business
configuration. Its signed descriptor now requires `network.http`, and the
installation-scoped binding named `digest-feed` is the setup boundary. The
binding uses the existing reviewed credential and reviewed-network custody path;
no new Portal schema or global configuration table was added.

After initial activation, a Scheduled Digest installation whose binding is not
active is persisted as `setup_required`. It is not openable and its backend is
not launchable. The settings projection reports both the phase gap and the exact
missing binding. The administrator can open the existing credential page, create
the credential, and approve the `digest-feed` binding. Approval atomically
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
credential rotation back to `setup_required`, reapproval, binding revocation, and
refusal to persist an unconfigured task. Portal web tests cover the directory
state, settings gap, exact `digest-feed` guidance, credential-page prefill, and
the administrator link. The Linux exact-image path is being extended to require
the same setup before executing the real digest task.

The remaining acceptance evidence is a fresh hosted run with the updated
descriptor and browser coverage for the setup-required user state and the
administrator setup transition.
