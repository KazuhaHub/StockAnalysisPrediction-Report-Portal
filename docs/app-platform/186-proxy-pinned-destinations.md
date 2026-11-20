# Proposal: pinned destinations for the reviewed forward proxy

Status: Accepted 2026-11-20; implemented on branch `feat/app-platform-proxy-pins` (P1 to P3 in code, P4 as a handbook procedure, example configuration and acceptance probe), not merged, released or deployed. The owner accepted the recommended answer to every open question and the JSON format changes (proxy policy and plan 1-dev.2, the PRPX0002 profile query). The implementation changed no Portal database schema and no runner journal schema (section 11). This document extends:

- [184](184-reviewed-forward-proxy.md): the reviewed forward proxy, its policy, broker steps, limits and refusal codes;
- [175](175-reviewed-multi-origin-and-private-network.md): the address guard and the operator-owned private-network allowlist (decision 3).

Related: [174](174-background-broker-egress.md), [182](182-cross-platform-backend-runtime.md), 187 (draft: app-runner as a Compose service).

Evidence is against `main` at `bcb7c03a`.

## 1. Problem and evidence

market-monitor on anchan needs a quotes API that is served on the same host: aaPanel nginx terminates TLS for `quotes.internal.anchan.kazuhahub.com` and passes it to an frp tunnel on `127.0.0.1:8000`. The app reaches the internet only through `network.proxy` (184), and the broker cannot reach that site:

- **Address guard.** After resolving a reviewed name, `firstAllowed` (`internal/apprunner/broker/proxy.go:433-450`) skips every answer that fails `publicAddress` (`http.go:349-366`: loopback, RFC 1918, CGNAT, documentation and reserved ranges), `appplatform.AlwaysDenied` (`private_networks.go:78-88`) or `onHost` (`address_guard.go:52-62`: any address of a host interface and anything inside a bridge subnet). The host's own public address is therefore denied too, and the stream is refused as `address_denied`.
- **Lifting the guard is not acceptable.** It exists for Aliyun metadata (`100.100.100.200`), the tailnet (`100.64.0.0/10`), Docker and libvirt bridges, and unauthenticated services on the host's loopback (Portal, Dify, databases).
- **`network.http` does not help.** Its private targets (175 §3.1) are per call, capped at 1 MiB, and the runner refuses any allowlist prefix that contains a host interface address (175 §3.2), which excludes loopback and the bridges.

What is needed is a narrow exception: one reviewed name, one destination chosen by people, nothing else.

## 2. Proposal in one paragraph

**What gets granted.** The proxy policy gains a list of **pinned entries**. Each maps one exact host name, always reached by the app on port 443, to one address and port: `quotes.internal.anchan.kazuhahub.com` → `127.0.0.1:20443`. A Portal administrator sets entries per installation in the existing proxy review. The same name, address and port must be one the host operator permits in the runner's root-owned configuration: a new `proxy_pins` block, following the 175 decision-3 model. The operator thus approves a destination, not only a socket. The descriptor may only suggest the name, through the existing `network_proxy.hosts`.

**What the broker does.** For a CONNECT whose host equals a pinned name, the broker does not resolve the name. It checks the pin against the operator allowlist and a fixed denied set (section 6), and dials the pinned address. `publicAddress` and `onHost` are skipped for that entry only.

**What stays.** Everything else in 184 is unchanged: CONNECT to port 443 only, the SNI check, every limit and budget, the refusal codes and `X-Portal-Proxy-Refusal`, the forwarder and the SDK. TLS stays end to end. The app connects to `https://<name>`, and the pinned listener must present a valid certificate for that name.

## 3. Where the broker dials from

- **The broker runs in the runner's process.** Proxy tunnels are dialled by `ProxyRunner.Dial` in that process (`proxy.go:126-132`), so a pin is reached from whatever network namespace the runner is in.
- **Systemd deployment.** The shipped unit runs the runner as root with no network isolation (`deploy/app-runner/portal-app-runner.service`: no `PrivateNetwork=` and no `NetworkNamespacePath=`). It is in the host namespace.
- **Compose deployment.** Draft doc 187 runs app-runner as a Compose service. Pins have one precondition there: the service uses `network_mode: host`. In its own bridge namespace, `127.0.0.1` would be the container's loopback, and a pin would reach nothing or the wrong thing.
- **Check: pins only in the host network namespace.** The runner already identifies the dockerd on the other end of its Docker socket and compares that process's namespaces with its own (`internal/apprunner/driver/daemon_peer_linux.go:67-74`, `sharesNetworkNamespace`, which compares `/proc/<pid>/ns/net` with `/proc/self/ns/net`). dockerd runs in the host namespace, so it is the reference.
  - When `proxy_pins` is not empty, the runner requires that check to pass at start, and repeats it on every Docker connection it already vets.
  - If the namespaces differ, or the dockerd's `/proc/<pid>/ns/net` cannot be read (for example a container without `pid: host`), the runner refuses to start with `proxy_pins_netns`. It fails closed.
  - 187 must keep a host-namespace reference that this check can use. If it changes the daemon-peer check, it states what replaces it here.
