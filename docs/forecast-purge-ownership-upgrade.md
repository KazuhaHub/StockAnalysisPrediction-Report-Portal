# Purge ownership evidence: explicit upgrade

Status: the business paths are wired and covered by dual-driver tests (retention records the evidence,
OU deletion carries it, own-scoped reads use it). No production upgrade or reader rollout is recorded
here. The own-history release hold stays until this upgrade has run on the target databases and the
independent review of the wiring is recorded.

Step `0009` adds the owner-approved `prediction_purge_owners` table. The frozen v0.4.72 baseline and the
automatic steps before it are unchanged. The explicit path applies any of those that are still missing
before `0009`: `0001` to `0006` on this branch, and also `0007` (notification) and `0008` (extension)
when those steps ship in the same release. They are all applied by the same `--apply`; the dry run lists
exactly what is pending, for example `[0007, 0008, 0009]`. All supported baseline databases have an
explicit path; pre-baseline databases must first pass through the documented v0.4.72 bridge.

## Before upgrading

1. Obtain deployment approval separately. Stop every portal instance and other database writer; the
   command does not enforce that all older processes have stopped.
   **A refused container must be stopped first.** A new image started against a database that has not
   been upgraded exits with the refusal below. Under a restart policy (`restart: unless-stopped`, as in
   the shipped compose file, or `always`) it is started again, refuses again, and keeps doing so, which
   is a restart loop that also keeps opening the database. Stop it (`docker compose stop report-portal`,
   or `docker update --restart=no <container>` followed by `docker stop`) before taking the backup and
   running the upgrade, and start it again only after the upgrade has been verified.
2. With the **previous** release, take a full backup. The new binary cannot do this: `backup`,
   `restore`, `adduser`, `security`, `recompute-kinds` and `freeze-names` all open the database the way
   server startup does, so each of them refuses a database that has not been upgraded. Keep the
   previous image or binary for this, and for rollback. Verify the backup with that release's restore
   dry run on a disposable target, and preserve a secure copy of the infrastructure config and its
   secret keys. Backups contain sensitive data.
3. Point the new binary at the same infrastructure configuration using `RP_CONFIG`. Existing `RP_DB_*`
   overrides still apply: the command prints no target, so verify that it receives exactly the
   environment the portal runs with, or it inspects a different database. Do not put credentials on
   the command line or into shell history; use an environment file.

## Running the command in a container

The image's entrypoint (`docker-entrypoint.sh`) ignores its arguments: it always ends by starting the
server. `docker run <image> schema-upgrade` would therefore start the portal, not the upgrade. Bypass
the entrypoint and give the one-off container the same volumes and environment as the service:

```sh
docker run --rm --user 10001:10001 --entrypoint /app/report-portal \
  -v "$PWD/config:/app/config" -v rp-data:/app/data \
  --env-file portal.env --network <the compose network> \
  ghcr.io/kazuhahub/stockanalysisprediction-report-portal:<tag> \
  schema-upgrade [--apply]
```

- `--entrypoint /app/report-portal` is what makes the trailing arguments reach the binary.
- `RP_CONFIG` is already `/app/config/config.yaml` in the image, so mounting the service's config
  directory at `/app/config` is enough. Supply the service's `RP_DB_DRIVER` and `RP_DB_DSN` (and any
  other `RP_DB_*` override) from the environment file, and join the network the database is on.
- **For SQLite, run as the user that owns the data**, `--user 10001:10001` unless the deployment sets
  `PUID` and `PGID`. The entrypoint is what normally drops privileges and fixes ownership; bypassing it
  would otherwise leave a root process creating root-owned journal files next to a database the
  service later opens as an unprivileged user. Postgres needs no particular user but gains nothing from
  root either.
- With compose, `docker compose run --rm --no-deps --user 10001:10001 --entrypoint /app/report-portal
  report-portal schema-upgrade` reuses the service's volumes and environment. The service's `image:`
  decides which release runs, so pin it to the intended tag first (the shipped file uses `:latest` with
  `pull_policy: always`). Drop `--no-deps` if the database service is not already running.
