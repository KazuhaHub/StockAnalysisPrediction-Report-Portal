# Application failure availability matrix

Status: expanded development coverage. Explicit migration failure still needs
the same real-host availability wrapper before OPS-03 can be closed completely.

## Shared business check

Every covered failure scenario repeats the same authenticated HTTP checks:

- `/healthz` remains healthy and reports database health;
- public site branding remains available;
- login and the current session remain valid;
- `/api/home` returns the ordinary feed;
- an existing report remains readable; and
- the central notification unread queue remains available.

The checks run before and after the affected application reaches its failure or
recovery state.

## Covered failures

Download and verification:

- changed descriptor bytes;
- changed signature;
- changed archive bytes;
- interrupted archive transfer;
- publisher trust disabled before archive download; and
- canceled fetch before any request.

Workers and operators:

- a task handler returns a bounded failure while the other Portal routes remain
  available;
- a notification delivery fails while the queue and normal Portal routes remain
  available;
- an invalid administrator lifecycle request is rejected and does not affect
  unrelated routes; and
- a managed disable with an absent runner reaches truthful recovery state while
  health, session, home, report and notification queue remain available before
  and after a Portal route-stack restart.

Readiness and protocol:

- a zero/unstarted candidate cannot become ready; and
- malformed native/payload/source/schema/protocol/timestamp/candidate state is
  refused without changing durable readiness.

Each readiness/protocol refusal repeats the same Portal business-route check.

## Evidence

Portal `cbe37346` passed general test `41948502214`, quota/browser
`41948502082`, runner daemon `41948502128` and CodeQL. The first race attempt
failed in the pre-existing forecast SQLite diagnostics test with `SQLITE_BUSY`;
the unchanged job passed on rerun, and that initial failure remains part of the
run history. Evidence is in
[failure availability](handoff/evidence/platform-failure-availability/acceptance.json).

Explicit migration failure remains outside this wrapper. Browser launcher
refusal and readiness UX remain covered by the browser failure matrix.
