# Application platform implementation progress

Reviewed: 2026-10-31, America/Los_Angeles.

During this pass, Portal main was `d0158f19`
and Apps main was `54239874c3e8c7af7071cfc6d8de669449440925`. The current
integration candidates are Portal draft PR 364 (`f181fbe`) and Apps draft PR 36
(`78531bf`); the earlier reviewable network chain remains open. The verified
candidate and SDK WIP commits in the table are the starting points. An integration
tree now also contains H01-H03, the current-main regression fixes and the
conditional CodeQL corrections.

| Task | Owner role | Candidate work | Local verification | Remaining gap |
| --- | --- | --- | --- | --- |
| H00 | repository owner; reviewers not yet named | Portal baseline `8a11f1d4`; Apps baseline `e7d12a60`; WIP `5374def0` / `f50eee22`; current candidates Portal PR 364 `f181fbe` and Apps PR 36 `78531bf` | Integration PRs are open drafts; older Portal PRs 352, 354 and 356 also remain open in their review chain; no production action | Name development, schema, visual and deployment approvers |
| H01 | integration reviewer required | Portal PR 364 and Apps PR 36 are the current paired candidates. Unpublished object storage now uses forward migration `0018` on top of immutable `0017`, fixing reused-database upgrades. | Apps PR 36 checks pass, including browser/container/artifact jobs. Portal PR 364 at `4bd5b5bf` is mergeable and all required checks pass, including Go, race, web, CodeQL, quota/browser, runner daemon and reused-database coverage. | Obtain review and make the integration decision |
| H02 | platform developer | Reviewed network transport, launch hint, shared hook, source pins and browser fixture path | Portal `a57aaf83`: test `41004183082` and quota/browser `41004183946` passed; Apps `415c66d1`: check `41002023666` passed. The current Portal source was re-synchronized into both Apps worktrees; `generate_app_contract.py --check` now matches for the network and integration branches. The integration Apps worktree passed typecheck and 296 SDK tests. | None for H02; integration remains H01 |
| H03 | platform developer | Explicit public-key bootstrap, private service config, long-running runner service, systemd restart bounds, Portal environment endpoint and static-app fail-open behavior | Integrated candidate `e622c7f3`: test `41049324619`, quota/browser `41049324669` and runner daemon `41049324581` passed. The quota/browser workflow started `service.Serve`, completed the Portal managed installation and SDK browser path, verified registration/RPC/receipts, then stopped the service and performed owned IPC/container cleanup | None for H03; production remains separately gated |
| H04 | platform developer | Portal `374993b8` adds current-viewer report list/detail authorization, launch grant advertisement, the reports bridge and `AppFrameV2` transport. Apps `576c6445` adds `useAppReports`, pinned shared bridge files, docs and examples. No database structure changed. | Portal test `41062843928`, quota/browser `41062843843` and runner daemon `41062843932` passed. The Linux browser requires `SDK_NATIVE_BROWSER_REPORTS` and exercised real launch -> root hook -> bridge -> Portal route query/get, then denied an old frame after access revocation. Apps checks `41059898215` and `41059889070` passed; shared contract check matched. Local Portal web ran 2312 tests, Apps SDK ran 288, and focused Go report/capability tests passed. | None for H04 report resources; files and credential resources remain H05/H06, and production remains separately gated |
| H05 | platform developer | D2 approved in `148-file-business-storage-proposal.md` and implemented in `149-application-object-storage.md`: one dedicated fixed-schema data table per plugin, with exact installation/account isolation and separate plugin/global counters. The owner requires uninstall to clear the plugin table; reinstall starts empty. Cleanup is revocation-first and transactional. Immutable migration `0017` remains the legacy namespace product; forward migration `0018` creates the plugin registry/table model and migrates recorded `0017` databases atomically. Object HTTP/API, dynamic-table backup/restore, bridge transport, Apps `78531bfe` SDK hook and static Notes reference app are implemented. | Focused Go plugin-table separation, `0017`→`0018` data migration, lifecycle/uninstall/account-deletion/quota/backup tests passed, including rollback after a post-cleanup admission failure. Migration and the entire `Platform*Postgres` suite passed on a temporary PostgreSQL 18.6 database. Portal bridge/transport/frame tests passed; the Apps integration worktree passed 296 SDK tests, Notes UI, Notes production build and contract-drift checks after regenerating both Apps worktrees from Portal source. | Native browser acceptance and full CI |
| H06 | platform developer | Implemented `150-secret-binding-use-and-rotation.md`: exact credential/binding reviews, strict canonical policies, current-use resolution, fixed Bearer/X-API-Key injection through the signed peer-authenticated runner exchange, response echo denial, reviewed shared rotation/revocation, binding/uninstall revocation, management HTTP and Portal UI, plus Apps SDK credential-name forwarding. No new schema or raw-read path. | Focused Go custody/lifecycle/review/HTTP/control/broker tests passed; credential concurrent mutation, restore, schema drift, secret binding and uninstall suites passed as part of the entire `Platform*Postgres` suite on PostgreSQL 18.6. Portal network tests and credential UI/locale tests passed; Apps SDK focused tests and TypeScript checks passed. | Native browser/service acceptance and complete CI |
| H07 | platform developer | Implemented `151-background-service-identity.md`: short-lived HMAC service assertions derived from the installation identity seed, no user/viewer/installer fields, bounded resource narrowing, managed-install capability proof and database revalidation on every checkpoint. `background.run` is now an explicit selected install provider. No new schema. | Focused app identity, expiry, wrong-seed, generation/policy/restore/capability revocation, background probe, install selection and managed-requirements tests passed | H09 durable task leases/reconciliation and native service acceptance |
| H08 | platform developer | Implemented `152-app-notification-publishing.md`: foreground launch-bound and background service-assertion publication into the existing central notification capture/routing/history/inbox/worker stack; closed occurrence/title/message/severity input, app-derived namespace, catalogue registration and no new notification storage. | Focused occurrence/idempotency/conflict, foreground binding refusal, service revocation, recipient injection refusal, disposable webhook delivery and install-capability tests passed | Native browser acceptance, real external providers and complete CI |
| H08A | not retained in this pass | `network.stream` remains denied. No ordinary buffered HTTP behavior is being represented as stream acceptance, and no owner decision has removed the first-release streaming promise. | Capability remains unavailable; existing tests prove denial | Owner scope decision, or a separately bounded protocol/frame/backpressure/cancel implementation with native end-to-end evidence |
| H09 | platform developer | Pure task state/attempt/fence/lease/backoff/uncertain/review transitions, fixed handler/payload registry, quota admission, execution deadline and result classification are implemented under `internal/appplatform/task*.go`. The two-table async schema is proposed in `153-app-task-schema-proposal.md`; no migration, worker or persistence has been added. | State-machine, lease/fence, retry/uncertain/review, handler-capability, canonical JSON, arbitrary command/path/callback refusal, quota, deadline and result-classification tests pass | Owner approval of the exact D2 tables, then migration/worker/UI/backup acceptance |
| H10 | not yet started in this pass | Event subscription/delivery requires its own approved D2 schema and depends on task/service authority. | None | Follow `144` H10 and preserve separate approval |
| H11 | platform developer | Static Notes uses the shared SDK object hook, reviewed `storage.files`, exact account/installation isolation, conflict handling and owner-approved clear-on-uninstall lifecycle. | Apps Notes UI 1 case, object hook/bridge 83 cases, both TypeScript builds and shared contract check passed after the per-plugin table migration. The real management-page fixture completed market discovery, TLS/signature verification, install, clear-on-uninstall, installable-state refresh and reinstall. A real Chromium run of the production Notes bundle through the SDK bridge passed at [320px dark zh-CN](handoff/evidence/notes-sample-320-zh-dark.png), [320px editor](handoff/evidence/notes-sample-320-zh-dark-editor.png) and [1440px light en-US](handoff/evidence/notes-sample-1440-en-light.png): document and body widths matched the 296px iframe viewport, there were no page/console errors, Tab entered the sandbox and reached Open, Enter opened the persisted note, the next Tab reached the editor, and the real page action completed an update before returning the new-note state. | Paired Portal/Apps CI on the final candidate |
| H12 | not yet started in this pass | Scheduled Digest depends on approved H09 persistence and the separate H12 neutral reference app. | None | H09 approval and H12 UI/runtime implementation |
| H13 | platform developer | `154-lifecycle-action-matrix.md` records every action, review, authority, data/code, failure and uncertainty boundary. Static-frontend update review/consume and the scheduler path remain in place. Managed updates now have exact review/admission, runtime selection in management HTTP, a managed queue and scheduler route, signed source quiesce, checkpoint-plan finalization and migration-to-start progression. Runner journal source lineage accepts ordinary install, writable install, `update-start` and `restore-start`, so a second update can stop/checkpoint the currently active generation instead of assuming generation one. Managed uninstall is admitted through the same review path and executed as `revoke -> stop -> remove_code -> complete`. `155-managed-update-checkpoint-design.md` fixes the checkpoint, journal, candidate-generation, rollback and acceptance gate. Portal persists signed `snapshot` and managed `restore` intents, validates exact receipt/key/endpoint/source tuples, and atomically applies `preparing -> verified -> migrate` and `restoring -> verified -> start`. Update-start registration and boot evidence, final active generation, namespace writer fence and installation tuple are checked before activation commits. Retry-exhausted managed updates preserve the source active tuple and enter `recovery_required`; the restore queue stops/removes the exact failed candidate and runs the signed restore-start chain once a reviewed restore operation exists. | Full `internal/app`, `internal/appplatform/...`, `internal/apprunner/...`, `go vet`, and the entire `TestPlatform*Postgres` set pass. New tests cover managed update projection/refusals, exact `managed=1` admission, scheduler claim, quiesce intent/commit, checkpoint finalization, migrate-to-start, whole synthetic activation, and journal stop/checkpoint from an update-start source. | Native A-to-B acceptance, native restore-vs-runner acceptance, complete ordinary managed-install activation audit, and reviewed managed namespace deletion |
| H14 | platform developer | Existing install/admin/app-shell pages provide many states. The application-platform panel now has complete install, update and danger-styled uninstall review states, installed-release detection, permission/generation diff and explicit clear-on-uninstall copy. A local real-browser pass checked the panel and uninstall dialog at 1440px and 320px in dark zh-CN, light en-US and light zh-TW; the 320px documents had no horizontal overflow and the dialog remained inside the viewport. A malformed `/api/version` response now degrades to a failed update check instead of taking down the management shell. | Component suites, typecheck and locale-key parity pass. Axe now checks the complete lifecycle panel and dialog; the test checks keyboard audience entry, Enter opening, focus entering the dialog and returning to the trigger after Escape. Browser screenshots/AX inspection cover the changed panel across those viewport/theme/language combinations. | Full neutral-state D5 review for every lifecycle page, screen-reader audit outside the changed dialog and complete UX-01..10 evidence |
| H15 | platform developer | Shared legacy/v2 templates, create-app, plugin-standard, strict descriptor/package builder, deterministic ZIP, publisher/descriptor signing verification, contract generation, catalogue rejection of development versions and exact source/test pins are implemented. | The current Apps integration worktree reran all 55 Python script tests, the maintained frontend standard and fresh legacy/v2 template typecheck/test/build/candidate generation without catalogue or trust changes. Contract products match Portal source after regeneration. Status UI passed 9 tests, and the Notes reference app passed its UI test and production build. | Actual release-key workflow, externally downloaded release assets for both architectures, non-implementer tutorial walkthrough and final publication approval |
| H16-H18 | not yet started in this pass | Full acceptance requires named owners, exact candidate SHA evidence and external CI/PostgreSQL/native hosts; production and legacy adoption remain separate owner decisions. | Existing focused/local evidence is mapped above but does not satisfy full matrix | Follow `144` H16-H18 with repository-owner assignment and explicit production/migration approvals |

