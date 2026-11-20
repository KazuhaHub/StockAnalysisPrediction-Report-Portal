# Proposal: pinned destinations for the reviewed forward proxy

Status: Proposed 2026-11-20. This document changes no code, schema or contract. It extends:

- [184](184-reviewed-forward-proxy.md): the reviewed forward proxy, its policy, broker steps, limits and refusal codes;
- [175](175-reviewed-multi-origin-and-private-network.md): the address guard and the operator-owned private-network allowlist (decision 3).

Related: [174](174-background-broker-egress.md), [182](182-cross-platform-backend-runtime.md).

Evidence is against `main` at `bcb7c03a`.

## 1. Problem and evidence

market-monitor on anchan needs a quotes API that is served on the same host: aaPanel nginx terminates TLS for `quotes.internal.anchan.kazuhahub.com` and passes it to an frp tunnel on `127.0.0.1:8000`. The app reaches the internet only through `network.proxy` (184), and the broker cannot reach that site:

- **Address guard.** After resolving a reviewed name, `firstAllowed` (`internal/apprunner/broker/proxy.go:433-450`) skips every answer that fails `publicAddress` (`http.go:349-366`: loopback, RFC 1918, CGNAT, documentation and reserved ranges), `appplatform.AlwaysDenied` (`private_networks.go:78-88`) or `onHost` (`address_guard.go:52-62`: any address of a host interface and anything inside a bridge subnet). The host's own public address is therefore denied too, and the stream is refused as `address_denied`.
- **Lifting the guard is not acceptable.** It exists for Aliyun metadata (`100.100.100.200`), the tailnet (`100.64.0.0/10`), Docker and libvirt bridges, and unauthenticated services on the host's loopback (Portal, Dify, databases).
- **`network.http` does not help.** Its private targets (175 §3.1) are per call, capped at 1 MiB, and the runner refuses any allowlist prefix that contains a host interface address (175 §3.2), which excludes loopback and the bridges.

What is needed is a narrow exception: one reviewed name, one destination chosen by people, nothing else.

## 2. Proposal in one paragraph

**What gets granted.** The proxy policy gains a list of **pinned entries**. Each maps one exact host name, always reached by the app on port 443, to one address and port: `quotes.internal.anchan.kazuhahub.com` → `127.0.0.1:20443`. A Portal administrator sets entries per installation in the existing proxy review. The address and port must be one the host operator permits in the runner's root-owned configuration (a new `proxy_pins` block, the 175 decision-3 model). The descriptor may only suggest the name, through the existing `network_proxy.hosts`.

**What the broker does.** For a CONNECT whose host equals a pinned name, the broker does not resolve the name. It checks the pin against the operator allowlist and a fixed denied set (section 6), and dials the pinned address. `publicAddress` and `onHost` are skipped for that entry only.

**What stays.** Everything else in 184 is unchanged: CONNECT to port 443 only, the SNI check, every limit and budget, the refusal codes and `X-Portal-Proxy-Refusal`, the forwarder and the SDK. TLS stays end to end. The app connects to `https://<name>`, and the pinned listener must present a valid certificate for that name.

## 3. Where the broker dials from

- **The broker runs in the runner's process.** The runner is a host systemd service running as root with no network isolation (`deploy/app-runner/portal-app-runner.service`: no `PrivateNetwork=` and no `NetworkNamespacePath=`). Proxy tunnels are dialled by `ProxyRunner.Dial` in that process (`proxy.go:126-132`).
- **So `127.0.0.1` is the host's loopback**, and a bridge gateway (`172.17.0.1`) is the host's own address on that bridge. The app's container (`NetworkMode` `none`) never sees either address. It only names the host.
- **The runner never runs in a container.** It needs the Docker socket, root and the XFS mounts (182 §2), so there is no container namespace to account for.
- **In a 182 M1 deployment** (the whole stack in a Linux VM), the runner runs in the VM. Loopback is then the VM's own, and reaching a service on the physical host means pinning the address of the host as seen from the VM. That is an ordinary private address, so the operator allowlist has to name it.
- **anchan.** The app platform is not deployed there yet. Before the first entry is reviewed, the deploy must confirm that the runner runs from the shipped unit, in the host namespace.

