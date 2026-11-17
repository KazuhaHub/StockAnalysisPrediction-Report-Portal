# Frontend installation service lifecycle

The Portal process now owns the no-data frontend installation scheduler described
in [57](57-runtime-free-frontend-installation.md). An admitted compatible initial
installation can progress without a test harness manually calling the scheduler.
This is source implementation for development. It does not approve deployment,
configure a production runner or complete the marketplace installation experience.

## Source map

| File | Responsibility |
| --- | --- |
| internal/app/app_platform_install_service.go | Process owner, identity, start-once, cancellation, join and panic boundary |
| internal/app/server.go | Start after existing boot reconciliation; stop before closing the store |
| internal/app/app_platform_install_scheduler.go | Existing bounded profile-separated selection and execution |
| internal/app/app_platform_install_service_test.go | Actual autonomous install, ownership, pause/native/fresh-store refusal, panic containment |

## Start and stop

1. Normal Portal startup calls `startPlatformFrontendInstallService` after boot
   reconciliation, next to the notification runtime startup.
2. A `sync.Once` owner creates a random worker UUID and a cancellable context.
   Concurrent/repeated starts on that Server return the same owner's stop
   function; they do not start another scheduler or replace its identity.
3. The scheduler immediately attempts work and then polls every 500 ms. It
   receives an empty native endpoint and selects only the canonical no-data
   frontend review format. All authority, lease, fencing and transaction checks
   from the existing selector/driver still apply.
4. After HTTP requests drain, shutdown cancels this owner before stopping the
   other owned runtimes and closing SQL. The stop call waits for worker exit up
   to the existing shared worker shutdown deadline. It is safe to call repeatedly
   or concurrently. A deadline overrun logs a fixed message; it is not proof that
   the worker joined. Context-bound database work is canceled and retained work
   remains subject to existing fenced recovery on the next process.
5. A worker panic terminates this owner and signals exit instead of terminating
   Portal or retrying an uncertain stage. Only a fixed log message is recorded;
   panic values, SQL and secrets are not exposed. Durable leases/evidence are not
   cleared by panic handling. A subsequent process starts a new owner; the same
   owner does not automatically restart after panic or stop.

Constructor/identity failure starts no worker and records a fixed service error.
This change adds no YAML setting, database table, migration, native endpoint,
credential, OS identity, Docker request or public scheduler control endpoint.

## Authority and compatibility boundaries

A fresh database has no platform control identity. Polling refuses without
initializing that identity. A restore-paused controller stays paused; startup
does not resume it. An eligible native-data operation is ignored by this owner,
including if it exists before a no-data operation. Native service startup remains
an explicitly bootstrapped development operation and is not added to RunServer.

No browser credential, app-use grant or required capability is created by merely
starting the service. Review admission must already have bound current
administrator authority, exact release bytes/trust and explicit audience.
Activation still grants exactly that reviewed audience. There is no administrator
launch bypass. Frontend required providers continue to refuse; optional providers
remain declined. A managed-backend install is not made compatible by this worker.

## Reproduce the focused tests

Use the repository's pinned toolchain. From the Portal repository:

```sh
GOWORK=off GOTOOLCHAIN=go1.26.10 GOMAXPROCS=2 go test -p 2 ./internal/app -run '^TestPlatformFrontendService' -count=1
```

With an owned disposable PostgreSQL database, set TEST_POSTGRES_DSN and run the
same command to include the PostgreSQL ownership/install test. Do not point this
at production: these integration fixtures own and reset their test database.

The tests prove autonomous queued-to-launch completion both at startup and after
an idle period, without runtime binding, namespace or external-work effect,
concurrent start/stop ownership, worker join,
no changes on fresh/paused/native-only stores and containment of a deliberately
broken worker. They do not prove managed container isolation, physical quotas,
network exfiltration prevention, real marketplace downloads, browser installation
confirmation or full shutdown behavior of every existing Portal background loop.
Those retain their separate acceptance gates.

Local validation passed the full Go suite with disposable PostgreSQL (application
package 155.990 s), focused SQLite/PostgreSQL frontend service/scheduler/install
race tests (4.882 s), `go vet`, `staticcheck` and Linux
amd64 Portal cross-compilation. The newly added admission-after-idle test also
passed independently under the race detector. These results do not substitute
for the disposable Linux native regression workflow or production acceptance.

## Next integration

The administrator plan/commit/progress HTTP routes and confirmation UI must now
use the existing canonical review and idempotent admission repositories; they
must not run a worker inline in an HTTP handler. Operation progress comes from
durable SQL, not the last in-memory scheduler status. Remote v2 catalogue and
publication, managed lifecycle, broker capabilities, recovery UX and production
approval remain open in the full platform plan.
