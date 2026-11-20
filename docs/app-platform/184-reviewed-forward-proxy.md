# Proposal: a reviewed forward proxy for backend egress

Status: Accepted 2026-11-20; implemented on the Portal side (section 14), with the journal v4 budget table the owner approved on 2026-11-20. The Apps side is pending. The owner decided D1–D5 and accepted the recommended answers to Q1–Q7 on 2026-11-20 (sections 12 and 13). It builds on:

- [174](174-background-broker-egress.md): the background egress session, the broker socket and the session lifetime;
- [175](175-reviewed-multi-origin-and-private-network.md): reviewed targets, the private-network allowlist and the address guard.

Related: [176](176-background-credential-use.md), [177](177-provider-request-signing.md), [182](182-cross-platform-backend-runtime.md).

Evidence is against `main` at `3ad8cbb5`.

## 1. Problem and evidence

Consider market-monitor, the first real background app.
- **Volume and sources.** Its Python backend sends thousands of HTTP(S) requests a day to Eastmoney, Tencent, Sina, the Shanghai Stock Exchange and the Shenzhen Stock Exchange. Some of them run concurrently, about 8 workers for the margin-trading feed.
- **Third-party code.** Most of them go through akshare, which uses `requests` and `curl_cffi` internally. `curl_cffi` impersonates browser TLS fingerprints, and some of these sites need that to answer at all.

Today the only way out is a broker call:

- **No network in the container.** A backend container has no network: `NetworkMode` is `"none"` (`internal/apprunner/driver/container.go:388`). Its one channel is the broker socket, `PORTAL_APP_BROKER_SOCKET=/run/portal-app/broker/broker.sock` (`container.go:385`).
- **One signed request per call.** Each outbound call is a single signed `network.http` request. Portal checks it against the reviewed policy and the runner executes it (174 §3, 175 §4).
- **Fixed limits.** Request body 64 KiB, response 1 MiB, deadline 15 s (`internal/appplatform/http_policy.go:21-23`). Concurrency is at most 4 per installation (`http_policy.go:24`). The runner serves at most 8 calls at once (`internal/apprunner/broker/network_pool.go:16`), and background calls may hold half of them. Background sessions get 120 calls per minute with a burst of 20 (`internal/apprunner/control/service_egress.go:27-28`).

**What that costs.** Every scraping call would have to be rewritten into a broker call. Calls made inside akshare cannot be rewritten at all. Several akshare endpoints also return more than 1 MiB.

**The constraint.** The fix must keep the container without a network and keep review, audit and limits in the platform.

## 2. Proposal in one paragraph

**What gets granted.** A new optional backend capability, `network.proxy`, is reviewed as a list of hosts. An entry is exact (`qt.gtimg.cn`) or a one-level wildcard (`*.eastmoney.com`, D2).

**How the app reaches it.** When the capability is declared, the runner sets `HTTPS_PROXY`/`https_proxy` to `http://127.0.0.1:3128` in the container. A forwarder inside the container listens there (D3). It is the SDK's `portal-egress-proxy` program or the app's own resident backend using the SDK library. The forwarder relays each `CONNECT host:443` over the existing broker socket.

**What the broker does.**
- It checks the host against the reviewed list.
- It resolves the name and applies the 175 address guard to every address.
- It dials one pinned address.
- It requires that the TLS ClientHello's SNI equals the CONNECT host.
- It then splices bytes in both directions.

TLS stays end to end between the app and the site, so browser fingerprint impersonation keeps working.

**Where review, audit and limits apply.** Per host and per tunnel: open tunnels, new tunnels per minute, idle time, lifetime and a daily byte budget (D4). Plain HTTP is refused (D5). Tunnels exist only while Portal holds the installation's egress session (174 §3.2), and close when the session ends.

## 3. What this capability gives up, deliberately

A CONNECT tunnel carries TLS that the platform does not decrypt, so the guarantees of 175–177 do not carry over.

| Guarantee for a `network.http` call | Under `network.proxy` |
|---|---|
| Method and path reviewed per call | Host (and port 443) only |
| 64 KiB request / 1 MiB response / 15 s per call | Per-tunnel idle and lifetime limits, daily byte budget |
| Reviewed credential placed by Portal (176), provider signing (177) | Not available; calls that need a credential stay on `network.http` |
| Per-call audit | Per-tunnel counters and per-host rollups; refusals audited |
| 120 calls/min | New tunnels per minute; requests inside a keep-alive or HTTP/2 tunnel are not counted |

