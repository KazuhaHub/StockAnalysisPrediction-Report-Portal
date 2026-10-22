# Status and generic extension storage proposal

Status: awaiting owner approval. No DDL or migration is implemented by this file.
Prepared 2026-10-22 for the accepted shared application repository.

## Decision requested

Approve Option A below: a single new generic Portal connection table using the
next available migration after notification 0007, plus the independent Status
SQLite tables listed here. Use DB-backed feature settings, purpose-bound sealed
secrets, fail-closed identity checks, transactional transitions/outbox and paused
restore. Do not change the frozen Portal baseline or Portal business pages.

Migration numbers are allocated immediately before implementation from the actual
branch. No forecast plan's reserved migration/ADR number is reused silently.

## A. Portal: generic backend connection

One table, `app_backend_connections`, not a Status-specific table. One row per
installed app. Explicit deletion is owned by the same transaction as uninstall;
reinstall invalidates prior app tokens and disables/reviews an existing connection
when scopes/version change. App-token identity stays ephemeral in memory.

| Column | Type and constraint | Purpose |
| --- | --- | --- |
| app_id | TEXT PRIMARY KEY | Installed app identity |
| instance_id | TEXT NOT NULL | Random registration generation; reinstall/restore rotates it |
| label | TEXT NOT NULL | Administrator-facing label |
| base_url | TEXT NOT NULL | Fixed trusted backend root, HTTPS except explicit private-network policy |
| audience | TEXT NOT NULL | Backend assertion audience |
| secret | TEXT NOT NULL | Sealed JSON key ID/shared signing key and independent stable identity seed; never returned |
| policy | TEXT NOT NULL | Versioned JSON exact path-prefix/method rules and explicit network allowlists; bounded validated object |
| enabled | INTEGER NOT NULL DEFAULT 0 | Review gate |
| revision | BIGINT NOT NULL DEFAULT 1 | Compare-and-swap and request fencing |
| created_at | BIGINT NOT NULL | UTC Unix milliseconds |
| updated_at | BIGINT NOT NULL | UTC Unix milliseconds |

No per-account identity table is added. Subject derives from Portal issuer,
username, account creation identity and the existing notification-account deletion
generation; every proxy call compares app-token account/session identity and
reads current role/OU ancestry from the store. Assertions never authorize from
client facts. OU references carry a creation identity as well as numeric ID so an
old grant cannot match a deleted/recreated group. Unknown ancestry fails closed.

A connection save validates the installed app, schema policy, secret/keyring and
bounded host/port/CIDR allowances before committing; it requires the loaded
revision. No network calls run in the transaction. Request dispatch verifies
current enabled registration revision before sending, uses a bounded transport,
strips client authorization/cookies and accepts no client host override.

Use the existing keyring purpose `app-backend:<app_id>:<instance_id>`. A signing-key rotation
changes revision, key ID and signing secret together; it preserves the independent
identity seed so current account/OU grants do not break on ordinary key rotation. The backend can accept a short,
explicitly configured old/new verification window; it does not accept arbitrary
key IDs. No secret survives into a response, audit detail or ZIP.

Backup enumeration includes the table. Restore disables every connection and
rotates registration instance IDs; old assertions and app tokens are invalidated.
An approved older dump without the table restores as no registrations. Restore
cannot resume private traffic to a possibly stale destination silently. Uninstall
removes the registration and secret; already sent network calls cannot be undone.
Both SQLite and PostgreSQL get create/verify/restore/delete fixtures.

## B. Independent Status SQLite database

The Status service has its own database, migration ledger and encryption key.
It never reads or migrates Portal data. All instants below are Unix milliseconds;
wire DTOs render UTC RFC3339. IDs are opaque TEXT except autoincrement sequence
numbers. Booleans use INTEGER checks. JSON columns are bounded versioned objects,
not arbitrary executable probe scripts. All references are explicit foreign keys
with SQLite `foreign_keys=ON`; deletion semantics are listed below.

