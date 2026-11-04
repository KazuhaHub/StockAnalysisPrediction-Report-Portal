# Application platform implementation progress

Reviewed: 2026-11-01, America/Los_Angeles.

During this pass, Portal main was `d0158f19`
and Apps main was `54239874c3e8c7af7071cfc6d8de669449440925`. The current
integration candidates are Portal draft PR 364 (`ffdd3b16`) and Apps draft PR 36
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
| H05 | platform developer | D2 approved in `148-file-business-storage-proposal.md` and implemented in `149-application-object-storage.md`: one dedicated fixed-schema data table per plugin, with exact installation/account isolation and separate plugin/global counters. The owner requires uninstall to clear the plugin table; reinstall starts empty. Cleanup removes only the selected installation's rows, keeps other installations of the same plugin intact, and drops the plugin table only after its last non-removed installation is gone. Other plugins and their counters are unaffected. Cleanup is revocation-first and transactional. Install admission and last-uninstall cleanup now share a PostgreSQL per-app advisory lock before storage mutation, fixing a reproduced global-storage/installation deadlock during concurrent reinstall. Immutable migration `0017` remains the legacy namespace product; forward migration `0018` creates the plugin registry/table model and migrates recorded `0017` databases atomically. Object HTTP/API, dynamic-table backup/restore, bridge transport, Apps `78531bfe` SDK hook and static Notes reference app are implemented. | Focused Go plugin-table separation, `0017`→`0018` data migration, lifecycle/uninstall/account-deletion/quota/backup tests passed, including rollback after a post-cleanup admission failure. Portal `3cdb0659` adds cross-driver management-API evidence that uninstalling one plugin drops only its table while another plugin's table, rows and counters survive. Candidate `58b07314` adds [plugin storage evidence](evidence/platform-plugin-storage/acceptance.json): a concurrent last-uninstall/fresh-install PostgreSQL scenario passed 20 iterations and five race-enabled iterations after the advisory-lock fix. Migration and the entire `Platform*Postgres` suite passed on a temporary PostgreSQL 18.6 database. Portal bridge/transport/frame tests passed; the Apps integration worktree passed 296 SDK tests, Notes UI, Notes production build and contract-drift checks. The real management fixture completed clear-on-uninstall and empty reinstall; H14 Chromium evidence confirms the explicit single-plugin review boundary. Portal `2693793e` passed general `41402057322`, quota/browser `41402057341` and runner daemon `41402057334`. | None for development H05. Production data migration/volume approval remains separately gated |
| H06 | platform developer | Implemented `150-secret-binding-use-and-rotation.md`: exact credential/binding reviews, strict canonical policies, current-use resolution, fixed Bearer/X-API-Key injection through the signed peer-authenticated runner exchange, response echo denial, reviewed shared rotation/revocation, binding/uninstall revocation, management HTTP and Portal UI, plus Apps SDK credential-name forwarding. No new schema or raw-read path. Launch metadata now advertises every foreground capability actually granted instead of a stale three-item list. | At Portal `bdc9cb4c`, quota/browser run `41367466784`, general test run `41367466839` and runner daemon run `41367466921` pass. The real Chromium fixture sends only the binding name, the runner injects the fixed header into the disposable HTTPS request, the browser receives the response without the value, and policy 1 bindings remain unavailable after policy 2 until explicit reapproval. Focused SQLite/PostgreSQL custody, lifecycle, review, HTTP, control, broker, credential restore, secret binding and uninstall suites pass. | Development acceptance complete for H06. Real external providers, restored production credentials and production deployment remain separately gated |
| H07 | platform developer | Implemented `151-background-service-identity.md`: short-lived HMAC service assertions derived from the installation identity seed, no user/viewer/installer fields, bounded resource narrowing, managed-install capability proof and database revalidation on every checkpoint. `background.run` is now an explicit selected install provider. No new schema. | Focused app identity, expiry, wrong-seed, generation/policy/restore/capability revocation, background probe, install selection and managed-requirements tests passed | H09 durable task leases/reconciliation and native service acceptance |
| H08 | platform developer | Implemented `152-app-notification-publishing.md`: foreground launch-bound and background service-assertion publication into the existing central notification capture/routing/history/inbox/worker stack; closed occurrence/title/message/severity input, app-derived namespace, catalogue registration and no new notification storage. Apps `1dedcc0` adds `useAppNotifications()`, and Portal advertises the exact foreground grant through the shared bridge. | Portal `58eadbba`: quota/browser run `41375050128`, general test run `41375050171` and runner daemon run `41375050161` pass. The real Chromium fixture publishes through the SDK and the parent independently verifies exactly one `extension:app.<id>:browser-notification` event in `notification_events`. Portal Web passed 2821 tests; Apps SDK passed 301 tests; Apps PR 36 at `1dedcc0` is clean. Focused idempotency/conflict, foreground binding refusal, service revocation, recipient injection refusal, disposable webhook delivery and capability-selection tests pass. | Development acceptance complete for foreground SDK and central capture. Real external providers, administrator delivery-rule acceptance and production notification enablement remain separate gates |
| H08A | platform developer | Implemented `158-reviewed-network-stream.md`: `network.stream` now has an independent canonical ordinary-HTTPS response policy, exact grant/install/runtime-profile binding, a separate signed runner command and challenge domain, 32 KiB ordered frames, 4 MiB total/30-second duration bounds, per-frame current-authority checks, credential echo protection, cancellation and end-to-end backpressure. Portal writes bounded NDJSON start/chunk/end/error frames; the bridge waits for an app acknowledgement before the host reads the next chunk; Apps exposes `useAppNetwork().stream()` with binary callback delivery. No schema or buffered `network.http` semantics changed. | Portal `b7ce1d19` and Apps `b3a5d1b`: full Chromium/native run `41706400426` passed with a real TLS disposable upstream, Portal NDJSON session, SDK browser stream, app-ack slow consumer, revoke-before-frame and revoke-after-frame cases. Native-only run `41693464881` also passed. The artifact records `native_sdk_network_stream=true` and requires `SDK_NATIVE_BROWSER_NETWORK_STREAM`; evidence is in [platform network stream](evidence/platform-network-stream/acceptance.json). Portal Web typecheck, 118 focused network/bridge/frame tests and all Apps PR 36 checks pass. | Development acceptance complete for H08A. Public SDK/protocol freeze, production grant issuance and production egress policy remain separately gated |
| H09 | platform developer | Owner approved the exact D2 schema in `153-app-task-schema-proposal.md`. Migration `0019` adds exactly the task and attempt tables. Durable admission, canonical occurrence replay, PostgreSQL/SQLite conditional claim, same-transaction attempt creation, H07 service authority, fixed handler registry, bounded retry/backoff, fence/revision checks, uncertainty and review handling, lifecycle invalidation, restore review, `cmd/app-task-worker`, management API and administrator task page are implemented. `app.backend.task` is pinned to `/v1/tasks/run`; no payload-defined command, destination or method is accepted. A separate signed service-task command now binds task/lease/installation/generation/policy/epoch/release/identity/body and the runner forwards the fixed route without a viewer. Candidate `308a35a3` records [application task evidence](evidence/platform-application-tasks/acceptance.json). | Focused SQLite and PostgreSQL 18.6 task schema, concurrent claim, worker execution, authority revocation, retry, uncertain/no-replay, backup/restore, lifecycle invalidation, API, locale and Web component tests pass. Portal Web typecheck, lint, production build and all 2855 tests pass. Full `go test ./...`, focused race and `go vet ./...` pass; Linux cross-compilation covers the new runner transport. Hosted test `41760001967`, quota/browser `41760001701`, runner daemon `41760001776` and CodeQL `41759996027` all pass at `308a35a3`. The first hosted race shard failed a same-second schedule replay; `ff9d2f4b` fixed it and passed 20 repeated race runs. | `app.backend.task` native initial-managed transport is implemented; Scheduled Digest app route, update-generation registration variants and production remain gated |
| H10 | platform developer | Exact one-table subscription design is in `159-event-subscription-schema-proposal.md`; delivery reuses the approved task queue and fixed `app.event.delivery` handler. The schema-independent closed event catalogue, stable `(occurred_ms,event_id)` sequence, canonical bounded delivery envelope, same-instant ordering and occurrence identity are implemented in `internal/appplatform/event.go`. | Event catalogue, resource binding, canonical payload, duplicate/trailing/unknown-field, sequence-order and occurrence tests pass. No schema or product admission path applied. | Owner approval of `0020`, then transaction-integrated delivery, native app handler and SQLite/PostgreSQL/JOB-05/06/08 acceptance |
| H11 | platform developer | Static Notes uses the shared SDK object hook, reviewed `storage.files`, exact account/installation isolation, conflict handling and owner-approved clear-on-uninstall lifecycle. Its editor route is an explicit viewport layout with one intentional internal scroll region. | Apps `baa9a66f` passed TypeScript, SDK and Notes tests plus production packaging. Portal `14a1a374` exercised the production Notes package through the real `AppViewV2` shell at 1440px light en-US and 320px dark zh-TW: one title, no version row or outer frame, save/reload persistence, viewport scroll, keyboard scroll, AA contrast and no serious/critical axe findings are recorded in [platform Notes](handoff/evidence/platform-notes-shell/acceptance.json). | Paired development CI is green; the remaining UX-01 gap is the second managed reference app from H12 and the manual screen-reader audit |
| H12 | not yet started in this pass | Scheduled Digest depends on approved H09 persistence and the separate H12 neutral reference app. | None | H09 approval and H12 UI/runtime implementation |
| H13 | platform developer | `154-lifecycle-action-matrix.md` records every action, review, authority, data/code, failure and uncertainty boundary. Static-frontend update review/consume and the scheduler path remain in place. Managed updates now have exact review/admission, runtime selection in management HTTP, a managed queue and scheduler route, signed source quiesce, checkpoint-plan finalization and migration-to-start progression. Runner journal source lineage accepts ordinary install, writable install, `update-start` and `restore-start`, so a second update can stop/checkpoint the currently active generation instead of assuming generation one. Completed update history now reconstructs the exact active generation from its checkpoint, update-start, registration and data-lineage evidence; launch, directory, page binding, current-viewer backend requests and finalized-update network grants all bind the current generation instead of install generation one. Verified recovery supersedes only its exact completed restore lineage, so a resolved failed update no longer blocks the restored or re-enabled generation. The runner has a distinct signed update-start backend request path and releases the removed source's protected HTTP storage. Managed uninstall is admitted through the same review path and now executes `revoke -> stop -> remove_code -> remove_data -> complete`; the signed delete-data command binds the original preparation receipt, project ID, device, inode, quota, writer fence and release. Portal marks the namespace `deleted` only after a runner receipt proves the namespace tree and quota are absent. Reviewed standalone `delete_data` now has its own exact namespace projection and `queued -> validate -> revoke -> remove_data -> complete` executor. It binds the namespace revision/owner/state, retired activation and release, prepare-data operation/effect/receipt, data format, writer fence, project/device/inode/quota/ownership, recovery-set digest, runtime/controller epoch and actor review. Admission clears the namespace owner and marks it `deleting` without touching code, grants or the dedicated plugin table; completion requires the exact runner receipt, and uncertainty stops at `recovery_required` without automatic replay. Disable now has distinct frontend and managed scheduler execution: it immediately retires live authority while preserving code, access rows, credentials and data; managed completion requires an exact stopped receipt, while an unavailable runner is recorded as unavailable or `recovery_required`, never as stopped. Managed enable preserves the source release/policy/grants, checkpoints the retained namespace, starts a new generation and requires probe, registration, fresh boot and activation. | Native-only run `41330590646` completed install, A-to-B update, failed B-to-C recovery, exact failed-target stop/remove-code, verified XFS restore, generation-3 restore-start activation, managed disable, managed enable, a real reader backend request, then managed uninstall through `stop -> remove_code -> remove_data`. The same run passed the focused resolved-recovery, historical enable and backend-history tests. Its later one-image UUID replacement check was skipped because the disposable loop remained externally held and is recorded as a separate open identity-test gap. The standalone delete-data tests passed on SQLite and a fresh PostgreSQL 18.6 database, including stale plan refusal, concurrent same-key confirmation, two-admin race, audit-failure rollback and uncertain effect handling; the full local `internal/app` suite, Linux amd64 test compilation and Linux staticcheck also pass. Native-only run `41715252815` at `e3fb26f4` then passed real managed repair start/probe/registration/activation, a current-viewer backend request, standalone namespace deletion to `deleted` with no owner, managed uninstall and fresh reinstall; the artifact records all three new markers and is in [standalone delete evidence](evidence/platform-standalone-delete-data/acceptance.json). | Broader concurrent operator acceptance across enable, update and repair remains |
| H14 | platform developer | Existing install/admin/app-shell pages provide the lifecycle review and settings surfaces. Disable preserves data; uninstall clears only the selected installation and drops the plugin table only when no non-removed installation remains. Exact reviews cover install, update, enable, disable, uninstall, permanent deletion, repair and checkpoint restore. Review dialogs are viewport-bounded with both footer actions reachable, a keyboard-focusable inner scroll region and high-contrast cancel text. My Apps links administrators to the selected installation, and terminal polling refreshes the release directory once. The native Linux flow now starts the production SPA inside the controller-owned process, confirms repair and restore through the normal management API, executes those exact operations through the signed real-runner path, then opens a second browser session that requires terminal progress and the final Ready settings projection. Scripts, HOME and evidence are staged in the controller-owned child directory. | Browser report binds `ba0f4667`: 30 scenarios, 13 at browser-equivalent 200% zoom, zero serious/critical axe violations, no overflow, clipped tags or dialog clipping, and successful focus restoration. Real-runner evidence binds `ce3e07bd`: restore operation `c1ff46d6-efc4-4628-8518-448214967414` and repair operation `bd984b9b-ebb6-4cf9-9fbd-f644926b7bf4` each moved from browser confirmation to runner completion and Ready. Evidence is in [platform install](evidence/platform-install/acceptance.json) and [lifecycle runner](evidence/platform-lifecycle-runner/acceptance.json). Portal Web passed 270 files / 2844 tests, typecheck, lint and build; local `internal/app` passed in 271.645s. Hosted CI for `ce3e07bd` passed quota/browser `41509323922`, general test `41509323957`, runner daemon `41509323798` and CodeQL `41509313480`. Controller access/HOME failures and the two Cancel contrast failures remain recorded. | Install/update/enable/disable/uninstall, progress, settings, deletion, repair, restore, paused control, enlarged text and real-runner confirmation/progress browser paths are complete. Full neutral-state D5 review, retained-data reinstall, remaining lifecycle pages and a manual screen-reader audit remain open for UX-01..10 |
| H15 | platform developer | Shared legacy/v2 templates, create-app, plugin-standard, strict descriptor/package builder, deterministic ZIP, publisher/descriptor signing verification, contract generation and exact source/test pins are implemented. Managed packages stage through the signed market path, appear as backend releases in the catalogue, and management plan/commit select the managed consumer from canonical plan bytes. The static development `.portalapp` envelope now has a bounded three-member ZIP parser, closed signature syntax, exact descriptor/frontend binding, deterministic Apps packer, local inspector, administrator staging and complete review/activate/launch installation on SQLite and PostgreSQL. Market downloads retain a bounded in-memory prefix within one fetch and retry with Range/If-Range: a valid 206 continues at the exact offset, a 200 safely restarts, redirects preserve the range headers, and final descriptor/signature/frontend digest checks still decide admission. An interrupted managed writable install resumes the same durable operation under a new higher-fenced scheduler and completes real data/start/readiness/registration/activation without duplicating effects. Interrupted fetches leave no staged release; a file-backed SQLite capacity test proves release/frontend/audit rollback when the database cannot grow. Publisher policy revocation or key revocation verifies active releases and atomically queues the existing disable/stop lifecycle while preserving code, runtime, namespace and data. A real disposable managed runner now records a successful stop initiated by package revocation; after trust restoration, managed enable and repair reverify the same signed release under the current trust revision and activate a new generation. The Python package builder consumes the same pinned 56-case descriptor corpus through the TypeScript validator. The official released Portal v2026.43.2 and Status Beta.2/Beta.3 bytes completed an isolated install, upgrade, rollback and uninstall lifecycle with three distinct installations, stale-token denial and retained configured store. | Portal `f314fe4b` and Apps `4c53f10` bind [package validation](evidence/platform-package-validation/acceptance.json), [native package revocation](evidence/platform-package-revocation/acceptance.json), [managed staging restart](evidence/platform-staging-restart/acceptance.json) and [released lifecycle](evidence/platform-released-lifecycle/acceptance.json); the format and commands are in [offline envelope](157-offline-envelope.md). Focused Go suites pass on SQLite and a temporary PostgreSQL 18.6 instance, including Range resume, uninstall isolation and trust-revision recovery; the complete local `internal/app` suite passed in 127.998s. The selected SDK suite passed 140 tests with typecheck, and all 60 Python package/publication tests passed. Status Beta.3 was independently downloaded from GitHub and every SHA-256 entry, including both Linux architectures, macOS, armv7 and Windows assets, matched. Native package revocation passed app-project-quota run `41594944957`; native managed staging restart passed run `41599541528`; runner daemon runs `41594945155` and `41599541499` passed. Bound earlier CI for `1421984d` is Portal general/race `41582763189`, quota/browser `41582763073`, runner daemon `41582763413` and CodeQL `41582758211`; Apps check `41581446614` and `41581440281` pass. | PKG-02 through PKG-07, PKG-10, DEV-05 and the static portion of PKG-01 pass their development matrices. PKG-01 still needs a signed managed OCI image payload and public envelope-format freeze; PKG-08 now has same-fetch HTTP Range resume and native managed staging restart recovery but still lacks an approved persistent cache. DEV-01 still needs a non-implementer walkthrough. Formal release-key/environment approval and final publication remain open |
| H16-H18 | H16 in progress; H17-H18 not started | H16 now has exact development evidence for AUTH-01 through AUTH-08: reader management/commit/API refusal, disjoint-OU directory/report/direct-link isolation, denied optional direct requests, required-capability staging refusal, account/OU lifecycle revalidation, wrong-app/generation/viewer refusal, capability/secret binding revocation during staging and in-flight work, userless background service authority, and update/key-rotation/restore/uninstall identity fencing. SEC-02 proves passive frontend, offline-envelope, command-inspector and local image-metadata inspection with observable no-execution markers. SEC-03 proves exact frame/nonce/channel binding and bounded resize; SEC-04 adds a Chromium attack fixture for DOM, Cookie, storage, top navigation, popup and raw network denial; SEC-05 covers actual-address network policy, rebinding, redirects and post-IO uncertainty; SEC-06/07 pass portable policy plus real disposable Linux Docker/XFS container, mount, socket and identity checks; SEC-08 covers sealed credential metadata, fixed injection, echo refusal, restore floors and uninstall isolation; SEC-10 passes native peer/epoch refusal and inert replay. RUN-03/04/07 prove reference-container isolation, repeated exit without implicit restart, and lost-ack uncertainty; RUN-05 proves an absent runner forces managed disable to a truthful recovery state while health, public site, login, session, home, report and notification queue stay available, and a fresh route stack retains the durable projection; RUN-06 proves real stale/orphan/duplicate resources are observed but not guessed or deleted after a daemon-client restart; RUN-08 exhausts real XFS byte/inode quotas, a 16 MiB tmpfs and bounded json-file logging without host-wide growth. LIFE-01/05 prove server idempotency and one-operation concurrency; LIFE-07 refuses newly required update authority before admission; LIFE-14 proves reviewed clear-on-uninstall followed by a distinct managed installation, fresh namespace, registration, activation and launch. DATA-01/02/03/07 pass their fresh-database SQLite/PostgreSQL development matrices; JOB-07/08 prove central occurrence deduplication and review-required notification restore; OPS-02 proves one-time runner bootstrap/restart plus managed installation through the running service, while OPS-03 has only its first runner-unavailable availability drill. DEV-01 has an automated clean-template build and DEV-04 has release provenance/publication tests; DEV-02 now has one shared AppRoot/useAppBackend failure harness plus the production refusal matrix. SEC-09, the remaining OPS rows, LIFE-02/03/08, DATA-04/05/06/08, JOB-01..06 and the DEV-01/04 gaps retain explicit native, manual, approval, review or measured-capacity gaps. SEC-01 remains partial because Portal has no product registry-authentication path. Full acceptance still requires named owners, capacity measurements, deployed-runner/version matrices, browser/manual review, and native/fault-host coverage; production and legacy adoption remain separate owner decisions. | Portal `58b05232` adds [security package evidence](evidence/platform-security-package/acceptance.json) and [runtime/data matrix](evidence/platform-runtime-data-matrix/acceptance.json); Apps `f87568b`; runner daemon `41636603183` binds stale/orphan/duplicate and peer/epoch results; project quota `41652513493` binds native bounds and managed clear/reinstall; Portal `556d1631` binds [auth matrix](evidence/platform-auth-matrix/acceptance.json), including native artifact `7af3a921905973faed88f30f05f97ddfa57aa62bc4c73bddda848df90e8b8e37`. | Follow `144` H16-H18 with repository-owner assignment and explicit production/migration approvals |

