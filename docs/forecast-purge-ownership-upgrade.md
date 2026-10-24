# Purge ownership evidence: upgrade notes

Status: the business paths are wired and covered by dual-driver tests (retention records the evidence,
OU deletion carries it, own-scoped reads use it). The own-history release hold stays until a release
carrying the step has been deployed to the target databases and the independent review of the wiring is
recorded.

Step `0009` adds the owner-approved `prediction_purge_owners` table. It is an ordinary migration step
([ADR 0035](adr/0035-migrations-after-a-frozen-baseline.md)): **a normal deploy applies it.** Pull the new
image and restart. At startup the step runs in its own transaction, creates an empty table, verifies its
exact shape, and only then records its ledger row. Nothing has to be run first, and a new image never refuses
to start because this step is missing. The frozen v0.4.72 baseline and every earlier step are unchanged.

**The list is in merge order, not id order.** The ids `0007` to `0009` were reserved by branches that were
still open when `0010` reached main, so they come after it. An id is a label; the order is the order the
steps reached main: `[0001 … 0006, 0010, 0009, 0007, 0008]`. A database that has recorded `0001` to `0006`
therefore receives `0010`, `0009`, `0007` (notification) and `0008` (extension), in that order. An id that
looks out of place in that list is not an error. The released prefix is fixed, and later steps append after
it. All supported baseline databases take this path; pre-baseline databases must first pass through the
documented v0.4.72 bridge.

## Before upgrading: back up with the previous release

The program takes no automatic backups; the maintainer takes their own, as before any release, with the
**previous** release. The new binary cannot stand in for it: `backup`, `restore`, `adduser`, `security`,
`recompute-kinds` and `freeze-names` all open the database the way server startup does, so each of them
applies the pending steps first, and a backup taken afterwards declares `0009` and cannot be restored by
the previous release. Verify the backup with that release's restore dry run on a disposable target, and
keep a secure copy of the infrastructure config and its secret keys. Backups contain sensitive data.

### Under a moving tag

The shipped compose file uses `:latest` with `pull_policy: always`. Under that policy `docker compose run`
and `docker compose up` pull first, so a `docker compose run ... backup -` would run the **new** release
against the old database and upgrade it before dumping it. Take the backup from the image the portal is
running now, by id, before anything pulls:

```sh
# 1. The previous release, by image id (it prints as sha256:<hex>). The container keeps pointing at
#    it after a later pull.
img=$(docker inspect report-portal --format '{{.Image}}'); echo "$img"

# 2. The service's RP_* settings (RP_DB_DRIVER, RP_DB_DSN and the like) in a file only you can read.
(umask 077; docker inspect report-portal --format '{{range .Config.Env}}{{println .}}{{end}}' \
  | grep '^RP_' > backup.env)

# 3. Stop the portal so nothing writes during the backup, then dump from the previous image,
#    reusing the stopped container's mounts. Add --network <compose network> for Postgres.
docker stop report-portal
docker run --rm --volumes-from report-portal --env-file backup.env \
  --user 10001:10001 --entrypoint /app/report-portal \
  "$img" backup - | gzip > pre-upgrade-$(date +%F).gz

# 4. The file holds database credentials. Remove it.
rm -f backup.env
```

- Keep the printed id: `img` lives only in the shell that ran step 1, and in another shell it must be set
  again from that value (`img=sha256:<hex>`, prefix included).
- `--entrypoint /app/report-portal` is what makes the trailing arguments reach the binary:
  `docker-entrypoint.sh` ignores them and always starts the server.
- `--volumes-from` takes the data volume and the config mount from the stopped container, so there is no
  project-prefixed volume name to get wrong (a literal `-v rp-data:/app/data` mounts a new empty volume,
  and on SQLite `backup` then dumps an empty database and exits 0).
- For SQLite use `--user 10001:10001`, or the deployment's `PUID:PGID`, so no root-owned files appear
  beside the database. Postgres needs the `db` service running and the compose network joined.
- `backup` prints `backup: <tables> tables, <rows> rows -> -` on stderr. **Check that the row count is the
  size of your deployment** before going on; a handful of rows means an empty database was dumped.

Then pull the new image and start it. If the tag is pinned, or the previous image is otherwise at hand,
the same `docker run` works with that image.

## Rollback

A binary that does not know step `0009` refuses its ledger row. Do not delete the ledger row, drop the
evidence table or force an older binary onto the upgraded database. Restore the verified pre-upgrade
backup into a separate clean target with the previous release (a container started against an empty
database needs nothing else), verify it there, then switch the stopped deployment to that target.
Post-upgrade writes are not in that backup; account for them before choosing rollback. A forward fix is
the alternative. This is the same story as for every other step, which is why `0009` needs no special
gate.

## Optional: inspect or apply offline

`report-portal schema-upgrade` is not part of a deploy. It is for an operator who wants to see what the
next start would apply, or to apply it while the portal is stopped:

```sh
report-portal schema-upgrade            # lists the pending steps; changes nothing
report-portal schema-upgrade --apply    # applies them with the normal runner, then verifies
```

Inspection reports pending steps and writes no schema, rows or config; a SQLite file is opened read-only
and a missing database or config is never created. `--apply` uses the same initialization serialization
and transactional, idempotent runner as startup; repeating it finds nothing pending and rewrites no
ledger row. Unknown arguments, unknown migration ids, unsupported baselines and already-recorded damaged
products are refused. In a container, bypass the entrypoint as above and give the one-off container the
service's mounts, `RP_*` environment and network.

A recorded step with a missing table, a nullable or wrong-type key, a wrong primary key or a missing
positive-owner check is refused at startup rather than repaired, and a lookalike table in front of an
unrecorded step is refused before its ledger row is written.

## Backups and restores

The portable dump declares `0009` and carries its table and ownership rows; a dump claiming the step but
omitting its table or header section is refused atomically. A legitimate older dump is still loadable
into an upgraded or fresh target, leaving ownership evidence empty and rebuilding the migration ledger
inside the restore transaction. Restores are offline operations. Take and test a backup after the first
start of the new release too.

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

Schema completion and wiring do not release the feature. Own-scoped reads stay held until a release
carrying this step has been deployed to the target databases, a post-deploy backup has been verified, and
the independent review of the wiring is recorded in the implementation log.
