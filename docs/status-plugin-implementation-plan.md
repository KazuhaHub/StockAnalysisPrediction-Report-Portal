# Status application: implementation and execution plan

Status: implementation in progress. The shared public application repository and
protocol/catalogue bootstrap exist; Status runtime, schemas, monitors and deployment
are not yet implemented. See the execution log and storage approval gate.

Prepared: 2026-10-21. Baseline: notification consolidation PR #141, commit
`b4741355118c28d95dcf3c096115963ce5936f01`. Verify that PR's merge and deployed
version before relying on its notification endpoint. The existing extension
platform alone does not yet implement every capability proposed below.

## 1. Locate the software and distribution repositories

| Item | Verified location |
| --- | --- |
| Portal source | https://github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal |
| Default application catalogue | `apps/index.json` in that repository |
| Default catalogue URL | https://raw.githubusercontent.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/main/apps/index.json |
| Application authoring contract | `apps/README.md` and ADRs 0002/0003 |
| Market configuration reader | `internal/app/apps_market.go`, `appMarketIndexURL()` |
| Installed application view | `web/src/pages/AppView.tsx`, `/apps/x/:id` |
| Local Portal checkout | `/Users/kazuha/Codes/StockAnalysisPrediction-Report-Portal` |

The default catalogue currently lists `demo-symbols`. Status can be the first
production monitoring application, rather than literally the first catalogue
entry. Built-in batch and quote applications already exist separately.

The verified default market is part of the Portal repository. The deployment can
have a different DB `app_market_index_url` override; inspect the existing market
settings UI to determine the production value without opening a database or
copying its secrets. No dedicated market repository was identified in the
accessible organization inventory. Do not claim an invented repository exists.

Direction confirmed with the owner: one application-market monorepo contains all
installable applications, rather than a dedicated source repository per plugin.
Created public repository: `KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps`. Status is planned as its first
production application, including its independently deployed service. The Portal
repository continues to own only the generic host platform and built-in business
applications; it must not vendor Status source into the host.

The new repository owns its root `index.json`, app source trees, shared developer
tooling, package validation and per-app releases. Each app has its own version,
ZIP and optional service image. A change to one app does not release every app.
The Status service/database remains operationally independent even though its
source shares a repository with other applications. Repository name and public visibility were confirmed by the owner; the bootstrap
repository was created on 2026-10-22. Production catalogue migration remains pending.

## 2. Product outcome and firm boundaries

Deliver a continuous monitoring service, an installable sandboxed Portal app, and
an optional externally accessible status website. The service checks Dify,
PostgreSQL, DeepSeek and Qwen/Alibaba Cloud accounts; maintains component status,
incidents and history; and publishes state transitions through the consolidated
Portal notification service. More than one provider/account is supported from
the first data model. DeepSeek and Qwen are both first-release acceptance targets.

Installation, upgrade, disablement and uninstall must not inject scripts into
Portal pages, alter run queue behavior, rewrite business code or read Portal
credentials. The run-analysis availability hint is outside this release. A
future generic host display slot requires its own platform design; Status must
not patch that page or alter submission eligibility to display a message.

The Portal app ZIP contains only public frontend assets and a manifest. A ZIP
cannot run a scheduler, open a PostgreSQL connection or safely hold a provider
key. Closing the tab must not stop monitoring. A separately deployed service is
therefore a real prerequisite, not an installation detail that can be hidden.

All developer-facing source, comments, diagnostics and repository documentation
are English. Product UI uses locale keys for English, Simplified Chinese and
Traditional Chinese. Do not put feature settings in Portal `config.yaml`.

## 3. Existing capabilities and explicit gaps

Read before implementation: ADRs 0002, 0003, 0022, 0023, 0034, 0035 and notification
ADR 0038; `apps/README.md`; `web/src/lib/appBridge.ts`; `AppView.tsx`;
`internal/app/apps_api.go`; and the notification API/runbook documents from #141.

