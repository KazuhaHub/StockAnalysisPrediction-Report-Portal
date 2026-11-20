# Proposal: PostgreSQL liveness probe as a restricted stream capability

Status: accepted 2026-11-17; contract decisions C1-C5 adopted 2026-11-20; implemented
(section 12). Depends on
[174](174-background-broker-egress.md), [175](175-reviewed-multi-origin-and-private-network.md)
and [176](176-background-credential-use.md). Index and order: doc 174.

## 1. Problem and evidence

- Doc [01](01-architecture.md) §4 (lines 92-95) reserves a "restricted stream
  mode" for databases: fixed destination and port, byte/time/concurrency
  limits, validated DNS, "not a generic CONNECT proxy", with a fixture protocol
  and refusal tests defined first. It does not exist.
- The only stream capability, `network.stream`, is HTTPS response streaming
  with a closed media-type set (doc [158](158-reviewed-network-stream.md);
  `internal/appplatform/http_stream_policy.go:16-23`).
- The backend has no network (`internal/apprunner/driver/container.go:384-388`).
- Portal already depends on `github.com/jackc/pgx/v5` (`go.mod:15`).

Status checks PostgreSQL by connecting with `sslmode=verify-full`, a guarded
dial to the configured host and port, the monitor's username and password, and
`SELECT 1`, returning only reachable/failed
(Apps `apps/status/service/internal/status/probes.go:361-379`, `:381-406`,
`:418-463`). It also supports a Unix-socket transport under
`/run/status-postgres` (`postgres_socket.go:16-34`, `probes.go:363-369`).

## 2. Decision in brief

The broker terminates the PostgreSQL protocol itself and performs one fixed
liveness exchange. The app never receives a byte stream, never sends SQL and
never sees the password. This is the restricted stream mode of doc 01 reduced
to the only operation the reviewed use needs.

Rejected: a TCP relay with protocol filtering. To keep the password from the
app, the relay would have to intercept SCRAM authentication and then parse and
filter every subsequent frame to hold queries to a liveness check; that is a
SQL firewall, not a bounded capability, and it fails open on parser gaps.

## 3. Threat model

| Threat | Control |
| --- | --- |
| Arbitrary SQL or data exfiltration | No query text crosses the API; the statement is a compiled-in constant |
| Lateral movement to other ports or hosts | Reviewed targets only (host, port, database); doc 175 address classes and private allowlist |
| Password disclosure | Credential bound by name; value only in the signed runner command, as for Bearer values |
| TLS downgrade or MITM | TLS required, full certificate and host-name verification; no `prefer`/`allow` modes |
| Connection exhaustion of the target | Per-installation concurrency ≤ 2, the doc 174 rate budget, one connection per check, closed after the check |
| Slow-loris from the server | Connect, TLS, auth and query share one deadline ≤ 10 s; bounded close |
| Server messages leaking data to the app | Only an outcome class and latency are returned |

## 4. Contract

### 4.1 Capability and descriptor

New capability `network.postgres` (descriptor contract revision: added to the
closed capability enum, `internal/appplatform/contract/descriptor-v2.schema.json:397-409`,
`:425-437`, and `descriptor_contract_generated.go:142`). It requires a backend,
like `network.http`, and is optional by default.

### 4.2 Policy `portal.postgres-probe-policy/1-dev.1`

```json
{"format":"portal.postgres-probe-policy/1-dev.1",
 "targets":[{"id":"orders-primary","host":"db.example.com","port":5432,"database":"orders","network":"public"}],
 "deadline_ms":10000,"concurrency":2}
```

1..64 targets sorted by `id`; `host` follows doc 175 rules for its `network`
class (DNS name for `public`; DNS name or IP literal inside the deployment
allowlist for `private`); `port` 1..65535 and, for private targets, listed in
the allowlist entry; `database` 1..63 bytes of `[A-Za-z0-9_-]`;
`deadline_ms` 1..10,000; `concurrency` 1..2. Canonical, ≤ 16,384 bytes,
stored in the `network.postgres` grant row. Changes use the doc 175 capability
policy review.

### 4.3 Credential binding

New binding operation `pg_password`. Credential value:

```json
{"username":"status_probe","password":"..."}
```

`username` 1..63 bytes, `password` 1..1,024 bytes. Binding policy v2 (doc 176)
with `network_target_sha256` = SHA-256 of the canonical Postgres target object.

### 4.4 Broker request

```text
POST /v1/postgres/liveness
{"requestId":"<uuid>","target":"orders-primary","credential":"orders-probe"}
```