- The same invocation with the previous image and `backup -` (redirected to a file, then compressed)
  takes the pre-upgrade backup from step 2 once the portal is stopped.

On a host install, run `report-portal schema-upgrade` as the account that owns the database with the
service's configuration and environment.

## Inspect, apply, verify

```sh
report-portal schema-upgrade
report-portal schema-upgrade --apply
report-portal schema-upgrade
```

The first command reports pending steps and changes no schema, rows or config. SQLite inspection opens
the source read-only and cannot create a missing database. Only `--apply` permits mutation. Unknown
arguments, unknown migration ids, unsupported baselines and already-recorded damaged products are
refused. Applying uses the same initialization serialization and transactional, idempotent migration
runner as normal initialization, then verifies the resulting products. Step `0009` creates an empty
evidence table and records its ledger row after verification. Repeating the explicit command verifies
without rewriting the applied ledger timestamp. Older supported baselines receive their missing earlier
steps on this explicit path too.

A fresh database creates the complete schema normally. A partially initialized database with a baseline
marker but no `0009` record must use the explicit command; normal startup does not infer permission to
resume this schema change. Server startup refuses a missing explicit step before running any earlier
automatic migration, writes no DDL, and names this document in the message:

```
migration 0009 requires an explicit offline upgrade: stop the portal, take a pre-upgrade backup with the
previous release, then run report-portal schema-upgrade --apply (procedure:
docs/forecast-purge-ownership-upgrade.md); server startup does not alter this schema
```

A recorded step with a missing table, nullable or wrong-type key, wrong primary key or missing
positive-owner check is refused rather than repaired.

Restart with the new release only after the second inspection has no pending steps. Take and test a
post-upgrade backup. The portable dump declares `0009` and carries its table and ownership rows; a dump
claiming the step but omitting its table/header section is refused atomically. A legitimate older dump is
still loadable into an upgraded or fresh target, leaving ownership evidence empty and rebuilding the
migration ledger inside the restore transaction. Restores are offline operations.

## Behaviour after the upgrade

- The table starts empty and is never back-filled. A purge marker written before the upgrade is global
  history: every `all` reader and the administrator views keep seeing it, and no own reader is ever told
  about it, because nothing proves which OU's sets it removed.
- Every retention pass after the upgrade snapshots the distinct owning OUs of each month before deleting
  its sets, in the same transaction as the deletion and the marker. A month with nothing attributed to an
  existing OU records no owner and stays invisible to own readers.
- Deleting an OU moves its evidence to the same destination as its sets (the parent, or the Default OU
  for a root), absorbs duplicates, and drops evidence that has no destination, all in the OU-delete
  transaction. No evidence row names an OU that no longer exists.
- Retention and OU deletion serialize on one database-wide lock taken as the first statement of each
  transaction (a Postgres transaction-scoped advisory lock; on SQLite the writer reservation). Both are
  short; the only visible effect of contention is that one waits for the other to commit.
- Own-scoped reads join the evidence to the reader's current subtree. If the table cannot be read, an
  own read answers `503 forecast_purge_history_unavailable` and never falls back to the global history.

## Rollout hold

Schema completion and wiring do not release the feature. Own-scoped reads stay held until this upgrade
has run on the target databases, a post-upgrade backup has been verified, and the independent review of
the wiring is recorded in the implementation log.

## Rollback

A binary that does not know step `0009` refuses its ledger row. Do not delete the ledger row, drop the
evidence table or force an older binary onto the upgraded database. Restore the verified pre-upgrade
backup into a separate clean target with the previous release (a container started against an empty
database does not need the upgrade command), verify it there, then switch the stopped deployment to
that target. Post-upgrade writes are not present in that backup; account for them before choosing
rollback. A forward fix is the alternative.