| Capability | Current evidence | Required treatment |
| --- | --- | --- |
| Install ZIP and sandbox app | Existing parser, registry, iframe and theme messages | Reuse without changing isolation |
| Bridge-mediated v1 calls | Host retains short-lived token | Reuse and extend only with a generic, reviewed capability |
| External source notices | PR #141: `POST /api/v1/notifications` with dedicated scope | Reuse after merge/deployment; no second Email/Slack sender |
| Continuous checks | Not supplied by static apps | External Status service |
| Current viewer/OU in an external service | No suitable viewer-bound extension API | Separate platform prerequisite; never trust browser-supplied username/OU |
| Private Status reads | Existing `query` tokens are not a Status-specific viewer identity | Do not use broad `query` as an authorization shortcut |
| Custom `/status` route | ZIP installation does not register SPA/server routes | Deployment gateway rule; define private/public modes explicitly |
| Public-safe projection | Not supplied by the app market | Implement in Status backend with separate DTOs and queries |
| Independent app disablement | App uninstall alone does not stop external processes | Service lifecycle controls, credential revocation and documented cleanup |

Platform changes belong in their own prerequisite PR and must be reusable by
other external apps. They must contain no Dify, balance or Status-specific probe
logic. After that platform version is deployed, installing Status needs no
Portal business-code changes. This plan does not claim zero platform development.

## 4. Proposed deployment and trust boundaries

```text
Portal browser session
    -> trusted app host
    -> existing sandboxed Status frontend (no keys/cookies/host DOM)
    -> new generic, viewer-bound extension bridge/proxy
    -> authenticated Status service
          -> bounded probe scheduler
          -> independent Status database
          -> Dify / PostgreSQL / provider read endpoints
          -> durable notification outbox
          -> Portal notification publication API
                -> existing Email / Slack / signed Webhook / inbox

Optional public browser
    -> deployment gateway /status
    -> Status public projection only
```

Implement the service in Go and the frontend in TypeScript using the established
project conventions. Use an independently versioned image/binary and a mounted
SQLite volume for the first single-instance service. Do not mount `portal.db`,
reuse the Portal's database login or copy its keyring into the service. A dedicated
PostgreSQL storage backend or active-active service is a later feature; PostgreSQL
monitoring does not require PostgreSQL as Status's own storage engine.

### 4.1 Generic platform prerequisite

Proposed contract, not an existing endpoint:

- A registered installed app may have one administrator-approved backend
  connection, server-side secret and fixed route/method allowlist. The browser
  cannot provide a host, full URL, authorization header or arbitrary proxy path.
- Capabilities such as `extension.read` and `extension.manage` are scoped to that
  installed app. They do not imply report `query`/`ingest` or notification `notify`.
- The host validates iframe window identity and the app-bound ephemeral token;
  the server repeats installation, scope and current viewer checks. A token for
  app A cannot address app B's backend. Management requires current administrator
  authority; its exact UI entry must remain inaccessible to ordinary viewers.
- On every private call the Portal derives viewer facts from the current session
  identity: immutable account identity/generation, current session revision,
  active/expiry state, current role and current OU ancestry identifiers. No
  client-provided role or OU is authoritative.
- The Portal authenticates a short-lived server-to-server assertion containing
  issuer, backend/app audience, request ID, issued/expiry instants and those viewer
  facts. Bind it to the request method, normalized path and body digest. Never
  send the connection secret/assertion to the iframe. Verification fails closed
  on wrong audience, time, signature, installation or replay. Pin this contract
  and key rotation behavior in platform tests before building Status against it.
- The proxy exposes only configured relative routes, bounds body/response sizes,
  rejects redirects and limits timeout/concurrency. Strip upstream cookies and
  unapproved response headers. No arbitrary host override, DNS rebinding or
  credential forwarding across redirects. Status backend is an administrator
  approved private service; a private-network exception is specific, explicit and
  connection-bound, not a global SSRF bypass.
- Current membership is checked per request, not only when the app opened. Never
  use a process-lifetime copy of a user's OU. Uninstall, connection disablement,
  account deletion/recreation, expiry and session invalidation revoke access.
- Browser-facing private responses are `Cache-Control: no-store`. Private exports,
  incident detail, counts, charts and search use the same authorization path.

Do not implement this by calling `/api/admin/*` from the iframe or by returning an
admin/machine token to frontend code. Existing unrestricted app query access does
not establish safe per-viewer Status access.

The proposed connection storage is structured per-app data and sealed secrets.
Before writing any Portal DDL, propose its exact schema and restore/deletion rules
and obtain owner approval under AGENTS.md. Notification Option A approval does
not approve this new schema. Do not hide relational records in `meta` to bypass
that decision. Allocate ADR/migration numbers from the actual merged tree: the
forecast planning documents also reserve future numbers, so never copy `0007` or
`0038` from a plan into a second implementation.

