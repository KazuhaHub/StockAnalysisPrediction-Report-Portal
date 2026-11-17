# Cross-process recovery bundle prerequisite

Status: partial `DATA-05` evidence. This proves a portable Portal, keyring and
fixture-data bundle can reconstruct a paused control plane in a separate OS
process. It does not claim the required isolated fresh-host native data restore.

## Bundle

The source fixture creates:

- one active static platform installation with an exact release, data namespace
  and complete install operation;
- one verified recovery-set row bound to that installation, namespace,
  operation and release;
- one credential sealed through the real keyring;
- one waiting application task;
- one active event subscription;
- a small fixture-data payload and manifest; and
- a full logical Portal dump containing the control, keyring and work records.

The payload is copied into an independent bundle directory. The logical dump,
metadata and payload are owner-readable regular files.

## Child process

The parent starts a separate instance of the test binary. The child receives
only the bundle path and:

1. verifies the copied data digest;
2. opens a new SQLite database and restores the logical Portal dump;
3. verifies restore pause and notification pause;
4. verifies runtime bindings have no enabled or sealed execution authority;
5. verifies the waiting task and active subscription become `review_required`;
6. verifies the installation and recovery set require review;
7. decrypts the fixture credential with the restored keyring; and
8. serves `/healthz` successfully while external work remains paused.

The child emits explicit markers for database restore, paused effects, secret
decryption, data identity and availability. The parent requires every marker
and a zero child exit status.

## Evidence

The focused command is:

```sh
go test ./internal/app \
  -run '^TestFreshHostPortalRecoveryBundleRestore$' -count=1
```

Portal `b1225e6d` passed general test `41984691217`, project quota/native
`41984691142`, runner daemon `41984691139` and CodeQL `41984688175`. Evidence is
in
[recovery bundle](handoff/evidence/platform-recovery-bundle/acceptance.json).

## Remaining DATA-05 work

This is the same GitHub-hosted Linux kernel, not a newly provisioned host. The
bundle does not yet carry or restore:

- the native XFS checkpoint tree into a newly created project-quota namespace;
- the runner journal/data-root identities; or
- a post-restore native app with every external effect still paused.

Those remain required before `DATA-05` can be marked complete.