## 4. Threat model

| Threat | Control |
|---|---|
| A Portal administrator alone opens the host's loopback or the LAN | A pin must lie in the operator's `proxy_pins` allowlist (exact address and port) in the root-owned runner configuration. Portal can only select inside it, as in 175 decision 3 |
| The app or its descriptor sets a destination | Pins come only from a consumed administrator review. The descriptor carries names only, and no descriptor field holds an address |
| Pin to metadata, link-local, multicast or an embedded-IPv4 form | A fixed denied set (section 6) is checked at runner start, at review, at session apply and at every dial. It cannot be configured away |
| DNS rebinding of a pinned name | A pinned name is never resolved, so no answer can move it |
| Domain fronting through a pinned listener | The SNI check of 184 step 5 is unchanged: the outer SNI must equal the pinned name |
| Host-header crossing on a shared nginx listener (SNI says `quotes`, the HTTP `Host` says `portal`) | The broker cannot see inside TLS. The operator pins a **dedicated listener** that serves only that name (section 7, Q3), and nginx answers 421 to any other `Host` |
| The app skips certificate verification | The tunnel still reaches only the pinned listener. The certificate protects the app, not the host, so this widens nothing |
| A wildcard silently reaches the pinned address | Only the exact pinned name is dialled by pin. Other names under a wildcard resolve and pass the normal guard (section 5.6) |
| A pin outlives its review or the operator's allowlist | A policy change cuts tunnels (184 §14 item 9). The broker re-checks the allowlist at every dial, and a runner restart reloads it |

## 5. Contract

### 5.1 A pinned entry

`{"host": "quotes.internal.anchan.kazuhahub.com", "address": "127.0.0.1", "port": 20443}`

- **`host`.** An exact name by `validProxyHostName` (`proxy_policy.go:94-122`): lower case, at least two labels, no trailing dot, not an IP literal. A wildcard is refused.
- **`address`.** The canonical `netip.Addr` string. A zone is refused. An IPv4-mapped literal (`::ffff:127.0.0.1`) is refused as written rather than unmapped, so there is one spelling. The address must pass section 6.
- **`port`.** 1 to 65535; 0 is refused. It may differ from 443 (Q4). The app's CONNECT port stays 443 (184 Q1); only the broker's dial uses the pinned port.
- **Bounds.** At most 8 pinned entries per installation (Q5). Ordinary hosts and pins together are at most 64 (`MaxProxyHosts`), and the canonical policy stays within 16,384 bytes (`MaxProxyPolicyBytes`).
- **Uniqueness.** Pins are sorted by `host`, each `host` appears once, and it may not also appear as an exact ordinary host entry. A wildcard ordinary entry that also covers it is allowed (section 5.6).

### 5.2 Operator allowlist (runner configuration)

The JSON runner configuration (`internal/apprunner/service/config.go`, beside `private_networks` at `:62`) gains an optional block:

```json
"proxy_pins": [{"address": "127.0.0.1", "ports": [20443]}]
```

- **Bounds.** At most 16 addresses, each with 1 to 16 sorted, unique ports. Addresses are exact, never prefixes. Each must pass section 6, or the runner refuses to start.
- **Absent or empty** means no pin can be granted or dialled.
- **Changes.** Changing the block needs a runner restart, as `private_networks` does.
- **Advertising.** The runner advertises the block in its proxy profile (section 5.4). Portal shows it in the review dialog as the only choices.

### 5.3 Policy format

When a policy has at least one pin, it is `portal.proxy-policy/1-dev.2`:

```json
{"format":"portal.proxy-policy/1-dev.2","hosts":["*.eastmoney.com","qt.gtimg.cn"],
 "pins":[{"host":"quotes.internal.anchan.kazuhahub.com","address":"127.0.0.1","port":20443}],
 "daily_bytes":2147483648}
```