### 4.2 `/status` access modes

Choose and document one gateway mode per deployment:

1. **Private by default:** a gateway redirect from `/status` to the installed
   `/apps/x/status` page, using the Portal's normal login and viewer-bound bridge.
   This preserves Portal OU authority without sharing its session with Status.
2. **Optional public:** the gateway proxies `/status` and its explicitly listed
   assets/public API paths to the Status service's public website. The installed
   private app remains `/apps/x/status`. Public endpoints do not inspect Portal
   cookies and never expose internal snapshots or management routes.

Prefix-aware assets and APIs must work under `/status/`, not just at domain root.
A redirect is deliberately not a claim that the ZIP registered a native route.
A public page is explicitly enabled and only shows individually approved public
components. No public component is the default. Avoid weakening Portal CSP,
adding `allow-same-origin` or using broad cross-origin `Origin: null` CORS rules.
A down Status service can still make its own website unavailable; true independent
availability monitoring requires an external vantage point, documented as such.

## 5. Authorization and visibility

Status administrators manage monitor configuration and visibility. Ordinary users
only read components permitted by server-verified Portal viewer facts.

Each component has an internal ID, a separate public label, a visibility policy
and optional exact-balance permission. Default visibility is administrator-only;
public visibility and OU grants are explicit. OU descendants follow the Portal's
current ancestry. Moving an account/OU takes effect on the next authorized read.
Missing/stale identity information denies private data, never falls back to all.

Keep three distinct projections:

| Projection | May expose | Must exclude |
| --- | --- | --- |
| Public | Approved label, coarse state, safe incident text, freshness, approved aggregate history | Keys, hostnames/IPs, DB names/DSNs, provider account IDs, amounts, OU identifiers, detailed errors |
| OU viewer | Authorized components, safe state/incident/history | Other OUs, credentials and financial details without separate grant |
| Authorized operator | Allowed configuration metadata, private diagnosis and optional exact balances | Plaintext saved secrets in reads, exports, logs or browser storage |

Apply filtering in SQL/service queries before aggregation and pagination. Counts,
uptime percentages, search, incident IDs and charts must not reveal hidden
components. Detail endpoints for unauthorized IDs return a nondisclosing result.
No shared response cache may mix public/private or different viewers.

Internal incidents have independent public summaries. Never sanitize a private
incident by simply hiding a field in the frontend. Financial information remains
private by default even when a component's coarse service status is public.

## 6. First-release monitors and capability semantics

| Monitor | Non-destructive check | What a success proves |
| --- | --- | --- |
| Portal availability | Configured public readiness endpoint from external process | HTTP service/readiness from this observer, not all portal features |
| Dify | Version/deployment-specific documented health or authenticated read probe | Reachability or authentication at the selected check depth |
| PostgreSQL | Dedicated least-privilege connection, bounded `SELECT 1` | Connection/authentication/read-query success, not general database health |
| DeepSeek account | Official authenticated `GET /user/balance` | Reported account credit state at the sample instant |
| Qwen/Alibaba account | Official billing API with separate minimal RAM credentials where supported | Cloud-account financial state, not the balance of a DashScope API key |
| Generic HTTP | Fixed approved URL, status/body condition and latency | Exactly the configured HTTP condition |
| Heartbeat | Authenticated caller sends a bounded heartbeat; missed deadline | Whether the registered external job reported recently |

Support multiple Dify instances, PostgreSQL servers, providers and accounts.
Deduplicate shared financial accounts: several keys backed by one billing account
must not appear as independent credit pools. A key-authentication check and a
billing-account check are distinct monitors that can have different outcomes.

Never run a paid workflow, generate a report, call a billable model or write to a
database as a default health check. An explicit optional synthetic canary needs a
separate cost policy and acceptance; it is not part of the first release.

### 6.1 DeepSeek

Use the documented read endpoint and treat `is_available` and decimal-string
balance records as separate fields. Preserve currency and reported grant/top-up
composition when authorized. Parse exact decimal values; do not compare money
through floating-point equality, sum unrelated currencies or label an account
balance as a per-key quota. Authentication errors/timeouts/malformed responses
produce unknown/configuration failure, never zero credit.