Latest H16 package-security evidence binds Portal `bcb7081e` and
[security package evidence](evidence/platform-security-package/acceptance.json).
On SQLite and a dedicated PostgreSQL 18.6 database, altered descriptors,
signatures, frontend/backend digests, versions and trust revisions were refused
before admission; mixed DNS sets containing any unsafe address never dialed;
redirects revalidated each destination and dropped cookies, authorization and
referer; fixed credential headers were echo-safe; and Docker image references
had to match the exact digest-pinned repository. Passive inspection was tested
with application JavaScript and an image entrypoint that would each create a
marker file: neither marker appeared. A local Chromium fixture extracted the
production document CSP and proved a null-origin child could not read the parent
DOM or storage, read the host cookie, navigate the top frame, open a popup or
issue a raw private fetch. `SEC-02`, `SEC-03`, `SEC-04`, `SEC-05` and `SEC-08`
are `pass-development` for their stated surfaces.
`SEC-01` remains partial only for registry-authentication scope because the
current product has no registry-auth client or token store; local image
inspection never reads registry credentials. The disposable Linux runner run
`41623015662` executed the real closed container policy, repeated exit,
mount-pin substitution, service-profile, controller-identity, successor-epoch
refusal and inert old-intent replay checks. `SEC-06`, `SEC-07` and `SEC-10` are
therefore `pass-development`. `SEC-09` remains partial for measured capacity.