Latest H13 evidence: a reviewed recovery operation can now run the same closed
generation-2 start/probe/activate chain as an update without replacing the
restored installation identity. `restore-start` requires its own signed operation,
persists reservation and running receipts, reuses the protected registration
channel, and verifies a fresh boot answer. A missing start effect is refused at
`activate`; altered registration, boot, native identity, release, namespace,
writer-fence or installation-tuple evidence cannot commit. SQLite and
PostgreSQL 18 tests also inject an audit failure after all activation writes and
prove the source generation remains active while the operation stays suspended.
This is repository transaction and recovery-executor evidence, not native
A-to-B or native restore acceptance.

Managed update admission is now wired from management HTTP through an exact
runtime observation into a `managed=1` operation. The writable scheduler claims
that operation before removal work; the executor performs source quiesce,
checkpoint verification and plan finalization, then reaches the existing
update-start launch/probe/registration/boot/activation chain. Journal fixtures
cover stopping and checkpointing a source that was itself launched by
`update-start`, rather than assuming an install-only source.

Earlier H13 evidence: the generation-2 `update-start` reservation is now stored
atomically on the prepared Portal start effect after exact runner acknowledgement.
Reload and replay accept only the same resource/effect/namespace/release/generation/
fence tuple; altered evidence is refused, and a failed audit insert rolls the
reservation update back. Focused SQLite tests and the full `internal/app`,
`internal/apprunner/...` and `internal/appplatform` suites pass. PostgreSQL 18.6
transaction tests also pass; native execution still requires its dedicated
environment.