Reference checked 2026-10-21:
https://api-docs.deepseek.com/api/get-user-balance/

### 6.2 Qwen / Alibaba Cloud

Do not invent a DashScope `/balance` endpoint or assume a model API key authorizes
billing access. `QueryAccountBalance` is an Alibaba Cloud billing operation with
its own RAM authorization. Confirm account region/site, financial account identity,
endpoint and the exact minimal permission against the official SDK/docs using a
non-production credential before enabling it. Do not request a main-account key.

Keep cash, available credit, free model quota and resource-package remaining units
as distinct capabilities and units. A cloud-account cash balance cannot prove
whether a particular Qwen model can run, or whether a free quota is exhausted.
If the configured credential/API does not support a requested measurement, return
`unsupported` with a clear setup explanation. A manual value is allowed only as
`manual` with its own observation/expiry, never presented as a fresh API result.

First-release Qwen acceptance includes a real adapter/fixture for the verified
account-balance capability or a clearly unsupported state for a deployment that
cannot supply billing access. Token/model quota must not be falsely marked done
by displaying account cash instead. The investigation gate records which actual
capability is available before its implementation begins.

References checked 2026-10-21:

- https://help.aliyun.com/en/user-center/developer-reference/api-bssopenapi-2017-12-14-queryaccountbalance
- https://help.aliyun.com/zh/model-studio/model-usage-statistics
- https://help.aliyun.com/zh/model-studio/new-free-quota

### 6.3 Additional signals with defined priority

P1 after the first end-to-end release: SMTP connection/TLS readiness, signed
Webhook receiver/heartbeat freshness, certificate expiry, scheduled-task missed
heartbeat, backup-age heartbeat and maintenance notices. Do not equate an SMTP
handshake with inbox delivery or a Slack 2xx with human notification receipt.

P2: Redis/object-storage checks, disk/resource telemetry supplied by a separately
installed exporter, vendor status feeds and more providers. Do not mount host
Docker sockets, run arbitrary shell commands or add unrestricted SQL/JavaScript
probe editors to implement these features.

## 7. State, time and history contracts

Separate monitor configuration, observed result and incident lifecycle.

- Check outcomes: `success`, `failed`, `unknown`, `unsupported`.
- Display states: `operational`, `degraded`, `outage`, `unknown`, `maintenance`,
  `paused`. `stale` is an explicit freshness property, not success.
- Measurements include `observed_at`, `last_success_at`, `next_check_at`, timeout,
  latency, capability, value/unit/currency where applicable and a bounded reason
  code. All wire instants are UTC RFC3339; business/display timezone is explicit.
- Suggested defaults: HTTP/Dify/PostgreSQL every 60 seconds; balances every 5
  minutes; jitter 10%; HTTP 5-second timeout, DB 5 seconds, provider 10 seconds.
  Clamp editable intervals to 30 seconds–24 hours and timeouts to 1–15 seconds.
- A sample becomes stale after `max(3 * interval, 2 * timeout)`. Show its last
  observation, not a current green badge. Paused checks never renew freshness.
- Ordinary availability opens an incident after three consecutive valid failures
  and recovers after two successes. Confirmed exhausted credit may open
  immediately; low credit uses two samples plus an operator-defined threshold.
  Use a separate recovery threshold above the low-credit threshold to prevent
  flapping. Defaults for currency amounts require operator setup, not invention.
- Unknown/unsupported results cannot close an outage or prove recovery. They can
  open a separate stale/observer incident after its own threshold. Startup with
  no result shows unknown. First observation never generates a recovery notice.
- Maintenance suppresses routine alerts, does not falsify samples, and creates a
  visible interval. On exit, require fresh results before declaring recovery.
- Version first-release uptime denominator explicitly: eligible fresh observed
  time excluding maintenance/paused periods; unavailable observation time is
  shown separately as unknown coverage. Do not silently count unknown time as
  uptime or invent history before installation. Clip partial boundary intervals.
- Retain raw results for 30 days and hourly aggregates/incidents for 90 days as
  proposed defaults; configurable bounded cleanup protects active incidents and
  undelivered outbox records. These are Status storage policies, independent of
  Portal notification retention.

