# Acceptance specification and evidence register

Status: all cases below are **required, not yet performed for platform v2**.
Existing legacy tests are regression evidence only, not proof that new contracts
exist. Test case IDs are stable specification identifiers; they are not existing
test-function names or executable commands.

## 1. Fixture and evidence setup

Prepare two clean Portal installations: SQLite and dedicated disposable PostgreSQL.
Use the supported Linux rootless/runtime profiles from D1 and record OS, engine,
kernel, architecture and quota support. Use fake email/HTTP/event destinations,
test-only credentials and isolated networks. Never run hostile fixtures or data
deletion drills on production.

Provide accounts: administrator, current reader, disjoint-OU reader, no-app-access
reader, disabled user and deleted/recreated identities. Provide static Notes and
managed Scheduled Digest releases A/B/C, including migration/readiness failures.
Include a malicious package corpus and a destination recording accepted effects.

For each ID record: exact source/released artifact digests, fixture version,
command or UI steps, expected/actual result, safe evidence location, timestamp,
pass/fail and limitation. No keys, passwords, actual balances or production
personal data in screenshots/logs. A test artifact must contain assertions,
not merely screenshots or a green job with unrelated coverage.

## 2. UX and integration

| ID | Steps | Required result |
| --- | --- | --- |
| UX-01 | Install/open both reference apps from My apps | One platform title/navigation; no outer app box, duplicate header or version row |
| UX-02 | Open and refresh repeatedly in light and dark modes with delayed assets/init | No light-content flash in dark mode; bounded loading/error; matching initial tokens |
| UX-03 | Change panel language/theme/timezone with an unsaved form open | Live presentation update, no independent language picker, no draft loss or write replay |
| UX-04 | Navigate an app route, deep link, back and forward | Host URL/history reflects registered routes without document reload |
| UX-05 | Leave a dirty form by host navigation and browser history | Standard bounded prompt; confirm/cancel works; app cannot trap user forever |
| UX-06 | Use 320 px, desktop, long labels and enlarged text | No page-wide horizontal overflow; readable wrapping and reachable controls |
| UX-07 | Open long document content and explicit viewport editor | One outer scroll for documents; intentional internal editor scroll only |
| UX-08 | Keyboard through actions, form, dialog, error and app boundary | Correct labels/order, visible focus, Escape and focus restoration; no inaccessible host prompt |
| UX-09 | Send invalid/huge/oscillating height and shell descriptors | Bounds enforced; no infinite page growth, host HTML injection or unbounded rerenders |
| UX-10 | Deny access, stop backend, omit app-ready and supply incompatible protocol | Safe truthful host states and recovery actions; no hidden content/count leak |

Automated WCAG scans and keyboard/browser checks are required. Human browser
review verifies the visible flow; report scan scope accurately and do not claim
complete screen-reader certification from an automated result.

## 3. Packages, publication and trust

| ID | Steps | Required result |
| --- | --- | --- |
| PKG-01 | Install static and managed packages by market and offline envelope | Same validator, digests, permission review and resulting code |
| PKG-02 | Change frontend, backend image/platform digest or descriptor after signing | Refused before code execution or live replacement |
| PKG-03 | Use wrong publisher key/namespace, unknown algorithm or revoked key | Refused with safe reason; changing catalogue does not establish trust |
| PKG-04 | Supply traversal, symlink/device, duplicate path/key, ZIP bomb and oversized corpus | Bounded refusal; no files outside staging or lost current installation |
| PKG-05 | Use unsupported platform/API/runner/schema or missing architecture | Compatibility refusal before install commit |
| PKG-06 | Change catalogue/package/policy while permission review is open | Old plan conflicts; new exact plan must be reviewed |
| PKG-07 | Reuse an application version with different executable bytes | Publication/installation refuses mutable identity; old assets retained |
| PKG-08 | Interrupt/resume download, corrupt cache and exhaust staging disk | Digest revalidation, bounded cleanup, no partial activation |
| PKG-09 | Publish actual release, download externally and inspect both image architectures | Every public artifact matches descriptor/source/released executable identity |
| PKG-10 | Revoke a running package; make trust source temporarily unreachable | Defined quarantine/deny-new-install behavior; no trust bypass or silent data deletion |

## 4. Authorization and isolation

