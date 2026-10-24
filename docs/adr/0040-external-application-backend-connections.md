# ADR 0040: Viewer-bound external application backend connections

Status: accepted 2026-10-22. The owner explicitly approved Option A in
[the storage proposal](../status-storage-and-platform-proposal.md).

## Decision

Keep application backend code and its database outside the Portal process. The
Portal supplies a generic, administrator-paired connection and a bounded proxy
for installed sandboxed apps. It does not execute probes or alter business pages.

Migration 0008 adds one `app_backend_connections` table. The frozen baseline stays
unchanged. All columns are declared migration products; backups include the table.
An older backup without the migration restores with no registrations. A backup
claiming the step must contain its products. SQLite and PostgreSQL are supported.

Connections seal their signing key and independent identity seed with the existing
keyring. Ordinary signing-key rotation preserves opaque account/OU identities.
Reinstall and restore clear the pairing, disable the connection, increment its
revision and rotate its instance. This intentionally invalidates old identities;
operators must pair the new registration and review its grants. Clearing ciphertext
before rotation avoids keeping an envelope bound to an obsolete encryption purpose.
Uninstall deletes the registration in the app/file removal transaction.

Only an ephemeral token bound to the same app and current account can proxy.
Every dispatch reads current account/session/deletion identity, role and verified
OU ancestry. It repeats authority after the authenticated backend boot handshake.
Separate extension.read and extension.manage capabilities cannot be replaced with
query, ingest, notify or a machine token's all scope. Management requires the
current administrator role. The administrator directory exposes opaque grant
references plus labels and is itself app-bound and paginated.

Requests carry the shared protocol's short-lived method/path/body-bound assertion.
The shared client validates all DNS answers, pins dial addresses, forbids reserved
and metadata addresses, constrains private networks/ports, verifies TLS, refuses
redirects and bounds time, headers, bodies and concurrency. No incoming cookie,
authorization header, destination override or identity claim reaches the backend.
Sixteen proxy requests and sixty-four cached transports are independent of business
queues. Backend error bodies never reach the iframe.

## Consequences

An application can fail without running inside the Portal. A transport failure
returns a safe JSON error; it cannot block a business queue. Pairing is an explicit
operator task. Restoring a backup never resumes traffic to a previously trusted
backend without review. Status owns its separate approved SQLite schema and does
not gain direct access to Portal storage or native page internals.

The service implementation, provider probes, Status frontend, public gateway,
notification outbox and release migration remain separate acceptance packages.
The platform capability alone is not a delivered Status application.