Bound the scheduler: four workers, one in-flight check per monitor, one provider
request per credential at a time, bounded due work and no catch-up storm after
restart. Persist ownership/lease state; expired in-flight checks become unknown
observations, not synthetic success. Start with one service replica and enforce
that contract; a second replica must fail startup rather than double-probe.

## 8. Configuration, storage and secret handling

Proposed Status entities: monitors, sealed credentials, visibility grants,
observations, state cursors, incidents/transitions, maintenance windows, hourly
aggregates and notification outbox. Before writing DDL, present exact fields,
keys, indexes, retention, transactional boundaries and restore behavior for owner
approval. This plan is not schema approval. The separate Status database has its
own migration ledger and version; it does not use Portal migration IDs.

Monitor settings and thresholds are managed in the Status administration UI/API.
Only bootstrap infrastructure (listen address, Status DSN and secret/key source)
belongs in service startup configuration. Store provider secrets sealed at rest;
rotation preserves metadata and clears readiness until tested. Read/export APIs
return presence/fingerprint only. Restrict files/volumes and backup permissions.
Never read provider secrets from the Portal's database or return them via bridge.

Protect outbound probes against SSRF/DNS rebinding and redirects. Private Dify/DB
networks need explicit administrator-configured host/CIDR/port allowances, checked
at connection time; deny loopback/link-local/metadata by default. Restrict provider
hosts to verified endpoints, require TLS verification, cap body size, and forbid
userinfo/fragment/secret query credentials. Use secret references instead.

Validate monitor configuration and record a revision. A check captured before a
credential/target change cannot publish a result under the new revision. Audit
configuration, visibility, maintenance, secret rotation, pause/resume and public
publication using actor identity and bounded metadata, not values/secrets.

## 9. Incident notifications and existing API limits

Do not build new SMTP, Slack or raw Webhook senders in Status. A committed state
transition and notification intent are one transaction in its own database.
A bounded outbox sender calls the Portal with stable occurrence identity:
`status:<component-id>:<incident-id>:<transition-sequence>`. Persist the serialized
message once; retries reuse identical occurrence ID and bytes/fields. A timeout
retries the same occurrence; a 409 is an operator conflict, not a new random ID.
On startup drain only due outbox entries with bounded backoff. Restore starts
outbox publication paused for deliberate reconciliation.

The current publication API has an important limit: it accepts source-owned plain
text but no component resource or recipient selector. All events from one source
share one event type. It therefore cannot automatically intersect a private
component ACL with arbitrary Portal notification rules.

For first release:

1. Use a distinct opaque service publisher identity per independently routed
   component/security audience, such as `notify:status-<component-id>`. Store its
   token only in the service. Do not use one universal Status publisher to fan
   private events to every user's rule. Multiple probes belonging to one shared
   billing account use that account's component identity.
2. Configure the matching exact event selector in Portal notification rules with
   deliberate operator/OU recipients. Document that Status read grants and Portal
   notification grants are separate administrator policies; neither automatically
   synchronizes the other. Revoke/update both when changing notification access.
3. Send only approved safe alert text: coarse state, safe label and transition.
   Exclude amounts, private URLs, account identifiers and diagnoses by default.
   Private detail stays behind the authorized Status UI. No global rule is created
   automatically. Verify two disjoint OUs and two component publishers in tests.
4. Initially send private operational alerts to explicitly designated operators.
   General user notices require reviewed per-component routing. If fully automatic
   ACL intersection/revocation is required, stop that expansion and design a generic
   external-resource authorization contract in a separate platform PR; do not
   imply the existing endpoint supplies it.
5. Stay within the existing 120-new-occurrences/hour/source and shared delivery
   bounds. Coalesce flapping transitions and emit at most five state-change notices
   per component/hour by default; a visible suppression record explains the gap.
   Recovery and incident summary remain available in Status history.

A 200 from Portal means durable capture, not recipient acceptance. If Status has
no notification credential/rule it continues monitoring and shows notification
setup as incomplete. Portal notification failure never blocks probe completion.
Existing Portal templates and n8n recipes are reused; no new retry layer around
actual Email/Slack delivery is added to Status.

## 10. API and UI deliverables

Proposed service-private API groups (all names versioned before implementation):

- Authorized component list/detail, current snapshot and bounded history.
- Authorized incident list/detail with public/private narrative separation.
- Administrator monitor/secret references, grants, thresholds and test-check.
- Administrator maintenance, pause/resume and retention policy.
- Administrator notification setup/outbox diagnostics and deliberate reconciliation.
- Separate public component/incident/summary reads returning public DTOs only.
- Process `/healthz` and dependency `/readyz`, containing no configuration values.