The two capabilities coexist. An app can hold both: `network.http` for credentialed or signed calls, and `network.proxy` for anonymous bulk reads.

**TLS interception is rejected.** A design that installed a platform CA in the container and decrypted the traffic would restore path review. It is rejected for three reasons:
- It breaks `curl_cffi`'s fingerprint impersonation, which is the reason these sites answer.
- It makes the platform a decrypting party for third-party traffic.
- The CA would have to be trusted separately by each HTTP stack in each app.

## 4. Threat model

| Threat | Control |
|---|---|
| App reaches an unreviewed host | Host matched against the reviewed list before resolution; refusal is audited |
| Wildcard used to reach anything under a public suffix | A wildcard's base must be a registrable domain or deeper (Public Suffix List, compiled in); `*.com`, `*.com.cn` and `*.github.io` are refused at review |
| DNS rebinding, or a reviewed name resolving to an internal address | Broker resolves, applies the 175 address guard to every answer (always-denied ranges, interfaces, bridges, private allowlist), dials one pinned address |
| IP literal in CONNECT | Refused; private addresses stay reachable only through `network.http` and the 175 allowlist |
| Domain fronting (CONNECT to a reviewed host, TLS to another) | First record must be a ClientHello whose outer SNI equals the CONNECT host (an ECH extension alone is not refused, Q3); missing SNI, mismatch, or a non-TLS first record closes the tunnel before anything is forwarded |
| Exfiltration to a reviewed host | Same exposure as a reviewed `network.http` target, now without per-call body caps; bounded by the daily byte budget and audited per host |
| Starving the shared runner | Per-installation and runner-wide tunnel caps, separate from the `network.http` pool |
| Tunnels outliving revocation | Tunnels belong to the egress session; any drift, revocation, restore pause or runner shutdown closes all of them |

## 5. Contract

### 5.1 Capability and descriptor

- **Capability.** `network.proxy` is optional, needs a backend and `background.run` (Q2), and is off at install.
- **Descriptor.** It may suggest hosts in `network_proxy.hosts` (at most 64 entries). These are suggestions only: authority comes from the administrator's review, as in 175 (Q7). The descriptor schema change goes through `scripts/generate_app_contract.py` and the Apps SDK in lockstep.

### 5.2 Review policy

The policy is `portal.proxy-policy/1-dev.1`: a sorted, unique list of host entries, at most 64.

- **Entry forms.**
  - An exact host: lower-case ASCII or punycode with no trailing dot.
  - `*.` followed by a base. The base must be a registrable domain (eTLD+1 by the compiled-in Public Suffix List) or a name below one. The wildcard matches exactly one label in front of the base:
    - `*.eastmoney.com` matches `push2.eastmoney.com`, but not `82.push2.eastmoney.com`, which has two labels in front of the base;
    - that name needs its own entry, `*.push2.eastmoney.com`.
- **Port.** 443 only (Q1).
- **Review flow.** It follows 175: plan, consume with compare-and-swap, an audit row, and the runner capability query before the transaction. The administrator dialog shows each entry and marks wildcards.
- **Revision.** The policy has its own revision. Changing it closes the installation's open tunnels.

### 5.3 Container environment

When the active release declares `network.proxy`, the runner adds these at container creation:
- `HTTPS_PROXY` and `https_proxy`, set to `http://127.0.0.1:3128`;
- `NO_PROXY` and `no_proxy`, set to `127.0.0.1,localhost`;
- `PORTAL_APP_PROXY_LISTEN`, set to `127.0.0.1:3128`, which the forwarder reads.

They are set whether or not the capability is granted, so granting and withdrawing need no restart; without a grant every CONNECT is refused. `HTTP_PROXY` is not set (D5).

### 5.4 Forwarder (D3)

The forwarder runs inside the container and listens on `127.0.0.1:3128`. The container's loopback exists even with `NetworkMode` `none`. It comes in two forms:
- `portal-egress-proxy`, a static Go program shipped by the SDK, for apps without a resident process;
- a Go library in the SDK (`proxy.Serve(ctx)`) for apps whose resident backend should host it. market-monitor's Go wrapper is one.