Latest H16 runtime/data evidence binds Portal `8911a9fc` and
[runtime/data matrix](evidence/platform-runtime-data-matrix/acceptance.json).
Separate fresh PostgreSQL 18.6 databases were used for lifecycle concurrency,
schema migration, logical restore and account/OU cleanup; the earlier mixed
groups that produced residual-product or lock failures were discarded rather
than counted. Server idempotency and one-claim concurrency passed for install,
update, uninstall and delete-data. Lost start/stop/checkpoint acknowledgements
became uncertain and were not retried. SQLite and PostgreSQL migration/restore
matrices passed their declared product, rollback and startup-refusal cases.
The runner-unavailable drill passed with an absent managed endpoint on SQLite,
a fresh PostgreSQL 18.6 database and race instrumentation; Portal CI test
`41631852903` includes the SQLite case. It kept health, public site, login,
session, home, report and notification queue available while the affected
managed disable settled at `recovery_required` with a prepared but unconfirmed
stop effect. Re-instantiating the full route stack retained that state.
The update-review boundary now refuses a newly required host capability before
admission, leaving the active A release, generation, policy and grants
unchanged and launchable. The disposable runner daemon run `41636603183` then found a real stale/orphan
container by its private mount, rejected its mismatched policy, detected a
duplicate reference after a fresh client restart, and left both test-owned
resources unchanged instead of guessing ownership. The disposable project
quota run `41636602878` exhausted a real XFS project's byte and inode limits,
verified a healthy neighboring project and remount, filled and recovered a
16 MiB tmpfs, and bounded an 8 MiB json-file flood to the configured rotation.
The managed lifecycle run `41652513493` then completed an exact uninstall,
removed the old namespace and plugin rows, reinstalled the same signed release
under a distinct installation and fresh namespace, and launched the resulting
document after registration and activation.
The native checkpoint path now cross-checks one recovery set against the final
update plan, source activation, namespace/schema, source and target releases,
controller epoch, writer fence, runtime key fingerprint and applied journal
receipt. `DATA-04` is therefore `pass-development` for a quiesced coordinated
data checkpoint. `DATA-05` remains partial until the complete Portal/runner/
data/key set is restored on an isolated fresh host with external effects paused.
`RUN-03`, `RUN-04`, `RUN-05`, `RUN-06`, `RUN-07` and `RUN-08`, `LIFE-01`,
`LIFE-05`, `LIFE-07`, `LIFE-14`, `DATA-01`, `DATA-02`, `DATA-03`, `DATA-04`, `DATA-06`, `DATA-07`, `JOB-07` and `JOB-08`
are `pass-development` for their stated repository surfaces. `OPS-03` remains
partial after its first runner-unavailable drill. Real crash loops,
coordinated live backup/fresh-host recovery, measured capacity and the
unapproved task/event persistence products remain open.

