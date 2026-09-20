# ADR 0035 — Migrations after a frozen baseline, and restoring across them

**Status: Accepted.** 2026-09-20. Amends the database boundary of
[ADR 0034](0034-calver-baseline-and-database-compatibility-reset.md) and the restore compatibility
rule of [ADR 0027](0027-backup-and-restore.md).

## Context

ADR 0034 froze the base schema as the *acceptance contract*: `baseSchemaStmts()` is what a database
must already satisfy to be opened, `verifyBaseSchema()` runs before any DDL, and a database that does
not satisfy it is refused with instructions to take it through v0.4.72. That decision was made for a
reason — it removed a runtime that converted shapes nobody could test — and it left the codebase with
two ways to change the schema afterwards: move the baseline, or add nothing.

The first change after the reset exposed the cost. Adding three nullable columns to `user_groups`
would have meant declaring them in `baseSchemaStmts()`, which by construction refuses every database
written before this release — including every deployment inside the support window, and the one the
backup runbook tells an operator to take before upgrading. The alternative, storing per-OU policy in
the `meta` key-value store, would have avoided the schema question by giving the product a second,
weaker way to hold structured per-entity data: no types, no cascade on delete, a hand-parsed key
namespace.

Neither is what every other system does. Rails, Django, Flyway, golang-migrate and Prisma all apply
an ordered, forward-only list of steps and record which have run. The runtime was missing that ledger,
not a policy about migrations.

## Decision

### 1. The baseline stays frozen; additions are ordered steps

`baseSchemaStmts()` keeps the v0.4.72 shape and is not appended to. Everything a release adds after
it is a `migration` (`internal/app/migrate_steps.go`): an id, the columns/tables/indices it
guarantees, a restore rule, and an `up` function. The list is ordered, append-only, and a released
step is never edited — a database that already ran one will not run it again, so editing it would
leave upgraded databases on the old shape for ever.

The baseline is frozen **for this compatibility window**. ADR 0034 §5's mechanism — proving no
in-window source loses support, publishing a tested bridge, verifying it on both drivers — remains
the only way the baseline itself moves.

### 2. The ledger lives in `meta`

Each applied step records `mig:<id>` = the time it was applied. `meta` is in the frozen baseline, so
this needs no table of its own: a ledger table would be an acceptance-contract change of the kind
this ADR exists to avoid. A step is recorded **after** its work and its verification, never before.

### 3. A step is one transaction on one connection

Probing, DDL, product verification and the ledger row happen inside a single transaction, through a
`migExec` that carries it. Two consequences are the point of it:

- On SQLite the pool is a single connection (`store.go`), so a step that probed the schema through
  the pooled helpers while its own transaction held that connection would **deadlock**, not wait.
  `columnExists`/`tableExists`/`indexExists` now have one implementation, the `migExec` one, and the
  pooled methods delegate to it.
- A step that fails leaves nothing: the rollback takes the DDL with it. There is no "half-applied"
  state to reason about, and the retry applies the step from scratch.

### 4. What is recorded is verified on every start

Every step's declared products are checked at startup — for steps the ledger says are applied as
well. "Recorded" is not evidence: a column dropped by hand, or by a restore from a hand-edited dump,
would otherwise start up looking migrated. A `mig:` row naming a step this build does not have is
refused, exactly as a newer schema generation is: this build cannot know what that step changed.

### 5. Exclusion comes from one place

The ledger is re-read inside each step's transaction, so whoever takes the write lock second sees the
first one's row and does nothing. The exclusion that makes that the right test already existed:
`initSerialized` holds a Postgres advisory lock for the whole of `init`, and SQLite has a single
writer. A contended step is retried with backoff rather than failed — a restart that overlapped its
predecessor is an ordinary event, not a broken database.

### 6. A dump's migration list is a claim its content has to bear out

The dump header carries `migrations`: the steps the *data* satisfies, read from the ledger when the
dump was written. Restore validates it before anything is deleted:

- it must be a **contiguous prefix** of the steps this build knows — a hole, a repeat, an unknown id
  or a different order names no point in the sequence;
- a step the dump does **not** carry must declare `olderDumpOK`, or the dump is refused with the
  pre-existing instruction to restore it with the release that wrote it. A step that back-fills data
  cannot declare it, and `needsDataUpgrade` is the marker for the case this release does not handle:
  rather than guess, the restore refuses;
- a column may be missing from a dump **only** when the dump does not claim the step that added it
  and that step says an older dump is fine. A dump that claims `0001` and then arrives without
  0001's columns is refused — otherwise a damaged backup could be loaded as an old one.

This restores [ADR 0027](0027-backup-and-restore.md)'s stated intent — "an older dump, a newer schema
loads fine; this is the ordinary upgrade path" — which the implementation had drifted from, having
hardened into refusing *any* missing column. The hardening was right about the danger (a restore that
succeeds while silently producing rows that never had the data) and wrong about the remedy: the dump
itself says which columns it can legitimately lack, so the tolerance is now evidence-based instead of
blanket. Columns belonging to the **baseline** are still all required.

### 7. A restore is one transaction, ledger included

Clearing the tables, loading the dump, validating the result and rebuilding the ledger happen in one
transaction. A failure anywhere leaves the previous database and its ledger untouched. The ledger is
rebuilt rather than carried: `meta` is one of the tables a restore replaces, so the dump's rows
describe the release that wrote it, not the schema this binary has just verified.

### 8. Backups carry a step's tables

`backupTables()` is the baseline's tables **plus** the tables a migration declares. Without the
second half, a future step that adds a table would silently leave that table's data out of every
backup — the kind of loss nobody notices until a restore.

## Consequences

- **The acceptance contract is unchanged in shape.** A database that does not satisfy the baseline is
  still refused before any statement runs. What changed is the *startup behaviour* for a database
  that does: it is verified, and then the steps it lacks are applied. That is a real change and is
  stated as one.
- Adding a column is now a step and a declaration, not a debate about the boundary. The first,
  `0001`, adds `user_groups.totp_enroll`, `.passkey_enroll` and `.require_2fa` — all nullable, NULL
  meaning "inherit", so `olderDumpOK` holds and no data is touched.
- **Rollback.** A build that predates the ledger ignores `mig:` rows and accepts the wider shape (the
  boundary accepts supersets), so rolling back to it is safe for the database. A build that knows the
  ledger but not a newer step **refuses to start** — so the "an old binary can open this database"
  guarantee holds only up to that line, and not across it. Either way an older build does not know
  the new settings, so **policy enforcement silently stops applying**: that is the price of a code
  rollback, and the reason to roll back to a build ≥ the one that wrote the data.
- Restoring a pre-upgrade backup remains the supported way back, and now actually works across the
  migration boundary.
- The development loop is unchanged: delete `data/portal.db` and let it rebuild (it will rebuild as
  the baseline plus every step).

## Alternatives considered

- **Move the baseline.** Declare the columns in `baseSchemaStmts()` and mint a new generation: every
  existing database is refused and must be bridged. Correct, and periodically necessary — but doing it
  for three nullable columns would spend the reset on the first change, and would do it again for the
  next.
- **Keep everything in `meta`.** Store per-OU policy as key-value rows and never change the schema.
  No migration machinery at all, and the model for per-entity data becomes a hand-parsed key
  namespace — which is where the next five features also end up.
- **A `schema_migrations` table.** The conventional home for the ledger, and one more table to create
  outside the declared schema on every existing database. `meta` already exists in the baseline and
  already holds the generation marker.
- **A generic data-upgrade framework for restores.** Not written yet, so a dump that predates a
  data-upgrading step is refused rather than loaded and half-upgraded. The refusal is the honest
  state until that machinery exists.