- **Configuration file in a container.** `readPrivateConfig` (`internal/apprunner/service/config.go:116-127`) requires a regular file with mode `0600` or tighter, owned by the effective user, in a directory that is not a symlink. It must still hold through the Compose bind mount: root-owned on the host, mounted read-only, and the runner running as root. Otherwise the runner refuses its configuration, and so its pins.
- **So, in the host namespace, `127.0.0.1` is the host's loopback**, and a bridge gateway (`172.17.0.1`) is the host's own address on that bridge. The app's container (`NetworkMode` `none`) never sees either address. It only names the host.
- **In a 182 M1 deployment** (the whole stack in a Linux VM), the runner runs in the VM. Loopback is then the VM's own, and reaching a service on the physical host means pinning the address of the host as seen from the VM. That is an ordinary private address, so the operator allowlist has to name it.

## 4. Threat model

| Threat | Control |
|---|---|
| A Portal administrator alone opens the host's loopback or the LAN, or reuses an operator socket under another name | A pin must equal an entry of the operator's `proxy_pins` allowlist (exact host, address and port) in the root-owned runner configuration. Portal can only select one, as in 175 decision 3 |
| Pins dialled from the wrong network namespace | Non-empty `proxy_pins` requires the runner to share dockerd's network namespace, checked at start and on each vetted Docker connection (section 3) |
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
"proxy_pins": [{"host": "quotes.internal.anchan.kazuhahub.com", "address": "127.0.0.1", "port": 20443}]
```

- **Entries.** Each entry is a full destination with the same field rules as a pinned entry (section 5.1). A Portal pin must equal one entry exactly, so an operator who opens `127.0.0.1:20443` for the quotes name has not opened it for any other name.
- **Bounds.** At most 16 entries, sorted by `(host, address, port)` and unique. Addresses are exact, never prefixes. Each address must pass section 6, and the network-namespace check of section 3 must pass, or the runner refuses to start.
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
- **Type.** `ProxyPolicy` gains `Pins []ProxyPin`.
- **Normalising.** `NormalizeProxyPolicy` changes its first test from `len(p.Hosts) < 1` to `len(p.Hosts)+len(p.Pins) < 1`. It checks the pins (section 5.1) and the combined bound of 64, and returns `Hosts` as a non-nil slice.
- **Encoding.** `CanonicalProxyPolicy` writes 1-dev.1 when `Pins` is empty and 1-dev.2 otherwise. A pins-only policy canonicalises to `"hosts":[]`, never `null` and never with the key omitted.
- **Parsing.** `ParseProxyPolicy` dispatches on `format`. It still rejects a nil `hosts`, which keeps `"hosts":[]` the one spelling. It rejects `pins` in 1-dev.1, and an absent or empty `pins` in 1-dev.2. In either format it accepts only bytes equal to the canonical re-encoding.
- **Matching.** `Match` covers ordinary entries. A new `Pin(host)` returns the pinned entry for an exact name.

### 5.4 Session command and runner profile

- **Session.** The session command keeps `portal.service-egress-session/1-dev.2`. The `proxy` block already carries the canonical policy text and its digest (`control/service_egress.go:34-50`), so pins travel inside it, signed, and add no new field. `validateServiceEgressProxy` accepts either policy format.
**A new query; the old one is unchanged.**
- **The old query.** `PRPX0001`/`PRPY0001` keeps answering exactly `portal.runner.proxy-profile/1-dev.1`, byte for byte as today, whatever `proxy_pins` holds. An old Portal's `ParseProxyProfile` (`transport/proxy_profile.go:72-83`) and its equality with `NewProxyProfile()` therefore keep passing after the runner is upgraded. Without this, upgrading the runner before Portal would make `runnerServesProxy` false and silently drop every proxy grant.
- **The new query.** A new pair, `PRPX0002`/`PRPY0002`, answers `portal.runner.proxy-profile/1-dev.2`:
  - `fixed`: the exact 1-dev.1 profile object;
  - `pins`: the canonical operator allowlist.
- **How a new Portal asks.** It asks `PRPX0002` first. A runner that closes at the unknown magic predates pins, and Portal then asks `PRPX0001`. Any other failure is not "unsupported" (184 §14 item 8).
- **No upgrade order is needed.** The runner and Portal can be upgraded in either order.
- **Size.** `maxProxyProfileBytes` (`proxy_profile.go:26`, 1024) applies to `PRPY0001`. `PRPY0002` gets its own cap of 8,192 bytes. The largest valid profile is about 5.6 KB: 16 entries with 253-byte names and a full-length IPv6 address, plus the fixed part, so 8 KiB leaves a margin.

**Comparison: split the fixed limits from the pins.**
- **Fixed part.** It is still compared with the constant `transport.NewProxyProfile()`:
  - in `runnerServesProxy`;
  - in `observePlatformProxy` (`app_platform_proxy_review.go:18`);
  - in `parsePlatformProxyPlan` (`app_platform_proxy.go:78`).

  A plan's `runner` field stays exactly that 1-dev.1 struct, in both plan formats.
- **Pins.** A 1-dev.2 plan adds `runner_pins`: the operator entries its own pins use, one per pin, equal to them. It never holds the whole operator list and no digest of it.
  - **At plan and consume:** every pin must be in the runner's current `pins`. Otherwise the answer is 409, `app_platform_runner_unsupported`.
  - **At session open:** the coordinator checks the same thing. A pin the operator has since removed is reported on the console as not permitted by the runner. The session still carries the reviewed policy, and the broker refuses that pin as `address_denied` (section 5.5).
- **Result.** An operator edit to `proxy_pins` cannot invalidate a grant without pins, because nothing in its plan or session refers to the operator list. A pinned grant stays bound to the exact entries it was reviewed against.
- **Plan lookup across formats.** `platformProxyPlanPattern()` (`app_platform_proxy.go:96-98`) becomes `{"format":"portal.proxy-policy-review/1-dev.%`, which matches both plan formats. `parsePlatformProxyPlan` accepts either format exactly, so the exact parse still decides. `loadPlatformLatestProxyPlan` keeps ordering by `policy_revision DESC`, so a 1-dev.2 plan consumed after a 1-dev.1 plan is the one read.
- **Runner without pin support.** Plan and consume refuse a policy with pins (409) unless the runner answers `PRPY0002`.
- **Downgrades.** If a runner is downgraded after a pinned policy is consumed, the coordinator opens the session without the proxy, as 184 §14 item 8 already does for a runner that answers other limits.

### 5.5 Broker steps (184 §5.5, changed steps only)

1. Session and admission: unchanged.
2. **Host.**
   - If `Pin(host)` returns an entry, the tunnel is a **pinned tunnel**.
   - Otherwise `Match` decides as today.
   - The CONNECT port must still be 443 (`port_not_permitted`).
3. **Pin check instead of resolution.** For a pinned tunnel, the broker resolves nothing. It refuses the stream as `address_denied` if either holds:
   - the pinned entry `(host, address, port)` does not equal an entry of the runner's current `proxy_pins`;
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

A pinned address must be **positively** one of the following. Go's `IsGlobalUnicast` is not used, because it excludes loopback.
- **IPv4 unicast:** `Is4()`, not an IPv4-mapped form, and outside `224.0.0.0/4` and `240.0.0.0/4`. The second range covers `255.255.255.255`.
- `::1`.
- An address inside `fc00::/7`.
- An address inside `2000::/3`.

It must also lie **outside** every range below. The rule is checked:
- at runner start, for `proxy_pins`;
- at review;
- at session apply;
- at every dial.

The set has three parts:
- `appplatform.AlwaysDenied`, which already contains both metadata addresses, `100.100.100.200` and `fd20:ce::254`;
- with exactly loopback removed (Q2);
- with two IPv4 additions that `AlwaysDenied` does not cover because `network.http` reaches them through `publicAddress` instead.

It becomes a new function, `appplatform.PinDenied`.

| Family | Denied |
|---|---|
| IPv4 | `0.0.0.0/8`; `169.254.0.0/16` (link-local, including AWS, Azure and GCP metadata); `224.0.0.0/4`; `240.0.0.0/4`; `100.100.100.200/32` (Aliyun metadata); added: `192.0.0.0/24` (IETF protocol assignments, including Oracle metadata `192.0.0.192`) and `168.63.129.16/32` (Azure WireServer) |
| IPv6 | `::/128`; `fe80::/10`; `ff00::/8`; `::ffff:0:0/96` (IPv4-mapped, refused as written); `64:ff9b::/96` and `64:ff9b:1::/48` (NAT64); `2002::/16` (6to4); `2001::/32` (Teredo); `fd00:ec2::254/128` (AWS metadata); `fd20:ce::254/128` (GCP metadata) |

- **Loopback.** Of `AlwaysDenied`, only `127.0.0.0/8` and `::1/128` are released, and only for pins. `AlwaysDenied` itself, `network.http` and ordinary proxy hosts are unchanged.
- **Other non-public ranges.** RFC 1918, `100.64.0.0/10` (tailnet) and bridge addresses are not denied for pins, but each needs an explicit operator allowlist entry.

## 7. TLS

- **What the broker can check.** It cannot see inside TLS. It checks only the outer SNI, which must equal the pinned name (184 step 5). The certificate is verified by the app's TLS stack against the name it dialled, and that verification is the guarantee that the app reached the right site. `NO_PROXY` and the forwarder are unchanged, so `https://quotes.internal.anchan.kazuhahub.com` goes through the tunnel with that name as SNI.
- **Certificate.** The pinned listener presents a certificate valid for the name. On anchan that is the aaPanel site's Let's Encrypt certificate, issued by DNS-01, because the name need not be publicly reachable.
- **Dedicated listener (Q3).** nginx selects the TLS server by SNI, but then routes each request by its `Host` header. A pin to the shared `127.0.0.1:443` would let the app send `Host: <any other site on that nginx>`. The operator therefore pins a dedicated listener, for example `listen 127.0.0.1:20443 ssl;`, with one `server` block for this name, which returns 421 when `$host` is not the name, and a `default_server` block with `ssl_reject_handshake on;` for any other SNI. Port 20443 follows the host port plan (20000–20999 for loopback services behind a proxy).
- **What the listener must do.** The P4 acceptance probe (section 14) checks every item:
  - it is the only `listen` on port 20443, including `[::]:20443`;
  - it is bound to loopback only;
  - any other SNI hits the `default_server` block, whose `ssl_reject_handshake on;` refuses the handshake;
  - it returns 421 when `$host` is not the name, including for an absolute-URI request line (`GET https://other/ HTTP/1.1`), whose host nginx takes as `$host`;
  - it sends the upstream the fixed name (`proxy_set_header Host quotes.internal.anchan.kazuhahub.com`), never `$http_host`.
- **aaPanel rewrites its vhost files.** When the site is edited in the panel, aaPanel regenerates `/www/server/panel/vhost/nginx/<site>.conf`. The 20443 listener therefore lives in a separate include that aaPanel does not manage, and the probe is re-run after any panel edit of that site.

## 8. Limits

- **Unchanged from 184 §6.** A pinned tunnel counts against the same open-tunnel cap, new-tunnel bucket, daily byte budget, throughput cap and timers as any other.
- **New bounds.** At most 8 pins per installation (within the 64 entries). At most 16 operator entries. `PRPY0002` is at most 8,192 bytes.

## 9. Refusal codes

No new code; the wire table (`proxyforwarder/wire.go:39-55`) and the forwarder are unchanged.

| Case | Code |
|---|---|
| Pin not equal to an operator entry, or in the denied set | `address_denied` |
| Pinned dial failed or timed out | `connect_failed` |
| CONNECT port not 443 | `port_not_permitted` |
| SNI not the pinned name | closed after `200`, audited as `sni_mismatch` |

`resolve_failed` never occurs for a pinned tunnel.

## 10. Review, audit, reports and console

- **Review.** The plan of `POST .../installations/{id}/network-proxy/plan` gains `pins: [{host, address, port}]`.
  - The plan format becomes `portal.proxy-policy-review/1-dev.2`. Beside `hosts_added` and `hosts_removed` it adds:
    - `pins_added`, `pins_removed` and `pins_changed` (`{host, old, new}`);
    - `runner_pins` (section 5.4).

    `runner` keeps the fixed 1-dev.1 profile.
  - Consume, its compare-and-swap and the revision bump are those of 184 §14 item 12.
  - The session loader keeps accepting consumed 1-dev.1 plans. `platformProxyPlanPattern()` matches both formats (section 5.4).
  - A plan that removes every host and every pin withdraws the grant.
- **Audit of changes.** `app_platform.proxy_policy_changed` gains `pins_added`, `pins_removed` and `pins_changed` in its detail. The administrator, the plan ID and the policy digests are already recorded. Each pin is written as `host → address:port`.
- **Audit of traffic.** Rollup and refusal reports need no wire change (`EgressFrameProxyReport` is unchanged). When Portal writes `app_platform.proxy_rollup` and `app_platform.proxy_refused`, it adds `pinned: true` and `pin: "address:port"` to the detail of a host that is pinned in the policy of the session carrying the report.
- **Console.** `AppNetworkProxyPanel` lists pinned entries in the reviewed-hosts list, each with a "pinned to `address:port`" tag.
  - In the review dialog, an exact host can be marked as pinned. The address and port are chosen from the runner's advertised `proxy_pins` entries for that name (a select, not free text). A pin the operator has since removed is shown as not permitted by the runner. Each pin carries a warning line, as wildcards do.
  - The diff shows pins added, changed and removed.
  - The new keys go under `appPlatform.networkProxy.pin.*` in en, zh-CN and zh-TW, and the parity test covers them.

## 11. Schema change — needs owner approval

- **Portal database: none.** No migration and no new column or table. Pins live in the grant row's existing `policy_json` (J16384), in policy format `portal.proxy-policy/1-dev.2`. Plans live in `app_platform_review_plans.plan_json` in plan format `portal.proxy-policy-review/1-dev.2`, with the new fields `pins_added`, `pins_removed`, `pins_changed` and `runner_pins`.
- **JSON formats that change:**
  - policy `portal.proxy-policy/1-dev.2`, with `pins`;
  - plan `portal.proxy-policy-review/1-dev.2`;
  - runner profile `portal.runner.proxy-profile/1-dev.2`, `{format, fixed, pins}`, served only on the new query `PRPX0002`/`PRPY0002`. `PRPX0001` and 1-dev.1 are unchanged;
  - audit details, with the pin fields of section 10.

  The session command format is unchanged.
- **Runner configuration.** An optional `proxy_pins` block of `{host, address, port}` entries. The config format name is unchanged, as with `private_networks`.
- **Runner journal: none.** It stays at schema v5.

## 12. Open questions (recommended answer in each)

Q1. **Two keys (operator allowlist and administrator review) or the administrator alone?** Recommended: two keys. The operator entry is a full destination, `{host, address, port}`, and the Portal pin must equal it, so the operator approves a destination, not just a socket. 175 decision 3 keeps a Portal administrator alone from opening the LAN, and the host's loopback is more sensitive than the LAN. On anchan the operator and the administrator are the same person, so the cost is one line of runner configuration.

Q2. **Loopback is in `AlwaysDenied`.** The requirement says that whatever `AlwaysDenied` covers stays denied, but it covers `127.0.0.0/8` and `::1`, which the first entry needs. Recommended: release exactly those two prefixes, for pins only, through `PinDenied`. Every other `AlwaysDenied` range and both metadata addresses stay denied.

Q3. **Pin `127.0.0.1:443` (the shared nginx listener) or a dedicated listener?** Recommended: a dedicated `127.0.0.1:20443` listener that serves only the name and answers other `Host` values with 421. On the shared listener the app could reach Portal, Dify or any other vhost by changing `Host`, which the broker cannot see.

Q4. **May the pinned port differ from 443?** Recommended: yes, any port from 1 to 65535 that the operator lists. The app still CONNECTs to 443. Q3 needs this.

Q5. **Maximum number of pins per installation.** Recommended: 8, counted within the 64 entries. One is needed today.

Q6. **A name that is both pinned and covered by a wildcard.** Recommended: the pin wins for its exact name, with no DNS fallback. An exact ordinary entry with the same name as a pin is refused at review.

Q7. **A runner without pin support.** Recommended: the review refuses with 409. After a downgrade, the session opens without the proxy (the 184 behaviour), rather than with the proxy minus its pins.

Q8. **How a new runner stays compatible with an old Portal.** Options: a new query magic, answering 1-dev.1 only while `proxy_pins` is empty, or upgrading Portal first. Recommended: a new query magic (`PRPX0002`), with `PRPX0001` frozen at 1-dev.1. It needs no upgrade order. The "1-dev.1 while empty" option would break an old Portal as soon as the operator adds a pin.

Q9. **Can tailnet, RFC 1918 or bridge addresses be pinned?** Recommended: yes, but only when the operator lists the exact host, address and port. The fixed denied set covers metadata, link-local and the translation forms, not ordinary private space.

Q10. **New refusal codes.** Recommended: none. The cases map to `address_denied` and `connect_failed`, and the audit row marks the tunnel as pinned. The forwarder and the SDK stay unchanged.

Q11. **A descriptor field for pin suggestions.** Recommended: none. The app suggests the name in `network_proxy.hosts`, and the administrator turns it into a pin. No descriptor field ever carries an address.

Q12. **Pins when the runner runs under Compose (187).** Recommended: allowed only with `network_mode: host`, enforced by the network-namespace check of section 3. Otherwise the runner refuses to start with `proxy_pins` set.

## 13. Test plan (acceptance conditions; these must fail first)

Each condition states which way the guard fails.

**Policy and guard (`internal/appplatform`):**
- A policy without pins encodes byte-identically to 1-dev.1 and keeps its digest. Fails closed: any other encoding is refused by `ParseProxyPolicy`.
- A pin with a wildcard host, an IP-literal host, port 0, port 65536, a zoned address or an IPv4-mapped literal is refused. A duplicate pin name, or a pin name equal to an exact host, is refused. A 9th pin, or a 65th entry in total, is refused. Fails closed: refused at parse.
- `PinDenied` refuses every prefix in section 6 at its first and last address, and refuses `100.100.100.200`, `192.0.0.192`, `168.63.129.16`, `fd00:ec2::254` and `fd20:ce::254`. It also refuses IPv6 outside `::1`, `fc00::/7` and `2000::/3`. It admits `127.0.0.1`, `::1`, `172.17.0.1`, `10.0.0.5` and `100.64.0.1`. Fails closed: a denied address is never admitted.
- `AlwaysDenied` is unchanged: `127.0.0.1` is still denied. Fails closed.
- A pins-only policy encodes `"hosts":[]`. The same bytes with `"hosts":null` or without `hosts` are refused, `pins` in 1-dev.1 is refused, and a 1-dev.2 policy with empty `pins` is refused. Fails closed.

**Runner configuration:**
- `proxy_pins` with a denied address, a wildcard host, port 0, a duplicate or unsorted entry, or more than 16 entries stops the runner at start. Fails closed: the runner does not run.
- Non-empty `proxy_pins` with the dockerd peer in another network namespace, or with an unreadable `/proc/<pid>/ns/net`, stops the runner with `proxy_pins_netns`. Empty `proxy_pins` starts as today. Fails closed.
- A configuration file that is not `0600` or not owned by the effective user is refused, as today; the Compose recipe's mount is tested to pass. Fails closed.

**Runner profile:**
- `PRPX0001` answers the exact 1-dev.1 bytes with any `proxy_pins`, including the largest valid one. An old Portal's `ParseProxyProfile` plus its `NewProxyProfile()` equality accept it, so its `runnerServesProxy` stays true. This is the new-runner, old-Portal test. It fails open only for 1-dev.1 policies, which the old Portal can grant anyway.
- The largest valid `proxy_pins` (16 entries, 253-byte names, full IPv6) gives a `PRPY0002` under 8,192 bytes that a new Portal parses. One byte over the cap is refused as unsupported. Fails closed.
- A new Portal against an old runner: `PRPX0002` closes, the `PRPX0001` fallback answers, and unpinned grants keep working. Fails closed for pins only.

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
- A plan with pins against a runner that answers only `PRPY0001`, or one whose `pins` lacks the exact `{host, address, port}`, is 409 `app_platform_runner_unsupported`. Fails closed.
- An operator edit to `proxy_pins` (an entry added, removed or changed, and a runner restart) leaves every unpinned grant's plan, session and console state unchanged. A pinned grant whose entry was removed opens its session, and that pin is refused as `address_denied` and shown as not permitted. Fails closed for the pin only.
- A 1-dev.2 plan consumed after a 1-dev.1 plan for the same installation: `loadPlatformLatestProxyPlan` returns the 1-dev.2 plan (higher `policy_revision`), and a grant backed by the older plan alone is not granted. Fails closed.
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
4. **P4, anchan.** No code.
   1. Add the dedicated nginx listener for `quotes.internal.anchan.kazuhahub.com` on `127.0.0.1:20443`, in an include that aaPanel does not manage, with the Let's Encrypt certificate and the 421 rule.
   2. Run the acceptance probe below.
   3. Add `"proxy_pins": [{"host":"quotes.internal.anchan.kazuhahub.com","address":"127.0.0.1","port":20443}]` and restart the runner. The network-namespace check (section 3) replaces any manual confirmation of how the runner is deployed: if the runner is not in the host namespace, it refuses to start.
   4. Review the pin for market-monitor.

   **Acceptance probe**, run on the host. Each check fails the deploy if it does not hold:
   - `curl --resolve quotes.internal.anchan.kazuhahub.com:20443:127.0.0.1 https://quotes.internal.anchan.kazuhahub.com:20443/` succeeds with certificate verification on.
   - The same request with `-H 'Host: portal.anchan.kazuhahub.com'` answers 421.
   - `openssl s_client -connect 127.0.0.1:20443 -servername other.example` is rejected at the handshake and gets no certificate.
   - Inside TLS with the right SNI, the request line `GET https://portal.anchan.kazuhahub.com/ HTTP/1.1` with `Host: quotes.internal.anchan.kazuhahub.com` answers 421.
   - The site config sets `proxy_set_header Host quotes.internal.anchan.kazuhahub.com` (the fixed name, never `$http_host`). The frp upstream log shows that name for the curl request above.
   - `ss -ltnH 'sport = :20443'` lists exactly one socket, `127.0.0.1:20443`. There is no `[::]:20443` and no `0.0.0.0:20443`.
   - The probe is re-run after any aaPanel edit of the site, because aaPanel rewrites its vhost files.

**Apps side.** No SDK or forwarder change. market-monitor adds the name to `network_proxy.hosts` and calls `https://quotes.internal.anchan.kazuhahub.com`.

## 15. Implementation notes (2026-11-20)

Where this section differs from the text above, it wins. The branch changes no Portal database schema, no migration and no runner journal schema (still v5): pins live in the grant row's `policy_json` and in the plan's `plan_json`, as section 11 says.

### Decisions and deviations

1. **Every new plan is 1-dev.2, whether or not it has pins.** Section 10 says the plan format "becomes" 1-dev.2; the code makes that literal. A review of a hosts-only policy writes a 1-dev.2 plan with empty pin members, and the grant row's policy stays 1-dev.1 (section 5.3: a policy without pins keeps its encoding). `parsePlatformProxyPlan` reads both formats exactly, each by its own closed wire struct, so a consumed 1-dev.1 plan keeps backing its grant. The constant `platformProxyReviewFormat` still names 1-dev.1.
2. **The plan's `pins_changed` entries are `{host, old, new}` with `old` and `new` as full pin objects.** The audit row writes them differently, as section 10 asks: `pins_added` and `pins_removed` are lists of `"host → address:port"`, and each `pins_changed` entry is `{host, old, new}` with `old` and `new` as `"address:port"`. `RunnerPins` in a plan is checked to be exactly the policy's own pins, and the diff lists are checked to be sorted, disjoint and to name only pins of that policy.
3. **PRPX0001 is not touched, and Portal no longer uses it in production.** `QueryControllerProxy` and `handleProxyProfile` stay as the frozen 1-dev.1 pair (tests pin the bytes with the largest valid allowlist). Portal asks `QueryControllerProxyState`: PRPX0002 first, and PRPX0001 only when PRPX0002 is closed or reset at the unknown magic. A truncated or failed PRPX0002 answer is a failure to ask, with no fallback (so a granted proxy is never dropped on a transient failure). A PRPX0002 answer that is malformed or over its 8,192-byte cap is `ErrProxyUnsupported`, also with no fallback: a runner that speaks the query and says something invalid is not trusted for the unpinned grants either.
3a. **Seams.** `platformObserveProxy` and the coordinator's `QueryProxy` now return `transport.ProxyRunnerState` (`Profile`, `PinsSupported`, `Pins`). The existing tests were adapted to it.
4. **The namespace check is the daemon-peer identification that already existed, made mandatory.** `driver.CheckHostNetworkNamespace` asks the daemon one read-only question (`GET /version`) through a client that requires the host namespace, and `service.CheckProxyPinsNamespace` turns a failure into `proxy_pins_netns` before the runtime is opened. "Every Docker connection it already vets" is implemented for the runtime's own client (`CandidateRuntime`, through `driver.WithHostNetworkNamespace`): a connection whose daemon holder is in another network namespace, or that cannot be identified as a local dockerd at all, makes the request fail with `ErrHostNetworkNamespace`, and the refusal is sticky until restart. The stricter reading (an unidentifiable daemon also refuses, not only a different namespace) follows from "an unreadable `/proc/<pid>/ns/net` fails closed". The separate image-load and inspection clients are not vetted this way: they carry no pin.
5. **"Checked at session apply" is the parse.** The runner's session verification parses the carried policy, and `ParseProxyPolicy` runs every pin through `ValidProxyPin`, which applies `PinDenied`. The runner does not compare the session's pins with its allowlist when it applies a session, on purpose: section 5.4 says a pin the operator has removed still opens the session and the broker refuses it. The allowlist is compared at every dial (`ProxyRunner.PinPermitted`), together with `PinDenied` again.
6. **`PinDenied` is a positive rule first.** An address is admitted only as IPv4 unicast, `::1`, `fc00::/7` or `2000::/3`; then loopback is released from `AlwaysDenied` and everything else of `AlwaysDenied` plus `192.0.0.0/24` and `168.63.129.16/32` is denied. `AlwaysDenied` itself is unchanged and has its own regression test.
7. **A pin is dialled as `ProxyPin.Endpoint()`**, which brackets an IPv6 address (`[::1]:20443`). The pin check comes after the host and port checks, so CONNECT to a pinned name on port 80 or 20443 is `port_not_permitted` before the allowlist is consulted. `Lookup`, the interface list and `publicAddress` are not touched for a pinned tunnel. Nothing about a pinned tunnel changes in the runner's reports; Portal annotates (item 10).
8. **Operator allowlist.** The configuration block accepts more than one entry for one name (sorted by host, then address, then port), so the console select can offer several destinations for a name. The advertised list is exactly the configured one.
9. **Review of the pin set.** A request's pins must be valid and sorted by host after Portal sorts them; a pin name that is also an exact host, more than 8 pins, or more than 64 entries together is an input error before the runner is asked. The runner is asked only for a plan that is not a withdrawal; at plan and at consume, every pin of the plan must be an exact entry of the runner's current list, and a runner without pin support or without the entry is `409 app_platform_runner_unsupported`. A consume of a plan without pins is not held to the runner's pins, so an operator edit never invalidates an unpinned review.
10. **Reports.** The session state keeps the policy it carried; rollup and refusal audit rows of a host that is pinned in it carry `pinned: true` and `pin: "address:port"`. The usage row is unchanged.
11. **Console API.** The plan request's `pins` member is optional, so a client that predates pins keeps working; the other members are still required and the body is still closed JSON, with the port bounded to 1 to 65535 before it is narrowed. The view gains `policy.pins[]` (each with `permitted`, true only when the runner now answers PRPY0002 with the exact entry) and `runner` (`available`, `pins_supported`, `pins`). The view asks the runner with a 4 s timeout and does not fail when it cannot be asked: `available` is false, and every reviewed pin then shows as not permitted.
12. **Console UI.** Pinned names stay in the one list of approved hosts. For each exact host in that list for which the runner lists a destination (or which already carries a choice), the panel shows a select limited to the runner's entries for that name; choosing one moves the name out of `hosts` and into `pins` when the review is requested. A reviewed pin the operator has removed stays selectable and is marked not permitted. The dialog shows pins added, changed and removed, with a warning line for every destination that is added or changed. The new keys are `appPlatform.networkProxy.pin.*` in en-US, zh-CN and zh-TW.
13. **P4 materials (nothing is run against a server).** `docs/app-platform/07-handbook.md` section 4.5 holds the operator procedure. `deploy/app-runner/proxy-pin-listener.nginx.example.conf` is the dedicated listener and `deploy/app-runner/service.proxy-pins.example.json` is the plain example configuration plus exactly the `proxy_pins` block (a test keeps the two from drifting, and the plain example stays pin-free). `scripts/probe-proxy-pin-listener.sh` is the acceptance probe of section 14 (wrong `Host` gives 421, another SNI is rejected with no certificate, an absolute-URI request line for another site gives 421, the include fixes the upstream `Host`, the upstream log shows the name, and exactly one loopback socket listens on 20443). Beyond the listed checks it also reads the include: a `listen` on the port that is not the loopback address, a missing `ssl_reject_handshake on` and a missing `return 421` fail it. `--nginx-include` is required, and the upstream-log check needs either `--upstream-log` or an explicit `--no-upstream-log`, which is reported as skipped. `scripts/proxy_pin_probe_test.sh` runs it against shims of curl, openssl and ss (every check fails closed) and against the shipped nginx example, and runs in the CI step of the other shell tests; both scripts are shellcheck-clean. Which aaPanel include point survives a panel edit is for the operator to confirm: the probe is the proof.
14. **Native acceptance.** `TestProxyEndpointAcceptance` gained the pinned case (a network-less child reaches the loopback TLS fixture through the pinned name, and the parent asserts the name was never looked up). It needs root on a GitHub-hosted runner and has not run. The same path is covered without root by `TestPinnedPolicyInASessionReachesThePinnedAddress`, which drives a signed session through the registry to the broker.

### Not done

- P4 itself: the nginx listener, the runner configuration and the Portal review for market-monitor are operator work on anchan.
- The native acceptance case has not run (item 14).
- The Apps repository is untouched. `scripts/generate_app_contract.py --check` passes for Portal; with `--apps-dir` it reports drift in the Apps checkout, in files this change does not influence (no descriptor, schema or SDK field changed: no descriptor field ever carries an address, Q11).
