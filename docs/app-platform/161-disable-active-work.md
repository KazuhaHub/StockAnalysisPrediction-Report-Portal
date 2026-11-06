# Disable with active application work

Status: development acceptance. Production load, deployed multi-process
availability and manual operator review remain separate.

## Admission boundary

Disabling an installation is an immediate authority change, not a consequence
of the later runner stop. The reviewed disable transaction:

- moves the installation to `disabled/disabled`;
- retires the active activation;
- changes every running or leased task to
  `uncertain/installation_disabled`;
- closes its attempt as uncertain;
- clears the task worker and lease without retaining a result;
- cancels waiting work; and
- pauses event subscriptions while retaining the reviewed data and bindings.

The task then cannot acknowledge a late handler result. Current service
identity and managed backend access both fail closed after the disable commits.
The worker claim query also excludes the installation, so no new task or event
delivery can start.

## Runner unavailable

The managed runner stop remains a separate durable operation. If the runner is
absent before a stop intent is persisted, the operation reports
`managed_disable_unavailable` and does not claim that a process stopped. If a
stop intent exists but its receipt is lost, the installation remains
`disabled/recovery_required` with a prepared stop effect. Neither state is
reported as a confirmed shutdown.

## Evidence

The focused test exercises a running task and a leased attempt, commits the
normal administrator disable review, and verifies the task and attempt become
uncertain, the live worker lease and result disappear, and a later claim
returns nothing. The same test runs on SQLite and PostgreSQL when the CI
PostgreSQL service is present.

The managed disable test verifies broker access before disable and immediate
local denial after disable, before any runner recovery behavior. Existing
offline and scheduler tests continue to verify truthful pending/recovery states
without fabricated stop receipts.

Portal `3199cf82` passed general test `41931370312`, quota/browser
`41931370229`, runner daemon `41931370293` and CodeQL. Evidence is in
[disable active work](handoff/evidence/platform-disable-active-work/acceptance.json).