It accepts only `CONNECT host:port` in HTTP/1.1 and answers anything else with 405. It opens a broker stream (section 5.5) and, on success, answers `200 Connection established` and splices. A broker refusal becomes `403`, with `X-Portal-Proxy-Refusal: <code>` (section 8) and no body. The forwarder holds no authority: every decision is the broker's.

### 5.5 Broker protocol

The broker protocol gets a new stream type on the existing broker socket, `PRXY0001`.

- **Opening.** The forwarder sends one open frame `{host, port}`, bounded to 253 + 5 bytes. The broker answers `{status, code}` and then carries raw bytes in both directions until either side closes.
- **Admission.** The broker admits a stream only under the installation's current egress session. The session command gains the proxy policy digest and the fixed limits (section 6). Those values are compared exactly, so a change to them is a format revision, as in `service_egress.go:23-26`.

**Broker steps, in order:**
1. **Session and admission.** A current session must exist. The admission counters (open tunnels, new-tunnel bucket, daily budget) are checked before any lookup.
2. **Host.** The host must match the reviewed list (section 5.2), must not be an IP literal, and the port must be 443.
3. **Resolution and guard.** The broker resolves the name with the runner's resolver, applies the address guard to every answer, and picks the first allowed address. If none is allowed, the stream is refused as `address_denied`.
4. **Dial.** It dials with a 10 s timeout.
5. **SNI check.** It reads the first TLS record from the app, at most 16 KiB within 10 s. That record must be a ClientHello whose SNI equals the host. Only then is it forwarded.
6. **Splice.** It splices with per-direction byte accounting. The idle timer resets on bytes in either direction. The tunnel closes at the lifetime limit, the idle limit, a spent budget or session close.

**Runner capability.** The runner answers a capability query, `PRPX0001`/`PRPY0001` (as 175 and 178 do). Portal includes the proxy policy in a session only when the runner answers.

## 6. Limits (D4)

The limits are fixed per platform version and carried in the signed session command. They are not administrator fields, except that the daily budget may be reviewed lower.

| Limit | Value |
|---|---|
| Open tunnels per installation | 16 |
| New tunnels per installation | 300 per minute, burst 50 |
| Idle timeout | 60 s |
| Tunnel lifetime | 10 min |
| Daily byte budget per installation (both directions) | 2 GiB; reviewable down, never up |
| Open tunnels per runner | 64 |
| Connect timeout / first-record timeout | 10 s / 10 s |

**Budget storage.** The daily budget is counted in the runner journal, in UTC days, so a runner restart does not reset it.

**Measured fit.** market-monitor's 8 concurrent margin-trading workers with `requests.Session` reuse need about 8–16 tunnels, and its traffic is estimated at a few hundred MB a day.

**Bandwidth cap.** 50 Mbit/s per installation, enforced in the splice (Q4).

**Why the proxy has its own pool.** `network.http` calls keep their own pool and limits. The proxy's 64 tunnels are separate, so bulk scraping cannot take the slots that interactive calls need.

## 7. Audit and operations

- **Portal audit records:**
  - grant, policy change and withdrawal (as 175);
  - each refused tunnel open, at most one row per installation, host and refusal code per minute, with a count;
  - an hourly rollup per installation and host: tunnels opened, bytes in each direction, closes by reason.
- **Rollup transport.** The runner reports the rollup over the session control stream. Portal stores it in the audit log; no new table is expected (Q6).
- **Admin console.** The installation page shows the reviewed hosts, today's bytes against the budget, and the last 24 hours of per-host rollups. Text is in en-US, zh-CN and zh-TW.
- **Interruption.** A tunnel open when the session closes is cut. The app sees a connection reset; the owner accepted this for scheduled work (section 12). Revocation, a restore pause, runner shutdown and policy change all close tunnels.

## 8. Refusal codes

The forwarder relays these as `X-Portal-Proxy-Refusal`.

| Code | Meaning |
|---|---|
| `not_granted` | `network.proxy` is not granted, or there is no current egress session |
| `host_not_reviewed` | The host matches no reviewed entry, or is an IP literal |
| `port_not_permitted` | The port is not 443 |
| `address_denied` | Every resolved address is denied by the address guard |
| `resolve_failed` | The name does not resolve |
| `connect_failed` | Dial failed or timed out |
| `busy` | The open-tunnel cap or the new-tunnel bucket is exhausted, so retry later |
| `budget_spent` | The daily byte budget is used up until 00:00 UTC |
| `unavailable` | The runner does not support the proxy, or a restore pause or shutdown is under way |