Latest DATA-06 evidence binds Portal `303bd8a8` and native runner daemon
`41726689783`. A runner journal retained an admitted epoch-402 command and then
refused the restored epoch-401 command both directly and over the real Unix
control socket. The newer command replayed to its original effect and state; the
stale command changed nothing. The same native run retained the opposite
direction, where an old runner refused successor authority and kept prior
replay inert. Task replay remains explicitly outside this evidence until H09 is
approved.

Latest H16 developer-harness evidence binds Portal `8911a9fc` and Apps
`f87568b` in [developer harness](evidence/platform-developer-harness/acceptance.json).
A clean temporary directory generated both React templates and completed their
typecheck, unit test and production build without manual source edits. The
full SDK suite passed 304 tests. One AppRoot/useAppBackend harness exercised a
changed-OU refusal, a retired bridge and a missing runtime through the shared
stable error contract. The Apps packaging/publication/catalogue corpus passed
60 Python tests, and release tests bound exact source, CI, targets, checksums,
draft-only publication and redirect authorization. `DEV-02` is therefore
`pass-development`; DEV-01 still needs a non-implementer walkthrough, and
DEV-04 still needs the final dependency/license/SBOM/CVE review and release-key
approval.

Latest H14 evidence binds Portal `14a1a3748c322896e08cdf88afdc0efb75ddf366`,
Apps `baa9a66f2fa1a048e19bbd1f6294e9adcc5fbd23`, quota/browser
`41555201862`, general test `41555201842`, runner daemon `41555201826`
and CodeQL `41555197449`. The real SDK browser matrix now proves UX-02, UX-03,
UX-04, UX-05, UX-06, UX-07 and UX-09. It repeatedly delayed and refreshed
dark/light application assets while sampling the loading cover and frame;
exercised form Tab/Enter, an application dialog with Escape and focus restore;
enforced viewport-only internal scroll and keyboard scroll; rejected malformed
shell metadata and a 100001px document; bounded oscillating height and React's
self-triggering update loop; and kept `sandbox="allow-scripts"`. A deliberate
draft-safe navigation intent lets a successful save return to its app list
without weakening the normal dirty prompt. UX-08 remains partial because the
required human screen-reader audit has not been performed. UX-01 has one real
reference app in the shared shell but still needs the managed Scheduled Digest
reference app and the two-app install/open path; H12 remains gated on the
unapproved H09/D2 persistence design. Normal uninstall behavior follows the
owner decision: clear only the selected installation's plugin data, drop its
dedicated table only after the last live installation is removed, and leave
other plugins untouched.