Generation-2 native launch now has a separate `update-start` claim and running
receipt. The runner persists unknown intent before Docker mutation, pins the
exact project-quota namespace and sandbox, starts once, and settles the running
container atomically. Portal validates the complete receipt against the stored
reservation and advances only `start -> probe`. Focused SQLite and PostgreSQL
18.6 tests pass, including changed receipt refusal and audit-failure rollback.

Generation-2 readiness, registration, boot and activation now form one closed
chain. Registration uses a separate signed, digest-only command and a durable
runner journal claim; delivery rechecks the exact container, data profile,
protected file and private readiness socket. Portal verifies the resulting
receipt, requires a fresh boot answer from the registration key, then retires the
source activation, activates the candidate, advances the namespace writer fence,
updates installation generation/policy, replaces capability rows and completes
the operation atomically. Changed running receipts, native IDs, target policy,
document, boot, generation or activation evidence is refused. Retry-exhausted
managed updates now preserve the old active generation and enter
`recovery_required` instead of becoming terminal failures.

Native restore-start and A-to-B acceptance remain. Reviewed restore stops and
removes the exact failed update target, reaches `restore_data`, then executes
the signed `restore-start` reservation, launch, readiness, protected
registration, boot and activation chain for the restored release. The
generation-2 chain has focused SQLite and PostgreSQL 18.6 transaction evidence;
it is not yet a production update claim.

Production remains out of scope for this record. No real credential was imported,
no legacy application was migrated, no market release was published, and the
server beta channel was not pinned or changed.