An SNI mismatch or a non-TLS first record is reported after `200`, so it appears as a closed connection. It is audited as `sni_mismatch`.

## 9. App side (Apps repository)

1. Add `network.proxy` and `network_proxy.hosts` to the contract, regenerated from Portal.
2. In `packages/proxy` (Go), add the library and the `portal-egress-proxy` command. The broker framing is shared with `packages/extensionprotocol`.
3. market-monitor:
   - declare the capability and suggest its hosts;
   - start the forwarder in its Go wrapper;
   - change the few `http://` URLs (for example `http://qt.gtimg.cn`) to `https://` (D5).

The owner confirmed that those hosts serve HTTPS.

## 10. Test plan (these must fail first)

Most of this runs locally (broker unit tests, the forwarder against a test broker, review and contract checks). Only the native container test needs a root Docker host. CI runner capacity is shared with other projects, so the native test runs on the existing runner-probe workflow and is not duplicated elsewhere.

**Broker, against in-test TLS servers:**
- A reviewed exact host connects.
- A one-level wildcard matches one label and refuses two.
- An unreviewed host is refused before resolution.
- Port 80 or 8443 is refused.
- An IP literal is refused.
- A reviewed name resolving to loopback, link-local, metadata or private addresses is `address_denied`.
- A mixed answer dials only the allowed address.
- An SNI mismatch, a missing SNI and a plain-HTTP first record close the tunnel, with nothing forwarded to the dialled host.
- A ClientHello carrying a GREASE `encrypted_client_hello` extension with the right outer SNI (a recorded Chrome / `curl_cffi` handshake) passes, and one with real ECH (outer SNI a different public name) closes as `sni_mismatch`.
- Limits:
  - the 17th open tunnel is `busy`, and so is the 51st burst open;
  - the idle and lifetime timers close the tunnel;
  - the budget closes the tunnel mid-stream and refuses new tunnels until the UTC day changes;
  - the budget survives a runner restart (journal).
- Session close, revocation and policy change cut open tunnels.

**Review:**
- The Public Suffix List refuses `*.com`, `*.com.cn` and `*.github.io`.
- Entries must be sorted and unique.
- Consume compare-and-swap and its audit row.
- A capability query against an old runner refuses enabling (409).

**Container:** the environment variables are present only when the capability is declared, and `HTTP_PROXY` is absent.

**Forwarder:**
- CONNECT only; any other method gets 405.
- A refusal maps to 403 plus its code.
- Half-close is propagated.
- An end-to-end test from Python `requests` and from `curl_cffi` (fingerprint preserved), through a test broker, to a TLS server.

**Native:** one runner acceptance test with a real container (`NetworkMode` `none`) reaching a TLS fixture through the forwarder.

## 11. Schema

No Portal database schema change is expected:
- The policy is stored like the 175 network policy, in the installation's grant policy JSON.
- Rollups go into the audit log.
- Budgets live in the runner journal.

If implementation shows a table is needed, it comes back to the owner first.

Implementation showed one was needed: the runner journal had no table for the daily budget. The owner approved it on 2026-11-20: journal schema version 4 adds `runner_proxy_budget(installation_id, utc_day, bytes)`, upgraded from version 3 by creating the empty table, with old days pruned. Portal's own database is unchanged. See section 14, "Journal v4".

## 12. Owner decisions (2026-11-20)

D1. **Build it.** Yes. Without it, no scraping work of market-monitor can run in production.

D2. **Wildcard hosts.** Allowed with limits: only under a concrete registrable domain, never a public suffix (`*.com` refused), one label in front of the base. The reason is that Eastmoney serves each API from its own subdomain (`push2`, `82.push2`, `push2his`, `datacenter-web`, …) and akshare may change them between releases. This deliberately departs from 175 decision 2 (no wildcards), for the proxy capability only. Under the one-label rule of section 5.2, `82.push2.eastmoney.com` needs the entry `*.push2.eastmoney.com`.

D3. **How the container reaches the proxy.** Option (a): a forwarder inside the container, so the container keeps no network and the boundary is unchanged. market-monitor's resident Go backend can host it via the SDK library, or the app can use the SDK program.
- The alternatives were (b), an internal Docker network attached to the container, and a listener the runner places in the container's network namespace.
- (b) is rejected: it gives the container a network interface.
- The namespace listener is not pursued: (a) needs no host-side namespace entry and works the same inside the 182 VM recipes.