Reply:

```json
{"outcome":"reachable","latencyMs":42}
```

`outcome` is one of `reachable`, `connect_failed`, `tls_failed`,
`auth_failed`, `query_failed`, `timeout`. Errors use the doc 174 codes
(`denied`, `unavailable`, `busy`, `invalid`). There is no `uncertain`: the
check has no external effect.

Portal signs a `portal.postgres-liveness-command/1-dev.1` carrying the same
authority tuple as `NetworkHTTPRequestCommand`
(`internal/apprunner/control/network_http.go:24-49`), the canonical target,
policy digest, and the username/password. The runner:

1. resolves the host, applies the target's address class rules to every
   address, pins the first;
2. connects with TLS required (`ServerName` = host; system roots), minimum TLS
   1.2, no fallback;
3. sends a startup message with only `user`, `database` and
   `application_name=portal-app-probe`;
4. authenticates (SCRAM-SHA-256 or password-over-TLS as the server requests;
   `trust` is accepted and reported as `reachable`);
5. executes the constant `SELECT 1` and requires exactly one row with the
   integer 1;
6. sends `Terminate` and closes; re-checks online authority before replying.

Bytes read from the server are bounded to 64 KiB per check.

## 5. Unix-socket targets: out of scope

Status's `/run/status-postgres` transport is not supported. A socket target
would require the runner to mount or open a host filesystem path for an app,
which doc 01 §4 forbids ("no arbitrary host paths"), and there is no reviewed
way to bind a host socket path to an installation. Status v2 reports such
monitors as `unsupported`; operators move them to TLS TCP.

## 6. Review and consent

Install or capability policy review lists each target as
`postgres://host:port/database` with its network class, and states that only a
connect-and-`SELECT 1` check is possible. The binding review shows the username
(not the password) and the target. Administrators are told to use a role with
no table privileges.

## 7. Failure and audit

Outcome classes above; `denied` for an unknown target, wrong binding principal
or target digest mismatch, before any dial. The doc 174 summary counts checks
per target and outcome. No server error text is logged or returned.

## 8. Migration and compatibility

Additive capability and operation. The runner network profile advertises
`postgres_probe_format`; Portal refuses the capability against a runner
without it. Status replaces `postgresCheck` with the broker call and drops the
`pgx` dependency from its backend.

## 9. Test plan (these must fail first)

1. Descriptor parsing refuses `network.postgres` today; it must accept it only
   with a backend.
2. Policy parser: unsorted targets, IP literal in a public target, port outside
   the allowlist entry, oversize database name: refused.
3. A fixture PostgreSQL server with a test CA: `reachable`; wrong password:
   `auth_failed`; self-signed or wrong-name certificate: `tls_failed`; server
   that offers no TLS: `tls_failed`; server that stalls after startup:
   `timeout` within the deadline.
4. The fixture records every statement it receives: exactly one `SELECT 1`
   per check, and only the three startup parameters.
5. A third concurrent check with `concurrency=2`: `busy`.
6. Password absent from reply, logs, journal and audit.
7. Revoking `network.postgres`, the binding or `background.run` mid-check: the
   check ends and returns `denied`/`unavailable`.
8. SQLite and PostgreSQL control stores; native run with the fixture server.

## 10. Schema change (approved by the owner on 2026-11-17)

Approved by the owner on 2026-11-17, together with the schema changes in docs
177, 178 and 180, as one migration, `0022`.

Yes: widen the `operation` CHECK of `app_platform_secret_bindings` with
`pg_password`. Same mechanism, constraint names and caveats as doc
[177](177-provider-request-signing.md) §8 (PostgreSQL `_ck_15`, SQLite
`_ck_29`); shipped in migration `0022`, the same step as `acs3_hmac_sha256` and
`ingress_hmac_sha256`.

PostgreSQL (shown alone; the combined step lists every approved value):

```sql
ALTER TABLE app_platform_secret_bindings DROP CONSTRAINT app_platform_secret_bindings_ck_15;
ALTER TABLE app_platform_secret_bindings ADD CONSTRAINT app_platform_secret_bindings_ck_15
  CHECK ((operation='http_bearer') OR (operation='http_api_key') OR (operation='raw_read')
      OR (operation='pg_password'));
```

SQLite:

```sql
PRAGMA writable_schema=ON;
UPDATE sqlite_master
   SET sql=replace(sql,
     'CHECK ((operation=''http_bearer'') OR (operation=''http_api_key'') OR (operation=''raw_read''))',
     'CHECK ((operation=''http_bearer'') OR (operation=''http_api_key'') OR (operation=''raw_read'') OR (operation=''pg_password''))')
 WHERE type='table' AND name='app_platform_secret_bindings'
   AND instr(sql,'CHECK ((operation=''http_bearer'') OR (operation=''http_api_key'') OR (operation=''raw_read''))')>0;
PRAGMA writable_schema=OFF;
PRAGMA schema_version=<cookie+1>;
PRAGMA integrity_check(app_platform_secret_bindings);
PRAGMA foreign_key_check(app_platform_secret_bindings);
```

The grant and its policy need no change: the `network.postgres` row fits
`app_platform_capability_grants` (`capability` T96, `policy_json` J16384,
`internal/app/app_platform_schema.go:45`).

## 11. Owner decisions (2026-11-17)

The owner decided every question below on 2026-11-17.

1. Private CAs. Decided: allow an optional reviewed CA certificate (PEM
   <= 4 KiB) per target in the policy, pinned by the review; never a "skip
   verification" switch.
2. Return the server version. Decided: no; it is fingerprinting data with no
   liveness value.
3. Other databases (MySQL, Redis). Decided: separate capabilities with their
   own fixed exchanges, only on demand.

## 12. Implementation notes (2026-11-20)

The owner adopted contract decisions C1-C5 on 2026-11-20, on top of the 2026-11-17 decisions
(a reviewed private CA per target, never skip-verify; no server version returned; Unix sockets out
of scope). No schema change beyond migration 0022, which already carries `pg_password`. The
sections above are the proposal; where they differ from what shipped, this section wins.

### 12.1 Decisions

- C1. `network.postgres` is a separate optional capability: a descriptor may declare it only as
  optional and only with a backend, install writes its row `granted=0` with policy `{}`, and the
  policy is `portal.postgres-probe-policy/1-dev.1`. The target-id pattern, the host and address-class
  rules (one shared `validNetworkTargetHost`, extracted from the HTTP target origin check) and the
  runner private-network allowlist with its `PrivateNetworksSHA256` binding are doc 175's. The
  address guard (`hostSnapshot`, `onHost`, `addressesAllowed`) is extracted from the HTTP executor
  and shared with the probe, which applies it with scheme `https` (PostgreSQL always runs over TLS).
- C2. The grant is its own administrator two-call review,
  `POST /api/admin/app-platform/installations/{id}/postgres-policy/plan` and `.../consume`
  (and `GET` for the settings page). The consume updates the existing grant row in place under a
  compare-and-swap on `(granted, policy_json)` and writes `app_platform.postgres_policy_changed`; it
  does not move `policy_revision`. Withdrawing is a plan with no targets. The capability match
  exceptions admit a granted `network.postgres` row whose policy parses (`ValidPostgresPolicy`),
  as they do for `public.projection`. Private targets are checked against the runner's PRNT0001
  allowlist at plan time (an IP literal must lie in an entry listing its port, a DNS name needs an
  entry listing its port) and again at consume time against the digest the plan carries.
- C3. A target may carry `ca_pem` (at most 4096 bytes, exactly one `CERTIFICATE` block, an x509 CA,
  in its one canonical PEM spelling). When present it is the only root pool the probe uses; without
  it the system roots apply. There is no skip-verify field and unknown keys are refused. The
  certificate is part of the canonical target object, so its digest changes with it.
- C4. `pg_password` bindings are always the target-scoped `portal.secret-binding-policy/3-dev.1`
  with `network_target_sha256` the SHA-256 of the canonical PostgreSQL target object, for the
  service only. Whole-policy and viewer spellings are refused. A binding is valid while its target
  digest is still in the granted policy; changing a target's host, port, database, class or CA
  leaves it on a digest the policy no longer has.
- C5. The runner answers a new capability query, `PRPQ0001` / `PRPR0001`, with
  `portal.runner.postgres-probe/1-dev.1` and the members `format`, `command_format`,
  `policy_format`. Portal adds `network.postgres` to an egress session only when the runner
  answered it: an older runner would refuse the whole session for an unknown resource.

### 12.2 Where the proposal and the code differed

- Stale citations. The file and line references of sections 1 and 4.1 predate the contract
  generator; the capability enum is generated from `contract/descriptor-v2.schema.json` by
  `scripts/generate_app_contract.py`, and the Status references are Apps-side.
- v3, not v2. Section 4.3 says binding policy v2. v2 is the whole-policy spelling with principals;
  a binding that must follow one target needs v3 (doc 175), so that is what `pg_password` uses.