| ID | Steps | Required result |
| --- | --- | --- |
| AUTH-01 | Reader commits install/manage/update request | Server refuses regardless of frontend controls |
| AUTH-02 | Grant app-use to one OU; enumerate as disjoint/no-access readers | Lists, counts, routes, details and deep links reveal no hidden app data |
| AUTH-03 | Deny optional grant, then call its backend/platform operation directly | Feature disabled and server refuses; required refusal cancels install |
| AUTH-04 | Move/demote/expire/disable/delete/recreate account or OU | Next admitted request uses current identity; no name/ID-based grant resurrection |
| AUTH-05 | Call as wrong app, wrong generation, machine token or forged viewer | Refused before platform/business data read or side effect |
| AUTH-06 | Remove capability/secret binding during queued/in-flight work | New admission denied; stale commit fenced; ambiguous external effects classified |
| AUTH-07 | Run background handler without browser and request arbitrary user identity | Only explicit service authority applies; no administrator impersonation |
| AUTH-08 | Update normally, rotate signing key, uninstall/reinstall and restore | Approved identity continuity only; old generations/epochs revoked and restored grants reviewed |
| SEC-01 | Tamper trust/descriptor path, mixed DNS answers, redirects and registry auth scope | No credential leak, unchecked destination or unreviewed executable |
| SEC-02 | Inspect package without installing | No executable hooks, app code or image entrypoint runs |
| SEC-03 | Forge parent/sibling messages, old document channel and replayed init/resize | Exact frame/channel/nonces enforced; stale document cannot control host |
| SEC-04 | Attempt host DOM/cookie/storage, top navigation, popup and raw frontend network | Sandbox/CSP refuse; bearer/signing secrets never reach child |
| SEC-05 | Attempt host network, metadata IP, rebinding, undeclared origin/port or arbitrary stream | Runtime and broker deny at actual connection; direct egress cannot bypass policy |
| SEC-06 | Request privileged mode, socket/host/device mounts and arbitrary runner command | Package/control validation refuses; app cannot access control socket |
| SEC-07 | Substitute Unix socket path, symlink, ownership label or app data mount | No cross-installation/host access; uncertain resource quarantined |
| SEC-08 | Read credential metadata, logs, export, errors, backup evidence and browser payload | Secret values absent; authorized use separately audited |
| SEC-09 | Flood API/messages/tasks/logs/memory/PIDs/storage | Per-app/global bounds enforced; Portal business executor capacity remains available |
| SEC-10 | Hijack control transport or replay command from previous deployment/restore epoch | Peer/deployment/epoch authentication fails; no Docker action admitted |

## 5. Runtime and lifecycle

| ID | Steps | Required result |
| --- | --- | --- |
| RUN-01 | Install static app without configured runner | Works; no fake runtime prerequisite |
| RUN-02 | Install managed app without compatible runner/quota support | Clear preflight refusal; no half-installed code |
| RUN-03 | Inspect actual reference container and probe isolation | Required user, rootfs, capabilities, limits, no host ports/network/socket access |
| RUN-04 | Kill app repeatedly and trigger readiness timeout | Bounded restart/circuit breaker and safe health state |
| RUN-05 | Stop runner, Portal or app independently | Defined availability and bounded authority leases; unrelated Portal routes remain healthy |
| RUN-06 | Restart runner with orphan/stale/duplicate resources | Journal reconciliation fences stale writers; no guessed ownership deletion |
| RUN-07 | Lose runtime acknowledgement after successful start/stop/snapshot | Same operation result recovered without duplicate effects |
| RUN-08 | Fill disk or exceed data/log/temp quota | Safe refusal/cleanup and clear diagnosis; no host-wide uncontrolled growth |
| LIFE-01 | Double-click install, retry POST and reload progress page | One operation/installation; same key/different payload conflicts |
| LIFE-02 | Install managed app from a clean market | Automatically provisioned data/backend/pairing; no manual SSH/key/address step |
| LIFE-03 | Fail download/verification/readiness at each install stage | No user traffic to failed candidate; recoverable bounded residue |
| LIFE-04 | Install app requiring business configuration | Administrator setup works; ordinary users see truthful unavailable/setup state; no unconfigured jobs |
| LIFE-05 | Concurrent install/update/restore/uninstall from two clients | One admitted operation per installation; deterministic conflict/observation |
| LIFE-06 | Update A to compatible B with active configuration/grants | Data/installation identity retained; complete release tuple activates; old calls fenced |
| LIFE-07 | Refuse newly required update permission | A remains usable; no partial privilege change |
| LIFE-08 | Fail explicit migration midway or lose its acknowledgement | No blind rerun or old-code start against unproven schema; recovery choice shown |
| LIFE-09 | Fail B readiness after data migration | Compatible rollback or verified snapshot restoration; no false ready |
| LIFE-10 | Roll back after B has accepted new writes | Warn about restore data-loss boundary; controlled maintenance and verified recovery, never silent loss |
| LIFE-11 | Disable during active handler and while runner unavailable | Broker denial immediate; shutdown pending truthful; no new claims |
| LIFE-12 | Enable or repair disabled app | Same trusted release/config, health checked; no new grants or hidden schema change |
| LIFE-13 | Uninstall and retain data | No runnable code, schedules, subscriptions, publication or secret use; retained data inspectable by admin |
| LIFE-14 | Reinstall using retained data | Explicit compatibility/identity review; no automatic grants from old namespace |
| LIFE-15 | Delete retained data with concurrent/stale request | Current confirmation/revision required; exact data scope cleaned; recovery copies accounted for |