D4. **Limits.** The values in section 6 are accepted.

D5. **Plain HTTP.** Refused. The proxy accepts only CONNECT to port 443. market-monitor changes its few `http://` URLs to `https://`.

Also confirmed by the owner:
- The 45 s background task limit (`internal/app/app_platform_task_worker.go:15`) fits a task that only enqueues and returns at once.
- Calls that fail when the egress session is interrupted are retried at the next scheduled run.

## 13. Further decisions (2026-11-20)

The owner accepted the recommended answer to each question below.

Q1. **Ports other than 443.** Decided: 443 only in version 1. An exact entry may later carry an explicit port if a real source needs one.

Q2. **Does a `network.proxy` grant require `background.run`.** Decided: yes. The proxy rides the egress session, which exists only with `background.run`. There is no foreground proxy in version 1.

Q3. **Handling of Encrypted ClientHello.** Decided, as clarified by the owner on 2026-11-20:
- The broker judges only the outer SNI. It must equal the CONNECT host, or the tunnel closes as `sni_mismatch`.
- The presence of an `encrypted_client_hello` extension is never a reason to refuse by itself. Chrome, and `curl_cffi` impersonating it, send a GREASE ECH extension with no real configuration and the real host as the outer SNI, so those handshakes pass.
- Only a ClientHello whose outer SNI differs from the CONNECT host is refused. That is what real ECH looks like: the public name outside, the real host encrypted inside.

Q4. **A per-installation throughput cap on the shared host.** Decided: 50 Mbit/s per installation, enforced in the splice. This protects other apps and the second user's desktop without slowing market-monitor's few hundred MB a day.

Q5. **Who may create wildcard entries.** Decided: the same administrators who review `network.http` targets (175 decision 1). The dialog shows each wildcard with a warning line; there is no separate role.

Q6. **Rollups in the audit log versus a table.** Decided: the audit log (hourly rows per installation and host, at most 24 × 64 per installation per day). A table only if the console needs queries the audit log cannot answer.

Q7. **Descriptor suggestions.** Decided: an app may suggest hosts, and the review dialog pre-fills them for the administrator to accept or edit, as 175 in-context consent does. Nothing is granted without the review.

## 14. Implementation notes (Portal side, 2026-11-19)

The Portal side of this document is implemented. Where it differs from the text above, this section wins. The sections are in the order of the spec.

### Not done

1. **The daily budget is in the runner journal (owner-approved table, 2026-11-20).** Implementation showed that the journal's closed schema had no table for a counter per installation and UTC day, so it came back to the owner as section 11 requires, and the owner approved journal schema version 4 with one table, `runner_proxy_budget(installation_id, utc_day, bytes)`. It is described under "Journal v4" below. Nothing in the deviations is left open for the budget, except that a crash (not a clean shutdown) can lose the bytes counted since the last flush, at most five seconds.
2. **The Python `curl_cffi` end-to-end test has not run.** The forwarder tests run `requests` through the forwarder and a test broker when the module is present, and the `curl_cffi` variant skips when it is not. It was not installed on the development host. The fingerprint is preserved by construction (the broker forwards the first record unchanged), and a GREASE-ECH ClientHello is accepted in the broker tests, but no recorded Chrome or `curl_cffi` handshake was used: the test hellos are built by hand in the shape of a browser's (GREASE values, a large hybrid key share, an outer `encrypted_client_hello` extension).
3. **The native container acceptance (section 10, "Native") has not run.** It needs root. `TestProxyEndpointAcceptance` (`internal/apprunner/transport/service_egress_proxy_native_linux_test.go`) uses the gate of `TestServiceEgressEndpointAcceptance` (`PORTAL_APP_RUNNER_EGRESS_ACCEPTANCE=1`, root, a GitHub-hosted runner) and is run by the same step of `.github/workflows/app-runner-probe.yml`, whose `-test.run` now names both tests. It stands a fresh network namespace with only loopback in for the container's `NetworkMode` `none`, runs the reference forwarder in it as uid 65532 against the real root-owned broker socket, and reaches a TLS fixture. It is not a Docker container: the existing egress acceptance is not one either. The resolver, the dial and the interface list are the test seams, as in every other egress test, so the fixture on loopback is not refused by the address guard.