Latest H13 delete-data evidence: the management plan/commit API names one
installation and one data namespace. Review creation does not mutate authority
or data. Confirmation atomically consumes the plan, advances the installation
control fence, marks the namespace `deleting`, clears its owner and advances the
writer fence. The executor revalidates the exact source receipt and recovery-set
digest at every transition, records one durable signed delete-data effect, and
commits `deleted`, effect receipt and operation completion together. A failed
completion audit rolls the entire transaction back. A prepared effect with no
confirmed receipt becomes `recovery_required` and is not automatically reissued.
Tests also prove that this standalone operation does not clear the dedicated
plugin table; clear-on-uninstall remains a separate reviewed action.
Candidate `602f91fe` passed Portal test `41341861685`, quota/browser
`41341861749`, runner daemon `41341861650` and CodeQL.

Latest H13 concurrency evidence: the enable/repair operator race now uses two
distinct administrator users and sessions rather than one actor issuing both
requests. SQLite, race-enabled SQLite and the complete Portal CI matrix pass.
Exactly one review plan is consumed and one enable/repair operation remains
pending; the other request is refused as stale or conflicting. Concurrent
update paired with enable/repair remains the remaining H13 operator drill.

Latest H13 repair evidence: the reviewed managed repair path now uses its own
review format and `queued -> validate -> download -> stage -> quiesce -> start
-> probe -> activate -> complete` operation. Review requires the current
same-release runtime, policy, grants and audience. Admission moves the
installation to a pending target generation without replacing grants; quiesce
must produce a new verified checkpoint before start. Probe, registration, boot
and activation reuse the signed update-start chain. SQLite and PostgreSQL 18.6
tests cover successful repair, grant drift refusal, healthy-app refusal,
concurrent worker claims, enable/repair operator races, failure to
`recovery_required`, retry after that failure, and backend history. Repeated
enable cycles now reconstruct enable sources instead of assuming install
generation one. The disposable Linux/XFS/Docker native lifecycle then stopped
the enabled generation again, reviewed and executed repair, served a real reader
backend request on the repaired generation, stopped it once more, and completed
standalone `delete_data` for its retained namespace. Candidate `ffdd3b16`
passed quota/browser run `41353916160`.

