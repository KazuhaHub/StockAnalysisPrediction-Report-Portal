# Install failure boundary

Status: development acceptance. This record covers the repository install
stages individually. It is not a production deployment drill and it does not
claim one continuous native run injected every named stage.

## No failed candidate traffic

The persisted install pipeline has a single user-facing admission boundary:
candidate traffic is allowed only after an active activation, current release,
intact document and current audience/runtime checks succeed.

For validate, download and stage failures, the fixture:

- injects a real stage audit failure at the named stage;
- runs the production frontend scheduler;
- requires the failure rather than a silent stage skip;
- requires the installation not to become `ready`;
- requires zero active activation rows and zero access grants;
- bounds release/frontend residue to the original staged package; and
- sends the normal launch HTTP request, which must return 404.

## Download and verification

The market fixture covers changed descriptor bytes, changed signature, changed
archive bytes, interrupted archive transfer, publisher revocation before
artifact download and canceled fetch. Invalid/rejected packages do not create
`app_platform_releases`, and untrusted descriptors do not trigger archive
download. The same authenticated Portal business checks remain available.

## Stage and readiness

Data preparation, activation and retry failures have focused SQLite/PostgreSQL
rollback and recovery coverage in the runtime/data matrix. Readiness zero-state,
malformed state and protocol mismatch are refused without durable mutation, and
the real Portal business routes remain available in the failure-availability
matrix.

## Evidence

Portal `5ae75b4f` passed general test `41964920990`, quota/browser
`41964921034`, runner daemon `41964921405` and CodeQL. Evidence is in
[install failure](handoff/evidence/platform-install-failure/acceptance.json)
and [failure availability](handoff/evidence/platform-failure-availability/acceptance.json).
