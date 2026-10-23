# Purge ownership evidence: explicit upgrade

Status: development verification only; no production upgrade or reader rollout is recorded here.
Step `0007` adds the owner-approved `prediction_purge_owners` table. The frozen v0.4.72 baseline and
released steps `0001`–`0006` are unchanged. All supported baseline databases have an explicit path;
pre-baseline databases must first pass through the documented v0.4.72 bridge.

## Before upgrading

1. Obtain deployment approval separately. Stop every portal instance and other database writer;
   this command does not enforce that all older processes have stopped.
2. With the previous release, take a full `report-portal backup pre-purge-owners.jsonl`. Verify it
   using that release's restore dry run on a disposable target, and preserve a secure copy of the
   infrastructure config and its secret keys. Backups contain sensitive data. Keep the previous
   binary/image and the pre-upgrade backup for rollback.
3. Point the new binary at the same infrastructure configuration using `RP_CONFIG`. Existing
   `RP_DB_*` overrides still apply: verify the intended target before applying anything. Do not
   put credentials on the command line or into shell history.

## Inspect, apply, verify

```sh
report-portal schema-upgrade
report-portal schema-upgrade --apply
report-portal schema-upgrade
```

The first command reports pending steps and changes no schema, rows or config. SQLite inspection
opens the source read-only and cannot create a missing database. Only `--apply` permits mutation.
Unknown arguments, unknown migration ids, unsupported baselines and already-recorded damaged
products are refused. Applying uses the same initialization serialization and transactional,
idempotent migration runner as normal initialization, then verifies the resulting products.
Step `0007` creates an empty evidence table and records its ledger row after verification. Repeating
the explicit command verifies without rewriting the applied ledger timestamp. Older supported
baselines receive their missing earlier steps on this explicit path too.

A fresh database creates the complete schema normally. A partially initialized database with a
baseline marker but no `0007` record must use the explicit command; normal startup does not infer
permission to resume this schema change. Existing server startup refuses a missing explicit step
before running any earlier automatic migration. A recorded step with a missing table, nullable or
wrong-type key, wrong primary key or missing positive-owner check is refused rather than repaired.

Restart with the new release only after the second inspection has no pending steps. Take and test
a post-upgrade backup. The portable dump declares `0007` and carries its table and ownership rows;
a dump claiming the step but omitting its table/header section is refused atomically. A legitimate
older dump is still loadable into an upgraded/fresh target, leaving ownership evidence empty and
rebuilding the migration ledger inside the restore transaction. Restores are offline operations.

## Historical visibility and rollout

No upgrade reconstructs ownership for old global purge markers. They remain global history for
all readers but must be invisible to own readers unless actual evidence exists. Evidence capture,
OU-delete transfer and reader filtering are separate business-path work and must pass their full
access/concurrency tests before release; schema completion alone does not close that hold.

## Rollback

A binary that knows only `0001`–`0006` refuses the `0007` ledger. Do not delete the ledger row, drop
the evidence table or force an older binary onto the upgraded database. Restore the verified
pre-upgrade backup into a separate clean target with the previous release, verify it there, then
switch the stopped deployment to that target. Post-upgrade writes are not present in that backup;
account for them before choosing rollback. A forward fix is the alternative.
