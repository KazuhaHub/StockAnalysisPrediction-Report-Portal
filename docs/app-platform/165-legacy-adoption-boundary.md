# Legacy adoption boundary

Status: development acceptance. This proves that matching names do not grant
managed ownership. It does not implement P14 app-specific adoption.

## No name-only app adoption

Legacy ZIP apps and platform releases remain separate records:

- legacy manifest/assets live in `apps` and `app_files`;
- reviewed platform packages live in the platform release/install/operation
  tables; and
- an application ID is descriptive identity, not ownership proof or migration
  permission.

The integration fixture installs a legacy app, then stages a managed package
with the same application ID. Presenting that managed package through the legacy
install consumer is refused. The refusal creates no platform installation or
operation. The legacy name, version, entry, file content and token path remain
unchanged.

The SQLite and PostgreSQL fixtures exercise the same boundary.

## No container takeover

Managed lifecycle code verifies the exact installation, resource, release,
control fence and ownership labels before start or stop. A deterministic
external-container fixture with a foreign installation label is inspected once
and then refused. No managed POST is issued and the observed fixture state
remains running.

Existing disposable Docker evidence covers the native case: a restart discovers
a stale container that references the app's exact private mount, but the
reference alone is not adoption authority. The container is not mutated or
removed, and a duplicate reference is reported as ambiguous.

## Evidence

The focused commands are:

```sh
go test ./internal/app \
  -run '^TestLegacyAppContinuesWhenManagedReleaseIsRejected(SQLite|Postgres)$' \
  -count=1
go test ./internal/apprunner/driver \
  -run '^(TestLegacyContainerContinuesAfterManagedLifecycleRefusal|TestCandidateLifecycleRefusesForeignPolicyBeforeMutation)$' \
  -count=1
```

Portal `965214ee` passed general test `41977522768`, project quota/native
`41977522875`, runner daemon `41977522785` and CodeQL `41977518689`.
Evidence is in
[legacy adoption](handoff/evidence/platform-legacy-adoption/acceptance.json).

## Limits

This is a refusal and continuity boundary, not a data/code/key migration. A
future accepted adoption plan still needs an exact package/version map,
ownership proof, offline recovery point, credential/OU namespace review,
no-duplicate-writer cutover and rollback evidence.

The new container lifecycle fixture uses a deterministic daemon client. Native
resource discovery and non-adoption remain bound to the earlier disposable
Docker/XFS matrix, not a new production daemon drill.