- **Without pins.** A policy without pins keeps `1-dev.1` and its exact encoding. Existing grants, digests and sessions are therefore unchanged, and a runner that predates pins still serves them.
- **Parsing.** `ParseProxyPolicy` accepts both formats, each only in its exact canonical form.
- **Hosts.** `hosts` may be empty when `pins` is not. A policy needs at least one of the two.
- **Matching.** `Match` covers ordinary entries. A new `Pin(host)` returns the pinned entry for an exact name.

### 5.4 Session command and runner profile

- **Session.** The session command keeps `portal.service-egress-session/1-dev.2`. The `proxy` block already carries the canonical policy text and its digest (`control/service_egress.go:34-50`), so pins travel inside it, signed, and add no new field. `validateServiceEgressProxy` accepts either policy format.
- **Profile.** The proxy profile becomes `portal.runner.proxy-profile/1-dev.2`. It keeps every 1-dev.1 field and adds:
  - `policy_formats`: both formats;
  - `pins`: the canonical operator allowlist;
  - `pins_sha256`: the digest of that allowlist.
- **Old runners.** Portal accepts a 1-dev.1 answer for a policy without pins.
- **Plan and consume.** Both refuse a policy with pins (409, `app_platform_runner_unsupported`) unless the runner answers 1-dev.2 and lists every pin.
- **Downgrades.** If a runner is downgraded after a pinned policy is consumed, the coordinator opens the session without the proxy, as 184 §14 item 8 already does for a runner that answers other limits.

### 5.5 Broker steps (184 §5.5, changed steps only)

1. Session and admission: unchanged.
2. **Host.**
   - If `Pin(host)` returns an entry, the tunnel is a **pinned tunnel**.
   - Otherwise `Match` decides as today.
   - The CONNECT port must still be 443 (`port_not_permitted`).
3. **Pin check instead of resolution.** For a pinned tunnel, the broker resolves nothing. It refuses the stream as `address_denied` if either holds:
   - the address and port are not in the runner's current `proxy_pins`;
   - the address fails section 6.

   `publicAddress` and `onHost` are not applied.
4. **Dial.** It dials `address:port` with the same 10 s timeout. A failure is `connect_failed`.
5. SNI check: unchanged. The outer SNI must equal the pinned name, and HelloRetryRequest handling is unchanged.
6. Splice, budget, throughput cap, idle and lifetime: unchanged.

### 5.6 Interaction with wildcards

The pin is looked up first, and for its exact name it always wins. With an ordinary entry `*.internal.anchan.kazuhahub.com` and a pin for `quotes.internal.anchan.kazuhahub.com`:
- `quotes.…` is dialled by pin, never by DNS;
- `other.internal.anchan.kazuhahub.com` resolves and meets the full guard, so it is `address_denied` if it resolves to loopback.

A pin never falls back to DNS, and DNS never falls back to a pin.

### 5.7 Propagation to a running session

This is the mechanism 184 §14 items 5 and 9 already use:
1. A consumed review raises `installations.policy_revision`.
2. The open session sees drift and closes.
3. The coordinator opens the next session with the new canonical policy.
4. `ProxyInstall.Apply` sees a new digest and cuts every open tunnel.

Adding, changing or removing a pin therefore takes effect within one session renewal, and no tunnel runs under the old pin. An operator change to `proxy_pins` takes effect at the runner restart it needs. That restart closes every session, and the broker also re-checks at every dial.

## 6. Addresses never pinnable

A pinned address must be **positively** one of:
- IPv4 unicast;
- `::1`;
- inside `fc00::/7`;
- inside `2000::/3`.

It must also lie **outside** every range below. The rule is checked:
- at runner start, for `proxy_pins`;
- at review;
- at session apply;
- at every dial.

The set is `appplatform.AlwaysDenied` with exactly loopback removed (Q2), plus the metadata addresses. It becomes a new function, `appplatform.PinDenied`.