- The CA field. Section 11.1 decided an optional reviewed CA per target; it is the `ca_pem` member
  above, canonicalized by Portal at review time.
- The policy carries the allowlist digest. A granted row has no review plan of its own to hold
  `private_networks_sha256`, so a policy with a private target carries it as an optional member
  (present exactly when a target is private), written from the runner's answer at plan time. A
  command carries the same digest, and the runner serves a private target only against that digest.
- md5 is refused. Section 4.4 step 4 says "password-over-TLS as the server requests". The probe sets
  `RequireAuth` to `scram-sha-256,password,none`, so a server that offers md5 ends the check as
  `auth_failed` before any password is sent; cleartext is only ever sent inside the verified TLS
  session, and `trust` is accepted as reachable.
- A capability query, not a profile member. Section 8 says the runner network profile advertises
  `postgres_probe_format`. That profile is compared byte for byte with the plan Portal stored, so a
  new member would break every installation's foreground calls; the separate query is the same
  mechanism as `PRNT0001` and `PAEGRV01`.
- The command. Its name is `portal.runner.postgres-probe-request/1-dev.1` (signing purpose
  `portal.app/runner-postgres-probe/v1`), not `portal.postgres-liveness-command/1-dev.1`. It carries
  the authority tuple of the HTTP command without a review plan id (the grant is not a plan), the
  policy bytes and digest, `target_id`, the binding name and the role. Lifetime is at most 30 s and
  at least the policy deadline. Its checks are `portal.runner.postgres-probe-check/1-dev.1` under
  their own signing purpose, so a proof for an HTTP call never answers a probe challenge.
- The wire. The app calls `POST /v1/postgres/liveness {requestId, target, credential}` on a
  background egress session and gets `{outcome, latencyMs}`; the errors are only `invalid`, `denied`,
  `unavailable` and `busy` (never `uncertain`). Runner to Portal the call starts with admit frame
  `0x0A` (`{target, credential}`), is answered by the signed command, and ends with an outcome frame
  whose status 1-6 is the outcome in the order of section 4.4.
- pgconn takes the password as a string. The library cannot be given a byte slice, so a copy of the
  username and password lives in its config until the garbage collector reclaims it. Everything
  Portal and the runner own is wiped (the opened value, the signed bytes, the frame copies, the
  command's slices), the credential value is printable ASCII, and the probe never logs or returns a
  library error: a failure is classified by phase (dial, TLS including a server that answers the SSL
  request with `N`, authentication including class 28 and a `RequireAuth` refusal, the statement,
  the deadline) and the error is dropped.
- The probe. One connection through a pinned dial that ignores the library's address, a lookup
  function that returns only the pinned address, a deadline-only context watcher (no cancel-request
  connection), every config field set explicitly, protocol 3.0, reads capped at 64 KiB, the constant
  statement `SELECT 1` as one simple query that must return exactly one row and one column with the
  text `1`, a bounded Terminate, and three authority checks (before resolving, before dialing, before
  releasing the outcome). It takes a slot of the pool's background share and at most two per
  installation (`busy` otherwise).
- Sessions. A session picks the grant up when it is next renewed (at most five minutes). A withdrawn
  or changed grant ends a session that carries the resource as drift, because the session pin holds
  the policy digest, and a probe call re-reads the grant, the binding and the credential at its admit
  and at every check.
- Audit. The egress summary gains `postgres: [{target, outcomes}]` for targets that resolved and
  `postgres_unresolved` (outcome counts only) for names the application chose that did not.

### 12.3 Console and contract

The settings page has a "PostgreSQL liveness" review (en-US, zh-CN, zh-TW) listing each target as
`postgres://host:port/db` with its class, the allowlist entry for a private one and the certificate
authority fingerprint, and saying "connect + SELECT 1 only; use a role with no table privileges". The
credentials page has a username and password credential type and the "PostgreSQL liveness" binding
operation. The descriptor contract (`descriptor-v2.schema.json`, its generated Go and TypeScript
products) lists `network.postgres` in the optional capability domain only.

Apps-side sync needed: regenerate the Apps SDK products with
`python3 scripts/generate_app_contract.py --apps-dir <Apps checkout>` (the schema, the generated
`descriptor-contract.generated.ts` and the generator itself), so a descriptor may declare
`network.postgres`; then Status can replace `postgresCheck` with the broker call
(`POST /v1/postgres/liveness`) and drop its `pgx` dependency, as section 8 intends.
