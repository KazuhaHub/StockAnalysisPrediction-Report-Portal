# Lifecycle restart matrix

Status: development acceptance. This covers every nonterminal durable
lifecycle stage through a Portal store restart and a runner journal restart.
It does not claim exactly-once physical effects or a production host-failure
drill.

## Covered stages

The restart matrix covers the sixteen nonterminal operation stages:

- `queued`, `validate`, `download`, `stage`, `prepare_data`;
- `quiesce`, `snapshot`, `migrate`;
- `start`, `probe`, `activate`;
- `revoke`, `stop`, `remove_code`, `remove_data`; and
- `restore_data`.

`complete`, `failed` and `recovery_required` are terminal or review states, not
work available for another writer. Existing lease guards keep them out of the
claim path.

## Portal restart

The matrix uses a file-backed SQLite Portal and a second PostgreSQL connection.
For each stage it:

1. gives worker A the current operation lease;
2. creates one exact effect intent for stages that can dispatch an effect, then
   marks that intent `unknown` to represent an interrupted acknowledgement;
3. closes the Portal store and opens it again;
4. proves that a live pre-restart lease still cannot be stolen;
5. expires that lease and gives worker B the same operation with exactly one
   higher fence;
6. refuses worker A's heartbeat and effect preparation;
7. returns worker B the original ambiguous effect, with its original admitted
   fence, without inserting a second effect;
8. proves worker B can renew the lease; and
9. proves takeover did not skip or rewrite the durable stage.

The ten effect-intent stages are `stage`, `prepare_data`, `snapshot`, `start`,
`probe`, `activate`, `stop`, `remove_code`, `remove_data` and `restore_data`.
The remaining six stages are Portal control transitions; their restart proof is
the exact lease/owner fence rather than a fabricated native effect.

Reclaim is permission to inspect and reconcile this operation only. It is not
permission to replay an unknown effect. Existing scheduler and recovery tests
still require a typed receipt or move the operation to an explicit review
state.

## Runner restart

The runner journal test enrolls one controller, persists an unknown command row
for every closed journal stage, closes the owner-only journal, and reopens it.
After restart:

- every command remains `unknown`;
- no command becomes `applied`;
- effect identity, kind, receipt and admitted fence are unchanged;
- opening the journal does not replay or synthesize work; and
- a second row for the same operation/stage/effect identity is refused.

The existing signed-command test separately proves that a stale installation
fence is refused after journal restart and that the original admitted fence is
not rewritten.

## Evidence

The focused commands are:

```sh
go test ./internal/app \
  -run '^TestPlatformLifecycleRestartMatrix(SQLite|Coverage)$' -count=1
go test ./internal/apprunner/journal \
  -run '^TestLifecycleUnknownCommandsSurviveRunnerRestart$' -count=1
TEST_POSTGRES_DSN=<fresh-database> go test ./internal/app \
  -run '^TestPlatformLifecycleRestartMatrixPostgres$' -count=1
```

The PostgreSQL test uses a second connection to the same database while the
pre-restart store remains open. Evidence is recorded in
[lifecycle restart](handoff/evidence/platform-lifecycle-restart/acceptance.json).

Portal `8c5a4b12` passed general test `41972143589`, project quota/native
`41972143744`, runner daemon `41972143587` and CodeQL `41972137793`.

## Limits

The Portal restart is a real close/reopen of the durable store and ownership
state, not a process-wide SIGKILL at every stage on a disposable Linux host.
The runner restart is the production owner-only journal close/reopen path, not
the compiled runner daemon killed at every command stage.

Typed native start, stop, data, registration, restore and removal boundaries
retain their separate disposable Linux evidence. This matrix binds their
durable stage identities and stale-writer rules; it does not replace those
native drills or production rollout.