| Family | Denied |
|---|---|
| IPv4 | `0.0.0.0/8`, `169.254.0.0/16` (link-local, AWS/Azure/GCP metadata), `224.0.0.0/4`, `240.0.0.0/4` (incl. `255.255.255.255`), `100.100.100.200/32` (Aliyun metadata) |
| IPv6 | `::/128`, `fe80::/10`, `ff00::/8`, `::ffff:0:0/96` (IPv4-mapped; refused as written), `64:ff9b::/96`, `64:ff9b:1::/48` (NAT64), `2002::/16` (6to4), `2001::/32` (Teredo), `fd00:ec2::254/128` (AWS metadata), `fd20:ce::254/128` (GCP metadata) |

- **Loopback.** Of `AlwaysDenied`, only `127.0.0.0/8` and `::1/128` are released, and only for pins. `AlwaysDenied` itself, `network.http` and ordinary proxy hosts are unchanged.
- **Other non-public ranges.** RFC 1918, `100.64.0.0/10` (tailnet) and bridge addresses are not denied for pins, but each needs an explicit operator allowlist entry.

## 7. TLS

- **What the broker can check.** It cannot see inside TLS. It checks only the outer SNI, which must equal the pinned name (184 step 5). The certificate is verified by the app's TLS stack against the name it dialled, and that verification is the guarantee that the app reached the right site. `NO_PROXY` and the forwarder are unchanged, so `https://quotes.internal.anchan.kazuhahub.com` goes through the tunnel with that name as SNI.
- **Certificate.** The pinned listener presents a certificate valid for the name. On anchan that is the aaPanel site's Let's Encrypt certificate, issued by DNS-01, because the name need not be publicly reachable.
- **Dedicated listener (Q3).** nginx selects the TLS server by SNI, but then routes each request by its `Host` header. A pin to the shared `127.0.0.1:443` would let the app send `Host: <any other site on that nginx>`. The operator therefore pins a dedicated listener, for example `listen 127.0.0.1:20443 ssl;`, with one `server` block for this name, the default on that port, that returns 421 when `$host` is not the name. Port 20443 follows the host port plan (20000–20999 for loopback services behind a proxy).

## 8. Limits

- **Unchanged from 184 §6.** A pinned tunnel counts against the same open-tunnel cap, new-tunnel bucket, daily byte budget, throughput cap and timers as any other.
- **New bounds.** At most 8 pins per installation (within the 64 entries), and at most 16 operator addresses with 16 ports each.

## 9. Refusal codes

No new code; the wire table (`proxyforwarder/wire.go:39-55`) and the forwarder are unchanged.

| Case | Code |
|---|---|
| Pin outside the operator allowlist, or in the denied set | `address_denied` |
| Pinned dial failed or timed out | `connect_failed` |
| CONNECT port not 443 | `port_not_permitted` |
| SNI not the pinned name | closed after `200`, audited as `sni_mismatch` |

`resolve_failed` never occurs for a pinned tunnel.

## 10. Review, audit, reports and console

- **Review.** The plan of `POST .../installations/{id}/network-proxy/plan` gains `pins: [{host, address, port}]`.
  - The plan format becomes `portal.proxy-policy-review/1-dev.2`, adding `pins_added`, `pins_removed` and `pins_changed` (`{host, old, new}`) beside `hosts_added` and `hosts_removed`.
  - Consume, its compare-and-swap and the revision bump are those of 184 §14 item 12.
  - The session loader keeps accepting consumed 1-dev.1 plans.
  - A plan that removes every host and every pin withdraws the grant.
- **Audit of changes.** `app_platform.proxy_policy_changed` gains `pins_added`, `pins_removed` and `pins_changed` in its detail. The administrator, the plan ID and the policy digests are already recorded. Each pin is written as `host → address:port`.
- **Audit of traffic.** Rollup and refusal reports need no wire change (`EgressFrameProxyReport` is unchanged). When Portal writes `app_platform.proxy_rollup` and `app_platform.proxy_refused`, it adds `pinned: true` and `pin: "address:port"` to the detail of a host that is pinned in the policy of the session carrying the report.
- **Console.** `AppNetworkProxyPanel` lists pinned entries in the reviewed-hosts list, each with a "pinned to `address:port`" tag.
  - In the review dialog, an exact host can be marked as pinned. The address and port are chosen from the runner's advertised `proxy_pins` (a select, not free text). Each pin carries a warning line, as wildcards do.
  - The diff shows pins added, changed and removed.
  - The new keys go under `appPlatform.networkProxy.pin.*` in en, zh-CN and zh-TW, and the parity test covers them.