Latest H14 lifecycle-progress evidence: the management panel now renders a
generic operation progress surface rather than an install-only one. All current
lifecycle stages, including repair/quiesce/snapshot, managed stop/remove-code/
remove-data and restore-data, have zh-CN/en-US/zh-TW labels. A paused controller
is shown as a warning; failed and `recovery_required` operations are errors;
complete is success. Unknown backend error codes use a bounded generic
administrator-review explanation instead of displaying a raw key. Component
tests cover repair recovery and paused `delete_data` progress. The real Chromium
fixture now captures queued, paused and recovery-required operations from
durable backend rows; all three page-level axe scans are clean. Reviewed disable
is a separate path that retains plugin data and access configuration while
retiring live authority, so it cannot be mistaken for uninstall. Candidate
`2693793e` passed general `41402057322`, quota/browser `41402057341` and runner
daemon `41402057334`. The full UX-01..10 visual matrix remains open.

Latest H14 installation-settings evidence: the administrator can open one
installed application from My Apps and see its current release, audience,
capability grants, backend registration state, data usage, recovery state and
credential binding metadata. The projection also carries the exact data
namespace identity. Disabled or removed managed installations can start the
reviewed `delete_data` flow from the page; the confirmation binds the exact
namespace, generation transition, format and recovery scope. The same page
offers a server-derived repair review for a disabled or recovery/review-required
managed installation with an active namespace, enabled runtime and no pending
operation. The repair dialog binds the current release, generation transition,
policy revision, retained data and unchanged grants; the management API repeats
the runtime, trust, authority, compatibility and state checks at plan and commit.
The endpoint is installation-scoped and omits sealed identity material,
controller epoch, runtime fingerprints, policy JSON and credential values. It
returns at most 128 audience principals and 256 capability rows. Audience, grant
and credential writes stay in their reviewed flows. The Chromium matrix renders
the page at 1440px English/light and 320px Simplified Chinese/dark, opens a
retained-data deletion review against a managed runtime, and opens repair
reviews at 1440px English/light and 320px Simplified Chinese/light, with no
overflow, clipped tags, dialog clipping or serious/critical axe violations.
Browser evidence binds `49754cbb`; full local Web passed 270 files / 2842 tests
and `internal/app` passed in 444.062s. Hosted CI passed quota/browser
`41457471636`, general test `41457471642` and runner daemon `41457471418`.