Bound pagination to 50 items/default and 100/max, history windows to 90 days,
response/body sizes, public request rate, export size and administrator explicit
test concurrency. UI uses visible-tab polling, backend snapshots and freshness;
loading/error/unsupported must not render green. Provide keyboard operation,
contrast, full labels, desktop/mobile layout, light/dark and all three locales.
Do not expose raw upstream response bodies to end users.

## 11. Ordered work packages and exact exit gates

Every package starts with tests for its contract and ends with a small reviewable
change. Do not start a dependent package while its prerequisite decision is open.
No package is completed merely by writing this plan.

| Package | Deliverables | Exit gate |
| --- | --- | --- |
| S00 inventory | Market/default/override check, notification merge/deployment check, extension capability matrix, Dify check endpoint and Qwen account capability investigation | Evidence and unresolved decisions recorded; no real keys in artifacts |
| S01 platform contract | Generic app-bound backend proxy, viewer assertion, OU authority, route/method allowlist, scoped permission UI, restore/revocation design | Exact Portal storage proposal approved before DDL; capability/negative tests defined; separate platform PR approved |
| S02 service/storage contract | Shared application repository bootstrap, isolated Status package/service, configuration spec, approved Status storage layout, incident/outbox transaction design | Owner approves schema and key/backup semantics; no Portal DB dependency |
| S03 authorization skeleton | Service startup, own storage, authenticated private API, OU filters, public-safe DTO boundary | Two disjoint OUs, account recreation, stale assertion, wrong audience/app and count/detail leak tests pass |
| S04 scheduler/probes | Bounded scheduler, fake clock, HTTP/Dify/DB checks, exact revision fencing and restart behavior | No overlap, bounded timeouts, no paid/write action, SSRF/rebinding/redirect tests pass |
| S05 both provider families | Multi-account registry, DeepSeek adapter, verified Qwen billing capability, unsupported/manual states, exact-decimal/currency behavior | Multi-provider/multi-account fixtures and non-production authorized checks; no fabricated per-key balance |
| S06 state/incidents | Thresholds, hysteresis, stale/maintenance/paused behavior, uptime/coverage and bounded history/retention | Deterministic transition and restart tests; no recovery from unknown; no fake history |
| S07 notification adoption | Durable transition outbox, per-component publication identities, Portal exact-event rules and operator setup walkthrough | Restart/timeout dedup, changed-content 409, quota/backoff, disjoint OU routing and amount-redaction tests pass |
| S08 installable app | Self-contained ZIP, manifest, new generic bridge capabilities, admin/viewer screens, themes/locales | Installs through existing preview/parser; token never reaches iframe; readonly user cannot configure; closing tab leaves probes running |
| S09 `/status` website | Private gateway redirect recipe plus optional public projection and prefix-safe assets | Private mode uses normal login; anonymous public APIs expose only approved components and contain no private metrics |
| S10 distribution | Per-app release workflow, reproducible package/checksum/image digest, root catalogue entry, backward-compatible market migration and install/upgrade/uninstall docs | Fresh install and disabled-backend behavior pass; actual catalogue URL/hash/version verified; explicit publish step |
| S11 release acceptance | All automated/security/browser checks, isolated backup/restore/reconciliation, failure drills, operator sign-off | Deployable artifacts and runbook reviewed; no unresolved blocking schema/auth/capability claims |

S01 is platform development, S02 onward is independent Status work. After the
platform prerequisite lands, routine Status releases must not require a Portal
rebuild. Do not append these changes to notification PR #141. Work on a separate
branch and publish separate PRs when implementation is requested.

### 11.1 Application-market monorepo layout