## 11. Schema change — needs owner approval

- **Portal database: none.** No migration and no new column or table. Pins live in the grant row's existing `policy_json` (J16384), in policy format `portal.proxy-policy/1-dev.2`. Plans live in `app_platform_review_plans.plan_json` in plan format `portal.proxy-policy-review/1-dev.2`, with the new fields `pins_added`, `pins_removed` and `pins_changed`.
- **JSON formats that change:**
  - policy `portal.proxy-policy/1-dev.2`, with `pins`;
  - plan `portal.proxy-policy-review/1-dev.2`;
  - runner profile `portal.runner.proxy-profile/1-dev.2`, with `policy_formats`, `pins` and `pins_sha256`;
  - audit details, with the pin fields of section 10.

  The session command format is unchanged.
- **Runner configuration.** An optional `proxy_pins` block. The config format name is unchanged, as with `private_networks`.
- **Runner journal: none.** It stays at schema v5.

## 12. Open questions (recommended answer in each)

Q1. **Two keys (operator allowlist and administrator review) or the administrator alone?** Recommended: two keys. 175 decision 3 keeps a Portal administrator alone from opening the LAN, and the host's loopback is more sensitive than the LAN. On anchan the operator and the administrator are the same person, so the cost is one line of runner configuration.

Q2. **Loopback is in `AlwaysDenied`.** The requirement says that whatever `AlwaysDenied` covers stays denied, but it covers `127.0.0.0/8` and `::1`, which the first entry needs. Recommended: release exactly those two prefixes, for pins only, through `PinDenied`. Every other `AlwaysDenied` range and both metadata addresses stay denied.

Q3. **Pin `127.0.0.1:443` (the shared nginx listener) or a dedicated listener?** Recommended: a dedicated `127.0.0.1:20443` listener that serves only the name and answers other `Host` values with 421. On the shared listener the app could reach Portal, Dify or any other vhost by changing `Host`, which the broker cannot see.

Q4. **May the pinned port differ from 443?** Recommended: yes, any port from 1 to 65535 that the operator lists. The app still CONNECTs to 443. Q3 needs this.

Q5. **Maximum number of pins per installation.** Recommended: 8, counted within the 64 entries. One is needed today.

Q6. **A name that is both pinned and covered by a wildcard.** Recommended: the pin wins for its exact name, with no DNS fallback. An exact ordinary entry with the same name as a pin is refused at review.

Q7. **A runner without pin support.** Recommended: the review refuses with 409. After a downgrade, the session opens without the proxy (the 184 behaviour), rather than with the proxy minus its pins.

Q8. **Can tailnet, RFC 1918 or bridge addresses be pinned?** Recommended: yes, but only when the operator lists the exact address and port. The fixed denied set covers metadata, link-local and the translation forms, not ordinary private space.

Q9. **New refusal codes.** Recommended: none. The cases map to `address_denied` and `connect_failed`, and the audit row marks the tunnel as pinned. The forwarder and the SDK stay unchanged.

Q10. **A descriptor field for pin suggestions.** Recommended: none. The app suggests the name in `network_proxy.hosts`, and the administrator turns it into a pin. No descriptor field ever carries an address.

## 13. Test plan (acceptance conditions; these must fail first)

Each condition states which way the guard fails.