| Table | Columns and constraints |
| --- | --- |
| schema_migrations | version INTEGER PRIMARY KEY, applied_at BIGINT NOT NULL |
| settings | key TEXT PRIMARY KEY, value TEXT NOT NULL; only registered service feature settings |
| credentials | id TEXT PRIMARY KEY, kind TEXT NOT NULL, label TEXT NOT NULL, sealed_value TEXT NOT NULL, revision BIGINT NOT NULL, created_at/updated_at BIGINT NOT NULL |
| components | id TEXT PRIMARY KEY, label TEXT NOT NULL, public_label TEXT NOT NULL DEFAULT '', public_enabled INTEGER NOT NULL DEFAULT 0, enabled INTEGER NOT NULL DEFAULT 0, revision BIGINT NOT NULL, created_at/updated_at BIGINT NOT NULL |
| component_grants | component_id TEXT REFERENCES components ON DELETE CASCADE, principal TEXT NOT NULL, permission TEXT NOT NULL (`read` or `finance`), PRIMARY KEY(component_id,principal,permission) |
| monitors | id TEXT PRIMARY KEY, component_id TEXT NOT NULL REFERENCES components ON DELETE RESTRICT, kind TEXT NOT NULL, label TEXT NOT NULL, credential_id TEXT NULL REFERENCES credentials ON DELETE RESTRICT, config TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 0, interval_seconds INTEGER NOT NULL, timeout_seconds INTEGER NOT NULL, revision BIGINT NOT NULL, next_at BIGINT NOT NULL, lease_token TEXT NOT NULL DEFAULT '', lease_until BIGINT NOT NULL DEFAULT 0, created_at/updated_at BIGINT NOT NULL |
| observations | id TEXT PRIMARY KEY, monitor_id TEXT NOT NULL REFERENCES monitors ON DELETE RESTRICT, monitor_revision BIGINT NOT NULL, outcome TEXT NOT NULL, reason_code TEXT NOT NULL, measurements TEXT NOT NULL, observed_at BIGINT NOT NULL, duration_ms BIGINT NOT NULL; no raw upstream error/credential content |
| monitor_states | monitor_id TEXT PRIMARY KEY REFERENCES monitors ON DELETE RESTRICT, revision BIGINT NOT NULL, current_state TEXT NOT NULL, success_streak/failure_streak INTEGER NOT NULL, last_observation_id TEXT NULL REFERENCES observations ON DELETE SET NULL, last_success_at BIGINT NOT NULL DEFAULT 0, updated_at BIGINT NOT NULL |
| incidents | id TEXT PRIMARY KEY, component_id TEXT NOT NULL REFERENCES components ON DELETE RESTRICT, state TEXT NOT NULL, safe_summary TEXT NOT NULL, private_detail TEXT NOT NULL, public_summary TEXT NOT NULL DEFAULT '', public_enabled INTEGER NOT NULL DEFAULT 0, opened_at BIGINT NOT NULL, resolved_at BIGINT NOT NULL DEFAULT 0, revision BIGINT NOT NULL |
| incident_transitions | id TEXT PRIMARY KEY, incident_id TEXT NOT NULL REFERENCES incidents ON DELETE CASCADE, sequence INTEGER NOT NULL, kind TEXT NOT NULL, safe_message TEXT NOT NULL, occurred_at BIGINT NOT NULL, UNIQUE(incident_id,sequence) |
| maintenance_windows | id TEXT PRIMARY KEY, component_id TEXT NOT NULL REFERENCES components ON DELETE CASCADE, starts_at/ends_at BIGINT NOT NULL, safe_summary TEXT NOT NULL, created_by TEXT NOT NULL, created_at BIGINT NOT NULL; ends_at > starts_at |
| hourly_aggregates | monitor_id TEXT REFERENCES monitors ON DELETE CASCADE, hour_at BIGINT NOT NULL, operational_ms/outage_ms/degraded_ms/unknown_ms/excluded_ms BIGINT NOT NULL, PRIMARY KEY(monitor_id,hour_at) |
| notification_publishers | component_id TEXT PRIMARY KEY REFERENCES components ON DELETE RESTRICT, publisher_name TEXT NOT NULL UNIQUE, credential_id TEXT NOT NULL REFERENCES credentials ON DELETE RESTRICT, enabled INTEGER NOT NULL DEFAULT 0, revision BIGINT NOT NULL, updated_at BIGINT NOT NULL |
| notification_outbox | id TEXT PRIMARY KEY, transition_id TEXT NOT NULL UNIQUE REFERENCES incident_transitions ON DELETE RESTRICT, component_id TEXT NOT NULL REFERENCES components ON DELETE RESTRICT, occurrence_id TEXT NOT NULL UNIQUE, payload TEXT NOT NULL, publisher_revision BIGINT NOT NULL, state TEXT NOT NULL, attempts INTEGER NOT NULL DEFAULT 0, next_at BIGINT NOT NULL, lease_token TEXT NOT NULL DEFAULT '', lease_until BIGINT NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '', portal_event_id TEXT NOT NULL DEFAULT '', created_at/updated_at BIGINT NOT NULL |
| audit_events | id TEXT PRIMARY KEY, actor TEXT NOT NULL, action TEXT NOT NULL, target_id TEXT NOT NULL, safe_detail TEXT NOT NULL, created_at BIGINT NOT NULL |