```text
StockAnalysisPrediction-Report-Portal-Apps/
  index.json
  apps/
    status/
      app.json
      frontend/             # Installable private viewer/admin application
      service/              # Its own Go module, binary and deployment
        go.mod
        cmd/status-service/
        internal/
          config/
          auth/             # Verify host assertions; no user passwords
          store/            # Status-only persistence and migrations
          probe/            # Scheduler and typed HTTP/Dify/DB/provider adapters
          incidents/
          notify/           # Portal publication outbox, not mail delivery
          publicstatus/     # Public projection boundary
      public-web/           # Public-only status website
      packaging/
      examples/             # Placeholder-only deployment/gateway recipes
      docs/
    demo-symbols/
      app.json
      frontend/
      docs/
  packages/
    bridge-sdk/             # Shared documented host bridge, no app secrets
  scripts/                  # Catalogue checks and per-app build orchestration
  docs/                     # Contribution, capability and publication contracts
  .github/workflows/
```

The source tree need not match the ZIP tree: a packaged ZIP still has `app.json`
and built frontend assets at its root. Service executables/images are separate
artifacts, never files the Portal attempts to execute after installing a ZIP.

Give each app its own package metadata and test/build commands. Shared tooling
must not import app business logic. Add shared UI only after there is a real
second consumer; do not force every app to use one frontend framework. Do not
create a single cross-app Go module, database, runtime or version solely because
source shares a repository.

Proposed manifest ID is `status`. Do not request `ingest` or broad report `query`
for convenience. Declare only the new generic extension capabilities after they
actually exist in a released Portal version. The background publisher uses a
separate service credential, not the administrator-opened app's ephemeral `notify`
capability. Define minimum Portal/service/frontend versions in release docs and
perform a capability handshake; the existing manifest parser does not enforce an
invented `min_portal_version` field.

### 11.2 Test and packaging procedure

Use the Portal's existing backend/frontend gates for S01 and host market changes.
Run this proposed service loop from `apps/status/service` in the new monorepo
once its scaffolding is implemented:

```sh
go build ./...
go vet ./...
go test ./...
go test -race ./internal/auth ./internal/probe ./internal/incidents ./internal/notify
```

Create documented frontend scripts for typecheck, test and production build in
`apps/status/frontend`; do not claim those commands exist before scaffolding. Fixture
upstreams must simulate unauthorized credentials, malformed payloads, 429 with
Retry-After, TLS errors, slow headers/bodies, connection loss and provider format
drift. Real provider/DB tests require explicit test credentials and never default
to production infrastructure. No test calls paid inference automatically.

Package only built app assets and `app.json` at ZIP root; exclude `.env`, source
maps containing secrets, database files, key material and development servers.
Verify ZIP traversal/file/size checks through the real Portal installer, use
immutable release URLs, record SHA-256 in release metadata and update the market
only after artifact verification. The current catalogue has no enforced checksum
field: do not claim that merely adding a JSON field enables host verification.
If enforced integrity/minimum-version policy is required, implement it as a
separate generic market enhancement before relying on it as a security boundary.

## 12. Operator deployment and rollback sequence

1. Back up Portal. Deploy the approved generic platform release and verify #141
   publication capabilities. Do not remove migration markers for a downgrade.
2. Prepare an independent Status volume/database and encryption key source.
   Approve explicit network destinations. Start with probes/public access/outbox
   paused and no real credentials in example files.
3. Register the app backend through the new platform administration surface.
   Validate server-to-server identity and revocation with disposable accounts.
4. Install the verified app bundle. Configure monitor secrets through its admin
   UI, verify each read-only check, choose private/OU/public policy deliberately.
5. Enable probes for a small monitored subset, confirm stale/incident/recovery
   behavior, then configure Portal publication credentials/rules and unpause the
   outbox only after deliberate recipient review.
6. Add the selected private/public `/status` gateway mode. Public mode requires a
   field-by-field public projection review and anonymous endpoint tests.
7. Exercise a simulated outage/recovery and a denied viewer. Confirm that Portal
   reading, analysis submission, run queue and authentication still behave normally.
8. Roll back a Status release independently by pausing probes/outbox, restoring
   its compatible backup or previous image as required by its schema contract, and
   keeping publication paused until incident/outbox evidence is reconciled.
9. Disable/uninstall: revoke publisher credentials, disable backend registration,
   stop probes/service, remove market app and gateway rule. External monitors do
   not stop simply because the ZIP was uninstalled. Retain/delete Status data only
   under an explicit operator retention choice.

Failure drills must cover: Status unavailable; Portal unavailable; provider quota
API unavailable; negative/zero credit; expired key; DB timeout; last-success data
becoming stale; removed OU membership; service restart mid-probe; outbox response
loss; restore after a message may already have been accepted; and wrong public
projection. Neither outage may block the other's normal application startup.

