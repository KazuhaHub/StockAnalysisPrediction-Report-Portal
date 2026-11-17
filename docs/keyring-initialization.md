# Atomic keyring initialization

The application-platform actor seed must use the existing protected keyring.
Before adding that seed, the keyring's first-use concurrency behavior needs to
preserve the same encryption key across independent Server instances.

## Reproduced failure

The previous creation path checked two settings independently and wrote salt and
wrapped data key with separate commits. Concurrent creators could overwrite or
mix those values. A creator also returned its locally generated data key even if
another complete keyring had won storage. Its newly sealed secrets then could not
be opened by a fresh instance reading the database.

The added eight-creator regression observed seven unpersisted cached keys and
authentication failures before the fix. A SQLite trigger rejecting the wrapped-key
insert also reproduced a leftover salt row. Initialization of a partial existing
pair overwrote it instead of refusing uncertain key custody.

## Implementation

`internal/app/keyring_store.go` reads both settings with one SELECT. Absence of both
rows means uninitialized. A missing, NULL or empty half is an error; it never means
permission to replace the surviving key material.

`SaveKeyring` starts one transaction and obtains write serialization before reading:

- SQLite acquires write intent using the existing metadata table.
- PostgreSQL takes transaction advisory lock `780717`, separate from the OU
  lifecycle lock `780716`.

It rechecks the pair under that lock, retains an existing complete pair, or inserts
both new rows with one statement in the same transaction. A failed insert rolls
back the complete creation. No table, column, index or baseline change is needed.

`loadOrCreateDEK` rereads and unwraps the stored pair after attempting creation.
It never caches the local candidate merely because saving returned success.
Creators with the same configured secret therefore use the stored winner's key.
A creator that cannot unwrap the winner refuses rather than sealing with an
unrecoverable candidate.

The existing encrypted format, purpose binding, lazy initialization, previous-key
rotation and healthy stored key material stay compatible. `Keyring()` retains its
legacy non-error read signature; the initialization path uses the strict error-
returning reader so storage errors and partial state cannot become regeneration.

Acquire the Server data key **before** starting a business, OU or application-control
transaction. The keyring initializer opens its own transaction. In particular,
calling it while holding SQLite's sole pool connection would block on that same
connection. After loading the key, use it for purpose-bound cryptography within
the later control transaction without opening another database connection.

## Verification scope

Meaningful failing regressions were run before implementation. The new tests cover:

- Eight independent Server caches racing initialization; each result matches the
  durable data key and every secret opens through a fresh cache.
- Concurrent store-level create attempts preserving a matching salt/key pair.
- Insert failure leaving neither keyring row.
- Missing, NULL and empty half cases refusing load and replacement without database mutation.
- Existing round-trip, purpose binding and secret-key rotation regressions.

The concurrent cache and partial-state fixtures run on SQLite and PostgreSQL.
The PostgreSQL fixtures use pooled connections against a disposable loopback
database. They restore prior test metadata after completion. The trigger-based
write-failure fixture is SQLite-specific. These are database integration tests,
not a claim of an operating-system process/crash or production recovery rehearsal.

Local commands use bounded workers and the repository's declared toolchain:

```sh
TEST_POSTGRES_DSN='<disposable-loopback-database>' \
  GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 \
  go test -race ./internal/app \
  -run '^(TestKeyring|TestPostgresKeyring|TestRotate|TestSecret|TestSealed)' -count=1
```

Local full Go tests with the disposable PostgreSQL target, full vet/build and the
targeted race suite passed. The contention cases also passed 20 repetitions with
the race detector. Exact-source CI is a separate required review gate.

This repair is a prerequisite for the application identity/review repository.
It does not implement review-plan consumption, application execution or a complete
installer. No production credentials, deployment or server version configuration
are changed.