Indices: monitors(enabled,next_at,lease_until); observations(monitor_id,observed_at,id);
incidents(component_id,opened_at,id); incident_transitions(incident_id,sequence);
maintenance_windows(component_id,starts_at,ends_at); notification_outbox(state,next_at,lease_until);
audit_events(created_at,id). Verify all declared products at startup; newer/partial
schema is refused. A migration is one transaction, marker written last.

Principals use `user:<opaque-account-identity>` or
`ou:<issuer-bound-ou-id-and-generation>`, not display names. Missing identity denies
private reads. Finance is a distinct grant; public visibility never grants finance.

## C. Transactions and lifecycle

- Create/edit a component, its monitor settings and grants atomically; revision
  checks reject concurrent overwrite. A saved credential never appears in a read.
- A claim marks one due monitor leased; no network inside SQL. Persist observation,
  fenced state reduction, incident/transition and outbox intent atomically after
  the check. A target/credential revision change discards a stale result.
- A component may have multiple monitors. Its incident state uses an explicit
  reducer across current fresh monitor states; unknown is never healthy. A single
  billing component can back several keys without duplicate balance pools.
- A transition's serialized notification payload/occurrence is immutable. Claim,
  bounded publish, acknowledge/retry are fenced by lease and captured publisher
  revision. Changes to destination require review, not retargeting pending text.
- Use an exclusive process lock on the database volume to enforce one replica.
  After restart, expired checks become unknown; due work is bounded/jittered.
- Delete means disable/archive while active incidents, retained observations or
  pending publication exist. Purge is a deliberate transaction after retention,
  never a hidden cascade erasing unresolved evidence. Credential deletion refuses
  while referenced. Deleting an OU/account in Portal revokes its current access;
  stale Status grants become inert and can be cleaned by an administrator.
- Backup uses a consistent SQLite snapshot with format/version and key fingerprint.
  Restore is offline, validated into a temporary DB then atomically installed;
  rollback preserves the previous database. After restore, probes and outbox are
  paused, leases invalidated and started publications marked `review_required`.
  Resume requires a reviewed queue fingerprint. A previous image needs a backup
  compatible with that image's schema; no destructive down migration.
- Defaults: raw observations 30 days, aggregate/closed incidents/audit 90 days.
  Bounded cleanup preserves active incidents, unresolved/retrying/review-required
  outbox and referenced transitions. Outbox acknowledgement tombstones retain
  occurrence identity until their incident family is eligible for cleanup; a
  restored old snapshot remains paused to avoid duplicate publication.
- Public projection uses its own explicit DTO/query and published narrative;
  amounts, credentials, private targets and private diagnostic text never enter it.

## Alternatives and recommendation

Option A (recommended) keeps Portal generic and Status independent, with explicit
records and tested migrations. Option B stores the connection/Status objects in
Portal `meta` or its notification tables; rejected because it couples lifecycle,
weakens relational constraints and bypasses the storage approval boundary. Option
C puts monitoring in the Portal binary; rejected by the agreed plugin isolation.

Approve A as described, or specify structural changes before any schema code is
written. Minor implementation helpers do not expand the approved object set.