## 6. Data, jobs and operations

| ID | Steps | Required result |
| --- | --- | --- |
| DATA-01 | Apply approved migration to both Portal drivers | Same products/constraints; frozen baseline unchanged |
| DATA-02 | Fail/interleave migration or corrupt its declared product/ledger | Transactional failure or startup refusal; no false ledger success |
| DATA-03 | Restore old backup, new backup and a missing-product/newer-schema backup | Documented compatible initialization or safe refusal before destructive effects |
| DATA-04 | Make consistent coordinated backup while app has writes | Proven writer fence/checkpoint; data/config/code/key association recorded |
| DATA-05 | Restore recovery set on isolated fresh host and decrypt fixture secret | Identity/config/data match; health works with all external effects paused |
| DATA-06 | Restore Portal control plane while runner still has newer state | Old epoch refused; no stale activation, task replay or identity resurrection |
| DATA-07 | Remove key material or mix checkpoint/schema/key generations | Explicit unrecoverable/refused state, no fabricated successful restore |
| DATA-08 | Exercise retention and account/OU deletion with pending/uncertain work | Policy-compliant cleanup; required recovery/audit evidence retained without excess payload |
| JOB-01 | Close browser with scheduled work active | Handler continues under explicit background authority |
| JOB-02 | Crash worker before/after external effect and before acknowledgement | Lease recovery and stable idempotency; no exactly-once claim |
| JOB-03 | Saturate one app's schedules/events | Fair/bounded admission, no analysis queue or authentication mail starvation |
| JOB-04 | Revoke/disable/update app while jobs/events are claimed | Obsolete generation cannot acknowledge/commit; no new claims |
| JOB-05 | Deliver duplicate/out-of-order event with lost acknowledgement | Registered handler deduplicates and respects sequence contract |
| JOB-06 | Subscribe beyond event/resource authority | Denied without counts/payload leak |
| JOB-07 | Publish repeated app notification with same occurrence ID | Existing consolidated notification service deduplicates; recipients remain centrally authorized |
| JOB-08 | Restore possibly delivered work | Review-required, publication/subscriptions paused, no automatic paid/message replay |
| OPS-01 | Interrupt every durable lifecycle stage then restart both sides | Proven reconciliation or review-required state; no dual writer |
| OPS-02 | Perform supported runtime bootstrap using only the handbook | One-time setup succeeds; app installation requires no server command |
| OPS-03 | Run all app failure drills while checking Portal home/login/report/queue | Existing business behavior/availability remains within documented bounds |
| OPS-04 | Inspect progress, errors, logs and diagnostics as reader/admin | Least necessary data, bounded retention and actionable safe reasons |
| OPS-05 | Deploy released platform through existing beta configuration | Server version reference unchanged; actual loaded digest/version verified |
| OPS-06 | Restore protected recovery set after failed platform rollout | Portal/runtime compatibility and key recovery verified before activation |
| OPS-07 | Present unsupported legacy app/container for managed adoption | No automatic takeover; old installation continues under legacy rules |
| OPS-08 | Repeat test at declared maximum apps/tasks/resource envelope | Measured capacity/latency recorded, quotas reject excess and reserve host headroom |

## 7. Developer and delivery verification

| ID | Steps | Required result |
| --- | --- | --- |
| DEV-01 | New developer follows generator/tutorial from clean checkout | Builds/tests a complete static and managed sample without hidden manual edits |
| DEV-02 | Simulate denied grants, changed OU, expired bridge and missing runtime | Development harness matches production contract and safe error states |
| DEV-03 | Compare Go/TypeScript/package validator corpus | Same acceptance/rejection and protocol error codes |
| DEV-04 | Review declared dependencies/source provenance and publication identity | Reproducible identity and verified artifact relationships; signing isolated from PR code |
| DEV-05 | Install actual public sample release, update, disable, uninstall and recover | Released-byte evidence, no development-only installer or database mutation shortcut |

## 8. Final release gate

Required evidence bundle:

- Accepted decisions D1..D5 and physical schema approval reference.
- Complete matrix above, with exact fixture and supported-host coverage.
- Required Portal and Apps CI on the exact release source.
- Public descriptor/signature/artifact re-verification and both image architectures.
- A tutorial run by a developer using the handbook, with missing steps corrected.
- Independent failure/recovery and old/new version compatibility drills.
- App shell browser acceptance for reader/admin, both themes and all locales.
- Privacy/privilege/network/trust review and documented supported limits.
- Staging then explicitly authorized production rollout with protected backups.

Any required failure is a no-go. A consciously deferred feature must be removed
from the first-release promise through an explicit scope decision, not reclassified
after a test fails. Status configuration and older published app evidence cannot
substitute for platform-v2 release acceptance.