**Policy and guard (`internal/appplatform`):**
- A policy without pins encodes byte-identically to 1-dev.1 and keeps its digest. Fails closed: any other encoding is refused by `ParseProxyPolicy`.
- A pin with a wildcard host, an IP-literal host, port 0, port 65536, a zoned address or an IPv4-mapped literal is refused. A duplicate pin name, or a pin name equal to an exact host, is refused. A 9th pin, or a 65th entry in total, is refused. Fails closed: refused at parse.
- `PinDenied` refuses every prefix in section 6 at its first and last address, and refuses `100.100.100.200`, `fd00:ec2::254` and `fd20:ce::254`. It also refuses IPv6 outside `::1`, `fc00::/7` and `2000::/3`. It admits `127.0.0.1`, `::1`, `172.17.0.1`, `10.0.0.5` and `100.64.0.1`. Fails closed: a denied address is never admitted.
- `AlwaysDenied` is unchanged: `127.0.0.1` is still denied. Fails closed.

**Runner configuration:**
- `proxy_pins` with a denied address, a duplicate, an unsorted or duplicate port, or more than 16 entries stops the runner at start. Fails closed: the runner does not run.

**Broker, against in-test TLS servers on loopback:**
- A pinned name in the allowlist connects to `127.0.0.1:<port>`. The `Lookup` seam is never called, which a lookup that fails the test proves.
- A pin whose address or port is not in the allowlist is `address_denied`, and nothing is dialled. Fails closed.
- With `Dial` refusing, the result is `connect_failed`. Fails closed: no `200`.
- A wildcard covering a pinned name: the pinned name dials the pin, and a sibling name resolving to `127.0.0.1` is `address_denied`. Fails closed.
- An SNI other than the pinned name closes as `sni_mismatch`, with nothing forwarded. Fails closed.
- CONNECT to the pinned name on port 20443 or 80 is `port_not_permitted`. Fails closed.
- A policy change that removes or moves a pin cuts open pinned tunnels, and the next open uses the new pin or is `host_not_reviewed`. Fails closed.
- Pinned tunnels count against the 16-tunnel cap, the bucket and the budget. The 17th is `busy`. Fails closed.

**Portal:**
- A plan with pins against a runner profile 1-dev.1, or one whose `pins` lacks the address, is 409 `app_platform_runner_unsupported`. Fails closed.
- Consume writes `policy_json` in 1-dev.2, raises `policy_revision`, and writes one `app_platform.proxy_policy_changed` row with `pins_added`, `pins_changed` and `pins_removed`. A compare-and-swap loss writes nothing. Fails closed.
- A consumed 1-dev.1 plan still backs its grant after upgrade. Fails open for old grants by design: their policy is unchanged.
- A session for a pinned policy carries the policy text and digest, and the runner's `validateServiceEgressProxy` accepts it. A downgraded runner (profile 1-dev.1) gets a session without the proxy. Fails closed for the proxy.
- Rollup and refusal audit rows for a pinned host carry `pinned` and `pin`.

**Console:** the pinned tag, the select limited to the runner's `proxy_pins`, the diff lines and the warning, and i18n parity in three languages.

**Native:** the existing `TestProxyEndpointAcceptance` gains one case: a pinned name reaching a loopback TLS fixture with no resolver seam. It runs in the same runner-probe step, not as a new job.

## 14. Phasing

1. **P1, policy and runner.** `ProxyPolicy` 1-dev.2, `PinDenied`, the `proxy_pins` configuration, proxy profile 1-dev.2, and broker steps 2–4 for pins. A runner with this change serves existing 1-dev.1 policies unchanged.
2. **P2, Portal.** The review plan 1-dev.2, the profile check, the audit fields, the report annotation and the session loader.
3. **P3, console.** Panel, dialog and i18n. P2 and P3 can ship together, after the owner sees the demo.
4. **P4, anchan.** No code. Confirm the runner unit and its namespace (section 3). Add the dedicated nginx listener for `quotes.internal.anchan.kazuhahub.com` on `127.0.0.1:20443`, with the Let's Encrypt certificate and the 421 rule. Add `"proxy_pins": [{"address":"127.0.0.1","ports":[20443]}]` and restart the runner. Then review the pin for market-monitor.

**Apps side.** No SDK or forwarder change. market-monitor adds the name to `network_proxy.hosts` and calls `https://quotes.internal.anchan.kazuhahub.com`.