### Journal v4 (the approved table)

- **Schema.** `runner_proxy_budget(installation_id ID, utc_day N, bytes N, PRIMARY KEY(installation_id, utc_day))`, declared in `schema.go` like every other product; `schemaVersion` is 4 and the inspection pins the declaration. Version 3 is otherwise unchanged.
- **Upgrade.** From v3, `Open` creates the empty table in the upgrade transaction and inspects the result before committing; from v1 and v2 it first runs the existing steps to v3, then the same. An older binary refuses a v4 journal as newer (`ErrNewerVersion`), as it does any newer file. Existing tests that pinned version 3 or five tables now expect version 4 and six.
- **Store.** `Store.ProxyBudgetUsed`, `AddProxyBudget` (an upsert that adds) and `PruneProxyBudget` (deletes days before a bound), each refusing a malformed installation ID or a negative value.
- **Budget.** `broker.DurableProxyBudget` implements `ProxyBudget` over any `ProxyBudgetStore`. Bytes are counted in memory and written in batches every five seconds and at a clean shutdown, because a write per 32 KiB chunk would be hundreds a second at the throughput cap; a failed write stays pending and is retried, never counted twice. Days older than yesterday are pruned when the UTC day changes, so the table holds a few days per installation however long the runner runs. `ListenNetworkManagedController` wires it to the journal; the narrower constructors keep the in-memory budget, as they have no session and so no tunnel.
- **Tests.** `journal/proxy_budget_test.go` (a fresh v4 journal, v3 and v1 upgrades, counting, survival across a close and reopen, pruning), `broker/proxy_budget_test.go` (batching, restart, retry, pruning, periodic flush) and `TestProxyBudgetSurvivesARunnerRestart` in the transport package (a spent budget is still spent after the registry closes and a new one opens over the same store).

### Decisions and deviations

