# Runner journal storage and startup verification

Status: P03 in progress. This implements the approved four-table local journal
from [the initial storage specification](13-control-storage-spec.md#7-runner-sqlite-journal-four-tables-one-runtime).
The Portal control tables and its baseline are unchanged. This is one journal per
runtime, not another database or database container for each installed app.

## API and source ownership

Source: `internal/apprunner/journal`.

- `Open(directory)` acquires exclusive local ownership, validates existing storage
  without repair DDL, and initializes only an empty journal.
- `Close()` closes SQLite before releasing ownership. It is idempotent.

The directory is a dedicated, existing, canonical absolute path. Runtime setup
must create it with the runtime owner's identity and mode 0700 before calling
Open. That is a one-time runtime setup responsibility; application manifests and
individual app installations never choose journal paths or configure databases.

The Store deliberately exposes no SQL handle, command dispatch, container adapter,
enrollment operation or credential API. A successful Open proves the implemented
storage checks passed. It does not authorize resuming recorded work or prove that
an enrollment key is currently trusted by Portal.

## Filesystem and ownership contract

The initial filesystem implementation supports Linux, plus Darwin for local
development. Other operating systems return `ErrUnsupported` before creating files.
The Portal can still be cross-built for its existing targets; this package does
not install a runner service or change the Portal startup path.

The journal directory must be owned by the effective runtime UID and have exactly
0700 permission bits. Ancestors must belong to that UID or root and may not be
group/world-writable without a trusted sticky-directory boundary. Symlink path
aliases are refused instead of silently selecting another runtime directory.

Allowed files are:

| Name | Purpose |
| --- | --- |
| `journal.sqlite3` | The single control journal |
| `journal.sqlite3-wal` | Committed/pending SQLite WAL state |
| `journal.sqlite3-shm` | SQLite WAL index/coordination |
| `journal.sqlite3-journal` | Recognizable SQLite rollback sidecar; not silently deleted |
| `.owner.lock` | Persistent ownership-lock inode |

Every existing entry must be a regular file, owned by the runtime UID, mode 0600
and singly linked. Unknown entries, unsafe permissions, symlinks, hard links and
orphaned sidecars refuse. Existing file permissions are not silently repaired.
The directory and database inode are checked again across opening boundaries.

The owner file is opened without following symlinks and held with a nonblocking
kernel flock. Another process cannot open the same runtime journal while that
owner is live. The lock file is not unlinked on Close: deleting it would permit
different processes to lock different inodes under the same filename.

New database/lock files are created privately; file and directory synchronization
is explicit. These checks do not prove filesystem hardware durability, container
UID mapping, ACL behavior or network-filesystem compatibility. D1 host-profile
acceptance remains required before workload deployment. A hostile process with the
same runtime UID, or host root compromise, is not an application sandbox boundary.

## SQLite connection and startup order

The package uses a separate driver registration. Its connection hooks do not change
the Portal's SQLite driver or the default driver used by other tools.

Each connection enables foreign keys, FULL synchronous writes, disabled trusted
schema, enabled CHECK enforcement and a bounded busy timeout. Darwin additionally
enables fullfsync/checkpoint_fullfsync. A single-connection pool provides the local
writer boundary, while flock excludes a separate cooperating process.

Opening follows this order:

1. Validate the directory/files and acquire ownership. Create only a missing empty
   database file; never replace existing contents or discard sidecars.
2. Open SQLite read-only with normal WAL visibility. Do not use `immutable=1` for
   real startup: committed intent can exist only in WAL after a process exits.
3. Read `user_version`. A future version refuses with `ErrNewerVersion`; version
   zero is initializable only when the schema has no objects. A partial/unknown
   schema is not adopted or repaired.
4. Match all user schema objects against the exact closed table/index definitions.
   Same-named weaker tables/indexes, extra views/triggers/tables and missing products
   refuse. Run engine integrity/foreign-key checks and direct physical-row checks.
5. Validate deployment-record presence when commands/resources/checkpoints exist,
   and verify that recorded public-key bytes match their fingerprint. An empty
   journal remains unenrolled; no identity or key is fabricated during Open.
6. Only after preflight succeeds, open a writer. For a truly empty journal, select
   WAL and create all tables/indexes plus `user_version=1` in one transaction.
7. Verify the writer profile and private files, synchronize directory metadata,
   and return ownership to the caller. Startup SQL work has a bounded context.

SQLite treats WAL as persistent database state; copying only the main file can
omit committed transactions. Keep that distinction in future checkpoint exports.
See [SQLite WAL documentation](https://www.sqlite.org/wal.html#the_wal_file).
The package currently provides no combined journal/app-data backup export.

The development fixture exposed a CHECK-invalid row that the read-only engine
integrity pragma reported as `ok`. Startup therefore evaluates its own closed row
predicates as well. These predicates are generated with the DDL; arbitrary catalog
SQL is never used as a validator. The failing bypass/reopen regression now proves
refusal before a writer is enabled. Engine integrity and foreign-key checks remain
independent; see [SQLite integrity-check semantics](https://www.sqlite.org/pragma.html#pragma_integrity_check).

## Physical products and enum clarification

`schema.go` owns all columns, nullability, SQL bounds and constraints. There are
four tables and four named explicit indexes:

| Table | Explicit indexes |
| --- | --- |
| `runner_deployment` | None beyond primary/unique constraints |
| `runner_commands` | `idx_runner_commands_installation`, `idx_runner_commands_state` |
| `runner_resources` | `idx_runner_resources_installation` |
| `runner_checkpoints` | `idx_runner_checkpoints_namespace` |

UUIDs/digests must have canonical lowercase ASCII form. Counters are signed SQLite
integers with positive/nonnegative rules. Text/blob limits count bytes. Receipts
must be bounded JSON objects; a prepared command cannot claim an acknowledgement
attempt. Verified checkpoints require manifest digest and verification time.

The private v1 implementation makes the initial physical enums explicit:

- Command results: prepared, applied, reverted, unknown.
- Command kinds follow the proposed runner actions: stage, verify, prepare-data,
  start-candidate, probe, activate, stop, snapshot, restore, remove-code, delete-data.
- Command stages use the existing work stages, excluding queued/terminal markers.
- Resource kinds: container, volume, socket, artifact.
- Resource intent: present, running, stopped, absent.
- Resource observation: unknown, present, running, stopped, absent, failed, quarantined.
- Checkpoint state: preparing, verified, restoring, deleted, review_required.

Resource presence is separate from application readiness or namespace retention.
These are development storage declarations, not a frozen public wire protocol or
an approved signature algorithm. D3/D4 must still review the authenticated control
and typed receipt contract before publisher/runtime use. SQL JSON-object checks
do not replace that future strict duplicate-key/unknown-field/parser validation.
After v1 is distributed, changing its physical contract needs a new journal version
and reviewed migration; a newer journal is never silently read as v1.

## Verification performed

Initial behavior tests were written and observed failing before implementation.
Additional orphan/key-fingerprint and bypassed-constraint regressions were also
observed failing before the corresponding checks were added.

The tests cover:

- Four-table/four-index initialization, all-empty unenrolled state and reopen.
- Connection replacement retaining the required per-connection settings.
- Unknown/newer/partial/corrupt input refusal without rewriting the main database.
- Same-named index drift and persisted CHECK violations, including read-only preflight.
- File modes, aliases/hard links, unknown entries, orphaned WAL and DSN-like path text.
- Counter/type/byte/enum bounds, premature acknowledgements and unverified checkpoints.
- Exclusive ownership in a separate test process and release after Close.
- A child that commits an intent then exits without SQLite Close/checkpoint. A
  test-only immutable read proves the row is absent from the base file; normal Open
  then recovers the command from WAL. No real startup uses that immutable shortcut.
- Real pager exhaustion during initialization, proving schema/version rollback and
  a subsequent clean retry without a test-only implementation failure hook.

The local engine is the repository-pinned SQLite 3.53.4 through modernc v1.60.1.
This does not upgrade any dependency or production version. Tests are synthetic;
the child processes are test binaries, not app containers or a deployed runner.
Process-exit recovery is not a hardware power-loss rehearsal.

Reproduce from Portal root with bounded workers:

```sh
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 \
  go test -race ./internal/apprunner/journal -count=1
TEST_POSTGRES_DSN='<disposable-loopback-database>' \
  GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 go test -p 2 -count=1 ./...
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 go vet -p 2 ./...
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 go build -p 2 ./...
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 GOWORK=off \
  GOTOOLCHAIN=go1.26.10 GOMAXPROCS=1 \
  go test -c -o /tmp/app-runner-journal-windows.test.exe ./internal/apprunner/journal
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 GOWORK=off \
  GOTOOLCHAIN=go1.26.10 GOMAXPROCS=1 \
  go test -c -o /tmp/app-runner-journal-linux-arm64.test ./internal/apprunner/journal
```

The local full Go/SQLite/PostgreSQL suite, journal race suite, vet/build, pinned
workflow lint and Windows/amd64 plus Linux/arm64 test cross-builds passed. Cross-
building is compile evidence, not execution on those hosts. The PR race concurrency-
package job now includes this package. CI for this commit on Linux and later
host/runtime acceptance remain separate evidence gates.

## Still required

- Authenticated enrollment and old-epoch/fence refusal, typed command admission,
  idempotent observation/acknowledgement and resource/checkpoint repositories.
- Durable Portal stage/effect commits, dispatch/reconciliation and actual native
  resource ownership/stop evidence. A stored `applied` label alone is not proof.
- Coordinated journal/app-data/key checkpoints, restored-host drills, retention
  and quota enforcement against the approved host/filesystem profile.
- The runner daemon/adapter, automatic app installer, capability broker and host SDK.

This slice does not complete P03, grant Docker access, publish an executable runner,
deploy a container, adopt Status or change any server beta configuration.