Latest H14 managed-restore evidence: the settings projection now lists up to 16
recovery points with exact recovery-set and checkpoint IDs, release, data
format, snapshot size and verification time. `enabled/recovery_required`
installations can review restore from one verified recovery point; the
destructive dialog binds the recovery set, checkpoint operation/effect, release,
manifest, archive bytes and generation transition. The management plan/commit
route revalidates the failed update, active installation, namespace, writer
fence, runtime, trust, grants and audience. Read-only settings remain available
while application control is paused, with the pause reason shown and restore
eligibility disabled; all mutation admission remains refused. The Chromium
matrix now has thirty scenarios, including thirteen at browser-equivalent 200%
zoom. Dialog bounds include the top, bottom and footer actions; the inner review
scroll region is keyboard focusable. Browser evidence binds `ba0f4667`; local
Web passed 270 files / 2844 tests and `internal/app` passed in 271.645s. Hosted
CI passed quota/browser `41493409034`, general test `41493408915`, runner daemon
`41493408984` and CodeQL `41493401071`. Two intermediate browser attempts failed
on the real Cancel-button contrast assertion; footer-scoped high-contrast
styling fixed the cause and both failures remain recorded.

Latest H14 real-runner evidence: the production SPA now confirms repair and
restore through the normal management HTTP API inside the controller-owned
native process. The signed runner executes each operation; a second browser
session requires terminal progress and final Ready state. Candidate `ce3e07bd`
passed quota/browser `41509323922`, general test `41509323957`, runner daemon
`41509323798` and CodeQL `41509313480`. The evidence report records the exact
operation IDs and screenshots, including the earlier controller access and HOME
isolation failures.