## 13. Decisions required before implementation

These are recorded design gates, not approvals already supplied by the notification
storage decision:

1. Approve the generic viewer-bound backend connection contract and its exact
   Portal persistence proposal; no Status-specific hook in Portal business pages.
2. Approve the independent Status storage and backup/key contract before DDL.
3. Shared repository creation is complete: `StockAnalysisPrediction-Report-Portal-Apps`,
   public as confirmed by the owner. A separate Status source repo is not planned.
4. Select private `/status` redirect as default, or explicitly approve public mode
   and component projection before deployment.
5. Identify the actual Qwen billing account/site and allowed read-only credentials;
   unsupported measurements stay visibly unsupported.
6. Review per-component notification identities/audiences. Fully automatic private
   component ACL intersection is additional platform work, not an existing feature.

The plan can be reviewed now without credentials, schema mutations, new external
repositories or production network probes. Begin execution with S00 evidence and
S01's concrete platform/storage proposal, then proceed in the dependency order.


## 14. Shared marketplace release and migration contract

### 14.1 Independent releases inside one repository

- Use app-qualified immutable tags such as `status/v0.1.0`; demo and future app
  tags have their own prefixes. Tag format and archive names must be validated by
  CI rather than inferred from an unrestricted manual input.
- Release only the selected app ZIP, its checksums and, when needed, its separate
  service image. Pin the image digest and document compatible frontend/service
  and minimum Portal capabilities. Never reuse a published version for new bytes.
- Normal app changes run that app's checks plus root catalogue checks. Changes to
  shared SDK/tooling/workspace lockfiles run every affected consumer; changes to
  root CI/security/build infrastructure run the full app matrix. Do not use a
  narrow path filter that skips apps affected by shared dependencies.
- A release builds once from the tested commit. Publish verified artifacts first,
  then update the catalogue to their immutable download URLs. A catalogue entry
  must never point at a future/missing asset or a mutable `latest` ZIP.
- Keep catalogue structure compatible with the existing host: `{ "apps": [...] }`
  with existing ID/name/icon/version/description/scopes/URL fields. Use absolute
  immutable `url` values when archives live in GitHub Releases. Additional
  metadata does not imply the existing Portal validates it.
- Validate unique/stable app IDs, versions, declared scope parity with the bundle,
  HTTPS release URLs, file/size limits, required assets and secret-free archives.
  New scopes require an actual supported host capability and permission preview.
- Roll back a catalogue pointer to a prior tested version only with documented
  app/service data compatibility. Catalogue rollback does not downgrade a user's
  already installed app or service automatically.

### 14.2 Move the market without breaking current installations

1. Create the approved shared repository and validate its catalogue/build/release
   scaffolding before changing the Portal's default market URL.
2. Move the demo source with its stable ID and preserve a known working archive;
   add Status only after its installable release passes S10 acceptance. Do not
   change the built-in batch/quote applications into downloadable apps in this work.
3. Publish and verify the new repository's `main/index.json` and all referenced
   assets through the real existing market preview/install path.
4. In a separate small Portal change, update `defaultAppMarketIndexURL` to the
   verified new root catalogue URL. Leave explicit administrator DB overrides
   unchanged. Do not mass-edit deployment settings or assume an override follows
   a changed compiled default.
5. Retain `apps/index.json` and the old demo archive at their original Portal URLs
   for older binaries and installations explicitly using them. During transition,
   generate the legacy compatible index from the new canonical catalogue through
   an explicit release-maintenance change; do not let two manually maintained
   catalogues become conflicting sources. Freeze/retire legacy distribution only
   under a separately documented compatibility decision.
6. Verify fresh installs, old-default installs, custom-market overrides, upgrades,
   installed data preservation, unavailable market behavior and permission preview.
   Changing where the catalogue is downloaded must not uninstall or overwrite an
   installed app until the administrator explicitly chooses an install/upgrade.
7. Document both the new canonical location and legacy compatibility URLs. An
   emergency default-URL rollback is a host configuration/release action, not a
   reason to rewrite app tags or modify published package bytes.

One source repository does not mean one trust boundary: installed apps remain
sandboxed and individually permissioned, backend credentials remain per app,
Status keeps its own service/storage lifecycle, and host business pages remain
owned by the Portal.