4. **The policy carries the budget.** `portal.proxy-policy/1-dev.1` is `{"format","hosts","daily_bytes"}`: D4 says the budget may be reviewed lower, so the reviewed value needs a place. It is between 1 MiB and 2 GiB. The canonical policy is at most 16,384 bytes, the grant column's bound (`policy_json` is `J16384`), so a list of 64 of the longest entries is refused as input; 60 fit. Wildcard bases are checked with `golang.org/x/net/publicsuffix`, already a dependency, which covers private suffixes: `*.com`, `*.com.cn` and `*.github.io` are refused.
5. **"The policy has its own revision" is the installation's policy revision.** Without a column, the only revision grant readers accept is `installations.policy_revision`, so a consume raises it as in 175 section 3.4 (revision and the binding set unchanged, grants, the active activation and active bindings re-stamped). The open session sees the new revision as drift and closes; the coordinator opens the next with the new policy. A review therefore also reloads open app documents (175 decision 4).
6. **Session command.** A session with a proxy uses `portal.service-egress-session/1-dev.2`, the resource `network.proxy` and a `proxy` block holding the canonical policy text, its digest, the reviewed budget and every fixed limit, all compared exactly. A session without one keeps `1-dev.1` and its exact encoding (a test pins this), so a runner that predates the proxy still opens it. A policy of 16 KiB fits `MaxCommandBytes`.
7. **Wire.** The proxy stream is not an HTTP route. The broker socket is sniffed for eight bytes (5 s): `PRXY0001` starts a tunnel, anything else is the HTTP server's. The open frame is `PRXY0001 | u16 length | host:port`, at most 259 bytes (253 + the colon + 5), not the 258 of section 5.5, which had no room for the separator. The answer is `PRXR0001 | status | code`, with one code byte per refusal of section 8. The codec is `internal/appplatform/proxyforwarder/wire.go`, used by both the broker and the forwarder. A refusal that arrives after the tunnel opened (`sni_mismatch`) closes the connection and is reported, as section 8 says.
8. **Capability query.** `PRPX0001`/`PRPY0001` returns `portal.runner.proxy-profile/1-dev.1`: the formats and every fixed limit of section 6, which Portal compares exactly with its own. It is asked before a plan, before a consume and, by the coordinator, before every session that would carry a proxy. A runner that does not answer, or answers other limits, is refused for a review (409, `app_platform_runner_unsupported`) and gets a session without the proxy. A failure to ask is not "unsupported": the session is not opened, so a granted proxy is never dropped silently. A withdrawal never needs the runner (see item 12).
9. **Tunnels follow the installation's current session, not each session.** A renewal that carries the same policy digest keeps open tunnels; a different digest, a session without the proxy, no current session (close, revocation, drift, restore pause, expiry), the installation's close and runner shutdown cut them. Cutting at every session close would cut every tunnel five minutes after it opened, whatever its lifetime limit. A session that is replaced keeps no authority of its own. The 16-tunnel cap and the admission bucket are carried across a generation change; the open tunnels of the old generation are cut when its listener closes.
10. **Admission details.** A token of the new-tunnel bucket is taken at admission even when the open is then refused, so refused opens count against the 300 a minute. A stream that has not sent its open frame holds no tunnel slot; at most 32 per installation may be in that state. The lifetime counts from admission, so resolving and dialling are inside it. The budget is charged in the chunks that cross, both directions, including the ClientHello, and cuts the tunnel when spent. The 50 Mbit/s cap is a byte bucket shared by the installation's tunnels (burst a fifth of a second), waiting on the real clock. Idle is measured from the last byte in either direction. A clean end of one side is propagated as a half-close.
11. **Address guard.** Every answer is judged with the public-address rule and the always-denied set of the `network.http` executor, and against the host's interface addresses and bridge subnets (read per stream, a failed read denies). The first allowed answer is dialled and denied answers are skipped; this differs on purpose from `network.http`, which denies a mixed answer as a whole, because section 10 says a mixed answer dials only the allowed address.
12. **Review.** `portal.proxy-policy-review/1-dev.1` plans live in `app_platform_review_plans` as `update` plans with a NULL target release and their own format, so the 175 review never reads them. The grant row keeps its install-time `review_plan_id` (the install snapshot compares it); the session loader proves the stored policy against the latest consumed proxy plan for this release, epoch and generation, and treats a row no review backs as not granted. Routes: `GET .../installations/{id}/network-proxy`, `POST .../installations/{id}/network-proxy/plan`, `POST /api/admin/app-platform/network-proxy-reviews/{id}`. A plan with no hosts withdraws the grant; withdrawing needs neither the runner nor a review that backs the row. Removing hosts, or lowering the budget, is a plan like adding.
13. **Reports and the console.** The runner sends reports as one-way calls on the session (`EgressFrameProxyReport`, 0x0B; 0x0A went to doc 178 while this was in review): the hourly rollup per host, refusals coalesced per host and code at most once a minute, and the day's usage every ten minutes while it changes. They are sent a few per second, because Portal holds each call until its audit row is written and a session has a bounded number of calls open. Portal writes `app_platform.proxy_rollup`, `app_platform.proxy_refused` and `app_platform.proxy_usage`, and limits refusal rows to one per installation, host and code a minute and 64 a minute per installation. A refused name that is not a plain lower-case DNS name is reported as `-`. The console reads the last 24 hours of rollups and today's usage from `audit_log`; no table was needed. Without a usage row for today the console shows the rolled-up hours as a floor.
14. **Container environment.** Set by `planCandidateContainer` for a release whose optional capabilities include `network.proxy`, whether or not the grant exists. `HTTP_PROXY` and `ALL_PROXY` are never set.

### What the Apps side must do

1. Regenerate the contract from this repository (`scripts/generate_app_contract.py --apps-dir`): the capability `network.proxy` (optional only, needs a backend and `background.run`) and the descriptor block `network_proxy.hosts`.
2. Mirror `internal/appplatform/proxyforwarder` as `packages/proxy`: the `PRXY0001` codec (`wire.go`) and the CONNECT forwarder (`Serve`, `Run`), plus a `portal-egress-proxy` command that calls `Run`. `packages/extensionprotocol` does not need the codec.
3. market-monitor: declare `network.proxy` with its suggested hosts (`*.eastmoney.com`, `*.push2.eastmoney.com`, `qt.gtimg.cn`, the Shanghai and Shenzhen exchange hosts) and `background.run`; start the forwarder in the Go wrapper before the Python backend; change the `http://` URLs to `https://`; expect `403` with `X-Portal-Proxy-Refusal` until an administrator has reviewed the hosts.
4. Run a `curl_cffi` and a `requests` end-to-end check against a runner with this change.