Latest H14 My Apps evidence: v2 directory cards now distinguish pending,
disabled and attention states with separate explanations instead of one generic
"unavailable" label. Administrators get a direct management-page recovery link
only when the app is not openable; ordinary readers receive the truthful state
without installation IDs, counts or an administrative action. The production
SPA failure matrix on candidate `9b605cdb` covers all three non-openable
directory states plus denied launch, stopped backend, missing app-ready and
incompatible protocol. Refused and failed launches unmount executable content,
show only fixed host errors, retain a keyboard-operable Reopen app action with
host-owned deep-blue/white contrast and never expose server diagnostics. The
matrix has zero main-content
serious/critical axe findings or browser errors. Evidence is in
[application failure evidence](evidence/platform-failure/acceptance.json).
Hosted quota/browser `41528975733`, general test `41528975837`, runner daemon
`41528976293` and CodeQL `41528970251` pass. The two intermediate CI contrast
failures remain recorded.

Latest H14 SDK-host evidence: candidate `93542391` changes theme, time zone and
locale while an unsaved app form remains open. The app presentation updates
live, the draft remains, query effects are not replayed and no independent
language picker appears. The same Chromium run verifies registered-route deep
links, browser back/forward, typed-query round trips, dirty-form Stay/Leave and
final unmount. Long document content grows one outer frame without internal
scroll; the registered editor route remains mounted across history traversal.
The quota/browser run is `41537868706` and CodeQL is `41537862896`; evidence is
in [SDK host evidence](evidence/platform-sdk-host/acceptance.json). General
test `41537868765` and runner daemon `41537868781` also pass.

Native managed A-to-B acceptance now passes against the real Docker/XFS runner.
Run `41272158463` completed install, update quiesce/checkpoint/source removal,
generation-2 start/probe/registration/boot/activation, current-generation
launch, directory projection, page binding, current-viewer requests and exact
fixture cleanup. The default browser run `41272707838` repeated that chain with
the real SDK browser and also completed the managed registration/RPC, current
viewer, SDK capability and clear-on-managed-uninstall checks. A temporary
PostgreSQL 18.6 cluster ran the complete `TestPlatform*Postgres` set on a fresh
database, and the full local Go suites plus `go vet ./...` passed. This closes
native A-to-B and the ordinary managed-install activation audit. Namespace
deletion is now also verified end to end: run `41275610320` covered the bounded
XFS namespace/quota primitive, and run `41277140124` completed managed uninstall
with the namespace marked `deleted`. This does not substitute for native restore
acceptance.

Native restore-vs-runner acceptance completed in run `41288794210`. The run
installed the writable app, completed A-to-B, deliberately failed B-to-C after
the target started, created and consumed a reviewed restore, proved target
stop/remove-code, restored the XFS namespace, and activated the restored
generation through restore-start probe, registration and boot. It then
completed managed uninstall and verified namespace deletion and project-quota
clearing. The default browser/SDK run `41289817903` repeated the same chain on
the final exact-code candidate.

Final candidate `5ebb981b` repeated the full local Go suites, `go vet ./...`,
the complete `TestPlatform*Postgres` set on a fresh PostgreSQL 18.6 database,
and the default quota/browser workflow in run `41273768221`. The PR CodeQL check
also passes on that candidate.

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
